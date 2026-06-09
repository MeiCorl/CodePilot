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
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/mcp/adapter"
	mcpsession "github.com/MeiCorl/CodePilot/src/internal/mcp/session"
	memsession "github.com/MeiCorl/CodePilot/src/internal/memory/session"
	"github.com/MeiCorl/CodePilot/src/internal/security"
	"github.com/MeiCorl/CodePilot/src/llm"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// mcpServerByName 缓存 MCP 远端工具名 → server 名的映射(由 mcpPool 在 SetMCPPool 时填充)。
// 解析远端工具的 server 来源不依赖 adapter(避免 web → mcp 内部细节泄漏),只
// 依赖 adapter.ToolNamePrefix / nameSeparator 拆分命名。
var _ = adapter.ToolNamePrefix // 保留 import(防止未来误删导致 server 解析静默失败)

// defaultContextWindowSize 为 Handler 层的兜底默认值。
// 正常情况下 ContextWindowSize 由 Config 层（setting.json）提供并通过 main.go 传入，
// 此常量仅在传入值 <= 0 时作为安全回退。
const defaultContextWindowSize = 200000

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
	// fileDiffStore 是 WriteFile/EditFile 工具写入的 diff 数据存储。
	// Step 1.4 接入；Task 3 (get_file_diff 协议) 真正消费此字段。
	// 为 nil 时前端请求 diff 会得到 not_found 提示，等价于"未启用 diff 预览"。
	fileDiffStore *FileDiffStore

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

	// writeMu 保护 WebSocket 写操作的互斥锁。
	// gorilla/websocket 要求同一时刻只有一个 writer；Handler 的读循环 goroutine
	// （HandleLoop）和流式输出 goroutine（runStream）都会向 conn 写消息，
	// 必须通过 writeMu 串行化以避免 "concurrent write to websocket connection" panic。
	writeMu sync.Mutex

	stream streamState

	// mcpPool 是 MCP server 连接池（Step 8）。nil 时 MCP 相关消息跳过。
	// 提供两个能力：
	//   1. 通过 mcpPool.HealthyNames() 等查询健康状态,填充 mcp_status payload
	//   2. 通过 mcp/adapter adapter 命名规则(`mcp__<server>__<tool>`)解析
	//      远端工具的 server 名称,填入 ToolCallStartPayload.Server
	mcpPool *mcpsession.Pool
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
		provider:          provider,
		sessMgr:           sessMgr,
		cfg:               cfg,
		conv:              conversation.NewConversationManager(maxRounds),
		promptBuilder:     promptBuilder,
		contextWindowSize: contextWindowSize,
		workdir:           workdir,
		registry:          registry,
		toolHandler:       toolHandler,
		fileDiffStore:     fileDiffStore,
		pendingPermissions: make(map[string]chan security.PermissionResponse),
	}
	// 尝试恢复最近一个会话
	if latest, err := sessMgr.LoadLatest(); err == nil && latest != nil {
		h.current = latest
		h.conv.Reset(latest.Messages)
		logger.Info("Handler 已恢复最近会话",
			zap.String("session_id", latest.ID),
			zap.Int("message_count", len(latest.Messages)),
		)
	} else {
		h.current = sessMgr.CreateNew()
	}
	// 初次组装 System Prompt（同一会话内复用，避免每次 LLM 调用都重新 assemble）
	h.assembleSP()
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
	router.Register(MsgTypePermissionResponse, h.handlePermissionResponse)
	router.Register(MsgTypeSetPermissionMode, h.handleSetPermissionMode)
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

// ---- 消息 handler ----

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

	// 启动 goroutine 处理流式
	go h.runStream(acquired, conn)

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
// ctx 在 abort_stream 时被 cancel；AgentLoop 内部会响应 ctx 取消并
// 把当前 LLM 流 / 工具执行一并中断，由 runStream 根据 StopReason 映射退出原因。
func (h *Handler) runStream(ctx context.Context, conn *websocket.Conn) {
	// 记录当前活跃 WebSocket 连接，供 HITL 回调使用
	h.mu.Lock()
	h.pendingConn = conn
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
		// OnLoopDone 在 AgentLoop 结束后回调，用于增量保存会话。
		// 确保即使 AgentLoop 正常完成，会话也能在 stream_done 之前落盘。
		OnLoopDone: func(result conversation.AgentLoopResult) {
			h.saveCurrentSession()
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

// saveCurrentSession 把当前 ConversationManager 中的完整历史持久化到磁盘。
// 无论成功还是失败都不影响调用方继续运行；失败仅记录日志。
func (h *Handler) saveCurrentSession() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current == nil {
		return
	}
	h.current.Messages = h.conv.AllMessages()
	if err := h.sessMgr.Save(h.current); err != nil {
		logger.Warn("会话增量保存失败",
			zap.String("session_id", h.current.ID),
			zap.Error(err),
		)
	}
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
		h.mu.Unlock()
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
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.current != nil && len(h.current.Messages) > 0 {
		if err := h.sessMgr.Save(h.current); err != nil {
			logger.Warn("保存当前会话失败", zap.Error(err))
		}
	}
	h.current = h.sessMgr.CreateNew()
	h.conv.Reset(nil)
	// 新会话触发 SP 重新组装（虽然 result 通常一致，但保持与切换路径一致的处理）
	h.assembleSP()
	// 刷新前端 ctx left 显示：新会话无历史，remaining 应回到 ~100%
	// 注意：此处已持有 h.mu，必须使用 Locked 版本避免死锁
	usage := h.conv.GetContextUsage(h.contextWindowSize)
	_ = h.sendContextUsageLocked(conn, usage, h.sp)
	return h.sendSessionLoaded(conn, h.current)
}

// handleClearSession 清空当前会话的上下文：保留 session_id，
// 把消息数组置空、重置 ConvMgr，并落盘覆盖。
// 与 handleNewSession 的差异：不创建新会话，不在左侧历史中新增条目。
func (h *Handler) handleClearSession(conn *websocket.Conn, msg Message) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.current == nil {
		h.current = h.sessMgr.CreateNew()
	}
	h.current.Messages = nil
	h.conv.Reset(nil)
	// 清空也触发 SP 重新组装（与切换会话路径保持一致语义）
	h.assembleSP()
	// 覆盖写一份空会话，让历史列表预览/计数也同步清零
	if err := h.sessMgr.Save(h.current); err != nil {
		logger.Warn("保存清空后的会话失败", zap.Error(err))
	}
	// 刷新前端 ctx left 显示：清空后历史为空，remaining 应回到 ~100%
	// 注意：此处已持有 h.mu，必须使用 Locked 版本避免死锁
	usage := h.conv.GetContextUsage(h.contextWindowSize)
	_ = h.sendContextUsageLocked(conn, usage, h.sp)
	return h.sendSessionLoaded(conn, h.current)
}

// handleResumeSession 通过 ID 前缀匹配恢复历史会话。
//   - 0 匹配：stream_error(session_not_found)
//   - 1 匹配：加载、注入历史到 ConvMgr
//   - 多匹配：stream_error(session_ambiguous)
func (h *Handler) handleResumeSession(conn *websocket.Conn, msg Message) error {
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

	h.mu.Lock()
	// 保存当前会话（如有消息）
	if h.current != nil && len(h.current.Messages) > 0 {
		_ = h.sessMgr.Save(h.current)
	}
	h.current = sess
	h.conv.Reset(sess.Messages)
	// 切换会话后重新组装 SP（确保与新会话上下文一致）
	h.assembleSP()
	h.mu.Unlock()

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
		SystemBlocks:   systemTexts,
		LeadUserMessage: sp.LeadUserMessage,
		Stats:          stats,
		TotalTokens:    sp.TotalTokens,
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

// SetMCPPool 注入 MCP 连接池。
// 应在 main.go 启动流程中、构造 Handler 之后调用一次。
// pool 为 nil 时 MCP 相关能力（远端工具 server 解析 + 状态栏 mcp_status 推送）禁用。
func (h *Handler) SetMCPPool(pool *mcpsession.Pool) {
	h.mcpPool = pool
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
		Mode:            mode,
		RuleCount:       ruleCount,
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
		OS:     runtime.GOOS,
		CWD:    workdir,
		Date:   time.Now().Format("2006-01-02"),
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
