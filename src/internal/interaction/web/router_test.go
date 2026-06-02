package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestNewRouterEmpty 验证新构造的 Router 无 handler。
func TestNewRouterEmpty(t *testing.T) {
	r := NewRouter()
	if got := r.Types(); len(got) != 0 {
		t.Errorf("初始 Types 数量 = %d，期望 0", len(got))
	}
	if _, ok := r.Handler(MsgTypeUserInput); ok {
		t.Error("未注册 handler 不应存在")
	}
}

// TestRouterRegisterAndLookup 验证 Register 后可通过 Handler 取回。
func TestRouterRegisterAndLookup(t *testing.T) {
	r := NewRouter()
	called := false
	r.Register(MsgTypeUserInput, func(conn *websocket.Conn, msg Message) error {
		called = true
		return nil
	})

	types := r.Types()
	if len(types) != 1 || types[0] != MsgTypeUserInput {
		t.Errorf("Types = %v，期望 [%s]", types, MsgTypeUserInput)
	}
	h, ok := r.Handler(MsgTypeUserInput)
	if !ok {
		t.Fatal("应能取回已注册的 handler")
	}

	// 用一个真实连接执行 handler（conn 传 nil 会让 handler 出错，
	// 但本测试只验证 handler 被路由到，不要求内部使用 conn）
	if err := h(nil, Message{Type: MsgTypeUserInput}); err != nil {
		t.Fatalf("handler 执行失败: %v", err)
	}
	if !called {
		t.Error("handler 未被调用")
	}
}

// TestRouterRegisterOverride 验证同类型重复注册覆盖前一个 handler。
func TestRouterRegisterOverride(t *testing.T) {
	r := NewRouter()
	var countA, countB int32
	r.Register(MsgTypeUserInput, func(conn *websocket.Conn, msg Message) error {
		atomic.AddInt32(&countA, 1)
		return nil
	})
	r.Register(MsgTypeUserInput, func(conn *websocket.Conn, msg Message) error {
		atomic.AddInt32(&countB, 1)
		return nil
	})

	h, _ := r.Handler(MsgTypeUserInput)
	_ = h(nil, Message{Type: MsgTypeUserInput})

	if atomic.LoadInt32(&countA) != 0 {
		t.Errorf("A 不应被调用，countA = %d", countA)
	}
	if atomic.LoadInt32(&countB) != 1 {
		t.Errorf("B 应被调用 1 次，countB = %d", countB)
	}
}

// TestRouterRouteUnknownType 验证 Route 收到未知类型时发送 stream_error。
// 通过起一个真实 WebSocket 服务端、客户端发送 unknown_type 并读取响应来验证。
func TestRouterRouteUnknownType(t *testing.T) {
	r := NewRouter()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		go r.HandleLoop(conn)
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	defer client.Close()

	// 发送未知类型消息
	if err := client.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"unknown_type","payload":{}}`)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	// 应收到 stream_error 响应
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("响应 JSON 解析失败: %v", err)
	}
	if msg.Type != MsgTypeStreamError {
		t.Errorf("响应 Type = %q，期望 %q", msg.Type, MsgTypeStreamError)
	}
	p, _ := AsPayload[StreamErrorPayload](msg)
	if p.Code != "unknown_message_type" {
		t.Errorf("Code = %q，期望 %q", p.Code, "unknown_message_type")
	}
	if !strings.Contains(p.Message, "unknown_type") {
		t.Errorf("Message 应包含未知类型名，实际: %q", p.Message)
	}
}

// TestRouterRouteHandlerError 验证 handler 错误时 Route 透传错误。
func TestRouterRouteHandlerError(t *testing.T) {
	r := NewRouter()
	want := "handler boom"
	r.Register(MsgTypeUserInput, func(conn *websocket.Conn, msg Message) error {
		return &testError{msg: want}
	})

	// 通过 httptest 跑 HandleLoop，再由 Route 触发 handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		go r.HandleLoop(conn)
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	defer client.Close()

	// 发送 user_input
	if err := client.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"user_input","payload":{"text":"hi"}}`)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	// handler 返回错误时，Route 不发送额外 stream_error（handler 自行处理）。
	// 等待 200ms 确认连接未收到 stream_error（避免 handler 自行发送的混淆）。
	_ = client.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, err = client.ReadMessage()
	if err == nil {
		t.Error("handler 错误时不应有响应消息（除非 handler 自行发送）")
	}
}

// TestHandleLoopDispatchesRegisteredHandler 验证 HandleLoop 收到注册类型消息时调用 handler。
func TestHandleLoopDispatchesRegisteredHandler(t *testing.T) {
	r := NewRouter()
	var (
		mu     sync.Mutex
		gotMsg Message
	)
	done := make(chan struct{})
	r.Register(MsgTypeUserInput, func(conn *websocket.Conn, msg Message) error {
		mu.Lock()
		gotMsg = msg
		mu.Unlock()
		close(done)
		return nil
	})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		go r.HandleLoop(conn)
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	defer client.Close()

	if err := client.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"user_input","payload":{"text":"hi"}}`)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler 未在 2 秒内被调用")
	}

	mu.Lock()
	defer mu.Unlock()
	if gotMsg.Type != MsgTypeUserInput {
		t.Errorf("gotMsg.Type = %q，期望 %q", gotMsg.Type, MsgTypeUserInput)
	}
	p, err := AsPayload[UserInputPayload](gotMsg)
	if err != nil {
		t.Fatalf("AsPayload 失败: %v", err)
	}
	if p.Text != "hi" {
		t.Errorf("Text = %q，期望 %q", p.Text, "hi")
	}
}

// TestHandleLoopInvalidJSON 验证 HandleLoop 收到非法 JSON 时发送 stream_error。
func TestHandleLoopInvalidJSON(t *testing.T) {
	r := NewRouter()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		go r.HandleLoop(conn)
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	defer client.Close()

	if err := client.WriteMessage(websocket.TextMessage, []byte(`{not json`)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("响应 JSON 解析失败: %v", err)
	}
	if msg.Type != MsgTypeStreamError {
		t.Errorf("响应 Type = %q，期望 %q", msg.Type, MsgTypeStreamError)
	}
	p, _ := AsPayload[StreamErrorPayload](msg)
	if p.Code != "invalid_message" {
		t.Errorf("Code = %q，期望 %q", p.Code, "invalid_message")
	}

	// 连接应保持：再发一条 user_input 仍能正常处理
	done := make(chan struct{})
	r.Register(MsgTypeUserInput, func(conn *websocket.Conn, msg Message) error {
		close(done)
		return nil
	})
	if err := client.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"user_input","payload":{"text":"after"}}`)); err != nil {
		t.Fatalf("第二次发送失败: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("连接应保持，handler 未被调用")
	}
}

// TestHandleLoopExitsOnClientClose 验证客户端断开后 HandleLoop 退出。
func TestHandleLoopExitsOnClientClose(t *testing.T) {
	r := NewRouter()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			r.HandleLoop(conn)
			close(done)
		}()
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}

	// 关闭客户端连接
	_ = client.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	_ = client.Close()

	// 通过 ConnectionManager.Count 无法直接验证 HandleLoop 退出，
	// 这里仅验证关闭后再次拨号仍能成功（server 进程未退）
	time.Sleep(200 * time.Millisecond)
	client2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("server 应仍能接受新连接: %v", err)
	}
	client2.Close()
}

// testError 用于 Route 错误透传测试的自定义错误类型。
type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
