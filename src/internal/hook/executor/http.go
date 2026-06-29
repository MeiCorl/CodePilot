// Package executor — HttpExecutor 实现 (spec §D.3)。
//
// http action 让用户能在 Hook 触发点发 HTTP 请求到外部服务(Slack / 钉钉 /
// Webhook / 自家 CI),用于「Agent 改了某文件就通知频道」「工具调用审计日志
// 外推」等场景。
//
// 设计要点:
//   - 仅允许 http / https scheme(spec §D.3),其它 scheme(file / ftp / ...)直接
//     拒绝,防止用户配置被误导触发本地文件读取等意外行为;
//   - body 模板支持 $VAR 替换,与 command/prompt/agent 共用同一套 Interpolate;
//   - 非 2xx 响应转为 HttpError,Body 截断保留前 512 字节(Slack 等 webhook 服务
//     通常在 4xx body 中给出具体错误码,排错必备);
//   - response body 不返回给 LLM(spec §D.3),只走日志;
//   - 复用 http.Client.Timeout 作为整体超时(包含连接 + 写 + 读)。
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/hookcontext"
)

// HttpConfig 是 http action 的 type-specific 配置,对应 setting.json:
//   {
//     "type": "http",
//     "method": "POST",                       // 默认 POST
//     "url": "https://hooks.slack.com/...",
//     "headers": {"Content-Type": "application/json"},
//     "body": "{\"text\": \"...\"}",          // 支持 $VAR 插值
//     "timeout": "5s"                         // 默认 30s
//   }
type HttpConfig struct {
	// Method 为 HTTP 动词,默认 POST(spec §D.3 示例用 POST;GET 不带 body)。
	Method string `json:"method,omitempty"`
	// URL 为请求目标,必须 http/https scheme。
	URL string `json:"url"`
	// Headers 为附加请求头(可覆盖默认 Content-Type 等)。
	Headers map[string]string `json:"headers,omitempty"`
	// Body 为请求体模板,支持 $VAR 替换;GET 请求的 body 在 net/http 层会被忽略。
	Body string `json:"body,omitempty"`
	// Timeout 字符串(默认 30s)。
	Timeout string `json:"timeout,omitempty"`
}

// HttpExecutor 是 http action 的执行器实现。
//
// 不可变,Engine 在 LoadEntries 阶段一次性实例化,运行期只调用 Execute。
// 不持有可变状态所以天然并发安全。
type HttpExecutor struct {
	cfg     HttpConfig
	method  string
	timeout time.Duration
	client  *http.Client
}

// NewHttpExecutor 解析 raw action JSON 并构造 HttpExecutor。
//
// 校验:
//   - JSON 解析失败 → error;
//   - url 字段缺失 / 非 http/https scheme → error(避免后续 Execute 走危险默认)。
func NewHttpExecutor(raw json.RawMessage) (*HttpExecutor, error) {
	var cfg HttpConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("hook http action: parse: %w", err)
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("hook http action: url field required")
	}
	if err := assertHTTPScheme(cfg.URL); err != nil {
		return nil, fmt.Errorf("hook http action: %w", err)
	}
	method := strings.ToUpper(strings.TrimSpace(cfg.Method))
	if method == "" {
		method = http.MethodPost
	}

	timeout, err := ParseDuration(cfg.Timeout, DefaultHTTPTimeout)
	if err != nil {
		return nil, fmt.Errorf("hook http action: timeout: %w", err)
	}
	if timeout <= 0 {
		timeout = DefaultHTTPTimeout
	}

	return &HttpExecutor{
		cfg:     cfg,
		method:  method,
		timeout: timeout,
		client: &http.Client{
			Timeout: timeout,
			// [Why 不自定义 Transport] 用 DefaultTransport 即可,无需
			// 自定义连接池/MaxIdleConns 之类——单条 hook 调用频率低,
			// 偶尔一次 HTTP 不值得持有长连接。
		},
	}, nil
}

// Type 返回 "http"。
func (e *HttpExecutor) Type() string { return ActionTypeHTTP }

// Execute 发送单次 HTTP 请求并按 spec §D.3 处理响应。
//
// 流程:
//  1. body 模板变量替换;
//  2. 构造 http.Request;
//  3. 应用用户自定义 headers(Content-Type 默认 application/json);
//  4. 设置超时通过 http.Client.Timeout(spec §D.3「timeout 用 http.Client.Timeout」);
//  5. 发送请求;
//  6. 按响应状态码处理:
//     - 2xx → 读 body 但不返回(spec §D.3「response body 不返回给 LLM」);
//     - 非 2xx → 返回 *HttpError 含状态码 + body 截断片段。
func (e *HttpExecutor) Execute(ctx context.Context, hookCtx *hookcontext.HookContext, vars map[string]string) error {
	body := hookcontext.Interpolate(e.cfg.Body, vars)

	var bodyReader io.Reader
	if body != "" {
		bodyReader = bytes.NewReader([]byte(body))
	}

	req, err := http.NewRequestWithContext(ctx, e.method, e.cfg.URL, bodyReader)
	if err != nil {
		return fmt.Errorf("hook http: build request: %w", err)
	}

	// 默认 Content-Type:POST/PUT 等带 body 的请求自动设 JSON。
	// 用户可在 Headers 中显式覆盖。
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range e.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		// 网络层错误(连接失败 / DNS / 超时)直接透传,Engine 走 warn。
		return fmt.Errorf("hook http: do request: %w", err)
	}
	defer resp.Body.Close()

	// 一次性 read 上限避免恶意/异常的巨 body 撑爆内存;
	// 截断而非拒绝,可读到的前 N 字节足够排错。
	maxRead := int64(httpBodySnippetLimit) * 4 // 给截断前多一点冗余
	lr := io.LimitReader(resp.Body, maxRead)
	respBody, _ := io.ReadAll(lr)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HttpError{
			StatusCode: resp.StatusCode,
			Body:       truncate(string(respBody), httpBodySnippetLimit),
			Method:     e.method,
			URL:        e.cfg.URL,
		}
	}
	return nil
}

// assertHTTPScheme 校验 URL 的 scheme 必须为 http 或 https。
//
// [Why 用 url.Parse 而非 strings.HasPrefix] url.Parse 能识别
// `HTTPS://...` 大小写变体,以及更细微的格式错误(如 `http:/foo` 缺斜杠);
// HasPrefix 会被 `httpfoo.com` 误判通过。
func assertHTTPScheme(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url %q: %w", rawURL, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return &ErrInvalidURLScheme{URL: rawURL}
	}
	return nil
}
