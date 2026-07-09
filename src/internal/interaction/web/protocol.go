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
	// MsgTypeListProjectDir requests one project directory level for the WebUI file panel.
	MsgTypeListProjectDir = "list_project_dir"
	// MsgTypeReadProjectFile requests a safe read-only project file preview.
	MsgTypeReadProjectFile = "read_project_file"
	// MsgTypePermissionResponse 由前端权限确认对话框触发，携带用户的决策回传后端。
	MsgTypePermissionResponse = "permission_response"
	// MsgTypeSetPermissionMode 由前端「权限模式」下拉切换触发，
	// 携带目标档位（strict/default/permissive）请求后端运行时切换。
	// 后端会更新 Checker.SetMode() 并通过 MsgTypePermissionMode 回推新档位。
	MsgTypeSetPermissionMode = "set_permission_mode"
	// MsgTypeCompact 由前端「压缩」按钮或 /compact 斜杠命令触发，请求后端
	// 立即执行一次手动上下文压缩（第二层摘要，无视余量与熔断）。
	// 服务端通过 MsgTypeCompactionEvent 回推本轮压缩结果。
	MsgTypeCompact = "compact"
	// MsgTypeDump 由前端 /dump 斜杠命令触发，请求后端把当前会话内存中的
	// 完整历史上下文 + System Prompt 快照导出为会话目录下的 dump.json / dump.md。
	// 服务端通过 MsgTypeDumpResult 回推导出结果（含两个文件绝对路径）。
	MsgTypeDump = "dump"
	// MsgTypeListSlashCommands 由前端发起，请求后端返回当前所有可用的
	// slash 命令清单。等价于 ws 建立时的主动推送，供重连后兜底拉取。
	// 服务端通过 MsgTypeSlashCommands 回推命令清单。
	MsgTypeListSlashCommands = "list_slash_commands"
	// MsgTypeListSkills 由前端 /skills 命令触发，请求后端返回当前已加载
	// 的 Skill 列表（按项目级 / 用户级 / 内置级三档分组）。
	// 服务端通过 MsgTypeSkillsList 回推 Skill 清单 payload。
	MsgTypeListSkills = "list_skills"
	// MsgTypeSlashCommand 是 Step 10 引入的「通用 slash 命令执行」协议。
	// Payload 携带 Name（含 "/" 前缀，与后端 slash.Registry 注册名一致）
	// 与可选 Arg（NeedsArg=true 命令的用户输入文本）。
	// 用途：Step 10 Skill 系统的 /<skill-name> 命令没有专属 MsgType，
	// 前端在「下拉选中 / 直接键入」两条路径都通过本协议触发后端 Execute。
	// 与 MsgTypeListSlashCommands 的区别：后者是「返回命令清单」，
	// 本消息是「执行指定命令」；与 /resume 的区别：/resume 因历史原因保留
	// 走 MsgTypeResumeSession 专属路径，其余命令统一走本通用协议。
	// 服务端在 handleSlashCommand 中按 Name 查找 slash.Registry 并调 Execute，
	// 失败时返回 stream_error。
	MsgTypeSlashCommand = "slash_command"
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
	// MsgTypeProjectDir responds to list_project_dir with a stable directory payload.
	MsgTypeProjectDir = "project_dir"
	// MsgTypeProjectFile responds to read_project_file with file metadata/content or reason.
	MsgTypeProjectFile = "project_file"
	// MsgTypePermissionRequest 由后端推送给前端，请求用户确认工具执行权限。
	MsgTypePermissionRequest = "permission_request"
	// MsgTypePermissionMode 由后端推送，告知前端当前权限模式及规则概要。
	MsgTypePermissionMode = "permission_mode"
	// MsgTypeMCPStatus 由后端推送 MCP server 健康状态，前端在状态栏渲染。
	// 连接成功时立刻推送一次，运行期由 mcp 后端按需推送更新。
	MsgTypeMCPStatus = "mcp_status"
	// MsgTypeCompactionEvent 由后端推送，告知前端本轮上下文压缩的结果。
	// 覆盖两层压缩（light/summary），前端据此区分提示强度：
	//   - summary（第二层摘要）：强提示（toast「已将 N 条历史压缩为摘要」）；
	//   - light（第一层工具结果预览化）：轻量感知（状态栏压缩计数/小标记，不打扰）。
	// 自动压缩（每轮 API 请求前）与手动压缩（/compact）共用本消息，Manual 字段区分来源。
	MsgTypeCompactionEvent = "compaction_event"
	// MsgTypeMemoryReviewEvent 由后端推送，告知前端自动记忆回顾的生命周期状态。
	MsgTypeMemoryReviewEvent = "memory_review_event"
	// MsgTypeDumpResult 是 /dump（MsgTypeDump）请求的响应消息。
	// OK=true 时 JSONPath / MDPath 携带两个导出文件的绝对路径；
	// OK=false 时 Err 携带失败原因（busy / no_active_session / dump_failed）。
	MsgTypeDumpResult = "dump_result"
	// MsgTypeSlashCommands 是 list_slash_commands 请求的响应消息 / ws
	// 建立连接时的主动推送消息。Payload 为 SlashCommandsPayload，包含
	// 当前所有可用 slash 命令的元数据（name / description / needs_arg /
	// arg_hint / category）。前端据此渲染「/」候选下拉。
	MsgTypeSlashCommands = "slash_commands"
	// MsgTypeSlashCommandsUpdated 由后端在命令清单发生变化时主动推送，
	// 用于支持运行时动态注册（Step 10 Skill 系统接入后场景）。Payload 形态
	// 与 MsgTypeSlashCommands 一致；前端收到后整体覆盖本地命令清单。
	// 本步骤（Step 9.1）暂不主动触发该消息，通道仅作预留。
	MsgTypeSlashCommandsUpdated = "slash_commands_updated"
	// MsgTypeSkillsList 是 list_skills 请求的响应消息。
	// Payload 为 SkillsListPayload，按项目级 / 用户级 / 内置级三档
	// 分别返回当前已加载的 Skill 元数据，供前端 /skills 模态框渲染。
	MsgTypeSkillsList = "skills_list"
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
	// StatusCompacting 表示正在执行手动上下文压缩（/compact）。前端据此把发送按钮
	// 切为可中断的 Stop（abort_stream 可中断压缩中的 LLM 摘要调用），并禁用输入。
	StatusCompacting = "compacting"
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

// SlashCommandRequest 通用 slash 命令执行请求的 payload（Step 10 引入）。
//
// 字段：
//   - Name:命令名(含 "/" 前缀),与 slash.Registry 内的 Name() 一致;
//   - Arg:命令参数;NeedsArg=false 的命令此字段为空字符串。
//
// 与各业务专属 MsgType 的关系:本协议是「兜底通用协议」,覆盖 Step 10 之后
// 动态注册的命令(Skill 系统、MCP 工具、Step 11 Hook、Step 12 SubAgent 等),
// 无需为每个新命令类型新增 ws 协议;与 /resume 的历史兼容:前端仍走
// MsgTypeResumeSession(避免改动既有协议);/sessions / /skills 等 client 类
// 命令仍走前端本地逻辑(无 ws 消息)。
//
// 行为约定:
//   - Name 为空 → 后端 stream_error(code=invalid_payload);
//   - slash.Registry 中找不到 Name → stream_error(code=slash_command_not_found);
//   - 找到 → 调 cmd.Execute(ctx, conn, arg),执行成功由 Execute 自身负责业务;
//   - Execute 返回 error → 后端 stream_error(code=slash_command_failed)。
type SlashCommandRequest struct {
	Name string `json:"name"`
	Arg  string `json:"arg,omitempty"`
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
	Used          int            `json:"used"`
	Limit         int            `json:"limit"`
	PercentLeft   int            `json:"percent_left"`
	SPTotalTokens int            `json:"sp_total_tokens,omitempty"`
	SPBreakdown   []SPSourceStat `json:"sp_breakdown,omitempty"`
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
	SystemBlocks    []string       `json:"system_blocks"`
	LeadUserMessage string         `json:"lead_user_message"`
	Stats           []SPSourceStat `json:"stats"`
	TotalTokens     int            `json:"total_tokens"`
}

// DumpPayload /dump 斜杠命令请求体（客户端 → 服务端）。
// 当前无入参（导出当前会话全量快照到固定文件名），预留结构体以便后续扩展
// （如指定导出目录、自定义文件名等）。
type DumpPayload struct{}

// DumpResultPayload /dump 导出结果（服务端 → 客户端）。
//   - OK=true：JSONPath / MDPath 为会话目录下 dump.json / dump.md 的绝对路径，
//     前端据 toast 提示用户「已导出到 …」并定位文件。
//   - OK=false：Err 携带失败原因；常见取值 busy（已有请求进行中）、
//     no_active_session（无当前会话）、dump_failed（写盘失败，含根因）。
type DumpResultPayload struct {
	OK        bool   `json:"ok"`
	JSONPath  string `json:"json_path,omitempty"`
	MDPath    string `json:"md_path,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Err       string `json:"err,omitempty"`
}

// SlashCommandInfo 单条 slash 命令的元数据，由后端下发给前端用于渲染候选下拉。
//
// 字段语义：
//   - Name        命令名（含前导 `/`，如 `/new`），候选下拉匹配与发送时的主键。
//   - Description 命令的简短描述，候选下拉中展示在 name 右侧。
//   - NeedsArg    是否需要参数；true 时前端选中后补全到输入框（用户填完按 Enter 提交），
//     false 时前端选中后直接按 commandTypeByName[name] 发送对应 MsgType。
//   - ArgHint     参数提示占位符（如 `<id>`），NeedsArg=false 时为前端忽略的占位空串。
//   - Category    分类标识（session/context/skill/client/debug 等）；
//     约定 `client` 类命令由前端识别后走本地逻辑，不发送 WS 消息。
//
// [Why] 与 tool.Tool 接口的轻量元数据风格保持一致，便于 Step 10 Skill 包
// 通过实现同一接口零成本注册为 slash 命令。结构体放 web 包而非 slash 包
// 是因为它本质上是 web 协议层 payload 类型（前后端共享 JSON Schema）。
type SlashCommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	NeedsArg    bool   `json:"needs_arg"`
	ArgHint     string `json:"arg_hint,omitempty"`
	Category    string `json:"category"`
}

// SlashCommandsPayload slash 命令清单响应（MsgTypeSlashCommands /
// MsgTypeSlashCommandsUpdated 的 payload）。Commands 按 Registry 注册顺序排列，
// 前端据此直接遍历渲染候选下拉，无须额外排序。
//
// 注：本步骤（Step 9.1）不引入新的「客户端 → 服务端」命令发送消息类型；
// 前端执行命令仍沿用现有 MsgTypeNewSession / MsgTypeClearSession /
// MsgTypeCompact / MsgTypeDump / MsgTypeResumeSession 等。本结构体仅用于
// 服务端主动推送命令清单 / 响应 list_slash_commands 请求这一种方向。
type SlashCommandsPayload struct {
	Commands []SlashCommandInfo `json:"commands"`
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

// ListProjectDirPayload requests a project directory by workdir-relative path.
type ListProjectDirPayload struct {
	Path      string `json:"path"`
	RequestID string `json:"request_id,omitempty"`
}

// ProjectDirPayload returns one project directory level or a stable error reason.
type ProjectDirPayload struct {
	OK          bool                `json:"ok"`
	Reason      string              `json:"reason,omitempty"`
	Path        string              `json:"path"`
	ParentPath  string              `json:"parent_path"`
	Breadcrumbs []ProjectBreadcrumb `json:"breadcrumbs"`
	Entries     []ProjectFileEntry  `json:"entries"`
	Truncated   bool                `json:"truncated"`
	RequestID   string              `json:"request_id,omitempty"`
}

// ReadProjectFilePayload requests a project file by workdir-relative path.
type ReadProjectFilePayload struct {
	Path      string `json:"path"`
	RequestID string `json:"request_id,omitempty"`
}

// ProjectFilePayload returns project file metadata/content or a stable error reason.
type ProjectFilePayload struct {
	Found     bool             `json:"found"`
	OK        bool             `json:"ok"`
	Reason    string           `json:"reason,omitempty"`
	File      ProjectFileEntry `json:"file"`
	Content   string           `json:"content,omitempty"`
	RequestID string           `json:"request_id,omitempty"`
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
//   - Loading 表示 MCP 是否正处于后台初始化中（InitializeAll 执行期间）
//
// [Why] MCP 初始化异步化后，WebUI 启动时 MCP 可能尚未握手完成。
// Loading=true 时前端展示"连接中…"loading 态（脉冲圆点），与 servers[]
// 正交：初始化中 servers 通常为空，但快的 server 可能已就绪并出现在列表中，
// 此时 Loading=true + 部分 servers 并存是合法的渐进式语义。
//
// 推送时机：CodePilot 启动完成 + 运行期按需（如某 server 进入 unhealthy 时）
// + MCP 后台初始化就绪（Loading 由 true 翻 false 时主动广播一次）。
type MCPStatusPayload struct {
	Servers        []MCPServerStatus `json:"servers"`
	HealthyCount   int               `json:"healthy_count"`
	UnhealthyCount int               `json:"unhealthy_count"`
	TotalTools     int               `json:"total_tools"`
	Loading        bool              `json:"loading"`
}

// ---------------------------------------------------------------------------
// 上下文压缩 Payload（Step 7 接入主流程）
// ---------------------------------------------------------------------------

// CompactionEventLevel 取值与 memory/context 包的 CompactionLevel 常量字符串一致：
// "none"（未发生压缩）、"light"（仅第一层工具结果预览化）、"summary"（第二层历史摘要化）。
// 刻意用字符串字面量而非常量引用，保持 web 协议层对记忆层的解耦（仅注释说明对齐关系）。
const (
	CompactionLevelNone    = "none"
	CompactionLevelLight   = "light"
	CompactionLevelSummary = "summary"
)

// CompactionEventPayload 后端 → 前端：一轮上下文压缩的结果。
//
// 字段语义（与 memory/context.CompactionResult 对齐）：
//   - Level：本轮生效的最高层级（none/light/summary），前端据此决定提示强度。
//   - LightChanged / SummaryChanged：两层各自是否产生变更。
//   - ReplacedBlocks：第一层本轮替换为预览的工具结果数。
//   - BeforeTokens / AfterTokens：压缩前后历史 token 估算，差值即压缩收益。
//   - Tripped：本轮结束时该会话是否处于熔断态（自动第二层被禁用），前端可展示熔断标识。
//   - Manual：是否由用户手动触发（/compact 或压缩按钮）；自动压缩为 false。
//     前端据此对手动触发的 summary 结果给更强提示（用户主动操作，应明确反馈）。
//   - Err：第二层摘要失败时的错误描述（第一层恒空）。非空时前端可提示「压缩失败」。
type CompactionEventPayload struct {
	Level          string `json:"level"`
	LightChanged   bool   `json:"light_changed"`
	SummaryChanged bool   `json:"summary_changed"`
	ReplacedBlocks int    `json:"replaced_blocks"`
	BeforeTokens   int    `json:"before_tokens"`
	AfterTokens    int    `json:"after_tokens"`
	Tripped        bool   `json:"tripped"`
	Manual         bool   `json:"manual,omitempty"`
	Err            string `json:"err,omitempty"`
}

// MemoryReviewEventPayload 后端 → 前端：自动记忆回顾生命周期事件。
// Status 取值：started / no_decision / completed / error。
type MemoryReviewEventPayload struct {
	ReviewID   string    `json:"review_id"`
	SessionID  string    `json:"session_id,omitempty"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	DurationMs int64     `json:"duration_ms,omitempty"`
	Total      int       `json:"total,omitempty"`
	Applied    int       `json:"applied,omitempty"`
	Err        string    `json:"err,omitempty"`
}

// ---------------------------------------------------------------------------
// Skill 列表 Payload（Step 10 Task 6 接入 /skills 模态框）
// ---------------------------------------------------------------------------

// SkillEntry 单条 Skill 的对外投影元数据（web 包不暴露 *skill.Skill）。
//
// 字段语义：
//   - Name        Skill 唯一标识（与 SKILL.md frontmatter 的 name 一致）
//   - Description 一句话用途（来自 frontmatter）
//   - Source      来源级别字符串：project / user / builtin（与 skill.Source.String() 对齐）
//   - Path        Skill 目录绝对路径；前端用于提示用户「这个 Skill 来自哪里」
//
// 字段顺序与命名刻意保持与前端 app.js 渲染逻辑对应（escapeHTML 防 XSS）。
// web 包定义此结构体而非 skill 包，是为了让 handler 不直接 import skill，
// 通过 main.go 顶层适配器注入 SkillProvider 解耦 import 方向。
type SkillEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Path        string `json:"path"`
}

// SkillsListPayload list_skills 请求的响应 payload。
// 按项目级 / 用户级 / 内置级三档分组，每组按 Skill 注册顺序排列。
// 三档数组均可为空（零 Skill 启动时三档均返回空数组，前端展示「暂无 Skill」）。
type SkillsListPayload struct {
	Project []SkillEntry `json:"project"`
	User    []SkillEntry `json:"user"`
	Builtin []SkillEntry `json:"builtin"`
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
