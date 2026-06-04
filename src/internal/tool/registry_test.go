package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeTool 测试用工具实现，字段均可自定义以便覆盖各种场景。
type fakeTool struct {
	BaseTool
	execFn func(ctx context.Context, input json.RawMessage) (string, error)
}

func (f *fakeTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	if f.execFn != nil {
		return f.execFn(ctx, input)
	}
	return "ok", nil
}

func newFakeTool(name string) *fakeTool {
	return &fakeTool{
		BaseTool: BaseTool{
			ToolName:        name,
			ToolDescription: "fake tool " + name,
			ToolInputSchema: json.RawMessage(`{"type":"object"}`),
			ToolPermission:  PermRead,
		},
	}
}

// TestRegisterAndGet 验证注册后能查到，且 Get 未命中时返回 ok=false。
func TestRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	a := newFakeTool("alpha")
	if err := r.Register(a); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}
	got, ok := r.Get("alpha")
	if !ok {
		t.Fatal("Get 未命中")
	}
	if got.Name() != "alpha" {
		t.Errorf("Name 错误: %s", got.Name())
	}
	if _, ok := r.Get("nonexistent"); ok {
		t.Error("Get 不存在的工具应返回 ok=false")
	}
}

// TestRegisterDuplicate 验证重复注册同名工具返回明确错误。
func TestRegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(newFakeTool("dup")); err != nil {
		t.Fatalf("首次注册失败: %v", err)
	}
	err := r.Register(newFakeTool("dup"))
	if err == nil {
		t.Fatal("重复注册应返回错误")
	}
	var dupErr *ErrToolAlreadyRegistered
	if !errors.As(err, &dupErr) {
		t.Errorf("错误类型应为 *ErrToolAlreadyRegistered, 实际 %T", err)
	}
	if dupErr.Name != "dup" {
		t.Errorf("Name 错误: %s", dupErr.Name)
	}
}

// TestRegisterEmptyName 验证空 Name 工具被拒绝。
func TestRegisterEmptyName(t *testing.T) {
	r := NewRegistry()
	empty := &fakeTool{BaseTool: BaseTool{ToolName: ""}}
	if err := r.Register(empty); err == nil {
		t.Fatal("空 Name 应返回错误")
	}
}

// TestRegisterNil 验证 nil 工具被拒绝。
func TestRegisterNil(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Fatal("nil 工具应返回错误")
	}
}

// TestListSorted 验证 List 返回按 Name 排序的快照。
func TestListSorted(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"zeta", "alpha", "mu"} {
		_ = r.Register(newFakeTool(n))
	}
	list := r.List()
	if len(list) != 3 {
		t.Fatalf("List 长度错误: %d", len(list))
	}
	expected := []string{"alpha", "mu", "zeta"}
	for i, want := range expected {
		if list[i].Name() != want {
			t.Errorf("List[%d] 错误: 期望 %s, 实际 %s", i, want, list[i].Name())
		}
	}
}

// TestCount 验证 Count 准确反映已注册数量。
func TestCount(t *testing.T) {
	r := NewRegistry()
	if r.Count() != 0 {
		t.Errorf("初始 Count 应为 0, 实际 %d", r.Count())
	}
	_ = r.Register(newFakeTool("a"))
	_ = r.Register(newFakeTool("b"))
	if r.Count() != 2 {
		t.Errorf("Count 应为 2, 实际 %d", r.Count())
	}
}

// TestEnabledNamesEmptyMeansAll 验证 enabled 为空时返回所有工具。
func TestEnabledNamesEmptyMeansAll(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFakeTool("a"))
	_ = r.Register(newFakeTool("b"))
	_ = r.Register(newFakeTool("c"))
	names := r.EnabledNames(nil)
	if len(names) != 3 {
		t.Fatalf("应返回 3 个, 实际 %d", len(names))
	}
	want := []string{"a", "b", "c"}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("names[%d] 错误: 期望 %s, 实际 %s", i, w, names[i])
		}
	}
}

// TestEnabledNamesWhitelist 验证白名单模式仅返回白名单内且已注册的工具。
func TestEnabledNamesWhitelist(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFakeTool("a"))
	_ = r.Register(newFakeTool("b"))
	_ = r.Register(newFakeTool("c"))
	// 包含未注册 + 重复
	names := r.EnabledNames([]string{"c", "a", "missing", "a"})
	want := []string{"a", "c"}
	if len(names) != len(want) {
		t.Fatalf("长度错误: 期望 %d, 实际 %d (%v)", len(want), len(names), names)
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("names[%d] 错误: 期望 %s, 实际 %s", i, w, names[i])
		}
	}
}

// TestMustRegisterPanicsOnDuplicate 验证 MustRegister 在重复时 panic。
func TestMustRegisterPanicsOnDuplicate(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(newFakeTool("x"))
	defer func() {
		if recover() == nil {
			t.Fatal("MustRegister 重复时应该 panic")
		}
	}()
	r.MustRegister(newFakeTool("x"))
}

// TestConcurrentRegisterAndGet 验证并发读写无 race（go test -race 验证）。
func TestConcurrentRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = r.Register(newFakeTool("tool_" + itoa(idx)))
		}(i)
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.List()
		}()
	}
	wg.Wait()
	if r.Count() != 50 {
		t.Errorf("Count 应为 50, 实际 %d", r.Count())
	}
}

// TestDefaultRegistrySingleton 验证 DefaultRegistry 是单例。
func TestDefaultRegistrySingleton(t *testing.T) {
	r1 := DefaultRegistry()
	r2 := DefaultRegistry()
	if r1 != r2 {
		t.Error("DefaultRegistry 应返回同一实例")
	}
}

// TestBaseToolDelegates 验证 BaseTool 的方法委托。
func TestBaseToolDelegates(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)
	b := BaseTool{
		ToolName:        "demo",
		ToolDescription: "demo tool",
		ToolInputSchema: schema,
		ToolPermission:  PermWrite,
	}
	if b.Name() != "demo" {
		t.Errorf("Name: %s", b.Name())
	}
	if b.Description() != "demo tool" {
		t.Errorf("Description: %s", b.Description())
	}
	if string(b.InputSchema()) != string(schema) {
		t.Errorf("InputSchema 错误")
	}
	if b.Permission() != PermWrite {
		t.Errorf("Permission: %s", b.Permission())
	}
}

// TestPermissionString 验证 Permission 枚举的 String 输出。
func TestPermissionString(t *testing.T) {
	cases := map[ToolPermission]string{
		PermRead:  "read",
		PermWrite: "write",
		PermExec:  "exec",
	}
	for p, want := range cases {
		if p.String() != want {
			t.Errorf("Permission(%d).String() = %s, 期望 %s", p, p.String(), want)
		}
	}
	// 未知值
	if !strings.HasPrefix(ToolPermission(99).String(), "unknown") {
		t.Error("未知 Permission 应输出 unknown 前缀")
	}
}

// TestToSpecsAll 验证 enabled 为空时返回所有工具的 ToolSpec 描述（按 Name 排序）。
func TestToSpecsAll(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFakeTool("alpha"))
	_ = r.Register(newFakeTool("beta"))

	specs := r.ToSpecs(nil)
	if len(specs) != 2 {
		t.Fatalf("应返回 2 个 spec, 实际 %d", len(specs))
	}
	if specs[0].Name != "alpha" {
		t.Errorf("specs[0].Name 错误: %s", specs[0].Name)
	}
	if specs[0].Description != "fake tool alpha" {
		t.Errorf("specs[0].Description 错误: %s", specs[0].Description)
	}
	if len(specs[0].InputSchema) == 0 {
		t.Error("specs[0].InputSchema 不应为空")
	}
}

// TestToSpecsWhitelist 验证白名单模式下仅返回 enabled 中已注册的工具。
func TestToSpecsWhitelist(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFakeTool("alpha"))
	_ = r.Register(newFakeTool("beta"))
	_ = r.Register(newFakeTool("gamma"))

	specs := r.ToSpecs([]string{"gamma", "missing", "alpha"})
	if len(specs) != 2 {
		t.Fatalf("应返回 2 个 spec, 实际 %d", len(specs))
	}
	// 按 Name 排序：alpha 在前
	if specs[0].Name != "alpha" || specs[1].Name != "gamma" {
		t.Errorf("排序错误: %s, %s", specs[0].Name, specs[1].Name)
	}
}

// TestToSpecsEmptyRegistry 验证空 Registry 返回空数组。
func TestToSpecsEmptyRegistry(t *testing.T) {
	r := NewRegistry()
	specs := r.ToSpecs(nil)
	if len(specs) != 0 {
		t.Errorf("空 Registry 应返回空数组, 实际长度 %d", len(specs))
	}
}

// TestReplaceOverridesExisting 验证 Replace 用同名工具覆盖已注册实例。
func TestReplaceOverridesExisting(t *testing.T) {
	r := NewRegistry()
	first := newFakeTool("dup")
	first.execFn = func(ctx context.Context, input json.RawMessage) (string, error) {
		return "first", nil
	}
	if err := r.Register(first); err != nil {
		t.Fatalf("首次注册失败: %v", err)
	}

	second := newFakeTool("dup")
	second.execFn = func(ctx context.Context, input json.RawMessage) (string, error) {
		return "second", nil
	}
	if err := r.Replace(second); err != nil {
		t.Fatalf("Replace 失败: %v", err)
	}

	got, ok := r.Get("dup")
	if !ok {
		t.Fatal("Replace 后 Get 应能取到 dup")
	}
	out, err := got.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if out != "second" {
		t.Errorf("Replace 后应拿到新实例, 实际输出 %q", out)
	}
	if r.Count() != 1 {
		t.Errorf("Replace 不应改变 Count, 实际 %d", r.Count())
	}
}

// TestReplaceOnEmptyRegistry 验证 Replace 在空 Registry 上等价于 Register。
func TestReplaceOnEmptyRegistry(t *testing.T) {
	r := NewRegistry()
	if err := r.Replace(newFakeTool("alpha")); err != nil {
		t.Fatalf("空 Registry Replace 失败: %v", err)
	}
	if _, ok := r.Get("alpha"); !ok {
		t.Error("Replace 后应能 Get 到 alpha")
	}
}

// itoa 避免 strconv 引入，测试代码保持极简。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	negative := false
	if i < 0 {
		negative = true
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if negative {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
