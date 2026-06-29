# Step 11 任务清单 — Hook 系统

> 实施顺序:Task 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8
> Task 1 是数据/配置层,Task 2-3 是基础类型与匹配器,Task 4 是四种 action 执行器,
> Task 5 是引擎核心,Task 6 是与 Agent Loop/ToolHandler/Session/Compact 的集成,
> Task 7 是 prompt 注入 + SP 自感知 + 既有 Skill 补充,Task 8 是端到端验证 + 回归
> 任务状态:文档生成时全部为 `待完成`;开始实现前更新为 `进行中`;完成且对应 checklist 通过后更新为 `已完成`

---

## Task 1: HookConfig 配置段(数据层)

**状态**:已完成

**目标**:在 `config.Config` 中新增 `HookConfig` 段,定义 `entries[]` schema,沿用 Step 5 / Step 8 / Step 10 的「全局 + 项目级字段级合并」语义,并提供默认值填充与校验。

**影响文件**:
- `src/internal/config/config.go` — 新增 `HookConfig` 结构 / `Hook` 字段 / `applyHookDefaults` / `MergeHooks` / `ValidateHookConfig`
- `src/internal/config/config_test.go` — 新增 `HookConfig` 字段测试(默认值 + 合并 + 校验)
- `config/setting.example.json` — 同步更新示例(添加 hooks 段)

**依赖**:无(Task 1 是入口)

**具体内容**:
1. 定义 `HookConfig` 结构体:
   - `Enabled *bool`(同 SkillConfig 模式,默认 true)
   - `Entries []HookEntryConfig`
2. 定义 `HookEntryConfig`(单条 hook 配置):
   - `Name string` (必须非空)
   - `Event string`(必须 12 类之一)
   - `Condition *json.RawMessage`(可选,缺省/null 视为匹配)
   - `Action HookActionConfig`(必填,见 3)
   - `Async bool`(默认 false)
   - `Once bool`(默认 false)
3. 定义 `HookActionConfig` 联合类型:
   - `Type string`(必填:command/http/prompt/agent)
   - 4 个 type-specific 子结构 + 用 `json.RawMessage` 透传 type-specific 字段
4. 实现 `applyHookDefaults(&HookConfig)`:`Enabled` 为 nil 时填默认 true;`Entries` 为 nil 时填空切片
5. 实现 `MergeHooks(global, project HookConfig) HookConfig`:
   - 同 Step 8 `MergeMemory` 风格:「项目级显式设置」时覆盖全局
   - `Enabled`:project.Enabled 非 nil 时覆盖
   - `Entries`:project 非空时整体替换(project 级 entries 完全覆盖 global);否则沿用 global
   - 合并完成后调用 `applyHookDefaults`
6. 实现 `ValidateHookConfig(*HookConfig) error`:
   - `Enabled` 必须为 bool(指针)
   - `Entries[i].Name` 非空
   - `Entries[i].Event` 必须 12 类之一
   - `Entries[i].Action.Type` 必须 command/http/prompt/agent 之一
   - `Async` / `Once` 不允许其它值(单纯 bool 解析自然 fail)
7. 在 `Config` struct 增加 `Hook HookConfig` 字段 + `json:"hook,omitempty"`
8. 在 `setDefaults` 调用 `applyHookDefaults(&c.Hook)`
9. 在 `validate` 调用 `ValidateHookConfig(&c.Hook)`
10. 默认值常量 `defaultHookEnabled = true`
11. 更新 `config/setting.example.json`:添加示例 hooks 段(注释说明)

**单测要点**:
- `applyHookDefaults`:nil / 部分字段 / 全字段 三种场景
- `MergeHooks`:global 全配 + project 全空 → 沿用 global;global 空 + project 全配 → 用 project;project 部分覆盖(只配 Enabled / Entries 一个)
- `ValidateHookConfig`:合法配置 / 缺 Name / Event 非法 / Action.Type 非法 / Action 缺 Type

**参考资料**:
- Step 5 权限配置:`src/internal/security/config.go` `PermissionsConfig`
- Step 8 记忆合并:`src/internal/config/config.go` `MergeMemory`
- Step 10 SkillConfig 范式:`src/internal/config/config.go` `SkillConfig` + `applySkillDefaults` + `IsEnabled`
- config 合并语义参考:`src/internal/security/policy.go` 多层配置合并

---

## Task 2: 事件类型 + HookContext + 变量插值(基础类型)

**状态**:已完成

**目标**:定义 12 类事件枚举 + `HookContext` 结构(携带事件上下文)+ 变量映射 + `$VAR_NAME` 插值函数。这是 hook 引擎与外部世界的数据契约。

**影响文件**:
- `src/internal/hook/event.go` — 新建,事件枚举 + 校验 + 分组常量
- `src/internal/hook/context.go` — 新建,HookContext + 构造工厂 + 变量映射
- `src/internal/hook/interpolate.go` — 新建,`$VAR` 插值函数
- `src/internal/hook/event_test.go` + `context_test.go` + `interpolate_test.go` — 新建,单测

**依赖**:Task 1(HookConfig 已定义,本任务不依赖具体配置但需在同一包内)

**具体内容**:

1. **`event.go`**:
   - 定义 12 个事件常量:
     - `EventProgramStart = "program_start"`
     - `EventProgramExit = "program_exit"`
     - `EventCompact = "compact"`
     - `EventError = "error"`
     - `EventSessionStart = "session_start"`
     - `EventSessionEnd = "session_end"`
     - `EventIterationStart = "iteration_start"`
     - `EventIterationEnd = "iteration_end"`
     - `EventPreToolUse = "pre_tool_use"`
     - `EventPostToolUse = "post_tool_use"`
     - `EventPreMessage = "pre_message"`
     - `EventPostMessage = "post_message"`
   - `AllEvents []string`(12 个,顺序与 spec §A 表格一致)
   - `EventCategory map[string]string`:event → category("system"/"session"/"iteration"/"tool"/"message")
   - `IsValidEvent(s string) bool`

2. **`context.go`** — `HookContext` struct:
   ```go
   type HookContext struct {
       Event             string                 // 事件名
       Category          string                 // "system"/"session"/...
       ToolName          string                 // 工具名(仅工具事件)
       ToolInput         map[string]any         // 工具参数(仅工具事件)
       ToolInputFilePath string                 // 便捷字段,ReadFile/WriteFile/EditFile/Glob/Grep 自动提取
       ToolResult        string                 // 工具结果(仅 post_tool_use)
       ToolIsError       bool                   // 是否失败(仅 post_tool_use)
       ToolDurationMs    int64                  // 耗时(仅 post_tool_use)
       MessageContent    string                 // 消息文本(仅消息事件)
       MessageRole       string                 // user/assistant(仅消息事件)
       Error             string                 // 错误文本(error / post_tool_use 失败)
       SessionID         string                 // 当前会话 ID
       Iteration         int                    // 轮次(轮次/工具/消息事件)
       Workdir           string                 // 工作目录
       Timestamp         time.Time              // 触发时间
   }
   ```
   - 提供构造工厂:`NewPreToolUseContext(...)` / `NewPostToolUseContext(...)` / `NewIterationContext(...)` / `NewSessionContext(...)` / `NewMessageContext(...)` / `NewProgramContext(...)` / `NewCompactContext(...)` / `NewErrorContext(...)`
   - 提供 `Vars() map[string]string`:把所有字段转成可插值的 `map[string]string`(嵌套字段如 `tool_input.command` 也展开)
   - `ToolInputFilePath` 自动提取:从 `ToolInput` 找 `file_path` / `path` 字段(优先 `file_path`)

3. **`interpolate.go`** — `Interpolate(template string, vars map[string]string) string`:
   - 扫描 `$VAR_NAME`(`[A-Z_][A-Z0-9_]*`),从 vars 查找替换;未找到时替换为空字符串
   - 支持嵌套字段:`$TOOL_INPUT.command` 自动展开为 `vars["tool_input.command"]`
   - 跳过 `$$` 转义(`$$FOO` 渲染为 `$FOO` 字面量)
   - 性能:用 `strings.Builder` 单遍扫描,不做正则

4. **单测**:
   - `IsValidEvent`:12 类 + 非法字符串
   - `NewPreToolUseContext`:ToolInput 解析 / ToolInputFilePath 自动提取(file_path 优先)
   - `Vars()`:所有字段非空 / 部分字段空 时变量映射完整性
   - `Interpolate`:`$A` 单变量 / 多个变量 / 嵌套字段(`$TOOL_INPUT.command`) / 未定义变量替换为空 / `$$` 转义 / `$` 后跟小写不替换

**参考资料**:
- ToolExecutionEvent 已有的字段设计:`src/internal/engine/conversation/tool_handler.go` `ToolExecutionEvent`(参考字段命名)
- `tool.PathTools` 路径参数 key 表:`src/internal/security/sandbox_middleware.go` `PathTools` map(参考 ToolInputFilePath 提取逻辑)
- 已有 JSON 反序列化:见 `src/internal/skill/loader/loader.go`(参考 ToolInput 反序列化)

---

## Task 3: 条件匹配器(Matcher)

**状态**:已完成

**目标**:实现 `Condition` JSON schema 解析(支持 leaf/all/any 三种基础形式)+ `Matcher.Evaluate(condition, hookCtx) bool` 评估函数,支持 eq/neq/glob/contains 四种 leaf 操作。

**影响文件**:
- `src/internal/hook/matcher/condition.go` — 新建,Condition 数据结构 + JSON 解析
- `src/internal/hook/matcher/matcher.go` — 新建,Matcher 评估函数
- `src/internal/hook/matcher/matcher_test.go` — 新建,单测覆盖所有组合

**依赖**:Task 2(HookContext 已定义)

**具体内容**:

1. **`condition.go`**:
   - `Condition struct`:
     - `All []Condition` (json:"all,omitempty")
     - `Any []Condition` (json:"any,omitempty")
     - `Field string` (json:"field,omitempty")  // leaf 字段
     - `Op string` (json:"op,omitempty")        // "eq"/"neq"/"glob"/"contains",默认 "eq"
     - `Value any` (json:"value,omitempty")      // leaf 值
   - `ParseCondition(raw json.RawMessage) (Condition, error)`:JSON → Condition 结构
   - `IsEmpty() bool`:三层都为 nil/空时返回 true(等价于"无条件")

2. **`matcher.go`**:
   - `Matcher struct`(无状态,可为零值)
   - `Evaluate(cond Condition, hookCtx *hook.HookContext) (matched bool, reason string)`:
     - cond 为 nil 或 IsEmpty → 匹配(true, "no condition")
     - All 非空 → 所有子 Evaluate 必须 true
     - Any 非空 → 任一子 Evaluate 为 true
     - leaf → 根据 Op 评估:
       - `eq`:ctxField == condValue(转 string 后相等)
       - `neq`:ctxField != condValue
       - `glob`:用 `path.Match(condValue, ctxField)`(注意 Windows 路径分隔符处理)
       - `contains`:strings.Contains(ctxField, condValue)
     - field 在 ctx 中不存在时(glob/contains/neq 视为不匹配,eq 视为不匹配;不抛错)
   - 字段解析:从 HookContext 读取字段
     - `event` / `tool_name` / `tool_input_file_path` / `message_role` / `session_id` / `workdir` / `error` → HookContext 直接字段
     - `tool_input.<key>` → 从 ToolInput map 取子键
     - `iteration` / `tool_duration_ms` / `tool_is_error` → 数值字段(转 string)
     - 其它字段返回空字符串

3. **单测要点**:
   - ParseCondition:leaf / all / any / 嵌套 all+any / 空对象
   - Evaluate 全场景:
     - 空 condition → true
     - leaf eq / neq / glob / contains 各算 PASS / FAIL
     - all 全部 PASS / 部分 FAIL → 正确
     - any 部分 PASS → true;全 FAIL → false
     - 嵌套 all+any
     - field 不存在 / 类型不匹配 → 不 panic
     - glob 处理 `*.go` / `internal/**` / 字面量
     - Windows 路径:输入 `internal\foo.go` 时 glob `internal/*.go` 应匹配

**参考资料**:
- `path/filepath.Match` 标准库文档(Go 官方:glob 匹配规则)
- 已有 glob 使用案例:`src/internal/skill/scanner.go` `HasMeta`/`GlobMatch` 等可参考
- HookContext 字段定义:Task 2 产出

---

## Task 4: 四种 Action 执行器

**状态**:已完成

**目标**:实现 4 个 `Executor`(command/http/prompt/agent),每个执行器实现统一接口 `Execute(ctx, hookCtx *hook.HookContext, vars map[string]string) error`。错误隔离(超时、panic、subprocess 失败)只记日志、不向上抛。

**影响文件**:
- `src/internal/hook/executor/executor.go` — 新建,公共接口 + 工厂 + 错误包装
- `src/internal/hook/executor/command.go` — 新建,CommandExecutor
- `src/internal/hook/executor/http.go` — 新建,HttpExecutor
- `src/internal/hook/executor/prompt.go` — 新建,PromptExecutor
- `src/internal/hook/executor/agent.go` — 新建,AgentExecutor(stub)
- `src/internal/hook/executor/*_test.go` — 新建,单测

**依赖**:Task 2(HookContext / Interpolate)

**具体内容**:

1. **`executor.go`** 公共层:
   ```go
   type Executor interface {
       Type() string                              // "command"/"http"/"prompt"/"agent"
       Execute(ctx context.Context, hookCtx *hook.HookContext, vars map[string]string) error
   }
   type Factory func(action json.RawMessage) (Executor, error)
   func ParseActionType(raw json.RawMessage) (string, error) // 提取 action.type
   func NewExecutorByType(action json.RawMessage) (Executor, error) // 工厂分发
   ```
   - 通用日志记录:zap info「hook action 开始」 + zap debug「hook action 完成」 / zap warn「hook action 失败」

2. **`command.go`** — `CommandExecutor`:
   - 配置:`Command string` / `WorkingDir string` / `Env map[string]string` / `Timeout Duration`
   - `Execute`:
     - 校验 command 非空
     - 走 `os/exec.CommandContext`,context 超时则 kill 子进程
     - cwd:WorkingDir 空时取 hookCtx.Workdir
     - env:合并 `os.Environ() + Env`(Env 覆盖同名键);用 Windows 兼容(go 默认即可)
     - 抓 stdout / stderr 写入 debug 日志
     - 退出码非 0 → 返回 `&CommandError{ExitCode, stderr}`
     - 超时 → 返回 `ErrCommandTimeout`
   - 安全:command 走现有 Bash 黑名单(`security.IsBlacklisted(cmd)`)?本任务评估后决定:本期简化,不在 hook 路径跑黑名单(避免与 tool.Bash 重复);黑名单警告写在 docs

3. **`http.go`** — `HttpExecutor`:
   - 配置:`Method string` / `URL string` / `Headers map[string]string` / `Body string` / `Timeout Duration`
   - `Execute`:
     - URL 必须 http/https scheme
     - body 用 `Interpolate` 替换变量
     - 构造 `http.Request` + `http.Client{Timeout: timeout}`
     - 2xx → success;非 2xx → 返回 `&HttpError{StatusCode, body[:512]}`
     - 网络错误 → 透传

4. **`prompt.go`** — `PromptExecutor`:
   - 配置:`Text string` / `As string`(本期固定 `"system_reminder"`,其它值 warn + 拒绝)
   - `Execute`:
     - 用 `Interpolate(text, vars)` 替换变量
     - 校验非空
     - 实际注入由 Engine 调 `PromptSink.AppendToCurrentMessage(text)` 完成
     - 本执行器只负责把「待注入文本」返回(用 `PromptResult` struct)→ Engine 收口注入

5. **`agent.go`** — `AgentExecutor`(本期 stub,Step 12 升级):
   - 配置:`Prompt string` / `MaxIterations int`(本期固定忽略,1) / `AllowTools []string` / `Timeout Duration`
   - 持有:`llm.Provider`(构造时注入) + 可选 `tool.Registry`(查 AllowTools 对应的 ToolSpec)
   - `Execute`:
     - 用 `Interpolate(prompt, vars)` 替换变量
     - 构造独立 messages:第一条 user = prompt(若 prompt 模板本身为空,返回 ErrEmptyPrompt)
     - 调一次 LLM(无 system / 单一 user 消息):`provider.Generate(ctx, llm.Request{Messages: ..., Tools: allowToolsSpecs})`
     - **不写回主会话 history**(关键:与 Step 12 完整 SubAgent 的区别)
     - LLM 响应文本写入 debug 日志(可观测)
     - 错误 / 超时 → 透传
   - **TODO 注释明确标注**:Step 12 SubAgent 上线后,本执行器升级为启动独立 SubAgent(独立 conversation / 独立 history / 独立 stream / 结果回传主会话)

6. **单测要点**:
   - command:正常执行 / 超时 kill / 退出非 0 / cwd / env 合并 / 变量替换
   - http:GET 200 / POST 200 / 404 失败 / 网络超时 / 变量替换
   - prompt:变量替换 / 空 prompt 拒绝 / as 非法值拒绝
   - agent:mock provider 验证一次 LLM 调用 / 错误透传 / 变量替换
   - 公共:panic recover(在 Execute 内层)不传播

**参考资料**:
- Bash 执行模式参考:`src/internal/tool/builtin/` 中 Bash 工具实现(参考超时 / env 处理)
- LLM Provider 接口:`src/llm/` 包(参考 Generate 签名)
- 变量插值:Task 2 `Interpolate`
- 现有日志模式:`src/internal/logger/`(`InfoCtx` / `DebugCtx` / `WarnCtx` / `ErrorCtx`)

---

## Task 5: HookEngine 编排核心

**状态**:已完成

**目标**:实现 `hook.Engine`,负责配置加载、entries 注册、事件调度、once 追踪、async 派发、错误隔离。是整个 hook 系统的中枢。

**影响文件**:
- `src/internal/hook/engine.go` — 新建,Engine 主体
- `src/internal/hook/engine_test.go` — 新建,单测覆盖 once / async / 错误隔离
- `src/internal/hook/loader.go` — 新建(loader 子包可省略,直接放在 hook 包),`LoadFromConfig(cfg *config.HookConfig) (*Engine, error)`

**依赖**:Task 1 + 2 + 3 + 4

**具体内容**:

1. **`engine.go`** — `Engine` 结构:
   ```go
   type Engine struct {
       cfg          EngineConfig
       entries      map[string][]*entry       // event → entries(顺序敏感)
       onceTracker  map[string]map[string]bool // sessionID → entry name → fired?
       mu           sync.RWMutex
   }
   type EngineConfig struct {
       Enabled       bool                     // 等同 cfg.Hook.Enabled
       DefaultTimeout time.Duration            // 默认 30s
       Logger        *zap.Logger
       LLMProvider   llm.Provider             // 仅 agent action 需要
       ToolRegistry  *tool.Registry           // 仅 agent action 需要
       PromptSink    PromptSink               // prompt action 注入接口
   }
   type entry struct {
       config   config.HookEntryConfig
       executor executor.Executor
       matcher  matcher.Matcher
   }
   ```

2. **`engine.go`** 核心方法:
   - `New(cfg EngineConfig) *Engine`:构造但不加载 entries
   - `LoadEntries(entries []config.HookEntryConfig) error`:把 entries 按 event 分组注册(同事件内按数组顺序);每条 entry 校验 + 构造 executor
   - `Dispatch(ctx context.Context, event string, hookCtx *hook.HookContext)`:核心调度
     - 检查 event 合法
     - 遍历该 event 的所有 entries(按注册顺序)
     - 对每个 entry:
       - 1) once 检查:本 sessionID+entry.Name 已触发 → skip + debug log
       - 2) condition matcher 评估:不匹配 → skip
       - 3) async 分支:`go func() { ... defer recover() ... execute entry }()`
       - 4) 同步分支:直接 execute(用 ctx.WithTimeout 包裹)
     - 任何 error / panic 只 log,不传播
     - 每个 entry 触发后:once=true 时 onceTracker 标记
   - `Stats() Stats`:返回 {EntriesTotal, FiredTotal, FailedTotal}(供 WebUI 状态栏)
   - `Shutdown(ctx context.Context)`:等待所有 async goroutine 完成(用 sync.WaitGroup)

3. **`loader.go`** — `LoadFromConfig`:
   - 接收 `*config.HookConfig` + `EngineConfig`
   - 校验每个 entry(已由 config.ValidateHookConfig 预校验,这里只检查 event 合法 + action.type 合法)
   - 构造 4 种 executor(委托 executor.NewExecutorByType)
   - `engine.LoadEntries(...)` 注册
   - 返回 engine / error

4. **`prompt_sink.go`** — `PromptSink` 接口:
   ```go
   type PromptSink interface {
       AppendToCurrentMessage(text string) error
   }
   ```
   - Engine 收到 prompt action 返回文本时,调 sink.AppendToCurrentMessage 注入
   - sink 为 nil 时,prompt action warn log + skip(降级)

5. **单测要点**:
   - LoadEntries:合法 / 非法 event / 非法 action.type
   - Dispatch:无 entries → no-op
   - once:true 时第二次同 session 触发 → skip
   - once:false 时重复触发都执行
   - async:true 时不阻塞 Dispatch 返回
   - 同步 hook panic → recover,不传播
   - 同步 hook 返回 error → log,不传播
   - condition 不匹配 → skip
   - 多个 entries 匹配同一事件 → 按顺序串行执行
   - Stats 计数正确(已配置 / 已触发 / 失败)

**参考资料**:
- Step 7 上下文压缩协调器:`src/internal/engine/conversation/compaction_coordinator.go`(参考异步派发 + WaitGroup)
- Step 3 AgentLoop `AgentLoopHooks` 回调模式:`src/internal/engine/conversation/agent_loop.go` `AgentLoopHooks`(参考回调注入风格)
- logger 用法:`src/internal/logger/`

---

## Task 6: 与 Agent Loop / ToolHandler / Session / Compact 集成

**状态**:已完成

**目标**:把 HookEngine 注入到 7 个集成点(program_start/exit、session_start/end、iteration_start/end、pre_tool_use、post_tool_use、pre_message、post_message、compact、error),每个集成点用 defer recover 包裹,确保 hook 异常不影响主流程。

**影响文件**:
- `src/internal/hook/integration/loop.go` — 新建,Agent Loop 集成
- `src/internal/hook/integration/tool.go` — 新建,ToolHandler 集成
- `src/internal/hook/integration/session.go` — 新建,Session 集成
- `src/internal/hook/integration/compact.go` — 新建,Compact 集成
- `src/internal/hook/integration/prompt.go` — 新建,PromptBuilder 集成(prompt 注入实现)
- `src/main.go` — 修改,新增 hook.Engine 构造 + LoadFromConfig + 各集成点 wire 调用
- 各被集成的源文件 — 微调,在关键节点加 hook.Dispatch 调用

**依赖**:Task 5(Engine 已就绪)

**具体内容**:

1. **`integration/loop.go`**:
   - `WireAgentLoop(engine *hook.Engine, mgr *conversation.ConversationManager)`:
     - 在 `agent_loop.go` 的 for 循环顶部插入 `engine.Dispatch(ctx, hook.EventIterationStart, hook.NewIterationContext(...))`
     - 在每轮迭代结束(LLM 响应处理完后)插入 `engine.Dispatch(... EventIterationEnd ...)`
     - 在 StopReason=Error 分支前插入 `engine.Dispatch(... EventError ...)`
   - 实际修改:`src/internal/engine/conversation/agent_loop.go` 在 4 个位置加 dispatch 调用

2. **`integration/tool.go`**:
   - `WireToolHandler(engine *hook.Engine, h *conversation.ToolHandler)`:
     - 在 `tool_handler.go` 的 `doExecute` 权限检查**前**插入 pre_tool_use dispatch(用 `llm.ToolUseBlock` 构造 context)
     - 在 `doExecute` execute 后、`result` 封装前插入 post_tool_use dispatch(用 `output` / `err` 构造 context)
     - 工具 hookCtx 含 ToolInput 完整 map + ToolInputFilePath 自动提取
   - 实际修改:`src/internal/engine/conversation/tool_handler.go` 在 2 个位置加 dispatch

3. **`integration/session.go`**:
   - `WireSession(engine *hook.Engine, sm *session.Manager)`:
     - session_create / session_resume 成功后:dispatch session_start
     - session_clear / session_switch 之前:dispatch session_end
   - 实际修改:session manager 相关函数体加 dispatch

4. **`integration/compact.go`**:
   - `WireCompact(engine *hook.Engine, coord *conversation.CompactionCoordinator)`:
     - 在两层压缩任何一层完成时 dispatch compact(用 summary 文本 + before/after token 数构造 context)
   - 实际修改:compaction_coordinator.go 完成回调位置加 dispatch

5. **`integration/prompt.go`**:
   - 实现 `PromptSink` 接口,绑定到 PromptBuilder / ConversationManager
     - 提供 `AppendToCurrentMessage(text string) error`:把 text 拼到「当前轮的 user 消息尾部」/ 若 Agent Loop 已在本轮则拼到本轮末尾
     - 用 `<system-reminder>` 标签包裹(LLM 明确感知)
     - Engine 在 prompt action 触发时调 sink.AppendToCurrentMessage
   - 实际修改:`src/internal/engine/conversation/manager.go` 在 `AddUserMessage` / `RunTurn` 处增加 sink 钩子

6. **`main.go`** 修改:
   - 在 run() 函数顶层构造 hook.Engine(`EngineConfig` 含 Logger / LLMProvider / ToolRegistry / PromptSink)
   - 加载 hook config + 构造 entries
   - `WireAgentLoop(engine, mgr)` / `WireToolHandler(engine, toolHandler)` / `WireSession(engine, sessionMgr)` / `WireCompact(engine, coord)` / `RegisterPromptSink(engine, b)` 一连串 wire 调用
   - program_start 在所有 wire 完成后 dispatch(一次)
   - program_exit 在 defer 链 / cleanup 路径 dispatch(通过 `engine.Shutdown(ctx)` 等异步 hook 收尾)

7. **集成安全**:
   - 每个集成点的 dispatch 用 `defer recover()` 包裹(防止 hook panic 影响主流程)
   - dispatch 同步调用 + 内部 async 派发,主流程不阻塞(async hook 在 goroutine 跑)

**单测要点**:
- 每个集成点:hook 触发 + 主流程行为不变(双轨断言)
- prompt sink:`AppendToCurrentMessage` 把 text 拼到正确位置
- defer recover:故意 panic 的 hook → 主流程不受影响

**参考资料**:
- Agent Loop 集成点:`src/internal/engine/conversation/agent_loop.go` 关注 113-275 行主循环
- ToolHandler 集成点:`src/internal/engine/conversation/tool_handler.go` 关注 196-286 行 doExecute
- session manager 集成点:Step 9 落地位置(可由 worker 现搜)
- 现有 logger 用法:`src/internal/logger/`

---

## Task 7: prompt action 注入实现 + HooksAwarenessSource + 既有 Skill 补充

**状态**:已完成

**目标**:让 LLM 感知到 hook 系统的存在(SP 自感知 + 既有 Skill 自描述);让 prompt action 的文本能注入到对话上下文;确保 LLM 能通过 `config-management` 查询 Hook 配置,通过 `codebase-overview` 查询 Hook 实现。

**影响文件**:
- `src/internal/engine/prompt/sources/hooks_awareness.go` — 新建,`HooksAwarenessSource`(告诉 LLM 有 hook 系统)
- `src/internal/engine/prompt/sources/static_test.go` — 修改,补充 HooksAwarenessSource 单测
- `src/internal/skill/builtin/config-management/SKILL.md` — 修改,补充 Hook 配置索引与触发词
- `src/internal/skill/builtin/config-management/reference/hook.md` — 新增/修改,补充 `hook` 配置 schema / 示例 / 排障
- `src/internal/skill/builtin/codebase-overview/SKILL.md` — 修改,Hook 模块索引从 stub 改为已实现
- `src/internal/skill/builtin/codebase-overview/reference/hook-system.md` — 修改,补充 Hook 源码实现导览
- `src/main.go` — 修改,注册 HooksAwarenessSource 到 Builder

**依赖**:Task 6(集成已就绪);Task 5(PromptSink 协议已定)

**具体内容**:

1. **`hooks_awareness.go`** — `HooksAwarenessSource`:
   - 常量 `hooksAwarenessContent`(~50-70 token):固定文案,告诉 LLM
     - 存在 hook 系统,配置位置 `~/.codepilot/setting.json + <cwd>/.codepilot/setting.json`
     - 支持 12 类事件(command/http/prompt/agent 四种 action)
     - 配置 schema / 示例 / 默认值见 `config-management` Skill;源码实现见 `codebase-overview` Skill
     - 修改用 ReadFile + EditFile/WriteFile
   - `Name() string` 返回 `"hooks_awareness"`
   - `Assemble(ctx, env) (Section, error)`:
     - Placement=PlacementSystem(进 Anthropic cache 复用)
     - Content=常量
     - Tokens=tokens.Estimate(content)
   - 沿用 Step 10.1 `ConfigAwarenessSource` 范式

2. **既有 Skill 文档补充**:
   - `config-management` frontmatter description 补充 Hook / event / action / condition 等触发词
   - `config-management` 入口新增 Hook 索引,`reference/hook.md` 覆盖 12 类事件、4 种 action、condition DSL、HookContext 变量、async / once / timeout、完整示例与排障
   - `codebase-overview` frontmatter 和模块索引把 Hook 从 stub 更新为已实现模块
   - `codebase-overview/reference/hook-system.md` 补充 Hook 源码结构、Engine 调度、executor、matcher、集成点与测试入口
3. **`main.go`** 注册:
   - 在 prompt.NewBuilder(...) 调用链尾追加 `sources.NewHooksAwarenessSource()`
   - (位置:在 CodebaseAwarenessSource 之后,与现有自感知 Source 同区)

4. **单测要点**:
   - HooksAwarenessSource.Assemble:Content/Tokens/Placement 断言
   - `config-management` description 覆盖 hook / event / action / condition 等关键触发词
   - `codebase-overview` Hook reference 已从 stub 改为实现导览且单文件 < 16KB

**参考资料**:
- Step 10.1 完美模板:`src/internal/engine/prompt/sources/config_awareness.go`
- Step 10.1 SKILL.md 模板:`src/internal/skill/builtin/config-management/SKILL.md`
- Skill 加载流程:`src/internal/skill/scanner.go` `LoadAll`(builtin 走 embed FS)
- tokens.Estimate:`src/internal/engine/prompt/tokens/tokens.go`

---

## Task 8: 端到端验证 + 现有功能回归 + smoke test

**状态**:已完成

**目标**:跑通 8+ 个端到端验证场景(command/http/prompt/agent action × 多个事件触发),并回归 Step 1~10 的核心能力,确保零回归。同时编写程序化 smoke test 锁定关键不变量。

**影响文件**:
- `src/internal/hook/e2e_test.go` — 新建,端到端验证(用真实 Engine + mock executor)
- `src/internal/hook/integration/integration_smoke_test.go` — 新建,集成 smoke test
- `docs/step11-Hook系统/tasks.md` — 本任务完成后把自身状态改为「已完成」

**依赖**:Task 1-7 全部完成

**具体内容**:

1. **构建并启动**:`make build` 或 `powershell -File build/build.ps1`,启动二进制,打开 WebUI

2. **验证场景 A — command action / pre_tool_use**:
   - 在 setting.json 配置:`event: pre_tool_use` + condition `tool_name=WriteFile` + action command `echo "pre-write: $TOOL_INPUT_FILE_PATH" >> /tmp/hook-test.log`
   - User: 「请用 WriteFile 写一个文件到 test.txt」
   - 预期:命令执行,`/tmp/hook-test.log` 追加「pre-write: test.txt」
   - 校验:命令退出码 0 + log 文件内容匹配

3. **验证场景 B — command action / post_tool_use + once**:
   - 配置:`event: post_tool_use` + tool_name=ReadFile + command `echo "post-read: $TOOL_INPUT_FILE_PATH"` + `once: true`
   - User: 让 Agent 多次 ReadFile 同一文件
   - 预期:第一次触发,后续不触发
   - 校验:hook 日志显示只触发一次

4. **验证场景 C — http action**:
   - 配置:`event: post_tool_use` + tool_name=WriteFile + action http POST 到本地 echo server(用 net/http/httptest.Server)
   - User: 让 Agent WriteFile
   - 预期:HTTP POST 收到 + body 含 `$TOOL_INPUT_FILE_PATH` 替换值
   - 校验:echo server 记录的 request body 匹配

5. **验证场景 D — prompt action 注入**:
   - 配置:`event: pre_tool_use` + tool_name=WriteFile + action prompt text `<system-reminder>该文件使用 tabs 缩进</system-reminder>`
   - User: 让 Agent WriteFile 一个 .go 文件
   - 预期:Agent 写入文件后,下一轮 LLM 调用 user 消息尾部出现 `<system-reminder>` 段
   - 校验:WebUI 对话栏 / 日志中能看到 reminder 文本被附加(可观测即可,不强制 UI 显示)

6. **验证场景 E — agent action stub**:
   - 配置:`event: post_tool_use` + tool_name=WriteFile + action agent prompt `请评论: $TOOL_INPUT_FILE_PATH` + `allow_tools: []`
   - User: 让 Agent WriteFile
   - 预期:agent action 调一次 LLM,LLM 输出评论写日志;不污染主会话 history
   - 校验:Agent 主对话中无评论内容,但日志有评论文本

7. **验证场景 F — condition 评估**:
   - 配置:`event: pre_tool_use` + condition `tool_input.file_path glob '*.go'` + action command `echo "go file: $TOOL_INPUT_FILE_PATH"`
   - User: 让 Agent 写 a.go 和 a.txt
   - 预期:a.go 触发 hook,a.txt 不触发
   - 校验:log 文件只含 a.go 行

8. **验证场景 G — 错误隔离**:
   - 配置:`event: pre_tool_use` + tool_name=WriteFile + action command `false`(故意退出码非 0)
   - User: 让 Agent WriteFile
   - 预期:hook 失败,主 Agent Loop 继续执行,WriteFile 仍成功
   - 校验:工具块显示 success,日志含 hook 失败 warn

9. **验证场景 H — async 不阻塞**:
   - 配置:`event: pre_tool_use` + tool_name=WriteFile + action command `sleep 5` + `async: true`
   - User: 让 Agent WriteFile
   - 预期:WriteFile 不等 hook 完成(立即执行),hook 在后台跑 5s
   - 校验:工具块显示 success,5s 后日志含 hook 完成

10. **验证场景 I — 全事件触发**:
    - 配置:`event: program_start` / `event: session_start` / `event: iteration_start` / `event: pre_tool_use` / `event: post_tool_use` / `event: iteration_end` / `event: session_end` / `event: program_exit` 各配 1 条 echo command
    - 启动 → User 让 Agent 跑一轮 → /clear → 退出
    - 预期:8 个事件依次触发,log 文件含 8 行
    - 校验:log 顺序与预期一致

11. **SP 自感知校验**:
    - 打开 WebUI SP 面板
    - 应见 `hooks_awareness` 段,Tokens < 100
    - `/skills` 列表应继续显示 `config-management` 与 `codebase-overview`,不新增独立 Hook 专属 Skill

12. **现有功能回归**:
    - F.1 WebUI 启动:web 启动链路无破坏
    - F.2 6 个内置工具:tool/builtin 测试全 PASS
    - F.3 6 条 slash 命令:command/slash 测试全 PASS
    - F.4 Step 5 权限:security 包全 PASS(`hooks.enabled=false` 降级不破坏权限系统)
    - F.5 Step 6 MCP:mcp 包全 PASS
    - F.6 Step 7 上下文压缩:compact 包全 PASS(compact 事件触发不破坏压缩流程)
    - F.7 Step 8 记忆:memory 包全 PASS
    - F.8 Step 9.1 slash 注册:slash 包全 PASS
    - F.9 Step 10/10.1/10.2 Skill:skill 全 5 包测试 PASS(hooks_awareness Source 注册不破坏 Skill)
    - F.10 Anthropic prompt cache:`hooks_awareness` Placement=System + Cacheable=true 不破坏
    - F.11 `go test ./...` 全部 PASS

13. **程序化 smoke test**:
    - `e2e_test.go`:覆盖 8+ 场景的程序化版本(用真实 Engine + mock executor / httptest server)
    - `integration_smoke_test.go`:覆盖 7 个集成点的「hook 触发 + 主流程行为不变」双轨断言

14. **全部通过后,更新**:
    - `docs/step11-Hook系统/tasks.md`:把 Task 8 状态改为 `已完成`
    - `.harness/PROGRESS.md`:追加 Step 11 完成条目(由主会话在阶段 C 整步收尾时统一处理,Task Worker 边界外)




