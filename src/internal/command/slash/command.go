package slash

import (
	"context"
	"fmt"
	"sync"

	"github.com/gorilla/websocket"
)

// SlashCommand 是所有 slash 命令必须实现的统一接口。
// 风格与 tool.Tool 对齐：元数据 + Execute 二段式。
//
// 上层（web 层 Handler / 前端 UI）只通过此接口与命令交互；
// 命令的注册、查找、执行均基于 Name()，因此 Name 必须全局唯一（含 / 前缀）。
//
// 命令实现应满足：
//   - Name() 返回稳定的命令标识（含 / 前缀，如 "/new"），全局唯一
//   - Description() 给前端 UI 展示，应清晰说明命令用途
//   - NeedsArg() true 时，前端选中后会把命令名 + arg 模板补全到输入框，
//     用户填完 arg 后按 Enter 提交
//   - ArgHint() 参数占位提示（如 "<id>"），仅在 NeedsArg()=true 时展示给用户
//   - Category() 分类标识（"session" / "context" / "debug" / "client" 等），
//     前端可据此分组渲染或本地处理（如 "client" 类不走 WS）
//   - Execute() 必须响应 ctx.Done()，被取消时尽快返回
type SlashCommand interface {
	// Name 返回命令名（含 / 前缀），全局唯一。
	Name() string
	// Description 返回命令描述，会被发给前端 UI 帮助用户理解命令用途。
	Description() string
	// NeedsArg 表示命令是否需要用户补充参数。
	//   - false：选中后直接执行（前端按对应 MsgType 发送）
	//   - true：选中后补全到输入框（用户填完参数按 Enter 后由前端发送）
	NeedsArg() bool
	// ArgHint 返回参数占位提示（如 "<id>"），仅在 NeedsArg()=true 时展示给用户。
	ArgHint() string
	// Category 返回命令分类标识（"session" / "context" / "debug" / "client" 等）。
	// "client" 类命令不通过 Execute 发起 WS 调用，由前端识别后走本地逻辑。
	Category() string
	// Execute 执行命令。
	//
	// 参数：
	//   - ctx: 支持通过 cancel 终止命令执行（用户中止 / 超时 / 进程退出）
	//   - conn: 触发该命令的 WebSocket 连接，用于向当前用户回推消息
	//   - arg: 命令参数；NeedsArg()=false 时为空字符串
	//
	// 返回值：
	//   - err: 执行失败；非 nil 时由 web 层把错误回推给前端 UI
	//
	// Execute 必须：
	//   - 响应 ctx.Done()，被取消时返回 ctx.Err()
	//   - 不向 panic 逃逸；如确需捕获内部 panic 后转为 error
	Execute(ctx context.Context, conn *websocket.Conn, arg string) error
}

// Registry 是 slash 命令的注册中心。
// 内部以 map 维护 Name -> SlashCommand 的映射，同时保留注册顺序（供前端稳定渲染）。
// 所有读写均通过 RWMutex 保护。
type Registry struct {
	mu       sync.RWMutex
	commands map[string]SlashCommand
	// order 保存注册顺序；List 时按此顺序返回，便于前端 UI 稳定展示。
	order []string
	// onChange 变化回调集合；Register 后会同步触发回调（持锁外）。
	onChange []func()
}

// NewRegistry 构造一个空的 Registry。
func NewRegistry() *Registry {
	return &Registry{
		commands: make(map[string]SlashCommand),
	}
}

// ErrCommandAlreadyRegistered 在重复注册同名命令时返回。
type ErrCommandAlreadyRegistered struct {
	Name string
}

// Error 实现 error 接口。
func (e *ErrCommandAlreadyRegistered) Error() string {
	return fmt.Sprintf("slash 命令已注册: %s", e.Name)
}

// Register 注册一个 slash 命令。
// Name 重复时返回 *ErrCommandAlreadyRegistered；注册成功后会同步触发 onChange 回调。
// 失败时（参数无效 / 重复注册）不会触发回调。
func (r *Registry) Register(cmd SlashCommand) error {
	if cmd == nil {
		return fmt.Errorf("注册命令不能为 nil")
	}
	name := cmd.Name()
	if name == "" {
		return fmt.Errorf("注册命令 Name 不能为空")
	}
	r.mu.Lock()
	if _, exists := r.commands[name]; exists {
		r.mu.Unlock()
		return &ErrCommandAlreadyRegistered{Name: name}
	}
	r.commands[name] = cmd
	r.order = append(r.order, name)
	// 复制回调列表后释放锁再调用，避免回调内再次 Register 时死锁
	// 或长时间持锁阻塞 List / Get 等读路径。
	callbacks := append([]func(){}, r.onChange...)
	r.mu.Unlock()
	for _, fn := range callbacks {
		fn()
	}
	return nil
}

// Get 按名称查找命令。未找到时返回 (nil, false)。
func (r *Registry) Get(name string) (SlashCommand, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cmd, ok := r.commands[name]
	return cmd, ok
}

// List 返回所有已注册命令的列表（按注册顺序）。
// 返回的列表是快照，调用方修改不影响 Registry 内部状态。
func (r *Registry) List() []SlashCommand {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SlashCommand, 0, len(r.order))
	for _, name := range r.order {
		if cmd, ok := r.commands[name]; ok {
			out = append(out, cmd)
		}
	}
	return out
}

// OnChange 注册一个变化回调，注册命令时会同步触发。
//
// 用途：web 层注册一个把当前最新命令清单通过 WS 推给所有连接的回调，
// 实现"slash_commands_updated"事件（Step 10 Skill 动态注册时会被实际触发）。
//
// 约束：
//   - 回调在 Register 调用 goroutine 中同步执行；不应在回调内做耗时操作
//   - 回调不应持锁回调到 Registry（避免死锁）
//   - 同一回调多次注册会触发多次；为 nil 时静默忽略
func (r *Registry) OnChange(fn func()) {
	if fn == nil {
		return
	}
	r.mu.Lock()
	r.onChange = append(r.onChange, fn)
	r.mu.Unlock()
}

// Count 返回已注册命令的数量。
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.commands)
}
