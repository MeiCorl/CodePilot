// light_compactor.go 实现第一层「轻量预防」压缩器。
//
// 第一层管「单条消息内工具结果的体积」——与第二层（整体历史摘要）互补：
//   - 单个工具结果超过阈值（默认 8K token）时，把完整结果落盘，内存中替换为头部预览；
//   - 单条消息内多个工具结果合计超过阈值时，按体积从大到小依次替换，直到合计降到阈值以下。
//
// 【为什么这样设计能保证 prompt cache 命中率】
// 替换是对内存中 *ToolResultBlock 的 in-place 修改。是否替换完全由「token 是否超阈值」
// 这一确定性规则决定——阈值不变则每轮重跑结果一致：一旦某 block 超阈值被替换为预览，
// 后续每轮重跑它仍超阈值（原文不变）→ 仍被替换为同一份预览（预览生成确定性）→ 内存
// Content 逐字一致 → 发给 LLM 的 prompt 前缀稳定，cache 命中。反之不超阈值的 block
// 永远保留原文。因此【无需额外维护替换状态记录】，靠规则确定性 + 存盘幂等即可。
//
// 【持久化时序（重要）】
// 工具结果原文在产生当轮已被 handler 追加到 messages.jsonl（append-only，原文）。
// 本压缩器发生在【下一轮 API 请求前】，只改内存 history 的 Content 字段，【从不回写 jsonl】
// ——本文件不依赖 session 包任何持久化方法（编译期保证）。因此：
//   - jsonl 始终保留工具结果原文（可追溯、可恢复）；
//   - 会话恢复后重新加载原文 → 重跑一次轻量预防 → 存盘幂等跳过、内存再次变为预览态，自洽。
//
// 【失败语义】
// 存盘子系统单点失败（IO 错误）时，该 block 降级为保留原文、记 warn 日志，不中断整轮
// 压缩，changed 只反映实际发生的替换。err 正常流程恒为 nil（失败已被降级吞掉），
// 保留返回值是为了与协调器（Task 5）签名对齐并预留整体性错误的扩展位。

package context

import (
	"context"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/llm"
	"go.uber.org/zap"
)

// LightCompactor 是第一层「轻量预防」压缩器。
//
// 无可变状态成员——阈值与预览预算来自配置（只读），存盘由无状态 ToolResultStore 承担。
// 故 LightCompactor 本身线程安全，可被多个会话的压缩编排并发调用（真正的并发隔离
// 由文件系统层的 O_EXCL 与按 sessionID/toolUseID 分文件保证）。
type LightCompactor struct {
	// store 为工具结果存盘子系统，负责落盘与路径管理。
	store *ToolResultStore
	// cfg 为压缩配置（取 ToolResultThreshold 与 PreviewTokens 两个字段）。
	cfg config.CompactionConfig
}

// NewLightCompactor 创建第一层压缩器。
//
// store 为已注入会话根目录的 ToolResultStore；cfg 应已过 applyCompactionDefaults
// 填充默认值（main.go 装配时保证），故本构造不再做默认值兜底。总开关（Enabled）的
// 判定由协调器负责，本压缩器假定被调用时压缩是开启的。
func NewLightCompactor(store *ToolResultStore, cfg config.CompactionConfig) *LightCompactor {
	return &LightCompactor{store: store, cfg: cfg}
}

// Compact 对一批消息执行第一层轻量预防压缩，in-place 修改超阈值工具结果为预览。
//
// 遍历每条消息，对消息内的 *ToolResultBlock 应用「单超 + 合计超」两步规则（见
// compactMessage）。返回 changed 表示本轮【是否实际发生过至少一次替换】——未超阈值
// 或全部已是预览态时返回 false（无噪音）。
//
// ctx 用于会话级日志路由（logger.WarnCtx/InfoCtx 据此把压缩日志写入对应会话目录）；
// 本层无 LLM 调用，ctx 仅服务于日志。sessionID 决定存盘子目录归属（跨会话隔离）。err 正常恒为 nil。
func (lc *LightCompactor) Compact(ctx context.Context, messages []llm.Message, sessionID string) (changed bool, err error) {
	threshold := lc.cfg.ToolResultThreshold
	previewTokens := lc.cfg.PreviewTokens
	for i := range messages {
		if lc.compactMessage(ctx, &messages[i], sessionID, threshold, previewTokens) {
			changed = true
		}
	}
	return changed, nil
}

// compactMessage 处理单条消息内的所有 ToolResultBlock，返回该消息是否发生替换。
//
// 两步编排（与 spec 对齐）：
//  1. 单超：单个 block token > threshold 者，立即存盘 + 替换为预览（这些大 block 必处理）。
//  2. 合计超：单超处理完后，若该消息所有 tool_result 合计 token 仍 > threshold，按【当前
//     token 降序】依次存盘 + 替换，每替换一个重新计合计，直到合计 ≤ threshold 或无更多
//     可替换 block。被预览替换后的 block 按预览长度重新计入合计。
//
// 跳过条件：已是预览态（isPreview）的 block 不进入候选，避免重复 IO。
// 防死循环：替换失败的 block（存盘失败 / 原文≤预览预算导致无需替换）被记入 failed 集合，
// 后续轮次排除，保证循环必然推进终止。
func (lc *LightCompactor) compactMessage(ctx context.Context, msg *llm.Message, sessionID string, threshold, previewTokens int) bool {
	// 收集候选 block 索引：ToolResultBlock 且当前非预览态。
	var candidates []int
	for i := range msg.Content {
		if tr, ok := msg.Content[i].(*llm.ToolResultBlock); ok && !isPreview(tr.Content) {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return false
	}

	changed := false

	// 第一步：单个 block 超 threshold → 存盘 + 替换。
	for _, i := range candidates {
		tr := msg.Content[i].(*llm.ToolResultBlock)
		if EstimateTextTokens(tr.Content) > threshold {
			if lc.replaceBlock(ctx, tr, sessionID, previewTokens) {
				changed = true
			}
		}
	}

	// 第二步：合计超 threshold → 按当前 token 降序依次替换。
	// failed 记录替换失败的候选索引（排除出后续轮次），防死循环。
	failed := make(map[int]bool)
	for {
		total := 0
		var remaining []int
		for _, i := range candidates {
			tr := msg.Content[i].(*llm.ToolResultBlock)
			total += EstimateTextTokens(tr.Content) // 已替换者按预览长度计入
			if !failed[i] && !isPreview(tr.Content) {
				remaining = append(remaining, i)
			}
		}
		if total <= threshold || len(remaining) == 0 {
			break // 已达标或无可替换者，终止
		}
		// 在剩余可替换者中找【当前 token 最大】的 block（体积从大到小）。
		pick := remaining[0]
		pickTokens := EstimateTextTokens(msg.Content[pick].(*llm.ToolResultBlock).Content)
		for _, i := range remaining[1:] {
			tk := EstimateTextTokens(msg.Content[i].(*llm.ToolResultBlock).Content)
			if tk > pickTokens {
				pick = i
				pickTokens = tk
			}
		}
		tr := msg.Content[pick].(*llm.ToolResultBlock)
		if lc.replaceBlock(ctx, tr, sessionID, previewTokens) {
			changed = true
		} else {
			// 替换未成功（存盘失败或原文≤预览预算）：标记排除，避免下轮重复选中导致死循环。
			failed[pick] = true
		}
	}

	return changed
}

// replaceBlock 把单个 *ToolResultBlock 的原文存盘并 in-place 替换为预览。
//
// 流程：
//  1. 调 ToolResultStore.Save 落盘（幂等：已存在则 skipped，不重复写）。
//  2. 存盘失败 → 记 warn、保留原文、返回 false（降级，不中断调用方）。
//  3. 成功 → BuildPreview 生成预览；若预览 == 原文（原文本就≤预览预算，无需预览化），
//     返回 false；否则 in-place 改 tr.Content 为预览、记 Info 日志、返回 true。
//
// tr 必须是 messages 历史中真实持有的 *ToolResultBlock 指针（由 compactMessage 经类型
// 断言取得），in-place 修改才会反映到上游 history。
func (lc *LightCompactor) replaceBlock(ctx context.Context, tr *llm.ToolResultBlock, sessionID string, previewTokens int) bool {
	original := tr.Content
	originalTokens := EstimateTextTokens(original)

	fp, _, err := lc.store.Save(sessionID, tr.ToolUseID, original)
	if err != nil {
		// 存盘失败：降级保留原文，仅记 warn，不向上抛错（保证整轮压缩不被单点 IO 失败中断）。
		logger.WarnCtx(ctx, "工具结果存盘失败，降级保留原文",
			zap.String("toolUseID", tr.ToolUseID),
			zap.Int("originalTokens", originalTokens),
			zap.Error(err),
		)
		return false
	}

	preview := BuildPreview(original, fp, previewTokens)
	if preview == original {
		// 原文短于预览预算，预览化无收益——不变更（理论上超阈值的 block 不会走到这里，
		// 但合计替换场景下可能选中中等大小 block，此处防御性返回 false）。
		return false
	}

	tr.Content = preview
	logger.InfoCtx(ctx, "工具结果已存盘并替换为预览",
		zap.String("toolUseID", tr.ToolUseID),
		zap.Int("originalTokens", originalTokens),
		zap.String("filePath", fp),
		zap.Int("previewTokens", EstimateTextTokens(preview)),
	)
	return true
}
