// Package context 实现上下文管理策略。
// 当前提供滑动窗口策略：从完整对话历史中派生出"最近 N 轮"的视图，供 LLM 调用使用。
// 滑动窗口本身不持有消息，完整历史由上层（对话管理器 / 会话）作为唯一真相源持有，
// 确保窗口裁剪只影响"发送给 LLM 的视图"，而不会破坏需要持久化的完整归档。
// 后续 Step 7 将实现高级上下文管理（摘要压缩、缓存策略等）。
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

// windowed 从完整历史尾部截取最近 maxRounds 轮消息。
// 当历史轮数超出 maxRounds 时，按 FIFO 顺序丢弃最早的消息对（一轮 User+Assistant）。
// 返回的是 history 的子切片（共享底层数组），调用方不应修改返回结果。
func (w *SlidingWindow) windowed(history []llm.Message) []llm.Message {
	msgs := history
	for countRounds(msgs) > w.maxRounds {
		// 移除最早的一对消息（User + Assistant）
		if len(msgs) >= 2 {
			msgs = msgs[2:]
		} else {
			break
		}
	}
	return msgs
}

// countRounds 统计消息列表中的对话轮数。
// 一轮对话由一条 User 消息和紧跟的一条 Assistant 消息组成。
func countRounds(messages []llm.Message) int {
	count := 0
	i := 0
	for i < len(messages) {
		if messages[i].Role == llm.RoleUser {
			// 检查下一条是否为 Assistant 消息，构成完整一轮
			if i+1 < len(messages) && messages[i+1].Role == llm.RoleAssistant {
				count++
				i += 2
				continue
			}
		}
		i++
	}
	return count
}
