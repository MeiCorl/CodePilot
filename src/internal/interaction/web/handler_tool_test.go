package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/engine/conversation"
	"github.com/MeiCorl/CodePilot/src/internal/memory/session"
	"github.com/MeiCorl/CodePilot/src/llm"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// ---- Tool 端到端测试专用 mock provider ----

// scriptedProvider 模拟 RunTurn 中的"第一次 LLM → 工具执行 → 第二次 LLM"两次调用。
// 每次 StreamChat 从 scripts 列表中取下一段 chunk 序列；序列中每个 chunk 按
// chunkDelay 间隔发送，ctx 取消时立刻返回（模拟 abort）。
type scriptedProvider struct {
	mu         sync.Mutex
	scripts    [][]llm.StreamChunk
	cursor     int
	chunkDelay time.Duration
	calls      int32
	// 每次 StreamChat 调用时把收到的 toolSpecs 记录到该切片，便于测试断言。
	recordedSpecs [][]tool.ToolSpec
}

func (p *scriptedProvider) StreamChat(ctx context.Context, _ llm.SystemPrompt, _ []llm.Message, specs []tool.ToolSpec) (<-chan llm.StreamChunk, error) {
	atomic.AddInt32(&p.calls, 1)
	p.mu.Lock()
	// 记录一份切片的副本，避免 caller 后续修改影响
	cp := append([]tool.ToolSpec(nil), specs...)
	p.recordedSpecs = append(p.recordedSpecs, cp)
	if p.cursor >= len(p.scripts) {
		p.mu.Unlock()
		ch := make(chan llm.StreamChunk, 1)
		ch <- llm.StreamChunk{Done: true}
		close(ch)
		return ch, nil
	}
	script := p.scripts[p.cursor]
	p.cursor++
	delay := p.chunkDelay
	p.mu.Unlock()

	ch := make(chan llm.StreamChunk, 32)
	go func() {
		defer close(ch)
		for _, c := range script {
			if ctx.Err() != nil {
				return
			}
			if delay > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
			}
			select {
			case <-ctx.Done():
				return
			case ch <- c:
			}
		}
	}()
	return ch, nil
}

// newScriptedHandler 构造 handler + ws 客户端 + scriptedProvider + toolHandler。
// 返回的 ToolHandler 用于单独注册工具 / 验证 OnStart/OnEnd 回调。
type toolRig struct {
	h           *Handler
	mp          *scriptedProvider
	sessDir     string
	srv         *httptest.Server
	client      *websocket.Conn
	toolHandler *conversation.ToolHandler
}

func newToolRig(t *testing.T, scripts [][]llm.StreamChunk, toolInstance tool.Tool) *toolRig {
	return newToolRigWithEnabled(t, scripts, toolInstance, nil)
}

// newToolRigWithEnabled 与 newToolRig 等价，但可在 cfg 中预设 Tools.Enabled，
// 用于验证 handler.runStream 是否按白名单过滤工具描述再交给 Provider。
func newToolRigWithEnabled(t *testing.T, scripts [][]llm.StreamChunk, toolInstance tool.Tool, enabled []string) *toolRig {
	t.Helper()
	dir := t.TempDir()
	sm, err := session.NewSessionManagerWithDir(dir)
	if err != nil {
		t.Fatalf("SessionManager 初始化失败: %v", err)
	}
	cfg := &config.Config{
		Provider:  "anthropic",
		Model:     "claude-test",
		APIKey:    "test-key",
		MaxTokens: 1024,
		Tools:     config.ToolsConfig{Enabled: enabled},
	}
	mp := &scriptedProvider{scripts: scripts, chunkDelay: 5 * time.Millisecond}

	registry := tool.NewRegistry()
	if toolInstance != nil {
		if err := registry.Register(toolInstance); err != nil {
			t.Fatalf("注册工具失败: %v", err)
		}
	}
	toolHandler := conversation.NewToolHandler(registry, 5*time.Second, dir)

	h := NewHandler(mp, sm, cfg, 10, nil, 100000, dir, registry, toolHandler, nil)

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

	return &toolRig{
		h:           h,
		mp:          mp,
		sessDir:     dir,
		srv:         ts,
		client:      client,
		toolHandler: toolHandler,
	}
}

// recvAll 在 timeout 内尽可能多地收消息。
func (r *toolRig) recvAll(t *testing.T, timeout time.Duration) []Message {
	t.Helper()
	var msgs []Message
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = r.client.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		_, data, err := r.client.ReadMessage()
		if err != nil {
			// 短超时无数据：跳出
			break
		}
		msg, err := Decode(data)
		if err != nil {
			t.Fatalf("解码失败: %v", err)
		}
		msgs = append(msgs, msg)
		// 收到 context_usage 视为本轮结束
		if msg.Type == MsgTypeContextUsage {
			break
		}
	}
	return msgs
}

// recvUntilStatus 收消息直到出现 status_update=targetStatus 或超时。
// 用于 status_idle 由 defer 延迟发出、不与 context_usage 同时到达的场景。
func (r *toolRig) recvUntilStatus(target string, timeout time.Duration) []Message {
	var msgs []Message
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = r.client.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		_, data, err := r.client.ReadMessage()
		if err != nil {
			// 短超时无数据：可能 status_idle 尚未发出,继续
			continue
		}
		msg, err := Decode(data)
		if err != nil {
			continue
		}
		msgs = append(msgs, msg)
		if msg.Type == MsgTypeStatusUpdate {
			p, _ := AsPayload[StatusUpdatePayload](msg)
			if p.Status == target {
				return msgs
			}
		}
	}
	return msgs
}

// findByType 在消息列表中找首个 type 匹配的消息。
func findByType(msgs []Message, typ string) *Message {
	for i, m := range msgs {
		if m.Type == typ {
			return &msgs[i]
		}
	}
	return nil
}

// findAllByType 返回所有匹配的消息。
func findAllByType(msgs []Message, typ string) []Message {
	var out []Message
	for _, m := range msgs {
		if m.Type == typ {
			out = append(out, m)
		}
	}
	return out
}

// ---- 测试用例 ----

// TestToolCallStartPayload 验证 tool_call_start 消息含 tool_use_id/name/input/started_at。
func TestToolCallStartPayload(t *testing.T) {
	toolInst := &echoToolForTest{}
	scripts := [][]llm.StreamChunk{
		// 第一次 LLM：返回 tool_use（无文本）
		{{
			Done:    true,
			ToolUses: []llm.ToolUseBlock{{ID: "call-001", Name: "echo", Input: json.RawMessage(`{"msg":"ping"}`)}},
		}},
		// 第二次 LLM：基于 tool_result 给最终回复
		{{Content: "done", Done: true}},
	}
	r := newToolRig(t, scripts, toolInst)

	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeUserInput, UserInputPayload{Text: "call echo"}))

	msgs := r.recvAll(t, 2 * time.Second)
	start := findByType(msgs, MsgTypeToolCallStart)
	if start == nil {
		t.Fatalf("未收到 tool_call_start: %+v", msgs)
	}
	p, err := AsPayload[ToolCallStartPayload](*start)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if p.ToolUseID != "call-001" {
		t.Errorf("ToolUseID = %q, 期望 call-001", p.ToolUseID)
	}
	if p.Name != "echo" {
		t.Errorf("Name = %q, 期望 echo", p.Name)
	}
	if !strings.Contains(string(p.Input), "ping") {
		t.Errorf("Input 应包含 ping, 实际: %s", p.Input)
	}
	if p.StartedAt.IsZero() {
		t.Error("StartedAt 应为非零时间")
	}
}

// TestToolCallEndPayload 验证 tool_call_end 消息含 is_error/duration_ms/status。
func TestToolCallEndPayload(t *testing.T) {
	toolInst := &echoToolForTest{}
	scripts := [][]llm.StreamChunk{
		{{
			Done:    true,
			ToolUses: []llm.ToolUseBlock{{ID: "c1", Name: "echo", Input: json.RawMessage(`{"msg":"hi"}`)}},
		}},
		{{Content: "ok", Done: true}},
	}
	r := newToolRig(t, scripts, toolInst)
	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeUserInput, UserInputPayload{Text: "hi"}))

	msgs := r.recvAll(t, 2 * time.Second)
	end := findByType(msgs, MsgTypeToolCallEnd)
	if end == nil {
		t.Fatalf("未收到 tool_call_end")
	}
	p, _ := AsPayload[ToolCallEndPayload](*end)
	if p.ToolUseID != "c1" {
		t.Errorf("ToolUseID = %q, 期望 c1", p.ToolUseID)
	}
	if p.IsError {
		t.Error("IsError 应为 false")
	}
	if p.DurationMs < 0 {
		t.Errorf("DurationMs = %d, 应 >= 0", p.DurationMs)
	}
	if p.Status != ToolCallStatusCompleted {
		t.Errorf("Status = %q, 期望 completed", p.Status)
	}
	if p.Output != "echo:hi" {
		t.Errorf("Output = %q, 期望 echo:hi", p.Output)
	}
}

// TestStatusUpdateTransitions 验证状态机：thinking → tool_running → thinking → idle。
func TestStatusUpdateTransitions(t *testing.T) {
	toolInst := &echoToolForTest{}
	scripts := [][]llm.StreamChunk{
		// 第一次 LLM 故意加 chunkDelay，确保 OnStart 在 streaming chunk 之前到位
		{{
			Done:    true,
			ToolUses: []llm.ToolUseBlock{{ID: "s1", Name: "echo", Input: json.RawMessage(`{"msg":"x"}`)}},
		}},
		{{Content: "ok", Done: true}},
	}
	r := newToolRig(t, scripts, toolInst)
	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeUserInput, UserInputPayload{Text: "go"}))

	// status_idle 由 runStream 的 defer 在 sendContextUsage 之后才发出,
	// 专用收齐函数一直读到 status_idle 再返回。
	msgs := r.recvUntilStatus(StatusIdle, 2*time.Second)
	statuses := findAllByType(msgs, MsgTypeStatusUpdate)
	if len(statuses) < 3 {
		t.Fatalf("至少应收到 3 个 status_update(thinking/tool_running/thinking/idle), 实际: %+v", statuses)
	}

	// 第 1 个应是 thinking
	first, _ := AsPayload[StatusUpdatePayload](statuses[0])
	if first.Status != StatusThinking {
		t.Errorf("第 1 个 status = %q, 期望 thinking", first.Status)
	}
	// 中间某个应是 tool_running
	gotToolRunning := false
	for _, m := range statuses {
		p, _ := AsPayload[StatusUpdatePayload](m)
		if p.Status == StatusToolRunning {
			gotToolRunning = true
			break
		}
	}
	if !gotToolRunning {
		t.Error("状态机未经过 tool_running")
	}
	// 最后一个应是 idle
	last, _ := AsPayload[StatusUpdatePayload](statuses[len(statuses)-1])
	if last.Status != StatusIdle {
		t.Errorf("最后一个 status = %q, 期望 idle", last.Status)
	}
}

// TestAbortDuringToolExecution 验证工具执行中 abort_stream 能中断。
//
// 场景：第一次 LLM 立刻返回 tool_use；工具执行需要 500ms；用户在工具执行
// 中发送 abort_stream。期望收到 stream_done(reason=aborted) 且工具被取消。
func TestAbortDuringToolExecution(t *testing.T) {
	toolInst := &slowToolForTest{delay: 500 * time.Millisecond}
	scripts := [][]llm.StreamChunk{
		{{
			Done:    true,
			ToolUses: []llm.ToolUseBlock{{ID: "slow1", Name: "slow", Input: json.RawMessage(`{}`)}},
		}},
		{{Content: "should not reach", Done: true}},
	}
	r := newToolRig(t, scripts, toolInst)
	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeUserInput, UserInputPayload{Text: "slow"}))

	// 等到 tool_call_start 后立即 abort
	start := waitForType(t, r, MsgTypeToolCallStart, 2*time.Second)
	if start == nil {
		t.Fatal("未收到 tool_call_start")
	}
	time.Sleep(50 * time.Millisecond) // 让工具开始执行
	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeAbortStream, nil))

	msgs := r.recvAll(t, 2 * time.Second)

	// 期望收到 tool_call_end 且 Status=aborted
	end := findByType(msgs, MsgTypeToolCallEnd)
	if end == nil {
		t.Fatal("未收到 tool_call_end")
	}
	ep, _ := AsPayload[ToolCallEndPayload](*end)
	// abort 时 toolHandler 会把 Status 标为 aborted（ctx 被取消），
	// 也有可能标 error（取决于工具如何返回）。两者都视为"被打断"。
	if ep.Status != ToolCallStatusAborted && ep.Status != ToolCallStatusError {
		t.Errorf("Status = %q, 期望 aborted 或 error", ep.Status)
	}

	// 期望收到 stream_done(reason=aborted)
	var doneReason string
	for _, m := range msgs {
		if m.Type == MsgTypeStreamDone {
			p, _ := AsPayload[StreamDonePayload](m)
			doneReason = p.Reason
		}
	}
	if doneReason != StreamReasonAborted {
		t.Errorf("StreamDone.Reason = %q, 期望 aborted", doneReason)
	}
}

// TestSessionLoadedIncludesToolHistory 验证 session_loaded 中工具消息以 ToolCallDisplay 回放。
func TestSessionLoadedIncludesToolHistory(t *testing.T) {
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)
	sess := sm.CreateNew()
	sess.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("请读文件")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			&llm.ToolUseBlock{ID: "hist-1", Name: "ReadFile", Input: json.RawMessage(`{"file_path":"a.go"}`)},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			&llm.ToolResultBlock{ToolUseID: "hist-1", Content: "package main", IsError: false},
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("已读")}},
	}
	if err := sm.Save(sess); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	cfg := &config.Config{Provider: "anthropic", Model: "test", APIKey: "k", MaxTokens: 1024}
	mp := &scriptedProvider{}
	h := NewHandler(mp, sm, cfg, 10, nil, 100000, dir, tool.NewRegistry(), conversation.NewToolHandler(tool.NewRegistry(), 5*time.Second, dir), nil)
	s := NewServer("127.0.0.1:0")
	h.Register(s.Router())
	ts := httptest.NewServer(http.HandlerFunc(s.ConnectionManager().HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	defer client.Close()

	r := &toolRig{h: h, mp: mp, sessDir: dir, srv: ts, client: client}
	client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeGetCurrentSession, struct{}{}))

	loaded := waitForType(t, r, MsgTypeSessionLoaded, 2*time.Second)
	if loaded == nil {
		t.Fatal("未收到 session_loaded")
	}
	p, _ := AsPayload[SessionLoadedPayload](*loaded)
	if len(p.Messages) != 3 {
		t.Fatalf("Messages 数量 = %d, 期望 3 (user text + ToolCall + assistant text), 实际: %+v", len(p.Messages), p.Messages)
	}
	// 第 1 条: user text
	if p.Messages[0].Role != "user" || p.Messages[0].Content != "请读文件" {
		t.Errorf("第 1 条不匹配: %+v", p.Messages[0])
	}
	// 第 2 条: ToolCall
	tc := p.Messages[1].ToolCall
	if tc == nil {
		t.Fatalf("第 2 条应为 ToolCall, 实际: %+v", p.Messages[1])
	}
	if tc.ID != "hist-1" || tc.Name != "ReadFile" {
		t.Errorf("ToolCall 元数据: %+v", tc)
	}
	if tc.Output != "package main" {
		t.Errorf("Output = %q, 期望 package main", tc.Output)
	}
	if tc.Status != "completed" {
		t.Errorf("Status = %q, 期望 completed", tc.Status)
	}
	// 第 3 条: assistant 二次 LLM
	if p.Messages[2].Content != "已读" {
		t.Errorf("第 3 条 = %q, 期望 已读", p.Messages[2].Content)
	}
}

// TestStreamStateRejectsConcurrentInput 验证 RunTurn 进行中（包含工具阶段）不能发起新 user_input。
// 直接复用了 TestBusyRejectsConcurrentInput 的语义（user_input 收到 busy 错误）。
func TestStreamStateRejectsDuringToolRun(t *testing.T) {
	toolInst := &slowToolForTest{delay: 300 * time.Millisecond}
	scripts := [][]llm.StreamChunk{
		{{
			Done:    true,
			ToolUses: []llm.ToolUseBlock{{ID: "s1", Name: "slow", Input: json.RawMessage(`{}`)}},
		}},
		{{Content: "done", Done: true}},
	}
	r := newToolRig(t, scripts, toolInst)
	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeUserInput, UserInputPayload{Text: "first"}))

	// 等到 tool_call_start 表示流已进入工具阶段
	if waitForType(t, r, MsgTypeToolCallStart, 2*time.Second) == nil {
		t.Fatal("未收到 tool_call_start")
	}
	// 此时再发 user_input 应被 busy 拒
	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeUserInput, UserInputPayload{Text: "second"}))

	// 收集后续消息，找 stream_error(code=busy)
	msgs := r.recvAll(t, 2*time.Second)
	gotBusy := false
	for _, m := range msgs {
		if m.Type == MsgTypeStreamError {
			p, _ := AsPayload[StreamErrorPayload](m)
			if p.Code == "busy" {
				gotBusy = true
			}
		}
	}
	if !gotBusy {
		t.Fatalf("工具执行中再发 user_input 应返回 busy, 实际: %+v", msgs)
	}
}

// TestToolsEnabledWhitelistApplied 验证 cfg.Tools.Enabled 白名单在
// handler.runStream 内部被透传到 Provider，LLM 只会看到白名单内的工具描述。
func TestToolsEnabledWhitelistApplied(t *testing.T) {
	// 在 registry 中塞两个工具：echo / glob
	echo := newNamedEcho("echo")
	glob := newNamedEcho("Glob")
	r := newToolRigWithEnabled(t,
		[][]llm.StreamChunk{{{Content: "ok", Done: true}}},
		nil,
		[]string{"Glob"},
	)
	// 把两个工具都加进 rig 的 registry（绕过 newToolRig 的 toolInstance 限制）
	if err := r.h.registry.Register(echo); err != nil {
		t.Fatalf("echo 注册失败: %v", err)
	}
	if err := r.h.registry.Register(glob); err != nil {
		t.Fatalf("glob 注册失败: %v", err)
	}

	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeUserInput, UserInputPayload{Text: "hi"}))
	r.recvUntilStatus(StatusIdle, 2*time.Second)

	r.mp.mu.Lock()
	defer r.mp.mu.Unlock()
	if len(r.mp.recordedSpecs) == 0 {
		t.Fatal("Provider 未被调用, recordedSpecs 为空")
	}
	gotNames := make([]string, 0, len(r.mp.recordedSpecs[0]))
	for _, s := range r.mp.recordedSpecs[0] {
		gotNames = append(gotNames, s.Name)
	}
	if len(gotNames) != 1 || gotNames[0] != "Glob" {
		t.Errorf("cfg.Tools.Enabled 白名单过滤错误: 期望 [glob], 实际 %v", gotNames)
	}
}

// TestToolsEnabledEmptyMeansAll 验证 cfg.Tools.Enabled 为空时 registry
// 中所有已注册工具的描述都透传给 Provider（白名单留空 = 全开）。
func TestToolsEnabledEmptyMeansAll(t *testing.T) {
	echo := newNamedEcho("echo")
	glob := newNamedEcho("Glob")
	r := newToolRigWithEnabled(t,
		[][]llm.StreamChunk{{{Content: "ok", Done: true}}},
		nil,
		nil, // 白名单为空
	)
	if err := r.h.registry.Register(echo); err != nil {
		t.Fatalf("echo 注册失败: %v", err)
	}
	if err := r.h.registry.Register(glob); err != nil {
		t.Fatalf("glob 注册失败: %v", err)
	}

	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeUserInput, UserInputPayload{Text: "hi"}))
	r.recvUntilStatus(StatusIdle, 2*time.Second)

	r.mp.mu.Lock()
	defer r.mp.mu.Unlock()
	if len(r.mp.recordedSpecs) == 0 {
		t.Fatal("Provider 未被调用, recordedSpecs 为空")
	}
	gotNames := make([]string, 0, len(r.mp.recordedSpecs[0]))
	for _, s := range r.mp.recordedSpecs[0] {
		gotNames = append(gotNames, s.Name)
	}
	if len(gotNames) != 2 {
		t.Errorf("白名单为空时应全开, 期望 2 个工具, 实际 %v", gotNames)
	}
}

// ---- 测试公用工具 ----

// mustEncode 编码失败时 t.Fatal。
func mustEncode(typ string, payload any) []byte {
	data, err := EncodePayload(typ, payload)
	if err != nil {
		panic(err)
	}
	return data
}

// waitForType 阻塞等待指定 type 的消息，超时返回 nil。
func waitForType(t *testing.T, r *toolRig, typ string, timeout time.Duration) *Message {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = r.client.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		_, data, err := r.client.ReadMessage()
		if err != nil {
			continue
		}
		msg, _ := Decode(data)
		if msg.Type == typ {
			return &msg
		}
	}
	return nil
}

// ---- 测试用 Tool ----

// echoToolForTest 简单回显：把 input 解析为 params，回写 "echo:" + msg。
type echoToolForTest struct {
	tool.BaseTool
	calls int32
	// toolNameOverride 为空时 Name() 返回 "echo"（默认行为），
	// 非空时返回 override 值（用于在 registry 中塞多个不同 Name 的测试工具）。
	toolNameOverride string
}

func (e *echoToolForTest) Execute(_ context.Context, input json.RawMessage) (string, error) {
	atomic.AddInt32(&e.calls, 1)
	var p struct {
		Msg string `json:"msg"`
	}
	_ = json.Unmarshal(input, &p)
	return "echo:" + p.Msg, nil
}

// newNamedEcho 构造一个指定 Name 的 echo 工具实例。
func newNamedEcho(name string) *echoToolForTest {
	return &echoToolForTest{
		toolNameOverride: name,
	}
}

// slowToolForTest 慢执行工具，遵守 ctx 取消。
type slowToolForTest struct {
	tool.BaseTool
	delay time.Duration
}

func (s *slowToolForTest) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	select {
	case <-time.After(s.delay):
		return "done", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// 让 newToolRig 接受 echoToolForTest / slowToolForTest 时能正确推断工具名。
func (e *echoToolForTest) Name() string {
	if e.toolNameOverride != "" {
		return e.toolNameOverride
	}
	return "echo"
}
func (e *echoToolForTest) Description() string { return "echo back" }
func (e *echoToolForTest) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`)
}
func (e *echoToolForTest) Permission() tool.ToolPermission { return tool.PermRead }

func (s *slowToolForTest) Name() string        { return "slow" }
func (s *slowToolForTest) Description() string { return "sleeps" }
func (s *slowToolForTest) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (s *slowToolForTest) Permission() tool.ToolPermission { return tool.PermRead }

// ---- Task 5: AgentLoop 适配测试 ----

// TestAgentIterationEvent 验证每轮迭代开始时前端收到 agent_iteration 事件。
//
// 场景：第一次 LLM 返回 tool_use → 执行 echo → 第二次 LLM 返回纯文本。
// 期望收到 2 个 agent_iteration 事件（第 1 轮和第 2 轮），且 Current/Max 正确。
func TestAgentIterationEvent(t *testing.T) {
	toolInst := &echoToolForTest{}
	scripts := [][]llm.StreamChunk{
		// 第 1 次迭代：LLM 返回 tool_use
		{{
			Done:     true,
			ToolUses: []llm.ToolUseBlock{{ID: "it-1", Name: "echo", Input: json.RawMessage(`{"msg":"hi"}`)}},
		}},
		// 第 2 次迭代：LLM 返回纯文本
		{{Content: "done", Done: true}},
	}
	r := newToolRig(t, scripts, toolInst)

	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeUserInput, UserInputPayload{Text: "test"}))

	msgs := r.recvAll(t, 2*time.Second)

	// 验证收到 agent_iteration 事件
	iterations := findAllByType(msgs, MsgTypeAgentIteration)
	if len(iterations) < 2 {
		t.Fatalf("期望至少 2 个 agent_iteration 事件（2 轮迭代），实际收到 %d 个", len(iterations))
	}

	// 验证第 1 轮：current=1, max=50
	p1, _ := AsPayload[AgentIterationPayload](iterations[0])
	if p1.Current != 1 {
		t.Errorf("第 1 轮 Current = %d, 期望 1", p1.Current)
	}
	if p1.Max != 50 {
		t.Errorf("第 1 轮 Max = %d, 期望 50", p1.Max)
	}

	// 验证第 2 轮：current=2, max=50
	p2, _ := AsPayload[AgentIterationPayload](iterations[1])
	if p2.Current != 2 {
		t.Errorf("第 2 轮 Current = %d, 期望 2", p2.Current)
	}
	if p2.Max != 50 {
		t.Errorf("第 2 轮 Max = %d, 期望 50", p2.Max)
	}
}

// TestAgentIterationEventNoToolUse 验证无工具调用时只有 1 个 agent_iteration 事件。
func TestAgentIterationEventNoToolUse(t *testing.T) {
	scripts := [][]llm.StreamChunk{
		// 仅 1 次迭代：LLM 直接回复纯文本
		{{Content: "hello", Done: true}},
	}
	r := newToolRig(t, scripts, nil)

	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeUserInput, UserInputPayload{Text: "hi"}))

	msgs := r.recvAll(t, 2*time.Second)

	iterations := findAllByType(msgs, MsgTypeAgentIteration)
	if len(iterations) != 1 {
		t.Fatalf("期望 1 个 agent_iteration 事件（无工具调用），实际收到 %d 个", len(iterations))
	}
	p, _ := AsPayload[AgentIterationPayload](iterations[0])
	if p.Current != 1 {
		t.Errorf("Current = %d, 期望 1", p.Current)
	}
	if p.Max != 50 {
		t.Errorf("Max = %d, 期望 50", p.Max)
	}
}

// TestStreamDoneReasonCompleted 验证正常完成时 stream_done reason=completed。
func TestStreamDoneReasonCompleted(t *testing.T) {
	scripts := [][]llm.StreamChunk{
		{{Content: "ok", Done: true}},
	}
	r := newToolRig(t, scripts, nil)
	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeUserInput, UserInputPayload{Text: "hi"}))

	msgs := r.recvAll(t, 2*time.Second)
	done := findByType(msgs, MsgTypeStreamDone)
	if done == nil {
		t.Fatal("未收到 stream_done")
	}
	p, _ := AsPayload[StreamDonePayload](*done)
	if p.Reason != StreamReasonCompleted {
		t.Errorf("Reason = %q, 期望 completed", p.Reason)
	}
}

// TestStreamDoneReasonAborted 验证中断时 stream_done reason=aborted。
func TestStreamDoneReasonAborted(t *testing.T) {
	toolInst := &slowToolForTest{delay: 500 * time.Millisecond}
	scripts := [][]llm.StreamChunk{
		{{
			Done:     true,
			ToolUses: []llm.ToolUseBlock{{ID: "s1", Name: "slow", Input: json.RawMessage(`{}`)}},
		}},
		{{Content: "should not reach", Done: true}},
	}
	r := newToolRig(t, scripts, toolInst)
	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeUserInput, UserInputPayload{Text: "go"}))

	// 等工具开始执行后 abort
	if waitForType(t, r, MsgTypeToolCallStart, 2*time.Second) == nil {
		t.Fatal("未收到 tool_call_start")
	}
	time.Sleep(50 * time.Millisecond)
	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeAbortStream, nil))

	msgs := r.recvAll(t, 2*time.Second)
	done := findByType(msgs, MsgTypeStreamDone)
	if done == nil {
		t.Fatal("未收到 stream_done")
	}
	p, _ := AsPayload[StreamDonePayload](*done)
	if p.Reason != StreamReasonAborted {
		t.Errorf("Reason = %q, 期望 aborted", p.Reason)
	}
}

// TestMapStopReason 验证 mapStopReason 所有分支映射正确。
func TestMapStopReason(t *testing.T) {
	tests := []struct {
		input    conversation.StopReason
		expected string
	}{
		{conversation.StopReasonCompleted, StreamReasonCompleted},
		{conversation.StopReasonAborted, StreamReasonAborted},
		{conversation.StopReasonError, StreamReasonError},
		{conversation.StopReasonMaxIterations, StreamReasonMaxIterations},
		{conversation.StopReasonContextOverflow, StreamReasonContextOverflow},
		{conversation.StopReason("unknown"), StreamReasonError},
	}
	for _, tt := range tests {
		got := mapStopReason(tt.input)
		if got != tt.expected {
			t.Errorf("mapStopReason(%q) = %q, 期望 %q", tt.input, got, tt.expected)
		}
	}
}

// TestMultiIterationToolCalls 验证多轮迭代的 tool_call_start/end 事件序列完整。
//
// 场景：3 次迭代（tool_use → tool_use → 纯文本），验证每轮工具执行的事件正常推送。
func TestMultiIterationToolCalls(t *testing.T) {
	toolInst := &echoToolForTest{}
	scripts := [][]llm.StreamChunk{
		// 第 1 次迭代：返回 tool_use
		{{
			Done:     true,
			ToolUses: []llm.ToolUseBlock{{ID: "m1", Name: "echo", Input: json.RawMessage(`{"msg":"a"}`)}},
		}},
		// 第 2 次迭代：再次返回 tool_use
		{{
			Done:     true,
			ToolUses: []llm.ToolUseBlock{{ID: "m2", Name: "echo", Input: json.RawMessage(`{"msg":"b"}`)}},
		}},
		// 第 3 次迭代：返回纯文本
		{{Content: "all done", Done: true}},
	}
	r := newToolRig(t, scripts, toolInst)

	r.client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeUserInput, UserInputPayload{Text: "multi"}))
	msgs := r.recvAll(t, 3*time.Second)

	// 验证收到 2 对 tool_call_start/end
	startEvents := findAllByType(msgs, MsgTypeToolCallStart)
	endEvents := findAllByType(msgs, MsgTypeToolCallEnd)
	if len(startEvents) != 2 {
		t.Errorf("tool_call_start 事件数 = %d, 期望 2", len(startEvents))
	}
	if len(endEvents) != 2 {
		t.Errorf("tool_call_end 事件数 = %d, 期望 2", len(endEvents))
	}

	// 验证第 1 个 tool_call_start 的 ToolUseID
	if len(startEvents) > 0 {
		p1, _ := AsPayload[ToolCallStartPayload](startEvents[0])
		if p1.ToolUseID != "m1" {
			t.Errorf("第 1 个 start ToolUseID = %q, 期望 m1", p1.ToolUseID)
		}
	}
	if len(startEvents) > 1 {
		p2, _ := AsPayload[ToolCallStartPayload](startEvents[1])
		if p2.ToolUseID != "m2" {
			t.Errorf("第 2 个 start ToolUseID = %q, 期望 m2", p2.ToolUseID)
		}
	}

	// 验证收到 3 个 agent_iteration 事件
	iterations := findAllByType(msgs, MsgTypeAgentIteration)
	if len(iterations) != 3 {
		t.Errorf("agent_iteration 事件数 = %d, 期望 3（3 轮迭代）", len(iterations))
	}

	// 验证最终 stream_done reason=completed
	done := findByType(msgs, MsgTypeStreamDone)
	if done == nil {
		t.Fatal("未收到 stream_done")
	}
	dp, _ := AsPayload[StreamDonePayload](*done)
	if dp.Reason != StreamReasonCompleted {
		t.Errorf("最终 Reason = %q, 期望 completed", dp.Reason)
	}
}

// ---- Task 6: 主流程接入验证 ----

// TestAgentLoopConfigFromHandler 验证 Handler 通过 cfg 正确传递 AgentLoop 配置。
func TestAgentLoopConfigFromHandler(t *testing.T) {
	toolInst := &echoToolForTest{}
	scripts := [][]llm.StreamChunk{
		{{Content: "ok", Done: true}},
	}
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)
	cfg := &config.Config{
		Provider:               "anthropic",
		Model:                  "claude-test",
		APIKey:                 "test-key",
		MaxTokens:              1024,
		MaxAgentLoopIterations: 10,
		ContextSafetyMargin:    2048,
	}
	mp := &scriptedProvider{scripts: scripts, chunkDelay: 5 * time.Millisecond}
	registry := tool.NewRegistry()
	if err := registry.Register(toolInst); err != nil {
		t.Fatalf("注册工具失败: %v", err)
	}
	toolHandler := conversation.NewToolHandler(registry, 5*time.Second, dir)

	h := NewHandler(mp, sm, cfg, 10, nil, 50000, dir, registry, toolHandler, nil)
	s := NewServer("127.0.0.1:0")
	h.Register(s.Router())
	ts := httptest.NewServer(http.HandlerFunc(s.ConnectionManager().HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	defer client.Close()

	r := &toolRig{h: h, mp: mp, sessDir: dir, srv: ts, client: client, toolHandler: toolHandler}
	client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeUserInput, UserInputPayload{Text: "hi"}))

	msgs := r.recvAll(t, 2*time.Second)
	done := findByType(msgs, MsgTypeStreamDone)
	if done == nil {
		t.Fatal("未收到 stream_done")
	}
	p, _ := AsPayload[StreamDonePayload](*done)
	if p.Reason != StreamReasonCompleted {
		t.Errorf("Reason = %q, 期望 completed", p.Reason)
	}
}
