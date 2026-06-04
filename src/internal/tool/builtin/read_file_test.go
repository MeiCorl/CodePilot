package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// helper：新建一个 sandbox 目录并写入测试文件，返回 sandbox 路径。
func setupSandbox(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("创建子目录失败: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("写入文件失败: %v", err)
		}
	}
	return dir
}

// TestReadFileBasic 验证：传入 file_path 成功返回内容 + 行号格式。
func TestReadFileBasic(t *testing.T) {
	sandbox := setupSandbox(t, map[string]string{
		"hello.txt": "first\nsecond\nthird\n",
	})
	tool := NewReadFileTool(sandbox)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"file_path": "hello.txt"}`))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "L1: first") {
		t.Errorf("缺少第 1 行: %s", out)
	}
	if !strings.Contains(out, "L2: second") {
		t.Errorf("缺少第 2 行: %s", out)
	}
	if !strings.Contains(out, "L3: third") {
		t.Errorf("缺少第 3 行: %s", out)
	}
	if !strings.Contains(out, "（共 3 行") {
		t.Errorf("缺少总行数摘要: %s", out)
	}
}

// TestReadFileOffsetLimit 验证 offset/limit 分页语义（offset=10 返回第 11 行起）。
func TestReadFileOffsetLimit(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		b.WriteString("line-")
		b.WriteString(itoa(i))
		b.WriteString("\n")
	}
	sandbox := setupSandbox(t, map[string]string{"paged.txt": b.String()})
	tool := NewReadFileTool(sandbox)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"file_path":"paged.txt","offset":10,"limit":5}`))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	// 应返回 L11-L15（offset=10 跳过前 10 行，下一行编号 11）
	if !strings.Contains(out, "L11: line-11") {
		t.Errorf("缺少 L11: %s", out)
	}
	if !strings.Contains(out, "L15: line-15") {
		t.Errorf("缺少 L15: %s", out)
	}
	if strings.Contains(out, "L16:") {
		t.Errorf("应只返回 5 行，但出现 L16: %s", out)
	}
	if !strings.Contains(out, "本次返回 5 行") {
		t.Errorf("摘要应说明本次返回 5 行: %s", out)
	}
}

// TestReadFileBinaryRejection 验证二进制文件返回明确错误。
func TestReadFileBinaryRejection(t *testing.T) {
	sandbox := t.TempDir()
	// 写入 PNG 文件头（包含 NUL/非文本字节）
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}
	if err := os.WriteFile(filepath.Join(sandbox, "image.png"), pngHeader, 0644); err != nil {
		t.Fatalf("准备 PNG 失败: %v", err)
	}
	tool := NewReadFileTool(sandbox)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"file_path":"image.png"}`))
	if err == nil {
		t.Fatal("二进制文件应被拒绝")
	}
	if !strings.Contains(err.Error(), "非文本文件") {
		t.Errorf("错误消息应提示非文本文件: %v", err)
	}
}

// TestReadFileNotFound 验证文件不存在时返回明确错误。
func TestReadFileNotFound(t *testing.T) {
	sandbox := t.TempDir()
	tool := NewReadFileTool(sandbox)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"file_path":"nonexistent.txt"}`))
	if err == nil {
		t.Fatal("应返回错误")
	}
	if !strings.Contains(err.Error(), "文件不存在") {
		t.Errorf("错误消息应说明文件不存在: %v", err)
	}
}

// TestReadFileEmptyPath 验证 file_path 为空报错。
func TestReadFileEmptyPath(t *testing.T) {
	sandbox := t.TempDir()
	tool := NewReadFileTool(sandbox)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"file_path":""}`))
	if err == nil {
		t.Fatal("空路径应报错")
	}
}

// TestReadFilePathOutside 验证 sandbox 越界被拦截。
func TestReadFilePathOutside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上无 etc/passwd, 跳过")
	}
	sandbox := t.TempDir()
	tool := NewReadFileTool(sandbox)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"file_path":"../../../etc/passwd"}`))
	if err == nil {
		t.Fatal("越界路径应被拦截")
	}
}

// itoa 简单整数转字符串，避免引用 strconv。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
