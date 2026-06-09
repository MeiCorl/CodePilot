// Package session 实现 MCP 单 server 会话管理：在 Transport 之上包装
// JSON-RPC 2.0 的三阶段握手（initialize / list tools / call tool），
// 并通过 id 匹配把「异步写入的请求」与「后台读取的响应」关联起来。
//
// 设计原则：
//   - 一次 Session 对应一个远端 server 进程；多 server 由 Pool 负责
//   - 内部 pending map 持有 in-flight 请求；recvLoop goroutine 负责消费响应
//   - 任何 Session 错误（transport 断开 / 上层 ctx 取消）都即时传播到所有等待方
//   - 暴露高层方法 Initialize / NotifyInitialized / ListTools / CallTool / Close
//
// 本包仅依赖 transport 与 jsonrpc，对外提供：
//   - Session 类型（Pool / Adapter 引用）
//   - MCP 协议相关数据结构（MCPTool / MCPCallResult / MCPContent）
//   - 错误哨兵（ErrSessionClosed / ErrRPCTimeout / ErrServerFailed / ErrServerUnhealthy）
package session

import (
	"encoding/json"
	"fmt"
)

// ProtocolVersion 是 CodePilot 客户端握手时声明的 MCP 协议版本。
//
// 当前对应 MCP 2025-03-26 规范；与远端 server 协商失败时由 server 决定
// 是否降级兼容，client 端不做版本协商（保留后续扩展空间）。
const ProtocolVersion = "2025-03-26"

// ClientName 是握手时声明的 clientInfo.name。
// 固定为 CodePilot，便于 server 端日志区分。
const ClientName = "CodePilot"

// ClientVersion 是握手时声明的 clientInfo.version。
// 启动时由 main.go 通过 SetClientVersion 注入，未注入时回退到 "dev"。
var ClientVersion = "dev"

// MCPServerCapabilities 是 initialize 响应中远端 server 声明的能力子集。
//
// 仅保留 client 关心的字段；其他字段（如 logging、prompts）忽略，避免
// 后续 MCP 协议扩展时影响 client 兼容。完整能力字段参见 MCP 2025-03-26 规范。
type MCPServerCapabilities struct {
	// Tools server 是否支持 tools/list / tools/call。
	// 本步骤固定为 true 才视为可用。
	Tools *struct{} `json:"tools,omitempty"`
	// 其他 capability 字段（resources / prompts / logging）按需扩展。
}

// MCPServerInfo 远端 server 自我介绍。
type MCPServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// InitializeResult 是 initialize 响应解析后的内部结构。
// 对外仅暴露 Capabilities + ServerInfo 两个字段。
type InitializeResult struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    MCPServerCapabilities  `json:"capabilities"`
	ServerInfo      MCPServerInfo          `json:"serverInfo"`
	Raw             json.RawMessage        `json:"-"` // 保留原始 JSON，便于未来扩展字段
}

// MCPTool 是 tools/list 返回的单个远端工具描述。
//
// 字段含义遵循 MCP tools 规范：
//   - Name        server 内部工具名（不含 mcp__ 前缀）
//   - Description 给 LLM 看的人类可读描述
//   - InputSchema JSON Schema 对象，描述入参形状
//
// 在 CodePilot 侧注册到 tool.Registry 时，会自动加上 mcp__<server>__ 前缀。
type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// MCPContentKind 区分 tools/call 返回 content 数组的元素类型。
type MCPContentKind string

const (
	// MCPContentText 纯文本片段（最常见）。
	MCPContentText MCPContentKind = "text"
	// MCPContentImage base64 编码的图片。
	MCPContentImage MCPContentKind = "image"
	// MCPContentResource 嵌入式资源链接（如 file://）。
	MCPContentResource MCPContentKind = "resource"
)

// MCPContent 是 tools/call 响应 content 数组的单个元素。
//
// MCP 规范允许 text / image / resource 等多种类型；CodePilot 侧目前
// 仅关心 text 字段，image/resource 序列化为 JSON 字符串透传展示。
// Data 与 MimeType 仅在非 text 类型时有意义。
type MCPContent struct {
	Type     MCPContentKind `json:"type"`
	Text     string         `json:"text,omitempty"`
	Data     string         `json:"data,omitempty"`
	MimeType string         `json:"mimeType,omitempty"`
}

// MCPCallResult 是 tools/call 的成功响应。
//
// IsError 字段来自 MCP 规范：业务执行失败但 JSON-RPC 层面成功时（如
// echo("") 返回参数错误），server 也会返回 IsError=true 的 result，
// client 必须将其视作工具执行错误而非协议错误。
type MCPCallResult struct {
	Content []MCPContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// formatRPCError 把 JSON-RPC 错误对象格式化为可读字符串。
// 供 transport 错误与 server returned error 两条路径共用。
func formatRPCError(code int, message string) string {
	return fmt.Sprintf("MCP server returned error: code=%d message=%s", code, message)
}
