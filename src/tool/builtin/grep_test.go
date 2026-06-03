package builtin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestGrepBasic 验证：在指定目录下搜索匹配行，输出 path:L<n>:text 格式。
func TestGrepBasic(t *testing.T) {
	sandbox := setupSandbox(t, map[string]string{
		"a.txt": "alpha\nbeta with TODO\ngamma\nTODO: finish\n",
		"b.txt": "no match here\n",
	})
	tool := NewGrepTool(sandbox)
	input, _ := json.Marshal(map[string]string{"pattern": "TODO", "path": sandbox})
	out, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "L2:beta with TODO") {
		t.Errorf("缺少 a.txt 第 2 行匹配: %s", out)
	}
	if !strings.Contains(out, "L4:TODO: finish") {
		t.Errorf("缺少 a.txt 第 4 行匹配: %s", out)
	}
	if strings.Contains(out, "b.txt") {
		t.Errorf("b.txt 不应被匹配: %s", out)
	}
}

// TestGrepIncludeFilter 验证 include glob 过滤文件。
func TestGrepIncludeFilter(t *testing.T) {
	sandbox := setupSandbox(t, map[string]string{
		"x.go":  "package main // MARKER\n",
		"x.txt": "MARKER text\n",
		"y.go":  "no marker\n",
	})
	tool := NewGrepTool(sandbox)
	input, _ := json.Marshal(map[string]string{
		"pattern": "MARKER",
		"include": "*.go",
		"path":    sandbox,
	})
	out, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "x.go") {
		t.Errorf("缺少 x.go 匹配: %s", out)
	}
	if strings.Contains(out, "x.txt") {
		t.Errorf("x.txt 应被 include=*.go 过滤: %s", out)
	}
	if strings.Contains(out, "y.go") {
		t.Errorf("y.go 不含 MARKER: %s", out)
	}
}

// TestGrepNoMatch 验证无匹配返回明确提示。
func TestGrepNoMatch(t *testing.T) {
	sandbox := setupSandbox(t, map[string]string{
		"a.txt": "alpha\nbeta\n",
	})
	tool := NewGrepTool(sandbox)
	input, _ := json.Marshal(map[string]string{"pattern": "XYZNOTHING", "path": sandbox})
	out, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "无匹配") {
		t.Errorf("应提示无匹配: %s", out)
	}
}

// TestGrepInvalidRegex 验证非法正则返回明确错误。
func TestGrepInvalidRegex(t *testing.T) {
	sandbox := t.TempDir()
	tool := NewGrepTool(sandbox)
	// Go regexp 不允许未闭合的 [
	input, _ := json.Marshal(map[string]string{"pattern": "[unclosed", "path": sandbox})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("非法正则应报错")
	}
	if !strings.Contains(err.Error(), "正则") {
		t.Errorf("错误应说明正则问题: %v", err)
	}
}

// TestGrepEmptyPattern 验证空 pattern 报错。
func TestGrepEmptyPattern(t *testing.T) {
	sandbox := t.TempDir()
	tool := NewGrepTool(sandbox)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":""}`))
	if err == nil {
		t.Fatal("空 pattern 应报错")
	}
}

// TestGrepOutputFormat 验证输出格式严格符合 path:L<line>:<text>。
func TestGrepOutputFormat(t *testing.T) {
	sandbox := setupSandbox(t, map[string]string{
		"f.txt": "hit\n",
	})
	tool := NewGrepTool(sandbox)
	input, _ := json.Marshal(map[string]string{"pattern": "hit", "path": sandbox})
	out, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	absFile := filepath.Join(sandbox, "f.txt")
	expected := absFile + ":L1:hit"
	if !strings.Contains(out, expected) {
		t.Errorf("输出格式不符: 期望包含 %q, 实际 %q", expected, out)
	}
}
