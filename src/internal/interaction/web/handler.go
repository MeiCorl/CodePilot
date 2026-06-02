package web

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/engine/conversation"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/memory/session"
	"github.com/MeiCorl/CodePilot/src/llm"
)

// DefaultContextWindowSize 模型上下文窗口默认大小（token 数）。
// 当 Config 未指定时使用该值，用于 context_usage 状态栏展示。
const DefaultContextWindowSize = 200000

// Handler 持有所有业务依赖并把 WebSocket 消息路由到具体业务能力。
// 它维护"当前活跃会话"状态（current Session + ConversationManager），
// 并通过 streamState 状态机保证同一时刻只有一个流式请求进行中。
// workdir 记录 CodePilot 启动时所在的工作目录，仅在 session_loaded 消息中透传至前端展示。
type Handler struct {
	provider           llm.Provider
	sessMgr            *session.SessionManager
	cfg                *config.Config
	conv               *conversation.ConversationManager
	systemPrompt       string
	contextWindowSize  int
	workdir            string

	mu      sync.Mutex
	current *session.Session

	stream streamState
}

// NewHandler 构造 Handler。
// 构造时会尝试 sessMgr.LoadLatest() 恢复最近会话；无历史时创建新会话（不立即落盘）。
// workdir 启动时获取，会随 session_loaded 透传给前端。
func NewHandler(
	provider llm.Provider,
	sessMgr *session.SessionManager,
	cfg *config.Config,
	maxRounds int,
	systemPrompt string,
	contextWindowSize int,
	workdir string,
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

// runStream 是流式响应的核心 goroutine。
// ctx 在 abort_stream 时被 cancel，从而中断 Provider。
// goroutine 退出前保证：状态置为空闲、流式状态机释放、发送 stream_done 与 context_usage。
func (h *Handler) runStream(ctx context.Context, conn *websocket.Conn) {
	// 防御性 defer：确保流式状态机被释放、状态切回 idle
	defer func() {
		_ = h.sendStatusUpdate(conn, StatusIdle)
		h.stream.release(ctx)
	}()

	// 取上下文窗口视图（在锁内拷一份，避免后续访问被并发修改）
	h.mu.Lock()
	messages := h.conv.GetContext(h.systemPrompt)
	h.mu.Unlock()

	chunkCh, err := h.provider.StreamChat(ctx, h.systemPrompt, messages)
	if err != nil {
		_ = h.sendStreamError(conn, "stream_init_failed", err.Error())
		_ = h.sendStreamDone(conn, StreamReasonError)
		return
	}

	// 累积助手消息文本
	var buffer strings.Builder
	aborted := false

	for {
		select {
		case <-ctx.Done():
			// abort_stream 触发 ctx 取消：标记 aborted 并排空可能残余的 chunk
			aborted = true
			for {
				select {
				case _, ok := <-chunkCh:
					if !ok {
						// channel 已关闭，退出 runStream
						_ = h.persistAndFinish(conn, buffer.String(), aborted)
						return
					}
				default:
					// 已无更多 chunk，退出排空循环
					_ = h.persistAndFinish(conn, buffer.String(), aborted)
					return
				}
			}
		case chunk, ok := <-chunkCh:
			if !ok {
				// channel 关闭（Provider 正常结束）
				_ = h.persistAndFinish(conn, buffer.String(), aborted)
				return
			}
			if chunk.Err != nil {
				_ = h.sendStreamError(conn, "stream_error", chunk.Err.Error())
				_ = h.sendStreamDone(conn, StreamReasonError)
				return
			}
			if chunk.Done {
				_ = h.persistAndFinish(conn, buffer.String(), aborted)
				return
			}
			if chunk.Content != "" {
				buffer.WriteString(chunk.Content)
				_ = h.sendStreamChunk(conn, chunk.Content)
			}
		}
	}
}

// persistAndFinish 累积完整助手消息、持久化会话、发送 stream_done 与 context_usage。
// 抽取出来减少 runStream 内部重复代码。
func (h *Handler) persistAndFinish(conn *websocket.Conn, assistantText string, aborted bool) error {
	h.mu.Lock()
	h.conv.AddAssistantMessage(assistantText)
	h.current.Messages = h.conv.AllMessages()
	saveErr := h.sessMgr.Save(h.current)
	h.mu.Unlock()
	if saveErr != nil {
		logger.Warn("会话保存失败",
			zap.String("session_id", h.current.ID),
			zap.Error(saveErr),
		)
	}

	reason := StreamReasonCompleted
	if aborted {
		reason = StreamReasonAborted
	}
	_ = h.sendStreamDone(conn, reason)
	return h.sendContextUsage(conn)
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

// sendMessage 编码并发送一条带 payload 的消息。失败仅记录日志，不返回错误。
func (h *Handler) sendMessage(conn *websocket.Conn, typ string, payload any) error {
	data, err := EncodePayload(typ, payload)
	if err != nil {
		logger.Warn("编码消息失败", zap.String("type", typ), zap.Error(err))
		return err
	}
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

// sendSessionLoaded 发送 session_loaded 消息。
func (h *Handler) sendSessionLoaded(conn *websocket.Conn, sess *session.Session) error {
	chatMsgs := make([]ChatMessage, 0, len(sess.Messages))
	for _, m := range sess.Messages {
		chatMsgs = append(chatMsgs, ChatMessage{
			Role:    string(m.Role),
			Content: extractText(m.Content),
		})
	}
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

// extractText 从 ContentBlock 数组中提取首段文本（多模态尚未启用）。
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
