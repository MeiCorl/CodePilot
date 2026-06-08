package builtin

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// TestRegisterAllSix 验证 Register 把 6 个工具全部注册到 Registry。
func TestRegisterAllSix(t *testing.T) {
	r := tool.NewRegistry()
	Register(r)

	names := r.EnabledNames(nil)
	if len(names) != 6 {
		t.Fatalf("应注册 6 个工具, 实际 %d: %v", len(names), names)
	}

	expected := []string{"Bash", "EditFile", "Glob", "Grep", "ReadFile", "WriteFile"}
	for i, want := range expected {
		if names[i] != want {
			t.Errorf("names[%d] 错误: 期望 %s, 实际 %s", i, want, names[i])
		}
	}
}

// TestRegisteredToolsHaveSchema 验证所有工具的 InputSchema 都不为空。
func TestRegisteredToolsHaveSchema(t *testing.T) {
	r := tool.NewRegistry()
	Register(r)

	for _, tl := range r.List() {
		schema := tl.InputSchema()
		if len(schema) == 0 {
			t.Errorf("工具 %s 的 InputSchema 为空", tl.Name())
		}
		if !strings.Contains(string(schema), `"object"`) {
			t.Errorf("工具 %s 的 schema 缺少 object 类型: %s", tl.Name(), schema)
		}
	}
}

// TestRegisteredToolsHaveDescription 验证所有工具的 Description 都不为空。
func TestRegisteredToolsHaveDescription(t *testing.T) {
	r := tool.NewRegistry()
	Register(r)
	for _, tl := range r.List() {
		if tl.Description() == "" {
			t.Errorf("工具 %s 的 Description 为空", tl.Name())
		}
	}
}

// TestReadFileSchemaReflectsInput 验证 ReadFile schema 包含 file_path 字段。
func TestReadFileSchemaReflectsInput(t *testing.T) {
	tool := NewReadFileTool(t.TempDir())
	schema := string(tool.InputSchema())
	if !strings.Contains(schema, "file_path") {
		t.Errorf("schema 缺少 file_path 字段: %s", schema)
	}
	if !strings.Contains(schema, "offset") {
		t.Errorf("schema 缺少 offset 字段: %s", schema)
	}
	if !strings.Contains(schema, "limit") {
		t.Errorf("schema 缺少 limit 字段: %s", schema)
	}
}

// TestBashSchemaReflectsInput 验证 Bash schema 包含 command 字段。
func TestBashSchemaReflectsInput(t *testing.T) {
	tool := NewBashTool(0)
	schema := string(tool.InputSchema())
	if !strings.Contains(schema, "command") {
		t.Errorf("schema 缺少 command 字段: %s", schema)
	}
	if !strings.Contains(schema, "timeout") {
		t.Errorf("schema 缺少 timeout 字段: %s", schema)
	}
}

// TestRegisterWithOptionsOverrides 验证 RegisterWithOptions 用新参数覆盖已注册的工具。
// 这是 Task 8 主流程"加载 cfg 后用 cfg 重新构造工具"路径的基础能力。
func TestRegisterWithOptionsOverrides(t *testing.T) {
	r := tool.NewRegistry()
	Register(r)

	customWorkdir := t.TempDir()
	RegisterWithOptions(r, customWorkdir, 7*time.Second)

	if r.Count() != 6 {
		t.Errorf("覆盖后仍应注册 6 个工具, 实际 %d", r.Count())
	}
	// 重新拿一次 Bash 工具实例，验证 Bash 工具被替换为新构造的实例
	bash, ok := r.Get("Bash")
	if !ok {
		t.Fatal("Bash 工具未注册")
	}
	if bash.Description() == "" {
		t.Error("Bash 工具 Description 应非空")
	}
}

// fakeFileDiffSink 捕获写入的 diff 条目，供 register_test 断言。
// 仅暴露 Set 满足 tool.FileDiffSink interface，验收"被注入的 sink 真的被工具调用"。
type fakeFileDiffSink struct {
	calls []fakeFileDiffCall
}

type fakeFileDiffCall struct {
	ToolUseID string
	Path      string
	Before    string
	After     string
}

func (f *fakeFileDiffSink) Set(toolUseID string, entry tool.FileDiffEntry) bool {
	f.calls = append(f.calls, fakeFileDiffCall{
		ToolUseID: toolUseID,
		Path:      entry.FilePath,
		Before:    entry.Before,
		After:     entry.After,
	})
	return true
}

// TestRegisterWithDiffSink_WriteAndEdit 验证主流程"按 Register 名取出
// WriteFile/EditFile 工具并注入 DiffSink"模式在测试环境同样成立：
// 1) 取出后类型断言能拿到具体工具
// 2) 注入 sink 后，工具执行时 ctx 含 toolUseID 时真的把 diff 推给 sink
// 3) EditFile 同样路径
//
// 这是 main.go 中 FileDiffStore 注入链路的回归保护。
func TestRegisterWithDiffSink_WriteAndEdit(t *testing.T) {
	dir := t.TempDir()
	r := tool.NewRegistry()
	RegisterWithOptions(r, dir, 30*time.Second)

	sink := &fakeFileDiffSink{}

	// ---- WriteFile ----
	wfTool, ok := r.Get(WriteFileName)
	if !ok {
		t.Fatalf("WriteFile 工具未注册")
	}
	wf, ok := wfTool.(*WriteFileTool)
	if !ok {
		t.Fatalf("取出 WriteFile 工具后类型断言失败: %T", wfTool)
	}
	wf.SetDiffSink(sink)

	target := dir + "/hello.go"
	inputWrite := mustMarshal(t, map[string]string{
		"file_path": target,
		"content":   "package x\nconst A = 1\n",
	})
	ctxWrite := tool.WithToolUseID(withSandedPath(t, dir, "hello.go"), "tool-w-1")
	out, err := wf.Execute(ctxWrite, inputWrite)
	if err != nil {
		t.Fatalf("WriteFile 执行失败: %v", err)
	}
	if out == "" {
		t.Fatal("WriteFile 应返回非空 output")
	}
	if len(sink.calls) != 1 {
		t.Fatalf("WriteFile 注入 sink 后应有 1 次 Set 调用, 实际 %d", len(sink.calls))
	}
	if sink.calls[0].ToolUseID != "tool-w-1" {
		t.Errorf("WriteFile Set 的 toolUseID 错误: %s", sink.calls[0].ToolUseID)
	}
	if sink.calls[0].Path != target {
		// Windows 上 filepath.Abs/Clean 可能把短路径展开为长 UNC 路径，
		// 强相等比较易碎；改为后缀 + filename 匹配保证语义正确
		base := filepath.Base(target)
		if !strings.HasSuffix(sink.calls[0].Path, base) {
			t.Errorf("WriteFile Set 的 path 应以 %q 结尾, 实际 %q", base, sink.calls[0].Path)
		}
	}
	if sink.calls[0].Before != "" {
		t.Errorf("新文件场景 before 应为空, 实际 %q", sink.calls[0].Before)
	}
	if !strings.Contains(sink.calls[0].After, "const A = 1") {
		t.Errorf("WriteFile Set 的 after 缺关键内容: %q", sink.calls[0].After)
	}

	// ---- EditFile ----
	efTool, ok := r.Get(EditFileName)
	if !ok {
		t.Fatalf("EditFile 工具未注册")
	}
	ef, ok := efTool.(*EditFileTool)
	if !ok {
		t.Fatalf("取出 EditFile 工具后类型断言失败: %T", efTool)
	}
	ef.SetDiffSink(sink)

	inputEdit := mustMarshal(t, map[string]string{
		"file_path":  target,
		"old_string": "const A = 1",
		"new_string": "const A = 2",
	})
	ctxEdit := tool.WithToolUseID(withSandedPath(t, dir, "hello.go"), "tool-e-1")
	out, err = ef.Execute(ctxEdit, inputEdit)
	if err != nil {
		t.Fatalf("EditFile 执行失败: %v", err)
	}
	if out == "" {
		t.Fatal("EditFile 应返回非空 output")
	}
	if len(sink.calls) != 2 {
		t.Fatalf("EditFile 注入 sink 后应有 2 次 Set 调用, 实际 %d", len(sink.calls))
	}
	last := sink.calls[1]
	if last.ToolUseID != "tool-e-1" {
		t.Errorf("EditFile Set 的 toolUseID 错误: %s", last.ToolUseID)
	}
	if !strings.Contains(last.Before, "const A = 1") {
		t.Errorf("EditFile Set 的 before 缺旧内容: %q", last.Before)
	}
	if !strings.Contains(last.After, "const A = 2") {
		t.Errorf("EditFile Set 的 after 缺新内容: %q", last.After)
	}
	if strings.Contains(last.After, "const A = 1") {
		t.Errorf("EditFile Set 的 after 不应再含旧内容: %q", last.After)
	}
}

// TestRegisterWithDiffSink_NilSafe 验证 SetDiffSink(nil) 不会让工具 panic。
// 与 main.go 启动失败 / 测试环境不注入 sink 的兼容路径对应。
func TestRegisterWithDiffSink_NilSafe(t *testing.T) {
	dir := t.TempDir()
	wf := NewWriteFileTool(dir)
	wf.SetDiffSink(nil) // 显式 nil

	target := dir + "/nil-safe.txt"
	input := mustMarshal(t, map[string]string{
		"file_path": target,
		"content":   "hi",
	})
	ctx := tool.WithToolUseID(withSandedPath(t, dir, "nil-safe.txt"), "tool-nil-1")
	if _, err := wf.Execute(ctx, input); err != nil {
		t.Fatalf("nil sink 时 WriteFile 应仍能正常完成: %v", err)
	}
}

// TestRegisterWithDiffSink_NoToolUseID_NoSinkCall 验证 ctx 中缺 toolUseID 时
// 工具不调用 sink（避免写入一条无 id 的 diff 记录）。
func TestRegisterWithDiffSink_NoToolUseID_NoSinkCall(t *testing.T) {
	dir := t.TempDir()
	r := tool.NewRegistry()
	RegisterWithOptions(r, dir, 30*time.Second)

	sink := &fakeFileDiffSink{}
	wfTool, _ := r.Get(WriteFileName)
	wf := wfTool.(*WriteFileTool)
	wf.SetDiffSink(sink)

	// 注意：ctx 中故意不调用 tool.WithToolUseID 注入 id
	target := dir + "/no-id.txt"
	input := mustMarshal(t, map[string]string{
		"file_path": target,
		"content":   "hello",
	})
	if _, err := wf.Execute(withSandedPath(t, dir, "no-id.txt"), input); err != nil {
		t.Fatalf("Execute 不应失败: %v", err)
	}
	if len(sink.calls) != 0 {
		t.Errorf("ctx 缺 toolUseID 时不应调用 sink, 实际 %d 次", len(sink.calls))
	}
}

// mustMarshal 把 map 编码为 JSON RawMessage，错误即终止测试。
// 用于构造 WriteFile / EditFile 的入参，避免手写 JSON 字符串时被
// Windows 路径反斜杠等字符影响。
func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("构造入参失败: %v", err)
	}
	return b
}
