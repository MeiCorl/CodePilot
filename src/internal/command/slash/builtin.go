package slash

// 本文件实现 6 条内置 slash 命令（/new、/sessions、/resume、/clear、/compact、/dump）。
//
// 设计要点：
//  1. 每条命令实现 slash.SlashCommand 接口，并通过 RegisterBuiltin 一站式注册到 Registry。
//  2. 命令实现位于 builtin 子包（与 command.go 同包），可以直接引用 *web.Handler。
//     这是 spec 中的"特殊包边界"——command/slash 主包不 import web（避免 Step 10
//     Skill 注册命令时被 web 反向依赖），但 builtin.go 作为命令实现层与 web 处于
//     同一物理目录（slice-level 隔离），允许持有 *web.Handler 引用。
//  3. Execute 方法**直接复用** web.Handler 中现有 handleXxx 函数体（handleNewSession
//     / handleClearSession / handleResumeSession / handleCompact / handleDump），
//     业务逻辑 0 改动。/sessions 命令 Category="client"，Execute 返回 nil，由
//     前端识别后走本地 openSessionsTable()。
//  4. /resume 命令 NeedsArg=true + ArgHint="<id>"；Execute 接收 arg 参数后构造
//     ResumeSessionPayload{ID: arg} 再委托 handleResumeSession。
//  5. 每个 struct 仅持有 *web.Handler 引用，依赖最小化；handler 函数签名稳定。

import (
	"context"

	"github.com/gorilla/websocket"

	"github.com/MeiCorl/CodePilot/src/internal/interaction/web"
)

// ---- 命令分类常量 ----
//
// Category 字段是前端分类渲染与本地处理的依据；命名保持小写语义化字符串。
const (
	CategorySession = "session" // 会话管理类命令（/new、/resume、/clear）
	CategoryContext = "context" // 上下文管理类命令（/compact）
	CategoryDebug   = "debug"   // 调试导出类命令（/dump）
	CategoryClient  = "client"  // 纯前端本地命令（/sessions），不通过 Execute 发起 WS 调用
)

// ---- 命令元数据常量 ----
//
// 命令名统一含 / 前缀；Description 为前端候选下拉中展示的中文说明。
const (
	nameNew      = "/new"
	descNew      = "新建一个会话"
	nameSessions = "/sessions"
	descSessions = "查看历史会话列表"
	nameResume   = "/resume"
	descResume   = "恢复指定 ID 的会话（需后接 ID 前缀）"
	nameClear    = "/clear"
	descClear    = "清空当前会话上下文"
	nameCompact  = "/compact"
	descCompact  = "手动压缩上下文（历史摘要化）"
	nameDump     = "/dump"
	descDump     = "导出当前会话上下文与 System Prompt 到本地文件（dump.json/dump.md）"

	// /resume 的参数占位提示。
	resumeArgHint = "<id>"
)

// ---- /new ----

// newCmd 实现 /new 命令，委托 handler.handleNewSession。
type newCmd struct {
	h *web.Handler
}

// Name 返回命令名（含 / 前缀）。
func (c *newCmd) Name() string { return nameNew }

// Description 返回命令描述。
func (c *newCmd) Description() string { return descNew }

// NeedsArg 表示命令是否需要用户补充参数。/new 无需参数。
func (c *newCmd) NeedsArg() bool { return false }

// ArgHint 参数占位提示；NeedsArg=false 时返回空字符串。
func (c *newCmd) ArgHint() string { return "" }

// Category 返回命令分类标识。会话管理类。
func (c *newCmd) Category() string { return CategorySession }

// Execute 委托 handler.handleNewSession，传入空 Message（payload 不需要字段）。
// 业务逻辑完全复用现有 handleNewSession 函数体，0 改动。
func (c *newCmd) Execute(ctx context.Context, conn *websocket.Conn, arg string) error {
	// 响应 ctx 取消；handleNewSession 内部流程天然具备锁保护，无需额外等待。
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.h.HandleNewSessionForSlash(conn)
}

// ---- /sessions ----

// sessionsCmd 实现 /sessions 命令；Category=client，前端识别后走本地 openSessionsTable()。
//
// 为什么 Execute 返回 nil：spec.md 明确规定 Category="client" 类命令**不通过 Execute
// 发起 WS 调用**，由前端识别 Category 后走本地逻辑（如打开会话表格视图）。
// 因此 Execute 是占位实现，确保接口满足即可，不执行任何后端业务。
type sessionsCmd struct {
	h *web.Handler // 保留 *web.Handler 引用以满足统一构造模式，但 Execute 不使用
}

// Name 返回命令名（含 / 前缀）。
func (c *sessionsCmd) Name() string { return nameSessions }

// Description 返回命令描述。
func (c *sessionsCmd) Description() string { return descSessions }

// NeedsArg 表示命令是否需要用户补充参数。/sessions 无需参数。
func (c *sessionsCmd) NeedsArg() bool { return false }

// ArgHint 参数占位提示；NeedsArg=false 时返回空字符串。
func (c *sessionsCmd) ArgHint() string { return "" }

// Category 返回命令分类标识。"client" 类不通过 Execute 发起 WS 调用。
func (c *sessionsCmd) Category() string { return CategoryClient }

// Execute 占位实现：始终返回 nil，由前端识别 Category 后走本地逻辑。
func (c *sessionsCmd) Execute(ctx context.Context, conn *websocket.Conn, arg string) error {
	_ = ctx
	_ = conn
	_ = arg
	return nil
}

// ---- /resume ----

// resumeCmd 实现 /resume 命令，NeedsArg=true，ArgHint="<id>"。
//
// Execute 把 arg 作为 ResumeSessionPayload.ID 包装后委托 handler.handleResumeSession。
// 若 arg 为空字符串则直接返回 stream_error(empty_id) 错误，与现有 handleResumeSession 行为一致。
type resumeCmd struct {
	h *web.Handler
}

// Name 返回命令名（含 / 前缀）。
func (c *resumeCmd) Name() string { return nameResume }

// Description 返回命令描述。
func (c *resumeCmd) Description() string { return descResume }

// NeedsArg 表示命令是否需要用户补充参数。/resume 需 ID 前缀。
func (c *resumeCmd) NeedsArg() bool { return true }

// ArgHint 参数占位提示。<id> 提示用户输入会话 ID。
func (c *resumeCmd) ArgHint() string { return resumeArgHint }

// Category 返回命令分类标识。会话管理类。
func (c *resumeCmd) Category() string { return CategorySession }

// Execute 把 arg 作为 ID 委托 handler.handleResumeSession。
// arg 为空时返回 empty_id 错误（与现有 handleResumeSession 的语义对齐）。
func (c *resumeCmd) Execute(ctx context.Context, conn *websocket.Conn, arg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.h.HandleResumeSessionForSlash(conn, arg)
}

// ---- /clear ----

// clearCmd 实现 /clear 命令，委托 handler.handleClearSession。
type clearCmd struct {
	h *web.Handler
}

// Name 返回命令名（含 / 前缀）。
func (c *clearCmd) Name() string { return nameClear }

// Description 返回命令描述。
func (c *clearCmd) Description() string { return descClear }

// NeedsArg 表示命令是否需要用户补充参数。/clear 无需参数。
func (c *clearCmd) NeedsArg() bool { return false }

// ArgHint 参数占位提示；NeedsArg=false 时返回空字符串。
func (c *clearCmd) ArgHint() string { return "" }

// Category 返回命令分类标识。会话管理类。
func (c *clearCmd) Category() string { return CategorySession }

// Execute 委托 handler.handleClearSession。
func (c *clearCmd) Execute(ctx context.Context, conn *websocket.Conn, arg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.h.HandleClearSessionForSlash(conn)
}

// ---- /compact ----

// compactCmd 实现 /compact 命令，委托 handler.handleCompact。
type compactCmd struct {
	h *web.Handler
}

// Name 返回命令名（含 / 前缀）。
func (c *compactCmd) Name() string { return nameCompact }

// Description 返回命令描述。
func (c *compactCmd) Description() string { return descCompact }

// NeedsArg 表示命令是否需要用户补充参数。/compact 无需参数。
func (c *compactCmd) NeedsArg() bool { return false }

// ArgHint 参数占位提示；NeedsArg=false 时返回空字符串。
func (c *compactCmd) ArgHint() string { return "" }

// Category 返回命令分类标识。上下文管理类。
func (c *compactCmd) Category() string { return CategoryContext }

// Execute 委托 handler.handleCompact。
func (c *compactCmd) Execute(ctx context.Context, conn *websocket.Conn, arg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.h.HandleCompactForSlash(conn)
}

// ---- /dump ----

// dumpCmd 实现 /dump 命令，委托 handler.handleDump。
type dumpCmd struct {
	h *web.Handler
}

// Name 返回命令名（含 / 前缀）。
func (c *dumpCmd) Name() string { return nameDump }

// Description 返回命令描述。
func (c *dumpCmd) Description() string { return descDump }

// NeedsArg 表示命令是否需要用户补充参数。/dump 无需参数。
func (c *dumpCmd) NeedsArg() bool { return false }

// ArgHint 参数占位提示；NeedsArg=false 时返回空字符串。
func (c *dumpCmd) ArgHint() string { return "" }

// Category 返回命令分类标识。调试导出类。
func (c *dumpCmd) Category() string { return CategoryDebug }

// Execute 委托 handler.handleDump。
func (c *dumpCmd) Execute(ctx context.Context, conn *websocket.Conn, arg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.h.HandleDumpForSlash(conn)
}

// ---- 一站式注册 ----

// RegisterBuiltin 把 6 条内置命令一次性注册到给定 Registry。
//
// 调用方式（main.go 顶层装配）：
//
//	slashRegistry := slash.NewRegistry()
//	if err := slash.RegisterBuiltin(slashRegistry, h); err != nil {
//	    logger.Fatal("注册 slash 内置命令失败", zap.Error(err))
//	}
//	h.SetSlashRegistry(slashRegistry)
//
// 参数：
//   - r：Registry 指针；为 nil 时返回 error（防止 nil panic）
//   - h：web.Handler 指针；为 nil 时返回 error
//
// 返回值：
//   - 任一命令注册失败时返回 error；已成功注册的会保留（不撤销），由调用方决定
//     是否继续。重复调用 RegisterBuiltin 会因名称冲突返回 error，Registry 内部
//     去重，不会重复注册同一命令。
func RegisterBuiltin(r *Registry, h *web.Handler) error {
	if r == nil {
		return ErrNilRegistry
	}
	if h == nil {
		return ErrNilHandler
	}

	// 一次性注册 6 条内置命令。失败立即返回，保留已注册的（Registry 内部 map
	// 维护，可由调用方决定后续是跳过还是 panic）。
	commands := []SlashCommand{
		&newCmd{h: h},
		&sessionsCmd{h: h},
		&resumeCmd{h: h},
		&clearCmd{h: h},
		&compactCmd{h: h},
		&dumpCmd{h: h},
	}
	for _, cmd := range commands {
		if err := r.Register(cmd); err != nil {
			return err
		}
	}
	return nil
}

// ---- 错误定义 ----

// RegisterBuiltin 入参校验错误。
var (
	// ErrNilRegistry 在 RegisterBuiltin 传入 nil Registry 时返回。
	ErrNilRegistry = &builtinError{msg: "RegisterBuiltin: Registry 不能为 nil"}
	// ErrNilHandler 在 RegisterBuiltin 传入 nil *web.Handler 时返回。
	ErrNilHandler = &builtinError{msg: "RegisterBuiltin: *web.Handler 不能为 nil"}
)

// builtinError 是 RegisterBuiltin 参数校验错误的内部类型，避免与 Registry 的
// ErrCommandAlreadyRegistered 命名混淆。
type builtinError struct {
	msg string
}

// Error 实现 error 接口。
func (e *builtinError) Error() string { return e.msg }