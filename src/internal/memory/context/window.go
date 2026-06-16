// Package context 实现上下文管理策略。
//
// ⚠️ 本文件内的 SlidingWindow【当前未被主流程使用】。
// 上下文体积控制自 Step 7 起改由同包下的两层压缩（light_compactor.go 工具结果
// 预览化 + summary_compactor.go 整体历史摘要）承担；engine/conversation.ConversationManager
// 的 GetContext() 直接返回完整活跃 history，不再经过本文件的窗口裁剪（见
// manager.go:200 GetContextKeepsFullHistory 注释与对应回归测试）。
//
// 本文件保留的意义：
//  1. 兼容历史：Step 1~6 期间的滑动窗口行为可作为压缩关闭时的回退参考；
//  2. 单测守护：window_test.go 持续校验窗口算法本身（按轮裁剪 + SystemPrompt 保护 + 不修改 history）；
//  3. 未来复用：SubAgent 等场景可能需要"按轮数"截断作为辅助策略。
//
// 因此【请勿删除】本文件，但新增上下文管理能力时请优先考虑两层压缩与 token 估算，
// 而不是恢复窗口方式。
//
// ---- 历史说明（保留以便理解原始设计） ----
// 滑动窗口策略：从完整对话历史中派生出"最近 N 轮"的视图，供 LLM 调用使用。
// 滑动窗口本身不持有消息，完整历史由上层（对话管理器 / 会话）作为唯一真相源持有，
// 确保窗口裁剪只影响"发送给 LLM 的视图"，而不会破坏需要持久化的完整归档。
package context

import "github.com/MeiCorl/CodePilot/src/llm"

// SlidingWindow 是无状态的滑动窗口上下文策略。
// 它不存储消息，而是基于外部传入的完整历史，派生出保留最近 maxRounds 轮
// （一轮 = 一对 User+Assistant）的窗口视图，System Prompt 始终固定在最前。
type SlidingWindow struct {
	// maxRounds 为最大保留的对话轮数（一轮 = 一对 User+Assistant）
	maxRounds int
}

// NewSlidingWindow 创建一个滑动窗口策略。
// maxRounds 指定最大保留的对话轮数，<=0 时默认为 10。
func NewSlidingWindow(maxRounds int) *SlidingWindow {
	if maxRounds <= 0 {
		maxRounds = 10
	}
	return &SlidingWindow{maxRounds: maxRounds}
}

// View 基于完整对话历史 history 派生出窗口视图：
// 保留最近 maxRounds 轮对话，并在最前固定 System Prompt（systemPrompt 非空时）。
// 该方法不修改入参 history，返回新的消息切片。
func (w *SlidingWindow) View(history []llm.Message, systemPrompt string) []llm.Message {
	windowed := w.windowed(history)

	result := make([]llm.Message, 0, len(windowed)+1)
	// System Prompt 固定保留在最前，不受窗口裁剪影响
	if systemPrompt != "" {
		result = append(result, llm.Message{
			Role:    llm.RoleSystem,
			Content: []llm.ContentBlock{llm.NewTextBlock(systemPrompt)},
		})
	}
	result = append(result, windowed...)
	return result
}

// MaxRounds 返回窗口保留的最大对话轮数。
func (w *SlidingWindow) MaxRounds() int {
	return w.maxRounds
}

// windowed 从完整历史中提取最近 maxRounds 轮消息，确保裁剪后的消息序列
// 以 User 消息开头且保持 User/Assistant 交替结构。
//
// 在 Agent Loop 中，消息序列可能是：
//
//	User(text) → Assistant(text+tool_use) → User(tool_result) → Assistant(text) → ...
//
// 因此"一轮"被定义为：以 User 消息开头到下一个 User 消息之前的所有连续消息。
// 例如上面序列中，第 1-2 条为第 1 轮，第 3-4 条为第 2 轮。
//
// 裁剪策略：先定位所有轮次边界，然后保留最后 maxRounds 轮，确保结构完整性。
func (w *SlidingWindow) windowed(history []llm.Message) []llm.Message {
	if len(history) == 0 {
		return history
	}

	// 1. 定位所有 User 消息的索引（轮次边界）
	roundStarts := make([]int, 0, len(history))
	for i, msg := range history {
		if msg.Role == llm.RoleUser {
			roundStarts = append(roundStarts, i)
		}
	}

	// 如果轮数不超过 maxRounds，不需要裁剪
	totalRounds := len(roundStarts)
	if totalRounds <= w.maxRounds {
		return history
	}

	// 2. 计算需要保留的起始轮次索引
	keepFromRoundIdx := totalRounds - w.maxRounds
	cutPos := roundStarts[keepFromRoundIdx]

	// 3. 从 cutPos 开始截取，保证第一条消息是 User 角色
	return history[cutPos:]
}

// countRounds 统计消息列表中的对话轮数。
// 一轮以第一条 User 消息开始，到下一条 User 消息之前结束。
// 这是兼容旧接口的辅助方法，新代码应优先使用 windowed 的轮次定位逻辑。
func countRounds(messages []llm.Message) int {
	count := 0
	for _, msg := range messages {
		if msg.Role == llm.RoleUser {
			count++
		}
	}
	return count
}
