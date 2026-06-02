package web

import (
	"encoding/json"
	"fmt"
	"time"
)

// 客户端 → 服务端 消息类型常量。
const (
	MsgTypeUserInput         = "user_input"
	MsgTypeListSessions      = "list_sessions"
	MsgTypeNewSession        = "new_session"
	MsgTypeResumeSession     = "resume_session"
	MsgTypeAbortStream       = "abort_stream"
	MsgTypeGetCurrentSession = "get_current_session"
	MsgTypeClearSession      = "clear_session"
	MsgTypeDeleteSession     = "delete_session"
)

// 服务端 → 客户端 消息类型常量。
const (
	MsgTypeStreamChunk    = "stream_chunk"
	MsgTypeStreamDone     = "stream_done"
	MsgTypeStreamError    = "stream_error"
	MsgTypeSessionList    = "session_list"
	MsgTypeSessionLoaded  = "session_loaded"
	MsgTypeSessionDeleted = "session_deleted"
	MsgTypeStatusUpdate   = "status_update"
	MsgTypeContextUsage   = "context_usage"
)

// 流式结束原因与 Agent 状态的取值常量。
const (
	StreamReasonCompleted = "completed"
	StreamReasonAborted   = "aborted"
	StreamReasonError     = "error"

	StatusIdle     = "idle"
	StatusThinking = "thinking"
	StatusError    = "error"
)

// Message 通用消息信封。所有 WebSocket 业务消息均使用此格式。
// Payload 使用 json.RawMessage 以延迟具体类型的解码，由 handler 按需解析。
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// UserInputPayload 用户输入文本。
type UserInputPayload struct {
	Text string `json:"text"`
}

// ResumeSessionPayload 恢复会话请求，ID 支持前缀匹配。
type ResumeSessionPayload struct {
	ID string `json:"id"`
}

// DeleteSessionPayload 删除指定会话请求。ID 必须为完整的会话 ID（前端拿到的列表数据带完整 ID）。
type DeleteSessionPayload struct {
	ID string `json:"id"`
}

// SessionDeletedPayload 删除完成响应。
// DeletedID 为被删除的会话 ID；CurrentChanged 表示当前激活会话是否因删除发生切换，
// 若发生切换，服务端在本消息之后会再发一条 session_loaded 把新会话推给前端。
type SessionDeletedPayload struct {
	DeletedID      string `json:"deleted_id"`
	CurrentChanged bool   `json:"current_changed"`
}

// StreamChunkPayload 流式输出片段。
type StreamChunkPayload struct {
	Delta string `json:"delta"`
}

// StreamDonePayload 流式完成通知，Reason 标识结束原因。
type StreamDonePayload struct {
	Reason string `json:"reason"`
}

// StreamErrorPayload 流式错误或消息层错误。
type StreamErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SessionSummary 会话摘要，用于会话列表展示。
// CreatedAt 暴露给前端用于「按创建时间倒序」的表格视图。
type SessionSummary struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
	Preview      string    `json:"preview"`
}

// ListSessionsPayload 列出历史会话请求。
// Mode 决定返回结果的数据形态：
//   - "table"：按 CreatedAt 降序、取最近 10 条（用于 /sessions 命令的表格视图）
//   - "" 或缺省：按 UpdatedAt 降序、返回全部（用于侧边栏刷新与 /resume 前缀匹配）
type ListSessionsPayload struct {
	Mode string `json:"mode,omitempty"`
}

// SessionListPayload 会话列表响应，按 UpdatedAt 降序。
type SessionListPayload struct {
	Sessions []SessionSummary `json:"sessions"`
}

// ChatMessage 前端可渲染的会话消息条目。
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SessionLoadedPayload 加载会话成功响应。
// Model 为后端当前配置使用的模型名，便于前端一次性把状态栏更新到正确值。
// Workdir 为 CodePilot 启动时所在的工作目录，前端用于顶栏展示。
type SessionLoadedPayload struct {
	SessionID string         `json:"session_id"`
	Summary   SessionSummary `json:"summary"`
	Messages  []ChatMessage  `json:"messages"`
	Model     string         `json:"model,omitempty"`
	Workdir   string         `json:"workdir,omitempty"`
}

// StatusUpdatePayload Agent 状态更新。
type StatusUpdatePayload struct {
	Status string `json:"status"`
}

// ContextUsagePayload 上下文窗口使用情况，PercentLeft 范围 0~100。
type ContextUsagePayload struct {
	Used        int `json:"used"`
	Limit       int `json:"limit"`
	PercentLeft int `json:"percent_left"`
}

// Encode 编码消息为 JSON 字节。
func Encode(msg Message) ([]byte, error) {
	if msg.Type == "" {
		return nil, fmt.Errorf("消息类型不能为空")
	}
	return json.Marshal(msg)
}

// Decode 解码 JSON 字节为 Message 信封。type 字段为空时返回错误。
func Decode(data []byte) (Message, error) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return Message{}, fmt.Errorf("解码 WebSocket 消息失败: %w", err)
	}
	if msg.Type == "" {
		return Message{}, fmt.Errorf("消息 type 字段不能为空")
	}
	return msg, nil
}

// EncodePayload 构造并编码一条带 payload 的消息。
// payload 传 nil 时序列化结果为 JSON null。
func EncodePayload(typ string, payload any) ([]byte, error) {
	if typ == "" {
		return nil, fmt.Errorf("消息类型不能为空")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("编码 %s payload 失败: %w", typ, err)
	}
	return json.Marshal(Message{
		Type:    typ,
		Payload: raw,
	})
}

// AsPayload 把 Message.Payload 解码为指定类型。
// handler 可通过类型推导直接拿到具体 payload；msg.Payload 为空时返回零值。
func AsPayload[T any](msg Message) (T, error) {
	var p T
	if len(msg.Payload) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return p, fmt.Errorf("解码 %s payload 失败: %w", msg.Type, err)
	}
	return p, nil
}
