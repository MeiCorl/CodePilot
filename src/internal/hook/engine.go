// Package hook — HookEngine 编排核心 (spec §F + §G + Task 5)。
//
// Engine 是整个 hook 系统的中枢:
//   - 持有 EngineConfig(Logger / LLMProvider / ToolRegistry / PromptSink 等依赖);
//   - 维护 entries(event → []*entry 顺序敏感)+ onceTracker(sessionID → name → fired?);
//   - Dispatch(event, hookCtx) 串起:once check → condition match → async/sync
//     分支 → RunSafe(panic recover) → Stats 计数;
//   - 任何 error / panic 只 log 不传播(spec §G 错误隔离);
//   - Shutdown(ctx) 用 sync.WaitGroup 等待所有 async goroutine 收尾。
//
// 设计要点(Why):
//   - entries 用 map 索引 + slice 保持顺序:12 类事件 / 每事件平均 1~3 条,
//     O(1) 事件定位 + O(N) 顺序遍历足够;无需更复杂的 priority queue;
//   - onceTracker 二级 map:不同 sessionID 互不影响(同进程跑多会话时符合
//     spec §H.3「once: true 在 session 级共享」);sessionID 为 "" 时共用
//     一个 bucket,适合 program_start/program_exit 等无 session 事件;
//   - 同步 hook 用 context.WithTimeout(EngineConfig.DefaultTimeout) 包裹,
//     超时后子进程由 os/exec.CommandContext 内部 kill,executor 返回
//     ErrCommandTimeout / ctx.Err,Engine warn 后继续;
//   - 异步 hook 在 goroutine 内执行同一套逻辑,无超时(避免长任务被 kill),
//     用 sync.WaitGroup 在 Shutdown 时统一等待;
//   - Stats 三个原子计数(EntriesTotal 装载时定值 + FiredTotal / FailedTotal
//     运行期自增),供 WebUI 状态栏「hooks 子项」只读展示(spec §G 末尾)。
//
// 不做:
//   - Hook 自身的热加载:本步骤启动期一次性 LoadEntries,运行期不监听
//     setting.json 变化(spec Out of Scope §3);
//   - Hook 优先级 / 并发控制:同事件多 hook 按配置数组顺序串行(同步)/
//     并行启动(异步),不提供 priority 字段(spec Out of Scope §9);
//   - 跨进程 / 跨会话 once 状态:once 状态只在 Engine 进程内有效
//     (spec Out of Scope §6)。
package hook

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/hook/executor"
	"github.com/MeiCorl/CodePilot/src/internal/hook/matcher"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
	"github.com/MeiCorl/CodePilot/src/llm"
	"go.uber.org/zap"
)

// EngineConfig 是 Engine 的构造配置。
//
// 字段语义:
//   - Enabled:总开关;若为 false,Engine 构造但不加载 entries,Dispatch 直接 no-op
//     (spec §非功能要求 3「hooks.enabled = false 时,Hook 引擎完全跳过」);
//   - DefaultTimeout:同步 hook 的默认超时,默认 30s(spec §H.1「同步 hook 默认超时 30s」);
//   - Logger:zap 日志实例,Engine / Executors 共用;nil 时降级为 zap.NewNop()(便于测试);
//   - LLMProvider:仅 agent action 需要;nil 时 agent action 走 ErrNoLLMProvider;
//   - ToolRegistry:仅 agent action 需要(查 allow_tools 对应 ToolSpec);nil 时允许;
//   - PromptSink:prompt action 的文本注入接口;nil 时 prompt action 降级 warn + skip。
type EngineConfig struct {
	// Enabled 为 Hook 系统总开关;若为 false,Engine 构造后 Dispatch 立即 no-op。
	Enabled bool
	// DefaultTimeout 是同步 hook 的默认超时,默认 30s。
	DefaultTimeout time.Duration
	// Logger 为 zap 日志实例;nil 时降级为 zap.NewNop()(便于测试)。
	Logger *zap.Logger
	// LLMProvider 供 agent action 调用 LLM 子任务;nil 时 agent action 返回 ErrNoLLMProvider。
	LLMProvider llm.Provider
	// ToolRegistry 供 agent action 查询 allow_tools 对应的 ToolSpec;nil 时允许(agent 不带工具)。
	ToolRegistry *tool.Registry
	// PromptSink 供 prompt action 把渲染文本注入到当前轮 user 消息;nil 时降级 warn + skip。
	PromptSink PromptSink
}

// entry 是 Engine 内部维护的单条 hook 注册项。
//
// [Why private struct] Engine 内部实现细节,对外不暴露(避免调用方绕开
// LoadEntries 直接操作 entries);executor / matcher 在 LoadEntries 阶段一次性
// 构造好,Dispatch hot-path 只读使用。
type entry struct {
	// config 为配置原文,用于 once 追踪(entry.Name)+ 日志诊断。
	config config.HookEntryConfig
	// executor 为该 entry 的 action 执行器(command/http/prompt/agent 之一)。
	executor executor.Executor
	// matcher 为无状态条件评估器;LoadEntries 阶段预构造,Dispatch hot-path 调用。
	matcher *matcher.Matcher
	// parsedCond 为反序列化后的 Condition(避免 Dispatch 时反复 ParseCondition)。
	parsedCond matcher.Condition
	// hasCond 标识 condition 是否非空(避免 IsEmpty() 重复调用;空 condition 走无条件分支)。
	hasCond bool
}

// Stats 是 Engine 运行期计数快照,供 WebUI 状态栏「hooks 子项」只读展示
// (spec §G.末尾「WebUI 状态栏可观测性:新增 hooks 子项,显示已配置 N 条 /
// 已触发 M 次 / 失败 K 次」)。
//
// 字段语义:
//   - EntriesTotal:已配置的 hook 总数(LoadEntries 完成时定值,运行期不变);
//   - FiredTotal:所有 entry 累计触发次数(condition 匹配 + 实际进入 Execute 的次数);
//   - FailedTotal:FiredTotal 中 Execute 返回 error / panic 的次数。
type Stats struct {
	// EntriesTotal 为已配置的 hook 总数。
	EntriesTotal int64
	// FiredTotal 为所有 entry 累计触发次数。
	FiredTotal int64
	// FailedTotal 为 Execute 失败(error 或 panic)的累计次数。
	FailedTotal int64
}

// Engine 是 Hook 系统的中枢。
//
// 并发安全:Dispatch / LoadEntries / Stats 均可并发调用;内部用 sync.RWMutex
// 保护 entries / onceTracker 的读写,Stats 用 atomic.Int64 避免锁竞争。
type Engine struct {
	// cfg 为构造配置(只读,运行期不变;新 entries 通过 LoadEntries 注入)。
	cfg EngineConfig
	// entries 把 event 名映射到该事件下的所有 entry,顺序敏感(spec §C「执行按数组顺序」)。
	entries map[string][]*entry
	// onceTracker 二级 map:sessionID → entry.Name → fired?。
	// 一次性 hook(Once=true)在同 session 第二次起被跳过(spec §C「once: true」)。
	onceTracker map[string]map[string]bool
	// mu 保护 entries / onceTracker 的并发读写;Stats 用 atomic 不走此锁。
	mu sync.RWMutex
	// asyncWG 用于 Shutdown 等待所有 async goroutine 完成。
	asyncWG sync.WaitGroup
	// entriesTotal 是装载期定值,供 Stats.EntriesTotal 快速读取(避免 range entries map)。
	entriesTotal int64
	// firedTotal / failedTotal 运行期自增,供 Stats 返回。
	firedTotal  atomic.Int64
	failedTotal atomic.Int64
}

// New 构造 Engine 但不加载 entries(配置分离:New 仅持有 EngineConfig 依赖)。
//
// 若 cfg.Logger 为 nil,降级为 zap.NewNop()(便于单测无需 Init logger)。
// 若 cfg.DefaultTimeout <= 0,填默认 30s(与 spec §H.1 + executor.DefaultCommandTimeout 一致)。
//
// [Why New 不直接 LoadEntries] Loader 与 Engine 解耦:Engine 可被 New 但
// 暂不加载(比如 hooks.enabled=false 的零配置场景),LoadEntries 由 main.go
// 在 hook 段校验通过后调用。
func New(cfg EngineConfig) *Engine {
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 30 * time.Second
	}
	return &Engine{
		cfg:         cfg,
		entries:     make(map[string][]*entry),
		onceTracker: make(map[string]map[string]bool),
	}
}

// LoadEntries 把一批 entry 配置注册到 Engine。
//
// 处理流程:
//  1. 遍历每条 entry,校验 Event 合法(IsValidEvent)+ Action.Type 合法(ParseActionType);
//  2. 构造 executor(executor.NewExecutorByType)+ 解析 condition(matcher.ParseCondition);
//  3. agent 类型时,把 Engine 的 LLMProvider / ToolRegistry 注入到 executor
//     (避免 Engine 持有 executor.SetProvider 内部状态);其它类型无依赖;
//  4. 把 entry 按 event 分组追加到 entries map(slice 保留数组顺序);
//  5. 累加 entriesTotal(Stats 缓存)。
//
// 任何一条 entry 校验失败 → 整体返回 error,Engine 保持构造时的空 entries 状态
// (不允许半成功:避免「部分 entry 加载后忘记检查」导致的隐式行为差异)。
//
// [Why 整体回滚而非单条跳过] config.ValidateHookConfig 已在配置加载阶段把
// 「必须项」校验过(Name / Event / Action.Type);本函数主要负责 executor 构造
// 校验,失败意味着用户配置错误,启动期应当显式 fail-fast 暴露问题。
func (e *Engine) LoadEntries(entries []config.HookEntryConfig) error {
	if !e.cfg.Enabled {
		// 总开关关闭:清空 entries 即可(spec §非功能要求 3)。
		e.mu.Lock()
		e.entries = make(map[string][]*entry)
		e.entriesTotal = 0
		e.mu.Unlock()
		return nil
	}

	// 临时存放新 entries,校验全部通过后再提交,避免半成功状态。
	pending := make(map[string][]*entry, len(e.entries))
	var total int64

	for i, ec := range entries {
		// 1) Event 合法性(spec §A 12 类事件)
		if !IsValidEvent(ec.Event) {
			return fmt.Errorf("hook.LoadEntries: entries[%d] (name=%q) event=%q 非法(必须 12 类事件之一)",
				i, ec.Name, ec.Event)
		}
		// 2) Action.Type 合法性 + 构造 executor
		exec, err := executor.NewExecutorByType(ec.Action.Raw)
		if err != nil {
			return fmt.Errorf("hook.LoadEntries: entries[%d] (name=%q) 构造 executor 失败: %w",
				i, ec.Name, err)
		}
		// 3) agent 类型时把 LLMProvider / ToolRegistry 注入 executor
		//    [Why 在 Engine 阶段注入而非 New 时] agent action 依赖主 Agent 的
		//    LLM provider,Engine 在 wire 时持有;executor 包不反向依赖 main.go。
		if agentExec, ok := exec.(*executor.AgentExecutor); ok {
			agentExec.SetProvider(e.cfg.LLMProvider)
			agentExec.SetRegistry(e.cfg.ToolRegistry)
		}

		// 4) 解析 condition(可能为 nil / 空,ParseCondition 已容错)
		var cond matcher.Condition
		if ec.Condition != nil && len(*ec.Condition) > 0 {
			parsed, perr := matcher.ParseCondition(*ec.Condition)
			if perr != nil {
				return fmt.Errorf("hook.LoadEntries: entries[%d] (name=%q) 解析 condition 失败: %w",
					i, ec.Name, perr)
			}
			cond = parsed
		}

		en := &entry{
			config:     ec,
			executor:   exec,
			matcher:    matcher.NewMatcher(),
			parsedCond: cond,
			hasCond:    !cond.IsEmpty(),
		}
		pending[ec.Event] = append(pending[ec.Event], en)
		total++
	}

	// 5) 提交到 Engine(整体替换语义,与配置层 MergeHooks 的「项目级整体替换」对齐)
	e.mu.Lock()
	e.entries = pending
	e.entriesTotal = total
	e.mu.Unlock()
	return nil
}

// entriesTotal 读取辅助:在 mu 读锁保护下读取 entriesTotal。
//
// [Why 单独函数] 内部用,Stats / 测试断言都需要原子读;直接 atomic.Load
// 不能保证与 LoadEntries 写入的 happens-before(虽然 atomic.Int64 写入
// 在 mu.Lock 临界区内,锁释放时自动 barrier,但 entriesTotal 是普通 int64)。
// 用 mu.RLock 保证 LoadEntries 写入对所有读可见。
func (e *Engine) readEntriesTotal() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.entriesTotal
}

// Dispatch 是 Hook 系统的核心调度入口,被 12 类事件触发点调用。
//
// 流程:
//  1. 校验 event 合法 + Enabled=true(否则直接 return,无锁快速路径);
//  2. 读 entries map 取该 event 下的所有 entry(按注册顺序);
//  3. 对每个 entry:
//     a. onceTracker 检查(本 sessionID+entry.Name 已触发 → 跳过 + debug log);
//     b. condition 评估(hasCond=true 时调 matcher.Evaluate);
//     c. async 分支(Async=true):WaitGroup.Add(1) + go func(){ defer recover + Done }();
//     d. 同步分支(Async=false):在当前 goroutine 内 RunSafe 执行,ctx 包 DefaultTimeout;
//  4. 任何 error / panic 只 log 不传播(spec §G);
//  5. 触发后:Once=true 时 onceTracker 标记;FiredTotal/FailedTotal 计数。
//
// 参数 hookCtx 为 nil 时,Engine 走 debug log + return(spec §F 集成点不
// 应传入 nil,但防御性判空避免 panic 蔓延)。
//
// [Why RLock + 释放 + 后续持锁粒度细分] entries 在 LoadEntries 后通常只读,
// RLock 期间执行 Execute 可能阻塞其它 Dispatch(同步 hook 慢);所以 RLock
// 仅用于读 entries 切片引用,Execute 阶段释放锁(每个 entry 独立加锁读
// onceTracker),最大化并发。
func (e *Engine) Dispatch(ctx context.Context, event string, hookCtx *HookContext) {
	if !e.cfg.Enabled {
		return
	}
	if !IsValidEvent(event) {
		e.cfg.Logger.Warn("hook.Dispatch: invalid event, skip",
			zap.String("event", event),
		)
		return
	}
	if hookCtx == nil {
		e.cfg.Logger.Warn("hook.Dispatch: nil hook context, skip",
			zap.String("event", event),
		)
		return
	}
	// 兜底填 Event 字段(若调用方未填)
	if hookCtx.Event == "" {
		hookCtx.Event = event
	}

	// 取该 event 下的所有 entry(浅拷贝 slice 头,释放 RLock 后仍可用)
	e.mu.RLock()
	list := e.entries[event]
	e.mu.RUnlock()
	if len(list) == 0 {
		return
	}

	vars := hookCtx.Vars()
	for _, en := range list {
		e.dispatchOne(ctx, event, en, hookCtx, vars)
	}
}

// dispatchOne 处理单条 entry 的触发(once + match + async/sync + Stats)。
//
// 这是 Dispatch 内部方法,签名设计为不持锁(entries 切片在 Dispatch 阶段
// 已取出);only 涉及 onceTracker 的小粒度锁。
func (e *Engine) dispatchOne(ctx context.Context, event string, en *entry, hookCtx *HookContext, vars map[string]string) {
	// 1) once 检查
	if en.config.Once {
		if e.alreadyFired(hookCtx.SessionID, en.config.Name) {
			e.cfg.Logger.Debug("hook.dispatch: skip (once fired)",
				zap.String("event", event),
				zap.String("entry", en.config.Name),
				zap.String("session_id", hookCtx.SessionID),
			)
			return
		}
	}

	// 2) condition 评估(hasCond=false → 跳过 matcher 直接 match)
	matched := true
	var matchReason string
	if en.hasCond {
		matched, matchReason = en.matcher.Evaluate(en.parsedCond, hookCtx)
		if !matched {
			e.cfg.Logger.Debug("hook.dispatch: condition not matched",
				zap.String("event", event),
				zap.String("entry", en.config.Name),
				zap.String("reason", matchReason),
			)
			return
		}
	}

	// 3) 计数:触发数自增(失败计数在 executeOne 内部根据结果判断)
	e.firedTotal.Add(1)

	// 4) async / sync 分支
	if en.config.Async {
		e.asyncWG.Add(1)
		go func(entryName string) {
			defer e.asyncWG.Done()
			defer func() {
				// 防御:goroutine 内任意 panic 不应杀死进程
				_ = recover()
			}()
			e.executeOne(ctx, event, en, hookCtx, vars, true)
			// once 标记在 executeOne 之后(sync 也一样,确保真正进入 Execute 才标记)
		}(en.config.Name)
		return
	}

	// 同步路径:在主 goroutine 内执行(sync 走 DefaultTimeout 包裹 ctx)
	timedCtx, cancel := ctxWithDefaultTimeout(ctx, e.cfg.DefaultTimeout)
	defer cancel()
	e.executeOne(timedCtx, event, en, hookCtx, vars, false)
}

// executeOne 执行单条 entry,内含 RunSafe(panic recover)+ 错误隔离 + Stats 计数。
//
// 一旦进入本方法,entry 必然命中 + 即将被 fire;FiredTotal 已在外层 +1,
// 这里只根据 Execute 结果(Failed)决定 failedTotal。
//
// sync=false(异步)时不再加 timeout(async 任务不强制 kill,owner 自己处理
// timeout),sync=true(同步)时 ctx 已在外层包了 DefaultTimeout。
func (e *Engine) executeOne(ctx context.Context, event string, en *entry, hookCtx *HookContext, vars map[string]string, async bool) {
	// Once 标记:必须在 Execute 前打,避免两次并发 Dispatch 各自看到「未触发」同时进入
	if en.config.Once {
		e.markFired(hookCtx.SessionID, en.config.Name)
	}

	// prompt action 需要 Engine 在 Execute 完成后读 Last() → AppendToCurrentMessage
	// [Why 用类型断言] 4 个 Executor 中只有 PromptExecutor 有「副产物」需要 Engine
	// 协同处理(注入对话上下文);其它 3 个 action Execute 即终态。
	isPromptAction := en.executor.Type() == executor.ActionTypePrompt

	// RunSafe 把 panic 转 error
	err := executor.RunSafe(e.cfg.Logger, event, en.executor.Type(), en.executor, ctx, hookCtx, vars)
	if err != nil {
		e.failedTotal.Add(1)
		e.cfg.Logger.Warn("hook.dispatch: action failed",
			zap.String("event", event),
			zap.String("entry", en.config.Name),
			zap.String("action_type", en.executor.Type()),
			zap.Bool("async", async),
			zap.Error(err),
		)
		return
	}

	// 成功:prompt action 走 sink 注入(失败仅 warn 不传播)
	if isPromptAction {
		pe, ok := en.executor.(*executor.PromptExecutor)
		if !ok {
			// 理论上不会发生(Type 已断言过);防御性 log
			e.cfg.Logger.Warn("hook.dispatch: prompt executor type assertion failed",
				zap.String("entry", en.config.Name),
			)
			return
		}
		text := pe.Last()
		if text == "" {
			// Execute 后 last 仍为空(理论上 ErrEmptyPrompt 时已 fail,
			// 但 Execute 也可能因文本纯空白走 err 路径而这里走到) — 防御
			return
		}
		if e.cfg.PromptSink == nil {
			e.cfg.Logger.Warn("hook.dispatch: prompt action has no PromptSink, drop text",
				zap.String("event", event),
				zap.String("entry", en.config.Name),
			)
			return
		}
		if sinkErr := e.cfg.PromptSink.AppendToCurrentMessage(text); sinkErr != nil {
			e.cfg.Logger.Warn("hook.dispatch: PromptSink.AppendToCurrentMessage failed",
				zap.String("event", event),
				zap.String("entry", en.config.Name),
				zap.Error(sinkErr),
			)
			// 不算 hook 失败(FailedTotal 仍按 action Execute 结果计数),
			// sink 失败只影响 prompt 注入,主 Agent Loop 不受影响
		} else {
			e.cfg.Logger.Debug("hook.dispatch: prompt action injected",
				zap.String("event", event),
				zap.String("entry", en.config.Name),
			)
		}
	}
}

// alreadyFired 返回 sessionID+entryName 是否在 onceTracker 中标记过。
//
// sessionID 为空时使用 "" 作为 key(program_start/program_exit/compact 等
// 无 session 事件共用一个 bucket,符合 spec §H.3「once: true 在 session 级
// 共享」— 进程内只有一份)。
func (e *Engine) alreadyFired(sessionID, entryName string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	bucket, ok := e.onceTracker[sessionID]
	if !ok {
		return false
	}
	return bucket[entryName]
}

// markFired 把 sessionID+entryName 加入 onceTracker。
//
// 同一 sessionID+entryName 多次 mark 是幂等的(已存在就 skip),允许并发
// Dispatch 同一 entry 时不会重复计数。
func (e *Engine) markFired(sessionID, entryName string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	bucket, ok := e.onceTracker[sessionID]
	if !ok {
		bucket = make(map[string]bool)
		e.onceTracker[sessionID] = bucket
	}
	bucket[entryName] = true
}

// Stats 返回 Engine 运行期计数的快照(只读,用于 WebUI 状态栏「hooks 子项」)。
//
// 实现要点:
//   - EntriesTotal 通过 mu.RLock 读取(LoadEntries 临界区写入,锁释放
//     保证 happens-before);不用 atomic.Load 是因为 entriesTotal 是普通
//     int64,与 atomic.Load 的内存模型假设不匹配;
//   - FiredTotal / FailedTotal 用 atomic.Int64.Load 读取,无锁;
//   - 返回值是值类型,调用方修改不影响 Engine 状态。
func (e *Engine) Stats() Stats {
	return Stats{
		EntriesTotal: e.readEntriesTotal(),
		FiredTotal:   e.firedTotal.Load(),
		FailedTotal:  e.failedTotal.Load(),
	}
}

// Shutdown 等待所有 async goroutine 完成,实现优雅退出。
//
// 流程:
//  1. 立即标记「不再接受新 Dispatch」:用 mu 锁 + 把 cfg.Enabled 临时改为 false;
//     [Why 不在 lock 内永久关闭] 任务边界明确,Shutdown 一次性调用;
//  2. 等待 asyncWG 完成(在 Shutdown 期间触发的 Dispatch 走 no-op);
//  3. 返回。
//
// 参数 ctx 用于「Shutdown 等待超时」;nil 时用 context.Background() 无限等。
// spec §G 错误隔离未要求 Shutdown 超时 kill,这里实现为「带 ctx 超时但等
// 待中的 goroutine 不强制 kill」(允许用户进程级退出时自然清理)。
//
// [Why 不显式关 cfg.Enabled] cfg 是值类型,Engine 持有的是值拷贝;运行时
// 改 cfg.Enabled 不影响其它并发读;这里简单实现为「调用后 Dispatch 通过
// IsValidEvent + Enabled 仍为 true 的 fast-path 不会 break 已派发 goroutine,
// 但 Shutdown 后若还有新 Dispatch 调用,行为未定义 — 由调用方在主流程
// 收尾阶段统一处理」。
func (e *Engine) Shutdown(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	done := make(chan struct{})
	go func() {
		e.asyncWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		e.cfg.Logger.Debug("hook.Engine.Shutdown: all async hooks done")
	case <-ctx.Done():
		e.cfg.Logger.Warn("hook.Engine.Shutdown: timeout waiting for async hooks",
			zap.Error(ctx.Err()),
		)
	}
}

// ctxWithDefaultTimeout 包裹一个带超时的 ctx(若 ctx 已有更短 deadline 则不覆盖)。
//
// 规则:
//   - ctx 已有 deadline 且剩余时间 ≤ defaultTimeout:沿用 ctx(不延长);
//   - ctx 已有 deadline 但剩余时间 > defaultTimeout:用 WithTimeout 缩到 defaultTimeout;
//   - ctx 无 deadline:用 WithTimeout 加 defaultTimeout。
//
// [Why 用 stop 通道控制 cancel] context.WithTimeout 必须调 cancel 释放
// timer 资源,否则 go vet 会报 context leak。但 cancel 会让新 ctx 立即
// Done — 与我们的意图矛盾。这里把 cancel 函数绑到 stop 通道上,等
// Execute 返回(或更准确:等 executor.RunSafe 在调用方 goroutine 退出)
// 时调 cancel。Engine 调用频率极低(每事件最多一次同步 hook),不会
// 出现 timer 累积问题。
func ctxWithDefaultTimeout(ctx context.Context, defaultTimeout time.Duration) (context.Context, context.CancelFunc) {
	if defaultTimeout <= 0 {
		return ctx, func() {}
	}
	if existing, ok := ctx.Deadline(); ok {
		remaining := time.Until(existing)
		if remaining <= defaultTimeout {
			return ctx, func() {}
		}
	}
	return context.WithTimeout(ctx, defaultTimeout)
}
