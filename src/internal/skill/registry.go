package skill

import (
	"fmt"
	"sync"
)

// Registry 是 Skill 的内存合并注册表,负责把多档扫描结果按 spec §A.4 规则合并:
//
//   - 三档优先级(数字越小越高):project(1) > user(2) > builtin(3);
//   - 项目级同名 → 静默覆盖用户级同名(用户级不出现在 List);覆盖不返回 error;
//   - 同级别同名(如两个项目级同名)→ 立即返回 *ErrSkillConflict,registry 状态不变;
//   - 加载顺序约束:由 Scanner 保证「内置级 → 用户级 → 项目级」按顺序调用 Register,
//     保证项目级后到可触发覆盖路径。
//
// 并发模型:所有读写均通过 RWMutex 保护。Register 是写路径,Get/List/ListBySource/
// Count 是读路径;use_skill 工具的运行期调用全部走读路径,与启动期 Register 互不阻塞。
//
// 字段说明:
//
//	- mu:读写锁,保护 byName 与 order 的原子访问;
//	- byName:按 Skill.Name 索引的 map,值指针为最后一次 Register 成功的 *Skill;
//	- order:Skill.Name 的注册顺序,List/ListBySource 按此顺序返回(同一 Source 内也
//	  遵循注册顺序),供 /skills 模态框与 SP 索引稳定展示。
type Registry struct {
	mu     sync.RWMutex
	byName map[string]*Skill
	order  []string
}

// NewRegistry 构造一个空的 Registry。
//
// [Why] 必须显式构造而非简单字面量:byName 需要 make 初始化,order 需要 nil 切片;
// 同时保证后续 mu 的零值可用(sync.RWMutex 零值即可直接使用)。
func NewRegistry() *Registry {
	return &Registry{
		byName: make(map[string]*Skill),
	}
}

// ErrSkillConflict 在同级别同名冲突时返回(两个项目级 / 两个用户级 / 两个内置级同名)。
//
// 字段:
//
//	- Name:冲突的 Skill 名(等于两次 Register 的 Skill.Name);
//	- ExistingSource:先注册到 Registry 中的那个 Skill 的 Source(项目级 / 用户级 / 内置级),
//	  供调用方判断是哪一档冲突;
//
// [Why] 单独定义结构体而非通用 ErrConflict:spec §A.4 明确要求冲突时携带 Name 与
// ExistingSource 两个字段供 main.go 启动期打印;使用通用 error 会丢失 Source 信息。
type ErrSkillConflict struct {
	Name           string
	ExistingSource Source
}

// Error 实现 error 接口。
//
// 错误格式:"skill name conflict: <name> (existing source: <source>)",
// 便于 main.go 直接 fmt.Fprintln(os.Stderr, err) 打印给用户。
func (e *ErrSkillConflict) Error() string {
	return fmt.Sprintf("skill name conflict: %s (existing source: %s)",
		e.Name, e.ExistingSource.String())
}

// Register 把 Skill 注册到 Registry。
//
// 冲突规则(spec §A.4):
//   - 已有同名 Skill 且 ExistingSource 优先级 ≥ 待注册 Skill 的 Source
//     (数值更小 = 优先级更高,即 existing <= incoming 时):
//     -- existing == incoming(同级别):返回 *ErrSkillConflict;
//     -- existing 优先级更高(数值更小,如 existing=project/1 vs incoming=user/2
//        是非法方向,因为我们约定的加载顺序是「内置 → 用户 → 项目」,
//        不会让低优先级后到覆盖高优先级;若发生则同样视为冲突返回 error);
//     -- incoming 优先级更高(数值更小,如 existing=user/2 vs incoming=project/1):
//        silent skip(用户级同名不出现在 List 中,project 替换 byName 与 order 中
//        已存在的同名条目,Return nil);
//
//   - 未发现同名:正常注册,append 到 order,记录到 byName。
//
// 入参约束:
//
//	- s 为 nil → 返回 error,不修改 Registry;
//	- s.Name 为空 → 返回 error(spec §A.3 要求 name 必填,但防御性校验仍保留);
//
// 返回值:
//
//	- nil:注册成功 或 项目级 silent skip 覆盖用户级;
//	- *ErrSkillConflict:同级别同名冲突,Registry 内部状态保持不变;
//	- 非 nil 普通 error:参数非法(ni / 空 Name)。
//
// 并发安全:写路径持写锁,Register 期间 Get/List 等读路径阻塞。覆盖(silent skip)路径
// 在锁内完成 byName 与 order 的同步更新。
func (r *Registry) Register(s *Skill) error {
	if r == nil {
		return fmt.Errorf("skill.Registry: Register called on nil Registry")
	}
	if s == nil {
		return fmt.Errorf("skill.Registry: Register called with nil Skill")
	}
	if s.Name == "" {
		return fmt.Errorf("skill.Registry: Register Skill.Name must not be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	existing, exists := r.byName[s.Name]
	if exists {
		// 同级别同名 → 立即返回冲突错误,Registry 状态不变
		if existing.Source == s.Source {
			return &ErrSkillConflict{
				Name:           s.Name,
				ExistingSource: existing.Source,
			}
		}
		// 优先级比较:数值越小优先级越高(project=1 > user=2 > builtin=3)
		//   - existing 数值更小 → 已注册的高优先级 → 拒绝新注册,返回冲突错误
		//   - s 数值更小       → 新注册的优先级更高 → silent skip(覆盖已有)
		if existing.Source < s.Source {
			// 已注册的是高优先级(如 project),新来的是低优先级(如 user)
			// 这不应发生在正常加载顺序中(项目级最后到),防御性返回冲突错误
			return &ErrSkillConflict{
				Name:           s.Name,
				ExistingSource: existing.Source,
			}
		}
		// 正常覆盖路径:已注册的是低优先级(如 user),新来的是高优先级(如 project)
		// → silent skip,更新 byName 与 order 中的条目
		r.byName[s.Name] = s
		// order 中已有 s.Name(由 Register 首次注册时 append),无需调整顺序,
		// 因为「先到」的低优先级条目保持其位置,「后到」的高优先级不破坏稳定性。
		// [Why] 不重排 order:List/ListBySource 按 order 渲染,若把 project 移到 user
		// 之后会导致 SP 索引顺序漂移;保留 user 的位置只是让 user 的 Name 仍
		// 占一个 order 槽但 byName 已指向 project。语义上等价于「user 的 Name
		// 被 project 顶替,但不影响其他 Skill 的展示顺序」。
		return nil
	}

	// 未发现同名:正常注册
	r.byName[s.Name] = s
	r.order = append(r.order, s.Name)
	return nil
}

// Get 按 Name 查找已注册的 Skill。
//
// 未找到时返回 (nil, false),调用方应区分 false 与 nil Skill 两种情况。
// 读路径,持读锁。
func (r *Registry) Get(name string) (*Skill, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byName[name]
	return s, ok
}

// List 按注册顺序返回所有已注册 Skill 的快照(项目级 → 用户级 → 内置级,
// 同 Source 内遵循首次注册顺序)。
//
// 返回的切片是拷贝,调用方修改不影响 Registry 内部状态。
// 读路径,持读锁。
func (r *Registry) List() []*Skill {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Skill, 0, len(r.order))
	for _, name := range r.order {
		if s, ok := r.byName[name]; ok && s != nil {
			out = append(out, s)
		}
	}
	return out
}

// ListBySource 按 Source 级别分组返回 Skill 列表。
//
// 同 Source 内仍遵循注册顺序(由 Registry.order 决定),便于 /skills 模态框
// 按三档稳定展示;SkillsIndexSource 也走此方法分组拼 SP 索引段。
//
// 入参 src 取值:
//
//	- SourceProject:仅项目级;
//	- SourceUser:仅用户级;
//	- SourceBuiltin:仅内置级;
//	- 其他(Source(0)/Source(99)等):返回空切片,不报错。
//
// 读路径,持读锁。
func (r *Registry) ListBySource(src Source) []*Skill {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Skill, 0)
	for _, name := range r.order {
		s, ok := r.byName[name]
		if !ok || s == nil {
			continue
		}
		if s.Source != src {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Count 返回当前 Registry 中已注册 Skill 的数量(已覆盖的低优先级同名条目
// 也仍占一个 order 槽,本计数等于 Registry.order 长度)。
//
// 读路径,持读锁。
func (r *Registry) Count() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.order)
}
