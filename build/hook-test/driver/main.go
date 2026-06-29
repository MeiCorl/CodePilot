// CodePilot hook e2e driver：
// 1. 用 cmd /c 启动 binary,把它输出重定向到文件
// 2. tail 文件抓端口
// 3. ws 连接 → 发送 user_input → 收集 stream 流
// 4. 处理 permission_ask (auto allow) / tool_call_start / tool_call_end
// 5. stream_done 后优雅退出 (关 ws,等 binary 自动退出)
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

type StreamChunkPayload struct {
	Delta string `json:"delta"`
}
type StreamDonePayload struct {
	Reason string `json:"reason"`
}
type StreamErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type ToolCallStartPayload struct {
	ToolUseID  string          `json:"tool_use_id"`
	Name       string          `json:"name"`
	Input      json.RawMessage `json:"input"`
	Server     string          `json:"server,omitempty"`
}
type ToolCallEndPayload struct {
	ToolUseID string `json:"tool_use_id"`
	Output    string `json:"output"`
	IsError   bool   `json:"is_error"`
}
type PermissionAskPayload struct {
	ToolUseID string `json:"tool_use_id"`
	Tool      string `json:"tool"`
	Pattern   string `json:"pattern"`
	Reason    string `json:"reason"`
}
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// 匹配 stdout 上 "CodePilot 已启动，访问地址：http://127.0.0.1:xxxxx" 这一行
var addrRegex = regexp.MustCompile(`127\.0\.0\.1:(\d+)`)

func waitForPort(path string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastSize int64 = 0
	for time.Now().Before(deadline) {
		f, err := os.Open(path)
		if err == nil {
			st, _ := f.Stat()
			if st != nil && st.Size() > lastSize {
				_, _ = f.Seek(lastSize, 0)
				scanner := bufio.NewScanner(f)
				for scanner.Scan() {
					if m := addrRegex.FindStringSubmatch(scanner.Text()); m != nil {
						_ = f.Close()
						return "127.0.0.1:" + m[1], nil
					}
				}
				lastSize = st.Size()
			}
			_ = f.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return "", fmt.Errorf("等待端口超时")
}

func main() {
	var (
		exePath = flag.String("exe", `F:\CodePilot\build\dist\CodePilot.exe`, "CodePilot 二进制路径(Win32 backslash path)")
		workdir = flag.String("cwd", `C:\tmp\hook-test`, "启动工作目录")
		prompt  = flag.String("prompt", "请用 WriteFile 工具创建文件 C:\\tmp\\hook-test\\demo.txt,内容为 hello from CodePilot hook test", "用户输入")
		timeout = flag.Duration("timeout", 90*time.Second, "整体超时")
	)
	flag.Parse()

	workWin := filepath.FromSlash(strings.ReplaceAll(*workdir, "/", string(filepath.Separator)))
	stdoutPath := filepath.Join(workWin, "binary-stdout.log")
	_ = os.Remove(stdoutPath)

	// 直接 exec binary,通过 SysProcAttr.CmdLine 强制传完整命令行给 CreateProcessW,
	// 绕过 Go 默认 forkExec 时 argv quoting 的问题
	f, err := os.Create(stdoutPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建 stdout 文件失败: %v\n", err)
		os.Exit(2)
	}
	exeWin := *exePath
	cmd := exec.Cmd{
		Path:        exeWin,
		Args:        []string{exeWin},
		Dir:         workWin,
		Stdout:      f,
		Stderr:      f,
		SysProcAttr: &syscall.SysProcAttr{CmdLine: fmt.Sprintf(`"%s"`, exeWin)},
	}
	if err := cmd.Start(); err != nil {
		_ = f.Close()
		fmt.Fprintf(os.Stderr, "启动 binary 失败: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf(">> CodePilot 已启动, pid=%d, stdout -> %s\n", cmd.Process.Pid, stdoutPath)

	// 等端口(从 stdout 文件抓)
	addr, err := waitForPort(stdoutPath, 30*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		_ = cmd.Process.Kill()
		_ = f.Close()
		os.Exit(3)
	}
	fmt.Printf(">> CodePilot 监听: %s\n", addr)

	// WS 连接(CodePilot 注册在 /ws)
	u := url.URL{Scheme: "ws", Host: addr, Path: "/ws"}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WS Dial: %v\n", err)
		_ = cmd.Process.Kill()
		_ = f.Close()
		os.Exit(3)
	}
	defer conn.Close()
	fmt.Printf(">> WS 已连\n")

	// reader goroutine
	var (
		mu          sync.Mutex
		events      []string
		text        strings.Builder
		gotDone     bool
		doneReason  string
		gotErr      string
		toolStartCh = make(chan ToolCallStartPayload, 32)
		toolEndCh   = make(chan ToolCallEndPayload, 32)
		permCh      = make(chan PermissionAskPayload, 32)
	)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				fmt.Fprintf(os.Stderr, "WS read err: %v\n", err)
				return
			}
			var m Message
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			mu.Lock()
			events = append(events, m.Type)
			mu.Unlock()
			switch m.Type {
			case "stream_chunk":
				var p StreamChunkPayload
				_ = json.Unmarshal(m.Payload, &p)
				text.WriteString(p.Delta)
			case "stream_done":
				var p StreamDonePayload
				_ = json.Unmarshal(m.Payload, &p)
				doneReason = p.Reason
				gotDone = true
			case "stream_error":
				var p StreamErrorPayload
				_ = json.Unmarshal(m.Payload, &p)
				gotErr = fmt.Sprintf("%s: %s", p.Code, p.Message)
			case "tool_call_start":
				var p ToolCallStartPayload
				_ = json.Unmarshal(m.Payload, &p)
				select {
				case toolStartCh <- p:
				default:
				}
			case "tool_call_end":
				var p ToolCallEndPayload
				_ = json.Unmarshal(m.Payload, &p)
				select {
				case toolEndCh <- p:
				default:
				}
			case "permission_ask":
				var p PermissionAskPayload
				_ = json.Unmarshal(m.Payload, &p)
				select {
				case permCh <- p:
				default:
				}
			}
		}
	}()

	// 发 user_input
	in := map[string]any{"type": "user_input", "payload": map[string]string{"text": *prompt}}
	inB, _ := json.Marshal(in)
	if err := conn.WriteMessage(websocket.TextMessage, inB); err != nil {
		fmt.Fprintf(os.Stderr, "WS write: %v\n", err)
		os.Exit(3)
	}
	fmt.Println(">> user_input 已发送,等待 stream_done...")

	absDeadline := time.Now().Add(*timeout)
	for time.Now().Before(absDeadline) {
		select {
		case tcs := <-toolStartCh:
			in := string(tcs.Input)
			if len(in) > 200 {
				in = in[:200] + "..."
			}
			fmt.Printf("[tool_call_start] %s input=%s\n", tcs.Name, in)
		case tce := <-toolEndCh:
			out := tce.Output
			if len(out) > 200 {
				out = out[:200] + "..."
			}
			fmt.Printf("[tool_call_end] id=%s err=%v out=%s\n", tce.ToolUseID, tce.IsError, out)
		case perm := <-permCh:
			fmt.Printf("[permission_ask] tool=%s pattern=%s -> auto allow\n", perm.Tool, perm.Pattern)
			resp := map[string]any{
				"type":    "permission_response",
				"payload": map[string]any{"tool_use_id": perm.ToolUseID, "decision": "allow"},
			}
			rb, _ := json.Marshal(resp)
			_ = conn.WriteMessage(websocket.TextMessage, rb)
		default:
			mu.Lock()
			done := gotDone || gotErr != ""
			mu.Unlock()
			if done {
				goto FINISH
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
FINISH:
	mu.Lock()
	defer mu.Unlock()
	fmt.Printf(">> events count: %d\n", len(events))
	fmt.Printf(">> text length: %d\n", text.Len())
	if text.Len() > 0 {
		preview := text.String()
		if len(preview) > 400 {
			preview = preview[:400] + "..."
		}
		fmt.Printf(">> text preview: %s\n", preview)
	}
	fmt.Printf(">> done reason: %s\n", doneReason)
	fmt.Printf(">> error: %s\n", gotErr)

	evidencePath := filepath.Join(workWin, "evidence.json")
	evidence := map[string]any{
		"events":     events,
		"text":       text.String(),
		"doneReason": doneReason,
		"error":      gotErr,
	}
	eb, _ := json.MarshalIndent(evidence, "", "  ")
	_ = os.WriteFile(evidencePath, eb, 0644)
	fmt.Printf(">> evidence -> %s\n", evidencePath)

	if gotErr != "" {
		os.Exit(4)
	}
	if !gotDone {
		os.Exit(5)
	}

	// 等异步 hook 收尾(给 agent hook 足够时间调 LLM + 写日志)
	time.Sleep(15 * time.Second)
	_ = conn.Close()
	fmt.Println(">> ws 已关,等待 binary 自动退出...")

	// 等 binary 退出(浏览器关闭 → 5s 宽限期)
	// 因为 binary 实际是被 cmd /c 启动后 detach 了,我们直接等端口消失即可
	portDeadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(portDeadline) {
		c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
		if err != nil {
			fmt.Println(">> CodePilot 已退出")
			return
		}
		_ = c.Close()
		time.Sleep(1 * time.Second)
	}
	fmt.Println(">> 超时未退出,继续")
}