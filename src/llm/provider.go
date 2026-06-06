package llm

import (
	"context"
	"fmt"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// Provider 是 LLM 供应商的统一抽象接口。
// 不同供应商（Anthropic、OpenAI 等）通过实现此接口接入系统，
// 上层代码通过此接口与 LLM 交互，无需关心底层 SDK 差异。
type Provider interface {
	// StreamChat 发起一次流式对话请求。
	//
	// 参数：
	//   - ctx: 支持通过 cancel 终止流式请求（如用户按 Esc 中断）
	//   - sp: System Prompt 结构化形态（system 字段内容 + 首条 user 消息）；
	//     当 sp.IsEmpty() 为 true 时，Provider 不构造 system 字段、不注入首条 user 消息
	//   - messages: 对话历史（ContentBlock 数组形式的通用消息），不含 system 字段
	//   - toolSpecs: 当前可用的工具描述列表；为空表示本次不启用任何工具
	//
	// 返回值：
	//   - <-chan StreamChunk: 只读 channel，消费方从中读取流式数据块
	//   - error: 请求初始化阶段的错误（如参数校验失败）
	//
	// 流结束时 channel 会收到 Done=true 的 chunk 并自动关闭。
	// 若 LLM 返回了 tool_use 块，Done=true 的最后一个 chunk 上会捎带 ToolUse 字段。
	// 请求过程中发生错误时，channel 会收到 Err 非 nil 的 chunk。
	//
	// 关于 SystemPrompt 的协议处理：
	//   - Anthropic: sp.SystemBlocks 转换为多段带 cache_control 标记的 system 内容；
	//     sp.LeadUserMessage 作为首条 user-role 消息插入 messages 最前
	//   - OpenAI: sp.SystemBlocks 按顺序拼接为单条 system-role 消息；
	//     sp.LeadUserMessage 作为首条 user-role 消息插入 messages 最前
	StreamChat(ctx context.Context, sp SystemPrompt, messages []Message, toolSpecs []tool.ToolSpec) (<-chan StreamChunk, error)
}

// NewProvider 根据配置创建对应的 Provider 实例。
// 不支持的供应商会返回明确错误。
func NewProvider(cfg *config.Config) (Provider, error) {
	switch cfg.Provider {
	case "anthropic":
		return NewAnthropicProvider(cfg), nil
	case "openai":
		return NewOpenAIProvider(cfg), nil
	default:
		return nil, fmt.Errorf("不支持的供应商: %s", cfg.Provider)
	}
}
