# Step 11 — Hook 系统

## 背景

CodePilot 已具备 LLM 交互、工具系统、Agent Loop、权限控制、MCP、Skill、上下文管理、自动记忆、快捷命令等能力,构成了一个 AI Coding Agent 的核心骨架。但在「用户/团队对 Agent 工作流的定制与增强」这一维度上,还缺少一类基础能力——**Hook**:让用户可以在 Agent 生命周期的关键节点(启动、会话开始、工具调用前后、消息发送前后、压缩、退出、异常)定义自己的自动化动作(跑 shell 命令、注入提示词、HTTP 通知、调用 LLM 子任务),而无需修改 CodePilot 源码。

Hook 是 Claude Code / Cursor / Codex / Continue 等主流 AI Coding Agent 的事实标准能力,各产品的事件类型与触发时机大同小异,语义稳定;本步骤要让 CodePilot 拥有同等能力,并与现有 Skill / 权限 / 上下文 / Agent Loop 等模块**无缝协作**,不引入新的纵深依赖、不破坏 5 层架构。

## 目标用户

1. **个人开发者**——为自己的项目配置 Hook(比如 "WriteFile 后自动跑 prettier"、"ReadFile 前先在 SP 注入一段该项目的代码风格指南"),放到 `<cwd>/.codepilot/setting.json`
2. **跨项目用户**——把通用工作流(比如 "Agent 退出时 Slack 通知")放到 `~/.codepilot/setting.json`
3. **CodePilot 集成方**——通过 HTTP action 把 Agent 行为外推到 CI/CD、监控告警、团队协作平台
4. **CodePilot 内置 Skill 作者(预留)**——后续 Skill 可以声明自己需要的 Hook(留接口,本步骤不实现)

## 能力清单

### A. 事件类型(Event)

Hook 系统支持以下 12 类事件,事件枚举 **预留可扩展**(后续可在不破坏现有 Hook 配置文件的前提下追加):

| 分类 | 事件名 | 触发时机 |
| --- | --- | --- |
| 系统级 | `program_start` | CodePilot 二进制启动成功、配置加载完成后(每个进程一次) |
| 系统级 | `program_exit` | CodePilot 即将退出(优雅关闭 + 异常路径都触发) |
| 系统级 | `compact` | 上下文压缩完成(Step 7 的两层压缩任何一层完成后触发) |
| 系统级 | `error` | Agent Loop 不可恢复错误(StopReason=error,本轮失败前) |
| 会话级 | `session_start` | 新建会话(`/new`)或恢复会话(`/resume`)成功后 |
| 会话级 | `session_end` | 用户主动 `/clear` 或切走当前会话时 |
| 轮次级 | `iteration_start` | Agent Loop 每轮迭代开始(每次 LLM 调用前) |
| 轮次级 | `iteration_end` | Agent Loop 每轮迭代结束(LLM 响应已写入 history) |
| 工具级 | `pre_tool_use` | 工具执行前(权限拦截 + 中间件链之前) |
| 工具级 | `post_tool_use` | 工具执行后(无论成功/失败/超时/取消) |
| 消息级 | `pre_message` | 用户消息即将发往 LLM 之前(已拼好完整 user 消息后) |
| 消息级 | `post_message` | LLM 最终回复已写入 history 后 |

事件枚举用小写 snake_case 字符串常量;每个事件触发时构造一个 `HookContext` 携带上下文信息(见 §E)。

### B. 触发条件(Condition)

条件 DSL 简洁但表达力足够,支持三种基础形式 + 两种组合形式:

- **基础 leaf**(对象):`{ field: <string>, op: <eq|neq|glob|contains>, value: <any> }`
  - `field` 支持 `event`(固定为事件名)/ `tool_name` / `session_id` / `iteration` / 任意 `tool_input.*` 字段(如 `tool_input.file_path`、`tool_input.command`)
  - `op`: `eq`(默认,字符串相等)、`neq`、`glob`(用 `path.Match` 风格 glob,如 `*.go`)、`contains`(子串包含)
- **all 组合**(数组):所有子条件为真才匹配,空数组视为真
- **any 组合**(数组):任一子条件为真就匹配,空数组视为假
- 顶层 condition 缺省/`null` 视为永远匹配
- 不支持正则、算术、跨字段计算 — 简洁可控,覆盖 90% 场景

### C. 配置文件结构

`setting.json` 新增 `hooks` 段,沿用 Step 5 权限 / Step 8 记忆 / Step 10 Skill 的「全局 + 项目级字段级合并」语义(项目级 `<cwd>/.codepilot/setting.json` 覆盖全局 `~/.codepilot/setting.json`)。

结构:

```json
{
  "hooks": {
    "enabled": true,
    "entries": [
      {
        "name": "auto-format-go",
        "event": "post_tool_use",
        "condition": {
          "all": [
            { "field": "tool_name", "op": "eq", "value": "WriteFile" },
            { "field": "tool_input.file_path", "op": "glob", "value": "*.go" }
          ]
        },
        "action": {
          "type": "command",
          "command": "prettier --write $TOOL_INPUT_FILE_PATH",
          "timeout": "10s",
          "env": { "NO_COLOR": "1" }
        },
        "async": false,
        "once": false
      }
    ]
  }
}
```

`entries[]` 每条 hook 配置字段:

- `name`:唯一标识(同一事件内可重复,执行按数组顺序;同名仅便于排错)
- `event`:事件名(必须 12 类之一)
- `condition`:可选,缺省/null/空对象都视为匹配
- `action`:四选一,见 §D
- `async`:true 时 action 在 goroutine 中执行,不阻塞主 Agent Loop(执行日志仍记录);false 时同步阻塞
- `once`:true 时本会话生命周期内只触发一次(第二次起跳过 + 记 debug 日志)

### D. 四种 Action 类型

四类 action 共享同一外壳:`type` 字段必填,类型特定字段按 type 分支。

**D.1 command** — 本地 Shell 命令

```yaml
action:
  type: command
  command: "prettier --write $TOOL_INPUT_FILE_PATH"   # 支持变量插值
  working_dir: "<optional, 缺省取 toolWorkdir>"      # 子进程 cwd
  env: { "NO_COLOR": "1" }                            # 合并到子进程 env(同名键覆盖)
  timeout: "10s"                                      # 字符串 duration,默认 30s
```

执行语义:

- 走 `os/exec.CommandContext`,超时未完成则 kill 子进程 + 返回 timeout 错误
- 退出码非 0 视为 action 失败,记 warn 日志,**不传播到主 Agent Loop**
- stdout / stderr 写入 debug 日志(可观测性)

**D.2 prompt** — 注入到当前轮 user 消息尾部

```yaml
action:
  type: prompt
  text: "<system-reminder>该项目的 Go 文件使用 tabs 缩进,提交前请用 gofmt 格式化。</system-reminder>"
  as: "system_reminder"   # 当前仅支持这一种(预留扩展)
```

执行语义:

- 立即把 `text` 拼到「当前轮的 user 消息尾部」(若 Agent Loop 已在本轮,则拼到本轮末尾;若在轮次之间触发,则作为下轮 user 消息的前缀追加)
- 用 XML 标签 `<system-reminder>...</system-reminder>` 包裹,LLM 明确感知到「这是规约边界」
- 不进入 system 字段(不破坏 Anthropic prompt caching),不修改历史 messages
- 与 Skill 加载 / Memory 索引等已有的 PlacementUserMessage 共用同一条组装通道(由 Builder 在下一轮 assemble 时一并输出)

**D.3 http** — 发送 HTTP 请求

```yaml
action:
  type: http
  method: POST
  url: "https://hooks.slack.com/services/xxx"
  headers: { "Content-Type": "application/json" }
  body: '{"text": "CodePilot: Agent 修改了 $TOOL_INPUT_FILE_PATH"}'
  timeout: "5s"           # 默认 30s
```

执行语义:

- 走 `net/http.Client.Do`,timeout 用 `http.Client.Timeout`
- 2xx 视为成功,4xx / 5xx / 网络错误记 warn 日志,不传播
- response body 不返回给 LLM(避免污染上下文),只记日志

**D.4 agent** — 调用 LLM 子任务(本期 stub)

```yaml
action:
  type: agent
  prompt: "请检查刚才写入的文件 $TOOL_INPUT_FILE_PATH 是否有安全漏洞。"
  max_iterations: 1      # 默认 1,本步骤固定 1(Step 12 升级为完整 SubAgent)
  allow_tools: ["ReadFile", "Grep"]   # 可选,缺省为空(纯 LLM 评论)
  timeout: "60s"         # 默认 60s
```

执行语义(**本期实现**):

- 复用主 Agent 的 LLM provider,把 `prompt` 作为独立 user 消息调用一次
- 可选传入 `allow_tools` 列出的工具描述;无工具时 LLM 只能给纯文本评论
- 最多 1 轮迭代(`max_iterations` 本步骤固定 1,忽略配置)
- LLM 响应**不写回主会话 history**(避免污染),结果仅记日志

> **Step 12 升级点(本期在源码中标记 TODO)**:Step 12 SubAgent 上线后,`agent` action 升级为「启动独立 SubAgent(独立 conversation、独立 history、独立 LLM stream)、结果回传主会话」,本期的轻量版作为降级实现保留。

### E. HookContext 上下文变量

每个事件触发时构造 `HookContext`(只读,Go struct),所有 action 执行前都先把 `HookContext` 的字段填入变量映射,供 `$VAR_NAME` 插值使用。

| 字段 | 类型 | 含义 | 可用事件 |
| --- | --- | --- | --- |
| `Event` | string | 事件名(如 `pre_tool_use`) | 全部 |
| `ToolName` | string | 工具名(大驼峰,如 `WriteFile`) | 工具级事件 |
| `ToolInput` | map[string]any | 工具参数原始 map(JSON 反序列化结果) | 工具级事件 |
| `ToolInputFilePath` | string | 工具参数中的路径字段(ReadFile/WriteFile/EditFile/Glob/Grep 取 `file_path`/`path`) | 工具级事件 |
| `ToolResult` | string | 工具执行结果文本(成功时);失败时为错误描述 | `post_tool_use` |
| `ToolIsError` | bool | 工具是否执行失败 | `post_tool_use` |
| `ToolDurationMs` | int64 | 工具执行耗时(ms) | `post_tool_use` |
| `MessageContent` | string | 消息文本(用户消息或 LLM 回复) | 消息级事件 |
| `MessageRole` | string | `user` / `assistant` | 消息级事件 |
| `Error` | string | 错误文本(无错为空) | `error` / `post_tool_use`(失败时) |
| `SessionID` | string | 当前会话 ID | 全部 |
| `Iteration` | int | 当前轮次号(1-based) | 轮次级 + 工具级 + 消息级 |
| `Workdir` | string | 工作目录绝对路径 | 全部 |

变量插值语法:`$VAR_NAME`(大写字母+下划线+数字),如 `$TOOL_INPUT_FILE_PATH` / `$SESSION_ID` / `$ITERATION`;同时支持嵌套字段如 `$TOOL_INPUT.command`(自动走 map 路径)。变量未设置 / 不适用当前事件时,插值为空字符串。

### F. Agent Loop 与生命周期集成

Hook 引擎在以下节点插入,所有 hook 失败仅记 warn 日志,**绝不**中断主 Agent Loop:

| 节点 | 触发事件 | 集成位置 |
| --- | --- | --- |
| 二进制启动完成 | `program_start` | `main.go run()` 中、HTTP server 启动前 |
| 二进制即将退出 | `program_exit` | `main.go` 的 cleanup / defer 链 |
| `/new` 或 `/resume` 成功 | `session_start` | session manager 创建/恢复后 |
| `/clear` 或切会话 | `session_end` | session manager 删除前 |
| AgentLoop 每轮迭代开始 | `iteration_start` | `agent_loop.go` 循环顶部 |
| AgentLoop 每轮迭代结束 | `iteration_end` | `agent_loop.go` 循环尾部 + 异常分支 |
| 工具执行前 | `pre_tool_use` | `tool_handler.go` `doExecute` 权限检查前 |
| 工具执行后 | `post_tool_use` | `tool_handler.go` `doExecute` execute 后,封装 ToolResult 前 |
| 用户消息发往 LLM 前 | `pre_message` | 消息送入 LLM 前(hook 可改 message) |
| LLM 回复写入 history 后 | `post_message` | LLM 响应解析后 |
| 上下文压缩完成 | `compact` | Step 7 压缩协调器完成时 |
| AgentLoop 不可恢复错误 | `error` | `agent_loop.go` 错误分支前 |

### G. 错误隔离与日志

- 任何 hook 抛出 panic → 内部 recover + 记 error 日志,不传播
- 任何 hook 返回 error → 记 warn 日志,不传播
- 每个 hook 执行记录:开始时间、结束时间、耗时、匹配条件数、action 类型、success/fail 原因
- `async: true` 的 hook 启动时记 info 日志,完成时记 debug 日志(避免日志噪声)
- WebUI 状态栏可观测性:新增 `hooks` 子项,显示「已配置 N 条 / 已触发 M 次 / 失败 K 次」(只读,不开 UI 配置页)

### H. 主 Agent Loop 影响

- 同步 hook 默认超时 30s(可配置),超时后 kill + 记 error 日志,不阻塞主流程
- 异步 hook 在 goroutine 中跑,不阻塞 Agent Loop;但同一 action 内串行执行多个子步骤(如有)
- `once: true` 的 hook 在 session 级共享状态,本会话内只触发一次

## 非功能要求

1. **架构分层**:Hook 系统归 **第 3 层 工具层**,与 Skill / MCP / SlashCommand / SubAgent 同层;不破坏现有 5 层架构,单向依赖:`engine/conversation` / `engine/prompt` / `tool` / `slash` → `hook`;`hook` → 不 import 任何上层
2. **包边界**:
   - `src/internal/hook/` 主包:定义 `Engine` / `Event` / `HookContext` / `Action` 接口
   - `src/internal/hook/loader/` 配置加载(全局 + 项目级合并 + 校验)
   - `src/internal/hook/matcher/` 条件匹配(glob / eq / all / any)
   - `src/internal/hook/executor/` 四个 action 子执行器(command/http/prompt/agent)
   - `src/internal/hook/integration/` Engine 与 Agent Loop / ToolHandler / Session / Compact / Prompt 的集成胶水代码
3. **兼容性**:
   - 现有 6 条 slash 命令行为完全不变
   - 现有 6 个内置工具行为完全不变(权限拦截 + 中间件链顺序保留,hook 仅在工具 handler 入口/出口插入)
   - Step 1~10 已有功能(权限 / 上下文 / 记忆 / MCP / Skill / 缓存)零回归
   - `hooks.enabled = false` 时,Hook 引擎完全跳过(不加载配置、不注册事件、不影响主流程)
   - 不存在 `hooks` 配置段时,等同于 `enabled: true` + `entries: []`(零配置安全降级)
4. **配置零成本降级**:零 hook 配置启动时,HookEngine 存在但空跑,启动耗时增加 < 5ms,运行期主 Agent Loop 性能损失 < 1%
5. **可观测性**:Hook 加载日志(每条 hook 加载成功/失败/跳过原因);运行期 hook 触发日志(事件名、hook 名、耗时、action 类型、success/fail);WebUI 状态栏 hook 子项(已配置数 / 已触发数 / 失败数)
6. **安全性**:
   - command action 走现有权限系统:命中黑名单(如 `rm -rf /`)直接拒绝 + 记 error 日志
   - http action 限制外网白名单(可选配置,默认全开 + 警告)
   - agent action 限制 max_iterations(本期固定 1)
   - 所有 action 沙箱:command 子进程 cwd 默认锁到 workdir(同 tool.sandbox);agent 复用主 LLM 不引入新通道
7. **扩展性**:`Event` 枚举 + `Action` 接口都预留可扩展;未来增加事件类型不需改 hook 配置 schema,只需在 Engine 注册新事件源

## 设计骨架

```text
┌─────────────────────────────────────────────────────────────┐
│                    启动期(配置加载)                            │
│                                                              │
│   ~/.codepilot/setting.json (全局)                            │
│      hooks: { enabled: true, entries: [...] }               │
│                             ┌───── 字段级合并 (同 Step 5/8/10)│
│   <cwd>/.codepilot/setting.json (项目级覆盖)                  │
│                             │                               │
│                             ▼                               │
│                   ┌──────────────────┐                       │
│                   │ hook.Loader      │  解析 + 校验 + 合并   │
│                   │  LoadAndMerge()  │  返回 []HookEntry    │
│                   └────────┬─────────┘                       │
│                            │                                │
│                            ▼                                │
│                   ┌──────────────────┐                       │
│                   │ hook.Engine      │  构造 + 注入各集成点   │
│                   │  - Entries map    │  (Agent Loop /       │
│                   │  - OnceTracker    │   ToolHandler /      │
│                   │  - ActionExecs    │   Session / Compact /│
│                   └────────┬─────────┘  Prompt Builder)     │
│                            │                                │
│       ┌────────────────────┼────────────────────┐           │
│       ▼                    ▼                    ▼           │
│ matcher.Matcher    executor.CommandExec  executor.PromptExec│
│ (glob/eq/all/any)  executor.HttpExec     executor.AgentExec │
│       │                    │                    │           │
│       ▼                    ▼                    ▼           │
│ HookEngine.Dispatch(event, ctx)                             │
│   → matcher.Evaluate(condition, ctx)                        │
│   → action.Execute(ctx, vars)                               │
│   → 错误隔离(只 log,不 panic / 不影响主循环)                  │
└─────────────────────────────────────────────────────────────┘
```

关键模块:

| 模块 | 职责 | 关键导出 |
| --- | --- | --- |
| `hook/event.go` | 事件枚举(12 类) + 扩展点 | `Event`, `AllEvents`, `IsValidEvent()` |
| `hook/context.go` | `HookContext` + 变量映射 + 插值 | `HookContext`, `NewContextForEvent()`, `Interpolate()` |
| `hook/loader/loader.go` | 配置加载 + 全局/项目级合并 + 校验 | `Loader`, `LoadAndMerge()`, `HookEntry`, `HookAction` |
| `hook/matcher/matcher.go` | 条件匹配(glob/eq/all/any) | `Condition`, `Matcher`, `Evaluate()` |
| `hook/executor/command.go` | shell 命令执行器 | `CommandExecutor`, `Execute()` |
| `hook/executor/http.go` | HTTP 请求执行器 | `HttpExecutor`, `Execute()` |
| `hook/executor/prompt.go` | 提示词注入器(返回注入文本) | `PromptExecutor`, `Execute()` |
| `hook/executor/agent.go` | LLM 子任务执行器(本期 stub) | `AgentExecutor`, `Execute()` |
| `hook/engine.go` | 引擎核心:事件注册、once 追踪、调度 | `Engine`, `New()`, `Dispatch()`, `EngineConfig` |
| `hook/integration/loop.go` | 与 Agent Loop 集成 | `WireAgentLoop(engine, mgr)` |
| `hook/integration/tool.go` | 与 ToolHandler 集成 | `WireToolHandler(engine, h)` |
| `hook/integration/session.go` | 与 Session 生命周期集成 | `WireSession(engine, sm)` |
| `hook/integration/compact.go` | 与上下文压缩集成 | `WireCompact(engine, coord)` |
| `hook/integration/prompt.go` | 与 PromptBuilder 集成(prompt action 注入) | `RegisterPromptSink(engine, b)` |
| `hook/sources/hooks_awareness.go` | SP 自感知 Source(告诉 LLM 有 hook) | `HooksAwarenessSource`, `NewHooksAwarenessSource()` |

## Out of Scope(本步骤不做)

1. **WebUI Hook 配置 UI** — 本步骤 Hook 配置只通过 setting.json 编辑,不开 WebUI 配置面板(避免双端配置同步的复杂度);运行期触发通过 SP 状态栏可观测性查看
2. **WebUI Hook 触发可视化** — hook 执行详情仅写日志,不在 WebUI 工具块中展示(避免与 Skill/工具块 UI 混淆)
3. **Hook 自身热加载** — 本步骤启动期一次性加载,运行期不监听 setting.json 变化(后续可由 watcher 扩展)
4. **Hook 嵌套 / 链式触发** — hook A 触发后不允许再触发其它 hook(包括自身),避免循环;同一事件的多个 hook 串行执行但互不影响
5. **Hook 调用 SubAgent** — `agent` action 本期是「主 LLM 一次性调用」(见 §D.4),Step 12 完整 SubAgent 上线后再升级
6. **跨进程 / 跨会话 Hook 触发** — `once: true` 仅在单会话内生效;程序重启后 once 状态重置
7. **Hook 远程分发** — Hook 配置只从本地 setting.json 加载,不从 URL/Git 仓库拉取
8. **Hook 调用统计 / 计费** — 不记录 hook 触发的 token 消耗(避免与 Skill 统计冲突)
9. **Hook 优先级 / 并发控制** — 多 hook 匹配同一事件时按配置数组顺序串行执行(同步)或并行启动(异步);不提供 priority 字段
10. **Hook 调用图谱 / 审计日志** — 不持久化 hook 触发历史(只走 zap 日志流)
11. **Hook 自身的权限控制** — hook action 默认信任用户配置,不二次询问(因为 hook 是用户自己写的);command 黑名单仍然生效(同 Step 5 Bash 黑名单)
12. **Hook 与 Skill 的双向联动** — Skill 不能声明自己需要的 hook(预留接口,本期不实现)