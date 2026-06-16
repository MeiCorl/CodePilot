// summary_compactor.go 实现第二层「重量兜底」摘要压缩器。
//
// 第一层（light_compactor.go）管「单条消息内工具结果体积」，但当【整体历史】逼近
// 上下文窗口上限时，第一层已无能为力——需要把较早的整段历史摘要化。本文件实现这一层：
//
//   - splitByTailTokens：把历史切成「待摘要的早期段」+「保留的近期原文段」。近期原文
//     保留量取「约 1 万 token」与「至少 5 条消息」的较大者（详见函数注释）。
//   - summarize：调 LLM 对早期段生成结构化摘要。关键约束——【禁止调用任何工具】
//     （toolSpecs=nil 强制禁用），并要求模型先写 <draft> 分析草稿、再写正式摘要，
//     草稿由 stripDraft 剥离丢弃、不进入最终上下文（草稿是模型「思考过程」，占用 token
//     但对后续对话无价值）。
//   - Compact：编排「切分 → 摘要 → 归档 → 重写」全流程，返回压缩后的新活跃历史。
//
// 【编排顺序的刻意选择（满足 checklist「摘要失败不修改 history」）】
// 顺序为：切分 → 摘要 →（成功后）归档早期原文 → 构造新历史 → 重写 messages.jsonl。
// 即【先摘要、成功后才动 archive 与 jsonl】。这样摘要失败时 archive/jsonl/history 均
// 未被改动（归档与重写都在摘要成功之后）。若按「先归档再摘要」，摘要失败时 archive 已
// 写入、违反「失败不修改」语义。
//
// 【持久化与内存的分工】
// 本压缩器只负责生成【新活跃历史切片】并把它落盘（经 HistoryArchiver 接口）。把新历史
// 【应用到内存 ConversationManager】由调用方（协调器 Task 5 + manager ReplaceHistory Task 7）
// 完成——故 Compact 返回 newHistory，不直接操作 manager，保持本层与引擎层解耦。
//
// 【失败语义】
// 任一步失败返回 err 且不修改内存 history（Compact 把原 history 原样返回 + changed=false）。
// 摘要失败由协调器计入熔断计数；归档/重写失败属于持久化异常，同样上抛由调用方决策。

package context

import (
	stdctx "context" // 别名：本包名也是 context，需规避与标准库 context 的命名冲突
	"fmt"
	"strings"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/llm"
	"go.uber.org/zap"
)

// HistoryArchiver 抽象「历史原文归档 + 活跃历史重写」两类持久化能力。
//
// 定义在 context 包（而非导入 session 包）是为了让 SummaryCompactor 依赖【抽象】：
// session.SessionManager 天然实现这两个方法（鸭子类型自动满足接口），从而避免
// context → session 的硬依赖，保持记忆层内 context 包与 session 包松耦合（与
// ToolResultStore 持 projectDir 字符串而非 SessionManager 引用同一解耦思路）。
type HistoryArchiver interface {
	// ArchiveMessages 把被压缩掉的早期原文追加写入归档文件（append-only）。
	ArchiveMessages(sessionID string, msgs []llm.Message) error
	// RewriteActiveMessages 把压缩后的活跃历史全量覆盖写到 messages.jsonl。
	RewriteActiveMessages(sessionID string, msgs []llm.Message) error
}

// summarySystemPrompt 是摘要阶段的 System Prompt（固定文案）。
//
// 刻意约束：
//  1. 【禁止调用任何工具】——即便 toolSpecs=nil 已在协议层禁用，Prompt 内再强调一遍
//     作双保险，防止模型在摘要里「虚构工具调用」。
//  2. 【先写 <draft> 草稿再写正式摘要】——草稿是模型的推理过程，占用 token 但对后续
//     对话无价值，stripDraft 会剥离丢弃，只保留正式摘要进入上下文。
//  3. 【5 段固定结构】——让摘要可被程序与模型按段定位（目标/进展/决策/待办/关键文件）。
//  4. 【保留关键信息】——文件路径、函数名、约定、报错等必须原样保留，避免摘要后丢失
//     后续工作所需的具体锚点（这是「摘要不能脑补」的前提）。
const summarySystemPrompt = `你是会话摘要助手。请把给定的历史对话压缩为一份结构化摘要，供后续对话作为上下文。

【硬性约束】
- 禁止调用任何工具，只输出文本。
- 先在 <draft>...</draft> 标签内写分析草稿（梳理对话脉络、标记关键信息），再在草稿之后写正式摘要。草稿仅用于你的思考，会被程序丢弃，不会进入最终上下文。
- 正式摘要必须客观、准确，不得脑补或推测对话中未出现的内容。

【正式摘要结构】（草稿之后，按以下 5 个部分组织，缺失的部分标注「无」）
1. 用户目标与意图：用户想完成什么。
2. 已完成的工作：已经做了哪些事、改了哪些文件。
3. 关键决策与结论：确定的技术方案、约定、取舍。
4. 尚未解决的问题 / 待办：遗留的 bug、未完成的任务、待确认的点。
5. 关键文件路径：涉及的所有文件路径、函数名、报错信息（原样保留，不要改写）。

【信息保留要求】
- 所有文件路径、函数名、变量名、命令、报错文本必须原样保留。
- 摘要应足够具体，使后续工作能据此继续，而无需重新读取全部历史。`

// summaryPrefix 是摘要消息的可识别前缀标记，便于程序与模型识别「这一条是会话摘要」。
const summaryPrefix = "[会话摘要]"

// boundaryPrompt 是压缩后追加的边界提示，提醒模型：上文是摘要（非原文），需要文件细节时
// 应重新读取，不要依据摘要脑补代码——避免模型在摘要基础上「凭印象」写出不存在的代码。
const boundaryPrompt = "以上为历史摘要，并非逐字原文。若后续需要文件细节或准确代码，请用 ReadFile 重新读取，不要依据摘要脑补代码。"

// SummaryCompactor 是第二层「重量兜底」摘要压缩器。
//
// 无可变状态——配置只读，持久化经无状态 HistoryArchiver 接口承担。故本类型线程安全
// （真正的并发隔离由调用方按 sessionID 串行化保证，协调器 Task 5 负责）。
type SummaryCompactor struct {
	// archiver 提供原文归档与活跃历史重写能力（通常由 session.SessionManager 实现）。
	archiver HistoryArchiver
	// cfg 为压缩配置（取 KeepRecentTokens 与 KeepRecentMinMessages 两个字段）。
	cfg config.CompactionConfig
}

// NewSummaryCompactor 创建第二层摘要压缩器。
//
// archiver 应为已就绪的 HistoryArchiver 实现（主流程装配时注入 *session.SessionManager）；
// cfg 应已过 applyCompactionDefaults 填充默认值。触发判定与熔断由协调器（Task 5）负责，
// 本压缩器假定被调用时已满足触发条件。
func NewSummaryCompactor(archiver HistoryArchiver, cfg config.CompactionConfig) *SummaryCompactor {
	return &SummaryCompactor{archiver: archiver, cfg: cfg}
}

// Compact 编排第二层摘要压缩，返回压缩后的新活跃历史。
//
// 流程（顺序见文件头注释——先摘要成功后才动 archive/jsonl）：
//  1. splitByTailTokens 切分早期段(toSummarize) 与近期段(keep)；toSummarize 为空则不压缩。
//  2. summarize 调 LLM 生成摘要；失败直接返回 err（archive/jsonl/history 未动）。
//  3. ArchiveMessages 归档早期原文（成功后才写，保证失败不污染 archive）。
//  4. 构造新历史 = [摘要消息, 边界消息] + keep，并 RewriteActiveMessages 落盘。
//
// 返回值：
//   - newHistory：压缩后的活跃历史切片（changed=true 时为新历史，false 时为原 history）。
//   - changed：是否实际发生压缩。
//   - err：任一步失败返回错误（此时 newHistory 为原 history、changed=false）。
//
// 边界消息紧跟摘要消息、位于 keep 之前：边界是对「摘要」的元说明，应紧邻摘要；keep
// （真实近期对话）放在尾部，让模型自然延续。摘要与边界均为 user 角色——Anthropic /
// OpenAI 协议均容忍相邻 user 消息。
func (sc *SummaryCompactor) Compact(
	ctx stdctx.Context,
	provider llm.Provider,
	history []llm.Message,
	sessionID string,
) (newHistory []llm.Message, changed bool, err error) {
	toSummarize, keep := splitByTailTokens(history, sc.cfg.KeepRecentTokens, sc.cfg.KeepRecentMinMessages)
	if len(toSummarize) == 0 {
		// 历史太短无可摘要内容（splitByTailTokens 在 n<=1 时返回 nil），不压缩。
		return history, false, nil
	}

	// 1. 生成摘要（失败则 archive/jsonl/history 全未动）。
	summary, err := sc.summarize(ctx, provider, toSummarize)
	if err != nil {
		return history, false, fmt.Errorf("生成摘要失败: %w", err)
	}
	if strings.TrimSpace(summary) == "" {
		// 空摘要无意义，视为失败交由协调器熔断计数。
		return history, false, fmt.Errorf("生成的摘要为空")
	}

	// 2. 归档早期原文（摘要成功后才写）。
	if err := sc.archiver.ArchiveMessages(sessionID, toSummarize); err != nil {
		return history, false, fmt.Errorf("归档早期原文失败: %w", err)
	}

	// 3. 构造新活跃历史：[摘要消息, 边界消息] + 近期原文。
	summaryMsg := llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{
		llm.NewTextBlock(summaryPrefix + "\n\n" + summary),
	}}
	boundaryMsg := llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{
		llm.NewTextBlock(boundaryPrompt),
	}}
	newHistory = make([]llm.Message, 0, len(keep)+2)
	newHistory = append(newHistory, summaryMsg, boundaryMsg)
	newHistory = append(newHistory, keep...)

	// 4. 重写 messages.jsonl 为新活跃历史，使持久化与内存一致。
	if err := sc.archiver.RewriteActiveMessages(sessionID, newHistory); err != nil {
		return history, false, fmt.Errorf("重写活跃历史失败: %w", err)
	}

	logger.InfoCtx(ctx, "已将历史压缩为摘要",
		zap.Int("summarizedCount", len(toSummarize)),
		zap.Int("keepCount", len(keep)),
		zap.Int("summaryTokens", EstimateTextTokens(summary)),
	)

	return newHistory, true, nil
}

// summarize 调 LLM 对 toSummarize 生成结构化摘要，剥离 draft 段后返回正式摘要。
//
// 关键：toolSpecs=nil 强制禁用工具（协议层保证模型无法 tool_use）；System Prompt 内再
// 强调禁工具（双保险）。流式消费参考 ConversationManager.runOneLLM 的范式，但简化——
// 摘要场景无需 hooks/abort 排空/tool_use 收集，只需累加文本 + 处理 ctx 取消与错误。
func (sc *SummaryCompactor) summarize(ctx stdctx.Context, provider llm.Provider, toSummarize []llm.Message) (string, error) {
	sp := llm.NewSystemPromptFromText(summarySystemPrompt)
	chunkCh, err := provider.StreamChat(ctx, sp, toSummarize, nil) // nil = 禁用所有工具
	if err != nil {
		return "", fmt.Errorf("发起摘要请求失败: %w", err)
	}

	var buf strings.Builder
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case chunk, ok := <-chunkCh:
			if !ok {
				// channel 关闭：流正常结束（部分 Provider 直接 close 不发 Done）。
				return stripDraft(buf.String()), nil
			}
			if chunk.Err != nil {
				return "", chunk.Err
			}
			if chunk.Content != "" {
				buf.WriteString(chunk.Content)
			}
			if chunk.Done {
				return stripDraft(buf.String()), nil
			}
		}
	}
}

// splitByTailTokens 把历史切成「待摘要的早期段 toSummarize」与「保留的近期段 keep」。
//
// 保留窗口（keep）取「token 维度」与「条数维度」的较大者（保留更多）：
//   - token 维度 splitTok：从尾部往回累加 token，累计首次 >= keepTokens 时的索引
//     （即 keep 段 token ≈ keepTokens）。若所有消息累加完仍 < keepTokens（历史总 token
//     不足），splitTok=0。
//   - 条数维度 splitMin = n - minKeep：保证 keep 至少 minKeep 条。
//   - split = min(splitTok, splitMin)：split 越小保留越多，取两者较小者 = 较大保留者。
//
// 边界处理：
//   - n == 0：返回 (nil, nil)。
//   - n <= 1：无法切出有意义的 toSummarize，返回 (nil, history)（调用方据此跳过）。
//   - 保证 toSummarize 至少 1 条（split >= 1）；极端情况（minKeep=0 等）防御性保证 keep
//     至少 1 条（split <= n-1）。
//
// 切分点对齐到消息边界（绝不拆单条消息）。toSummarize 非空是摘要有意义的前提。
func splitByTailTokens(history []llm.Message, keepTokens, minKeep int) (toSummarize, keep []llm.Message) {
	n := len(history)
	if n == 0 {
		return nil, nil
	}
	if n <= 1 {
		return nil, history
	}

	// 1. token 维度切分点：从尾部累加，首次 >= keepTokens 的索引。
	acc := 0
	splitTok := 0
	for i := n - 1; i >= 0; i-- {
		acc += EstimateMessageTokens(history[i])
		if acc >= keepTokens {
			splitTok = i
			break
		}
	}
	// 累加完所有消息仍 < keepTokens 时，splitTok 保持 0（历史总 token 不足保留预算）。

	// 2. 条数维度上界：保证 keep 至少 minKeep 条。
	splitMin := n - minKeep
	if splitMin < 0 {
		splitMin = 0
	}

	// 3. 取较大保留者 = 较小 split。
	split := splitTok
	if splitMin < split {
		split = splitMin
	}

	// 4. 保证 toSummarize 至少 1 条；防御性保证 keep 至少 1 条。
	if split < 1 {
		split = 1
	}
	if split > n-1 {
		split = n - 1
	}
	split = alignSplitForToolPairs(history, split)
	if split < 1 {
		return nil, history
	}

	return history[:split], history[split:]
}

// alignSplitForToolPairs keeps Anthropic tool_use/tool_result protocol pairs on
// the same side of the summary boundary. A raw tool_result cannot remain in the
// active tail unless its matching assistant tool_use is also visible before it.
func alignSplitForToolPairs(history []llm.Message, split int) int {
	for split > 0 && split < len(history) &&
		startsWithToolResult(history[split]) &&
		assistantHasToolUseFor(history[split-1], history[split]) {
		split--
	}
	return split
}

func startsWithToolResult(msg llm.Message) bool {
	if msg.Role != llm.RoleUser {
		return false
	}
	for _, block := range msg.Content {
		if _, ok := block.(*llm.ToolResultBlock); ok {
			return true
		}
	}
	return false
}

func assistantHasToolUseFor(assistant, result llm.Message) bool {
	if assistant.Role != llm.RoleAssistant {
		return false
	}
	toolUseIDs := make(map[string]struct{})
	for _, block := range assistant.Content {
		if tu, ok := block.(*llm.ToolUseBlock); ok {
			toolUseIDs[tu.ID] = struct{}{}
		}
	}
	if len(toolUseIDs) == 0 {
		return false
	}
	for _, block := range result.Content {
		if tr, ok := block.(*llm.ToolResultBlock); ok {
			if _, ok := toolUseIDs[tr.ToolUseID]; ok {
				return true
			}
		}
	}
	return false
}

// stripDraft 剥离文本中的 <draft>...</draft> 段（含可能存在的多个），返回剩余的正式摘要。
//
// 模型按要求先写草稿再写摘要，草稿是推理过程、不应进入最终上下文。处理规则：
//   - 完整的 <draft>...</draft> 段被整段移除；
//   - 若存在未闭合的 <draft>（无对应 </draft>），停止剥离并保留剩余文本（不误删，交由
//     上层判定——通常意味着模型未遵循格式，但宁保留不误删）；
//   - 无 draft 标签时原样返回；
//   - 结果去除首尾空白。
func stripDraft(s string) string {
	const openTag = "<draft>"
	const closeTag = "</draft>"
	for {
		start := strings.Index(s, openTag)
		if start == -1 {
			break
		}
		// 在 start 之后查找闭合标签
		closeIdx := strings.Index(s[start:], closeTag)
		if closeIdx == -1 {
			break // 未闭合，停止剥离避免误删
		}
		end := start + closeIdx + len(closeTag)
		s = s[:start] + s[end:]
	}
	return strings.TrimSpace(s)
}
