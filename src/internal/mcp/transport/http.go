package transport

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Option 是 httpTransport 的 Functional Options 配置项，由调用方通过 NewHTTP(...Option) 传入。
//
// 公开类型是为了让 external 包（mcp/config）能在不依赖 transport 内部结构的前提下
// 复用 WithBearerToken / WithHTTPHeader / WithHTTPTimeout 等构造器——这是
// Functional Options 模式的常见扩展方式。Option 的实现细节仍包内隐藏。
type Option func(*httpTransport)

// WithBearerToken 设置 Bearer Token 鉴权（Authorization: Bearer <token>）。
func WithBearerToken(token string) Option {
	return func(t *httpTransport) { t.bearerToken = token }
}

// WithBasicAuth 设置 HTTP Basic Auth。
func WithBasicAuth(user, pass string) Option {
	return func(t *httpTransport) { t.basicAuth = &basicCred{user: user, pass: pass} }
}

// WithHTTPHeader 增加自定义请求头（多次调用累加）。
func WithHTTPHeader(k, v string) Option {
	return func(t *httpTransport) {
		if t.extraHeaders == nil {
			t.extraHeaders = make(map[string]string)
		}
		t.extraHeaders[k] = v
	}
}

// WithHTTPTimeout 设置单次 HTTP 请求的超时时间。
func WithHTTPTimeout(d time.Duration) Option {
	return func(t *httpTransport) {
		t.timeout = d
		if t.client != nil {
			t.client.Timeout = d
		}
	}
}

// WithHTTPClient 替换默认 *http.Client（高级用法：自定义 Transport / CookieJar 等）。
func WithHTTPClient(c *http.Client) Option {
	return func(t *httpTransport) { t.client = c }
}

// WithHTTPRequestContext 设置每次 HTTP 请求绑定的 context。
//
// 用途：当 ctx 取消时，client.Do 会立即返回（无需等 client.Timeout）。
// 默认 nil（使用 client.Timeout 控制超时）。通常由 session.Pool.InitializeAll
// 把握手 ctx 注入，让 ctx 取消能立即中断 in-flight HTTP 请求。
func WithHTTPRequestContext(ctx context.Context) Option {
	return func(t *httpTransport) { t.reqCtx = ctx }
}

type basicCred struct {
	user, pass string
}

// HTTPError 表示 HTTP 状态码非 2xx 时的错误。
type HTTPError struct {
	StatusCode int
	Body       string
}

// Error 实现 error 接口。
func (e *HTTPError) Error() string {
	return fmt.Sprintf("http: 服务器返回 %d: %s", e.StatusCode, e.Body)
}

// httpTransport 实现 MCP Streamable HTTP 传输。
//
// 协议规范要点：
//   - POST 单次请求/响应，Content-Type: application/json
//   - Accept: application/json, text/event-stream（双兼容，server 决定返回哪种）
//   - 首次响应可能携带 Mcp-Session-Id header，后续请求回传
//   - server-initiated notification（本步骤不实现，留待后续）
//
// Transport 接口适配：
//   - HTTP 单次 POST 即可获得完整 request-response，但 Transport 接口要求 Send/Recv 分开
//   - 设计：Send 内部发请求 + 读 body + 解析为消息，投递到内部 chan；Recv 从 chan 读
//   - 调用方必须按 Send→Recv 顺序串行调用，与 stdio 语义一致
type httpTransport struct {
	url          string
	client       *http.Client
	bearerToken  string
	basicAuth    *basicCred
	extraHeaders map[string]string
	timeout      time.Duration

	mu        sync.Mutex
	sessionID string
	alive     bool
	closed    bool
	// reqCtx 绑定到每个 HTTP request；外部可通过 WithRequestContext 注入。
	// 为 nil 时使用 request.DefaultClient 默认行为（受 client.Timeout 限制）。
	reqCtx context.Context

	// respCh 在每次 Send 时重建，缓冲 1 允许 Send 完成后 Recv 还没调用时数据暂存
	respCh chan httpResponse
}

type httpResponse struct {
	data []byte
	err  error
}

// NewHTTP 构造 Streamable HTTP Transport。需调用 Connect 校验 URL。
//
// 返回类型为未导出的 *httpTransport,但实现 Transport 接口,调用方按
// transport.Transport 接口使用即可。opts 接收外部包（mcp/config）构造的 Option。
func NewHTTP(url string, opts ...Option) *httpTransport {
	t := &httpTransport{
		url:     url,
		client:  &http.Client{Timeout: 30 * time.Second},
		timeout: 30 * time.Second,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Connect 校验 URL 非空并标记 alive。
// HTTP Transport 不需要建立持久连接（每次请求独立 TCP），所以 Connect 只做 URL 校验。
func (t *httpTransport) Connect(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.alive {
		return nil
	}
	if t.url == "" {
		return fmt.Errorf("http: URL 不能为空")
	}
	t.alive = true
	t.closed = false
	return nil
}

// Send 发送单条 JSON-RPC 消息，解析响应后投递到 respCh。
func (t *httpTransport) Send(msg []byte) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return ErrClosed
	}
	if !t.alive {
		t.mu.Unlock()
		return ErrNotConnected
	}
	// 每次 Send 重建 chan（buffer 1），保证 Resp 与最近一次 Send 一一对应
	t.respCh = make(chan httpResponse, 1)
	ch := t.respCh
	sessionID := t.sessionID
	t.mu.Unlock()

	resp, err := t.doRequest(msg, sessionID)
	if err != nil {
		// 把错误投递到 ch，让 Recv 能拿到（避免调用方 Recv 永久阻塞）
		ch <- httpResponse{err: err}
		return nil
	}
	ch <- httpResponse{data: resp}
	return nil
}

// doRequest 构造并发送 HTTP 请求，返回解析后的响应字节。
// 错误情况下返回包装错误（含 *HTTPError 用于状态码区分）。
func (t *httpTransport) doRequest(msg []byte, sessionID string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, t.url, bytes.NewReader(msg))
	if err != nil {
		return nil, fmt.Errorf("http: 构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if t.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+t.bearerToken)
	}
	if t.basicAuth != nil {
		req.SetBasicAuth(t.basicAuth.user, t.basicAuth.pass)
	}
	for k, v := range t.extraHeaders {
		req.Header.Set(k, v)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	// 绑定 context 便于 ctx 取消时中断请求
	if t.reqCtx != nil {
		req = req.WithContext(t.reqCtx)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: 请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 提取 Mcp-Session-Id（首次响应携带，后续回传）
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}

	// 状态码非 2xx 视为错误
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("http: 读响应失败: %w", err)
	}

	// 按 Content-Type 解析
	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/json"):
		return body, nil
	case strings.HasPrefix(ct, "text/event-stream"):
		return parseSSE(body), nil
	default:
		return nil, fmt.Errorf("http: 不支持的 Content-Type: %s", ct)
	}
}

// parseSSE 解析 SSE 格式的 body，提取首个 data: 字段的 JSON 内容。
// MCP Streamable HTTP 模式下，server 在 SSE 流中发送单条 data 消息代表 Response。
func parseSSE(body []byte) []byte {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if bytes.HasPrefix(line, []byte("data: ")) {
			return append([]byte(nil), line[6:]...)
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			return append([]byte(nil), line[5:]...)
		}
	}
	return nil
}

// Recv 从 respCh 读取最近一次 Send 的响应。
// 调用方必须按 Send→Recv 顺序串行调用。
//
// 关键行为：每次循环重新从 t.respCh 拿最新 ch 引用。
// 原因：HTTP transport 每次 Send 会重建 respCh（旧 ch 数据被丢弃）；
// 如果 Recv 锁住旧 ch 引用，Send 重建的新 ch 永远没人读，导致永久阻塞。
//
// 这里采用"等 ch 出现 → 短超时读 → 重新检查"模式跟随 t.respCh 变化。
func (t *httpTransport) Recv() ([]byte, error) {
	deadline := time.Now().Add(60 * time.Second)
	for {
		t.mu.Lock()
		ch := t.respCh
		closed := t.closed
		t.mu.Unlock()
		if closed {
			return nil, ErrClosed
		}
		if ch != nil {
			// 短超时读：5ms 内没数据说明这条 ch 还没被 Send 填充，
			// 重新循环拿最新 ch（可能被 Send 重建）
			select {
			case r, ok := <-ch:
				if !ok {
					return nil, io.EOF
				}
				return r.data, r.err
			case <-time.After(5 * time.Millisecond):
				// 重新检查 t.respCh
				continue
			}
		}
		// ch 还没被 Send 重建：等
		if time.Now().After(deadline) {
			return nil, ErrNotConnected
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Close 关闭 Transport。多次调用幂等。
func (t *httpTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	t.alive = false
	if t.respCh != nil {
		close(t.respCh)
		t.respCh = nil
	}
	return nil
}

// IsAlive 查询当前 Transport 状态。
func (t *httpTransport) IsAlive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.alive && !t.closed
}
