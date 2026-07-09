package web

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/engine/conversation"
	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt"
	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/sources"
	"github.com/MeiCorl/CodePilot/src/internal/hook"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/mcp/adapter"
	mcpsession "github.com/MeiCorl/CodePilot/src/internal/mcp/session"
	"github.com/MeiCorl/CodePilot/src/internal/memory/autolearn"
	memctx "github.com/MeiCorl/CodePilot/src/internal/memory/context"
	memsession "github.com/MeiCorl/CodePilot/src/internal/memory/session"
	"github.com/MeiCorl/CodePilot/src/internal/security"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
	"github.com/MeiCorl/CodePilot/src/llm"
)

// mcpServerByName 缓存 MCP 远端工具名 → server 名的映射(由 mcpPool 在 SetMCPPool 时填充)。
// 解析远端工具的 server 来源不依赖 adapter(避免 web → mcp 内部细节泄漏),只
// 依赖 adapter.ToolNamePrefix / nameSeparator 拆分命名。
var _ = adapter.ToolNamePrefix // 保留 import(防止未来误删导致 server 解析静默失败)

// defaultContextWindowSize 为 Handler 层的兜底默认值。
// 正常情况下 ContextWindowSize 由 Config 层（setting.json）提供并通过 main.go 传入，
// 此常量仅在传入值 <= 0 时作为安全回退。
const defaultContextWindowSize = 200000

// SlashCommandEntry 是 handler 层从 slash 注册表读取单条命令的最小投影。
//
// [设计动机] spec 要求 slash 包不依赖 web 包；反过来 web 包需要消费 slash 的
// 数据。两个方向不能同时用具体类型直接 import（否则 import cycle）。
//
// 解决方案：web 包定义一个面向自身的最小投影接口 SlashCommandProvider +
// SlashCommandEntry；slash.Registry 由 main.go 适配为该接口后注入 handler。
// 这样 web 包只知道"拿一个命令列表"，不感知 slash 包的存在。
type SlashCommandEntry struct {
	Name        string
	Description string
	NeedsArg    bool
	ArgHint     string
	Category    string
}

// SlashCommandProvider 是 web 层消费 slash 命令注册表所需的最小接口。
// 实现方（典型为 *slash.Registry，由 main.go 顶层适配注入）必须保证：
//   - List 返回当前所有已注册命令（注册顺序）
//   - OnChange 注册变化回调；注册后**立刻**同步触发一次（用于让 handler 在
//     注入后就能感知到既有 6 条内置命令），后续命令增删时也会被触发；
//     回调在 web 层 goroutine 中同步执行，handler 应避免在回调内做耗时操作；
//   - Execute 按 Name 查找命令并执行（Step 10 引入，覆盖 Skill 系统等无专属
//     MsgType 的命令；web 包不直接 import command/slash，由 main.go 适配为
//     registry.Get + cmd.Execute 的实现）。
type SlashCommandProvider interface {
	List() []SlashCommandEntry
	OnChange(fn func())
	// Execute 按 name 查找 slash 命令并 Execute(ctx, conn, arg)。
	// name 含 "/" 前缀；arg 在 NeedsArg=false 的命令下为空字符串。
	// 返回 error 时由 handler 包装为 stream_error。
	Execute(ctx context.Context, conn *websocket.Conn, name, arg string) error
}

// SkillProvider 是 web 层消费 Skill 注册表所需的最小接口（Step 10 Task 6）。
//
// [Why 独立接口] 与 SlashCommandProvider 同构的"最小投影"模式：
//   - web 包不直接 import skill 包（避免 import cycle 与分层倒挂）；
//   - 实现方（典型为 main.go 顶层 skillProviderAdapter 包装 *skill.Registry）
//     必须把内部 *skill.Skill 投影为 web.SkillEntry，handler 只与 SkillEntry 交互；
//   - List 返回全量扁平列表（按 Registry 注册顺序），ListBySource 按 source
//     字符串（"project" / "user" / "builtin"）分组返回 SkillEntry，供 handleSkills
//     直接构造 SkillsListPayload 三档数组。
//
// 接口的 2 个方法保持最小化：handler 端只需要「按 source 分组」与「全部」两种读取，
// 既支持 /skills 模态框三档 tab 渲染，也支持未来按需推送 Skill 数量等聚合。
type SkillProvider interface {
	// List 返回所有已加载 Skill 的扁平列表（按 source 分组后展平）。
	// 通常用于聚合统计（如状态栏 skills_count）；为 nil 时返回 nil 切片。
	List() []SkillEntry
	// ListBySource 按 source 字符串（"project" / "user" / "builtin"）返回该档
	// 下的 Skill 列表，按 Registry 注册顺序。未识别的 source 返回 nil 切片。
	ListBySource(source string) []SkillEntry
}

// Handler 持有所有业务依赖并把 WebSocket 消息路由到具体业务能力。
// 它维护"当前活跃会话"状态（current Session + ConversationManager），
// 并通过 streamState 状态机保证同一时刻只有一个流式请求进行中。
// workdir 记录 CodePilot 启动时所在的工作目录，仅在 session_loaded 消息中透传至前端展示。
//
// Step 2 在此基础上接入 conversation.RunTurn + ToolHandler：
//   - registry 持有工具描述源，runStream 每次发起 LLM 时按 cfg.Tools.Enabled
//     过滤后转 []tool.ToolSpec 注入 Provider；
//   - toolHandler 负责 LLM 触发的 tool_use 实际执行，并通过 OnStart/OnEnd
//     把开始/结束事件外推为 tool_call_start / tool_call_end WebSocket 消息。
//
// Step 4 在此基础上接入 prompt.Builder：
//   - sp 字段缓存当前活跃会话对应的 llm.SystemPrompt 组装结果（同一会话内不变）
//   - promptBuilder 持有 Builder 实例；assembleSP 每次「切换会话」时调用一次并刷新缓存
//   - runStream 透传 sp 给 Provider.streamChat，Anthropic 协议据此打 cache_control
type Handler struct {
	provider          llm.Provider
	sessMgr           *memsession.SessionManager
	cfg               *config.Config
	conv              *conversation.ConversationManager
	promptBuilder     *prompt.Builder
	sp                llm.SystemPrompt
	contextWindowSize int
	workdir           string
	registry          *tool.Registry
	toolHandler       *conversation.ToolHandler
	// hookEngine 为 Hook 系统派发器。nil 时所有 Hook 集成点降级为 no-op。
	hookEngine *hook.Engine
	// fileDiffStore 是 WriteFile/EditFile 工具写入的 diff 数据存储。
	// Step 1.4 接入；Task 3 (get_file_diff 协议) 真正消费此字段。
	// 为 nil 时前端请求 diff 会得到 not_found 提示，等价于"未启用 diff 预览"。
	fileDiffStore *FileDiffStore

	// slashProvider 是 slash 命令注册表的最小投影（Step 9.1 Task 4 接入）。
	// 通过 SlashCommandProvider 接口抽象而非直接 import command/slash，
	// 避免 web → slash → web 的循环依赖（slash.builtin 内置命令实现当前
	// 依赖 *web.Handler 进行 Execute 委托，web 包不能反向 import slash 包）。
	// 为 nil 时前端 list_slash_commands 请求得到空命令清单（等价"未启用 slash"）。
	slashProvider SlashCommandProvider
	// slashCmdMap 是「命令名 → 既有 MsgType」的查找表，仅在 SetSlashRegistry
	// 一次性构造时填充；handler 内部不直接使用（前端按 state.commandTypeByName
	// 自行查找发送），保留是为日志/调试可见「已注册命令 → 目标协议」的映射关系。
	slashCmdMap map[string]string

	// compactor 为上下文压缩协调器（Step 7）。nil 表示压缩总开关关闭——此时
	// /compact 返回 compaction_disabled，自动压缩在 manager 侧见 nil 直接跳过。
	// 由 main.go 顶层装配后通过 SetCompactor 注入（同时转发给 ConversationManager）。
	compactor *memctx.Compactor

	// toolResultStore 为第一层压缩的工具结果存盘器（Step 7）。供 handleClearSession 在
	// /clear 时清理落盘的工具结果归档目录；为 nil 时（未注入）清理跳过，不影响清空消息。
	// 由 main.go 顶层装配后通过 SetToolResultStore 注入（与压缩总开关解耦：即便 compaction
	// 关闭，handler 仍持有 store 以清理残留的 tool_results）。
	toolResultStore *memctx.ToolResultStore

	// reviewer 为自动学习记忆的后台异步回顾器（Step 8）。nil 表示记忆总开关关闭或未注入——
	// 此时 OnLoopDone 不触发任何回顾。由 main.go 顶层装配后通过 SetReviewer 注入；
	// 回顾器内部对 provider/store/Enabled 做 nil/短路判断，故 OnLoopDone 调用方仅需判
	// h.reviewer != nil 即可，无需感知内部依赖。
	reviewer *autolearn.Reviewer

	// interceptor 为权限拦截器；为 nil 时 ToolHandler 不做权限检查。
	interceptor *security.Interceptor
	// checker 持有权限检查器引用，用于查询当前模式和规则数量。
	checker *security.Checker
	// pendingPermissions 管理等待用户确认的权限请求。
	// key 为请求 ID，value 为等待响应的 channel。
	pendingPermissions map[string]chan security.PermissionResponse
	// pendingMu 保护 pendingPermissions 的并发访问。
	pendingMu sync.Mutex
	// pendingConn 追踪当前 runStream goroutine 使用的 WebSocket 连接，
	// 供 HITL 回调获取连接使用。在 runStream 启动时设置，退出时置 nil。
	pendingConn *websocket.Conn

	mu      sync.Mutex
	current *memsession.Session
	// persistedMsgCount 记录当前会话已落盘到 messages.jsonl 的消息数。
	// saveCurrentSessionLocked 据此仅追加 history[persistedMsgCount:] 这批新消息，
	// 实现 append-only 增量持久化（避免每次全量重写历史）。
	// 所有读写均在持有 h.mu 的临界区内进行，与 current 配套维护。
	persistedMsgCount int

	// writeMu 保护 WebSocket 写操作的互斥锁。
	// gorilla/websocket 要求同一时刻只有一个 writer；Handler 的读循环 goroutine
	// （HandleLoop）和流式输出 goroutine（runStream）都会向 conn 写消息，
	// 必须通过 writeMu 串行化以避免 "concurrent write to websocket connection" panic。
	//
	// [强制约束] 所有向 *websocket.Conn 的写入都必须经 sendMessage（即持有 writeMu）。
	// 禁止在 Handler 内部任何位置直接调用裸 conn.WriteMessage 或 ConnectionManager.Broadcast
	// （后者不经 writeMu）——一旦与 sendMessage 混用，同一 conn 上会触发并发写 panic。
	// 需要向多个连接推送时，用 connMgr.Snapshot() 取连接后逐个调 sendMessage。
	writeMu sync.Mutex

	stream streamState

	// mcpPool 是 MCP server 连接池（Step 8）。nil 时 MCP 相关消息跳过。
	// 提供两个能力：
	//   1. 通过 mcpPool.HealthyNames() 等查询健康状态,填充 mcp_status payload
	//   2. 通过 mcp/adapter adapter 命名规则(`mcp__<server>__<tool>`)解析
	//      远端工具的 server 名称,填入 ToolCallStartPayload.Server
	mcpPool *mcpsession.Pool
	// connMgr 持有 WebSocket 连接管理器引用，供 BroadcastMCPStatus 向所有活跃连接
	// 推送 MCP 状态（如后台初始化就绪后）。
	// [Why] MCP 初始化异步化后，就绪时刻不固定，可能发生在任意 idle 状态（此时
	// pendingConn 为 nil）。需要一个不依赖 pendingConn 的广播入口。
	// 构造期一次性注入（main.go 在 server 构造后调 SetConnMgr），其后只读，无需加锁。
	// 为 nil 时 BroadcastMCPStatus 退化为 no-op（兼容未注入场景与测试）。
	connMgr *ConnectionManager

	// skillProvider 是 Skill 注册表的最小投影（Step 10 Task 6）。
	// 通过 SkillProvider 接口抽象而非直接 import skill 包，与 SlashCommandProvider
	// 同构：web 包不感知 skill 包内部实现，由 main.go 顶层适配器注入。
	// 为 nil 时 list_skills 请求回推空 payload（项目级/用户级/内置级三组均为空数组），
	// 前端 /skills 模态框展示「暂无 Skill」空状态。
	skillProvider SkillProvider
}

// NewHandler 构造 Handler。
// 构造时会尝试 sessMgr.LoadLatest() 恢复最近会话；无历史时创建新会话（不立即落盘）。
// workdir 启动时获取，会随 session_loaded 透传给前端。
// registry 为 nil 时 RunTurn 不会携带任何工具描述（与未启用工具等价）；
// toolHandler 为 nil 时 RunTurn 仍可工作（无 tool_use 分发能力，LLM 不会调工具）。
// promptBuilder 负责 System Prompt 的组装；为 nil 时降级为空 SP（不构造 system、首条 user 消息）。
// fileDiffStore 为 nil 时前端"查看改动"按钮点击会得到 not_found 提示，工具仍正常工作。
func NewHandler(
	provider llm.Provider,
	sessMgr *memsession.SessionManager,
	cfg *config.Config,
	maxRounds int,
	promptBuilder *prompt.Builder,
	contextWindowSize int,
	workdir string,
	registry *tool.Registry,
	toolHandler *conversation.ToolHandler,
	fileDiffStore *FileDiffStore,
) *Handler {
	if contextWindowSize <= 0 {
		contextWindowSize = defaultContextWindowSize
	}
	h := &Handler{
		provider:           provider,
		sessMgr:            sessMgr,
		cfg:                cfg,
		conv:               conversation.NewConversationManager(maxRounds),
		promptBuilder:      promptBuilder,
		contextWindowSize:  contextWindowSize,
		workdir:            workdir,
		registry:           registry,
		toolHandler:        toolHandler,
		fileDiffStore:      fileDiffStore,
		pendingPermissions: make(map[string]chan security.PermissionResponse),
	}
	// 尝试恢复最近一个会话
	if latest, err := sessMgr.LoadLatest(); err == nil && latest != nil {
		h.current = latest
		h.conv.Reset(latest.Messages)
		// 恢复的消息已在磁盘上，已落盘计数对齐到其长度
		h.persistedMsgCount = len(latest.Messages)
		logger.Info("Handler 已恢复最近会话",
			zap.String("session_id", latest.ID),
			zap.Int("message_count", len(latest.Messages)),
		)
	} else {
		h.current = sessMgr.CreateNew()
		// 新会话尚未落盘，首次追加消息时惰性创建目录
		h.persistedMsgCount = 0
	}
	// 初次组装 System Prompt（同一会话内复用，避免每次 LLM 调用都重新 assemble）
	h.assembleSP()
	// Step 7：注入 contextWindowSize（协调器 Remaining() 计算依赖）与当前会话 sessionID
	// （压缩存盘子目录归属 + 熔断状态隔离）。sessionID 在每次会话切换时由对应 handler 刷新。
	h.conv.SetContextWindowSize(contextWindowSize)
	if h.current != nil {
		h.conv.SetSessionID(h.current.ID)
		// 打开当前会话专属日志器：启动后核心链路日志即写入该会话目录的 codepilot.log。
		h.openSessionLogger(h.current.ID)
	}
	return h
}

// assembleSP 重新调用 prompt.Builder.Assemble 并把结果缓存到 h.sp。
//
// 调用时机：
//  1. NewHandler 构造时
//  2. handleNewSession 创建新会话后
//  3. handleResumeSession 恢复历史会话后
//  4. handleClearSession 清空当前会话后
//  5. handleDeleteSession 切换当前会话后
//
// 失败时降级为零值 SystemPrompt（不向上抛，避免阻塞会话流程）。
// 同时把 LeadUserMessage 注入到 ConversationManager，确保 runStream 透传。
func (h *Handler) assembleSP() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	env := buildSPEnv(h.cfg, h.workdir)
	if h.promptBuilder == nil {
		// 无 builder：清空 SP 与 lead
		h.sp = llm.SystemPrompt{}
		h.conv.SetLeadUserMessage("")
		return
	}
	sp, err := h.promptBuilder.Assemble(ctx, env)
	if err != nil {
		logger.Warn("System Prompt 组装失败，使用空 SP 降级", zap.Error(err))
		h.sp = llm.SystemPrompt{}
		h.conv.SetLeadUserMessage("")
		return
	}
	// 转换为 llm.SystemPrompt 并缓存
	h.sp = convertToLLMSystemPrompt(sp)
	// 把 LeadUserMessage 注入 ConversationManager，让 GetContext 拼到 messages 最前
	h.conv.SetLeadUserMessage(sp.LeadUserMessage)
}

// Register 把所有业务 handler 注册到给定 router。
func (h *Handler) Register(router *Router) {
	router.Register(MsgTypeUserInput, h.handleUserInput)
	router.Register(MsgTypeListSessions, h.handleListSessions)
	router.Register(MsgTypeNewSession, h.handleNewSession)
	router.Register(MsgTypeResumeSession, h.handleResumeSession)
	router.Register(MsgTypeAbortStream, h.handleAbortStream)
	router.Register(MsgTypeGetCurrentSession, h.handleGetCurrentSession)
	router.Register(MsgTypeClearSession, h.handleClearSession)
	router.Register(MsgTypeDeleteSession, h.handleDeleteSession)
	router.Register(MsgTypeDevExportSP, h.handleDevExportSP)
	router.Register(MsgTypeGetFileDiff, h.handleGetFileDiff)
	router.Register(MsgTypeListProjectDir, h.handleListProjectDir)
	router.Register(MsgTypeReadProjectFile, h.handleReadProjectFile)
	router.Register(MsgTypePermissionResponse, h.handlePermissionResponse)
	router.Register(MsgTypeSetPermissionMode, h.handleSetPermissionMode)
	router.Register(MsgTypeCompact, h.handleCompact)
	router.Register(MsgTypeDump, h.handleDump)
	// Step 9.1 Task 4：响应前端主动拉取命令清单的请求。
	// 等价于 onWSOpen 的推送逻辑，供重连后兜底拉取使用。
	router.Register(MsgTypeListSlashCommands, h.handleListSlashCommands)
	// Step 10 Task 6：响应前端 /skills 命令触发的 list_skills 请求，回推 skills_list payload。
	router.Register(MsgTypeListSkills, h.handleSkills)
	// Step 10：通用 slash 命令执行入口，覆盖 Skill 系统的 /<skill-name> 等无专属
	// MsgType 的命令。handleSlashCommand 按 Name 查找 slash.Registry 后调 Execute。
	router.Register(MsgTypeSlashCommand, h.handleSlashCommand)
}

// ModelName 返回当前配置中的模型名，供状态栏展示。
func (h *Handler) ModelName() string {
	if h.cfg == nil {
		return ""
	}
	return h.cfg.Model
}

// CurrentSessionID 返回当前会话 ID。
func (h *Handler) CurrentSessionID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current == nil {
		return ""
	}
	return h.current.ID
}

// ConversationManager 返回当前 Handler 持有的对话管理器,供 main.go 装配 Hook/测试使用。
func (h *Handler) ConversationManager() *conversation.ConversationManager {
	if h == nil {
		return nil
	}
	return h.conv
}

// SetHookEngine 注入 HookEngine 并转发给 ConversationManager / ToolHandler。
func (h *Handler) SetHookEngine(engine *hook.Engine) {
	h.mu.Lock()
	h.hookEngine = engine
	currentID := ""
	if h.current != nil {
		currentID = h.current.ID
	}
	h.mu.Unlock()
	h.conv.SetHookEngine(engine, h.workdir)
	if h.toolHandler != nil {
		h.toolHandler.SetHookEngine(engine)
		h.toolHandler.SetHookSessionID(currentID)
	}
}

// AppendToCurrentMessage 实现 hook.PromptSink,由 Engine 的 prompt action 调用。
func (h *Handler) AppendToCurrentMessage(text string) error {
	return h.conv.AppendToCurrentMessage(text)
}

// DispatchCurrentSessionStart 在 main.go 完成 wire 后触发启动恢复会话的 session_start。
func (h *Handler) DispatchCurrentSessionStart(ctx context.Context) {
	h.dispatchSessionHook(ctx, hook.EventSessionStart, h.CurrentSessionID())
}

func (h *Handler) dispatchSessionHook(ctx context.Context, event, sessionID string) {
	if h.hookEngine == nil || sessionID == "" {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logger.ErrorCtx(ctx, "session Hook 派发 panic，已恢复", zap.String("event", event), zap.Any("panic", r))
		}
	}()
	h.hookEngine.Dispatch(ctx, event, hook.NewSessionContext(event, sessionID, h.workdir))
}

func (h *Handler) dispatchCompactHook(ctx context.Context, sessionID string, res memctx.CompactionResult) {
	if h.hookEngine == nil || sessionID == "" {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logger.ErrorCtx(ctx, "compact Hook 派发 panic，已恢复", zap.Any("panic", r))
		}
	}()
	h.hookEngine.Dispatch(ctx, hook.EventCompact,
		hook.NewCompactContext(sessionID, h.workdir, string(res.Level), res.BeforeTokens, res.AfterTokens, 0))
}

// ---- 消息 handler ----

// 以下是 Step 9.1 为 slash 命令提供的"零参数版本"包装函数。
//
// 业务逻辑完全复用现有 handleXxx 函数体（handleNewSession / handleClearSession /
// handleResumeSession / handleCompact / handleDump），仅把"从 Message 解析 payload"
// 这一步省掉——slash 命令的入参已知（/resume 的 ID 由命令 Execute 直接传入），无需
// 经过 AsPayload 解析。
//
// 约束：
//   - 原 handleXxx 函数体零改动，保持 Step 9 已落地的 ws 协议兼容
//   - wrapper 命名带 "ForSlash" 后缀，便于在 grep 中与原 handleXxx 区分
//   - 返回 error 仅为满足 slash.SlashCommand.Execute 签名；当前所有 wrapper 实际
//     始终返回 nil（原 handleXxx 的错误已通过 sendStreamError 回推前端）

// HandleNewSessionForSlash 是 handleNewSession 的 slash 命令版本。
// 业务逻辑保持一致：保存当前会话 → 创建新会话 → 重置 ConvMgr → 重置 SP。
func (h *Handler) HandleNewSessionForSlash(conn *websocket.Conn) error {
	return h.handleNewSession(conn, Message{})
}

// HandleClearSessionForSlash 是 handleClearSession 的 slash 命令版本。
// 业务逻辑保持一致：保留 session_id、清空消息、重置 ConvMgr、清空磁盘消息、
// 清理第一层压缩工具结果归档。
func (h *Handler) HandleClearSessionForSlash(conn *websocket.Conn) error {
	return h.handleClearSession(conn, Message{})
}

// HandleResumeSessionForSlash 是 handleResumeSession 的 slash 命令版本。
// id 直接从前端补全提交时传入的 arg 取（NeedsArg=true 的 slash 命令由 Execute
// 把 arg 透传给本函数）；不经过 Message.Payload 解析。
func (h *Handler) HandleResumeSessionForSlash(conn *websocket.Conn, id string) error {
	return h.handleResumeSession(conn, Message{
		Payload: mustMarshalPayload(ResumeSessionPayload{ID: id}),
	})
}

// HandleCompactForSlash 是 handleCompact 的 slash 命令版本。
// 业务逻辑保持一致：compactor 为 nil 时返回 compaction_disabled；busy 时返回 stream_error。
func (h *Handler) HandleCompactForSlash(conn *websocket.Conn) error {
	return h.handleCompact(conn, Message{})
}

// HandleDumpForSlash 是 handleDump 的 slash 命令版本。
// 业务逻辑保持一致：抢占流式状态 → 复制 SP/会话快照 → 落盘 dump.json/dump.md。
func (h *Handler) HandleDumpForSlash(conn *websocket.Conn) error {
	return h.handleDump(conn, Message{})
}

// mustMarshalPayload 把任意 payload 序列化为 json.RawMessage；序列化失败时 panic。
//
// 仅供 Step 9.1 slash wrapper 使用——payload 类型固定为本包定义的 ResumeSessionPayload
// 等结构体，正常序列化必然成功（不会触发网络/IO 错误），用 panic 替代 error 返回值
// 以简化 wrapper 签名。panic 视为编程错误，应在开发阶段捕获。
func mustMarshalPayload(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("slash wrapper: marshal payload failed: %v", err))
	}
	return json.RawMessage(data)
}

// handleUserInput 处理用户输入：把消息加入 ConvMgr，调用 Provider 流式响应，
// 通过 stream_chunk / stream_done / context_usage 等消息回传给客户端。
// 同一时刻已有流式请求时返回 stream_error(busy)。
func (h *Handler) handleUserInput(conn *websocket.Conn, msg Message) error {
	p, err := AsPayload[UserInputPayload](msg)
	if err != nil {
		return h.sendStreamError(conn, "invalid_payload", err.Error())
	}
	if strings.TrimSpace(p.Text) == "" {
		return h.sendStreamError(conn, "empty_input", "用户输入为空")
	}

	// 抢占流式状态
	acquired, busy := h.stream.tryAcquire()
	if busy {
		return h.sendStreamError(conn, "busy", "当前已有流式请求进行中")
	}

	// 把用户消息加入历史
	h.mu.Lock()
	h.conv.AddUserMessage(p.Text)
	h.mu.Unlock()

	// 状态切到 thinking
	_ = h.sendStatusUpdate(conn, StatusThinking)

	// 启动 goroutine 处理流式（传入本轮用户输入，供 Step 8 记忆回顾快照使用）
	go h.runStream(acquired, conn, p.Text)

	return nil
}

// runStream 是流式响应的核心 goroutine（Step 3: 接入 AgentLoop + ToolHandler）。
//
// 调用流程：
//  1. 构造 AgentLoopHooks：把 chunk 推 stream_chunk、迭代进度推 agent_iteration、
//     工具事件推 tool_call_start/end、错误推 stream_error。
//  2. 调 conv.RunAgentLoop：内部完成 ReAct 循环迭代，每轮"LLM 推理 → 工具执行 →
//     结果反馈"自动写 history（tool_use / tool_result / 最终 assistant 文本）。
//  3. 退出前：把完整 history 持久化、推 stream_done + context_usage、
//     释放流式状态机、切回 idle。
//
// userInput 为本轮用户原始输入，Step 8 起作为后台记忆回顾快照的一部分（节流判断 +
// 回顾上下文），故从 handleUserInput 显式传入，避免回顾器反向依赖历史回溯。
//
// ctx 在 abort_stream 时被 cancel；AgentLoop 内部会响应 ctx 取消并
// 把当前 LLM 流 / 工具执行一并中断，由 runStream 根据 StopReason 映射退出原因。
func (h *Handler) runStream(ctx context.Context, conn *websocket.Conn, userInput string) {
	// 持锁快照本轮关键上下文：活跃连接、会话 ID、RunAgentLoop 前的历史消息数。
	//   - sessionID：注入 ctx 路由会话日志 + 作为 ReviewRequest.SessionID；
	//   - historyBefore：用户消息已在 handleUserInput 加入 history，故此处计数含本轮
	//     用户消息，RunAgentLoop 追加的 assistant/tool 消息即「本轮新增」范围，供
	//     OnLoopDone 提取本轮工具调用名摘要。
	var sessionID string
	var historyBefore int
	h.mu.Lock()
	h.pendingConn = conn
	if h.current != nil && h.current.ID != "" {
		sessionID = h.current.ID
		// 持锁取当前会话 ID 并注入 ctx：下游核心链路经 logger.InfoCtx 等函数据此把会话日志
		// 路由到对应会话目录的 codepilot.log（ctx 无 sessionID 时回退全局，不影响业务）。
		ctx = logger.WithSession(ctx, sessionID)
	}
	historyBefore = h.conv.MessageCount()
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.pendingConn = nil
		h.mu.Unlock()
	}()

	// recovered 标记是否因 panic 进入恢复逻辑
	var recovered bool

	// 防御性 defer：确保流式状态机被释放、stream_done 被发送。
	// 即使 runStream 内部 panic（如 gorilla/websocket 并发写），也能保证
	// 前端收到 stream_done 从而解除 streaming 状态。
	defer func() {
		if r := recover(); r != nil {
			recovered = true
			logger.Error("runStream goroutine panic，已恢复",
				zap.Any("panic", r),
				zap.String("stack", string(debug.Stack())),
			)
			// panic 恢复后通知前端流式结束（错误原因）
			_ = h.sendStreamError(conn, "internal_error", fmt.Sprintf("内部错误: %v", r))
		}
		// 无论正常退出还是 panic 恢复，都发送 stream_done 释放前端状态
		if !recovered {
			// 正常退出时 AgentLoop 已通过 result 发送 stream_done；
			// 但为安全起见，在 panic 场景下补发一次
		} else {
			_ = h.sendStreamDone(conn, StreamReasonError)
			_ = h.sendContextUsage(conn)
		}
		// 持久化当前已有的 history（即使 panic 也能保留已完成的消息）
		h.saveCurrentSession()
		_ = h.sendStatusUpdate(conn, StatusIdle)
		h.stream.release(ctx)
	}()

	// 把 toolHandler 的 OnStart/OnEnd 接到本连接的 WS 推送
	// 注意：OnStart/OnEnd 内部会同步调回调，所以"工具开始→状态切到 tool_running
	// →发 tool_call_start"三件事都在 ToolHandler.Execute 同步路径上完成。
	if h.toolHandler != nil {
		h.toolHandler.SetOnStart(func(evt conversation.ToolExecutionEvent) {
			_ = h.sendStatusUpdate(conn, StatusToolRunning)
			_ = h.sendToolCallStart(conn, ToolCallStartPayload{
				ToolUseID: evt.ToolUseID,
				Name:      evt.Name,
				Input:     evt.Input,
				StartedAt: evt.StartedAt,
				Server:    h.resolveMCPServerByToolName(evt.Name),
			})
		})
		h.toolHandler.SetOnEnd(func(evt conversation.ToolExecutionEvent) {
			// OnEnd 之后，AgentLoop 会立刻发起下一轮 LLM；提前把状态切到
			// StatusThinking，避免前端把"工具已结束但还在等 LLM 回复"
			// 这段间隙误判为 idle。
			_ = h.sendStatusUpdate(conn, StatusThinking)
			_ = h.sendToolCallEnd(conn, ToolCallEndPayload{
				ToolUseID:  evt.ToolUseID,
				Name:       evt.Name,
				Output:     SummarizeOutput(evt.Output),
				IsError:    evt.IsError,
				DurationMs: evt.DurationMs,
				Status:     mapToolEventStatus(evt.Status),
				Server:     h.resolveMCPServerByToolName(evt.Name),
			})
		})
	}

	// 构造 AgentLoopHooks：复用原有 TurnHooks 的 chunk/error 回调，
	// 新增 OnIterationStart 推送迭代进度和 thinking 状态，
	// 新增 OnLoopDone 在每次迭代结束时触发增量保存。
	loopHooks := conversation.AgentLoopHooks{
		TurnHooks: conversation.TurnHooks{
			OnStreamChunk: func(chunk llm.StreamChunk) {
				// 第一次 chunk 到达时，前端已通过 status_update(thinking) 切到
				// "思考中"，这里只推文本 delta。
				if chunk.Content != "" {
					_ = h.sendStreamChunk(conn, chunk.Content)
				}
			},
			OnError: func(err error) {
				_ = h.sendStreamError(conn, "stream_error", err.Error())
			},
			// Step 7：每轮自动压缩产生变更时推送 compaction_event（自动模式 manual=false）
			// 并刷新用量展示。回调在 runOneLLM 内同步触发，与流式写共享 writeMu，安全。
			OnCompaction: func(res memctx.CompactionResult) {
				h.dispatchCompactHook(ctx, sessionID, res)
				_ = h.sendCompactionEvent(conn, res, false)
				_ = h.sendContextUsage(conn)
			},
			// OnToolUse / OnToolResult 由 ToolHandler.OnStart/OnEnd 替代承担，
			// 这里置 nil；AgentLoop 内部会按 nil 跳过调用。
		},
		// OnIterationStart 在每轮迭代开始时推送迭代进度事件，
		// 同时将状态切到 thinking，告知前端 Agent 进入新一轮推理。
		OnIterationStart: func(iteration int, maxIterations int) {
			_ = h.sendStatusUpdate(conn, StatusThinking)
			_ = h.sendAgentIteration(conn, AgentIterationPayload{
				Current: iteration,
				Max:     maxIterations,
			})
		},
		// OnLoopDone 在 AgentLoop 结束后回调：
		//   1. 增量保存会话，确保 stream_done 之前落盘；
		//   2. Step 8：把本轮结果适配为 autolearn.ReviewRequest，触发后台异步记忆回顾。
		//      回顾器内部做节流（非 completed / 空输入 / 纯闲聊跳过）+ per-session 串行 +
		//      异步派发，本回调立即返回不阻塞响应流。reviewer 为 nil（记忆关闭）或
		//      Enabled=false 时回顾器内部短路，不产生任何回顾。
		OnLoopDone: func(result conversation.AgentLoopResult) {
			h.saveCurrentSession()
			if h.reviewer == nil {
				return
			}
			h.reviewer.OnLoopDone(autolearn.ReviewRequest{
				SessionID:     sessionID,
				Completed:     result.StopReason == conversation.StopReasonCompleted,
				UserInput:     userInput,
				FinalReply:    result.FinalText,
				ToolCallNames: h.collectTurnToolCallNames(historyBefore),
				OnEvent: func(evt autolearn.ReviewEvent) {
					_ = h.sendMemoryReviewEvent(conn, evt)
				},
			})
		},
	}

	// 工具描述：按配置过滤后转 []tool.ToolSpec；registry 为 nil 时不传任何工具。
	// cfg.Tools.Enabled 为空时透传 registry 中全部已注册工具（白名单留空 = 全开）。
	var toolSpecs []tool.ToolSpec
	if h.registry != nil {
		var enabled []string
		if h.cfg != nil {
			enabled = h.cfg.Tools.Enabled
		}
		toolSpecs = h.registry.ToSpecs(enabled)
	}

	// 构造 AgentLoopConfig：从全局 Config 读取迭代上限和上下文安全余量
	loopCfg := conversation.AgentLoopConfig{
		MaxIterations:       50,
		ContextSafetyMargin: 4096,
		ContextWindowSize:   h.contextWindowSize,
	}
	if h.cfg != nil {
		if h.cfg.MaxAgentLoopIterations > 0 {
			loopCfg.MaxIterations = h.cfg.MaxAgentLoopIterations
		}
		if h.cfg.ContextSafetyMargin > 0 {
			loopCfg.ContextSafetyMargin = h.cfg.ContextSafetyMargin
		}
	}

	result := h.conv.RunAgentLoop(ctx, h.provider, h.sp, toolSpecs, h.toolHandler, loopCfg, loopHooks)

	// 将 AgentLoopResult.StopReason 映射为前端 stream_done 的 reason 字符串
	reason := mapStopReason(result.StopReason)
	_ = h.sendStreamDone(conn, reason)
	_ = h.sendContextUsage(conn)
}

// collectTurnToolCallNames 从本轮 AgentLoop 新增的历史消息中提取去重保序的工具调用名摘要。
//
// historyBefore 为本轮 RunAgentLoop 开始前 conv 的消息数（由 runStream 在持锁快照时记录），
// 仅扫描该索引之后新增的 assistant 消息中的 tool_use 块，避免把历史轮的工具名混入本轮
// 回顾快照。回顾只需工具名（不含入参出参全文），既控制回顾成本又避免敏感数据进入回顾上下文。
// 边界防御：historyBefore 越界时回退到从头扫描，绝不 panic。
func (h *Handler) collectTurnToolCallNames(historyBefore int) []string {
	all := h.conv.AllMessages()
	if historyBefore < 0 || historyBefore > len(all) {
		historyBefore = 0
	}
	seen := make(map[string]struct{})
	var names []string
	for _, m := range all[historyBefore:] {
		for _, block := range m.Content {
			if tu, ok := block.(*llm.ToolUseBlock); ok && tu.Name != "" {
				if _, exists := seen[tu.Name]; !exists {
					seen[tu.Name] = struct{}{}
					names = append(names, tu.Name)
				}
			}
		}
	}
	return names
}

// saveCurrentSession 把当前 ConversationManager 中「尚未落盘的新消息」追加持久化到磁盘。
// 无锁版：调用方必须已持有 h.mu（切换会话路径均已持锁）。
// 通过 persistedMsgCount 仅追加 history[persistedMsgCount:] 实现真正的 append-only，
// 避免每次全量重写历史；失败仅记录日志，不影响调用方继续运行。
func (h *Handler) saveCurrentSessionLocked() {
	if h.current == nil {
		return
	}
	allMsgs := h.conv.AllMessages()
	// 幂等：已无新消息（OnLoopDone 与 defer 可能重复触发）则直接返回，不产生空写
	if len(allMsgs) <= h.persistedMsgCount {
		return
	}
	newMsgs := allMsgs[h.persistedMsgCount:]
	if err := h.sessMgr.AppendMessages(h.current.ID, newMsgs); err != nil {
		logger.Warn("会话增量保存失败",
			zap.String("session_id", h.current.ID),
			zap.Int("new_msgs", len(newMsgs)),
			zap.Error(err),
		)
		return
	}
	// 落盘成功：同步内存镜像与已落盘计数
	h.current.Messages = allMsgs
	h.current.UpdatedAt = time.Now()
	h.persistedMsgCount = len(allMsgs)
}

// saveCurrentSession 是 saveCurrentSessionLocked 的带锁包装，供未持有 h.mu 的调用点
// （如 runStream defer / OnLoopDone 回调）使用。已持锁的切换点必须直接调 Locked 版本，避免死锁。
func (h *Handler) saveCurrentSession() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.saveCurrentSessionLocked()
}

// handleAbortStream 中断当前流式请求。无正在进行的流时为 no-op。
// 中断后由 runStream goroutine 负责发送 stream_done(reason=aborted)。
func (h *Handler) handleAbortStream(conn *websocket.Conn, msg Message) error {
	h.stream.abort()
	return nil
}

// handleGetCurrentSession 把当前活动会话以 session_loaded 形式推回客户端。
// 前端在 WebSocket onopen 时主动调用，以建立"我正处在这个会话"的状态。
// 同步追加 status_update(idle) 与 context_usage，便于前端立即把状态栏更新正确。
func (h *Handler) handleGetCurrentSession(conn *websocket.Conn, msg Message) error {
	h.mu.Lock()
	cur := h.current
	h.mu.Unlock()
	if cur == nil {
		cur = h.sessMgr.CreateNew()
		h.mu.Lock()
		h.current = cur
		h.persistedMsgCount = 0
		h.conv.SetSessionID(cur.ID)
		h.mu.Unlock()
		// 打开当前会话专属日志器（释放锁后再做，避免锁内做日志目录 IO）。
		h.openSessionLogger(cur.ID)
	}
	if err := h.sendSessionLoaded(conn, cur); err != nil {
		return err
	}
	if err := h.sendStatusUpdate(conn, StatusIdle); err != nil {
		return err
	}
	return h.sendContextUsage(conn)
}

// handleListSessions 列出历史会话摘要。
// 根据请求 Mode 决定返回形态：
//   - mode="table"：按 CreatedAt 降序、最近 10 条（前端 /sessions 命令的表格视图）
//   - 其他：按 UpdatedAt 降序、全部（侧边栏刷新、/resume 前缀匹配）
func (h *Handler) handleListSessions(conn *websocket.Conn, msg Message) error {
	p, _ := AsPayload[ListSessionsPayload](msg)

	var summaries []memsession.SessionSummary
	var err error
	if p.Mode == "table" {
		summaries, err = h.sessMgr.ListRecentSessions(10)
	} else {
		summaries, err = h.sessMgr.ListSessions()
	}
	if err != nil {
		return h.sendStreamError(conn, "list_sessions_failed", err.Error())
	}
	out := make([]SessionSummary, len(summaries))
	for i, s := range summaries {
		out[i] = SessionSummary{
			ID:           s.ID,
			CreatedAt:    s.CreatedAt,
			UpdatedAt:    s.UpdatedAt,
			MessageCount: s.MessageCount,
			Preview:      s.Preview,
		}
	}
	return h.sendMessage(conn, MsgTypeSessionList, SessionListPayload{Sessions: out})
}

// handleNewSession 保存当前会话（如有消息）并创建新会话，重置 ConvMgr。
func (h *Handler) handleNewSession(conn *websocket.Conn, msg Message) error {
	h.dispatchSessionHook(context.Background(), hook.EventSessionEnd, h.CurrentSessionID())

	h.mu.Lock()
	// 增量落盘当前会话（切换点已持 h.mu，必须用 Locked 版本避免死锁）
	h.saveCurrentSessionLocked()
	h.current = h.sessMgr.CreateNew()
	newSessionID := h.current.ID
	h.conv.Reset(nil)
	h.conv.SetSessionID(newSessionID)
	if h.toolHandler != nil {
		h.toolHandler.SetHookSessionID(newSessionID)
	}
	// 打开新会话专属日志器（切换点已持 h.mu；OpenSession 自身线程安全，不会死锁）。
	h.openSessionLogger(newSessionID)
	h.persistedMsgCount = 0
	// 新会话触发 SP 重新组装（虽然 result 通常一致，但保持与切换路径一致的处理）
	h.assembleSP()
	// 刷新前端 ctx left 显示：新会话无历史，remaining 应回到 ~100%
	usage := h.conv.GetContextUsage(h.contextWindowSize)
	spSnapshot := h.sp
	current := h.current
	h.mu.Unlock()

	h.dispatchSessionHook(context.Background(), hook.EventSessionStart, newSessionID)
	_ = h.sendContextUsageLocked(conn, usage, spSnapshot)
	return h.sendSessionLoaded(conn, current)
}

// handleClearSession 清空当前会话的上下文：保留 session_id，
// 把消息数组置空、重置 ConvMgr，并落盘覆盖。
// 与 handleNewSession 的差异：不创建新会话，不在左侧历史中新增条目。
func (h *Handler) handleClearSession(conn *websocket.Conn, msg Message) error {
	h.dispatchSessionHook(context.Background(), hook.EventSessionEnd, h.CurrentSessionID())

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.current == nil {
		h.current = h.sessMgr.CreateNew()
	}
	h.current.Messages = nil
	h.conv.Reset(nil)
	// 清空也触发 SP 重新组装（与切换会话路径保持一致语义）
	h.assembleSP()
	// 清空磁盘消息日志（保留 session_id），让历史列表预览/计数同步归零。
	// TruncateMessages 内部已一并清理第二层摘要压缩归档（history_archive.jsonl）。
	if err := h.sessMgr.TruncateMessages(h.current.ID); err != nil {
		logger.Warn("清空会话消息失败", zap.Error(err))
	}
	// 清理第一层压缩落盘的工具结果归档目录（tool_results/）——TruncateMessages 不感知
	// context 包产物，故由 handler 持有的 store 在此补一刀，使 /clear 后会话目录彻底干净。
	// store 未注入（nil）时跳过；清理失败仅记日志，不影响已完成的清空。
	if h.toolResultStore != nil {
		if err := h.toolResultStore.Clear(h.current.ID); err != nil {
			logger.Warn("清理会话工具结果归档失败",
				zap.String("session_id", h.current.ID), zap.Error(err))
		}
	}
	h.persistedMsgCount = 0
	// 刷新前端 ctx left 显示：清空后历史为空，remaining 应回到 ~100%
	usage := h.conv.GetContextUsage(h.contextWindowSize)
	_ = h.sendContextUsageLocked(conn, usage, h.sp)
	return h.sendSessionLoaded(conn, h.current)
}

// handleResumeSession 通过 ID 前缀匹配恢复历史会话。
//   - 0 匹配：stream_error(session_not_found)
//   - 1 匹配：加载、注入历史到 ConvMgr
//   - 多匹配：stream_error(session_ambiguous)
func (h *Handler) handleResumeSession(conn *websocket.Conn, msg Message) error {
	oldSessionID := h.CurrentSessionID()
	p, err := AsPayload[ResumeSessionPayload](msg)
	if err != nil {
		return h.sendStreamError(conn, "invalid_payload", err.Error())
	}
	if p.ID == "" {
		return h.sendStreamError(conn, "empty_id", "会话 ID 不能为空")
	}

	summaries, err := h.sessMgr.ListSessions()
	if err != nil {
		return h.sendStreamError(conn, "list_sessions_failed", err.Error())
	}

	var matches []memsession.SessionSummary
	for _, s := range summaries {
		if strings.HasPrefix(s.ID, p.ID) {
			matches = append(matches, s)
		}
	}
	if len(matches) == 0 {
		return h.sendStreamError(conn, "session_not_found",
			fmt.Sprintf("未找到匹配 %q 的会话", p.ID))
	}
	if len(matches) > 1 {
		return h.sendStreamError(conn, "session_ambiguous",
			fmt.Sprintf("匹配到 %d 个会话，请输入更长的 ID 前缀", len(matches)))
	}

	sess, err := h.sessMgr.Load(matches[0].ID)
	if err != nil {
		return h.sendStreamError(conn, "session_load_failed", err.Error())
	}

	h.dispatchSessionHook(context.Background(), hook.EventSessionEnd, oldSessionID)

	h.mu.Lock()
	// 增量落盘当前会话（切换点已持 h.mu，必须用 Locked 版本避免死锁）
	h.saveCurrentSessionLocked()
	h.current = sess
	h.conv.Reset(sess.Messages)
	h.conv.SetSessionID(sess.ID)
	if h.toolHandler != nil {
		h.toolHandler.SetHookSessionID(sess.ID)
	}
	// 打开恢复会话专属日志器（幂等：已打开则直接复用，便于反复 resume）。
	h.openSessionLogger(sess.ID)
	// 恢复的消息已在磁盘上，已落盘计数对齐到其长度
	h.persistedMsgCount = len(sess.Messages)
	// 切换会话后重新组装 SP（确保与新会话上下文一致）
	h.assembleSP()
	h.mu.Unlock()

	h.dispatchSessionHook(context.Background(), hook.EventSessionStart, sess.ID)
	// 刷新前端 ctx left 显示：恢复会话后上下文用量已变
	_ = h.sendContextUsage(conn)
	return h.sendSessionLoaded(conn, sess)
}

// handleDeleteSession 删除指定 ID 的会话文件。
// 注意点：
//   - ID 必须为完整 ID（侧边栏点击删除时携带完整 ID，避免与 resume 的前缀匹配混淆）。
//   - 若被删除的是当前激活会话，则自动切到最近一次更新的其它会话；若已无任何会话，则新建一个空会话。
//   - 总是先发 session_deleted 通知前端，若发生当前会话切换，再追加一条 session_loaded。
func (h *Handler) handleDeleteSession(conn *websocket.Conn, msg Message) error {
	p, err := AsPayload[DeleteSessionPayload](msg)
	if err != nil {
		return h.sendStreamError(conn, "invalid_payload", err.Error())
	}
	if strings.TrimSpace(p.ID) == "" {
		return h.sendStreamError(conn, "empty_id", "会话 ID 不能为空")
	}

	// 删除文件（不存在会返回明确错误）
	if err := h.sessMgr.Delete(p.ID); err != nil {
		return h.sendStreamError(conn, "delete_session_failed", err.Error())
	}
	logger.Info("会话已删除", zap.String("session_id", p.ID))

	// 判断是否影响当前会话；若是，则选择新的当前会话
	h.mu.Lock()
	currentChanged := h.current != nil && h.current.ID == p.ID
	var newCurrent *memsession.Session
	if currentChanged {
		// 优先选最近更新的其它会话
		summaries, listErr := h.sessMgr.ListSessions()
		if listErr == nil && len(summaries) > 0 {
			if loaded, loadErr := h.sessMgr.Load(summaries[0].ID); loadErr == nil {
				newCurrent = loaded
			}
		}
		// 兜底：没有任何历史会话或加载失败，新建一个空会话
		if newCurrent == nil {
			newCurrent = h.sessMgr.CreateNew()
		}
		h.current = newCurrent
		h.conv.Reset(newCurrent.Messages)
		h.conv.SetSessionID(newCurrent.ID)
		// 切换后打开新当前会话专属日志器。
		h.openSessionLogger(newCurrent.ID)
		// 已落盘计数对齐到新当前会话的消息数（Load 出来的有计数，CreateNew 的为 0）
		h.persistedMsgCount = len(newCurrent.Messages)
		// 切换会话后重新组装 SP
		h.assembleSP()
	}
	h.mu.Unlock()

	// 先发 session_deleted，便于前端立即从列表里移除条目
	if err := h.sendMessage(conn, MsgTypeSessionDeleted, SessionDeletedPayload{
		DeletedID:      p.ID,
		CurrentChanged: currentChanged,
	}); err != nil {
		return err
	}

	// 若切换了当前会话，再推一条 session_loaded 让前端把消息区和高亮状态同步过来
	if currentChanged && newCurrent != nil {
		_ = h.sendContextUsage(conn)
		return h.sendSessionLoaded(conn, newCurrent)
	}
	return nil
}

// ---- 内部 send helper ----

// handleDevExportSP 响应前端「导出 System Prompt」按钮（开发者模式）：
// 把当前缓存的 sp 的完整结构（SystemBlocks 文本 + LeadUserMessage +
// Stats + TotalTokens）以 dev_export_sp 消息回推，便于用户在浏览器
// 检视/调试 SP 的组装结果。
func (h *Handler) handleDevExportSP(conn *websocket.Conn, msg Message) error {
	h.mu.Lock()
	sp := h.sp
	h.mu.Unlock()

	// SystemBlocks 转 string 数组
	systemTexts := make([]string, 0, len(sp.SystemBlocks))
	for _, b := range sp.SystemBlocks {
		systemTexts = append(systemTexts, b.Text)
	}
	// Stats 转结构体数组
	stats := make([]SPSourceStat, 0, len(sp.Stats))
	for _, s := range sp.Stats {
		stats = append(stats, SPSourceStat{Name: s.Name, Tokens: s.Tokens})
	}
	return h.sendMessage(conn, MsgTypeDevExportSP, DevExportSPPayload{
		SystemBlocks:    systemTexts,
		LeadUserMessage: sp.LeadUserMessage,
		Stats:           stats,
		TotalTokens:     sp.TotalTokens,
	})
}

// handleGetFileDiff 处理前端「查看改动」按钮的查询请求：
// 按 tool_use_id 从 FileDiffStore 取出对应 WriteFile/EditFile 的 before/after，
// 通过 file_diff 消息回推。
//
// 三种响应分支：
//   - 找到：found=true, reason="", 回填 file_path / language / before / after
//   - 找不到：found=false, reason="not_found"（store 为 nil / 已被淘汰 / 旧会话重启都走此分支）
//   - 空 tool_use_id：通过 stream_error(invalid_payload) 拒绝
//
// 不修改 store 内容（仅查询）。并发安全由 FileDiffStore 内部 RWMutex 负责。
func (h *Handler) handleGetFileDiff(conn *websocket.Conn, msg Message) error {
	p, err := AsPayload[GetFileDiffPayload](msg)
	if err != nil {
		return h.sendStreamError(conn, "invalid_payload", err.Error())
	}
	if strings.TrimSpace(p.ToolUseID) == "" {
		return h.sendStreamError(conn, "empty_tool_use_id", "tool_use_id 不能为空")
	}

	// store 为 nil 时也走 not_found，等价于"未启用 diff 预览"
	if h.fileDiffStore == nil {
		return h.sendFileDiff(conn, FileDiffPayload{
			ToolUseID: p.ToolUseID,
			Found:     false,
			Reason:    "not_found",
		})
	}

	diff, ok := h.fileDiffStore.Get(p.ToolUseID)
	if !ok {
		return h.sendFileDiff(conn, FileDiffPayload{
			ToolUseID: p.ToolUseID,
			Found:     false,
			Reason:    "not_found",
		})
	}
	return h.sendFileDiff(conn, FileDiffPayload{
		ToolUseID: diff.ToolUseID,
		Found:     true,
		FilePath:  diff.FilePath,
		Language:  diff.Language,
		Before:    diff.Before,
		After:     diff.After,
	})
}

func (h *Handler) handleListProjectDir(conn *websocket.Conn, msg Message) error {
	p, err := AsPayload[ListProjectDirPayload](msg)
	if err != nil {
		return h.sendProjectDir(conn, ProjectDirPayload{OK: false, Reason: "invalid_payload"})
	}

	result, err := NewProjectFileBrowser(h.workdir).ListDir(p.Path)
	payload := projectDirPayloadFromResult(result, err == nil)
	payload.RequestID = p.RequestID
	if payload.Reason == "" && err != nil {
		payload.Reason = ProjectFileReasonReadError
	}
	return h.sendProjectDir(conn, payload)
}

func (h *Handler) handleReadProjectFile(conn *websocket.Conn, msg Message) error {
	p, err := AsPayload[ReadProjectFilePayload](msg)
	if err != nil {
		return h.sendProjectFile(conn, ProjectFilePayload{Found: false, OK: false, Reason: "invalid_payload"})
	}

	result, err := NewProjectFileBrowser(h.workdir).ReadFile(p.Path)
	payload := projectFilePayloadFromResult(result)
	payload.RequestID = p.RequestID
	if payload.Reason == "" && err != nil {
		payload.Reason = ProjectFileReasonReadError
	}
	return h.sendProjectFile(conn, payload)
}

func projectDirPayloadFromResult(result ProjectDirResult, ok bool) ProjectDirPayload {
	return ProjectDirPayload{
		OK:          ok,
		Reason:      result.Reason,
		Path:        result.Path,
		ParentPath:  result.ParentPath,
		Breadcrumbs: result.Breadcrumbs,
		Entries:     result.Entries,
		Truncated:   result.Truncated,
	}
}

func projectFilePayloadFromResult(result ProjectFileResult) ProjectFilePayload {
	return ProjectFilePayload{
		Found:   result.Found,
		OK:      result.OK,
		Reason:  result.Reason,
		File:    result.File,
		Content: result.Content,
	}
}

// ---- Slash 命令下发相关 handler（Step 9.1 Task 4） ----

// sendSlashCommands 把当前 slash 命令清单推送给单个连接。
//
// typ 必须为 MsgTypeSlashCommands 或 MsgTypeSlashCommandsUpdated：
//   - MsgTypeSlashCommands 用于 ws onWSOpen 主动推送 / 响应 list_slash_commands 请求
//   - MsgTypeSlashCommandsUpdated 用于命令清单变化时（Step 10 Skill 动态注册）广播推送
//
// payload 形态两种用途相同（SlashCommandsPayload）；通过 typ 区分消息类型
// 便于前端做差异化的 UI 反馈（如 updated 触发一次轻量 toast）。
//
// slashProvider 为 nil 时回推空命令清单（等价"未启用 slash"）。
// 写入经 sendMessage 串行化（writeMu 保护），不会与 runStream 流式写竞争。
func (h *Handler) sendSlashCommands(conn *websocket.Conn, typ string) error {
	entries := h.collectSlashCommandEntries()
	cmds := make([]SlashCommandInfo, 0, len(entries))
	for _, e := range entries {
		cmds = append(cmds, SlashCommandInfo{
			Name:        e.Name,
			Description: e.Description,
			NeedsArg:    e.NeedsArg,
			ArgHint:     e.ArgHint,
			Category:    e.Category,
		})
	}
	return h.sendMessage(conn, typ, SlashCommandsPayload{Commands: cmds})
}

// collectSlashCommandEntries 从 SlashCommandProvider 读取当前命令列表。
// provider 为 nil 时返回空切片（等价"未启用 slash"）。
func (h *Handler) collectSlashCommandEntries() []SlashCommandEntry {
	if h.slashProvider == nil {
		return nil
	}
	return h.slashProvider.List()
}

// handleListSlashCommands 处理前端主动拉取命令清单的请求（list_slash_commands）。
// 等价于 onWSOpenSlash 的推送逻辑：把当前注册表完整返回。
//
// 用途：ws 断线重连后，前端可用本请求做兜底拉取（即使错过 onWSOpen 推送也能恢复）。
func (h *Handler) handleListSlashCommands(conn *websocket.Conn, msg Message) error {
	return h.sendSlashCommands(conn, MsgTypeSlashCommands)
}

// handleSkills 处理前端 /skills 命令触发的 list_skills 请求（Step 10 Task 6）。
//
// 流程：
//  1. 遍历 skillProvider.ListBySource("project" / "user" / "builtin") 得到三档数组
//  2. 构造 SkillsListPayload 并通过 MsgTypeSkillsList 推回
//
// skillProvider 为 nil 时回推三组均为空数组的 payload（前端展示「暂无 Skill」空状态）。
// provider 已注入但三档均为空时同样回推空 payload（零 Skill 启动兼容）。
//
// 入参 msg 暂未使用（list_skills 无入参），保留是为了与 router.Register 的
// 统一签名对齐；与 handleListSlashCommands / handleListSessions 风格一致。
func (h *Handler) handleSkills(conn *websocket.Conn, msg Message) error {
	_ = msg
	payload := SkillsListPayload{
		Project: []SkillEntry{},
		User:    []SkillEntry{},
		Builtin: []SkillEntry{},
	}
	if h.skillProvider != nil {
		payload.Project = h.skillProvider.ListBySource("project")
		payload.User = h.skillProvider.ListBySource("user")
		payload.Builtin = h.skillProvider.ListBySource("builtin")
	}
	return h.sendMessage(conn, MsgTypeSkillsList, payload)
}

// handleSlashCommand 处理通用 slash 命令执行请求（Step 10 引入，MsgTypeSlashCommand）。
//
// 流程：
//  1. 解析 payload → {Name, Arg}；Name 必填，空时回推 stream_error(invalid_payload)
//  2. slashProvider 为 nil（未注入 slash Registry）→ stream_error(slash_not_ready)
//  3. 调 slashProvider.Execute(ctx, conn, Name, Arg) 委托底层 registry.Get + cmd.Execute
//  4. Execute 返回 error → stream_error(slash_command_failed)
//
// 设计动机：Step 10 Skill 系统的 /<skill-name> 命令以及后续 Step 11/12 引入的
// Hook/SubAgent 子命令都没有专属 MsgType，逐个新增 ws 协议会让 router 膨胀。
// 因此引入通用执行入口，按 Name 在注册表里查找，命中即转发 Execute。
//
// 与 handleListSlashCommands 的边界：本 handler 不回推任何「结果」消息，
// Execute 内部的副作用（如 LeadUserMessage 注入）由 conversation/handler
// 后续流式消息回推；本 handler 只关心参数解析与错误回传。
//
// 与 /resume / /new 等命令的关系：这些命令在 Step 9 已有专属 MsgType（带历史
// 业务逻辑如消息持久化），前端继续走专属协议；本通用协议仅服务「无专属 MsgType」
// 的命令，避免与既有协议重复。
func (h *Handler) handleSlashCommand(conn *websocket.Conn, msg Message) error {
	p, err := AsPayload[SlashCommandRequest](msg)
	if err != nil {
		return h.sendStreamError(conn, "invalid_payload", err.Error())
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return h.sendStreamError(conn, "invalid_payload", "slash command name 不能为空")
	}
	if h.slashProvider == nil {
		return h.sendStreamError(conn, "slash_not_ready", "slash 命令注册表未注入")
	}
	// Execute 内已做 ctx 取消、nil 防御等；这里只透传 arg 字符串。
	if h.isSlashCommandCategory(name, "skill") {
		acquired, busy := h.stream.tryAcquire()
		if busy {
			return h.sendStreamError(conn, "busy", "current stream is busy")
		}
		if err := h.slashProvider.Execute(acquired, conn, name, p.Arg); err != nil {
			h.stream.release(acquired)
			return h.sendStreamError(conn, "slash_command_failed", err.Error())
		}
		userInput := formatSlashUserInput(name, p.Arg)
		h.mu.Lock()
		h.conv.AddUserMessage(userInput)
		h.mu.Unlock()
		_ = h.sendStatusUpdate(conn, StatusThinking)
		go h.runStream(acquired, conn, userInput)
		return nil
	}

	// Non-skill slash commands keep their original execute-only behavior.
	if err := h.slashProvider.Execute(context.Background(), conn, name, p.Arg); err != nil {
		return h.sendStreamError(conn, "slash_command_failed", err.Error())
	}
	return nil
}

func (h *Handler) isSlashCommandCategory(name, category string) bool {
	if name == "" || category == "" {
		return false
	}
	for _, entry := range h.collectSlashCommandEntries() {
		if entry.Name == name && entry.Category == category {
			return true
		}
	}
	return false
}

func formatSlashUserInput(name, arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return name
	}
	return name + " " + arg
}

// onWSOpenSlash 是 ws 连接建立后由 ConnectionManager.onOpenHook 触发的回调：
// 主动向当前 conn 推送 slash_commands 消息，确保前端在 ws onWSOpen 后立刻能拿到命令清单。
//
// 注册入口在 main.go：handler.SetConnMgr 之后，再注册本回调：
//
//	server.ConnectionManager().SetOnOpenHook(handler.PushSlashCommandsOnOpen)
//
// 本函数等价于 sendSlashCommands(conn, MsgTypeSlashCommands) 的精简包装，
// 单独保留函数名便于将来在 ws Open 处扩展其他主动推送逻辑。
func (h *Handler) onWSOpenSlash(conn *websocket.Conn) error {
	return h.sendSlashCommands(conn, MsgTypeSlashCommands)
}

// PushSlashCommandsOnOpen 是 onWSOpenSlash 的导出别名，供 main.go 在跨包装配时
// 传入 ConnectionManager.SetOnOpenHook（onWSOpenSlash 本身是包内未导出方法）。
// 典型用法（Step 9.1 Task 6）：
//
//	server.ConnectionManager().SetOnOpenHook(handler.PushSlashCommandsOnOpen)
//
// 行为与 onWSOpenSlash 完全一致，仅访问权限不同；不影响 handler 内部其他逻辑路径。
func (h *Handler) PushSlashCommandsOnOpen(conn *websocket.Conn) {
	_ = h.sendSlashCommands(conn, MsgTypeSlashCommands)
}

// broadcastSlashCommandsUpdated 向所有活跃 conn 推送 slash_commands_updated。
//
// 由 SlashCommandProvider.OnChange 回调触发：注册命令清单变化时（Step 10 Skill
// 动态注册场景）通知所有前端刷新候选下拉。本步骤（Task 4）暂不主动调用，
// 仅注册回调机制就位。
//
// 并发安全：遍历 connMgr.Snapshot() 后逐个调 sendMessage，每个 sendMessage
// 内部持有 writeMu——与 runStream 流式写共享同一把锁；connMgr 为 nil 时退化为
// no-op（兼容未注入场景与测试）。
func (h *Handler) broadcastSlashCommandsUpdated() {
	if h.connMgr == nil {
		return
	}
	for _, conn := range h.connMgr.Snapshot() {
		_ = h.sendSlashCommands(conn, MsgTypeSlashCommandsUpdated)
	}
}

// SetMCPPool 注入 MCP 连接池。
// 应在 main.go 启动流程中、构造 Handler 之后调用一次。
// pool 为 nil 时 MCP 相关能力（远端工具 server 解析 + 状态栏 mcp_status 推送）禁用。
func (h *Handler) SetMCPPool(pool *mcpsession.Pool) {
	h.mcpPool = pool
}

// SetConnMgr 注入 WebSocket 连接管理器，使 BroadcastMCPStatus 能向所有活跃连接推送。
// 应在 main.go 构造 Server 之后、启动 Web 服务前调用一次。
// mgr 为 nil 时 BroadcastMCPStatus 退化为 no-op。
func (h *Handler) SetConnMgr(mgr *ConnectionManager) {
	h.connMgr = mgr
}

// SetSlashRegistry 注入 slash 命令注册表（Step 9.1 Task 4）。
//
// 参数通过 SlashCommandProvider 接口抽象（web 包不直接 import command/slash，
// 避免 web → slash → web 的循环依赖）。典型用法（main.go 顶层装配）：
//
//	provider := newRegistryAdapter(slashRegistry) // 见 web/slash_adapter.go
//	h.SetSlashRegistry(provider)
//
// 行为：
//  1. 保存 provider 引用，供 sendSlashCommands / handleListSlashCommands 读取；
//  2. 一次性构造 slashCmdMap：「命令名 → 既有 MsgType」的静态映射，仅供调试可见。
//     前端按 name 查 state.commandTypeByName 自行决定发送什么，handler 不直接消费；
//  3. 注册 OnChange 回调：命令清单变化时（Step 10 Skill 动态注册）遍历所有活跃
//     conn 推送 slash_commands_updated；connMgr 为 nil 时退化为 no-op。
//
// provider 为 nil 时等价于"未启用 slash"：onWSOpen / list_slash_commands 均回
// 推空命令清单，前端候选下拉为空。
func (h *Handler) SetSlashRegistry(provider SlashCommandProvider) {
	h.slashProvider = provider
	h.slashCmdMap = map[string]string{
		"/new":     MsgTypeNewSession,
		"/clear":   MsgTypeClearSession,
		"/compact": MsgTypeCompact,
		"/dump":    MsgTypeDump,
		// /resume 因 NeedsArg=true，由前端补全到输入框后用户填 ID 提交，
		// 不通过此 map 查找；前端识别 Category=="session" 且 needsArg==true 时
		// 走 MsgTypeResumeSession 路径（payload.id 来自用户补全内容）。
	}
	if provider == nil {
		return
	}
	// 注册 OnChange 回调：命令清单变化时遍历当前所有活跃连接推送 slash_commands_updated。
	// 回调在 Register 同步路径上触发一次（slash.Registry 的 OnChange 机制），用于让
	// handler 在注入后就能感知到既有 6 条内置命令（不依赖 OnChange 在后续动态注册时
	// 才被触发）。本步骤（Task 4）仅注册回调，**不主动触发** Notify；
	// 实际推送由 SetSlashRegistry 注入后由 OnChange 的初次同步触发一次性完成推送。
	provider.OnChange(func() {
		h.broadcastSlashCommandsUpdated()
	})
}

// InjectLeadUserMessage 注入会话级「首条 user 消息」内容（Step 10 Task 7）。
//
// 用途：Skill slash 适配器（skilladapter.LeadMessageInjector 接口）需要在 /<skill>
// 触发时把 Skill 完整内容 + <user_args> 段写入 LeadUserMessage。web 包是唯一
// 直接持有 *ConversationManager 的层（assembleSP 在内部直接访问 h.conv），
// 故本方法提供「经由 Handler 写入 LeadUserMessage」的对外接口，main.go 顶层
// 把 *Handler 包装为 LeadMessageInjector 注入 Skill slash 适配器，避免 slash
// 适配器直接 import engine/conversation。
//
// 行为：直接调 h.conv.SetLeadUserMessage(text)——与 assembleSP 内 Assemble
// 完成后的写入路径完全一致，Skill 内容会作为下一轮 LLM user 消息头部传入。
func (h *Handler) InjectLeadUserMessage(text string) error {
	if h == nil || h.conv == nil {
		return nil
	}
	h.conv.SetLeadUserMessage(text)
	return nil
}

// SetSkillProvider 注入 Skill 注册表（Step 10 Task 6）。
//
// 参数通过 SkillProvider 接口抽象（web 包不直接 import skill 包）：
//   - 实现方（典型为 main.go 顶层 skillProviderAdapter 包装 *skill.Registry）
//     把 *skill.Skill 投影为 web.SkillEntry 后注入；
//   - handler 只与 SkillEntry 交互，不感知 skill 包的 Source 枚举等内部细节；
//   - provider 为 nil 时 list_skills 回推三组空数组，前端展示「暂无 Skill」。
//
// 调用时机：main.go 顶层装配后、handler.Register 之前；与 SetSlashRegistry 风格一致。
//
// 行为：仅保存 provider 引用；不立即推送 Skill 列表（与 SetMCPPool / SetConnMgr
// 风格保持一致），前端按需通过 list_skills 触发拉取。后续 Task 7 接入主流程时
// 可在 ws onOpen 主动推送 skills_list（与 slash_commands 对称），但本任务不要求。
func (h *Handler) SetSkillProvider(p SkillProvider) {
	h.skillProvider = p
}

// resolveMCPServerByToolName 从远端工具名（`mcp__<server>__<tool>`）中提取 server 部分。
//
// 解析规则严格遵循 adapter.BuildToolName：
//   - 必须以 "mcp__" 开头
//   - 双下划线 "__" 之后的剩余部分为 server 与 tool 的拼接
//   - 第一个 "__" 之前是 server 名
//
// 解析失败或工具名不属于 MCP 远端工具时返回空串,内置工具因此不会展示 server 徽标。
func (h *Handler) resolveMCPServerByToolName(toolName string) string {
	const prefix = "mcp__"
	if len(toolName) <= len(prefix) || !strings.HasPrefix(toolName, prefix) {
		return ""
	}
	rest := toolName[len(prefix):]
	// 分隔符是连续双下划线
	const sep = "__"
	idx := strings.Index(rest, sep)
	if idx < 0 {
		return ""
	}
	return rest[:idx]
}

// buildMCPStatusPayload 构造 mcp_status 推送 payload。
//
// 数据源：mcpPool.HealthyNames() + mcpPool.Unhealthy()。
// 远端工具数 = 各 healthy server 注册的 adapterTool 数量(由 tool.Registry 反查),
// 但为了避免 handler 强依赖 Registry 内部统计,这里用 mcpPool 已有的 HealthyNames
// + toolName 前缀匹配简单计算(每 server 取所有 "mcp__<server>__" 前缀的工具)。
//
// mcpPool 为 nil 时返回空 payload,Servers 为空数组,前端视为"未启用 MCP"。
func (h *Handler) buildMCPStatusPayload() MCPStatusPayload {
	payload := MCPStatusPayload{
		Servers: []MCPServerStatus{},
	}
	if h.mcpPool == nil {
		return payload
	}

	// [Why] MCP 初始化异步化：mcpPool 已注入但握手可能仍在后台进行。
	// Initializing()==true 时前端展示"连接中…"loading 态。初始化完成（成功或失败）
	// 后该标志复位为 false，前端据此停止 loading 动画。loading 与下方 servers[] 填充
	// 正交：初始化中 servers 通常为空，但快的 server 可能已就绪并出现在列表中。
	payload.Loading = h.mcpPool.Initializing()

	// 远端工具数(按 server 名分组):遍历 registry 统计 mcp__<server>__ 前缀
	toolsPerServer := make(map[string]int)
	if h.registry != nil {
		for _, t := range h.registry.List() {
			srv := h.resolveMCPServerByToolName(t.Name())
			if srv != "" {
				toolsPerServer[srv]++
			}
		}
	}

	// healthy servers
	healthy := make(map[string]bool, len(h.mcpPool.HealthyNames()))
	for _, name := range h.mcpPool.HealthyNames() {
		healthy[name] = true
		tools := toolsPerServer[name]
		payload.Servers = append(payload.Servers, MCPServerStatus{
			Name:  name,
			State: MCPHealthHealthy,
			Tools: tools,
		})
		payload.HealthyCount++
		payload.TotalTools += tools
	}
	// unhealthy servers
	for name, reason := range h.mcpPool.Unhealthy() {
		payload.Servers = append(payload.Servers, MCPServerStatus{
			Name:   name,
			State:  MCPHealthUnhealthy,
			Reason: reason,
		})
		payload.UnhealthyCount++
	}
	// 按 server 名字典序排序,稳定输出
	sort.SliceStable(payload.Servers, func(i, j int) bool {
		return payload.Servers[i].Name < payload.Servers[j].Name
	})
	return payload
}

// sendMCPStatus 推送 mcp_status 消息到当前 conn。
// 在 WebSocket 连接建立时和 MCP pool 健康状态变化时调用。
func (h *Handler) sendMCPStatus(conn *websocket.Conn) error {
	return h.sendMessage(conn, MsgTypeMCPStatus, h.buildMCPStatusPayload())
}

// BroadcastMCPStatus 向所有活跃 WebSocket 连接推送当前 MCP 状态快照。
//
// 调用时机：MCP 后台初始化就绪后由 main goroutine 触发一次（loading 由 true 翻 false）。
//
// [并发写安全] 遍历 connMgr.Snapshot() 后逐个调 sendMessage，每个 sendMessage
// 内部持有 writeMu——与 runStream 的流式写、sendSessionLoaded 的连接级写共享同一把锁，
// 同一 conn 不会同时进入 WriteMessage，杜绝 gorilla 并发写 panic。
// 刻意不走 ConnectionManager.Broadcast（裸 WriteMessage 不经 writeMu）。
//
// [Why payload 复用] buildMCPStatusPayload 需遍历 registry 统计工具数、无 conn 依赖，
// 一次构造给所有连接共用，避免重复计算。
func (h *Handler) BroadcastMCPStatus() {
	if h.connMgr == nil {
		return
	}
	payload := h.buildMCPStatusPayload()
	for _, conn := range h.connMgr.Snapshot() {
		_ = h.sendMessage(conn, MsgTypeMCPStatus, payload)
	}
}

// openSessionLogger 打开（幂等）指定会话的专属日志器，使该会话核心链路日志写入其会话目录
// 的 codepilot.log。在所有「会话切换」汇聚点（启动恢复 / new / resume / delete 后切换）调用，
// 确保切换后目标会话的日志目录就绪。幂等：同一 sessionID 重复打开直接复用，无副作用。
// 失败仅记全局 warn、不阻塞会话切换——logger.OpenSession 失败时 LCtx 会自动回退全局日志。
func (h *Handler) openSessionLogger(id string) {
	if id == "" {
		return
	}
	if err := logger.OpenSession(id, h.sessMgr.SessionDir(id)); err != nil {
		logger.Warn("打开会话日志器失败，将回退全局日志",
			zap.String("sessionID", id),
			zap.Error(err),
		)
	}
}

// SetCompactor 注入上下文压缩协调器（Step 7）。
// 应在 main.go 构造 Handler 后、启动服务前调用一次。c 为 nil 表示压缩关闭——
// /compact 返回 compaction_disabled，自动压缩在 manager 侧见 nil 直接跳过。
// 本方法同时把协调器转发注入 ConversationManager，使 runOneLLM 每轮自动压缩生效。
func (h *Handler) SetCompactor(c *memctx.Compactor) {
	h.compactor = c
	h.conv.SetCompactor(c)
}

// SetToolResultStore 注入第一层压缩的工具结果存盘器（Step 7）。
// 应在 main.go 构造 Handler 后、启动服务前调用一次。store 由 main.go 无条件构造
// （NewToolResultStore 纯内存无 IO），使 /clear 的工具结果清理能力与压缩总开关解耦。
// 为 nil 时 handleClearSession 跳过 tool_results 清理，不影响清空消息。
func (h *Handler) SetToolResultStore(s *memctx.ToolResultStore) {
	h.toolResultStore = s
}

// SetReviewer 注入自动学习记忆的后台回顾器（Step 8）。
// 应在 main.go 构造 Handler 后、启动服务前调用一次。reviewer 持有 provider + store +
// 配置，由 runStream 装配到每轮 AgentLoop 的 OnLoopDone 回调。为 nil 时（记忆总开关
// 关闭）OnLoopDone 直接跳过回顾，主流程不受影响。ReviewerConfig.Enabled=false 时
// 回顾器内部短路，故可无脑注入、由配置决定是否真正触发。
func (h *Handler) SetReviewer(r *autolearn.Reviewer) {
	h.reviewer = r
}

// handleCompact 处理前端 /compact 斜杠命令或状态栏「压缩」按钮的请求：
// 触发一次手动上下文压缩（第二层摘要，无视余量与熔断）。
//
// 串行化：复用 streamState 抢占——与 user_input / 再次 compact 互斥，避免手动压缩与
// 正在进行的 AgentLoop 并发改写 history（ReplaceHistory 与 GetContext 并发不安全）。
// busy 时返回 stream_error(busy)。压缩本身异步执行（runManualCompact goroutine），
// 不阻塞 HandleLoop 消息循环。
func (h *Handler) handleCompact(conn *websocket.Conn, msg Message) error {
	if h.compactor == nil {
		return h.sendStreamError(conn, "compaction_disabled",
			"上下文压缩未启用（setting.json 中 compaction.enabled=false）")
	}
	ctx, busy := h.stream.tryAcquire()
	if busy {
		return h.sendStreamError(conn, "busy", "当前已有请求进行中，请稍后再试")
	}
	go h.runManualCompact(ctx, conn)
	return nil
}

// runManualCompact 在独立 goroutine 中执行手动压缩并推送结果。
//
// 流程：状态切到「压缩中」→ 调协调器 Compact(manual=true) → 推送 compaction_event
// （manual=true；即使 Level=none 也推送，让前端反馈「无需压缩」）→ 刷新用量 → 切回 idle。
// 失败时额外推 stream_error，err 文案同时进入 compaction_event.Err 供前端展示根因。
// defer 保证 streamState 释放与 pendingConn 清理，panic 安全。
//
// 可中断：Compact 内部用本 ctx 调 LLM，用户点 Stop（abort_stream）会 cancel 该 ctx，
// 摘要调用随之返回 ctx.Err，runManualCompact 正常收尾。
func (h *Handler) runManualCompact(ctx context.Context, conn *websocket.Conn) {
	h.mu.Lock()
	h.pendingConn = conn
	sessionID := ""
	if h.current != nil {
		sessionID = h.current.ID
	}
	h.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			logger.Error("runManualCompact panic，已恢复",
				zap.Any("panic", r),
				zap.String("stack", string(debug.Stack())),
			)
			_ = h.sendStreamError(conn, "internal_error", fmt.Sprintf("压缩内部错误: %v", r))
		}
		h.mu.Lock()
		h.pendingConn = nil
		h.mu.Unlock()
		h.stream.release(ctx)
		_ = h.sendStatusUpdate(conn, StatusIdle)
	}()

	_ = h.sendStatusUpdate(conn, StatusCompacting)

	res, err := h.compactor.Compact(ctx, h.provider, h.conv, sessionID, true)
	h.dispatchCompactHook(ctx, sessionID, res)
	// 总是推送压缩事件（manual=true）；Level=none 时前端据此提示「当前无需压缩」。
	_ = h.sendCompactionEvent(conn, res, true)
	// 压缩改变了历史，刷新前端用量展示。
	_ = h.sendContextUsage(conn)

	if err != nil {
		_ = h.sendStreamError(conn, "compaction_failed", fmt.Sprintf("压缩失败: %v", err))
	}
}

// handleDump 处理前端 /dump 斜杠命令：把当前会话内存中的完整历史上下文 +
// System Prompt 快照导出为会话目录下的 dump.json / dump.md 两份文件。
//
// 流程（全程同步、ms 级，不开 goroutine）：
//  1. stream.tryAcquire 抢占流式状态；busy（已有 runStream / 压缩进行中）→
//     返回 stream_error(busy)。tryAcquire 成功后保证无并发 runStream 改写
//     history，此刻取到的快照是一致的。
//  2. defer release 兜底释放流式状态。
//  3. 持 h.mu 临界区复制 sp 快照、取 current 会话元信息与 sessionID；
//     current 为 nil（理论不会，构造时即创建/恢复）→ no_active_session。
//  4. AllMessages() 取历史副本（tryAcquire 已保证无并发写入）。
//  5. buildSessionDump + writeDumpFiles 落盘到 SessionDir。
//  6. 成功 → dump_result（含两个绝对路径）；失败 → stream_error(dump_failed)。
//
// [为什么同步执行] dump 只做内存读 + 两次小文件写，无 LLM 调用、无阻塞 IO，
// 同步执行比异步 goroutine 更简单（无需 pendingConn 管理与 panic 兜底），
// 且 tryAcquire 已串行化，不存在与 runStream 的并发。
func (h *Handler) handleDump(conn *websocket.Conn, msg Message) error {
	// 抢占流式状态：保证此刻无并发 runStream 改写 history，快照一致。
	ctx, busy := h.stream.tryAcquire()
	if busy {
		return h.sendStreamError(conn, "busy", "当前已有请求进行中，请稍后再试")
	}
	defer h.stream.release(ctx)

	// 持锁复制 SP 快照与会话元信息：h.sp 在 assembleSP 后同一会话内不变，
	// 此处复制值类型即可安全释放锁后使用。
	h.mu.Lock()
	sp := h.sp
	session := h.current
	h.mu.Unlock()

	if session == nil {
		return h.sendStreamError(conn, "no_active_session", "当前无活跃会话，无法导出")
	}

	// 历史副本：AllMessages 内部 copy，不含 leadUserMessage（已在 SP 段单独导出）。
	messages := h.conv.AllMessages()

	sd := buildSessionDump(session.ID, session.CreatedAt, session.UpdatedAt, sp, messages, time.Now())
	dir := h.sessMgr.SessionDir(session.ID)
	jsonPath, mdPath, err := writeDumpFiles(dir, sd)
	if err != nil {
		logger.Warn("会话导出写盘失败",
			zap.String("session_id", session.ID),
			zap.String("dir", dir),
			zap.Error(err),
		)
		return h.sendStreamError(conn, "dump_failed", fmt.Sprintf("导出失败: %v", err))
	}

	logger.Info("会话已导出",
		zap.String("session_id", session.ID),
		zap.Int("message_count", len(messages)),
		zap.String("json_path", jsonPath),
		zap.String("md_path", mdPath),
	)
	return h.sendMessage(conn, MsgTypeDumpResult, DumpResultPayload{
		OK:        true,
		JSONPath:  jsonPath,
		MDPath:    mdPath,
		SessionID: session.ID,
	})
}

// sendCompactionEvent 推送一轮压缩结果给前端（Step 7 可观测性）。
// manual 标识是否手动触发，前端据此对 summary 结果给更强提示（用户主动操作应明确反馈）。
func (h *Handler) sendCompactionEvent(conn *websocket.Conn, res memctx.CompactionResult, manual bool) error {
	errText := ""
	if res.Err != nil {
		errText = res.Err.Error()
	}
	return h.sendMessage(conn, MsgTypeCompactionEvent, CompactionEventPayload{
		Level:          string(res.Level),
		LightChanged:   res.LightChanged,
		SummaryChanged: res.SummaryChanged,
		ReplacedBlocks: res.ReplacedBlocks,
		BeforeTokens:   res.BeforeTokens,
		AfterTokens:    res.AfterTokens,
		Tripped:        res.Tripped,
		Manual:         manual,
		Err:            errText,
	})
}
func (h *Handler) sendMemoryReviewEvent(conn *websocket.Conn, evt autolearn.ReviewEvent) error {
	return h.sendMessage(conn, MsgTypeMemoryReviewEvent, MemoryReviewEventPayload{
		ReviewID:   evt.ReviewID,
		SessionID:  evt.SessionID,
		Status:     string(evt.Status),
		StartedAt:  evt.StartedAt,
		FinishedAt: evt.FinishedAt,
		DurationMs: evt.Duration.Milliseconds(),
		Total:      evt.Total,
		Applied:    evt.Applied,
		Err:        evt.Err,
	})
}

// SetInterceptor 设置权限拦截器并注入 HITL 回调。
// 应在 main.go 顶层构造后、启动服务前调用。
// interceptor 为 nil 时 ToolHandler 不做权限检查。
func (h *Handler) SetInterceptor(interceptor *security.Interceptor, checker *security.Checker) {
	h.interceptor = interceptor
	h.checker = checker
	if interceptor != nil {
		interceptor.SetHITLCallback(h.hitlCallback)
	}
}

// hitlCallback 是 HITL 确认的核心回调函数。
// 由 Interceptor 在 ActionAsk 时同步调用，负责：
//  1. 构造 permission_request WebSocket 消息并推送给前端
//  2. 通过 channel + select 等待用户响应或超时（默认 60 秒）
//  3. 处理 ScopePermanent 的配置文件写入
func (h *Handler) hitlCallback(ctx context.Context, req security.PermissionRequest) (security.PermissionResponse, error) {
	// 生成唯一请求 ID（使用时间戳纳秒后 8 位）
	id := fmt.Sprintf("perm_%d", time.Now().UnixNano()%1e8)

	// 构造匹配规则展示信息
	var matchedRule *PermissionMatchedRule
	if req.MatchedRule != nil {
		matchedRule = &PermissionMatchedRule{
			Tool:    req.MatchedRule.Tool,
			Pattern: req.MatchedRule.Pattern,
			Action:  string(req.MatchedRule.Action),
		}
	}

	// 注册等待 channel
	respCh := make(chan security.PermissionResponse, 1)
	h.pendingMu.Lock()
	h.pendingPermissions[id] = respCh
	h.pendingMu.Unlock()
	defer func() {
		h.pendingMu.Lock()
		delete(h.pendingPermissions, id)
		h.pendingMu.Unlock()
	}()

	// 获取当前活跃的 WebSocket 连接
	// hitlCallback 在 runStream goroutine 内被同步调用，
	// 此时 conn 已在 goroutine 闭包中，需要通过 pendingPermissions 机制路由响应。
	// 为获取 conn，我们把 conn 存入 context 或通过 Handler 字段传递。
	// 简化方案：使用 Handler 的 pendingConn 记录当前活跃连接。
	conn := h.getActiveConn()
	if conn == nil {
		return security.PermissionResponse{}, fmt.Errorf("无可用的 WebSocket 连接")
	}

	// 发送 permission_request 给前端
	_ = h.sendMessage(conn, MsgTypePermissionRequest, PermissionRequestPayload{
		ID:            id,
		ToolName:      req.ToolName,
		ParamsSummary: req.ParamsSummary,
		Reason:        req.Reason,
		MatchedRule:   matchedRule,
		TargetPath:    req.TargetPath,
		Workdir:       req.Workdir,
	})

	// 等待用户响应或超时（60 秒，独立于工具执行超时）
	const hitlTimeout = 60 * time.Second
	select {
	case resp := <-respCh:
		// 收到用户响应，处理 ScopePermanent 的配置文件写入
		// 路径类工具 + 有 TargetPath → 用 security.BuildPathPattern 生成
		// 目录级 Pattern（父目录 + /*），避免工具级豁免带来的安全风险。
		if resp.Allowed && resp.Scope == security.ScopePermanent {
			pattern := "*"
			if _, isPathTool := security.IsPathTool(req.ToolName); isPathTool && req.TargetPath != "" {
				pattern = security.BuildPathPattern(req.TargetPath, req.Workdir)
			}
			reason := "用户永久授权"
			if pattern != "*" {
				reason = fmt.Sprintf("用户永久授权：放行 %s", pattern)
			}
			h.handlePermanentAllow(req.ToolName, pattern, reason)
		}
		return resp, nil
	case <-time.After(hitlTimeout):
		return security.PermissionResponse{}, fmt.Errorf("权限确认超时（%s）", hitlTimeout)
	case <-ctx.Done():
		return security.PermissionResponse{}, ctx.Err()
	}
}

// handlePermissionResponse 处理前端发回的权限确认响应。
// 从 pendingPermissions 中找到对应的 channel，将响应传递给等待的 hitlCallback。
func (h *Handler) handlePermissionResponse(conn *websocket.Conn, msg Message) error {
	p, err := AsPayload[PermissionResponsePayload](msg)
	if err != nil {
		return h.sendStreamError(conn, "invalid_payload", err.Error())
	}

	h.pendingMu.Lock()
	ch, ok := h.pendingPermissions[p.ID]
	h.pendingMu.Unlock()

	if !ok {
		logger.Warn("收到未注册的权限响应",
			zap.String("id", p.ID),
		)
		return nil
	}

	// 非阻塞发送响应
	select {
	case ch <- security.PermissionResponse{
		Allowed: p.Allowed,
		Scope:   security.Scope(p.Scope),
	}:
	default:
		logger.Warn("权限响应 channel 已满，丢弃",
			zap.String("id", p.ID),
		)
	}
	return nil
}

// handleSetPermissionMode 处理前端「权限模式」下拉切换请求。
//
// 用户在状态栏点击 permission 区域会弹出 3 选 1 下拉（严格/默认/放行），
// 选中后前端发送 set_permission_mode{mode: "..."}，本 handler：
//  1. 校验 mode 合法性（必须是 strict / default / permissive）
//  2. 调用 Checker.SetMode() 立即生效
//  3. 通过 MsgTypePermissionMode 回推新档位给前端，状态栏 UI 同步更新
//  4. 不修改 setting.json（运行时切换，不影响磁盘配置）
//
// 运行时切换 vs 配置文件切换：
//   - 本接口是临时性切换，重启 CodePilot 后回到 setting.json 中配置的档位
//   - 若用户希望永久切换档位，需编辑 ~/.codepilot/setting.json 或 <cwd>/.codepilot/setting.json
func (h *Handler) handleSetPermissionMode(conn *websocket.Conn, msg Message) error {
	p, err := AsPayload[SetPermissionModePayload](msg)
	if err != nil {
		return h.sendStreamError(conn, "invalid_payload", err.Error())
	}

	// 校验：checker 为 nil 时旧会话无权限系统，直接拒绝
	if h.checker == nil {
		logger.Warn("收到 set_permission_mode 但 Checker 未初始化", zap.String("mode", p.Mode))
		return h.sendStreamError(conn, "permission_disabled", "权限系统未启用")
	}

	// 校验：mode 必须是合法档位（security.SetMode 内部也会校验，这里先校验一次以便日志）
	switch security.Mode(p.Mode) {
	case security.ModeStrict, security.ModeDefault, security.ModePermissive:
		// 合法
	default:
		logger.Warn("收到非法的 set_permission_mode", zap.String("mode", p.Mode))
		return h.sendStreamError(conn, "invalid_mode", "非法的权限模式: "+p.Mode)
	}

	// 校验：避免无意义的「同档位切换」日志噪声
	oldMode := h.checker.Mode()
	if security.Mode(p.Mode) == oldMode {
		logger.Debug("set_permission_mode 与当前档位相同，跳过", zap.String("mode", p.Mode))
	} else {
		h.checker.SetMode(security.Mode(p.Mode))
		logger.Info("权限模式已切换",
			zap.String("from", string(oldMode)),
			zap.String("to", p.Mode),
		)
	}

	// 回推新档位给前端（前端无须本地更新 UI）
	return h.sendPermissionMode(conn)
}

// handlePermanentAllow 将"永久允许"规则写入对应的 setting.json 配置文件。
// 优先写入项目级配置（.codepilot/setting.json），否则写入全局配置。
// 写入失败时降级为会话级规则（不阻断流程）。
//
// 参数：
//   - toolName: 工具名（大驼峰，如 "ReadFile"）
//   - pattern:  规则 Pattern，对路径类工具是目录级 glob（如 "/tmp/*"），
//     对非路径类工具是 "*"（工具级豁免）
//   - reason:   规则 Reason，会写入配置文件
func (h *Handler) handlePermanentAllow(toolName, pattern, reason string) {
	if pattern == "" {
		pattern = "*"
	}
	if reason == "" {
		reason = "用户永久授权"
	}
	rule := config.RuleConfig{
		Tool:    toolName,
		Pattern: pattern,
		Action:  "allow",
		Reason:  reason,
	}

	// 尝试写入项目级配置
	if h.workdir != "" {
		projectConfigPath := filepath.Join(h.workdir, ".codepilot", "setting.json")
		if err := writeRuleToConfig(projectConfigPath, rule); err == nil {
			logger.Info("永久允许规则已写入项目配置",
				zap.String("path", projectConfigPath),
				zap.String("tool", toolName),
				zap.String("pattern", pattern),
			)
			return
		}
	}

	// 回退到全局配置
	if h.cfg != nil {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			globalConfigPath := filepath.Join(homeDir, ".codepilot", "setting.json")
			if err := writeRuleToConfig(globalConfigPath, rule); err == nil {
				logger.Info("永久允许规则已写入全局配置",
					zap.String("path", globalConfigPath),
					zap.String("tool", toolName),
					zap.String("pattern", pattern),
				)
				return
			}
		}
	}

	// 写入失败：降级为会话级规则
	if h.checker != nil {
		h.checker.AddSessionRule(security.Rule{
			Tool:    toolName,
			Pattern: pattern,
			Action:  security.ActionAllow,
			Reason:  "用户永久授权（配置写入失败，降级为会话级）",
		})
	}
	logger.Warn("永久允许规则写入配置文件失败，已降级为会话级规则",
		zap.String("tool", toolName),
	)
}

// getActiveConn 获取当前活跃的 WebSocket 连接。
// 由于 hitlCallback 在 runStream goroutine 内被同步调用，
// 此时 runStream 闭包中的 conn 就是活跃连接。
// 简化实现：通过 pendingConn 字段追踪。
func (h *Handler) getActiveConn() *websocket.Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	// pendingConn 在 runStream 启动时设置
	if h.pendingConn != nil {
		return h.pendingConn
	}
	return nil
}

// sendPermissionMode 推送当前权限模式到前端。
func (h *Handler) sendPermissionMode(conn *websocket.Conn) error {
	mode := "default"
	ruleCount := 0
	sessionRuleCount := 0
	if h.checker != nil {
		mode = string(h.checker.Mode())
		ruleCount = h.checker.RuleCount()
		sessionRuleCount = h.checker.SessionRuleCount()
	}
	return h.sendMessage(conn, MsgTypePermissionMode, PermissionModePayload{
		Mode:             mode,
		RuleCount:        ruleCount,
		SessionRuleCount: sessionRuleCount,
	})
}

// buildSPEnv 构造 prompt.Builder.Assemble 所需的 Env 输入。
// 数据源：cfg + workdir + 进程启动时间 + VERSION。
//
// 注意：此函数内不做任何文件 / git 命令调用——所有「现场采集」由各 Source
// 内部按需进行，handler 仅负责传最基础的静态字段。
func buildSPEnv(cfg *config.Config, workdir string) sources.Env {
	env := sources.Env{
		OS:              runtime.GOOS,
		CWD:             workdir,
		Date:            time.Now().Format("2006-01-02"),
		StaticOverrides: nil,
	}
	// 预留：未来 cfg 中可加 SystemPromptConfig.StaticOverrides 注入
	return env
}

// convertToLLMSystemPrompt 把 prompt/sources 包产出的 SystemPrompt
// 转换为 llm.SystemPrompt（Provider 接收的形态）。
//
// 两者结构体字段一致，浅拷贝即可；保留独立类型是为避免 prompt → llm 的
// 循环依赖。
func convertToLLMSystemPrompt(in sources.SystemPrompt) llm.SystemPrompt {
	out := llm.SystemPrompt{
		LeadUserMessage: in.LeadUserMessage,
		TotalTokens:     in.TotalTokens,
	}
	if len(in.SystemBlocks) > 0 {
		out.SystemBlocks = make([]llm.SystemBlock, len(in.SystemBlocks))
		for i, b := range in.SystemBlocks {
			out.SystemBlocks[i] = llm.SystemBlock{Text: b.Text, Cacheable: b.Cacheable}
		}
	}
	if len(in.Stats) > 0 {
		out.Stats = make([]llm.SourceStat, len(in.Stats))
		for i, s := range in.Stats {
			out.Stats[i] = llm.SourceStat{Name: s.Name, Tokens: s.Tokens}
		}
	}
	return out
}

// sendMessage 编码并发送一条带 payload 的消息。
// 内部通过 writeMu 串行化 WebSocket 写操作，防止多个 goroutine 并发写入
// 导致 gorilla/websocket "concurrent write" panic。
// 失败仅记录日志，不返回错误。
func (h *Handler) sendMessage(conn *websocket.Conn, typ string, payload any) error {
	data, err := EncodePayload(typ, payload)
	if err != nil {
		logger.Warn("编码消息失败", zap.String("type", typ), zap.Error(err))
		return err
	}
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		logger.Warn("发送消息失败", zap.String("type", typ), zap.Error(err))
		return err
	}
	return nil
}

// sendStreamChunk 发送流式 chunk。
func (h *Handler) sendStreamChunk(conn *websocket.Conn, delta string) error {
	return h.sendMessage(conn, MsgTypeStreamChunk, StreamChunkPayload{Delta: delta})
}

// sendStreamDone 发送流式结束。
func (h *Handler) sendStreamDone(conn *websocket.Conn, reason string) error {
	return h.sendMessage(conn, MsgTypeStreamDone, StreamDonePayload{Reason: reason})
}

// sendStreamError 发送流式错误。
func (h *Handler) sendStreamError(conn *websocket.Conn, code, message string) error {
	return h.sendMessage(conn, MsgTypeStreamError, StreamErrorPayload{Code: code, Message: message})
}

// sendToolCallStart 发送工具调用开始事件。
func (h *Handler) sendToolCallStart(conn *websocket.Conn, p ToolCallStartPayload) error {
	return h.sendMessage(conn, MsgTypeToolCallStart, p)
}

// sendToolCallEnd 发送工具调用结束事件。
func (h *Handler) sendToolCallEnd(conn *websocket.Conn, p ToolCallEndPayload) error {
	return h.sendMessage(conn, MsgTypeToolCallEnd, p)
}

// sendAgentIteration 发送 Agent Loop 迭代进度事件。
// 每轮迭代开始时调用，告知前端当前迭代序号和最大迭代次数。
func (h *Handler) sendAgentIteration(conn *websocket.Conn, p AgentIterationPayload) error {
	return h.sendMessage(conn, MsgTypeAgentIteration, p)
}

// sendFileDiff 发送 file_diff 响应（响应 get_file_diff 请求）。
// payload.Found=false 时 Reason 必填，前端据此区分文案（not_found / too_large）。
func (h *Handler) sendFileDiff(conn *websocket.Conn, p FileDiffPayload) error {
	return h.sendMessage(conn, MsgTypeFileDiff, p)
}

func (h *Handler) sendProjectDir(conn *websocket.Conn, p ProjectDirPayload) error {
	return h.sendMessage(conn, MsgTypeProjectDir, p)
}

func (h *Handler) sendProjectFile(conn *websocket.Conn, p ProjectFilePayload) error {
	return h.sendMessage(conn, MsgTypeProjectFile, p)
}

// mapToolEventStatus 把 conversation 包的内部工具事件状态枚举
// 映射为 web 包对外的 ToolCallStatus* 常量（与前端约定保持一致）。
// 对应关系：running/completed/error/aborted 直接透传；toolHandler 没有
// 单独的 timeout 枚举，被归类为 error，前端在 status='error' 时可读 is_error 区分。
func mapToolEventStatus(s string) string {
	switch s {
	case conversation.ToolEventStatusRunning:
		return ToolCallStatusRunning
	case conversation.ToolEventStatusCompleted:
		return ToolCallStatusCompleted
	case conversation.ToolEventStatusError:
		return ToolCallStatusError
	case conversation.ToolEventStatusAborted:
		return ToolCallStatusAborted
	default:
		return ToolCallStatusError
	}
}

// mapStopReason 将 AgentLoop 的 StopReason 枚举映射为前端 stream_done 的 reason 字符串。
// 对应关系：completed → completed, aborted → aborted, error → error,
// max_iterations → max_iterations, context_overflow → context_overflow。
func mapStopReason(reason conversation.StopReason) string {
	switch reason {
	case conversation.StopReasonCompleted:
		return StreamReasonCompleted
	case conversation.StopReasonAborted:
		return StreamReasonAborted
	case conversation.StopReasonError:
		return StreamReasonError
	case conversation.StopReasonMaxIterations:
		return StreamReasonMaxIterations
	case conversation.StopReasonContextOverflow:
		return StreamReasonContextOverflow
	default:
		return StreamReasonError
	}
}

// sendStatusUpdate 发送状态更新。
func (h *Handler) sendStatusUpdate(conn *websocket.Conn, status string) error {
	return h.sendMessage(conn, MsgTypeStatusUpdate, StatusUpdatePayload{Status: status})
}

// sendContextUsage 发送当前上下文使用情况。
// 通过 ConversationManager.GetContextUsage 获取统一的用量计算结果，
// 转换为前端协议格式（PercentLeft = 100 - PercentUsed）后推送。
// 同时携带 System Prompt 的总 token 数与各 Source 小计（Step 4 可观测性）。
// sendContextUsage 推送上下文用量到前端（自动加锁版本，供未持锁的调用方使用）。
func (h *Handler) sendContextUsage(conn *websocket.Conn) error {
	h.mu.Lock()
	usage := h.conv.GetContextUsage(h.contextWindowSize)
	spSnapshot := h.sp
	h.mu.Unlock()
	return h.sendContextUsageLocked(conn, usage, spSnapshot)
}

// sendContextUsageLocked 推送上下文用量到前端（无锁版本，供已持有 h.mu 的调用方使用）。
// sp 为 System Prompt 快照，由调用方在持锁状态下从 h.sp 复制后传入，
// 避免持锁状态下再访问 h.sp 引发竞态。
func (h *Handler) sendContextUsageLocked(conn *websocket.Conn, usage conversation.ContextUsage, sp llm.SystemPrompt) error {
	payload := ContextUsagePayload{
		Used:        usage.Used,
		Limit:       usage.Limit,
		PercentLeft: 100 - usage.PercentUsed,
	}
	// Step 4 可观测性：携带 SP 总 token 与各 Source 小计
	payload.SPTotalTokens = sp.TotalTokens
	if len(sp.Stats) > 0 {
		payload.SPBreakdown = make([]SPSourceStat, 0, len(sp.Stats))
		for _, s := range sp.Stats {
			payload.SPBreakdown = append(payload.SPBreakdown, SPSourceStat{
				Name:   s.Name,
				Tokens: s.Tokens,
			})
		}
	}
	return h.sendMessage(conn, MsgTypeContextUsage, payload)
}

// sendSessionLoaded 发送 session_loaded 消息（Step 2: 支持工具消息回放）。
//
// 工具消息处理：assistant 同时含 text + tool_use 时拆成两条 ChatMessage
// （text 保持原样、tool_use 转为带 ToolCall 的展示条），user 消息里的
// tool_result 块因为已经在 ToolCallDisplay.Output 里体现，故跳过。
func (h *Handler) sendSessionLoaded(conn *websocket.Conn, sess *memsession.Session) error {
	chatMsgs := buildChatMessages(sess.Messages)
	// Step 8:历史会话中 MCP 远端工具的 server 来源回填。
	// buildChatMessages 是 free function,不在 h 上;此处统一遍历一次 ChatMessage
	// 给 ToolCall.Server 赋值,避免改动 buildChatMessages 签名。
	for i := range chatMsgs {
		if chatMsgs[i].ToolCall != nil {
			chatMsgs[i].ToolCall.Server = h.resolveMCPServerByToolName(chatMsgs[i].ToolCall.Name)
		}
	}
	summary := SessionSummary{
		ID:           sess.ID,
		CreatedAt:    sess.CreatedAt,
		UpdatedAt:    sess.UpdatedAt,
		MessageCount: len(sess.Messages),
		Preview:      firstUserPreview(sess.Messages),
	}
	if err := h.sendMessage(conn, MsgTypeSessionLoaded, SessionLoadedPayload{
		SessionID: sess.ID,
		Summary:   summary,
		Messages:  chatMsgs,
		Model:     h.ModelName(),
		Workdir:   h.workdir,
	}); err != nil {
		return err
	}
	// 会话加载完成后推送当前权限模式（状态栏展示）
	_ = h.sendPermissionMode(conn)
	// Step 8:同步推送 MCP 状态,让状态栏 MCP 区在会话加载后立刻有内容
	_ = h.sendMCPStatus(conn)
	return nil
}

// buildChatMessages 把 llm.Message 列表转换为前端 ChatMessage 列表，
// 集中处理 tool_use / tool_result / text 混排的拆分与配对。
//
// 规则：
//   - assistant 同时含 text + tool_use → 两条 ChatMessage（text 在前，ToolCall 在后）
//   - assistant 仅含 tool_use          → 一条 ChatMessage（仅 ToolCall）
//   - assistant 仅含 text               → 一条 ChatMessage（仅 Content）
//   - user 仅含 tool_result            → 跳过（已合并到对应 ToolCall.Output）
//   - user 含 text（含或不含 tool_result）→ 一条 ChatMessage
//
// 配对失败的 ToolUse（无对应 ToolResult）展示为 status=error / Output=""，
// 避免前端拿到残缺数据；这是边角情况，正常 RunTurn 总会回写 tool_result。
func buildChatMessages(messages []llm.Message) []ChatMessage {
	// 先建立 toolUseID -> ToolResultBlock 的索引，便于 O(1) 配对
	results := make(map[string]llm.ToolResultBlock)
	for _, m := range messages {
		if m.Role != llm.RoleUser {
			continue
		}
		for _, b := range m.Content {
			if tr, ok := b.(*llm.ToolResultBlock); ok {
				results[tr.ToolUseID] = *tr
			}
		}
	}

	out := make([]ChatMessage, 0, len(messages))
	for _, m := range messages {
		// user 消息中纯 tool_result 块：跳过
		if m.Role == llm.RoleUser && isOnlyToolResults(m.Content) {
			continue
		}

		var textParts []string
		for _, b := range m.Content {
			if tb, ok := b.(*llm.TextBlock); ok {
				if tb.Text != "" {
					textParts = append(textParts, tb.Text)
				}
			}
		}
		textContent := strings.Join(textParts, "\n")

		// 先放 text（若有）
		if textContent != "" {
			out = append(out, ChatMessage{
				Role:    string(m.Role),
				Content: textContent,
			})
		}

		// 再为每个 ToolUse 放一条 ToolCall 消息（仅 assistant 角色会出现 tool_use）
		for _, b := range m.Content {
			tu, ok := b.(*llm.ToolUseBlock)
			if !ok {
				continue
			}
			tr, hasResult := results[tu.ID]
			status := ToolCallStatusCompleted
			isErr := false
			output := ""
			if hasResult {
				isErr = tr.IsError
				output = SummarizeOutput(tr.Content)
				if isErr {
					status = ToolCallStatusError
				}
			} else {
				// 没有配对的 result（异常情况）：标记为 error
				status = ToolCallStatusError
				isErr = true
			}
			display := ToolDisplayFromExecution(
				tu.ID, tu.Name,
				SummarizeInput(tu.Input),
				output, isErr, 0, status,
				"", // server 字段在循环外统一设置(避免把 h 传给 free function)
			)
			out = append(out, ChatMessage{
				Role:     string(llm.RoleAssistant),
				Content:  "",
				ToolCall: &display,
			})
		}
	}
	return out
}

// isOnlyToolResults 判断 ContentBlock 数组是否全部是 ToolResultBlock。
// 用于 buildChatMessages 决定是否跳过整条 user 消息。
func isOnlyToolResults(blocks []llm.ContentBlock) bool {
	if len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if _, ok := b.(*llm.ToolResultBlock); !ok {
			return false
		}
	}
	return true
}

// extractText 从 ContentBlock 数组中提取首段文本（多模态尚未启用）。
// 仅用于 firstUserPreview 等"取一段用户消息文本"的旧逻辑。
func extractText(blocks []llm.ContentBlock) string {
	for _, b := range blocks {
		if t := b.ToText(); t != "" {
			return t
		}
	}
	return ""
}

// firstUserPreview 返回首条用户消息的前 N 字符预览。
func firstUserPreview(messages []llm.Message) string {
	const maxLen = 80
	for _, m := range messages {
		if m.Role == llm.RoleUser {
			text := strings.TrimSpace(extractText(m.Content))
			if text == "" {
				continue
			}
			runes := []rune(text)
			if len(runes) <= maxLen {
				return text
			}
			return string(runes[:maxLen-3]) + "..."
		}
	}
	return "(空会话)"
}

// writeRuleToConfig 将一条权限规则追加到指定 setting.json 文件中。
// 使用"读取-合并-写回"策略，保留文件中已有的其他配置字段。
// 文件不存在时自动创建（含目录）；写入失败时返回错误。
func writeRuleToConfig(configPath string, rule config.RuleConfig) error {
	// 确保目录存在
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}

	// 读取现有配置（文件不存在时使用空对象）
	var raw map[string]json.RawMessage
	if data, err := os.ReadFile(configPath); err == nil {
		_ = json.Unmarshal(data, &raw)
	}
	if raw == nil {
		raw = make(map[string]json.RawMessage)
	}

	// 解析现有 permissions
	var perms config.PermissionsConfig
	if permRaw, ok := raw["permissions"]; ok {
		_ = json.Unmarshal(permRaw, &perms)
	}

	// 追加新规则
	perms.Rules = append(perms.Rules, rule)

	// 写回 permissions 字段
	permData, err := json.Marshal(perms)
	if err != nil {
		return fmt.Errorf("序列化 permissions 失败: %w", err)
	}
	raw["permissions"] = json.RawMessage(permData)

	// 整体写回文件
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	return os.WriteFile(configPath, data, 0644)
}

// ---- 流式状态机 ----

// streamState 保证同一时刻只有一个流式请求。
//   - tryAcquire 返回 (ctx, busy)；busy=true 时 ctx 为 nil
//   - release 取消 ctx、释放状态
//   - abort 仅取消 ctx，不释放（由 runStream defer 释放）
type streamState struct {
	mu       sync.Mutex
	cancelFn context.CancelFunc
	active   bool
}

// tryAcquire 尝试进入流式状态。
// 成功：返回可取消的 ctx 与 true；失败：返回 nil, true(busy=true 表示当前已忙)。
func (s *streamState) tryAcquire() (context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return nil, true
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelFn = cancel
	s.active = true
	return ctx, false
}

// release 退出流式状态。重复调用安全。
func (s *streamState) release(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelFn != nil {
		s.cancelFn()
		s.cancelFn = nil
	}
	// 若 ctx 已与当前 cancelFn 匹配，调用 cancel 是幂等的
	_ = ctx
	s.active = false
}

// abort 仅取消 ctx（用于 abort_stream 路径），状态释放由 runStream defer 完成。
// 无活跃流时返回 false。
func (s *streamState) abort() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || s.cancelFn == nil {
		return false
	}
	s.cancelFn()
	return true
}
