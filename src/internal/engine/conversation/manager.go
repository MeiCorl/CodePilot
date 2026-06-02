// Package conversation 实现对话历史管理，负责消息的构造、添加和上下文获取。
// 它持有完整对话历史作为唯一真相源，并组合 memory/context 包的滑动窗口策略，
// 派生出发送给 LLM 的上下文视图，为上层提供简洁的对话管理接口。
package conversation

import (
	"unicode/utf8"

	"github.com/MeiCorl/CodePilot/src/internal/memory/context"
	"github.com/MeiCorl/CodePilot/src/llm"
)

// ConversationManager 管理多轮对话的消息历史。
// 它持有完整对话历史（history，唯一真相源），并通过 SlidingWindow 派生出
// 发送给 LLM 的窗口视图。完整历史可通过 AllMessages 获取用于持久化归档，
// 不受窗口裁剪影响，从而避免持久化时丢失超窗的早期消息。
type ConversationManager struct {
	// window 为滑动窗口策略，基于完整历史派生 LLM 上下文视图（无状态，不持有消息）
	window *context.SlidingWindow
	// history 为完整对话历史，作为唯一真相源；持久化与窗口派生均以此为基础
	history []llm.Message
}

// NewConversationManager 创建一个对话管理器。
// maxRounds 为滑动窗口最大保留的对话轮数。
func NewConversationManager(maxRounds int) *ConversationManager {
	return &ConversationManager{
		window:  context.NewSlidingWindow(maxRounds),
		history: make([]llm.Message, 0),
	}
}

// AddUserMessage 添加一条用户消息到完整对话历史。
// content 为用户输入的文本，内部构造为 Message{Role: RoleUser, Content: []ContentBlock{TextBlock}}。
func (m *ConversationManager) AddUserMessage(content string) {
	m.history = append(m.history, llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{llm.NewTextBlock(content)},
	})
}

// AddAssistantMessage 添加一条助手消息到完整对话历史。
// content 为助手回复的文本，内部构造为 Message{Role: RoleAssistant, Content: []ContentBlock{TextBlock}}。
func (m *ConversationManager) AddAssistantMessage(content string) {
	m.history = append(m.history, llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentBlock{llm.NewTextBlock(content)},
	})
}

// Reset 用给定消息替换完整对话历史。
// 用于恢复历史会话时把磁盘加载的消息注入到管理器；调用后
// 后续 AddXxx / GetContext / AllMessages 均以新历史为基础。
// 传入 nil 等价于清空历史。
func (m *ConversationManager) Reset(messages []llm.Message) {
	m.history = make([]llm.Message, len(messages))
	copy(m.history, messages)
}

// GetContext 返回发送给 LLM 的上下文窗口视图。
// systemPrompt 作为第一条 System 消息固定在最前，其余为滑动窗口派生的最近 N 轮对话。
// 注意：返回结果是经过窗口裁剪的视图，不一定是完整历史；持久化请使用 AllMessages。
func (m *ConversationManager) GetContext(systemPrompt string) []llm.Message {
	return m.window.View(m.history, systemPrompt)
}

// AllMessages 返回完整对话历史的副本，用于会话持久化归档。
// 与 GetContext 不同，该结果不受滑动窗口裁剪影响，包含所有历史消息，
// 是持久化时应当使用的唯一真相源。
func (m *ConversationManager) AllMessages() []llm.Message {
	out := make([]llm.Message, len(m.history))
	copy(out, m.history)
	return out
}

// TokenEstimate 估算当前发送给 LLM 的窗口视图已使用的 token 数。
// 采用粗估策略：中文按 2 字符/token，英文按 4 字符/token，
// 不需要精确，仅用于状态栏展示和窗口控制参考。
// 注意：此处基于窗口视图（而非完整历史）估算，反映的是实际发送给 LLM 的上下文量。
func (m *ConversationManager) TokenEstimate() int {
	messages := m.GetContext("")
	totalTokens := 0
	for _, msg := range messages {
		for _, block := range msg.Content {
			totalTokens += estimateTextTokens(block.ToText())
		}
	}
	return totalTokens
}

// RemainingTokens 返回在给定的最大 token 额度下，剩余可用的 token 数。
// 如果已超出额度，返回 0。
func (m *ConversationManager) RemainingTokens(maxTokens int) int {
	used := m.TokenEstimate()
	remaining := maxTokens - used
	if remaining < 0 {
		return 0
	}
	return remaining
}

// MessageCount 返回完整对话历史中的消息数量。
func (m *ConversationManager) MessageCount() int {
	return len(m.history)
}

// estimateTextTokens 对一段文本进行粗略 token 估算。
// CJK 字符按 2 字符/token 估算，ASCII/非 CJK 字符按 4 字符/token 估算。
func estimateTextTokens(text string) int {
	cjkCount := 0
	nonCJKCount := 0
	for _, r := range text {
		if isCJK(r) {
			cjkCount++
		} else {
			nonCJKCount++
		}
	}
	// CJK: 约 2 字符 = 1 token
	// 非 CJK: 约 4 字符 = 1 token
	cjkTokens := cjkCount / 2
	nonCJKTokens := nonCJKCount / 4
	if cjkCount > 0 && cjkTokens == 0 {
		cjkTokens = 1
	}
	if nonCJKCount > 0 && nonCJKTokens == 0 {
		nonCJKTokens = 1
	}
	return cjkTokens + nonCJKTokens
}

// isCJK 判断一个 rune 是否为 CJK（中日韩）字符。
func isCJK(r rune) bool {
	// CJK Unified Ideographs
	if r >= 0x4E00 && r <= 0x9FFF {
		return true
	}
	// CJK Unified Ideographs Extension A
	if r >= 0x3400 && r <= 0x4DBF {
		return true
	}
	// CJK Compatibility Ideographs
	if r >= 0xF900 && r <= 0xFAFF {
		return true
	}
	// Hiragana & Katakana
	if r >= 0x3040 && r <= 0x30FF {
		return true
	}
	// Fullwidth Forms
	if r >= 0xFF00 && r <= 0xFFEF {
		return true
	}
	// CJK punctuation and symbols
	if r >= 0x3000 && r <= 0x303F {
		return true
	}
	return false
}

// 确保编译时引用 utf8 包（用于 rune 相关操作）
var _ = utf8.RuneLen
