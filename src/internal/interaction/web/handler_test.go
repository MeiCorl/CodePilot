package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/memory/session"
	"github.com/MeiCorl/CodePilot/src/llm"
	"github.com/MeiCorl/CodePilot/src/tool"
)

// mockProvider 实现 llm.Provider，支持：
//   - 预设 chunk 序列
//   - 模拟 chunk 间隔延迟
//   - ctx 取消时立刻停止发送
type mockProvider struct {
	mu         sync.Mutex
	chunks     []llm.StreamChunk
	chunkDelay time.Duration
	abortHook  func() // ctx 取消时调用
	calls      int32
}

func (m *mockProvider) StreamChat(ctx context.Context, systemPrompt string, messages []llm.Message, toolSpecs []tool.ToolSpec) (<-chan llm.StreamChunk, error) {
	atomic.AddInt32(&m.calls, 1)

	m.mu.Lock()
	chunks := m.chunks
	delay := m.chunkDelay
	hook := m.abortHook
	m.mu.Unlock()

	ch := make(chan llm.StreamChunk, 32)
	go func() {
		defer close(ch)
		for _, c := range chunks {
			// 每个 chunk 之前先检测 ctx 取消
			if ctx.Err() != nil {
				if hook != nil {
					hook()
				}
				return
			}
			if delay > 0 {
				select {
				case <-ctx.Done():
					if hook != nil {
						hook()
					}
					return
				case <-time.After(delay):
				}
			}
			select {
			case <-ctx.Done():
				if hook != nil {
					hook()
				}
				return
			case ch <- c:
			}
		}
	}()
	return ch, nil
}

// ---- 测试公用工具 ----

// testRig 聚合 handler、mock provider、session dir、ws 客户端。
type testRig struct {
	h         *Handler
	mp        *mockProvider
	sessDir   string
	srv       *httptest.Server
	client    *websocket.Conn
	cancelHookCalled int32
}

func newTestRig(t *testing.T, chunks []llm.StreamChunk) *testRig {
	t.Helper()
	dir := t.TempDir()
	sm, err := session.NewSessionManagerWithDir(dir)
	if err != nil {
		t.Fatalf("SessionManager 初始化失败: %v", err)
	}
	cfg := &config.Config{
		Provider:  "anthropic",
		Model:     "claude-sonnet-test",
		APIKey:    "test-key",
		MaxTokens: 1024,
	}
	mp := &mockProvider{chunks: chunks}
	h := NewHandler(mp, sm, cfg, 10, "", 100000, t.TempDir(), nil, nil)

	s := NewServer("127.0.0.1:0")
	h.Register(s.Router())
	ts := httptest.NewServer(http.HandlerFunc(s.ConnectionManager().HandleWS))
	t.Cleanup(ts.Close)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws 拨号失败: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	return &testRig{
		h:       h,
		mp:      mp,
		sessDir: dir,
		srv:     ts,
		client:  client,
	}
}

func (r *testRig) send(t *testing.T, typ string, payload any) {
	t.Helper()
	data, err := EncodePayload(typ, payload)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if err := r.client.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("发送 %s 失败: %v", typ, err)
	}
}

func (r *testRig) recv(t *testing.T, timeout time.Duration) Message {
	t.Helper()
	_ = r.client.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := r.client.ReadMessage()
	if err != nil {
		t.Fatalf("读取消息失败: %v", err)
	}
	msg, err := Decode(data)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	return msg
}

// recvWithFilter 持续读取直到拿到 typ 匹配的消息或超时。
func (r *testRig) recvWithFilter(t *testing.T, want string, timeout time.Duration) (Message, []Message) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var skipped []Message
	for time.Now().Before(deadline) {
		msg := r.recv(t, time.Until(deadline))
		if msg.Type == want {
			return msg, skipped
		}
		skipped = append(skipped, msg)
	}
	t.Fatalf("等待 %s 超时", want)
	return Message{}, skipped
}

// ---- 测试用例 ----

// TestUserInputStreamsAndPersists 验证 user_input 触发流式响应、收齐消息、会话持久化。
func TestUserInputStreamsAndPersists(t *testing.T) {
	chunks := []llm.StreamChunk{
		{Content: "Hello"},
		{Content: ", "},
		{Content: "world!"},
		{Done: true},
	}
	r := newTestRig(t, chunks)

	r.send(t, MsgTypeUserInput, UserInputPayload{Text: "Hi"})

	// 期望顺序：status_update(thinking) → stream_chunk x 3 → stream_done(completed) → context_usage
	var (
		gotThinking   bool
		gotDeltas     []string
		doneReason    string
		ctxPayload    ContextUsagePayload
		gotContextUsg bool
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !gotContextUsg {
		msg := r.recv(t, time.Until(deadline))
		switch msg.Type {
		case MsgTypeStatusUpdate:
			p, _ := AsPayload[StatusUpdatePayload](msg)
			if p.Status == StatusThinking {
				gotThinking = true
			}
		case MsgTypeStreamChunk:
			p, _ := AsPayload[StreamChunkPayload](msg)
			gotDeltas = append(gotDeltas, p.Delta)
		case MsgTypeStreamDone:
			p, _ := AsPayload[StreamDonePayload](msg)
			doneReason = p.Reason
		case MsgTypeContextUsage:
			ctxPayload, _ = AsPayload[ContextUsagePayload](msg)
			gotContextUsg = true
		}
	}

	if !gotThinking {
		t.Error("未收到 status_update(thinking)")
	}
	if !gotContextUsg {
		t.Fatal("未收到 context_usage")
	}
	if doneReason != StreamReasonCompleted {
		t.Errorf("doneReason = %q，期望 %q", doneReason, StreamReasonCompleted)
	}
	if joined := strings.Join(gotDeltas, ""); joined != "Hello, world!" {
		t.Errorf("deltas 拼接 = %q，期望 %q", joined, "Hello, world!")
	}
	if ctxPayload.Limit != 100000 {
		t.Errorf("ctxPayload.Limit = %d，期望 100000", ctxPayload.Limit)
	}
	if ctxPayload.PercentLeft < 0 || ctxPayload.PercentLeft > 100 {
		t.Errorf("PercentLeft = %d，应在 0~100", ctxPayload.PercentLeft)
	}

	// 验证会话文件已写入
	files, err := os.ReadDir(r.sessDir)
	if err != nil {
		t.Fatalf("读取会话目录失败: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("期望 1 个会话文件，实际 %d", len(files))
	}
	// 验证文件内容包含用户消息和助手消息
	data, _ := os.ReadFile(filepath.Join(r.sessDir, files[0].Name()))
	if !strings.Contains(string(data), "Hi") {
		t.Errorf("会话文件应包含用户消息 'Hi'，实际: %s", data)
	}
	if !strings.Contains(string(data), "Hello, world!") {
		t.Errorf("会话文件应包含助手回复 'Hello, world!'，实际: %s", data)
	}
}

// TestAbortStreamStopsOngoing 验证流式过程中 abort_stream 立即中断。
func TestAbortStreamStopsOngoing(t *testing.T) {
	// 大量 chunk + 长延迟，模拟长时间流式
	chunks := make([]llm.StreamChunk, 100)
	for i := range chunks {
		chunks[i] = llm.StreamChunk{Content: fmt.Sprintf("chunk-%d ", i)}
	}
	chunks = append(chunks, llm.StreamChunk{Done: true})
	r := newTestRig(t, chunks)
	// chunk delay 短一些，让 abort 触发更确定
	r.mp.chunkDelay = 5 * time.Millisecond

	r.send(t, MsgTypeUserInput, UserInputPayload{Text: "long prompt"})

	// 等到收到第一个 stream_chunk 后再 abort
	firstChunk, _ := r.recvWithFilter(t, MsgTypeStreamChunk, 2*time.Second)
	firstDelta, _ := AsPayload[StreamChunkPayload](firstChunk)
	if firstDelta.Delta == "" {
		t.Fatal("第一个 chunk 应有内容")
	}

	// 发送 abort
	r.send(t, MsgTypeAbortStream, nil)

	// 期望收到 stream_done(reason=aborted)
	deadline := time.Now().Add(2 * time.Second)
	var gotAborted bool
	for time.Now().Before(deadline) && !gotAborted {
		msg := r.recv(t, time.Until(deadline))
		if msg.Type == MsgTypeStreamDone {
			p, _ := AsPayload[StreamDonePayload](msg)
			if p.Reason == StreamReasonAborted {
				gotAborted = true
			}
		}
	}
	if !gotAborted {
		t.Fatal("未收到 stream_done(reason=aborted)")
	}

	// 验证流式状态已释放（短超时内不再有 stream_chunk）
	_ = r.client.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, data, err := r.client.ReadMessage()
	if err == nil {
		// 不应有消息，但允许 context_usage 之类
		var msg Message
		_ = json.Unmarshal(data, &msg)
		if msg.Type == MsgTypeStreamChunk {
			t.Errorf("abort 后不应再有 stream_chunk，实际: %s", data)
		}
	}
}

// TestAbortStreamNoOpWhenIdle 验证无活跃流时 abort_stream 不报错。
func TestAbortStreamNoOpWhenIdle(t *testing.T) {
	r := newTestRig(t, nil)
	r.send(t, MsgTypeAbortStream, nil)
	// 不期望任何响应（不发送 stream_error 也不关闭连接）
	_ = r.client.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, _, err := r.client.ReadMessage(); err == nil {
		t.Error("无活跃流时 abort_stream 不应有响应")
	}
}

// TestBusyRejectsConcurrentInput 验证流式进行中再次 user_input 被拒。
func TestBusyRejectsConcurrentInput(t *testing.T) {
	// 用一个不会自然结束的 chunk 序列（仅第一个 chunk，剩余靠 abort 触发结束）
	chunks := []llm.StreamChunk{
		{Content: "first"},
		// 不发 Done，模拟长流
	}
	r := newTestRig(t, chunks)
	r.mp.chunkDelay = 50 * time.Millisecond

	r.send(t, MsgTypeUserInput, UserInputPayload{Text: "first"})

	// 等到第一个 stream_chunk，确认流已启动
	_, _ = r.recvWithFilter(t, MsgTypeStreamChunk, 2*time.Second)

	// 再次发 user_input，应被拒
	r.send(t, MsgTypeUserInput, UserInputPayload{Text: "second"})

	// 期望收到 stream_error(code=busy)
	deadline := time.Now().Add(2 * time.Second)
	var gotBusy bool
	for time.Now().Before(deadline) && !gotBusy {
		msg := r.recv(t, time.Until(deadline))
		if msg.Type == MsgTypeStreamError {
			p, _ := AsPayload[StreamErrorPayload](msg)
			if p.Code == "busy" {
				gotBusy = true
			}
		}
	}
	if !gotBusy {
		t.Fatal("流式进行中再发 user_input 应返回 busy")
	}
}

// TestEmptyUserInput 验证空文本返回 empty_input 错误。
func TestEmptyUserInput(t *testing.T) {
	r := newTestRig(t, nil)
	r.send(t, MsgTypeUserInput, UserInputPayload{Text: "   "})

	deadline := time.Now().Add(1 * time.Second)
	var gotEmpty bool
	for time.Now().Before(deadline) && !gotEmpty {
		msg := r.recv(t, time.Until(deadline))
		if msg.Type == MsgTypeStreamError {
			p, _ := AsPayload[StreamErrorPayload](msg)
			if p.Code == "empty_input" {
				gotEmpty = true
			}
		}
	}
	if !gotEmpty {
		t.Fatal("空输入应返回 empty_input 错误")
	}
}

// TestListSessions 验证 list_sessions 返回所有会话摘要按 UpdatedAt 降序。
func TestListSessions(t *testing.T) {
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)
	// 预创建 3 个会话
	for range []int{1, 2, 3} {
		s := sm.CreateNew()
		s.Messages = []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("hello")}},
		}
		if err := sm.Save(s); err != nil {
			t.Fatalf("保存失败: %v", err)
		}
		// 间隔 1ms 区分 UpdatedAt
		time.Sleep(2 * time.Millisecond)
	}

	cfg := &config.Config{Provider: "anthropic", Model: "test", APIKey: "k", MaxTokens: 1024}
	mp := &mockProvider{}
	h := NewHandler(mp, sm, cfg, 10, "", 100000, t.TempDir(), nil, nil)
	s := NewServer("127.0.0.1:0")
	h.Register(s.Router())
	ts := httptest.NewServer(http.HandlerFunc(s.ConnectionManager().HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	defer client.Close()

	_ = client.WriteMessage(websocket.TextMessage, []byte(`{"type":"list_sessions"}`))
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	var msg Message
	_ = json.Unmarshal(data, &msg)
	if msg.Type != MsgTypeSessionList {
		t.Fatalf("Type = %q，期望 %q", msg.Type, MsgTypeSessionList)
	}
	payload, _ := AsPayload[SessionListPayload](msg)
	if len(payload.Sessions) != 3 {
		t.Fatalf("Sessions 数量 = %d，期望 3", len(payload.Sessions))
	}
	// 验证按 UpdatedAt 降序
	for i := 1; i < len(payload.Sessions); i++ {
		if payload.Sessions[i-1].UpdatedAt.Before(payload.Sessions[i].UpdatedAt) {
			t.Errorf("Sessions 未按 UpdatedAt 降序: idx %d 比 %d 更新", i-1, i)
		}
	}
}

// TestListSessionsTableMode 验证 list_sessions 的 table 模式：
// 按 CreatedAt 降序、最多 10 条；并返回 created_at 字段。
func TestListSessionsTableMode(t *testing.T) {
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)

	// 依次创建 3 个会话，确保 CreatedAt 严格递增
	var ids []string
	for i := 0; i < 3; i++ {
		s := sm.CreateNew()
		s.Messages = []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock(fmt.Sprintf("msg-%d", i))}},
		}
		if err := sm.Save(s); err != nil {
			t.Fatalf("保存失败: %v", err)
		}
		ids = append(ids, s.ID)
		time.Sleep(5 * time.Millisecond)
	}

	cfg := &config.Config{Provider: "anthropic", Model: "test", APIKey: "k", MaxTokens: 1024}
	mp := &mockProvider{}
	h := NewHandler(mp, sm, cfg, 10, "", 100000, t.TempDir(), nil, nil)
	s := NewServer("127.0.0.1:0")
	h.Register(s.Router())
	ts := httptest.NewServer(http.HandlerFunc(s.ConnectionManager().HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	defer client.Close()

	// 发送 list_sessions 携带 mode=table
	r := &testRig{h: h, mp: mp, sessDir: dir, srv: ts, client: client}
	r.send(t, MsgTypeListSessions, ListSessionsPayload{Mode: "table"})

	list, _ := r.recvWithFilter(t, MsgTypeSessionList, 2*time.Second)
	payload, _ := AsPayload[SessionListPayload](list)
	if len(payload.Sessions) != 3 {
		t.Fatalf("table 模式应返回 3 条，实际 %d", len(payload.Sessions))
	}
	// 验证按 CreatedAt 降序
	for i := 1; i < len(payload.Sessions); i++ {
		if payload.Sessions[i-1].CreatedAt.Before(payload.Sessions[i].CreatedAt) {
			t.Errorf("table 模式未按 CreatedAt 降序: idx %d 比 %d 更老", i-1, i)
		}
	}
	// 验证返回了 created_at 字段（不为零值）
	if payload.Sessions[0].CreatedAt.IsZero() {
		t.Error("table 模式应返回 created_at，实际为零值")
	}
	// 第 1 条应是最新创建的（ids 顺序的最后一个）
	if payload.Sessions[0].ID != ids[2] {
		t.Errorf("table 模式第 1 条 ID = %q，期望 %q", payload.Sessions[0].ID, ids[2])
	}
}

// TestNewSessionCreatesAndSavesCurrent 验证 new_session 创建新会话。
func TestNewSessionCreatesAndSavesCurrent(t *testing.T) {
	// 准备一个会持久化的 chunks，让 handler 先保存一些消息
	chunks := []llm.StreamChunk{
		{Content: "ok"},
		{Done: true},
	}
	r := newTestRig(t, chunks)
	r.send(t, MsgTypeUserInput, UserInputPayload{Text: "hi"})

	// 等到 context_usage（说明第一次会话已持久化）
	_, _ = r.recvWithFilter(t, MsgTypeContextUsage, 2*time.Second)

	// 先记录旧会话 ID（用 list_sessions 拿）
	r.send(t, MsgTypeListSessions, nil)
	list, _ := r.recvWithFilter(t, MsgTypeSessionList, 2*time.Second)
	listPayload, _ := AsPayload[SessionListPayload](list)
	if len(listPayload.Sessions) == 0 {
		t.Fatal("list_sessions 应返回至少 1 条")
	}
	oldID := listPayload.Sessions[0].ID

	// 发 new_session
	r.send(t, MsgTypeNewSession, nil)

	// 收到 session_loaded
	loaded, _ := r.recvWithFilter(t, MsgTypeSessionLoaded, 2*time.Second)
	p, _ := AsPayload[SessionLoadedPayload](loaded)
	if p.SessionID == "" {
		t.Error("新会话 SessionID 应非空")
	}
	if p.SessionID == oldID {
		t.Errorf("新会话 ID = %q，应与旧 ID %q 不同", p.SessionID, oldID)
	}
	if p.Summary.MessageCount != 0 {
		t.Errorf("新会话 MessageCount = %d，期望 0", p.Summary.MessageCount)
	}
	if len(p.Messages) != 0 {
		t.Errorf("新会话 Messages = %d 条，期望 0", len(p.Messages))
	}

	// 验证 Handler 内部 current 已切到新会话
	if r.h.CurrentSessionID() != p.SessionID {
		t.Errorf("Handler.CurrentSessionID = %q，应等于 %q", r.h.CurrentSessionID(), p.SessionID)
	}

	// 验证目录里至少 1 个会话文件（旧会话已 save）
	files, _ := os.ReadDir(r.sessDir)
	if len(files) < 1 {
		t.Errorf("期望至少 1 个会话文件，实际 %d", len(files))
	}
}

// TestResumeSessionPrefixMatch 验证前缀匹配恢复历史会话。
func TestResumeSessionPrefixMatch(t *testing.T) {
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)

	// 创建一个含消息的会话
	sess := sm.CreateNew()
	sess.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("ask1")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("ans1")}},
	}
	if err := sm.Save(sess); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	prefix := sess.ID[:6]

	cfg := &config.Config{Provider: "anthropic", Model: "test", APIKey: "k", MaxTokens: 1024}
	mp := &mockProvider{}
	h := NewHandler(mp, sm, cfg, 10, "", 100000, t.TempDir(), nil, nil)
	s := NewServer("127.0.0.1:0")
	h.Register(s.Router())
	ts := httptest.NewServer(http.HandlerFunc(s.ConnectionManager().HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	defer client.Close()

	r := &testRig{h: h, mp: mp, sessDir: dir, srv: ts, client: client}
	r.send(t, MsgTypeResumeSession, ResumeSessionPayload{ID: prefix})

	loaded, _ := r.recvWithFilter(t, MsgTypeSessionLoaded, 2*time.Second)
	p, _ := AsPayload[SessionLoadedPayload](loaded)
	if p.SessionID != sess.ID {
		t.Errorf("SessionID = %q，期望 %q", p.SessionID, sess.ID)
	}
	if len(p.Messages) != 2 {
		t.Errorf("Messages 数量 = %d，期望 2", len(p.Messages))
	}
	if p.Messages[0].Content != "ask1" {
		t.Errorf("Messages[0] = %q，期望 %q", p.Messages[0].Content, "ask1")
	}
}

// TestResumeSessionNotFound 验证无匹配返回 session_not_found。
func TestResumeSessionNotFound(t *testing.T) {
	r := newTestRig(t, nil)
	r.send(t, MsgTypeResumeSession, ResumeSessionPayload{ID: "nope"})

	deadline := time.Now().Add(2 * time.Second)
	var gotCode string
	for time.Now().Before(deadline) && gotCode == "" {
		msg := r.recv(t, time.Until(deadline))
		if msg.Type == MsgTypeStreamError {
			p, _ := AsPayload[StreamErrorPayload](msg)
			gotCode = p.Code
		}
	}
	if gotCode != "session_not_found" {
		t.Errorf("Code = %q，期望 %q", gotCode, "session_not_found")
	}
}

// TestResumeSessionAmbiguous 验证多匹配返回 session_ambiguous。
func TestResumeSessionAmbiguous(t *testing.T) {
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)
	// 创建两个 ID 前缀相同的会话（不太可能，但可以构造：手动写文件）
	// 直接创建两个 session 让 ID 前 4 位相同是几乎不可能的；改为构造同样前缀
	// 通过写入两个 ID 都是 "amb" 开头的会话
	s1 := sm.CreateNew()
	s1.ID = "amb-1"
	s1.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("m1")}},
	}
	if err := sm.Save(s1); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	s2 := sm.CreateNew()
	s2.ID = "amb-2"
	s2.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("m2")}},
	}
	if err := sm.Save(s2); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	cfg := &config.Config{Provider: "anthropic", Model: "test", APIKey: "k", MaxTokens: 1024}
	mp := &mockProvider{}
	h := NewHandler(mp, sm, cfg, 10, "", 100000, t.TempDir(), nil, nil)
	s := NewServer("127.0.0.1:0")
	h.Register(s.Router())
	ts := httptest.NewServer(http.HandlerFunc(s.ConnectionManager().HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	defer client.Close()

	r := &testRig{h: h, mp: mp, sessDir: dir, srv: ts, client: client}
	r.send(t, MsgTypeResumeSession, ResumeSessionPayload{ID: "amb"})

	deadline := time.Now().Add(2 * time.Second)
	var gotCode string
	for time.Now().Before(deadline) && gotCode == "" {
		msg := r.recv(t, time.Until(deadline))
		if msg.Type == MsgTypeStreamError {
			p, _ := AsPayload[StreamErrorPayload](msg)
			gotCode = p.Code
		}
	}
	if gotCode != "session_ambiguous" {
		t.Errorf("Code = %q，期望 %q", gotCode, "session_ambiguous")
	}
}

// TestSessionLoadedIncludesChatMessages 验证 session_loaded 消息的 chat 字段。
func TestSessionLoadedIncludesChatMessages(t *testing.T) {
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)
	sess := sm.CreateNew()
	sess.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("hi")}},
	}
	_ = sm.Save(sess)

	cfg := &config.Config{Provider: "anthropic", Model: "test", APIKey: "k", MaxTokens: 1024}
	mp := &mockProvider{}
	h := NewHandler(mp, sm, cfg, 10, "", 100000, t.TempDir(), nil, nil)
	s := NewServer("127.0.0.1:0")
	h.Register(s.Router())
	ts := httptest.NewServer(http.HandlerFunc(s.ConnectionManager().HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	defer client.Close()

	r := &testRig{h: h, mp: mp, sessDir: dir, srv: ts, client: client}
	r.send(t, MsgTypeResumeSession, ResumeSessionPayload{ID: sess.ID})

	loaded, _ := r.recvWithFilter(t, MsgTypeSessionLoaded, 2*time.Second)
	p, _ := AsPayload[SessionLoadedPayload](loaded)
	if len(p.Messages) != 1 {
		t.Fatalf("Messages = %d，期望 1", len(p.Messages))
	}
	if p.Messages[0].Role != "user" {
		t.Errorf("Role = %q，期望 %q", p.Messages[0].Role, "user")
	}
	if p.Messages[0].Content != "hi" {
		t.Errorf("Content = %q，期望 %q", p.Messages[0].Content, "hi")
	}
	if p.Summary.MessageCount != 1 {
		t.Errorf("Summary.MessageCount = %d，期望 1", p.Summary.MessageCount)
	}
}

// TestGetCurrentSessionPushesCurrent 验证 get_current_session 把当前活动会话以 session_loaded 回推。
func TestGetCurrentSessionPushesCurrent(t *testing.T) {
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)
	sess := sm.CreateNew()
	sess.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("hello")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("hi there")}},
	}
	if err := sm.Save(sess); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	cfg := &config.Config{Provider: "anthropic", Model: "test", APIKey: "k", MaxTokens: 1024}
	mp := &mockProvider{}
	h := NewHandler(mp, sm, cfg, 10, "", 100000, t.TempDir(), nil, nil)
	if h.CurrentSessionID() != sess.ID {
		t.Fatalf("构造后 CurrentSessionID = %q，期望 %q", h.CurrentSessionID(), sess.ID)
	}

	s := NewServer("127.0.0.1:0")
	h.Register(s.Router())
	ts := httptest.NewServer(http.HandlerFunc(s.ConnectionManager().HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	defer client.Close()

	r := &testRig{h: h, mp: mp, sessDir: dir, srv: ts, client: client}
	r.send(t, MsgTypeGetCurrentSession, struct{}{})

	loaded, _ := r.recvWithFilter(t, MsgTypeSessionLoaded, 2*time.Second)
	p, _ := AsPayload[SessionLoadedPayload](loaded)
	if p.SessionID != sess.ID {
		t.Errorf("SessionID = %q，期望 %q", p.SessionID, sess.ID)
	}
	if len(p.Messages) != 2 {
		t.Errorf("Messages = %d，期望 2", len(p.Messages))
	}
	if p.Messages[0].Content != "hello" || p.Messages[1].Content != "hi there" {
		t.Errorf("Messages 内容不符: %+v", p.Messages)
	}
}

// TestGetCurrentSessionEmptyMgr 验证无历史时 get_current_session 仍返回 session_loaded（新建空会话）。
func TestGetCurrentSessionEmptyMgr(t *testing.T) {
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)
	cfg := &config.Config{Provider: "anthropic", Model: "test", APIKey: "k", MaxTokens: 1024}
	mp := &mockProvider{}
	h := NewHandler(mp, sm, cfg, 10, "", 100000, t.TempDir(), nil, nil)

	s := NewServer("127.0.0.1:0")
	h.Register(s.Router())
	ts := httptest.NewServer(http.HandlerFunc(s.ConnectionManager().HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	defer client.Close()

	r := &testRig{h: h, mp: mp, sessDir: dir, srv: ts, client: client}
	r.send(t, MsgTypeGetCurrentSession, struct{}{})

	loaded, _ := r.recvWithFilter(t, MsgTypeSessionLoaded, 2*time.Second)
	p, _ := AsPayload[SessionLoadedPayload](loaded)
	if p.SessionID == "" {
		t.Error("空历史时 SessionID 也不应为空")
	}
	if len(p.Messages) != 0 {
		t.Errorf("Messages = %d，期望 0", len(p.Messages))
	}
}

// TestHandlerRecoversLatestSession 验证构造时 LoadLatest 自动恢复。
func TestHandlerRecoversLatestSession(t *testing.T) {
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)
	sess := sm.CreateNew()
	sess.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("recovered")}},
	}
	_ = sm.Save(sess)

	cfg := &config.Config{Provider: "anthropic", Model: "test", APIKey: "k", MaxTokens: 1024}
	mp := &mockProvider{}
	h := NewHandler(mp, sm, cfg, 10, "", 100000, t.TempDir(), nil, nil)

	if h.CurrentSessionID() != sess.ID {
		t.Errorf("CurrentSessionID = %q，期望 %q", h.CurrentSessionID(), sess.ID)
	}
}

// TestDeleteSessionRemovesFileAndNotifies 验证删除非当前会话：文件被移除、收到 session_deleted、不发生 currentChanged。
func TestDeleteSessionRemovesFileAndNotifies(t *testing.T) {
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)
	// 预创建两个会话
	s1 := sm.CreateNew()
	_ = sm.Save(s1)
	time.Sleep(2 * time.Millisecond)
	s2 := sm.CreateNew()
	_ = sm.Save(s2)
	// 假设当前激活的是 s2（最近更新），删 s1
	cfg := &config.Config{Provider: "anthropic", Model: "test", APIKey: "k", MaxTokens: 1024}
	mp := &mockProvider{}
	h := NewHandler(mp, sm, cfg, 10, "", 100000, t.TempDir(), nil, nil)
	// 直接覆盖构造时 LoadLatest 决定的 current，确保其是 s2
	h.mu.Lock()
	loaded, _ := sm.Load(s2.ID)
	h.current = loaded
	h.mu.Unlock()

	srv := NewServer("127.0.0.1:0")
	h.Register(srv.Router())
	ts := httptest.NewServer(http.HandlerFunc(srv.ConnectionManager().HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	defer client.Close()

	r := &testRig{h: h, mp: mp, sessDir: dir, srv: ts, client: client}
	r.send(t, MsgTypeDeleteSession, DeleteSessionPayload{ID: s1.ID})

	// 应收到 session_deleted，current_changed=false
	deleted, _ := r.recvWithFilter(t, MsgTypeSessionDeleted, 2*time.Second)
	p, _ := AsPayload[SessionDeletedPayload](deleted)
	if p.DeletedID != s1.ID {
		t.Errorf("DeletedID = %q，期望 %q", p.DeletedID, s1.ID)
	}
	if p.CurrentChanged {
		t.Error("删除非当前会话时 CurrentChanged 应为 false")
	}

	// 文件已删除
	if _, err := os.Stat(filepath.Join(dir, s1.ID+".json")); !os.IsNotExist(err) {
		t.Errorf("会话文件应已删除，实际: %v", err)
	}
	// 当前会话未变
	if h.CurrentSessionID() != s2.ID {
		t.Errorf("CurrentSessionID = %q，期望 %q", h.CurrentSessionID(), s2.ID)
	}
}

// TestDeleteSessionSwitchesCurrentWhenDeletingCurrent 验证删除当前会话：
// 自动切到最近更新的其它会话、收到 session_deleted(current_changed=true) + session_loaded。
func TestDeleteSessionSwitchesCurrentWhenDeletingCurrent(t *testing.T) {
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)
	// 预创建两个会话
	s1 := sm.CreateNew()
	_ = sm.Save(s1)
	time.Sleep(5 * time.Millisecond)
	s2 := sm.CreateNew()
	s2.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("latest")}},
	}
	_ = sm.Save(s2)

	cfg := &config.Config{Provider: "anthropic", Model: "test", APIKey: "k", MaxTokens: 1024}
	mp := &mockProvider{}
	h := NewHandler(mp, sm, cfg, 10, "", 100000, t.TempDir(), nil, nil)
	// 强制把 current 设为 s1（稍旧），然后删除它，预期切到 s2
	h.mu.Lock()
	loaded, _ := sm.Load(s1.ID)
	h.current = loaded
	h.mu.Unlock()

	srv := NewServer("127.0.0.1:0")
	h.Register(srv.Router())
	ts := httptest.NewServer(http.HandlerFunc(srv.ConnectionManager().HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	defer client.Close()

	r := &testRig{h: h, mp: mp, sessDir: dir, srv: ts, client: client}
	r.send(t, MsgTypeDeleteSession, DeleteSessionPayload{ID: s1.ID})

	// 顺序：先 session_deleted 再 session_loaded
	var gotDeleted, gotLoaded bool
	var loadedID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (!gotDeleted || !gotLoaded) {
		msg := r.recv(t, time.Until(deadline))
		switch msg.Type {
		case MsgTypeSessionDeleted:
			p, _ := AsPayload[SessionDeletedPayload](msg)
			if p.DeletedID != s1.ID {
				t.Errorf("DeletedID = %q，期望 %q", p.DeletedID, s1.ID)
			}
			if !p.CurrentChanged {
				t.Error("删除当前会话时 CurrentChanged 应为 true")
			}
			gotDeleted = true
		case MsgTypeSessionLoaded:
			p, _ := AsPayload[SessionLoadedPayload](msg)
			loadedID = p.SessionID
			gotLoaded = true
		}
	}
	if !gotDeleted {
		t.Fatal("未收到 session_deleted")
	}
	if !gotLoaded {
		t.Fatal("未收到 session_loaded")
	}
	if loadedID != s2.ID {
		t.Errorf("切换后的 SessionID = %q，期望 %q（最近更新的另一个会话）", loadedID, s2.ID)
	}
	// Handler 内部 current 已切换
	if h.CurrentSessionID() != s2.ID {
		t.Errorf("Handler.CurrentSessionID = %q，期望 %q", h.CurrentSessionID(), s2.ID)
	}
	// 旧文件已删除
	if _, err := os.Stat(filepath.Join(dir, s1.ID+".json")); !os.IsNotExist(err) {
		t.Errorf("旧会话文件应已删除，实际: %v", err)
	}
}

// TestDeleteSessionEmptyID 验证空 ID 返回 stream_error(empty_id)。
func TestDeleteSessionEmptyID(t *testing.T) {
	r := newTestRig(t, nil)
	r.send(t, MsgTypeDeleteSession, DeleteSessionPayload{ID: ""})

	deadline := time.Now().Add(1 * time.Second)
	var gotEmpty bool
	for time.Now().Before(deadline) && !gotEmpty {
		msg := r.recv(t, time.Until(deadline))
		if msg.Type == MsgTypeStreamError {
			p, _ := AsPayload[StreamErrorPayload](msg)
			if p.Code == "empty_id" {
				gotEmpty = true
			}
		}
	}
	if !gotEmpty {
		t.Fatal("空 ID 应返回 empty_id 错误")
	}
}
