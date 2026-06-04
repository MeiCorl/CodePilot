package web

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/engine/conversation"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/memory/session"
	"github.com/MeiCorl/CodePilot/src/llm"
	"github.com/MeiCorl/CodePilot/src/tool"
)

// DefaultContextWindowSize 模型上下文窗口默认大小（token 数）。
// 当 Config 未指定时使用该值，用于 context_usage 状态栏展示。
const DefaultContextWindowSize = 200000

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
type Handler struct {
	provider           llm.Provider
	sessMgr            *session.SessionManager
	cfg                *config.Config
	conv               *conversation.ConversationManager
	systemPrompt       string
	contextWindowSize  int
	workdir            string
	registry           *tool.Registry
	toolHandler        *conversation.ToolHandler

	mu      sync.Mutex
	current *session.Session

	// writeMu 保护 WebSocket 写操作的互斥锁。
	// gorilla/websocket 要求同一时刻只有一个 writer；Handler 的读循环 goroutine
	// （HandleLoop）和流式输出 goroutine（runStream）都会向 conn 写消息，
	// 必须通过 writeMu 串行化以避免 "concurrent write to websocket connection" panic。
	writeMu sync.Mutex

	stream streamState
}

// NewHandler 构造 Handler。
// 构造时会尝试 sessMgr.LoadLatest() 恢复最近会话；无历史时创建新会话（不立即落盘）。
// workdir 启动时获取，会随 session_loaded 透传给前端。
// registry 为 nil 时 RunTurn 不会携带任何工具描述（与未启用工具等价）；
// toolHandler 为 nil 时 RunTurn 仍可工作（无 tool_use 分发能力，LLM 不会调工具）。
func NewHandler(
	provider llm.Provider,
	sessMgr *session.SessionManager,
	cfg *config.Config,
	maxRounds int,
	systemPrompt string,
	contextWindowSize int,
	workdir string,
	registry *tool.Registry,
	toolHandler *conversation.ToolHandler,
) *Handler {
	if contextWindowSize <= 0 {
		contextWindowSize = DefaultContextWindowSize
	}
	h := &Handler{
		provider:          provider,
		sessMgr:           sessMgr,
		cfg:               cfg,
		conv:              conversation.NewConversationManager(maxRounds),
		systemPrompt:      systemPrompt,
		contextWindowSize: contextWindowSize,
		workdir:           workdir,
		registry:          registry,
		toolHandler:       toolHandler,
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
	return h
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
		MaxIterations:       25,
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

	result := h.conv.RunAgentLoop(ctx, h.provider, h.systemPrompt, toolSpecs, h.toolHandler, loopCfg, loopHooks)

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

	var summaries []session.SessionSummary
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
	// 覆盖写一份空会话，让历史列表预览/计数也同步清零
	if err := h.sessMgr.Save(h.current); err != nil {
		logger.Warn("保存清空后的会话失败", zap.Error(err))
	}
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

	var matches []session.SessionSummary
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
	h.mu.Unlock()

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
	var newCurrent *session.Session
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
		return h.sendSessionLoaded(conn, newCurrent)
	}
	return nil
}

// ---- 内部 send helper ----

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
func (h *Handler) sendContextUsage(conn *websocket.Conn) error {
	h.mu.Lock()
	used := h.conv.TokenEstimate()
	h.mu.Unlock()
	limit := h.contextWindowSize
	percentLeft := 0
	if limit > 0 {
		percentLeft = (limit - used) * 100 / limit
		if percentLeft < 0 {
			percentLeft = 0
		}
	}
	return h.sendMessage(conn, MsgTypeContextUsage, ContextUsagePayload{
		Used:        used,
		Limit:       limit,
		PercentLeft: percentLeft,
	})
}

// sendSessionLoaded 发送 session_loaded 消息（Step 2: 支持工具消息回放）。
//
// 工具消息处理：assistant 同时含 text + tool_use 时拆成两条 ChatMessage
// （text 保持原样、tool_use 转为带 ToolCall 的展示条），user 消息里的
// tool_result 块因为已经在 ToolCallDisplay.Output 里体现，故跳过。
func (h *Handler) sendSessionLoaded(conn *websocket.Conn, sess *session.Session) error {
	chatMsgs := buildChatMessages(sess.Messages)
	summary := SessionSummary{
		ID:           sess.ID,
		CreatedAt:    sess.CreatedAt,
		UpdatedAt:    sess.UpdatedAt,
		MessageCount: len(sess.Messages),
		Preview:      firstUserPreview(sess.Messages),
	}
	return h.sendMessage(conn, MsgTypeSessionLoaded, SessionLoadedPayload{
		SessionID: sess.ID,
		Summary:   summary,
		Messages:  chatMsgs,
		Model:     h.ModelName(),
		Workdir:   h.workdir,
	})
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
