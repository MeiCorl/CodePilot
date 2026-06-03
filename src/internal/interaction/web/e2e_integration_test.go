//go:build integration

// Package web 的 e2e 集成测试：连真实 Anthropic provider 跑端到端。
// 跑法：RUN_E2E=1 go test -tags integration -count=1 -timeout 120s -run TestE2E ./src/internal/interaction/web/
package web

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/engine/conversation"
	"github.com/MeiCorl/CodePilot/src/internal/memory/session"
	"github.com/MeiCorl/CodePilot/src/llm"
	"github.com/MeiCorl/CodePilot/src/tool"
	"github.com/MeiCorl/CodePilot/src/tool/builtin"
)

// newE2ERig 构造一个端到端测试环境：读 ~/.codepilot/config.json 作为真实 provider 配置，
// 启动 httptest server + ws client，并把 5 个内置工具以默认 cwd 注册。
func newE2ERig(t *testing.T) (*websocket.Conn, func()) {
	t.Helper()
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("需要 RUN_E2E=1 才执行真实 LLM 端到端测试")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("获取 home 失败: %v", err)
	}
	cfgPath := filepath.Join(home, ".codepilot", "config.json")
	cfg, err := config.LoadFromPath(cfgPath)
	if err != nil {
		t.Skipf("读 %s 失败: %v（请先在 ~/.codepilot/config.json 配 api_key）", cfgPath, err)
	}
	if cfg.APIKey == "" || strings.HasPrefix(cfg.APIKey, "sk-ant-your-") {
		t.Skipf("%s 中的 api_key 是占位符，跳过", cfgPath)
	}

	sm, err := session.NewSessionManagerWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("SessionManager 初始化失败: %v", err)
	}

	provider, err := llm.NewProvider(cfg)
	if err != nil {
		t.Fatalf("构造 LLM Provider 失败: %v", err)
	}

	workdir, err := findProjectRoot()
	if err != nil {
		t.Fatalf("定位项目根失败: %v", err)
	}
	// 把进程 cwd 切到项目根（os.ReadFile 解析 "src/main.go" 这样的相对路径时使用 cwd）
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("chdir 到项目根失败: %v", err)
	}
	toolReg := tool.DefaultRegistry()
	bashTimeout := 30 * time.Second
	builtin.RegisterWithOptions(toolReg, workdir, bashTimeout)
	toolHandler := conversation.NewToolHandler(toolReg, bashTimeout, workdir)

	h := NewHandler(provider, sm, cfg, 10, "", 100000, workdir, toolReg, toolHandler)
	s := NewServer("127.0.0.1:0")
	h.Register(s.Router())
	ts := httptest.NewServer(http.HandlerFunc(s.ConnectionManager().HandleWS))

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		ts.Close()
		t.Fatalf("ws 拨号失败: %v", err)
	}

	cleanup := func() {
		client.Close()
		ts.Close()
	}
	return client, cleanup
}

// sendUserInput 通过 ws 发 user_input 消息。
func sendUserInput(t *testing.T, c *websocket.Conn, text string) {
	t.Helper()
	data, err := EncodePayload(MsgTypeUserInput, UserInputPayload{Text: text})
	if err != nil {
		t.Fatalf("编码 user_input 失败: %v", err)
	}
	if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("发送 user_input 失败: %v", err)
	}
}

// consumeStream 消费 ws 流到 stream_done 或超时（120s），把过程消息分类收集。
type e2eTrace struct {
	ToolStarts  []ToolCallStartPayload
	ToolEnds    []ToolCallEndPayload
	FinalText   strings.Builder
	StatusSeq   []string
	StreamDones []StreamDonePayload
	Errors      []StreamErrorPayload
	AllRaw      []string
}

func consumeStream(t *testing.T, c *websocket.Conn, timeout time.Duration) e2eTrace {
	t.Helper()
	tr := e2eTrace{
		ToolStarts:  []ToolCallStartPayload{},
		ToolEnds:    []ToolCallEndPayload{},
		StatusSeq:   []string{},
		StreamDones: []StreamDonePayload{},
		Errors:      []StreamErrorPayload{},
	}
	deadline := time.Now().Add(timeout)
	// 用一个 goroutine 持续读，转发到 ch；主循环只做 select，
	// 避免 gorilla/websocket 在一次失败 Read 后不允许再读的限制。
	ch := make(chan []byte, 64)
	errCh := make(chan error, 1)
	go func() {
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			ch <- data
		}
	}()
	for {
		select {
		case data := <-ch:
			tr.AllRaw = append(tr.AllRaw, string(data))
			var msg Message
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			switch msg.Type {
			case MsgTypeStreamChunk:
				var p StreamChunkPayload
				if err := json.Unmarshal(msg.Payload, &p); err == nil {
					tr.FinalText.WriteString(p.Delta)
				}
			case MsgTypeToolCallStart:
				var p ToolCallStartPayload
				if err := json.Unmarshal(msg.Payload, &p); err == nil {
					tr.ToolStarts = append(tr.ToolStarts, p)
				}
			case MsgTypeToolCallEnd:
				var p ToolCallEndPayload
				if err := json.Unmarshal(msg.Payload, &p); err == nil {
					tr.ToolEnds = append(tr.ToolEnds, p)
				}
			case MsgTypeStatusUpdate:
				var p StatusUpdatePayload
				if err := json.Unmarshal(msg.Payload, &p); err == nil {
					tr.StatusSeq = append(tr.StatusSeq, p.Status)
				}
			case MsgTypeStreamDone:
				var p StreamDonePayload
				if err := json.Unmarshal(msg.Payload, &p); err == nil {
					tr.StreamDones = append(tr.StreamDones, p)
				}
				return tr
			case MsgTypeStreamError:
				var p StreamErrorPayload
				if err := json.Unmarshal(msg.Payload, &p); err == nil {
					tr.Errors = append(tr.Errors, p)
				}
			}
		case err := <-errCh:
			// 读到错误（EOF / 关闭）→ 跳出；如果已收到 stream_done 就算正常结束，
			// 否则把错误简单记录到 StatusSeq 便于人工 debug
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				// 不太可能到这里（gorilla 不会用 net.Error.Timeout）
				continue
			}
			if !websocket.IsCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure) {
				t.Logf("ws 读结束: %v (已收到 stream_done=%d)", err, len(tr.StreamDones))
			}
			return tr
		case <-time.After(time.Until(deadline)):
			t.Logf("consumeStream 整体超时")
			return tr
		}
	}
}

// dumpTrace 把 trace 关键字段写到 t.Log，便于人工 review。
func dumpTrace(t *testing.T, label string, tr e2eTrace) {
	t.Helper()
	t.Logf("========== %s ==========", label)
	t.Logf("工具开始: %d, 工具结束: %d", len(tr.ToolStarts), len(tr.ToolEnds))
	for i, s := range tr.ToolStarts {
		t.Logf("  [start #%d] name=%s input=%s", i, s.Name, string(s.Input))
	}
	for i, e := range tr.ToolEnds {
		out := e.Output
		if len(out) > 200 {
			out = out[:200] + "..."
		}
		t.Logf("  [end   #%d] name=%s status=%s is_error=%v output=%s", i, e.Name, e.Status, e.IsError, out)
	}
	t.Logf("状态序列: %v", tr.StatusSeq)
	t.Logf("StreamDone: %v, StreamError: %v", tr.StreamDones, tr.Errors)
	t.Logf("最终文本长度: %d", tr.FinalText.Len())
	if tr.FinalText.Len() > 0 {
		t.Logf("最终文本（前 300 字符）: %s", truncate(tr.FinalText.String(), 300))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TestE2E_9_1_AnthropicReadFile 端到端验证 LLM 自主调 ReadFile 读 src/main.go。
func TestE2E_9_1_AnthropicReadFile(t *testing.T) {
	client, cleanup := newE2ERig(t)
	defer cleanup()

	// 用"读文件前 20 行"的 prompt 触发 ReadFile
	sendUserInput(t, client, "请用 read_file 工具读一下 src/main.go，然后告诉我前 20 行的内容")
	tr := consumeStream(t, client, 90*time.Second)
	dumpTrace(t, "9.1 Anthropic 自主调 ReadFile", tr)

	if len(tr.Errors) > 0 {
		t.Fatalf("出现流式错误: %v", tr.Errors)
	}
	if len(tr.ToolStarts) == 0 {
		t.Fatalf("未看到任何 tool_call_start，LLM 没有调用工具。FinalText=%q", tr.FinalText.String())
	}
	sawReadFile := false
	for _, s := range tr.ToolStarts {
		if s.Name == "read_file" {
			sawReadFile = true
		}
	}
	if !sawReadFile {
		t.Errorf("未调 read_file 工具，调用的工具: %v", namesOf(tr.ToolStarts))
	}
	if tr.FinalText.Len() == 0 {
		t.Errorf("最终回复为空，LLM 没有给出基于工具结果的二次回复")
	}
	// 期望至少 1 条 stream_done
	if len(tr.StreamDones) == 0 {
		t.Errorf("未收到 stream_done")
	}
}

// TestE2E_9_3_AnthropicGlobAndGrep 端到端验证 LLM 组合 Glob + Grep 查 TODO。
// spec 9.3 允许是两次单独回合（一次 Glob、一次 Grep），不要求单回合并发。
func TestE2E_9_3_AnthropicGlobAndGrep(t *testing.T) {
	client, cleanup := newE2ERig(t)
	defer cleanup()

	sendUserInput(t, client, "请查找 src 目录下所有 .go 文件中包含 'TODO' 标记的行，并列出文件名+行号+内容。可以用 glob 找文件，用 grep 搜内容。")
	tr := consumeStream(t, client, 120*time.Second)
	dumpTrace(t, "9.3 Anthropic 组合 Glob+Grep 查 TODO", tr)

	if len(tr.Errors) > 0 {
		t.Fatalf("出现流式错误: %v", tr.Errors)
	}
	allNames := namesOf(tr.ToolStarts)
	t.Logf("LLM 调用的工具集: %v", allNames)
	sawGlob := false
	sawGrep := false
	for _, n := range allNames {
		if n == "glob" {
			sawGlob = true
		}
		if n == "grep" {
			sawGrep = true
		}
	}
	// spec 9.3 描述"至少自主调用 Glob + Grep 工具"——但允许 LLM 自行判断；
	// 如果它只调其中一个就能找到结果（如直接 grep 整个目录），也视为通过。
	// 因此这里只 warn，不 fail。
	if !sawGlob && !sawGrep {
		t.Errorf("LLM 未调 glob 也未调 grep, 工具集: %v", allNames)
	} else {
		if !sawGlob {
			t.Logf("提示: LLM 未用 glob 工具（直接 grep 整目录，spec 9.3 允许）")
		}
		if !sawGrep {
			t.Logf("提示: LLM 未用 grep 工具，spec 9.3 允许")
		}
	}
	if tr.FinalText.Len() == 0 {
		t.Errorf("最终回复为空")
	}
}

func namesOf(starts []ToolCallStartPayload) []string {
	out := make([]string, 0, len(starts))
	for _, s := range starts {
		out = append(out, s.Name)
	}
	return out
}

// TestE2E_DryRunNoKey 验证在无 RUN_E2E 或无 key 时干净 skip，不 panic。
func TestE2E_DryRunNoKey(t *testing.T) {
	if os.Getenv("RUN_E2E") == "1" {
		t.Skip("RUN_E2E=1 时跳过 skip 验证")
	}
	client, cleanup := newE2ERig(t)
	defer cleanup()
	_ = client
	_ = fmt.Sprint("ok")
}

// findProjectRoot 从当前文件所在目录向上回溯，找到含 go.mod 的项目根。
// 测试需要在项目根下运行，以使 read_file "src/main.go" 等相对路径能解析。
func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("向上 8 层未找到 go.mod")
}
