# Step 6 — MCP 协议实现

## 背景

CodePilot 在 Step 2 阶段落地了工具系统与 5 个内置工具（ReadFile / WriteFile / EditFile / Bash / Glob / Grep），并在 Step 3 接入 ReAct Agent Loop 让 LLM 能自主调用。但工具能力被内置集合所限制：开发者想用 GitHub 操作、数据库查询、浏览器自动化、专业 IDE 能力时必须自写工具。Model Context Protocol（MCP，Anthropic 2024 年开源）正是为解决「Agent ↔ 外部工具生态」互操作而设计的开放协议，已在 Claude Desktop、Cursor、Cline 等编辑器广泛落地，社区存在大量可复用的 MCP server。

本步骤为 CodePilot 引入 MCP 客户端能力，让 Agent 能透明地调用任何符合 MCP 规范的外部工具，无需为每个新工具单独写内置实现。设计目标是把 MCP server 工具包装为 CodePilot 已有的 `tool.Tool` 接口，注册进既有 `tool.Registry`，使 Agent Loop、权限系统、WebUI 展示层完全无感。

## 目标用户

- **Agent 能力扩展者**：希望通过 MCP 协议接入 GitHub / Postgres / Playwright / Figma 等第三方能力的开发者
- **多工具复用者**：不愿为每个新工具重写内置实现，希望复用社区已有 MCP server 的项目维护者
- **自定义工具作者**：希望用 MCP 协议自行编写 server 扩展 CodePilot 能力的进阶用户

## 能力清单

1. **JSON-RPC 2.0 双向通信**：按标准 JSON-RPC 2.0 消息格式（Request / Response / Notification / Error）与外部 server 通信，支持批量请求/响应、错误码体系
2. **两种传输方式**：
   - **stdio 传输**：本地子进程方式，stdin 写 JSONL 请求、stdout 读 JSONL 响应
   - **Streamable HTTP 传输**：远程 HTTP 方式，POST 单次请求/响应 + 兼容 text/event-stream
3. **三阶段会话生命周期**：
   - **阶段 1 握手**：发送 `initialize`（声明 protocolVersion / capabilities / clientInfo）→ 接收 server capabilities → 发送 `initialized` 通知
   - **阶段 2 工具发现**：调用 `tools/list` 拉取远端工具列表（含 name / description / inputSchema）
   - **阶段 3 工具调用**：调用 `tools/call` 携带工具名 + 参数，接收 content 数组形式的返回
4. **请求-响应异步匹配**：每个请求生成唯一 id，发送后挂起到响应通道，server 返回 `result` / `error` 时按 id 关联唤醒
5. **Tool 适配层**：把 MCP `tools/list` 返回的每个远端工具包装为 CodePilot 原生 `tool.Tool` 接口（实现 Name / Description / InputSchema / Execute），无感注册进 `tool.Registry`
6. **多 server 连接池**：所有 server 的 Session 在内存中常驻，工具调用时按 server 名称查表复用，进程生命周期内不重连（除非断开）
7. **配置驱动声明 server 列表**：在 `setting.json` 中声明 `mcp.servers` 字段，包含 `name` / `command`（stdio）/ `url`（HTTP）/ `env` / `timeout` / `args` 等
8. **工具命名空间**：远端工具在 LLM 与 Registry 中以 `mcp__<server_name>__<tool_name>` 形式命名，避免与内置工具或跨 server 工具重名
9. **Eager 初始化 + 单 server 失败容错**：CodePilot 启动时并发尝试连接所有配置的 server，单 server 握手失败不影响其他 server，也不阻塞 CodePilot 启动
10. **指数退避重连**：stdio 子进程崩溃或 HTTP 连接断开后，下次调用该 server 工具时按 1s / 3s / 9s 退避重试 3 次，超过后该 server 标记为 `unhealthy`，需重启 CodePilot 恢复
11. **Step 5 权限系统无缝集成**：MCP 工具调用走与内置工具同一套 `permission.Decide` 链路，支持按 `mcp__<server>__<tool>` 粒度匹配 allow / deny / ask 规则，工具名前缀便于按 server 维度粗粒度管控
12. **WebUI 工具块来源展示**：工具执行展示时增加「server 来源」徽标（如 `mcp: github`），让用户清楚知道是本地工具还是远端 MCP server 提供的
13. **超时与审计日志**：每次 MCP 请求可配置超时（默认 30s）；握手 / 调用 / 断开 / 重连事件写入审计日志
14. **Tools 列表本地缓存**：单 server `tools/list` 结果在 60s TTL 内缓存，避免每次会话刷新都重新拉取

## 非功能要求

- **协议兼容性**：实现严格符合 MCP 2025-03-26 规范；至少支持 `tools/list` / `tools/call` 两个标准方法
- **性能**：单次 MCP 调用额外开销不超过一次 HTTP RTT；握手 / list 工具结果内存缓存；连接池复用避免重复 spawn
- **安全**：远端工具不允许绕过 Step 5 权限系统；stdio 子进程继承权限系统的危险命令黑名单；HTTP 传输可选 Basic / Bearer Token 鉴权
- **可观测**：所有 MCP 事件（连接 / 握手 / 调用 / 断开 / 重连）写日志；`session.json` 中可序列化 MCP 工具调用历史以便回放
- **可扩展**：Transport / Session / Adapter 三层接口化，未来加 SSE / WebSocket / Resources / Prompts 能力不需重写

## 设计骨架

### 目录结构

```
internal/mcp/
├── client.go              # 顶层 Client 入口，封装 Pool + Adapter
├── jsonrpc/               # JSON-RPC 2.0 编解码
│   ├── message.go         # Request / Response / Notification / Error 数据结构
│   └── codec.go           # 序列化 / 反序列化 / 错误码常量
├── transport/             # 传输层
│   ├── transport.go       # Transport 接口（Send / Recv / Close）
│   ├── stdio.go           # stdio 传输实现：os/exec 启子进程 + JSONL
│   └── http.go            # Streamable HTTP 传输实现
├── session/               # 会话管理
│   ├── session.go         # 单 server Session（handshake + request/response 协程）
│   └── pool.go            # 多 server 连接池（map + RWMutex）
├── adapter/               # Tool 适配层
│   ├── adapter.go         # 单个 MCP Tool → codePilot tool.Tool 实现
│   └── registry.go        # 批量注册远端工具到 tool.Registry
├── config/                # 配置加载
│   └── config.go          # 从 setting.json 解析 mcp.servers 段
├── reconnect/             # 重连策略
│   └── backoff.go         # 指数退避计算（1s / 3s / 9s）
└── errors.go              # MCP 错误类型（连接失败 / 握手失败 / 调用失败 / 超时）

internal/tool/             # 既有模块，新增 method 或接口扩展
└── registry.go            # Register() 已支持，已够用

config.example.json        # 配置文件示例（新增 mcp.servers 段）
```

### 关键流程

**启动流程（main.go 启动时）**：
```
Load setting.json → 解析 mcp.servers → 并发 NewSession 每个 server
   → 每个 Session 内部 Transport.Connect() + initialize 握手 + tools/list
   → 适配器把 tools 注册到 tool.Registry
   → 任一 server 失败仅记日志，不阻塞
```

**单次工具调用流程**：
```
Agent Loop 调 mcp__github__create_issue(name="...", args)
   → tool.Registry 查 adapter → adapter 调 Session.Call("tools/call", {...})
   → Session 生成 id 挂起 → Transport.Send()  → goroutine Recv() 按 id 唤醒
   → 返回 result.content[] → adapter 转 tool.Result → 回 Agent Loop
   → 整个链路中途经 permission.Decide()（Step 5）
```

**重连流程**：
```
Session 收到 Transport 断开事件 → 标记 unhealthy + 记录当前时间
   → 下次 tools/call 时检测 unhealthy → 进入 backoff(1s) → 重建 Transport
   → 重新握手 → 成功则恢复；失败 backoff(3s) → backoff(9s) → 最终标记永久 unhealthy
```

## Out of Scope

- **MCP Resources**（`resources/list` / `resources/read`）：资源读取能力，本步骤不实现
- **MCP Prompts**（`prompts/list` / `prompts/get`）：prompt 模板能力，本步骤不实现（Step 10 Skill 系统可能复用）
- **MCP Sampling**（server 主动请求 LLM 推理）：反向调用能力，本步骤不实现
- **MCP Roots**：客户端文件系统边界声明，本步骤不实现
- **SSE Legacy 传输**：旧版 `transport: sse` 不实现（仅 Streamable HTTP）
- **OAuth 2.0 鉴权握手**：本步骤仅支持静态 token / Basic Auth / 无 auth，复杂 OAuth 流程留待后续
- **远端 server 进程托管**：stdio 子进程的生命周期由 CodePilot 进程托管，但 server 自身的依赖安装 / 升级不在本步骤
- **MCP server 编写教程**：本步骤提供客户端能力，不提供 server 端实现示例（端到端测试会用 Go 写一个简单 mock）
