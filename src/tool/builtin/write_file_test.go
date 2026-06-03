package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteFileCreate 验证：传入 file_path + content 创建文件。
func TestWriteFileCreate(t *testing.T) {
	sandbox := t.TempDir()
	tool := NewWriteFileTool(sandbox)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"file_path":"out.txt","content":"hello world"}`))
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
	first := json.RawMessage(`{"file_path":"f.txt","content":"v1"}`)
	if _, err := tool.Execute(context.Background(), first); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	second := json.RawMessage(`{"file_path":"f.txt","content":"v2"}`)
	if _, err := tool.Execute(context.Background(), second); err != nil {
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
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"file_path":"deep/nest/dir/file.txt","content":"x"}`))
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

// TestWriteFilePathOutside 验证 sandbox 外写入被拦截。
func TestWriteFilePathOutside(t *testing.T) {
	sandbox := t.TempDir()
	tool := NewWriteFileTool(sandbox)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"file_path":"../../etc/evil","content":"x"}`))
	if err == nil {
		t.Fatal("越界写入应被拦截")
	}
	if !strings.Contains(err.Error(), "沙箱") {
		t.Errorf("错误应提示沙箱拦截: %v", err)
	}
}

// TestWriteFileEmptyPath 验证空 file_path 报错。
func TestWriteFileEmptyPath(t *testing.T) {
	sandbox := t.TempDir()
	tool := NewWriteFileTool(sandbox)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"file_path":"","content":"x"}`))
	if err == nil {
		t.Fatal("空路径应报错")
	}
}
