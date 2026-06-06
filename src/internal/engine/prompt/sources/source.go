// Package sources 定义 System Prompt 的「来源」抽象。
//
// System Prompt 由多个 Source 各自产出一段内容（Section），
// 再由 Builder 按 Placement 分组后拼成最终结构。
// 任何想要往 System Prompt 注入内容的子系统（静态规则、环境上下文、
// AGENTS.md、自动记忆等）只需实现 Source 接口并在 Builder 中注册即可。
package sources

import (
	"context"

	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/template"
)

// Placement 决定 Section 应当进入 LLM 请求的哪个位置。
//
// 	- PlacementSystem: 进入 system 字段（适合稳定、可缓存的全局指令）
// 	- PlacementUserMessage: 进入 messages 首条 user 消息（适合可能很长、
// 	  需要在多轮迭代中动态更新的内容，避免塞进 system 造成注意力稀释）
type Placement int

const (
	// PlacementSystem 表示该段内容应进入 LLM 请求的 system 字段。
	PlacementSystem Placement = iota
	// PlacementUserMessage 表示该段内容应作为首条 user-role 消息发送。
	// 多条 PlacementUserMessage 会被 Builder 合并为单条 LeadUserMessage。
	PlacementUserMessage
)

// Section 是一个 Source 产出的「一段」System Prompt 内容。
// 同一 Source 一次 Assemble 调用产出且仅产出一个 Section。
type Section struct {
	// Name 为人类可读的来源标识（如 "static"、"environment"），
	// 用于 Stats 报告与日志定位。
	Name string
	// Content 为本段内容原文。Builder 不会修改其内容，
	// 模板变量替换由 Source 自己在 Assemble 内完成。
	Content string
	// Placement 决定 Builder 把本段内容分发到 SystemBlocks 还是 LeadUserMessage。
	Placement Placement
	// Tokens 为本段内容的 token 估算值，由 Source 在 Assemble 内调用
	// tokens.Estimate 计算后填入，避免 Builder 重复计算。
	Tokens int
}

// SystemBlock 是进入 system 字段的一段内容。
//
// Anthropic 协议下 SystemBlocks 会被进一步切片为多段带 cache_control 的
// 内容；Cacheable=true 的段会被打上 cache 标记，Cacheable=false 的
// 段不会。Builder 默认把所有 PlacementSystem 的段都标记为 Cacheable。
type SystemBlock struct {
	// Text 为该段 system 内容的原文。
	Text string
	// Cacheable 表示该段是否可被 Anthropic 协议层标记为 cache 命中区。
	// 几乎所有静态内容都应保持 true；只有每次请求都变的内容才设为 false。
	Cacheable bool
}

// SourceStat 记录单个 Source 产出的内容在最终 System Prompt 中的
// token 开销，用于 WebUI 状态栏展示与可观测性。
type SourceStat struct {
	// Name 对应 Section.Name。
	Name string
	// Tokens 对应 Section.Tokens。
	Tokens int
}

// SystemPrompt 是 Builder.Assemble 的最终产物。
//
// 它是连接 prompt 模块与 LLM Provider 之间的契约类型：
//
//	- Anthropic Provider 会把 SystemBlocks 切片为多段带 cache_control 的内容
//	- OpenAI Provider 会把 SystemBlocks 拼为单个 system 字符串
//	- 两个 Provider 都会把 LeadUserMessage 追加到 messages 最前部
//	- WebUI 状态栏会展示 TotalTokens + Stats 给用户
type SystemPrompt struct {
	// SystemBlocks 为进入 LLM 请求 system 字段的内容段。
	// 切片顺序与 Source 注册顺序一致。
	SystemBlocks []SystemBlock
	// LeadUserMessage 为合并后的首条 user 消息。
	// 当所有 PlacementUserMessage 段都为空时，本字段为空字符串，
	// Provider 收到后不会创建空消息。
	LeadUserMessage string
	// Stats 记录每个 Source 贡献的 token 数，顺序与 Source 注册顺序一致。
	Stats []SourceStat
	// TotalTokens 为所有 Source 产出 token 的累加值。
	TotalTokens int
}

// IsEmpty 判定本 SystemPrompt 是否完全无内容。
// 用于 Provider 在收到空 SP 时跳过 system 字段构造的特殊场景。
func (sp SystemPrompt) IsEmpty() bool {
	return len(sp.SystemBlocks) == 0 && sp.LeadUserMessage == ""
}

// GitStatus 描述当前工作目录的 Git 状态，由 environment Source 采集。
// 定义在 template 子包以避免循环依赖。
//
// Deprecated: 该类型已迁至 template.GitStatus，本处保留类型别名以保持
// 向后兼容（Builder 仍通过 sources.GitStatus 引用）。
// 新代码请使用 template.GitStatus。
type GitStatus = template.GitStatus

// Env 是 Source.Assemble 接收的输入环境参数。
// 定义在 template 子包以避免循环依赖。
//
// Deprecated: 该类型已迁至 template.Env，本处保留类型别名以保持
// 向后兼容（Builder 仍通过 sources.Env 引用）。
// 新代码请使用 template.Env。
type Env = template.Env

// Source 是 System Prompt 内容来源的统一抽象。
//
// 每个 Source 负责产出一段独立的内容（Section），Builder 负责聚合。
// Source 应是**无状态**的（除依赖 Env 之外不依赖任何外部状态），
// 以保证多次调用结果一致（确定性）。
type Source interface {
	// Name 返回该 Source 的标识，用于 Stats / 日志 / 配置覆盖 key。
	Name() string

	// Assemble 产出本 Source 应贡献的 Section。
	//
	// 实现要求：
	//  1. 必须是纯函数 + 只读 Env：相同 Env 多次调用结果完全一致
	//  2. 模板变量替换（{{OS}} 等）由 Source 内部完成，Builder 不再做替换
	//  3. token 估算由 Source 内部填入 Section.Tokens
	//  4. 不得 panic：失败应返回 error，Builder 会包装后上抛
	Assemble(ctx context.Context, env Env) (Section, error)
}
