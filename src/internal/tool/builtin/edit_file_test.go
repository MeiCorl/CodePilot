package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// TestEditFile_ReplaceOnce 验证基本替换流程。
func TestEditFile_ReplaceOnce(t *testing.T) {
	sandbox := t.TempDir()
	target := filepath.Join(sandbox, "a.txt")
	if err := os.WriteFile(target, []byte("hello world"), 0644); err != nil {
		t.Fatalf("种子文件写入失败: %v", err)
	}
	tool := NewEditFileTool(sandbox)
	ctx := withSandedPath(t, sandbox, "a.txt")
	out, err := tool.Execute(ctx,
		json.RawMessage(`{"file_path":"a.txt","old_string":"world","new_string":"Go"}`))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "已编辑") {
		t.Errorf("输出应包含 '已编辑', 实际: %s", out)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "hello Go" {
		t.Errorf("内容错误: %s", got)
	}
}

// TestEditFile_NotFound 验证 old_string 不存在时返回 error。
func TestEditFile_NotFound(t *testing.T) {
	sandbox := t.TempDir()
	target := filepath.Join(sandbox, "a.txt")
	_ = os.WriteFile(target, []byte("hello"), 0644)
	tool := NewEditFileTool(sandbox)
	ctx := withSandedPath(t, sandbox, "a.txt")
	_, err := tool.Execute(ctx,
		json.RawMessage(`{"file_path":"a.txt","old_string":"not-in-file","new_string":"x"}`))
	if err == nil {
		t.Fatal("old_string 不应匹配时应当返回 error")
	}
}

// TestEditFile_NotUnique 验证 old_string 多处匹配时报错。
func TestEditFile_NotUnique(t *testing.T) {
	sandbox := t.TempDir()
	target := filepath.Join(sandbox, "a.txt")
	_ = os.WriteFile(target, []byte("aaa\naaa\n"), 0644)
	tool := NewEditFileTool(sandbox)
	ctx := withSandedPath(t, sandbox, "a.txt")
	_, err := tool.Execute(ctx,
		json.RawMessage(`{"file_path":"a.txt","old_string":"aaa","new_string":"bbb"}`))
	if err == nil {
		t.Fatal("多处匹配应返回 error")
	}
}

// TestEditFile_DeleteWithEmptyNew 验证 new_string="" 删除原文。
func TestEditFile_DeleteWithEmptyNew(t *testing.T) {
	sandbox := t.TempDir()
	target := filepath.Join(sandbox, "a.txt")
	_ = os.WriteFile(target, []byte("header\nmiddle\nfooter\n"), 0644)
	tool := NewEditFileTool(sandbox)
	ctx := withSandedPath(t, sandbox, "a.txt")
	_, err := tool.Execute(ctx,
		json.RawMessage(`{"file_path":"a.txt","old_string":"middle\n","new_string":""}`))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "header\nfooter\n" {
		t.Errorf("删除后内容错误: %q", got)
	}
}

// TestEditFile_EmptyOldString 验证空 old_string 报错。
func TestEditFile_EmptyOldString(t *testing.T) {
	sandbox := t.TempDir()
	tool := NewEditFileTool(sandbox)
	ctx := withSandedPath(t, sandbox, "a.txt")
	_, err := tool.Execute(ctx,
		json.RawMessage(`{"file_path":"a.txt","old_string":"","new_string":"x"}`))
	if err == nil {
		t.Fatal("空 old_string 应返回 error")
	}
}

// TestEditFile_DiffSink_RecordAfterSuccess 验证 sink 已注入 + ctx 含 toolUseID 时写入。
func TestEditFile_DiffSink_RecordAfterSuccess(t *testing.T) {
	sandbox := t.TempDir()
	target := filepath.Join(sandbox, "a.txt")
	_ = os.WriteFile(target, []byte("alpha\nbeta\ngamma\n"), 0644)

	te := NewEditFileTool(sandbox)
	sink := newFakeDiffSink()
	te.SetDiffSink(sink)

	ctx := tool.WithToolUseID(withSandedPath(t, sandbox, "a.txt"), "eu-1")
	if _, err := te.Execute(ctx,
		json.RawMessage(`{"file_path":"a.txt","old_string":"beta","new_string":"BETA"}`)); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if sink.count() != 1 {
		t.Fatalf("sink 应收到 1 条, 实际 %d", sink.count())
	}
	got := sink.entries["eu-1"]
	if got.FilePath != target {
		t.Errorf("FilePath 期望 %q, 实际 %q", target, got.FilePath)
	}
	if got.Before != "alpha\nbeta\ngamma\n" {
		t.Errorf("Before 不一致: %q", got.Before)
	}
	if got.After != "alpha\nBETA\ngamma\n" {
		t.Errorf("After 不一致: %q", got.After)
	}
}

// TestEditFile_DiffSink_NotRecordedWhenNotFound 验证 old_string 不匹配时不写入。
func TestEditFile_DiffSink_NotRecordedWhenNotFound(t *testing.T) {
	sandbox := t.TempDir()
	target := filepath.Join(sandbox, "a.txt")
	_ = os.WriteFile(target, []byte("hello"), 0644)
	te := NewEditFileTool(sandbox)
	sink := newFakeDiffSink()
	te.SetDiffSink(sink)

	ctx := tool.WithToolUseID(withSandedPath(t, sandbox, "a.txt"), "eu-nf")
	_, _ = te.Execute(ctx,
		json.RawMessage(`{"file_path":"a.txt","old_string":"nope","new_string":"x"}`))
	if sink.count() != 0 {
		t.Fatalf("执行失败时不应写入, 实际 %d 条", sink.count())
	}
}

// TestEditFile_DiffSink_NilSafe 验证 sink 为 nil 时不 panic。
func TestEditFile_DiffSink_NilSafe(t *testing.T) {
	sandbox := t.TempDir()
	target := filepath.Join(sandbox, "a.txt")
	_ = os.WriteFile(target, []byte("abc"), 0644)
	te := NewEditFileTool(sandbox)
	// 显式不调 SetDiffSink
	ctx := tool.WithToolUseID(withSandedPath(t, sandbox, "a.txt"), "eu-nil")
	if _, err := te.Execute(ctx,
		json.RawMessage(`{"file_path":"a.txt","old_string":"abc","new_string":"xyz"}`)); err != nil {
		t.Fatalf("sink 为 nil 时仍应成功: %v", err)
	}
}

// TestEditFile_DiffSink_NoToolUseID 验证 ctx 缺 toolUseID 时跳过。
func TestEditFile_DiffSink_NoToolUseID(t *testing.T) {
	sandbox := t.TempDir()
	target := filepath.Join(sandbox, "a.txt")
	_ = os.WriteFile(target, []byte("abc"), 0644)
	te := NewEditFileTool(sandbox)
	sink := newFakeDiffSink()
	te.SetDiffSink(sink)

	// 这里 ctx 含 PathResolver 但不注入 toolUseID
	ctx := withSandedPath(t, sandbox, "a.txt")
	if _, err := te.Execute(ctx,
		json.RawMessage(`{"file_path":"a.txt","old_string":"abc","new_string":"xyz"}`)); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if sink.count() != 0 {
		t.Fatalf("ctx 缺 toolUseID 时不应写入, 实际 %d", sink.count())
	}
}

// 注册侧集成测试：RegisterWithOptions 之后再注入 sink，验证拿到的就是新实例。
func TestRegisterWithOptions_InjectSink(t *testing.T) {
	r := tool.NewRegistry()
	RegisterWithOptions(r, t.TempDir(), 5*time.Second)
	sink := newFakeDiffSink()

	if wfAny, ok := r.Get(WriteFileName); ok {
		if wf, ok := wfAny.(*WriteFileTool); ok {
			wf.SetDiffSink(sink)
		} else {
			t.Fatal("Registry 拿到的不是 *WriteFileTool")
		}
	} else {
		t.Fatal("Registry 找不到 WriteFileName")
	}
	if efAny, ok := r.Get(EditFileName); ok {
		if ef, ok := efAny.(*EditFileTool); ok {
			ef.SetDiffSink(sink)
		} else {
			t.Fatal("Registry 拿到的不是 *EditFileTool")
		}
	} else {
		t.Fatal("Registry 找不到 EditFileName")
	}

	// 通过 Registry 拿到的就是注入 sink 的实例，执行后应能收到 entry
	wfTool, _ := r.Get(WriteFileName)
	sandbox := t.TempDir()
	ctx := tool.WithToolUseID(withSandedPath(t, sandbox, "a.txt"), "wu-int")
	if _, err := wfTool.Execute(ctx,
		json.RawMessage(`{"file_path":"a.txt","content":"hi"}`)); err != nil {
		t.Fatalf("通过 Registry 调 WriteFile 失败: %v", err)
	}
	if sink.count() != 1 {
		t.Fatalf("集成场景下 sink 应收到 1 条, 实际 %d", sink.count())
	}
}
