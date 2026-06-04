package tool

import (
	"fmt"
	"sort"
	"sync"
)

// Registry 是工具的全局注册中心。
// 内部以 map 维护 Name -> Tool 的映射，所有读写均通过 RWMutex 保护。
// Registry 实例可独立创建（便于测试隔离），同时通过 DefaultRegistry 提供全局单例。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry 构造一个空的 Registry。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// ErrToolAlreadyRegistered 在重复注册同名工具时返回。
type ErrToolAlreadyRegistered struct {
	Name string
}

// Error 实现 error 接口。
func (e *ErrToolAlreadyRegistered) Error() string {
	return fmt.Sprintf("工具已注册: %s", e.Name)
}

// Register 注册一个工具。Name 重复时返回 *ErrToolAlreadyRegistered。
func (r *Registry) Register(tool Tool) error {
	if tool == nil {
		return fmt.Errorf("注册工具不能为 nil")
	}
	name := tool.Name()
	if name == "" {
		return fmt.Errorf("注册工具 Name 不能为空")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; exists {
		return &ErrToolAlreadyRegistered{Name: name}
	}
	r.tools[name] = tool
	return nil
}

// MustRegister 是 Register 的 panic 版本，用于 init() 中的批量注册。
// 注册失败直接 panic，因为 init 期注册错误是配置/代码 bug，应在启动期暴露。
func (r *Registry) MustRegister(tools ...Tool) {
	for _, t := range tools {
		if err := r.Register(t); err != nil {
			panic(err)
		}
	}
}

// Replace 用同名工具覆盖 Registry 中已注册的工具实例。
//
// 语义：若目标 Name 未注册，等价于 Register；若已注册，用新实例替换旧实例。
// 适用于"配置加载后用 cfg 中的工作目录/超时重新构造工具并覆盖 init() 时的默认实例"
// 这类场景；不要在并发调用 Execute 的过程中调用 Replace（会替换正在使用的实例）。
func (r *Registry) Replace(tool Tool) error {
	if tool == nil {
		return fmt.Errorf("注册工具不能为 nil")
	}
	name := tool.Name()
	if name == "" {
		return fmt.Errorf("注册工具 Name 不能为空")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[name] = tool
	return nil
}

// MustReplace 是 Replace 的 panic 版本，用于启动期根据配置覆盖默认工具。
func (r *Registry) MustReplace(tools ...Tool) {
	for _, t := range tools {
		if err := r.Replace(t); err != nil {
			panic(err)
		}
	}
}

// Get 按名称查找工具。未找到时返回 (nil, false)。
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List 返回所有已注册工具的列表（按 Name 排序，便于展示稳定）。
// 返回的列表是快照，调用方修改不影响 Registry 内部状态。
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// EnabledNames 返回按配置启用的工具名列表。
// enabled 为空时视为全部启用；否则按顺序返回 enabled 中实际存在的工具名（去重）。
// 返回的列表是稳定的（按 Name 排序），便于 LLM 端观察到一致的描述顺序。
func (r *Registry) EnabledNames(enabled []string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]struct{}, len(r.tools))
	var out []string
	if len(enabled) == 0 {
		// 全开模式：返回所有工具
		for name := range r.tools {
			out = append(out, name)
		}
	} else {
		// 白名单模式：仅返回在白名单中且已注册的工具
		for _, name := range enabled {
			if _, exists := r.tools[name]; !exists {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Count 返回已注册工具的数量。
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// ---- 全局默认实例 ----

var (
	defaultRegistry     *Registry
	defaultRegistryOnce sync.Once
)

// DefaultRegistry 返回全局默认 Registry 单例。
// 首次调用时初始化；后续调用返回同一实例。
// 推荐在 main.go 通过 blank import 触发 builtin 包的 init() 后使用。
func DefaultRegistry() *Registry {
	defaultRegistryOnce.Do(func() {
		defaultRegistry = NewRegistry()
	})
	return defaultRegistry
}
