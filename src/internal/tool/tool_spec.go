package tool

import "encoding/json"

// ToolSpec 是工具的协议层描述，剥离了执行逻辑与权限分级。
//
// 设计目的：解决 llm 包 ↔ tool 包的循环依赖——
//
//	llm 包需要知道工具的 Name/Description/InputSchema 才能让 LLM 看到工具定义，
//	但不应反向依赖 Tool 接口本身（Permission/Execute 属于运行期能力）。
//
// 依赖方向：
//
//	tool 包 -> 自包含（仅 encoding/json）
//	llm 包 -> tool 包（通过 []ToolSpec 传入 Provider.StreamChat）
//
// ToolSpec 不携带 Permission/Execute，因此 llm 包无法误调执行，
// 真正的工具调用必须由 conversation manager 通过 ToolHandler 走 Registry。
type ToolSpec struct {
	// Name 为工具的 snake_case 标识，与 Tool.Name() 一致
	Name string
	// Description 为工具的 LLM 可读描述
	Description string
	// InputSchema 为工具输入参数的 JSON Schema（json.RawMessage，避免二次解析）
	InputSchema json.RawMessage
}

// ToSpecs 返回按 enabled 过滤后的 ToolSpec 列表。
//
// enabled 为空时返回所有已注册工具的描述；
// 否则仅返回 enabled 中实际存在的工具（去重、按 Name 排序）。
//
// 返回的列表是稳定有序的，便于 LLM 端观察到一致的描述顺序。
// 内部复用 EnabledNames 保证与 List() / ToSpecs() 过滤语义一致。
func (r *Registry) ToSpecs(enabled []string) []ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := r.EnabledNames(enabled)
	out := make([]ToolSpec, 0, len(names))
	for _, name := range names {
		t, ok := r.tools[name]
		if !ok {
			continue
		}
		out = append(out, ToolSpec{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return out
}
