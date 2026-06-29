# Hook 系统 — CodePilot 实现原理

> 状态:**已实现** | 目标 Step:11 | 架构层:第 3 层 工具层

Hook 系统让用户在 Agent 生命周期关键节点上配置自动化动作,用于日志、通知、格式化、提示词注入和轻量 LLM 检查。它属于工具层能力,通过集成胶水接入引擎层与交互层,核心 hook 包本身不依赖上层私有实现。

## §1 能力边界

- 支持 12 类事件:`program_start` / `program_exit` / `compact` / `error` / `session_start` / `session_end` / `iteration_start` / `iteration_end` / `pre_tool_use` / `post_tool_use` / `pre_message` / `post_message`
- 支持 condition DSL:leaf、`all`、`any`,leaf 操作为 `eq` / `neq` / `glob` / `contains`
- 支持 4 类 action:`command` / `http` / `prompt` / `agent`
- 支持 `async` 后台执行、`once` 会话内一次触发、Stats 计数与 Shutdown 等待异步任务
- Hook 失败只写日志与失败计数,不打断 Agent Loop、工具执行或会话切换

配置 schema、完整示例和排障方法在 `config-management/reference/hook.md` 维护;本文件只讲源码实现。

## §2 核心包结构

| 路径 | 作用 |
|------|------|
| `src/internal/hook/event.go` | 事件常量、事件分组、合法性校验 |
| `src/internal/hook/context.go` | `HookContext` 类型别名与构造工厂,对外保留 hook 包 API |
| `src/internal/hook/interpolate.go` | 插值函数别名,实际实现下沉到 `hookcontext` |
| `src/internal/hookcontext/` | 事件上下文、变量映射与 `$VAR` 插值,用于打破 import cycle |
| `src/internal/hook/matcher/` | condition 解析与匹配,读取 HookContext 字段与 `tool_input.*` 子字段 |
| `src/internal/hook/executor/` | 四类 action 执行器与公共 `RunSafe` panic 隔离 |
| `src/internal/hook/engine.go` | HookEngine 注册、调度、once/async、Stats、Shutdown |
| `src/internal/hook/loader.go` | 从 `config.HookConfig` 构造 Engine 并加载 entries |
| `src/internal/hook/prompt_sink.go` | prompt action 与对话消息注入之间的解耦接口 |
| `src/internal/hook/integration/` | Agent Loop / ToolHandler / Session / Compact / PromptSink 集成胶水 |

## §3 配置到引擎的加载链路

1. `src/internal/config/config.go` 定义 `HookConfig` / `HookEntryConfig` / `HookActionConfig`。
2. `setDefaults` 为 `hook.enabled` 填默认 true,`ValidateHookConfig` 校验 name/event/action.type。
3. `main.go` 使用 `hook.LoadFromConfig(&cfg.Hook, hook.EngineConfig{...})` 构造 Engine。
4. `LoadEntries` 按 event 分组 entries,解析 condition,并通过 `executor.NewExecutorByType` 构造具体 action executor。
5. `hooks.enabled=false` 时 Engine 仍可构造,但 Dispatch 走 no-op,实现零配置/关闭时低成本降级。

## §4 HookContext 与变量插值

`hookcontext.HookContext` 承载事件运行态信息,包括事件名、工具名、工具入参、工具结果、消息内容、session、iteration、workdir、错误信息等。

`Vars()` 会输出大写变量键,例如 `EVENT`、`SESSION_ID`、`TOOL_INPUT_FILE_PATH`、`TOOL_INPUT.COMMAND`。`Interpolate` 单遍扫描 `$VAR_NAME` 与 `$TOOL_INPUT.key`,未定义变量替换为空,`$$FOO` 保留字面 `$FOO`。

`hook/context.go` 与 `hook/interpolate.go` 保留别名 API,让外部仍可从 `hook` 包构造上下文,同时让 executor 不再 import hook 包,避免 `hook -> executor -> hook` 循环依赖。

## §5 Matcher

`src/internal/hook/matcher` 负责 condition DSL:

- 空 condition 匹配所有。
- `all` 所有子条件为 true 才匹配,显式空数组为 true。
- `any` 任一子条件为 true 即匹配,显式空数组为 false。
- leaf 从 HookContext 读取字段,包括 `event`、`tool_name`、`tool_input_file_path`、`message_role`、`session_id`、`iteration` 和 `tool_input.<key>`。
- `glob` 统一 Windows `\` 与 `/`,同时支持常见 basename 匹配。

## §6 Executor

`src/internal/hook/executor` 定义统一接口:

```go
type Executor interface {
    Type() string
    Execute(ctx context.Context, hookCtx *hookcontext.HookContext, vars map[string]string) error
}
```

四类实现:

- `CommandExecutor`:用 `exec.CommandContext` 执行命令,支持 cwd/env/timeout/stdout/stderr 日志。
- `HttpExecutor`:用 `http.Client` 发送请求,2xx 成功,非 2xx 与网络错误只计为 hook 失败。
- `PromptExecutor`:渲染文本并包装为 `<system-reminder>...</system-reminder>`,由 Engine 调 PromptSink 注入。
- `AgentExecutor`:当前是一次性 LLM 调用 stub,不写回主 history;源码保留 Step 12 SubAgent 升级 TODO。

公共 `RunSafe` 包装 panic recover,保证 executor panic 也不会传播到主流程。

## §7 HookEngine 调度

`src/internal/hook/engine.go` 的 Engine 负责核心编排:

1. `LoadEntries` 注册 entries,按 event 保存顺序敏感列表。
2. `Dispatch(ctx,event,hookCtx)` 校验 enabled/event/context 后取出该 event 的 entries。
3. 单条 entry 先做 once 检查,再跑 matcher。
4. `async=true` 时放入 goroutine 并纳入 `sync.WaitGroup`;同步 hook 直接执行。
5. `executeOne` 统一调用 executor.RunSafe,失败只递增 `FailedTotal` 并 warn log。
6. prompt action 成功后调用 `PromptSink.AppendToCurrentMessage`。
7. `Stats()` 返回 EntriesTotal/FiredTotal/FailedTotal,供 WebUI 状态栏展示。
8. `Shutdown(ctx)` 等待异步 hook 完成,用于 program_exit 收尾。

## §8 集成点

| 集成位置 | 文件 | 事件 |
|----------|------|------|
| 启动与退出 | `src/main.go` | `program_start` / `program_exit` |
| Agent Loop | `src/internal/engine/conversation/agent_loop.go` + `hook/integration/loop.go` | `iteration_start` / `iteration_end` / `error` |
| LLM 消息 | `src/internal/engine/conversation/manager.go` | `pre_message` / `post_message` |
| 工具执行 | `src/internal/engine/conversation/tool_handler.go` + `hook/integration/tool.go` | `pre_tool_use` / `post_tool_use` |
| 会话生命周期 | `src/internal/interaction/web/handler.go` + `hook/integration/session.go` | `session_start` / `session_end` |
| 上下文压缩 | `src/internal/interaction/web/handler.go` + `hook/integration/compact.go` | `compact` |
| prompt 注入 | `src/internal/engine/conversation/manager.go` + `hook/integration/prompt.go` | PromptSink |

每个集成点都通过胶水函数或局部 recover 防护,hook 报错不改变原有主流程行为。

## §9 Prompt 自感知

`src/internal/engine/prompt/sources/hooks_awareness.go` 新增 `HooksAwarenessSource`,Placement=System。它只放极短提示:Hook 配置在两层 `setting.json`,配置 schema 看 `config-management/reference/hook.md`,源码实现看 `codebase-overview/reference/hook-system.md`。这样不会新增独立 Skill,也不会让常驻 System Prompt 膨胀。

## §10 验证

主要测试:

- `src/internal/hook/*_test.go`:事件、上下文、插值、Engine once/async/Stats/Shutdown。
- `src/internal/hook/executor/*_test.go`:command/http/prompt/agent 执行器。
- `src/internal/hook/e2e_test.go`:真实 Engine 覆盖 command/http/prompt/agent、condition、once、async、错误隔离、12 类事件顺序。
- `src/internal/hook/integration/integration_smoke_test.go`:集成点触发与主流程不破坏。
- `src/internal/engine/prompt/sources/static_test.go`:SP 自感知内容与 token 上限。

当前全包测试中若出现 `build/dist` internal package 导入限制或 `tool/builtin` 既有 `withSandedPath` 未定义,属于非 Hook 既有噪声;Hook 相关包应保持通过。
