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
	if runtime.GOOS == "windows" {
		t.Skip("Bash 工具不支持 Windows, 跳过")
	}
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
	if runtime.GOOS == "windows" {
		t.Skip("Bash 工具不支持 Windows, 跳过")
	}
	tool := NewBashTool(5 * time.Second)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"ls /nonexistent_path_for_test 2>&1"}`))
	if err != nil {
		// 失败时我们的实现仍返回文本而非 error（让 LLM 看到 stderr）
		t.Logf("注意：实现选择返回文本而非 error, err=%v", err)
	}
	if !strings.Contains(out, "No such file") && !strings.Contains(out, "No such") {
		t.Errorf("stderr 应出现在输出中: %s", out)
	}
}

// TestBashDangerous 验证：危险命令在执行前被拦截。
func TestBashDangerous(t *testing.T) {
	tool := NewBashTool(5 * time.Second)
	cases := []string{
		`rm -rf /`,
		`mkfs.ext4 /dev/sda`,
		`shutdown -h now`,
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"`+cmd+`"}`))
			if err == nil {
				t.Fatalf("危险命令应被拦截: %q", cmd)
			}
			if !strings.Contains(err.Error(), "危险命令") {
				t.Errorf("错误应说明危险命令拦截: %v", err)
			}
		})
	}
}

// TestBashTimeout 验证：超过 timeout 时返回超时错误。
func TestBashTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash 工具不支持 Windows, 跳过")
	}
	tool := NewBashTool(1 * time.Second)
	// sleep 5 必然超时
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"sleep 5"}`))
	if err == nil {
		t.Fatal("应返回超时错误")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Errorf("错误应说明超时: %v", err)
	}
}

// TestBashContextCancel 验证：ctx 取消时工具及时返回。
func TestBashContextCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash 工具不支持 Windows, 跳过")
	}
	tool := NewBashTool(30 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	// 50ms 后取消
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := tool.Execute(ctx, json.RawMessage(`{"command":"sleep 30"}`))
	if err == nil {
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
