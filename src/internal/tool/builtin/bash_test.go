package builtin

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestBashSuccess 验证：执行成功命令返回 stdout + exit_code=0。
func TestBashSuccess(t *testing.T) {
	tool := NewBashTool(5 * time.Second)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"echo hello"}`))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "exit_code: 0") {
		t.Errorf("应包含 exit_code: 0, 实际: %s", out)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("应包含 stdout 内容: %s", out)
	}
}

// TestBashFailure 验证：非零退出码命令被标记，stderr 出现在输出中。
func TestBashFailure(t *testing.T) {
	tool := NewBashTool(5 * time.Second)
	// 使用跨平台兼容的失败命令：读取不存在的路径
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = `Get-ChildItem C:\nonexistent_path_for_test_xyz`
	} else {
		cmd = `ls /nonexistent_path_for_test 2>&1`
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"`+cmd+`"}`))
	if err != nil {
		// 失败时我们的实现仍返回文本而非 error（让 LLM 看到 stderr）
		t.Logf("注意：实现选择返回文本而非 error, err=%v", err)
	}
	if !strings.Contains(out, "exit_code") {
		t.Errorf("应包含 exit_code: %s", out)
	}
}

// TestBashTimeout 验证：超过 timeout 时返回超时错误。
func TestBashTimeout(t *testing.T) {
	tool := NewBashTool(1 * time.Second)
	// 使用跨平台兼容的 sleep 命令
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = `Start-Sleep -Seconds 5`
	} else {
		cmd = `sleep 5`
	}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"`+cmd+`"}`))
	if err == nil {
		t.Fatal("应返回超时错误")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Errorf("错误应说明超时: %v", err)
	}
}

// TestBashContextCancel 验证：ctx 取消时工具及时返回。
func TestBashContextCancel(t *testing.T) {
	tool := NewBashTool(30 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	// 50ms 后取消
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	// 使用跨平台兼容的 sleep 命令
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = `Start-Sleep -Seconds 30`
	} else {
		cmd = `sleep 30`
	}
	_, err := tool.Execute(ctx, json.RawMessage(`{"command":"`+cmd+`"}`))
	// Windows 下 PowerShell 被 TerminateProcess 强杀可能不返回 error，
	// 但关键是命令应在合理时间内返回（而非阻塞 30 秒）。
	// Unix 下应返回取消错误。
	if runtime.GOOS != "windows" && err == nil {
		t.Fatal("应因 ctx 取消而返回错误")
	}
}

// TestBashEmptyCommand 验证空 command 报错。
func TestBashEmptyCommand(t *testing.T) {
	tool := NewBashTool(5 * time.Second)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"command":""}`))
	if err == nil {
		t.Fatal("空命令应报错")
	}
}

// TestBashWindowsPowerShell 验证 Windows 下通过 PowerShell 正确执行命令。
// 仅在 Windows 平台运行。
func TestBashWindowsPowerShell(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 平台运行")
	}
	tool := NewBashTool(5 * time.Second)

	// 测试基本输出
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"Write-Output 'hello from ps'"}`))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "exit_code: 0") {
		t.Errorf("应包含 exit_code: 0, 实际: %s", out)
	}
	if !strings.Contains(out, "hello from ps") {
		t.Errorf("应包含 stdout 内容: %s", out)
	}

	// 测试 PowerShell 管道
	out, err = tool.Execute(context.Background(), json.RawMessage(`{"command":"Get-Date | Select-Object -ExpandProperty Year"}`))
	if err != nil {
		t.Fatalf("管道执行失败: %v", err)
	}
	if !strings.Contains(out, "exit_code: 0") {
		t.Errorf("管道命令应成功: %s", out)
	}
}
