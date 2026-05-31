// Package llm 提供 LLM 供应商的统一抽象层。
// 定义通用的消息类型（ContentBlock 体系）和 Provider 接口，
// 使上层代码无需关心底层供应商 SDK 的差异。
package llm

// ContentBlockType 标识 ContentBlock 的具体类型。
// 当前仅支持文本，后续将扩展图片（ImageBlock）、工具调用（ToolUseBlock）等类型。
type ContentBlockType string

const (
	// ContentBlockTypeText 表示文本内容块
	ContentBlockTypeText ContentBlockType = "text"
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
}
