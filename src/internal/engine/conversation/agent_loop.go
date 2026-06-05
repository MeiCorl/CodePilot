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

	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/llm"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// ---- AgentLoop 配置与结果 ----

// AgentLoopConfig 控制 AgentLoop 的行为参数。
type AgentLoopConfig struct {
	// MaxIterations 为最大迭代次数（一次迭代 = 一次 LLM 调用 + 可能的工具执行），默认 25
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
func (m *ConversationManager) AgentLoop(
	ctx context.Context,
	provider llm.Provider,
	systemPrompt string,
	toolSpecs []tool.ToolSpec,
	toolHandler *ToolHandler,
	cfg AgentLoopConfig,
	hooks AgentLoopHooks,
) AgentLoopResult {
	maxIter := cfg.MaxIterations
	if maxIter <= 0 {
		maxIter = 25
	}

	var (
		totalToolCalls int
		finalText      string
	)

	for iteration := 1; iteration <= maxIter; iteration++ {
		// ---- 检查 ctx 取消 ----
		if ctx.Err() != nil {
			logger.Info("AgentLoop 被用户中断", zap.Int("iteration", iteration))
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

		// ---- 通知上层迭代进度 ----
		hooks.fireIterationStart(iteration, maxIter)

		// ---- 检查上下文溢出 ----
		if cfg.ContextWindowSize > 0 && cfg.ContextSafetyMargin > 0 {
			remaining := m.RemainingTokens(cfg.ContextWindowSize)
			if remaining < cfg.ContextSafetyMargin {
				logger.Warn("上下文空间不足，触发优雅终止",
					zap.Int("remaining", remaining),
					zap.Int("safety_margin", cfg.ContextSafetyMargin),
					zap.Int("iteration", iteration),
				)
				finalText = m.injectTerminationPrompt(ctx, provider, systemPrompt, toolSpecs, hooks,
					"上下文空间即将耗尽，请立即总结当前进展并用简洁的语言回复用户。不要再调用任何工具。")
				finalText = m.ensureNonEmptyReply(ctx, provider, systemPrompt, toolSpecs, hooks, finalText)
				result := AgentLoopResult{
					FinalText:      finalText,
					Iterations:     iteration,
					TotalToolCalls: totalToolCalls,
					StopReason:     StopReasonContextOverflow,
				}
				hooks.fireLoopDone(result)
				return result
			}
		}

		// ---- 发起 LLM 调用 ----
		turnResult := m.runOneLLM(ctx, provider, systemPrompt, toolSpecs, hooks.TurnHooks)

		// 更新 token 用量（用于精确的上下文窗口剩余额度计算）
		// 即使本次调用出错或被取消，usage 仍可能有值（部分 Provider 在中断前已返回）
		m.UpdateUsage(turnResult.Usage)

		if turnResult.Err != nil {
			// LLM 调用失败，中断循环
			logger.Error("AgentLoop LLM 调用失败",
				zap.Int("iteration", iteration),
				zap.Error(turnResult.Err),
			)
			result := AgentLoopResult{
				FinalText:      finalText,
				Iterations:     iteration,
				TotalToolCalls: totalToolCalls,
				StopReason:     StopReasonError,
				Aborted:        turnResult.Aborted,
				Error:          turnResult.Err,
			}
			hooks.fireLoopDone(result)
			return result
		}

		if turnResult.Aborted {
			logger.Info("AgentLoop 在 LLM 调用中被取消", zap.Int("iteration", iteration))
			result := AgentLoopResult{
				FinalText:      finalText,
				Iterations:     iteration,
				TotalToolCalls: totalToolCalls,
				StopReason:     StopReasonAborted,
				Aborted:        true,
			}
			hooks.fireLoopDone(result)
			return result
		}

		// ---- 无 tool_use → 任务完成 ----
		if !turnResult.HasToolUse() {
			if turnResult.Text != "" {
				m.AddAssistantMessage(turnResult.Text)
				finalText = turnResult.Text
			} else {
				// LLM 返回了空文本且无工具调用，尝试补充一次让模型输出总结
				logger.Warn("LLM 返回空文本且无工具调用，尝试补充生成回复",
					zap.Int("iteration", iteration),
					zap.Int("total_tool_calls", totalToolCalls),
				)
				finalText = m.ensureNonEmptyReply(ctx, provider, systemPrompt, toolSpecs, hooks, finalText)
			}
			result := AgentLoopResult{
				FinalText:      finalText,
				Iterations:     iteration,
				TotalToolCalls: totalToolCalls,
				StopReason:     StopReasonCompleted,
			}
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

		// 继续下一轮迭代...
	}

	// ---- 达到最大迭代次数 ----
	logger.Warn("AgentLoop 达到最大迭代次数",
		zap.Int("max_iterations", maxIter),
		zap.Int("total_tool_calls", totalToolCalls),
	)
	finalText = m.injectTerminationPrompt(ctx, provider, systemPrompt, toolSpecs, hooks,
		fmt.Sprintf("已达到最大迭代次数限制（%d 次），请立即总结当前进展并回复用户。不要再调用任何工具。", maxIter))
	finalText = m.ensureNonEmptyReply(ctx, provider, systemPrompt, toolSpecs, hooks, finalText)

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
	systemPrompt string,
	toolSpecs []tool.ToolSpec,
	hooks AgentLoopHooks,
	promptText string,
) string {
	// 注入一条 user 消息作为终止提示
	m.AddUserMessage(promptText)

	// 不传工具描述，让模型只回复文本
	finalTurn := m.runOneLLM(ctx, provider, systemPrompt, nil, hooks.TurnHooks)
	if finalTurn.Err != nil {
		logger.Error("终止提示 LLM 调用失败", zap.Error(finalTurn.Err))
		return ""
	}
	if finalTurn.Text != "" {
		m.AddAssistantMessage(finalTurn.Text)
	} else {
		logger.Warn("终止提示后 LLM 仍然返回空文本")
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
	systemPrompt string,
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
	summarizeTurn := m.runOneLLM(ctx, provider, systemPrompt, nil, hooks.TurnHooks)
	if summarizeTurn.Err != nil {
		logger.Error("补充总结回复失败，使用兜底消息", zap.Error(summarizeTurn.Err))
		fallback := "（任务已执行完成，但生成回复时遇到问题）"
		m.AddAssistantMessage(fallback)
		return fallback
	}

	if summarizeTurn.Text != "" {
		m.AddAssistantMessage(summarizeTurn.Text)
		return summarizeTurn.Text
	}

	// 补充回复也为空，使用兜底消息
	logger.Warn("补充总结回复仍然为空，使用兜底消息")
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
