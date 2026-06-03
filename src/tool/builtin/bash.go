package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/MeiCorl/CodePilot/src/tool"
	"github.com/MeiCorl/CodePilot/src/tool/safety"
)

// BashName 是 Bash 工具的 snake_case 唯一标识。
const BashName = "bash"

const (
	bashDefaultTimeoutSec = 30
	bashMaxOutputBytes    = 1024 * 1024 // 1MB，单边 stdout/stderr 上限
)

// bashInput 是 Bash 工具的入参结构。
type bashInput struct {
	Command string `json:"command" jsonschema:"required,description=要执行的 shell 命令字符串"`
	Timeout int    `json:"timeout" jsonschema:"description=单次执行超时秒数（覆盖全局默认 30s），0 表示使用默认"`
}

var _ = bashInput{} // 见 schema.go

// BashTool 是 Bash 工具的实现。
type BashTool struct {
	tool.BaseTool
	// DefaultTimeout 是未指定 timeout 时的默认超时。
	DefaultTimeout time.Duration
}

// NewBashTool 构造 Bash 工具实例。
func NewBashTool(defaultTimeout time.Duration) *BashTool {
	if defaultTimeout <= 0 {
		defaultTimeout = bashDefaultTimeoutSec * time.Second
	}
	return &BashTool{
		BaseTool: tool.BaseTool{
			ToolName:        BashName,
			ToolDescription: "在宿主 shell 中执行一条命令，捕获 stdout/stderr/exit code。支持管道、重定向、复合命令。带超时控制（默认 30s）。危险命令（rm -rf /、mkfs、shutdown 等）会被黑名单拦截，不会执行。",
			ToolInputSchema: bashSchema,
			ToolPermission:  tool.PermExec,
		},
		DefaultTimeout: defaultTimeout,
	}
}

// Execute 实现 tool.Tool.Execute。
func (t *BashTool) Execute(parent context.Context, input json.RawMessage) (string, error) {
	var in bashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return "", errors.New("command 不能为空")
	}

	// 危险命令黑名单
	if err := safety.CheckBashCommand(in.Command); err != nil {
		return "", err
	}

	// Windows 平台暂不支持 sh
	if runtime.GOOS == "windows" {
		return "", errors.New("Bash 工具在 Windows 平台暂不支持，请使用 PowerShell 或 WSL 替代")
	}

	// 超时控制
	timeout := t.DefaultTimeout
	if in.Timeout > 0 {
		timeout = time.Duration(in.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	// 通过 sh -c 执行，支持管道/重定向/复合命令
	cmd := exec.CommandContext(ctx, "sh", "-c", in.Command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, n: bashMaxOutputBytes}
	cmd.Stderr = &limitedWriter{w: &stderr, n: bashMaxOutputBytes}

	err := cmd.Run()
	// 区分超时与其他错误
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("命令执行超时（> %s）", timeout)
	}
	if err != nil {
		// 退出码非零，但命令确实跑过——把 stdout/stderr 一起返回，标记为错误
		out := stdout.String()
		errOut := stderr.String()
		if errOut != "" {
			return formatBashOutput(out, errOut, -1) + "\n" + err.Error(), nil
		}
		// exec.ExitError 时提取 exit code
		if ee, ok := err.(*exec.ExitError); ok {
			return formatBashOutput(out, errOut, ee.ExitCode()), nil
		}
		return "", fmt.Errorf("执行命令失败: %w", err)
	}

	return formatBashOutput(stdout.String(), stderr.String(), 0), nil
}

// formatBashOutput 把 stdout/stderr/exit code 拼成 LLM 友好的文本。
func formatBashOutput(stdout, stderr string, exitCode int) string {
	var b strings.Builder
	b.WriteString("exit_code: ")
	fmt.Fprintf(&b, "%d\n", exitCode)
	if stdout != "" {
		b.WriteString("--- stdout ---\n")
		b.WriteString(stdout)
		if !strings.HasSuffix(stdout, "\n") {
			b.WriteString("\n")
		}
	}
	if stderr != "" {
		b.WriteString("--- stderr ---\n")
		b.WriteString(stderr)
		if !strings.HasSuffix(stderr, "\n") {
			b.WriteString("\n")
		}
	}
	if stdout == "" && stderr == "" {
		b.WriteString("(无输出)\n")
	}
	return b.String()
}

// limitedWriter 写入超过 n 字节后停止继续写入并丢弃。
// 防止单边输出把内存撑爆。
type limitedWriter struct {
	w       *bytes.Buffer
	n       int
	written int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.written >= l.n {
		// 超过上限：假装写入成功但不真正落盘
		return len(p), nil
	}
	remaining := l.n - l.written
	if len(p) <= remaining {
		l.written += len(p)
		return l.w.Write(p)
	}
	l.written = l.n
	written, err := l.w.Write(p[:remaining])
	// 假装完整写入以避免 os/exec 误判
	if err == nil {
		written = len(p)
	}
	return written, err
}
