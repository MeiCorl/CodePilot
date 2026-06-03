package builtin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestGlobRecursive 验证 ** 递归匹配。
func TestGlobRecursive(t *testing.T) {
	sandbox := setupSandbox(t, map[string]string{
		"a/x.go":        "",
		"a/b/y.go":      "",
		"a/b/c/z.go":    "",
		"a/b/c/other.txt": "",
	})
	tool := NewGlobTool(sandbox)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"a/**/*.go"}`))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	// 应有 3 个 .go
	if !strings.Contains(out, filepath.Join(sandbox, "a", "x.go")) {
		t.Errorf("缺少 a/x.go: %s", out)
	}
	if !strings.Contains(out, filepath.Join(sandbox, "a", "b", "y.go")) {
		t.Errorf("缺少 a/b/y.go: %s", out)
	}
	if !strings.Contains(out, filepath.Join(sandbox, "a", "b", "c", "z.go")) {
		t.Errorf("缺少 a/b/c/z.go: %s", out)
	}
	if strings.Contains(out, "other.txt") {
		t.Errorf("不应匹配 .txt: %s", out)
	}
}

// TestGlobSimplePattern 验证基本 *.ext 模式。
func TestGlobSimplePattern(t *testing.T) {
	sandbox := setupSandbox(t, map[string]string{
		"f1.go": "",
		"f2.go": "",
		"f3.txt": "",
	})
	tool := NewGlobTool(sandbox)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"*.go"}`))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	count := strings.Count(out, ".go")
	if count < 2 {
		t.Errorf("应至少匹配 2 个 .go, 实际: %s", out)
	}
	if strings.Contains(out, ".txt") {
		t.Errorf("不应匹配 .txt: %s", out)
	}
}

// TestGlobNoMatch 验证无匹配返回明确提示。
func TestGlobNoMatch(t *testing.T) {
	sandbox := t.TempDir()
	tool := NewGlobTool(sandbox)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"*.nonexistent"}`))
	if err != nil {
		t.Fatalf("无匹配不应报错: %v", err)
	}
	if !strings.Contains(out, "无匹配") {
		t.Errorf("应提示无匹配: %s", out)
	}
}

// TestGlobEmptyPattern 验证空 pattern 报错。
func TestGlobEmptyPattern(t *testing.T) {
	sandbox := t.TempDir()
	tool := NewGlobTool(sandbox)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":""}`))
	if err == nil {
		t.Fatal("空 pattern 应报错")
	}
}

// TestGlobBasePath 验证 path 参数指定基准目录。
func TestGlobBasePath(t *testing.T) {
	sandbox := setupSandbox(t, map[string]string{
		"sub/f1.go": "",
		"sub/f2.go": "",
		"f3.go":      "",
	})
	tool := NewGlobTool(sandbox)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"*.go","path":"sub"}`))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, filepath.Join(sandbox, "sub", "f1.go")) {
		t.Errorf("缺少 sub/f1.go: %s", out)
	}
	if strings.Contains(out, filepath.Join(sandbox, "f3.go")) {
		t.Errorf("不应匹配 sub 外的文件: %s", out)
	}
}
