package builtin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// fakeDiffSink 是 tool.FileDiffSink 的测试实现。
// 比 builtin 包内 fakeDiffSink 更通用：可被 WriteFile/EditFile 共用。
type fakeDiffSink struct {
	entries map[string]tool.FileDiffEntry // toolUseID -> entry
	reject  bool                          // 模拟"容量超限"
}

func newFakeDiffSink() *fakeDiffSink {
	return &fakeDiffSink{entries: make(map[string]tool.FileDiffEntry)}
}

func (f *fakeDiffSink) Set(toolUseID string, entry tool.FileDiffEntry) bool {
	if f.reject {
		return false
	}
	f.entries[toolUseID] = entry
	return true
}

func (f *fakeDiffSink) count() int { return len(f.entries) }

// TestWriteFile_DiffSink_RecordAfterSuccess 验证 sink 已注入 + ctx 含 toolUseID 时写入。
func TestWriteFile_DiffSink_RecordAfterSuccess(t *testing.T) {
	sandbox := t.TempDir()
	tw := NewWriteFileTool(sandbox)
	sink := newFakeDiffSink()
	tw.SetDiffSink(sink)

	ctx := tool.WithToolUseID(withSandedPath(t, sandbox, "new.txt"), "wu-1")
	input := json.RawMessage(`{"file_path":"new.txt","content":"hello"}`)
	if _, err := tw.Execute(ctx, input); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if sink.count() != 1 {
		t.Fatalf("sink 应收到 1 条, 实际 %d", sink.count())
	}
	got, ok := sink.entries["wu-1"]
	if !ok {
		t.Fatalf("应按 toolUseID 索引到记录")
	}
	if got.FilePath != filepath.Join(sandbox, "new.txt") {
		t.Errorf("FilePath 期望 %q, 实际 %q", filepath.Join(sandbox, "new.txt"), got.FilePath)
	}
	if got.Before != "" {
		t.Errorf("新文件 before 应为空, 实际 %q", got.Before)
	}
	if got.After != "hello" {
		t.Errorf("After 期望 hello, 实际 %q", got.After)
	}
}

// TestWriteFile_DiffSink_NewFileBeforeEmpty + TestWriteFile_DiffSink_OverwriteBefore 验证覆盖前/新建文件区分。
func TestWriteFile_DiffSink_OverwriteBefore(t *testing.T) {
	sandbox := t.TempDir()
	tw := NewWriteFileTool(sandbox)
	sink := newFakeDiffSink()
	tw.SetDiffSink(sink)

	// 首次写入
	ctx1 := tool.WithToolUseID(withSandedPath(t, sandbox, "a.txt"), "wu-first")
	if _, err := tw.Execute(ctx1, json.RawMessage(`{"file_path":"a.txt","content":"v1"}`)); err != nil {
		t.Fatalf("首次执行失败: %v", err)
	}
	if sink.entries["wu-first"].Before != "" {
		t.Errorf("新文件场景 before 应为空, 实际 %q", sink.entries["wu-first"].Before)
	}
	if sink.entries["wu-first"].After != "v1" {
		t.Errorf("新文件场景 after 应为 v1, 实际 %q", sink.entries["wu-first"].After)
	}

	// 二次覆盖
	ctx2 := tool.WithToolUseID(withSandedPath(t, sandbox, "a.txt"), "wu-second")
	if _, err := tw.Execute(ctx2, json.RawMessage(`{"file_path":"a.txt","content":"v2"}`)); err != nil {
		t.Fatalf("二次执行失败: %v", err)
	}
	if sink.entries["wu-second"].Before != "v1" {
		t.Errorf("覆盖场景 before 应为 v1, 实际 %q", sink.entries["wu-second"].Before)
	}
	if sink.entries["wu-second"].After != "v2" {
		t.Errorf("覆盖场景 after 应为 v2, 实际 %q", sink.entries["wu-second"].After)
	}
}

// TestWriteFile_DiffSink_NilSinkSafe 验证 sink 为 nil 时不 panic 且不写入。
func TestWriteFile_DiffSink_NilSinkSafe(t *testing.T) {
	sandbox := t.TempDir()
	tw := NewWriteFileTool(sandbox)
	// 显式不调 SetDiffSink，DiffSink 字段为 nil
	ctx := tool.WithToolUseID(withSandedPath(t, sandbox, "x.txt"), "wu-x")
	if _, err := tw.Execute(ctx, json.RawMessage(`{"file_path":"x.txt","content":"y"}`)); err != nil {
		t.Fatalf("sink 为 nil 时执行仍应成功: %v", err)
	}
}

// TestWriteFile_DiffSink_NoToolUseID 验证 ctx 缺 toolUseID 时跳过写入。
func TestWriteFile_DiffSink_NoToolUseID(t *testing.T) {
	sandbox := t.TempDir()
	tw := NewWriteFileTool(sandbox)
	sink := newFakeDiffSink()
	tw.SetDiffSink(sink)

	// ctx 含 PathResolver 但故意不注入 toolUseID
	ctx := withSandedPath(t, sandbox, "x.txt")
	if _, err := tw.Execute(ctx, json.RawMessage(`{"file_path":"x.txt","content":"y"}`)); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if sink.count() != 0 {
		t.Fatalf("ctx 缺 toolUseID 时不应写入, 实际收到 %d 条", sink.count())
	}
}

// TestWriteFile_DiffSink_RejectDoesNotAffectReturn 验证 sink 返回 false（容量超限）时主流程仍成功。
func TestWriteFile_DiffSink_RejectDoesNotAffectReturn(t *testing.T) {
	sandbox := t.TempDir()
	tw := NewWriteFileTool(sandbox)
	sink := newFakeDiffSink()
	sink.reject = true // 模拟"容量超限" → Set 返回 false
	tw.SetDiffSink(sink)

	ctx := tool.WithToolUseID(withSandedPath(t, sandbox, "x.txt"), "wu-rj")
	out, err := tw.Execute(ctx, json.RawMessage(`{"file_path":"x.txt","content":"y"}`))
	if err != nil {
		t.Fatalf("sink 拒绝时主流程仍应成功, 实际 err: %v", err)
	}
	if out == "" {
		t.Errorf("应有正常 output 返回")
	}
}

// TestWriteFile_DiffSink_ParamErrorNotRecorded 验证参数解析失败时不应写入。
func TestWriteFile_DiffSink_ParamErrorNotRecorded(t *testing.T) {
	sandbox := t.TempDir()
	tw := NewWriteFileTool(sandbox)
	sink := newFakeDiffSink()
	tw.SetDiffSink(sink)

	ctx := tool.WithToolUseID(context.Background(), "wu-bad")
	// 故意构造非法 JSON
	_, _ = tw.Execute(ctx, json.RawMessage(`{not-json`))
	if sink.count() != 0 {
		t.Fatalf("参数解析失败时不应写入, 实际 %d 条", sink.count())
	}
}
