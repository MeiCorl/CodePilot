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
	// MsgTypeDevExportSP 由前端的「开发者模式 → Export SP」按钮触发。
	// 服务端响应同类型消息，payload 包含完整 SP 结构。
	MsgTypeDevExportSP = "dev_export_sp"
	// MsgTypeGetFileDiff 由前端「查看改动」按钮触发，请求指定 tool_use_id
	// 对应的 WriteFile / EditFile 工具调用的文件 diff（before/after）。
	// 服务端响应 MsgTypeFileDiff 同名字段。
	MsgTypeGetFileDiff = "get_file_diff"
	// MsgTypePermissionResponse 由前端权限确认对话框触发，携带用户的决策回传后端。
	MsgTypePermissionResponse = "permission_response"
	// MsgTypeSetPermissionMode 由前端「权限模式」下拉切换触发，
	// 携带目标档位（strict/default/permissive）请求后端运行时切换。
	// 后端会更新 Checker.SetMode() 并通过 MsgTypePermissionMode 回推新档位。
	MsgTypeSetPermissionMode = "set_permission_mode"
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
	MsgTypeToolCallStart  = "tool_call_start"
	MsgTypeToolCallEnd    = "tool_call_end"
	MsgTypeAgentIteration = "agent_iteration"
	// MsgTypeFileDiff 是 get_file_diff 请求的响应消息。
	// Found=false 时 Reason 标识原因（"not_found" / "too_large"），Before/After 为空。
	MsgTypeFileDiff = "file_diff"
	// MsgTypePermissionRequest 由后端推送给前端，请求用户确认工具执行权限。
	MsgTypePermissionRequest = "permission_request"
	// MsgTypePermissionMode 由后端推送，告知前端当前权限模式及规则概要。
	MsgTypePermissionMode = "permission_mode"
	// MsgTypeMCPStatus 由后端推送 MCP server 健康状态，前端在状态栏渲染。
	// 连接成功时立刻推送一次，运行期由 mcp 后端按需推送更新。
	MsgTypeMCPStatus = "mcp_status"
)

// 流式结束原因与 Agent 状态的取值常量。
const (
	StreamReasonCompleted       = "completed"
	StreamReasonAborted         = "aborted"
	StreamReasonError           = "error"
	StreamReasonMaxIterations   = "max_iterations"
	StreamReasonContextOverflow = "context_overflow"

	StatusIdle        = "idle"
	StatusThinking    = "thinking"
	StatusToolRunning = "tool_running"
	StatusError       = "error"
)

// 工具执行结束事件的 status 取值。
// 与 conversation.ToolEventStatus* 一一对应，前端据此区分完成 / 失败 / 取消 / 超时。
const (
	ToolCallStatusRunning   = "running"
	ToolCallStatusCompleted = "completed"
	ToolCallStatusError     = "error"
	ToolCallStatusAborted   = "aborted"
	ToolCallStatusTimeout   = "timeout"
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
//
// 普通消息 Role + Content 即可；工具消息则把"参数/输出/状态"打包到 ToolCall 字段，
// Role 仍记为 assistant（tool_use 来自 assistant，tool_result 来自 user；这里
// 仅展示 LLM 视角的工具调用与结果，因此全部作为 assistant 的附属事件）。
type ChatMessage struct {
	Role     string           `json:"role"`
	Content  string           `json:"content"`
	ToolCall *ToolCallDisplay `json:"tool_call,omitempty"`
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

// ToolCallStartPayload 工具调用开始事件。
// 由 ToolHandler.OnStart 回调透传；Input 为 LLM 传入的原始 JSON 参数。
//
// Step 8 接入 MCP：Server 字段标识工具的远端来源（`mcp__<server>__<tool>`
// 命名时填 server 名，内置工具或命名不含 mcp__ 前缀的工具为空字符串）。
// 前端在工具块头部用紫色徽标 `mcp: <server>` 展示，让用户清楚知道是
// 本地工具还是远端 MCP server 提供的。
type ToolCallStartPayload struct {
	ToolUseID string          `json:"tool_use_id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	StartedAt time.Time       `json:"started_at"`
	// Server 为 MCP server 名称（远端工具时填 mcp server 的 name，内置工具时为空）。
	Server string `json:"server,omitempty"`
}

// ToolCallEndPayload 工具调用结束事件。
// 由 ToolHandler.OnEnd 回调透传；Output 为已截断（≤500 字符）的结果摘要。
// Status 取值：completed / error / aborted / timeout。
//
// Step 8 接入 MCP：Server 字段语义与 ToolCallStartPayload.Server 一致，
// 前端 updateToolEndNode 据此保证徽标在 end 后仍存在（end 消息重建时
// 不应丢失 server 信息）。
type ToolCallEndPayload struct {
	ToolUseID  string `json:"tool_use_id"`
	Name       string `json:"name"`
	Output     string `json:"output"`
	IsError    bool   `json:"is_error"`
	DurationMs int64  `json:"duration_ms"`
	Status     string `json:"status"`
	// Server 为 MCP server 名称（远端工具时填 mcp server 的 name，内置工具时为空）。
	Server string `json:"server,omitempty"`
}

// ToolCallDisplay 用于 session_loaded 中携带工具消息的完整展示数据。
// Input / Output 均为已截断的字符串（不再是 RawMessage），方便前端直接渲染。
// 持久化会话中恢复工具消息时使用该结构（区别于实时 tool_call_start/end）。
//
// Step 8 接入 MCP：Server 字段持久化在 session JSON 中，会话恢复时仍能
// 展示 server 来源徽标。Server 仅在 Name 符合 `mcp__<server>__<tool>`
// 命名时填充（避免历史会话中已存在的 mcp__ 工具块丢失来源信息）。
type ToolCallDisplay struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Input      string `json:"input"`
	Output     string `json:"output"`
	IsError    bool   `json:"is_error"`
	DurationMs int64  `json:"duration_ms"`
	Status     string `json:"status"`
	// Server 为 MCP server 名称（远端工具时填 mcp server 的 name，内置工具时为空）。
	Server string `json:"server,omitempty"`
}

// ContextUsagePayload 上下文窗口使用情况，PercentLeft 范围 0~100。
//
// Step 4 起新增可观测性字段：
//   - SPTotalTokens: 当前 System Prompt 的 token 估算总和
//   - SPBreakdown: 按 Source 拆分的小计（顺序与 Builder 注册顺序一致）
//
// 前端「状态栏 SP 区域」直接渲染这两个字段；鼠标悬停展示各 Source 小计。
type ContextUsagePayload struct {
	Used           int            `json:"used"`
	Limit          int            `json:"limit"`
	PercentLeft    int            `json:"percent_left"`
	SPTotalTokens  int            `json:"sp_total_tokens,omitempty"`
	SPBreakdown    []SPSourceStat `json:"sp_breakdown,omitempty"`
}

// SPSourceStat 描述单个 Source 在 System Prompt 中的 token 开销。
// 与 llm.SourceStat 同构；放 web 包独立类型以避免 llm 包依赖传导到前端协议。
type SPSourceStat struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
}

// DevExportSPPayload 响应前端「Export SP」请求的完整 System Prompt 快照。
// SystemBlocks 为多段 system 文本（顺序与 Source 注册顺序一致）；
// LeadUserMessage 为合并后的首条 user 消息；
// Stats / TotalTokens 与 context_usage 中携带的 SP 信息一致，
// 仅 TotalTokens 是精确累加值（与 Stats 求和一致）。
type DevExportSPPayload struct {
	SystemBlocks    []string `json:"system_blocks"`
	LeadUserMessage string   `json:"lead_user_message"`
	Stats           []SPSourceStat `json:"stats"`
	TotalTokens     int      `json:"total_tokens"`
}

// GetFileDiffPayload 文件 diff 查询请求（客户端 → 服务端）。
// 工具侧按 tool_use_id 索引到对应 FileDiff；找不到时服务端回 found=false。
type GetFileDiffPayload struct {
	ToolUseID string `json:"tool_use_id"`
}

// FileDiffPayload 文件 diff 查询响应（服务端 → 客户端）。
// Found=false 时 Reason 必填（"not_found" / "too_large"），FilePath / Language /
// Before / After 均为空，避免前端误以为存在数据。
// Found=true 时 Reason 必须为空，Before / After 为对应文件改动前后的全文。
type FileDiffPayload struct {
	ToolUseID string `json:"tool_use_id"`
	Found     bool   `json:"found"`
	Reason    string `json:"reason,omitempty"`
	FilePath  string `json:"file_path,omitempty"`
	Language  string `json:"language,omitempty"`
	Before    string `json:"before,omitempty"`
	After     string `json:"after,omitempty"`
}

// AgentIterationPayload Agent Loop 迭代进度事件。
// 每轮迭代开始时推送，前端可据此展示"第 N 轮 / 共 M 轮"的进度指示。
type AgentIterationPayload struct {
	// Current 为当前迭代序号（从 1 开始）
	Current int `json:"current"`
	// Max 为最大迭代次数
	Max int `json:"max"`
}

// ---------------------------------------------------------------------------
// HITL 权限确认相关 Payload
// ---------------------------------------------------------------------------

// PermissionRequestPayload 后端 → 前端：请求用户确认工具执行权限。
// 后端在 Agent Loop 中遇到需要用户确认的工具调用时发送此消息，
// 前端弹出权限确认对话框，用户操作后发送 permission_response 回传。
type PermissionRequestPayload struct {
	// ID 为本次权限确认请求的唯一标识，用于与 response 配对。
	ID string `json:"id"`
	// ToolName 为待确认的工具名（大驼峰，如 Bash / WriteFile）。
	ToolName string `json:"tool_name"`
	// ParamsSummary 为工具参数的可读摘要（如 "command: git push origin main"）。
	ParamsSummary string `json:"params_summary"`
	// Reason 为触发确认的原因说明。
	Reason string `json:"reason"`
	// MatchedRule 为命中的规则信息（可能为 nil，表示档位默认策略触发）。
	MatchedRule *PermissionMatchedRule `json:"matched_rule,omitempty"`
	// TargetPath 为路径类工具触发确认时的目标路径（原始输入）。
	// 前端用于"目标路径"独立一栏展示；用户选"永久允许"时由后端
	// 据此生成目录级 glob Pattern（父目录 + /*）。
	TargetPath string `json:"target_path,omitempty"`
	// Workdir 为当前工作目录绝对路径，前端展示用。
	Workdir string `json:"workdir,omitempty"`
}

// PermissionMatchedRule 权限请求中携带的匹配规则信息，供前端展示。
type PermissionMatchedRule struct {
	Tool    string `json:"tool"`
	Pattern string `json:"pattern"`
	Action  string `json:"action"`
}

// PermissionResponsePayload 前端 → 后端：用户对权限确认请求的响应。
type PermissionResponsePayload struct {
	// ID 对应 permission_request 的 ID。
	ID string `json:"id"`
	// Allowed 表示用户是否允许本次操作。
	Allowed bool `json:"allowed"`
	// Scope 为授权范围：once（仅本次）/ session（本会话）/ permanent（永久）。
	Scope string `json:"scope"`
}

// PermissionModePayload 后端 → 前端：告知当前权限模式及规则概要。
// 在会话启动时和权限模式变更时推送，前端据此更新状态栏展示。
type PermissionModePayload struct {
	// Mode 为当前权限模式：strict / default / permissive。
	Mode string `json:"mode"`
	// RuleCount 为配置级规则数量。
	RuleCount int `json:"rule_count"`
	// SessionRuleCount 为会话级临时规则数量。
	SessionRuleCount int `json:"session_rule_count"`
}

// SetPermissionModePayload 前端 → 后端：用户通过「权限模式」下拉请求切换档位。
// Mode 必须是合法档位（strict/default/permissive），非法值后端忽略并保持原档位。
// 切换成功后后端通过 MsgTypePermissionMode 回推新档位，前端无需本地更新。
type SetPermissionModePayload struct {
	// Mode 为目标档位。
	Mode string `json:"mode"`
}

// ---------------------------------------------------------------------------
// MCP 健康状态 Payload（Step 8 接入主流程）
// ---------------------------------------------------------------------------

// MCPHealthState 描述单个 MCP server 的连接状态。
//
// 状态机:
//   - healthy   transport 健康、握手已完成、远端工具已注册
//   - reconnecting 正在按指数退避重连
//   - unhealthy 启动失败或重连耗尽，需重启 CodePilot
//   - skipped   配置阶段即被跳过（如 disabled=true、type 非法）
type MCPHealthState string

const (
	// MCPHealthHealthy server 健康，所有远端工具可用。
	MCPHealthHealthy MCPHealthState = "healthy"
	// MCPHealthReconnecting server 正在重连（指数退避 1s/3s/9s）。
	MCPHealthReconnecting MCPHealthState = "reconnecting"
	// MCPHealthUnhealthy server 永久不可用。
	MCPHealthUnhealthy MCPHealthState = "unhealthy"
	// MCPSkipped 配置阶段被跳过（如 disabled=true）。
	MCPSkipped MCPHealthState = "skipped"
)

// MCPServerStatus 单个 MCP server 的健康状态描述。
//
// Fields:
//   - Name      server 唯一标识
//   - State     健康状态
//   - Tools     当前已注册的远端工具数量（healthy 时有值，unhealthy 时为 0）
//   - Reason    unhealthy / skipped 时的原因说明（healthy 时为空）
type MCPServerStatus struct {
	Name   string         `json:"name"`
	State  MCPHealthState `json:"state"`
	Tools  int            `json:"tools"`
	Reason string         `json:"reason,omitempty"`
}

// MCPStatusPayload 后端 → 前端：MCP pool 整体健康状态。
//
// 前端据此更新状态栏 MCP 区：
//   - Servers 为所有已知 server 列表（含 unhealthy / skipped）
//   - HealthyCount / UnhealthyCount 便于快速展示汇总数字
//   - TotalTools 所有 healthy server 暴露的工具数总和
//
// 推送时机：CodePilot 启动完成 + 运行期按需（如某 server 进入 unhealthy 时）。
type MCPStatusPayload struct {
	Servers        []MCPServerStatus `json:"servers"`
	HealthyCount   int               `json:"healthy_count"`
	UnhealthyCount int               `json:"unhealthy_count"`
	TotalTools     int               `json:"total_tools"`
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
