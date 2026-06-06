// Package llm 提供 LLM 供应商的统一抽象层。
// 定义通用的消息类型（ContentBlock 体系）和 Provider 接口，
// 使上层代码无需关心底层供应商 SDK 的差异。
package llm

import "encoding/json"

// ContentBlockType 标识 ContentBlock 的具体类型。
// 当前支持文本、工具调用（tool_use）、工具结果（tool_result），
// 后续将扩展图片（ImageBlock）等类型。
type ContentBlockType string

const (
	// ContentBlockTypeText 表示文本内容块
	ContentBlockTypeText ContentBlockType = "text"
	// ContentBlockTypeToolUse 表示 LLM 发出的工具调用请求（Anthropic 协议对齐为 "tool_use"）
	ContentBlockTypeToolUse ContentBlockType = "tool_use"
	// ContentBlockTypeToolResult 表示系统回传给 LLM 的工具执行结果（Anthropic 协议对齐为 "tool_result"）
	ContentBlockTypeToolResult ContentBlockType = "tool_result"
)

// ContentBlock 是消息内容块的统一接口。
// 不同类型的内容（文本、图片、工具调用）均实现此接口，
// 支持统一的消息内容表示和多模态扩展。
type ContentBlock interface {
	// Type 返回内容块的具体类型标识
	Type() ContentBlockType
	// ToText 返回内容块的文本表示（用于日志、调试等场景）
	ToText() string
}

// TextBlock 表示文本内容块，是最基础的消息内容类型。
type TextBlock struct {
	// Text 为文本内容
	Text string `json:"text"`
}

// Type 返回 TextBlock 的类型标识。
func (b *TextBlock) Type() ContentBlockType { return ContentBlockTypeText }

// ToText 返回 TextBlock 的文本内容。
func (b *TextBlock) ToText() string { return b.Text }

// NewTextBlock 创建一个文本内容块。
func NewTextBlock(text string) ContentBlock {
	return &TextBlock{Text: text}
}

// ToolUseBlock 表示 LLM 发出的工具调用请求。
// 对应 Anthropic 协议的 tool_use 与 OpenAI 协议的 tool_calls.function。
// Input 为原始 JSON，由工具自身解析到内部结构。
type ToolUseBlock struct {
	// ID 为本次调用的唯一标识（Anthropic: tool_use.id；OpenAI: tool_call.id）
	ID string `json:"id"`
	// Name 为被调用的工具名（必须与 Tool.Name() 一致）
	Name string `json:"name"`
	// Input 为 LLM 传入的参数，原始 JSON 对象
	Input json.RawMessage `json:"input"`
}

// Type 返回 ToolUseBlock 的类型标识。
func (b *ToolUseBlock) Type() ContentBlockType { return ContentBlockTypeToolUse }

// ToText 返回 ToolUseBlock 的文本表示，格式 `tool_use(<name>, id=<id>)`。
func (b *ToolUseBlock) ToText() string {
	return "tool_use(" + b.Name + ", id=" + b.ID + ")"
}

// NewToolUseBlock 创建一个工具调用内容块。
func NewToolUseBlock(id, name string, input json.RawMessage) ContentBlock {
	return &ToolUseBlock{ID: id, Name: name, Input: input}
}

// ToolResultBlock 表示系统回传给 LLM 的工具执行结果。
// 对应 Anthropic 协议的 tool_result 与 OpenAI 协议的 role=tool 消息。
// 失败时 IsError=true，Content 为错误描述字符串。
type ToolResultBlock struct {
	// ToolUseID 关联到对应的 ToolUseBlock.ID
	ToolUseID string `json:"tool_use_id"`
	// Content 为工具返回的文本结果（成功时为输出，失败时为错误描述）
	Content string `json:"content"`
	// IsError 标识工具是否执行失败；true 时 LLM 视 Content 为错误信息
	IsError bool `json:"is_error"`
}

// Type 返回 ToolResultBlock 的类型标识。
func (b *ToolResultBlock) Type() ContentBlockType { return ContentBlockTypeToolResult }

// ToText 返回 ToolResultBlock 的文本表示。
// 失败时前缀 `error:` 以便日志/调试时一眼区分。
func (b *ToolResultBlock) ToText() string {
	if b.IsError {
		return "error: " + b.Content
	}
	return b.Content
}

// NewToolResultBlock 创建一个工具结果内容块。
func NewToolResultBlock(toolUseID, content string, isError bool) ContentBlock {
	return &ToolResultBlock{ToolUseID: toolUseID, Content: content, IsError: isError}
}

// --- 消息角色 ---

// Role 标识消息的发送角色。
type Role string

const (
	// RoleSystem 表示系统指令消息
	RoleSystem Role = "system"
	// RoleUser 表示用户消息
	RoleUser Role = "user"
	// RoleAssistant 表示助手回复消息
	RoleAssistant Role = "assistant"
)

// Message 是通用的对话消息结构体。
// Content 使用 ContentBlock 数组表示，支持多种内容类型混合。
type Message struct {
	// Role 为消息发送角色
	Role Role `json:"role"`
	// Content 为消息内容块数组，每项实现 ContentBlock 接口
	Content []ContentBlock `json:"content"`
}

// TokenUsage 表示单次 LLM 调用的 token 用量统计。
// 在流式响应的最后一个 chunk（Done=true）上携带，供上层计算上下文窗口剩余额度。
type TokenUsage struct {
	// InputTokens 为输入 token 数（含 system_prompt + messages + tool_definitions）
	InputTokens int `json:"input_tokens"`
	// OutputTokens 为输出 token 数
	OutputTokens int `json:"output_tokens"`
}

// StreamChunk 表示流式响应中的一个数据块。
// Provider 的 StreamChat 方法通过 channel 传递此结构体，
// 消费方据此实现逐字输出、错误处理和流结束判断。
type StreamChunk struct {
	// Content 为本次数据块的文本内容（可能为空字符串）
	Content string
	// Done 为 true 表示流式响应已结束（正常结束或被取消）
	Done bool
	// Err 非 nil 表示发生错误（网络错误、API 错误等）
	Err error
	// ToolUses 为本次 LLM 响应中包含的所有 tool_use 块。
	// 仅在 Done=true 的最后一个 chunk 上携带；正常文本流保持 nil。
	// 支持模型一次返回多个工具调用（并行工具调用场景）。
	// 上层（conversation manager）据此判断是否进入工具执行阶段。
	ToolUses []ToolUseBlock
	// Usage 为本次 LLM 调用的 token 用量统计。
	// 仅在 Done=true 的最后一个 chunk 上携带；可能为 nil（流中断等场景），
	// 上层需判空后降级到字符估算。
	Usage *TokenUsage
}

// HasToolUse 返回本次流式响应是否包含 tool_use 块。
func (c StreamChunk) HasToolUse() bool {
	return len(c.ToolUses) > 0
}

// FirstToolUse 返回第一个 tool_use 块（如无则返回 nil）。
// 便捷方法，适用于单工具调用场景。
func (c StreamChunk) FirstToolUse() *ToolUseBlock {
	if len(c.ToolUses) == 0 {
		return nil
	}
	return &c.ToolUses[0]
}

// ---- Step 4: System Prompt 结构化 ----

// SystemBlock 是进入 LLM 请求 system 字段的一段内容。
//
// Anthropic 协议下 SystemBlocks 会被进一步切片为多段带 cache_control 的内容：
// Cacheable=true 的段会被打上 cache 标记，Cacheable=false 的段不会。
// OpenAI 协议下所有 SystemBlock 会按顺序拼成单条 system-role 消息内容，
// Cacheable 字段被忽略（OpenAI 不支持显式缓存控制，由服务端自动判定）。
//
// 顺序：SystemBlocks 内的元素顺序应与「来源注册顺序」一致——LLM 会按此顺序
// 解析各段语义（角色设定 → 行为准则 → ...）。
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
	// Name 对应 Source 标识（"static" / "environment" / "agents_md" / "memory"）。
	Name string
	// Tokens 为该 Source 贡献的 token 估算值。
	Tokens int
}

// SystemPrompt 是 LLM Provider 接收的 System Prompt 结构化形态。
//
// 它是连接 prompt 模块（src/internal/engine/prompt）与 LLM Provider 之间的
// 契约类型。prompt.Builder 的产物会经过简单的字段拷贝转换为本结构传给
// Provider；定义在 llm 包中可避免「prompt → llm」反向依赖。
//
// 三类内容布局：
//   - SystemBlocks: 进入 Anthropic `system` 字段的多段内容（可缓存）；
//     OpenAI 协议下拼成单条 system-role 消息
//   - LeadUserMessage: 作为首条 user-role 消息注入 messages（适合可能很长、
//     动态更新的内容，如 AGENTS.md 合并结果、Step 8 自动记忆）
//   - Stats / TotalTokens: 仅供 WebUI 可观测性展示，不影响 LLM 行为
//
// IsEmpty 在 Provider 收到空 SP 时用于跳过 system 字段构造的特殊场景。
type SystemPrompt struct {
	// SystemBlocks 为进入 LLM 请求 system 字段的内容段。
	SystemBlocks []SystemBlock
	// LeadUserMessage 为合并后的首条 user 消息。
	// 当为空字符串时，Provider 不会创建空 user 消息。
	LeadUserMessage string
	// Stats 记录每个 Source 贡献的 token 数（WebUI 状态栏展示用）。
	Stats []SourceStat
	// TotalTokens 为所有 Source 产出 token 的累加值。
	TotalTokens int
}

// IsEmpty 判定本 SystemPrompt 是否完全无内容。
// 用于 Provider 在收到空 SP 时跳过 system 字段构造、首条 user 消息注入等操作。
func (sp SystemPrompt) IsEmpty() bool {
	return len(sp.SystemBlocks) == 0 && sp.LeadUserMessage == ""
}

// NewSystemPromptFromText 便捷构造函数：把单个字符串视为一段可缓存 system 内容。
// 主要用于 Step 5 之前的过渡期调用点，以及测试中需要快速构造 SystemPrompt 的场景。
// 真实使用应通过 prompt.Builder 产生完整结构。
func NewSystemPromptFromText(text string) SystemPrompt {
	if text == "" {
		return SystemPrompt{}
	}
	return SystemPrompt{
		SystemBlocks: []SystemBlock{
			{Text: text, Cacheable: true},
		},
	}
}
