package web

import (
	"context"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestStaticFilesEmbedded 验证 embed.FS 嵌入的 index.html 可通过 HTTP 访问。
func TestStaticFilesEmbedded(t *testing.T) {
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		t.Fatalf("提取 static 子目录失败: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(staticSub)))

	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / 失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("状态码 = %d，期望 200", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	if !strings.Contains(string(body), "<title>CodePilot</title>") {
		t.Errorf("响应体应包含 <title>CodePilot</title>，实际: %s", body)
	}
	if !strings.Contains(string(body), `class="topbar"`) {
		t.Errorf("响应体应包含 topbar 结构，实际: %s", body)
	}
	if !strings.Contains(string(body), `href="/style.css"`) {
		t.Errorf("响应体应引用 /style.css，实际: %s", body)
	}
}

// TestWebSocketUpgrade 验证 ws:// 握手成功升级为 WebSocket 连接。
func TestWebSocketUpgrade(t *testing.T) {
	s := NewServer("127.0.0.1:0")
	ts := httptest.NewServer(http.HandlerFunc(s.ConnectionManager().HandleWS))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket 拨号失败: %v, status=%d", err, statusOf(resp))
	}
	defer conn.Close()

	if conn.RemoteAddr() == nil {
		t.Error("WebSocket 连接应有 RemoteAddr")
	}
}

// TestPortConflictReturnsFriendlyError 验证端口被占用时返回明确错误信息。
func TestPortConflictReturnsFriendlyError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占位 listen 失败: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	s := NewServer(addr)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = s.Start(ctx)
	if err == nil {
		t.Fatal("期望端口冲突错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "已被占用") {
		t.Errorf("错误应包含 '已被占用'，实际: %v", err)
	}
}

// TestCrossOriginRejected 验证带恶意 Origin 的 WebSocket 握手被拒绝（403）。
func TestCrossOriginRejected(t *testing.T) {
	s := NewServer("127.0.0.1:0")
	ts := httptest.NewServer(http.HandlerFunc(s.ConnectionManager().HandleWS))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	headers := http.Header{}
	headers.Set("Origin", "http://evil.com")

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("期望跨域握手被拒绝，实际握手成功")
	}
	if statusOf(resp) != http.StatusForbidden {
		t.Errorf("期望 HTTP 403，实际: %d", statusOf(resp))
	}
}

// TestConnectionManagerAddRemove 验证连接的 Add/Remove 与 Count 同步。
func TestConnectionManagerAddRemove(t *testing.T) {
	m := NewConnectionManager(nil)
	if got := m.Count(); got != 0 {
		t.Errorf("初始 Count = %d，期望 0", got)
	}

	ts := httptest.NewServer(http.HandlerFunc(m.HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}

	waitUntil(t, 1*time.Second, func() bool { return m.Count() == 1 }, "连接建立后 Count 应为 1")

	_ = conn.Close()
	waitUntil(t, 1*time.Second, func() bool { return m.Count() == 0 }, "连接断开后 Count 应为 0")
}

// TestConnectionManagerAllClosedSignalOnLastDisconnect 验证：
// 单连接场景下，最后一个连接断开时 AllClosed channel 收到一次信号。
// 这是「浏览器关闭 → 自动退出」链路的最关键合约。
func TestConnectionManagerAllClosedSignalOnLastDisconnect(t *testing.T) {
	m := NewConnectionManager(nil)
	ch := m.AllClosed()

	ts := httptest.NewServer(http.HandlerFunc(m.HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	waitUntil(t, 1*time.Second, func() bool { return m.Count() == 1 }, "Count 应为 1")

	// 有活跃连接时不应有 AllClosed 信号
	select {
	case <-ch:
		t.Fatal("有活跃连接时不应收到 AllClosed 信号")
	default:
	}

	// 关闭连接，期望收到信号
	_ = conn.Close()
	select {
	case <-ch:
		// OK
	case <-time.After(1 * time.Second):
		t.Fatal("最后一个连接断开后 1 秒内未收到 AllClosed 信号")
	}

	// channel 是"信号"语义，不应被关闭：第二次读取应阻塞而非拿到零值
	select {
	case <-ch:
		t.Fatal("AllClosed 信号应仅触发一次，channel 不应被关闭")
	case <-time.After(100 * time.Millisecond):
		// OK：未关闭
	}
}

// TestConnectionManagerAllClosedSignalOnlyOnLastOfMany 验证：
// 多连接场景下，只有最后一个连接断开时才发 AllClosed 信号；
// 中间断开（仍剩至少一个连接）时不应触发。
func TestConnectionManagerAllClosedSignalOnlyOnLastOfMany(t *testing.T) {
	m := NewConnectionManager(nil)
	ch := m.AllClosed()

	ts := httptest.NewServer(http.HandlerFunc(m.HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"

	conn1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("拨号 conn1 失败: %v", err)
	}
	waitUntil(t, 1*time.Second, func() bool { return m.Count() == 1 }, "Count 应为 1")

	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("拨号 conn2 失败: %v", err)
	}
	waitUntil(t, 1*time.Second, func() bool { return m.Count() == 2 }, "Count 应为 2")

	// 关掉 conn1：仍剩 conn2，不应触发信号
	_ = conn1.Close()
	waitUntil(t, 1*time.Second, func() bool { return m.Count() == 1 }, "Count 应降为 1")
	select {
	case <-ch:
		t.Fatal("仍有活跃连接时不应收到 AllClosed 信号")
	case <-time.After(150 * time.Millisecond):
		// OK
	}

	// 关掉 conn2：此时才应触发信号
	_ = conn2.Close()
	select {
	case <-ch:
		// OK
	case <-time.After(1 * time.Second):
		t.Fatal("最后一个连接断开后未收到 AllClosed 信号")
	}
}

// TestConnectionManagerAllClosedSignalRepeated 验证：
// 断开 → 重连 → 再断开 的循环里，每次断开（最后一条断开）都应触发一次信号。
// 这是 main 端做 5 秒防抖时需要的能力：浏览器若短时间内反复开关，
// 每次"关"都应被感知到。
func TestConnectionManagerAllClosedSignalRepeated(t *testing.T) {
	m := NewConnectionManager(nil)
	ch := m.AllClosed()

	ts := httptest.NewServer(http.HandlerFunc(m.HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"

	dialAndDisconnect := func() {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("拨号失败: %v", err)
		}
		waitUntil(t, 1*time.Second, func() bool { return m.Count() == 1 }, "Count 应为 1")
		_ = conn.Close()
	}

	dialAndDisconnect()
	select {
	case <-ch:
	case <-time.After(1 * time.Second):
		t.Fatal("第 1 轮断开未收到 AllClosed 信号")
	}

	// 消费掉信号后，下一轮断开应能再次产生信号（说明 channel 是复用而不是一次性）
	dialAndDisconnect()
	select {
	case <-ch:
	case <-time.After(1 * time.Second):
		t.Fatal("第 2 轮断开未收到 AllClosed 信号")
	}
}

// TestServerStartShutdown 验证 Server.Start 在 ctx 取消时优雅退出。
func TestServerStartShutdown(t *testing.T) {
	// 占用一个端口以避免与 dev 环境冲突
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占位 listen 失败: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	s := NewServer(addr)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	// 等待 server 真正 listen
	waitUntil(t, 2*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
		return false
	}, "server 应在 2 秒内 listen")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start 返回错误: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start 在 ctx 取消后 3 秒内未退出")
	}
}

// TestServerRandomPort 验证使用 :0 端口时由 OS 分配真实端口，
// Ready() 通道触发后 Addr() 返回真实可用地址。
// 这是支持「多 CodePilot 进程并行启动」的关键能力。
func TestServerRandomPort(t *testing.T) {
	s := NewServer("127.0.0.1:0")

	// 构造时 addr 仍是 :0，Ready 通道尚未关闭。
	select {
	case <-s.Ready():
		t.Fatal("Start 之前 Ready 通道不应触发")
	default:
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	// listen 完成后 Ready 应在合理时间内触发。
	select {
	case <-s.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("Ready 通道应在 2 秒内触发")
	}

	// Ready 后 Addr 必须是 OS 分配的真实端口，而不是 :0。
	realAddr := s.Addr()
	if realAddr == "" || strings.HasSuffix(realAddr, ":0") {
		t.Fatalf("Ready 后 Addr 应返回真实端口，实际: %q", realAddr)
	}

	// 真实端口应当可连通。
	c, err := net.DialTimeout("tcp", realAddr, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("无法连接真实端口 %s: %v", realAddr, err)
	}
	c.Close()

	// 再起一个 server 同样能拿到一个不同的随机端口（验证并行可用性）。
	s2 := NewServer("127.0.0.1:0")
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan error, 1)
	go func() { done2 <- s2.Start(ctx2) }()
	select {
	case <-s2.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("第二个 server 的 Ready 应在 2 秒内触发")
	}
	if s2.Addr() == realAddr {
		t.Errorf("两个 :0 server 不应分配到相同端口: %s", realAddr)
	}

	cancel()
	cancel2()
	<-done
	<-done2
}

// statusOf 兼容 resp 可能为 nil 的情况。
func statusOf(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

// waitUntil 轮询条件直到成立或超时；用于异步 goroutine 同步。
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", msg)
}
