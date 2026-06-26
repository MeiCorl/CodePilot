# 工具系统 — CodePilot 实现原理

> 隶属 Step 2（工具系统集成）+ Step 3（ReAct 与 Agent Loop）| 架构层:第 3 层 工具层 | 核心入口:`src/internal/tool/registry.go`

## §1 模块定位

工具系统位于第 3 层 工具层,是 Agent 的「双手」—— 提供与外部世界交互的能力(文件读写、Shell 执行、代码搜索等)。Step 3 在此基础上加入 Agent Loop 与 ReAct 调度,让 LLM 能自主决定调用哪个工具。

- **统一 `Tool` 接口**(`src/internal/tool/tool.go:48`)—— `Name / Description / InputSchema / Permission / Execute`
- **全局 Registry**(`src/internal/tool/registry.go:12`)—— `map[string]Tool + sync.RWMutex`,支持 MCP / Skill / SubAgent 等后续工具动态注册
- **6 个内置工具**:ReadFile / WriteFile / EditFile / Bash / Glob / Grep(`src/internal/tool/builtin/`)
- **tool_use schema 转换**:`ToolSpec` → Anthropic `ToolUnionParam` / OpenAI `ChatCompletionToolParam`
- **Agent Loop ReAct 调度**:`ConversationManager.RunAgentLoop` 迭代 `思考→决策→行动→观察`
- **5 种终止原因**:`end_turn / max_iterations / context_overflow / user_aborted / tool_error`

## §2 核心数据结构

- `ToolPermission`(tool.go:13)— `PermRead / PermWrite / PermExec` 三级权限分级
- `Tool`(tool.go:48)— 工具统一接口
- `BaseTool`(tool.go:77)— 元数据公共字段,内置到具体工具结构体即可减少样板
- `Registry`(registry.go:12)— 全局工具注册表,`Register / Get / Names / Count`
- `ToolSpec`(registry.go)— Provider 视角的工具描述,字段 `Name / Description / InputSchema []byte`
- `ToolHandler`(`src/internal/engine/conversation/tool_handler.go:75`)— 工具执行协调器,字段 `registry / timeout / workdir / interceptor / middlewares / onStart / onEnd`
- `ToolExecutionEvent`(tool_handler.go:28)— 工具生命周期事件,`Status` 字段含 `running / completed / error / aborted`
- `AgentLoopConfig`(agent_loop.go:22)— `MaxIterations / ContextSafetyMargin / ContextWindowSize`
- `StopReason`(agent_loop.go)— `end_turn / max_iterations / context_overflow / user_aborted / tool_error`
- `AgentLoopResult`(agent_loop.go:53)— `FinalText / Iterations / TotalToolCalls / StopReason / Aborted / Error`
- `TurnHooks`(manager.go:366)— `OnStreamChunk / OnToolUse / OnToolResult / OnError / OnCompaction`
- `TurnResult`(manager.go:390)— 单轮对话最终结果

## §3 关键流程

### 3.1 Tool 接口 + Registry 注册

6 个内置工具的注册在 `src/internal/tool/builtin/register.go`:

1. `init()` 或 `RegisterWithOptions(reg, workdir)` 把 `NewReadFileTool / NewWriteFileTool / NewEditFileTool / NewBashTool / NewGlobTool / NewGrepTool` 注册到 `tool.Registry`
2. main.go 在启动期调 `RegisterWithOptions(reg, workdir)` 完成 6 个工具注册
3. `ConvertToSpecs(reg)`(`src/internal/tool/tool_spec.go`)把 Registry 中的 Tool 转 `[]ToolSpec` 给 LLM Provider

[Why] 统一接口 + Registry:**Why** 工具实现只需关注 Execute,新增工具(MCP / Skill)无需改 engine;ToolSpec 与 Tool 解耦,Provider 转换层不感知具体工具类。

### 3.2 Bash 工具的黑名单与平台分支

`BashTool.Execute`(`src/internal/tool/builtin/bash.go:61`)流程:

1. `cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", utf8Setup+in.Command)`(Windows)或 `exec.CommandContext(ctx, "sh", "-c", in.Command)`(Unix)(`bash.go:83-89`)
2. **黑名单检查已提升至拦截器层**(`security.Checker.Decide` 硬安全预检,bash.go:70 注释)
3. `limitedWriter` 截断 stdout/stderr 各 1MB 防内存撑爆(bash.go:91-92)
4. 退出码非零时 `formatBashOutput` 拼 stdout + stderr + exit code 返回(LLM 友好文本)
5. 中文环境解码:优先 UTF-8,失败时 GBK → UTF-8 兜底(`decodeOutput` bash.go:171)

[Why] 黑名单提到拦截器层:**Why** 黑名单是权限决策而非工具实现细节;拦截器层(`security.Interceptor`)对所有工具统一拦截,工具自身无需关心黑名单规则。

### 3.3 路径类工具的沙箱解析

`SandboxMiddleware`(`src/internal/security/sandbox_middleware.go:183`)在 Tool.Execute 前置路径校验:

1. `IsPathTool(toolName)` 判断是否路径类工具(ReadFile / WriteFile / EditFile / Glob / Grep)
2. 提取 `params[paramKey]`(如 ReadFile 的 `file_path`)
3. `ResolveInSandboxWithRoots(pathStr, workdir, readRoots)`(`sandbox.go:64`)规范化 + 范围校验
4. 越界时 `ruleProvider.MatchPathRule` 查路径级 allow 规则,命中则放行,未命中则拦截
5. 合法路径经 `WithPathResolver(ctx, resolver)` 注入 ctx,工具 Execute 内 `resolvePathFromContext(ctx, "file_path")` 直接拿到 absPath

[Why] 沙箱独立于权限层:**Why** 沙箱是「路径合法性」的硬兜底,无法被配置关闭;权限层负责「Allow/Ask/Deny」决策。两层纵深防御,即使配置错误沙箱仍能兜底。

### 3.4 Agent Loop ReAct 调度(本模块核心)

`ConversationManager.RunAgentLoop(ctx, provider, sp, toolSpecs, toolHandler, cfg, hooks) AgentLoopResult`(manager.go:469)流程:

1. 进入循环 `for i := 1; i <= cfg.MaxIterations; i++`
2. 调 `runOneLLM(ctx, provider, sp, toolSpecs, hooks.TurnHooks)` 发起 LLM 流式调用
3. 收到 `ToolUses` 时调 `toolHandler.ExecuteBatch(toolUses, hooks)`(并行执行多个 tool_use,Step 3 引入)
4. 把工具结果作为 `ToolResultBlock` 追加到 history(`m.AddToolResults(...)`)
5. 检查停止条件:
   - **end_turn**:LLM 返回 `tool_use` 为空 + 文本非空 → 自然结束
   - **max_iterations**:达到 `cfg.MaxIterations` 上限 → 调 `injectTerminationPrompt` 请求 LLM 总结
   - **context_overflow**:Provider 返回 `prompt_too_long` → 调 `emergencyCompactOnWallHit`(Step 7 撞墙兜底)
   - **user_aborted**:`ctx.Done()` → 写 `abortMarker` 占位保持对话结构完整
   - **tool_error**:工具执行错误 → 错误回灌让 LLM 自行决策(下一步是否重试)
6. 循环结束后 `ensureNonEmptyReply` 确保 finalText 非空(避免 LLM 全程只调工具不说话)
7. 返回 `AgentLoopResult{FinalText, Iterations, TotalToolCalls, StopReason, Aborted, Error}`

[Why] 5 种终止原因显式枚举:**Why** 业务层(WebUI)能据此做差异化展示(用户中断与 LLM 自然结束 UI 表现不同);日志层能据此统计「为什么对话结束」优化 max_iterations 等参数。

### 3.5 工具并行执行(Step 3)

`ToolHandler.ExecuteBatch(toolUses []ToolUseBlock) []ToolResultBlock`(tool_handler.go:335)流程:

1. 按 `tool.Permission` 分组:
   - **PermRead**(ReadFile/Glob/Grep)→ 可并行(只读无副作用)
   - **PermWrite**(WriteFile/EditFile)→ 按顺序串行(避免文件锁竞争 + 写入顺序依赖)
   - **PermExec**(Bash)→ 顺序串行(子进程可能改全局状态)
2. 并行组用 `errgroup.Group` + `errgroup.SetLimit(4)` 控制并发上限
3. 每个工具执行前后 fire `OnStart / OnEnd` 回调 → handler 把事件转 WS 业务消息推到前端

[Why] 按权限分级调度:**Why** 并发执行能加速多文件读取,但写入/执行类工具并发可能导致竞态;`errgroup.SetLimit(4)` 限制并发避免同时打开 100 个文件句柄。

## §4 与其他模块的依赖

- **上游**(工具模块依赖):
  - `internal/security`(`src/internal/security/`)— 沙箱 / 黑名单 / 权限拦截器
  - `internal/engine/conversation`(ConversationManager)— `provider / sp / toolSpecs / toolHandler` 编排
  - `internal/llm`(Message / ContentBlock)— tool_use / tool_result 在 Content 层表达
- **下游被依赖**:
  - `internal/mcp/adapter`(MCP 工具自动注册到 tool.Registry)— `src/internal/mcp/adapter/registry.go`
  - `internal/skill/adapter/tool.go`(`use_skill` 工具)— 注册到 tool.Registry
  - `internal/interaction/web/handler`(runStream 编排)— 通过 `toolHandler.ExecuteBatch` 触发执行

## §5 设计决策

### 决策 1:`Tool` 接口 + Registry 模式

- **问题**:不同工具(内置 / MCP / Skill / 未来的 SubAgent)如何统一发现与调用
- **方案**:`Tool` 接口 + 全局 `Registry`(map + RWMutex)
- **理由**:**Why** 「新增工具不改系统代码」是核心扩展性诉求;接口约束 5 个方法,工具实现只需关注 Execute;Registry 提供 O(1) 查找

### 决策 2:`BaseTool` 公共字段减少样板

- **问题**:每个工具都需实现 Name/Description/InputSchema/Permission,大量重复代码
- **方案**:`BaseTool` 嵌入到具体工具,只填元数据,Execute 单独实现
- **理由**:**Why** 减少 80% 样板代码;新增工具只需 5 行元数据 + Execute 函数体

### 决策 3:Agent Loop 终止原因显式枚举

- **问题**:LLM 自然结束 / 迭代上限 / 用户中断 / 上下文溢出 / 工具错误 五种情况诊断价值差异大
- **方案**:`StopReason` 枚举 + `AgentLoopResult.StopReason` 字段
- **理由**:**Why** WebUI 能据此做差异化展示(`user_aborted` 与 `end_turn` UI 表现不同);日志能据此统计「为什么对话结束」

### 决策 4:ReAct 循环放 ConversationManager 而非独立类

- **问题**:ReAct 循环是否要独立成 AgentLoop 类?
- **方案**:**Why** `ConversationManager.RunAgentLoop` 是入口,内部调 `AgentLoop(...)` 方法;AgentLoop 是循环骨架,ConversationManager 是会话所有者
- **理由**:会话是 ReAct 循环的「宿主」,消息历史与上下文压缩都依附会话;独立成类会让两者的状态管理割裂

### 决策 5:工具执行按权限分级调度

- **问题**:多个 tool_use 并行能加速,但写入/执行类工具并发可能导致竞态
- **方案**:按 `ToolPermission` 分组:Read 并行(限并发 4),Write/Exec 串行
- **理由**:**Why** 并发读只读无副作用可放心并行;并发写会触发文件锁竞争甚至覆盖;并发执行子进程可能改全局状态(Git commit、网络请求等)

## §6 关键文件索引

| 路径 | 角色 |
|------|------|
| `src/internal/tool/tool.go:13` | `ToolPermission` 权限分级枚举 |
| `src/internal/tool/tool.go:48` | `Tool` 接口定义 |
| `src/internal/tool/tool.go:77` | `BaseTool` 公共字段 |
| `src/internal/tool/registry.go:12` | `Registry` 全局工具注册表 |
| `src/internal/tool/tool_spec.go` | `ToolSpec` Provider 视角工具描述 |
| `src/internal/tool/file_diff.go` | `FileDiffStore` 进程内 LRU diff 存储 |
| `src/internal/tool/builtin/read_file.go:43` | `ReadFileTool` ReadFile 工具 |
| `src/internal/tool/builtin/write_file.go:37` | `WriteFileTool` WriteFile 工具 |
| `src/internal/tool/builtin/edit_file.go:38` | `EditFileTool` EditFile 工具 |
| `src/internal/tool/builtin/bash.go:38` | `BashTool` Bash 工具 |
| `src/internal/engine/conversation/agent_loop.go:94` | `AgentLoop` ReAct 循环入口 |
| `src/internal/engine/conversation/agent_loop.go:31` | `StopReason` 5 种终止原因 |
| `src/internal/engine/conversation/manager.go:469` | `RunAgentLoop` 对外入口 |
| `src/internal/engine/conversation/manager.go:520` | `runOneLLM` 单次 LLM 流式调用 |
| `src/internal/engine/conversation/tool_handler.go:75` | `ToolHandler` 工具执行协调器 |
| `src/internal/engine/conversation/tool_handler.go:335` | `ExecuteBatch` 并行/串行调度 |
| `src/internal/security/sandbox_middleware.go:183` | `SandboxMiddleware` 沙箱中间件 |
| `src/internal/security/sandbox.go:64` | `ResolveInSandboxWithRoots` 路径校验 |
| `src/internal/mcp/adapter/registry.go` | MCP 工具自动注册到 tool.Registry |

## §X Agent Loop 调度与 ReAct(子章节)

> 隶属 Step 3(ReAct 与 Agent Loop)| 引擎层是第 2 层,但调度实现放在工具层(本文件)。详见 spec 「ReAct 循环归属决策」

### AgentLoopConfig 与 Hooks

```go
type AgentLoopConfig struct {
    MaxIterations        int  // 默认 50
    ContextSafetyMargin  int  // 上下文安全余量
    ContextWindowSize    int  // 模型上下文窗口总大小
}

type AgentLoopHooks struct {
    TurnHooks                              // 嵌入单轮回调
    OnIterationStart func(iteration, maxIterations int)
    OnLoopDone       func(result AgentLoopResult)
}
```

### 撞墙兜底(Step 7 接入)

`runOneLLM`(`manager.go:520`)中 `provider.StreamChat` 返回 `IsContextTooLongError(err)` 时:

1. `m.emergencyCompactOnWallHit(ctx, provider, hooks)` 调协调器 `EmergencyCompact`(强制第二层摘要 + 无视余量 + 临时豁免熔断)
2. 紧急压缩成功后 `messages = m.GetContext()` 拿压缩后历史,重试一次 `StreamChat`
3. 重试成功 → 继续正常消费流;失败 → 返回原始超长错误(不吞异常)

[Why] 紧急压缩复用 `Compact(manual=true)` 路径:**Why** 紧急模式语义等价 manual 触发(都无视余量),复用同一路径避免熔断/计数行为分叉。

### 收尾提示与空回复兜底

`injectTerminationPrompt`(agent_loop.go:284):在 max_iterations / context_overflow 时注入 user 消息「请总结…」让 LLM 给出收尾回复。

`ensureNonEmptyReply`(agent_loop.go:313):当 AgentLoop 结束时 finalText 为空(LLM 全程只调工具),注入提示消息请求 LLM 总结;补充回复失败时用兜底消息「(任务已执行完成,但模型未返回可显示的文本内容)」。