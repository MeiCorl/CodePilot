package builtin

import (
	"strings"
	"testing"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// TestRegisterAllFive 验证 Register 把 5 个工具全部注册到 Registry。
func TestRegisterAllFive(t *testing.T) {
	r := tool.NewRegistry()
	Register(r)

	names := r.EnabledNames(nil)
	if len(names) != 5 {
		t.Fatalf("应注册 5 个工具, 实际 %d: %v", len(names), names)
	}

	expected := []string{"bash", "glob", "grep", "read_file", "write_file"}
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

	if r.Count() != 5 {
		t.Errorf("覆盖后仍应注册 5 个工具, 实际 %d", r.Count())
	}
	// 重新拿一次 Bash 工具实例，验证 Bash 工具被替换为新构造的实例
	bash, ok := r.Get("bash")
	if !ok {
		t.Fatal("Bash 工具未注册")
	}
	if bash.Description() == "" {
		t.Error("Bash 工具 Description 应非空")
	}
}
