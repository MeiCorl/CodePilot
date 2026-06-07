# Step 2 — 工具系统集成

## 背景

Step 1 已经实现 WebUI 中与 LLM 的多轮对话流式交互，但 LLM 没有任何「感知/操作」真实世界（文件系统、Shell）的能力，模型只能基于训练知识做通用回答，无法真正完成 Coding Agent 的核心使命——**读懂代码、修改代码、执行命令、验证结果**。

Step 2 的目标是为 CodePilot 引入**工具系统**：让 LLM 在对话过程中能够主动调用预置工具，工具执行结果回传给 LLM 形成闭环。本期只实现最简的"单轮调用"形态（一次 `tool_use` → 执行 → `tool_result` → 把控制权交回用户），Agent Loop（ReAct 推理循环、连环调用）留到 Step 3。

完成本步骤后，用户在 WebUI 中下达 Coding 相关指令（如"帮我读一下 main.go"），LLM 应能自主调用 `ReadFile` 工具读取文件内容并基于真实代码回答。

## 目标用户

使用 CodePilot 进行 AI 辅助编程的开发者，希望 LLM 能直接读写文件、执行命令、操作真实项目，而不仅仅做通用对话。

## 能力清单

1. **统一工具抽象**：定义标准 Tool 接口（名称、描述、输入 Schema、权限分级、执行函数），所有内置工具遵循同一契约
2. **集中注册机制**：通过 Registry 全局注册工具，main.go 显式 import 触发注册；后续新增工具只需实现 Tool 接口并在 init() 中注册一行，**无需修改核心系统代码**
3. **5 个内置基础工具**：
  - `ReadFile`：读取文件内容，记录行号，支持 offset/limit 模式分页读取，非文本文件/不存在/无权限等异常给出明确错误
  - `WriteFile`：创建或覆盖写入文件，目录不存在时自动创建
  - `Bash`：执行 Shell 命令，捕获 stdout/stderr/exit code，带超时控制
  - `Glob`：按 glob 模式查找匹配的文件路径
  - `Grep`：在文件内容中按正则模式搜索，返回匹配行及行号
4. **LLM 工具描述同步**：将当前启用的工具列表（名称、描述、输入参数 Schema）以各家协议原生格式随请求发送给 LLM，让 LLM 知道有哪些工具可用
5. **tool_use 响应处理**：识别 LLM 返回的 `tool_use`/`tool_calls` 内容块（ContentBlock 新增 `ToolUseBlock`/`ToolResultBlock` 类型），从响应中提取工具名与参数
6. **工具执行引擎**：根据 `tool_use` 在 Registry 中查找对应工具，解析参数为 Go 类型，调用执行函数，捕获结果或错误，封装为 `ToolResultBlock`
7. **单轮闭环**：工具执行后将 `tool_result` 随同历史消息一起再次发给 LLM，LLM 基于真实结果给出最终回复，整个过程对用户表现为一次完整对话回合；执行完一次工具后**停下**，把控制权交回用户
8. **Anthropic 协议适配**：Provider 实现 tools 数组的消息格式转换，tool_use 使用 SDK 原生 `ToolUseBlock`，tool_result 使用 `ToolResultBlock`
9. **OpenAI 协议适配**：Provider 实现 function_calling 格式转换，tool_calls 使用 OpenAI 原生 `ToolCall` 字段，tool 消息使用 role=tool 消息
10. **WebUI 工具调用展示**：浏览器中间会话栏中按时间顺序呈现「🔧 正在调用工具: X, 参数: {...}」→「✓ 工具执行完成 (耗时 Xs)」→ 工具结果回传给 LLM 后的最终回复；每条工具消息为独立消息条目，与用户/助手消息视觉上明显区分（左侧图标栏 + 折叠/展开区域）；工具原始 result **不**在前端展示，避免视觉冗余与 LLM 重复回显
11. **流式中断兼容**：用户在 WebUI 中点击"停止"按钮（已有 `abort_stream` 消息入口），服务端的 `streamState` 会取消对应 ctx：若正在流式响应中则中断 LLM；若正在执行工具则取消工具 goroutine；若已经回传 `tool_result` 等待 LLM 则中断二次 LLM 调用；前端根据当前 `status_update` 判断按钮是否可点击
12. **工具执行超时**：所有工具执行受 `context.WithTimeout` 控制，默认 30 秒，可在 `setting.json` 的 `tool_execution_timeout_seconds` 字段覆盖；超时后工具返回明确的超时错误给 LLM
13. **安全兜底 - 危险命令拦截**：Bash 工具内置危险命令黑名单（`rm -rf /`、`mkfs`、`shutdown`、`format`、`dd if=` 等），命中黑名单直接拒绝执行并返回错误，**不**走到 shell
14. **安全兜底 - 路径沙箱**：ReadFile/WriteFile/Glob/Grep 等所有涉及路径的工具，必须将用户传入路径 resolve 为绝对路径后判断是否落在配置的 `working_directory`（默认当前进程 cwd）之内；越界路径直接拒绝并返回明确错误
15. **工具调用审计日志**：每次工具调用记录到日志文件（工具名、参数摘要、执行耗时、是否成功、错误信息），便于问题排查；日志不做脱敏（工具结果可能含代码，正常记录）
16. **会话持久化兼容**：`tool_use` 和 `tool_result` 必须能被序列化到 session JSON 文件，下次恢复会话时 LLM 能"记得"自己调用过哪些工具以及结果；`session_loaded` 消息需扩展 `ChatMessage` 携带工具调用条目（类型/工具名/参数摘要/结果摘要/是否错误/耗时），前端能完整渲染历史工具调用链
17. **配置可扩展**：配置文件新增 `tools` 段（启用的工具列表，默认全开）、`tool_execution_timeout_seconds`（默认 30）、`tool_working_directory`（默认 cwd）

## 非功能要求

1. **性能**：单次工具执行延迟对用户透明（不优化，但需要可观测）；工具查找通过 Registry O(1) map 查找
2. **可用性**：工具执行失败（参数错误、文件不存在、命令失败）必须返回结构化错误给 LLM，由 LLM 决定下一步；不向用户抛出 panic
3. **安全性**：
  - 路径沙箱必须 resolve 真实路径后再判断（防止 `../` 绕过）
  - Bash 工具禁止把命令字符串直接 `sh -c` 执行后再过滤，必须在执行前用正则/字符串匹配做拦截
  - 危险命令黑名单和路径沙箱**不可被工具配置关闭**
4. **兼容性**：ReadFile/WriteFile 在 Windows/macOS/Linux 均可用；Bash 工具在 Windows 上需明确告知该平台无 bash（提示降级到 PowerShell 或返回不支持错误）
5. **可扩展性**：
  - Tool interface 设计为后续 MCP（Step 6）工具、Skill 工具（Step 10）、SubAgent 工具（Step 12）的统一抽象
  - Registry 抽象允许后续从配置文件/插件加载工具
  - 工具的输入参数可通过 struct tag 或第三方 JSON Schema 库定义，step2 用 struct tag + 反射做基础校验
6. **可观测性**：每个工具执行有开始/结束日志，耗时、参数摘要、结果摘要、错误信息全记录
7. **可测试性**：Tool 接口设计应便于 mock；工具自身不依赖 WebUI/会话/全局状态，可独立单元测试

## 设计骨架

```
codepilot/
├── src/
│   ├── main.go                          # 程序入口，新增 import 触发工具注册
│   ├── internal/
│   │   ├── interaction/
│   │   │   └── web/                     # WebUI 扩展：工具执行展示
│   │   │       ├── protocol.go          # 扩展：tool_call_start / tool_call_end 消息类型
│   │   │       ├── handler.go           # 扩展：工具事件 push 逻辑
│   │   │       ├── static/              # 扩展：前端工具消息样式与渲染
│   │   │       │   ├── style.css        # 工具消息块样式
│   │   │       │   └── app.js           # 工具消息渲染逻辑
│   │   │       └── tool_msg.go          # 新建：工具消息渲染辅助函数
│   │   ├── engine/
│   │   │   └── conversation/
│   │   │       ├── manager.go           # 扩展：单轮 tool_use → 执行 → tool_result
│   │   │       └── tool_handler.go      # 工具分发执行器
│   │   ├── tool/                        # 【新增】第 3 层：工具层
│   │   │   ├── tool.go                  # Tool 接口、ToolInfo、Permission 枚举
│   │   │   ├── registry.go              # Registry 全局注册中心
│   │   │   ├── builtin/
│   │   │   │   ├── read_file.go         # ReadFile 工具
│   │   │   │   ├── write_file.go        # WriteFile 工具
│   │   │   │   ├── bash.go              # Bash 工具（含危险命令黑名单）
│   │   │   │   ├── glob.go              # Glob 工具
│   │   │   │   ├── grep.go              # Grep 工具
│   │   │   │   └── register.go          # init() 批量注册入口
│   │   │   └── safety/
│   │   │       ├── path.go              # 路径沙箱：resolve + 范围校验
│   │   │       └── bash_blacklist.go    # Bash 危险命令黑名单
│   │   ├── config/
│   │   │   └── config.go                # 扩展：Tools/ToolExecutionTimeout/ToolWorkingDirectory 字段
│   │   └── logger/
│   │       └── logger.go                # 工具审计日志入口
│   └── llm/
│       ├── types.go                     # 扩展：ContentBlock 新增 ToolUseBlock、ToolResultBlock
│       ├── types_json.go                # 扩展：上述类型的 JSON 序列化
│       ├── provider.go                  # 扩展：StreamChat 增加 tools []Tool 参数
│       ├── anthropic.go                 # 扩展：tools 数组与 tool_use 转换
│       └── openai.go                    # 扩展：tools 数组与 tool_calls 转换
├── config/
│   └── setting.example.json              # 新增 tools 段示例
└── docs/
    └── step2-工具系统集成/
        ├── spec.md
        ├── tasks.md
        └── checklist.md
```

### 核心数据结构

```
Tool interface {
    Name() string                        // 工具名（snake_case，全局唯一）
    Description() string                 // 工具描述，发给 LLM
    InputSchema() json.RawMessage        // 工具输入 JSON Schema
    Permission() ToolPermission          // 只读 / 写文件 / 执行命令
    Execute(ctx, input json.RawMessage) (output, error)
}

ToolUseBlock      { ID, Name, Input }                // LLM 发出的工具调用请求
ToolResultBlock   { ToolUseID, Content, IsError }     // 系统回传的工具执行结果
```

### WebSocket 消息扩展（新增）

继承 step1.1 协议（[src/internal/interaction/web/protocol.go](../../src/internal/interaction/web/protocol.go)），本步骤新增/扩展如下消息：

**新增服务端 → 客户端**：

| 消息类型 | Payload 关键字段 | 触发时机 |
|---|---|---|
| `tool_call_start` | `tool_use_id`, `name`, `input`(json.RawMessage), `started_at` | LLM 返回 `tool_use` 后、调度工具执行前 |
| `tool_call_end` | `tool_use_id`, `name`, `output`(string, 摘要), `is_error`, `duration_ms` | 工具执行结束（成功 / 失败 / 超时 / 被取消） |

**新增 Agent 状态**（`status_update.payload.status`）：

| 状态 | 含义 | 停止按钮 |
|---|---|---|
| `idle` | 空闲，等待用户输入 | 禁用 |
| `thinking` | LLM 流式响应中 | 启用（点击中断 LLM） |
| `tool_running` | 工具执行中 | 启用（点击中断工具 goroutine） |
| `error` | 错误态 | 禁用 |

**扩展 `ChatMessage`**（`session_loaded.payload.messages[]`）：

```go
type ChatMessage struct {
    Role    string                  // user / assistant / tool
    Content string                  // 文本（assistant/user）；tool 消息为空
    ToolCall *ToolCallDisplay       // role=tool 时填充
}

type ToolCallDisplay struct {
    ID         string          // tool_use_id
    Name       string          // 工具名
    Input      string          // 参数 JSON 字符串（前端可折叠）
    Output     string          // 结果摘要（截前 500 字符）
    IsError    bool
    DurationMs int64
    Status     string          // running / completed / error / aborted
}
```

**复用现有消息**：

- `abort_stream`（客户端 → 服务端）：扩展语义，状态为 `tool_running` 时点击停止按钮也发此消息，服务端 `streamState.abort()` 取消当前 ctx
- `status_update`：状态枚举扩展 `tool_running`

### 协议适配关键点


| 维度   | Anthropic                                                            | OpenAI                                                                        |
| ---- | -------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| 工具描述 | `request.Tools: []Tool`                                              | `request.Tools: []Tool` (含 `Type: "function"`)                                |
| 工具调用 | `content: [{type: "tool_use", id, name, input}]`                     | `assistant.tool_calls: [{id, type: "function", function: {name, arguments}}]` |
| 工具结果 | `role: user, content: [{type: "tool_result", tool_use_id, content}]` | `role: tool, tool_call_id, content`                                           |
| 流式事件 | `content_block_start` 携带 tool_use                                    | 流式增量 `tool_calls[].function.arguments` 字符串                                    |


## Out of Scope（本步骤不做）

1. Agent Loop / ReAct 推理循环（多轮连环 tool_use 调用）—— Step 3
2. 完整 System Prompt 注入工具使用规范 —— Step 4
3. 完整权限系统（白名单/黑名单/危险操作确认弹窗）—— Step 5；本期仅做最小化安全兜底
4. MCP 协议 —— Step 6；但 Tool 接口设计需为 MCP 预留扩展位
5. 上下文管理中的工具结果压缩/截断 —— Step 7；本期完整保留所有工具结果
6. 工具的并行执行（同一回合内多工具并发）—— Step 3+ 视需要引入
7. 工具的撤销/回滚（如 WriteFile 的备份恢复）—— 后续如有需要
8. 工具调用前的用户确认弹窗（危险工具 HITL）—— Step 5
9. 自定义工具（用户编写并通过配置文件加载）—— 后续
10. 工具的单元测试覆盖完整（每个工具至少 1 个 happy path 测试，复杂场景不要求全量覆盖）—— 本步骤按 checklist 验证即可
11. Hook 系统（工具执行前后钩子）—— Step 11；但 Tool.Execute 入口处预留扩展位
12. 工具的元数据本地化/多语言 —— 暂不做
13. Bash 工具的交互式命令支持（`vim`/`less` 等需要 TTY 的命令）—— 明确不支持

