// Package conversation 实现对话历史管理，负责消息的构造、添加和上下文获取。
// 它持有完整对话历史作为唯一真相源，并组合 memory/context 包的滑动窗口策略，
// 派生出发送给 LLM 的上下文视图，为上层提供简洁的对话管理接口。
//
// Step 2 在此基础上扩展 RunTurn 入口：把 LLM 流式响应与工具执行串联成
// "单轮闭环"——LLM 返回 tool_use → 调度工具 → tool_result 回传 →
// 二次 LLM 拿到最终回复，并把过程中产生的所有消息写回 history。
// RunTurn 假设由调用方（web handler）在已串行化的上下文中调用，
// 内部直接读写 history，不加锁（与现有 AddXxx 方法保持一致的并发契约）。
package conversation

import (
	"context"
	"unicode/utf8"

	memctx "github.com/MeiCorl/CodePilot/src/internal/memory/context"
	"github.com/MeiCorl/CodePilot/src/llm"
	"github.com/MeiCorl/CodePilot/src/tool"
)

// ConversationManager 管理多轮对话的消息历史。
// 它持有完整对话历史（history，唯一真相源），并通过 SlidingWindow 派生出
// 发送给 LLM 的窗口视图。完整历史可通过 AllMessages 获取用于持久化归档，
// 不受窗口裁剪影响，从而避免持久化时丢失超窗的早期消息。
type ConversationManager struct {
	// window 为滑动窗口策略，基于完整历史派生 LLM 上下文视图（无状态，不持有消息）
	window *memctx.SlidingWindow
	// history 为完整对话历史，作为唯一真相源；持久化与窗口派生均以此为基础
	history []llm.Message
}

// NewConversationManager 创建一个对话管理器。
// maxRounds 为滑动窗口最大保留的对话轮数。
func NewConversationManager(maxRounds int) *ConversationManager {
	return &ConversationManager{
		window:  memctx.NewSlidingWindow(maxRounds),
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

// AddMessage 添加一条任意角色/内容的消息到完整对话历史。
// 用于 RunTurn 内部插入 tool_use、tool_result 消息以及最终的
// assistant 文本消息；调用方需保证 msg 的 Role/Content 合法性。
func (m *ConversationManager) AddMessage(msg llm.Message) {
	m.history = append(m.history, msg)
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

// ---- Step 2: 单轮 LLM + 工具执行闭环 ----

// TurnHooks 把 RunTurn 执行过程中的流式事件外推给上层。
//
// 所有回调均为可选（nil 表示不关心该事件）；RunTurn 在收到对应事件
// 时同步调用，**不要在回调里做阻塞或耗时的操作**（如同步 I/O、长计算），
// 否则会拖慢 LLM 流式吞吐。
//
// OnStreamChunk 在每次收到 StreamChunk 时被调用一次（含 Done=true 的
// 最后一个 chunk）。OnToolUse 在第一次 LLM 决定调用工具时被调用一次。
// OnToolResult 在工具执行完成（成功/失败/超时/取消）后被调用一次。
// OnError 在 RunTurn 内部出现不可恢复错误时被调用一次（如 StreamChat
// 初始化失败、StreamChunk 携带 Err）；调用后 RunTurn 立即返回。
type TurnHooks struct {
	// OnStreamChunk 推送流式 chunk 文本与结束信号（流结束时 Done=true）
	OnStreamChunk func(llm.StreamChunk)
	// OnToolUse 在 LLM 决定调用工具时回调（不区分成功失败，仅通知）
	OnToolUse func(llm.ToolUseBlock)
	// OnToolResult 在工具执行结束后回调，含执行结果/错误
	OnToolResult func(llm.ToolResultBlock)
	// OnError 在 RunTurn 内部出现错误时回调
	OnError func(error)
}

// TurnResult 是 RunTurn 的返回值，描述本轮对话的最终结果。
//
// 字段语义：
//   - FinalText: 二次 LLM 调用（基于 tool_result 生成的最终回复）累积的完整文本；
//     当本轮没有触发 tool_use 时，等价于第一次 LLM 的回复文本
//   - ToolUse: 本轮触发的 tool_use 块（如有），未触发时为 nil
//   - ToolResult: 与 ToolUse 对应的工具执行结果（如有），未触发时为零值
//   - Aborted: 本轮是否被 ctx 取消
//   - Error: 本轮发生的不可恢复错误（StreamChat 失败、chunk.Err 等）
type TurnResult struct {
	// FinalText 为本轮用户可见的最终回复文本（流式累积）
	FinalText string
	// ToolUse 为触发的 tool_use 块（如有）
	ToolUse *llm.ToolUseBlock
	// ToolResult 为工具执行结果（如有）
	ToolResult llm.ToolResultBlock
	// Aborted 标识本轮是否被 ctx 取消中断
	Aborted bool
	// Error 为本轮发生的不可恢复错误（如有）
	Error error
}

// RunTurn 执行一轮 LLM 对话 +（可选的）单次工具执行。
//
// 调用流程：
//  1. 调 provider.StreamChat 发起第一次 LLM 请求；
//  2. 消费 StreamChunk 流，累积 assistant 文本与 ContentBlock 列表，
//     通过 OnStreamChunk 外推每个 chunk；
//  3. 流结束后检查 ToolUse 字段：
//     - 若 nil：本轮无 tool_use，把累积的 assistant 文本写入 history 并返回
//     - 若非 nil：把 assistant tool_use 消息写入 history → 调
//       toolHandler.Execute 获取 tool_result → 把 tool_result 消息写入
//       history → 通过 OnToolResult 外推 → 发起第二次 LLM 请求（同一 ctx）
//       → 消费流式并把最终回复文本写入 history → 返回
//
// 错误处理：StreamChat 初始化失败、StreamChunk.Err 通过 OnError 回调外推
// 并立即返回；RunTurn 不会向上层 panic。任何写入 history 的内容都已记录
// 完整消息结构，便于后续持久化与历史回放。
//
// 并发约束：RunTurn 直接读写 history，调用方需保证同一时刻只有一个
// RunTurn 活跃（实际由 web/handler 的 streamState 状态机串行化）。
func (m *ConversationManager) RunTurn(
	ctx context.Context,
	provider llm.Provider,
	systemPrompt string,
	toolSpecs []tool.ToolSpec,
	toolHandler *ToolHandler,
	hooks TurnHooks,
) TurnResult {
	// 第一次 LLM 调用
	var firstTurn RunOneTurnResult
	firstTurn = m.runOneLLM(ctx, provider, systemPrompt, toolSpecs, hooks)
	if firstTurn.Err != nil {
		return TurnResult{Aborted: firstTurn.Aborted, Error: firstTurn.Err}
	}

	// 无 tool_use → 写 assistant 文本到 history 并返回
	if firstTurn.ToolUse == nil {
		if firstTurn.Text != "" {
			m.AddAssistantMessage(firstTurn.Text)
		}
		return TurnResult{FinalText: firstTurn.Text, Aborted: firstTurn.Aborted}
	}

	// 触发 tool_use：写 assistant tool_use 消息、调工具、写 tool_result
	m.AddMessage(llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{
			firstTurn.ToolUse,
		},
	})
	if hooks.OnToolUse != nil {
		hooks.OnToolUse(*firstTurn.ToolUse)
	}

	result := toolHandler.Execute(ctx, *firstTurn.ToolUse)
	m.AddMessage(llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{
			&result,
		},
	})
	if hooks.OnToolResult != nil {
		hooks.OnToolResult(result)
	}

	// 第二次 LLM：把 tool_result 回传给 LLM 拿最终回复
	var second RunOneTurnResult
	second = m.runOneLLM(ctx, provider, systemPrompt, toolSpecs, hooks)
	if second.Err != nil {
		return TurnResult{
			FinalText:  firstTurn.Text,
			ToolUse:    firstTurn.ToolUse,
			ToolResult: result,
			Aborted:    second.Aborted,
			Error:      second.Err,
		}
	}
	if second.Text != "" {
		m.AddAssistantMessage(second.Text)
	}
	return TurnResult{
		FinalText:  second.Text,
		ToolUse:    firstTurn.ToolUse,
		ToolResult: result,
		Aborted:    second.Aborted,
	}
}

// runOneLLMResult 是 runOneLLM 的内部返回值，描述单次 LLM 流式调用的结果。
type RunOneTurnResult struct {
	// Text 为累积的 assistant 文本
	Text string
	// ToolUse 为流结束时的 ToolUse 字段（如有）
	ToolUse *llm.ToolUseBlock
	// Aborted 标识本次流是否被 ctx 取消
	Aborted bool
	// Err 为 StreamChat 初始化失败或 chunk.Err 携带的错误
	Err error
}

// runOneLLM 发起一次 LLM 流式调用并消费完整流。
//
// 不修改 history；返回累积的文本与 ToolUse 供 RunTurn 决策下一步。
// 通过 hooks.OnStreamChunk 把每个 chunk 推给上层（含 Done=true 的结束 chunk）。
func (m *ConversationManager) runOneLLM(
	ctx context.Context,
	provider llm.Provider,
	systemPrompt string,
	toolSpecs []tool.ToolSpec,
	hooks TurnHooks,
) RunOneTurnResult {
	messages := m.GetContext(systemPrompt)
	chunkCh, err := provider.StreamChat(ctx, systemPrompt, messages, toolSpecs)
	if err != nil {
		if hooks.OnError != nil {
			hooks.OnError(err)
		}
		return RunOneTurnResult{Err: err}
	}

	var (
		textBuf       []byte
		pendingToolUse *llm.ToolUseBlock
		aborted       bool
		streamDone    bool
	)
	for {
		select {
		case <-ctx.Done():
			// ctx 取消：标记 aborted 并尝试排空 channel 以让 Provider goroutine 退出
			aborted = true
			for {
				select {
				case _, ok := <-chunkCh:
					if !ok {
						return RunOneTurnResult{Text: string(textBuf), ToolUse: pendingToolUse, Aborted: true}
					}
				default:
					return RunOneTurnResult{Text: string(textBuf), ToolUse: pendingToolUse, Aborted: true}
				}
			}
		case chunk, ok := <-chunkCh:
			if !ok {
				// channel 关闭：流正常结束
				if !streamDone {
					// 合成一个 Done=true 的 chunk 推给上层（部分 Provider 在脚本耗尽时直接 close，不发 Done）
					if hooks.OnStreamChunk != nil {
						hooks.OnStreamChunk(llm.StreamChunk{Done: true})
					}
				}
				return RunOneTurnResult{Text: string(textBuf), ToolUse: pendingToolUse, Aborted: aborted}
			}
			if chunk.Err != nil {
				if hooks.OnError != nil {
					hooks.OnError(chunk.Err)
				}
				return RunOneTurnResult{Text: string(textBuf), ToolUse: pendingToolUse, Aborted: aborted, Err: chunk.Err}
			}
			if chunk.ToolUse != nil {
				pendingToolUse = chunk.ToolUse
			}
			if chunk.Content != "" {
				textBuf = append(textBuf, chunk.Content...)
			}
			if hooks.OnStreamChunk != nil {
				hooks.OnStreamChunk(chunk)
			}
			if chunk.Done {
				streamDone = true
				return RunOneTurnResult{Text: string(textBuf), ToolUse: pendingToolUse, Aborted: aborted}
			}
		}
	}
}
