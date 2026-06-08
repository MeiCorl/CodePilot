package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MeiCorl/CodePilot/src/internal/security"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// TestWriteFileCreate 验证：传入 file_path + content 创建文件。
func TestWriteFileCreate(t *testing.T) {
	sandbox := t.TempDir()
	tool := NewWriteFileTool(sandbox)
	ctx := withSandedPath(t, sandbox, "out.txt")
	_, err := tool.Execute(ctx, json.RawMessage(`{"file_path":"out.txt","content":"hello world"}`))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(sandbox, "out.txt"))
	if err != nil {
		t.Fatalf("读取文件失败: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("内容错误: %s", data)
	}
}

// TestWriteFileOverwrite 验证：重复调用相同 file_path 覆盖原内容。
func TestWriteFileOverwrite(t *testing.T) {
	sandbox := t.TempDir()
	tool := NewWriteFileTool(sandbox)
	ctx := withSandedPath(t, sandbox, "f.txt")
	first := json.RawMessage(`{"file_path":"f.txt","content":"v1"}`)
	if _, err := tool.Execute(ctx, first); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	second := json.RawMessage(`{"file_path":"f.txt","content":"v2"}`)
	if _, err := tool.Execute(ctx, second); err != nil {
		t.Fatalf("二次写入失败: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(sandbox, "f.txt"))
	if string(data) != "v2" {
		t.Errorf("覆盖后内容错误: %s", data)
	}
}

// TestWriteFileMkdirParents 验证：父目录不存在时自动创建。
func TestWriteFileMkdirParents(t *testing.T) {
	sandbox := t.TempDir()
	tool := NewWriteFileTool(sandbox)
	ctx := withSandedPath(t, sandbox, "deep/nest/dir/file.txt")
	_, err := tool.Execute(ctx, json.RawMessage(`{"file_path":"deep/nest/dir/file.txt","content":"x"}`))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(sandbox, "deep", "nest", "dir", "file.txt"))
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(data) != "x" {
		t.Errorf("内容错误: %s", data)
	}
}

// TestWriteFile_OutsideViaMiddleware 验证 sandbox 外写入被 Middleware 拦截。
func TestWriteFile_OutsideViaMiddleware(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上无 etc 目录, 跳过")
	}
	sandbox := t.TempDir()
	mw := security.SandboxMiddleware(sandbox, nil)
	_, err := mw(context.Background(), "WriteFile",
		json.RawMessage(`{"file_path":"../../etc/evil","content":"x"}`), tool.PermWrite)
	if err == nil {
		t.Fatal("越界写入应被 Middleware 拦截")
	}
	if !errorsIs(err, security.ErrPathOutsideSandbox) {
		t.Errorf("错误应为 ErrPathOutsideSandbox, 实际: %v", err)
	}
}

// TestWriteFileEmptyPath 验证空 file_path 报错（Middleware 透传，工具自检）。
func TestWriteFileEmptyPath(t *testing.T) {
	sandbox := t.TempDir()
	tool := NewWriteFileTool(sandbox)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"file_path":"","content":"x"}`))
	if err == nil {
		t.Fatal("空路径应报错")
	}
}
