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
	"github.com/MeiCorl/CodePilot/src/internal/security"
	"github.com/MeiCorl/CodePilot/src/llm"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
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
	// interceptor 为权限拦截器；为 nil 时不做权限检查（向后兼容）
	interceptor *security.Interceptor
	// middlewares 为 Tool Execute 前的中间件链，按注册顺序执行。
	// 任一中间件返回 err 即终止后续中间件与工具执行，err 透传给 LLM。
	// 当前用于 SandboxMiddleware（路径类工具的硬兜底沙箱），后续可扩展
	// 日志/指标/追踪等横切关注点。
	middlewares []security.MiddlewareFunc
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

// SetInterceptor 设置权限拦截器。
// 传入 nil 表示禁用权限检查（向后兼容无权限系统的旧代码路径）。
// 应在 main.go 顶层构造后、启动服务前调用。
func (h *ToolHandler) SetInterceptor(i *security.Interceptor) {
	h.interceptor = i
}

// RegisterMiddleware 追加一个中间件到执行链。
// 多个中间件按追加顺序串行执行；任一中间件返回 err 即终止后续中间件与工具执行。
// 应在 main.go 顶层构造后、启动服务前调用；运行中追加需自行评估并发安全。
//
// 典型用法：toolHandler.RegisterMiddleware(security.SandboxMiddleware(workdir, checker))
func (h *ToolHandler) RegisterMiddleware(mw security.MiddlewareFunc) {
	if mw == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.middlewares = append(h.middlewares, mw)
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
	// 把 tool_use_id 注入 ctx，供工具实现按 id 关联副作用（如 WriteFile/EditFile
	// 写入 FileDiffStore）。空 id 时 WithToolUseID 不注入，等价于 ctx 不变。
	execCtx = tool.WithToolUseID(execCtx, toolUse.ID)

	t, ok := h.lookup(toolUse.Name)
	if !ok {
		return "", &ErrToolNotFound{Name: toolUse.Name}
	}

	// 权限拦截：在工具执行前检查权限。
	// 拦截器决定放行/拒绝/需要用户确认；被拒绝时直接返回错误，
	// 作为 ToolResultBlock{IsError: true} 回传给 LLM，不触发 Agent Loop 终止。
	if h.interceptor != nil {
		result, err := h.interceptor.Check(execCtx, toolUse.Name, toolUse.Input, t.Permission())
		if err != nil {
			logger.WarnCtx(ctx,"权限拦截器检查异常",
				zap.String("tool", toolUse.Name),
				zap.Error(err),
			)
			return "", fmt.Errorf("权限检查异常: %w", err)
		}
		if result != nil {
			// result != nil 表示被拦截
			return "", fmt.Errorf("%s", result.Decision.Reason)
		}
		// result == nil 表示放行，继续执行工具
	}

	// Middleware 链：权限拦截后、工具 Execute 前执行。
	// 当前内置 SandboxMiddleware（路径类工具的硬兜底沙箱解析），
	// 解析结果通过 ctx 注入的 PathResolver 传给工具，工具不再自行校验。
	// 任一中间件返回 err 即终止后续中间件与工具执行，err 透传给 LLM。
	if len(h.middlewares) > 0 {
		h.mu.RLock()
		mws := h.middlewares
		h.mu.RUnlock()
		for _, mw := range mws {
			mwCtx, err := mw(execCtx, toolUse.Name, toolUse.Input, t.Permission())
			if err != nil {
				logger.WarnCtx(ctx,"中间件拦截工具执行",
					zap.String("tool", toolUse.Name),
					zap.Error(err),
				)
				return "", err
			}
			execCtx = mwCtx
		}
	}

	// 权限分级仅做信息记录，强制拦截由工具自身 + safety 包负责。
	logger.DebugCtx(ctx,"工具开始执行",
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

// ExecuteBatch 批量执行 LLM 发出的多个 tool_use，根据工具权限分级调度执行策略。
//
// 执行策略：
//   - 只读工具（PermRead）：并行执行，用 WaitGroup 协调，全部完成后统一收集结果
//   - 写入/执行工具（PermWrite / PermExec）：按原始顺序串行执行
//   - 未注册工具：直接标记为 IsError=true 的 ToolResult，不影响其他工具
//   - 单个工具失败：不影响同批次其他工具执行
//
// 返回值：与输入 toolUses 一一对应的 ToolResultBlock 切片，顺序保持一致。
// 每个 tool_use 的 OnStart/OnEnd 回调正常触发（与 Execute 单工具行为一致）。
func (h *ToolHandler) ExecuteBatch(ctx context.Context, toolUses []llm.ToolUseBlock) []llm.ToolResultBlock {
	if len(toolUses) == 0 {
		return nil
	}

	// 结果切片，按原始索引对齐
	results := make([]llm.ToolResultBlock, len(toolUses))

	// 第一步：对所有 tool_use 做预分类——查出对应的工具实例和权限
	type indexedToolUse struct {
		index     int
		toolUse   llm.ToolUseBlock
		tool      tool.Tool     // nil 表示未注册
		permission tool.ToolPermission
	}
	items := make([]indexedToolUse, len(toolUses))
	for i, tu := range toolUses {
		t, ok := h.lookup(tu.Name)
		perm := tool.PermRead // 默认当作只读（未注册工具不执行，权限无意义）
		if ok {
			perm = t.Permission()
		}
		items[i] = indexedToolUse{
			index:      i,
			toolUse:    tu,
			tool:       t,
			permission: perm,
		}
	}

	// 第二步：分组——只读组（并行）和写入/执行组（串行）
	var readOnlyGroup []indexedToolUse
	var writeExecGroup []indexedToolUse
	for _, item := range items {
		if item.tool == nil {
			// 未注册工具：直接生成错误结果，不加入任何组
			results[item.index] = llm.ToolResultBlock{
				ToolUseID: item.toolUse.ID,
				Content:   (&ErrToolNotFound{Name: item.toolUse.Name}).Error(),
				IsError:   true,
			}
			continue
		}
		if item.permission == tool.PermRead {
			readOnlyGroup = append(readOnlyGroup, item)
		} else {
			writeExecGroup = append(writeExecGroup, item)
		}
	}

	// 第三步：并行执行只读组
	if len(readOnlyGroup) > 0 {
		var wg sync.WaitGroup
		for _, item := range readOnlyGroup {
			wg.Add(1)
			go func(it indexedToolUse) {
				defer wg.Done()
				result := h.Execute(ctx, it.toolUse)
				results[it.index] = result
			}(item)
		}
		wg.Wait()
	}

	// 第四步：串行执行写入/执行组
	for _, item := range writeExecGroup {
		// 每次执行前检查 ctx 是否已取消，避免在已取消状态下继续串行
		if ctx.Err() != nil {
			results[item.index] = llm.ToolResultBlock{
				ToolUseID: item.toolUse.ID,
				Content:   "工具执行被取消: 上下文已终止",
				IsError:   true,
			}
			continue
		}
		result := h.Execute(ctx, item.toolUse)
		results[item.index] = result
	}

	return results
}
