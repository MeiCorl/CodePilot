// Package conversation 中的 agent_loop 实现 ReAct 模式的循环推理引擎。
//
// AgentLoop 将 Step 2 的「单轮 LLM + 工具执行闭环」升级为可循环迭代的
// 推理引擎，支持：多轮工具调用、迭代上限保护、上下文溢出检查、优雅中断、
// 工具错误智能反馈。它是 CodePilot Agent 自主完成复杂任务的核心驱动。
package conversation

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/hook"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
	"github.com/MeiCorl/CodePilot/src/llm"
)

// ---- AgentLoop 配置与结果 ----

// AgentLoopConfig 控制 AgentLoop 的行为参数。
type AgentLoopConfig struct {
	// MaxIterations 为最大迭代次数（一次迭代 = 一次 LLM 调用 + 可能的工具执行），默认 50
	MaxIterations int
	// ContextSafetyMargin 为上下文安全余量（token 数），剩余低于此值时触发优雅终止
	ContextSafetyMargin int
	// ContextWindowSize 为模型上下文窗口总大小（token 数），用于 RemainingTokens 计算
	ContextWindowSize int
}

// StopReason 描述 AgentLoop 终止原因的枚举。
type StopReason string

const (
	// StopReasonCompleted 表示 LLM 认为任务完成（无 tool_use），正常终止
	StopReasonCompleted StopReason = "completed"
	// StopReasonMaxIterations 表示达到最大迭代次数，已注入提示让模型收尾
	StopReasonMaxIterations StopReason = "max_iterations"
	// StopReasonContextOverflow 表示上下文空间不足，已注入提示让模型收尾
	StopReasonContextOverflow StopReason = "context_overflow"
	// StopReasonAborted 表示用户主动中断（ctx 取消）
	StopReasonAborted StopReason = "aborted"
	// StopReasonError 表示发生不可恢复错误
	StopReasonError StopReason = "error"
)

// abortMarker 是用户主动取消回复时写入 history 的标记文本。
// 当用户中断 LLM 流式输出后，后续发送新问题时，LLM 在上下文中看到此标记，
// 便知道前一个问题已被用户主动取消，无需再关注，只需回答最新的用户问题。
const abortMarker = "[用户取消了回复]"

// AgentLoopResult 是 AgentLoop 的返回值，描述整个循环执行的最终结果。
type AgentLoopResult struct {
	// FinalText 为最终回复文本（可能是正常回复，也可能是收尾提示后的总结）
	FinalText string
	// Iterations 为实际执行的迭代次数
	Iterations int
	// TotalToolCalls 为循环中所有工具调用的总次数
	TotalToolCalls int
	// StopReason 为终止原因枚举
	StopReason StopReason
	// Aborted 标识是否被用户中断
	Aborted bool
	// Error 为不可恢复错误（如有）
	Error error
}

// AgentLoopHooks 扩展 TurnHooks，增加 AgentLoop 级别的回调。
type AgentLoopHooks struct {
	// TurnHooks 为每轮迭代的流式事件回调（复用原有 TurnHooks）
	TurnHooks
	// OnIterationStart 在每轮迭代开始时回调，通知上层当前迭代进度
	OnIterationStart func(iteration int, maxIterations int)
	// OnLoopDone 在循环结束时回调，携带最终结果
	OnLoopDone func(result AgentLoopResult)
}

// ---- AgentLoop 核心方法 ----

// AgentLoop 执行 ReAct 模式的循环推理。
//
// 核心循环逻辑：
//  1. 每次迭代检查 ctx 取消 → 上下文溢出 → 发起 LLM 调用
//  2. LLM 无 tool_use → 任务完成，写 assistant 文本到 history，终止
//  3. LLM 有 tool_use → 写 assistant tool_use 消息 → ExecuteBatch 执行 →
//     写 user tool_result 消息 → 继续下一轮
//  4. 达到上限或溢出时，注入提示消息后再调一次 LLM 获取最终回复
//
// 并发约束：与 RunTurn 一致，调用方需保证同一时刻只有一个 AgentLoop 活跃。
//
// Step 4 起：systemPrompt 升级为 llm.SystemPrompt（结构化形态），内含
// SystemBlocks（Anthropic 可打 cache_control）与 LeadUserMessage（首条 user 消息）。
// 调用方（web handler）应通过 prompt.Builder.Assemble 一次性构建并复用。
func (m *ConversationManager) AgentLoop(
	ctx context.Context,
	provider llm.Provider,
	sp llm.SystemPrompt,
	toolSpecs []tool.ToolSpec,
	toolHandler *ToolHandler,
	cfg AgentLoopConfig,
	hooks AgentLoopHooks,
) AgentLoopResult {
	maxIter := cfg.MaxIterations
	if maxIter <= 0 {
		maxIter = 50
	}

	var (
		totalToolCalls int
		finalText      string
	)

	for iteration := 1; iteration <= maxIter; iteration++ {
		// ---- 检查 ctx 取消 ----
		if ctx.Err() != nil {
			logger.InfoCtx(ctx, "AgentLoop 被用户中断", zap.Int("iteration", iteration))
			result := AgentLoopResult{
				FinalText:      finalText,
				Iterations:     iteration - 1,
				TotalToolCalls: totalToolCalls,
				StopReason:     StopReasonAborted,
				Aborted:        true,
			}
			hooks.fireLoopDone(result)
			return result
		}

		// ---- 通知 Hook 与上层迭代进度 ----
		m.currentIteration = iteration
		if toolHandler != nil {
			toolHandler.SetHookSessionID(m.sessionID)
			toolHandler.SetHookIteration(iteration)
		}
		m.dispatchHook(ctx, hook.EventIterationStart,
			hook.NewIterationContext(hook.EventIterationStart, m.sessionID, m.hookWorkdir, iteration))
		hooks.fireIterationStart(iteration, maxIter)

		// ---- 检查上下文溢出 ----
		if cfg.ContextWindowSize > 0 && cfg.ContextSafetyMargin > 0 {
			remaining := m.RemainingTokens(cfg.ContextWindowSize)
			if remaining < cfg.ContextSafetyMargin {
				logger.WarnCtx(ctx, "上下文空间不足，触发优雅终止",
					zap.Int("remaining", remaining),
					zap.Int("safety_margin", cfg.ContextSafetyMargin),
					zap.Int("iteration", iteration),
				)
				finalText = m.injectTerminationPrompt(ctx, provider, sp, toolSpecs, hooks,
					"上下文空间即将耗尽，请立即总结当前进展并用简洁的语言回复用户。不要再调用任何工具。")
				finalText = m.ensureNonEmptyReply(ctx, provider, sp, toolSpecs, hooks, finalText)
				result := AgentLoopResult{
					FinalText:      finalText,
					Iterations:     iteration,
					TotalToolCalls: totalToolCalls,
					StopReason:     StopReasonContextOverflow,
				}
				m.dispatchHook(ctx, hook.EventIterationEnd,
					hook.NewIterationContext(hook.EventIterationEnd, m.sessionID, m.hookWorkdir, iteration))
				hooks.fireLoopDone(result)
				return result
			}
		}

		// ---- 发起 LLM 调用 ----
		turnResult := m.runOneLLM(ctx, provider, sp, toolSpecs, hooks.TurnHooks)

		// 更新 token 用量（用于精确的上下文窗口剩余额度计算）
		// 即使本次调用出错或被取消，usage 仍可能有值（部分 Provider 在中断前已返回）
		m.UpdateUsage(turnResult.Usage)

		if turnResult.Err != nil {
			// LLM 调用失败，中断循环
			logger.ErrorCtx(ctx, "AgentLoop LLM 调用失败",
				zap.Int("iteration", iteration),
				zap.Error(turnResult.Err),
			)
			m.dispatchHook(ctx, hook.EventError,
				hook.NewErrorContext(m.sessionID, m.hookWorkdir, turnResult.Err.Error(), hook.EventError, iteration))
			result := AgentLoopResult{
				FinalText:      finalText,
				Iterations:     iteration,
				TotalToolCalls: totalToolCalls,
				StopReason:     StopReasonError,
				Aborted:        turnResult.Aborted,
				Error:          turnResult.Err,
			}
			m.dispatchHook(ctx, hook.EventIterationEnd,
				hook.NewIterationContext(hook.EventIterationEnd, m.sessionID, m.hookWorkdir, iteration))
			hooks.fireLoopDone(result)
			return result
		}

		if turnResult.Aborted {
			logger.InfoCtx(ctx, "AgentLoop 在 LLM 调用中被取消", zap.Int("iteration", iteration))
			// 将取消标记写入 history，保持对话结构完整，
			// 使后续用户发新问题时 LLM 知道前一个问题已被取消，只需关注最新问题
			m.persistAbortedTurn(turnResult)
			result := AgentLoopResult{
				FinalText:      finalText,
				Iterations:     iteration,
				TotalToolCalls: totalToolCalls,
				StopReason:     StopReasonAborted,
				Aborted:        true,
			}
			m.dispatchHook(ctx, hook.EventIterationEnd,
				hook.NewIterationContext(hook.EventIterationEnd, m.sessionID, m.hookWorkdir, iteration))
			hooks.fireLoopDone(result)
			return result
		}

		// ---- 无 tool_use → 任务完成 ----
		if !turnResult.HasToolUse() {
			// 检测 LLM 输出是否因 max_tokens 被截断
			if turnResult.LLMStopReason == "max_tokens" || turnResult.LLMStopReason == "length" {
				logger.WarnCtx(ctx, "AgentLoop 检测到 LLM 输出被截断（stop_reason=max_tokens/length），回复可能不完整",
					zap.Int("iteration", iteration),
					zap.String("llm_stop_reason", turnResult.LLMStopReason),
					zap.Int("text_length", len(turnResult.Text)),
				)
			}
			if turnResult.Text != "" {
				m.AddAssistantMessage(turnResult.Text)
				m.dispatchHook(ctx, hook.EventPostMessage,
					hook.NewMessageContext(hook.EventPostMessage, turnResult.Text, string(llm.RoleAssistant), m.sessionID, m.hookWorkdir, iteration))
				finalText = turnResult.Text
			} else {
				// LLM 返回了空文本且无工具调用，尝试补充一次让模型输出总结
				logger.WarnCtx(ctx, "LLM 返回空文本且无工具调用，尝试补充生成回复",
					zap.Int("iteration", iteration),
					zap.Int("total_tool_calls", totalToolCalls),
				)
				finalText = m.ensureNonEmptyReply(ctx, provider, sp, toolSpecs, hooks, finalText)
			}
			result := AgentLoopResult{
				FinalText:      finalText,
				Iterations:     iteration,
				TotalToolCalls: totalToolCalls,
				StopReason:     StopReasonCompleted,
			}
			m.dispatchHook(ctx, hook.EventIterationEnd,
				hook.NewIterationContext(hook.EventIterationEnd, m.sessionID, m.hookWorkdir, iteration))
			hooks.fireLoopDone(result)
			return result
		}

		// ---- 有 tool_use → 执行工具 ----
		// 1. 写 assistant tool_use 消息到 history
		assistantContent := make([]llm.ContentBlock, 0, len(turnResult.ToolUses)+1)
		if turnResult.Text != "" {
			assistantContent = append(assistantContent, llm.NewTextBlock(turnResult.Text))
		}
		for i := range turnResult.ToolUses {
			tu := turnResult.ToolUses[i]
			assistantContent = append(assistantContent, &tu)
			// 通知上层每个 tool_use
			if hooks.TurnHooks.OnToolUse != nil {
				hooks.TurnHooks.OnToolUse(tu)
			}
		}
		m.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: assistantContent})
		m.dispatchHook(ctx, hook.EventPostMessage,
			hook.NewMessageContext(hook.EventPostMessage, messageText(llm.Message{Role: llm.RoleAssistant, Content: assistantContent}), string(llm.RoleAssistant), m.sessionID, m.hookWorkdir, iteration))

		// 2. 批量执行工具
		results := toolHandler.ExecuteBatch(ctx, turnResult.ToolUses)
		totalToolCalls += len(results)

		// 3. 写 user tool_result 消息到 history
		resultContent := make([]llm.ContentBlock, 0, len(results))
		for i := range results {
			resultContent = append(resultContent, &results[i])
			if hooks.TurnHooks.OnToolResult != nil {
				hooks.TurnHooks.OnToolResult(results[i])
			}
		}
		m.AddMessage(llm.Message{Role: llm.RoleUser, Content: resultContent})
		m.dispatchHook(ctx, hook.EventIterationEnd,
			hook.NewIterationContext(hook.EventIterationEnd, m.sessionID, m.hookWorkdir, iteration))

		// 继续下一轮迭代...
	}

	// ---- 达到最大迭代次数 ----
	logger.WarnCtx(ctx, "AgentLoop 达到最大迭代次数",
		zap.Int("max_iterations", maxIter),
		zap.Int("total_tool_calls", totalToolCalls),
	)
	finalText = m.injectTerminationPrompt(ctx, provider, sp, toolSpecs, hooks,
		fmt.Sprintf("已达到最大迭代次数限制（%d 次），请立即总结当前进展并回复用户。不要再调用任何工具。", maxIter))
	finalText = m.ensureNonEmptyReply(ctx, provider, sp, toolSpecs, hooks, finalText)

	result := AgentLoopResult{
		FinalText:      finalText,
		Iterations:     maxIter,
		TotalToolCalls: totalToolCalls,
		StopReason:     StopReasonMaxIterations,
	}
	hooks.fireLoopDone(result)
	return result
}

// ---- 辅助方法 ----

// injectTerminationPrompt 注入一条终止提示消息到 history 并发起一次 LLM 调用，
// 获取模型的收尾回复。用于上下文溢出和迭代上限两种场景。
//
// 返回模型的最终回复文本。
func (m *ConversationManager) injectTerminationPrompt(
	ctx context.Context,
	provider llm.Provider,
	sp llm.SystemPrompt,
	toolSpecs []tool.ToolSpec,
	hooks AgentLoopHooks,
	promptText string,
) string {
	// 注入一条 user 消息作为终止提示
	m.AddUserMessage(promptText)

	// 不传工具描述，让模型只回复文本
	finalTurn := m.runOneLLM(ctx, provider, sp, nil, hooks.TurnHooks)
	if finalTurn.Err != nil {
		logger.ErrorCtx(ctx, "终止提示 LLM 调用失败", zap.Error(finalTurn.Err))
		return ""
	}
	if finalTurn.Text != "" {
		m.AddAssistantMessage(finalTurn.Text)
	} else {
		logger.WarnCtx(ctx, "终止提示后 LLM 仍然返回空文本")
	}
	return finalTurn.Text
}

// ensureNonEmptyReply 确保最终回复非空。
// 当 AgentLoop 结束时 finalText 为空（LLM 全程只调用工具不说话等场景），
// 注入一条提示消息请求 LLM 给出总结回复。如果补充回复也失败或为空，
// 返回兜底的默认消息。
func (m *ConversationManager) ensureNonEmptyReply(
	ctx context.Context,
	provider llm.Provider,
	sp llm.SystemPrompt,
	toolSpecs []tool.ToolSpec,
	hooks AgentLoopHooks,
	currentFinalText string,
) string {
	// 如果已有文本，直接返回
	if currentFinalText != "" {
		return currentFinalText
	}

	// 注入提示，请求 LLM 总结当前工作
	m.AddUserMessage("请总结你刚才完成的工作，用简洁的语言回复用户。")

	// 不传工具，强制模型只回复文本
	summarizeTurn := m.runOneLLM(ctx, provider, sp, nil, hooks.TurnHooks)
	if summarizeTurn.Err != nil {
		logger.ErrorCtx(ctx, "补充总结回复失败，使用兜底消息", zap.Error(summarizeTurn.Err))
		fallback := "（任务已执行完成，但生成回复时遇到问题）"
		m.AddAssistantMessage(fallback)
		return fallback
	}

	if summarizeTurn.Text != "" {
		m.AddAssistantMessage(summarizeTurn.Text)
		return summarizeTurn.Text
	}

	// 补充回复也为空，使用兜底消息
	logger.WarnCtx(ctx, "补充总结回复仍然为空，使用兜底消息")
	fallback := "（任务已执行完成，但模型未返回可显示的文本内容）"
	m.AddAssistantMessage(fallback)
	return fallback
}

// fireIterationStart 安全地触发 OnIterationStart 回调。
func (h *AgentLoopHooks) fireIterationStart(iteration, maxIterations int) {
	if h.OnIterationStart != nil {
		h.OnIterationStart(iteration, maxIterations)
	}
}

// fireLoopDone 安全地触发 OnLoopDone 回调。
func (h *AgentLoopHooks) fireLoopDone(result AgentLoopResult) {
	if h.OnLoopDone != nil {
		h.OnLoopDone(result)
	}
}

// persistAbortedTurn 在用户主动取消时，将取消标记写入 history，
// 保持对话 User/Assistant 交替结构完整，使后续 LLM 调用上下文语义正确。
//
// 处理三种场景：
//   - 部分文本 + 无 tool_use：写入一条带取消标记的 assistant 消息
//   - 有 tool_use 块：写入 assistant 消息（含 tool_use）+ 合成 error tool_result 消息
//   - 空轮（未收到任何内容）：写入一条仅含取消标记的 assistant 消息
func (m *ConversationManager) persistAbortedTurn(turnResult RunOneTurnResult) {
	// 有 tool_use 块：需要同时写入 assistant 和合成 error tool_result，
	// 保持 tool_use / tool_result 配对结构完整
	if len(turnResult.ToolUses) > 0 {
		// 构造 assistant 消息：取消标记 + tool_use 块
		content := make([]llm.ContentBlock, 0, 1+len(turnResult.ToolUses))
		content = append(content, llm.NewTextBlock(abortMarker))
		for i := range turnResult.ToolUses {
			content = append(content, &turnResult.ToolUses[i])
		}
		m.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: content})

		// 为每个 tool_use 写入合成 error tool_result，保持 history 结构完整
		resultContent := make([]llm.ContentBlock, 0, len(turnResult.ToolUses))
		for _, tu := range turnResult.ToolUses {
			resultContent = append(resultContent, llm.NewToolResultBlock(
				tu.ID,
				"工具执行已取消：用户中断了回复",
				true, // isError
			))
		}
		m.AddMessage(llm.Message{Role: llm.RoleUser, Content: resultContent})
		return
	}

	// 无 tool_use（部分文本或空轮）：仅写入取消标记
	m.AddAssistantMessage(abortMarker)
}
