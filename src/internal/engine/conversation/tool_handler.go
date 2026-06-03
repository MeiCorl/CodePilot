package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/llm"
	"github.com/MeiCorl/CodePilot/src/tool"
)

// ToolExecutionEvent 描述单次工具执行的完整生命周期事件。
//
// 工具开始执行前 OnStart 回调会收到 StatusRunning 事件；
// 工具执行结束（无论成功、失败、超时、被取消）后 OnEnd 回调
// 会收到 StatusCompleted / StatusError / StatusAborted 事件。
//
// Input 与 Output 在开始时与结束时分别填充；其他字段（DurationMs
// 等）仅在结束时有效。
type ToolExecutionEvent struct {
	// ToolUseID 关联到对应的 ToolUseBlock.ID
	ToolUseID string
	// Name 为被执行的工具名
	Name string
	// Input 为 LLM 传入的原始参数
	Input json.RawMessage
	// Output 为工具执行结果文本（成功时）；失败时为空
	Output string
	// IsError 表示工具执行是否失败
	IsError bool
	// ErrorMsg 失败时的错误描述（成功时为空）
	ErrorMsg string
	// Status 标识事件对应的执行阶段：running / completed / error / aborted
	Status string
	// DurationMs 为执行耗时（毫秒），仅结束事件上有意义
	DurationMs int64
	// StartedAt 为执行开始时间
	StartedAt time.Time
}

// 工具执行状态枚举，事件 Status 字段使用。
const (
	ToolEventStatusRunning   = "running"
	ToolEventStatusCompleted = "completed"
	ToolEventStatusError     = "error"
	ToolEventStatusAborted   = "aborted"
)

// ErrToolNotFound 在 Registry 中查不到工具名时返回。
type ErrToolNotFound struct {
	Name string
}

func (e *ErrToolNotFound) Error() string {
	return fmt.Sprintf("工具未注册: %s", e.Name)
}

// ToolHandler 是单工具执行与审计入口。
//
// 持有 Registry 引用与执行超时配置，对 LLM 发出的 tool_use 做
//「查 Registry → 调 Execute → 封装为 ToolResultBlock」处理。
// 同时通过 OnStart/OnEnd 回调把执行事件外推，供上层（WebUI）
// 推送 tool_call_start / tool_call_end 消息。
//
// 工具自身的安全兜底（路径沙箱、Bash 黑名单）由 builtin 包
// 内部完成；ToolHandler 不再重复校验，**也不提供任何方式关闭**。
type ToolHandler struct {
	// registry 用于按 Name 查找 Tool 实例；为 nil 时所有工具调用都报 ErrToolNotFound
	registry *tool.Registry
	// timeout 为单次工具执行的最大耗时；<=0 视为无超时（不推荐）
	timeout time.Duration
	// workdir 为路径沙箱的工作目录；为空时取当前进程 cwd
	workdir string
	// onStart 工具开始执行时回调，可为 nil
	onStart func(ToolExecutionEvent)
	// onEnd 工具执行结束（成功/失败/超时/取消）时回调，可为 nil
	onEnd func(ToolExecutionEvent)

	// mu 保护回调函数的并发读写；回调可在 goroutine 中注册，但 Execute 内部
	// 通过 mu 拷贝后再调用，避免回调中修改自己影响正在执行的 Execute
	mu sync.RWMutex
}

// NewToolHandler 构造一个 ToolHandler。
//
// 参数：
//   - registry: 工具注册中心，nil 时所有工具调用都返回 ErrToolNotFound
//   - timeout: 单次工具执行超时；<=0 视为不超时（**仅用于测试，生产环境必传**）
//   - workdir: 路径沙箱工作目录；为空时取进程 cwd
func NewToolHandler(registry *tool.Registry, timeout time.Duration, workdir string) *ToolHandler {
	return &ToolHandler{
		registry: registry,
		timeout:  timeout,
		workdir:  workdir,
	}
}

// SetOnStart 注册工具开始事件回调。传入 nil 表示清空。
//
// 回调可能在 Execute 内部被同步调用，注意不要在回调里执行阻塞操作。
func (h *ToolHandler) SetOnStart(fn func(ToolExecutionEvent)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onStart = fn
}

// SetOnEnd 注册工具结束事件回调。传入 nil 表示清空。
//
// 回调可能在 Execute 内部被同步调用，注意不要在回调里执行阻塞操作。
func (h *ToolHandler) SetOnEnd(fn func(ToolExecutionEvent)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onEnd = fn
}

// Execute 执行 LLM 发出的 tool_use 并返回对应的 ToolResultBlock。
//
// 执行流程：
//  1. 构造 ToolExecutionEvent 起始态并通过 OnStart 回调外推
//  2. 若配置了 timeout，用 context.WithTimeout 包装入参 ctx
//  3. 在 Registry 中按 toolUse.Name 查找工具，未找到时封装为 is_error=true
//  4. 调 tool.Execute；捕获 panic 并转为 error（不向上层逃逸）
//  5. 构造 ToolResultBlock 返回，同时构造结束事件通过 OnEnd 回调外推
//
// 写入 history 不在 ToolHandler 职责内——返回的 ToolResultBlock
// 由调用方（manager.RunTurn）追加到消息历史。
func (h *ToolHandler) Execute(ctx context.Context, toolUse llm.ToolUseBlock) llm.ToolResultBlock {
	startedAt := time.Now()
	event := ToolExecutionEvent{
		ToolUseID: toolUse.ID,
		Name:      toolUse.Name,
		Input:     toolUse.Input,
		Status:    ToolEventStatusRunning,
		StartedAt: startedAt,
	}
	h.fireStart(event)

	output, execErr := h.doExecute(ctx, toolUse)

	duration := time.Since(startedAt).Milliseconds()
	result := llm.ToolResultBlock{
		ToolUseID: toolUse.ID,
		Content:   output,
		IsError:   execErr != nil,
	}
	if execErr != nil {
		result.Content = execErr.Error()
		event.IsError = true
		event.ErrorMsg = execErr.Error()
		event.Status = h.classifyStatus(execErr, ctx)
	} else {
		event.Output = output
		event.Status = ToolEventStatusCompleted
	}
	event.DurationMs = duration
	h.fireEnd(event)
	return result
}

// doExecute 是 Execute 的实际执行体，把工具查找、超时包装、panic 恢复集中处理。
func (h *ToolHandler) doExecute(ctx context.Context, toolUse llm.ToolUseBlock) (string, error) {
	execCtx := ctx
	if h.timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}

	t, ok := h.lookup(toolUse.Name)
	if !ok {
		return "", &ErrToolNotFound{Name: toolUse.Name}
	}

	// 权限分级仅做信息记录，强制拦截由工具自身 + safety 包负责。
	logger.Debug("工具开始执行",
		zap.String("tool", t.Name()),
		zap.String("permission", t.Permission().String()),
		zap.String("tool_use_id", toolUse.ID),
	)

	// 用 func() + recover 兜住工具内部 panic，转为 error 返回。
	var (
		output string
		err    error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("工具执行 panic: %v\n%s", r, debug.Stack())
			}
		}()
		output, err = t.Execute(execCtx, toolUse.Input)
	}()

	// 区分超时、取消、其它错误，便于上层选择 Status（aborted vs error）。
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			// 仅当上层 ctx 未取消、而是工具自身超时时，把 err 包装为可识别形式
			return output, fmt.Errorf("工具执行超时(%s): %w", h.timeout, err)
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return output, fmt.Errorf("工具执行被取消: %w", err)
		}
		return output, err
	}
	return output, nil
}

// classifyStatus 根据错误类型与 ctx 状态决定结束事件的状态枚举。
//
// 判定规则：
//   - 整体 ctx 已被用户取消（context.Canceled）→ aborted（用户主动中断）
//   - 否则（工具自身超时 / 工具内部错误）→ error（系统/工具问题）
//
// 工具超时（execCtx.DeadlineExceeded）在 doExecute 中已被包装为
// "工具执行超时" 错误，但 errors.Is 仍透传 DeadlineExceeded；
// 这里用 ctx.Err() == nil 区分——若外层 ctx 未取消则归类为 error。
func (h *ToolHandler) classifyStatus(err error, ctx context.Context) string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return ToolEventStatusAborted
	}
	if errors.Is(err, context.Canceled) {
		return ToolEventStatusAborted
	}
	return ToolEventStatusError
}

// lookup 在 Registry 中查工具；允许 registry 为 nil。
func (h *ToolHandler) lookup(name string) (tool.Tool, bool) {
	if h.registry == nil {
		return nil, false
	}
	return h.registry.Get(name)
}

// fireStart 安全地调用 OnStart 回调。
func (h *ToolHandler) fireStart(evt ToolExecutionEvent) {
	h.mu.RLock()
	fn := h.onStart
	h.mu.RUnlock()
	if fn != nil {
		fn(evt)
	}
}

// fireEnd 安全地调用 OnEnd 回调。
func (h *ToolHandler) fireEnd(evt ToolExecutionEvent) {
	h.mu.RLock()
	fn := h.onEnd
	h.mu.RUnlock()
	if fn != nil {
		fn(evt)
	}
}
