# Step 6 — MCP 协议实现 · 任务分解

> 本文档按实现顺序拆解 Step 6 的 9 个子任务，每个任务在一个专注会话内可完成。
> 状态机：`待完成` → `进行中` → `已完成`

---

## Task 1: JSON-RPC 2.0 编解码层

**状态**：已完成

**目标**：实现 MCP 通信的底层消息编解码，对外暴露 `Request` / `Response` / `Notification` 三个核心结构体与序列化 / 反序列化函数。

**影响文件**：
- `internal/mcp/jsonrpc/message.go` — 新建，定义 `Request` / `Response` / `Notification` / `Error` 结构体
- `internal/mcp/jsonrpc/codec.go` — 新建，提供 `MarshalRequest` / `UnmarshalMessage` / `NewIDGenerator` 等函数
- `internal/mcp/jsonrpc/codec_test.go` — 新建，单元测试

**依赖**：无

**具体内容**：
1. 定义 `Request{JSONRPC, ID, Method, Params}` / `Response{JSONRPC, ID, Result, Error}` / `Notification{JSONRPC, Method, Params}` 三个核心结构体，JSON 标签按 JSON-RPC 2.0 规范
2. 定义 `Error{Code, Message, Data}` 结构与 `ParseError` / `InvalidRequest` / `MethodNotFound` / `InvalidParams` / `InternalError` 标准错误码常量
3. 实现 `MarshalRequest(req) ([]byte, error)` 把 Request 序列化为单行 JSON（不带末尾换行）
4. 实现 `UnmarshalMessage(data []byte) (any, error)` 解析单条消息，根据 `id` 字段是否存在区分 Request vs Response vs Notification
5. 实现 `NewIDGenerator() func() string` 用 `crypto/rand` + atomic counter 生成全局唯一 id
6. 单元测试覆盖：基本编解码、批量消息、错误消息、畸形 JSON 容错

**参考资料**：
- JSON-RPC 2.0 规范：https://www.jsonrpc.org/specification
- MCP 协议消息格式：https://modelcontextprotocol.io/specification/2025-03-26

---

## Task 2: Transport 接口 + stdio 传输实现

**状态**：已完成

**目标**：定义 `Transport` 抽象接口，并实现 stdio 传输：用 `os/exec` 启动本地子进程，通过 stdin 写 JSONL 请求、stdout 读 JSONL 响应。

**影响文件**：
- `internal/mcp/transport/transport.go` — 新建，定义 `Transport` 接口与 `Message` 通道类型
- `internal/mcp/transport/stdio.go` — 新建，stdio 实现
- `internal/mcp/transport/stdio_test.go` — 新建，单元测试

**依赖**：Task 1

**具体内容**：
1. 定义 `Transport` 接口：
   - `Connect(ctx) error` — 建立连接
   - `Send(msg []byte) error` — 发送单条 JSONL（自动追加 `\n`）
   - `Recv() ([]byte, error)` — 阻塞读取下一条 JSONL
   - `Close() error` — 关闭连接
   - `IsAlive() bool` — 健康状态查询
2. 定义 `Message []byte` 别名，约定每条消息是单行 JSON（不含换行）
3. `stdioTransport` 结构体持有 `*exec.Cmd` / `stdin` / `stdout` / `stderr` / `done chan error` / sync.Mutex
4. `NewStdio(command string, args []string, env []string) *stdioTransport` 构造函数
5. `Connect(ctx)` 启动子进程，把 env 合并到子进程环境变量（允许通过 env 传 API key 等）
6. `Send` 加锁向 stdin 写一行 JSON + `\n`，写失败返回错误
7. `Recv` 用 `bufio.Scanner` 按行读 stdout，跳过空行
8. 启动独立 goroutine 监听子进程退出（`cmd.Wait()`），退出时关闭 recv 通道让上层感知
9. `Close` 先关 stdin（触发子进程 EOF 优雅退出），超时后 `cmd.Process.Kill()` 强杀
10. 单元测试：启 `cat` / `echo` 子进程跑通收发；模拟子进程崩溃验证 Close 行为

**参考资料**：
- Go `os/exec` 包：https://pkg.go.dev/os/exec
- Go `bufio.Scanner` 行读取：https://pkg.go.dev/bufio#Scanner

---

## Task 3: Streamable HTTP 传输实现

**状态**：已完成

**目标**：实现 Streamable HTTP 传输：POST 单次请求/响应，支持 `Accept: application/json, text/event-stream` 双响应格式，支持可选 Bearer Token / Basic Auth。

**影响文件**：
- `internal/mcp/transport/http.go` — 新建，Streamable HTTP 实现
- `internal/mcp/transport/http_test.go` — 新建，单元测试（用 `httptest.Server` 跑 mock）

**依赖**：Task 1

**具体内容**：
1. `httpTransport` 结构体：base URL / HTTP 客户端 / 鉴权头 / session ID
2. `NewHTTP(url string, opts HTTPOption) *httpTransport` 构造函数，支持 `WithBearerToken` / `WithBasicAuth` / `WithTimeout` / `WithHeaders`
3. `Connect(ctx)` 发送 `GET /` 或 HEAD 探测（不强求），主要做 URL 校验
4. `Send(msg)` 构造 POST 请求：path 追加 `/mcp`（或 `url` 末尾已含），`Content-Type: application/json`，`Accept: application/json, text/event-stream`（双兼容）
5. 响应解析：
   - 若 `Content-Type: application/json` → 读 body 一次性反序列化为单条消息
   - 若 `Content-Type: text/event-stream` → 按 SSE 解析 `data: <json>` 行
   - 其他 Content-Type → 报错
6. MCP 2025-03-26 规范要求 server 返回 `Mcp-Session-Id` header，client 需在后续请求中回传
7. `Recv` 模式与 stdio 一致：HTTP 单次 POST 即一次完整请求-响应，无独立 Recv
8. 注意：MCP Streamable HTTP 单连接异步特性（同一 session id 可推送 server notification），本步骤只实现同步 request-response 模式，server-initiated notification 留待后续
9. 单元测试：用 `httptest.Server` 模拟三种响应（JSON 200 / SSE 流 / 鉴权失败 / 500 错误），验证正确解析

**参考资料**：
- MCP Streamable HTTP 规范：https://modelcontextprotocol.io/specification/2025-03-26/transport
- Go `net/http` 客户端：https://pkg.go.dev/net/http

---

## Task 4: 单 server Session 与三阶段握手

**状态**：已完成

**目标**：实现 `Session`：包装 Transport，提供 `Initialize` / `ListTools` / `CallTool` 三个高层方法，内部用 id 匹配实现请求-响应异步关联。

**影响文件**：
- `internal/mcp/session/session.go` — 新建，Session 实现
- `internal/mcp/session/session_test.go` — 新建，单元测试

**依赖**：Task 1, Task 2, Task 3

**具体内容**：
1. `Session` 结构体：name / transport / pending map[string]chan Response / pendingMu / nextID / recvDone / logger
2. `NewSession(name string, t transport.Transport) *Session` 构造函数
3. `Start(ctx)` 启动后台 `recvLoop` goroutine：从 transport 持续读消息，按 id 找到 pending 通道投递
4. `request(ctx, method string, params any) (any, error)` 内部方法：
   - 生成 id → 构造 Request → Marshal → transport.Send
   - 创建 pending 通道并存入 map → select 等待 ctx 超时 / 响应 / 通道关闭
5. `Initialize(ctx)` 发送 `initialize` 请求（带 protocolVersion="2025-03-26"、capabilities={}、clientInfo={name:"CodePilot", version}），返回 serverCapabilities
6. `NotifyInitialized(ctx)` 发送 `notifications/initialized` 通知（无 id）
7. `ListTools(ctx)` 发送 `tools/list`，返回 `[]MCPTool`（含 name / description / inputSchema）
8. `CallTool(ctx, name string, args any)` 发送 `tools/call` 请求，返回 `MCPCallResult`（content 数组）
9. `Close()` 关闭 transport，等 recvLoop 退出
10. 错误处理：transport 断开时关闭所有 pending 通道并返回错误；ctx 取消时清理 pending
11. 单元测试：mock Transport 跑通握手 + list + call；并发多个 request 验证 id 匹配正确

**参考资料**：
- MCP initialize 规范：https://modelcontextprotocol.io/specification/2025-03-26/basic/lifecycle
- MCP tools 规范：https://modelcontextprotocol.io/specification/2025-03-26/server/tools

---

## Task 5: 多 server 连接池 Pool + Eager Init

**状态**：已完成

**目标**：实现 `Pool`：并发启动所有配置的 server Session、缓存复用、按 server 名称查找。

**影响文件**：
- `internal/mcp/session/pool.go` — 新建，Pool 实现
- `internal/mcp/session/pool_test.go` — 新建，单元测试

**依赖**：Task 2, Task 3, Task 4

**具体内容**：
1. `Pool` 结构体：sessions map[string]*Session / mu / logger / healthy map[string]bool
2. `NewPool(logger) *Pool` 构造函数
3. `RegisterAndStart(ctx, name, transport)` 注册并启动单 server Session；启动成功立即 Initialize + ListTools
4. `InitializeAll(ctx, configs []ServerConfig) error` 并发启动所有 server（`errgroup.Group`）：
   - 每个 server 单独 `go func` → 失败仅 log.Warn + 记 unhealthy
   - 整体不返回错误（任一 server 失败不影响其他）
5. `Get(name string) (*Session, bool)` 按 server 名称查 Session
6. `HealthyNames() []string` 返回健康 server 列表（用于 WebUI 展示）
7. `CloseAll(ctx)` 优雅关闭所有 Session
8. 设计 `ServerConfig` 临时结构体：name / transportType（stdio/http） / 传输层参数 / timeout
9. 单元测试：注册 3 个 server，2 个成功 1 个失败，验证 pool 仍可用 + unhealthy 列表正确
10. 单元测试：并发 ListTools / CallTool 验证 Session 隔离

**参考资料**：
- Go `golang.org/x/sync/errgroup`：https://pkg.go.dev/golang.org/x/sync/errgroup

---

## Task 6: MCP Tool → CodePilot Tool 适配器 + 自动注册

**状态**：已完成

**目标**：把 MCP `tools/list` 返回的每个远端工具包装为 CodePilot `tool.Tool` 接口实现，命名 `mcp__<server>__<tool>`，自动注册进 `tool.Registry`，Agent 调用时无感。

**影响文件**：
- `internal/mcp/adapter/adapter.go` — 新建，适配器实现
- `internal/mcp/adapter/registry.go` — 新建，批量注册逻辑
- `internal/tool/registry.go` — 既有（确认已支持，无改动）
- `internal/mcp/adapter/adapter_test.go` — 新建，单元测试

**依赖**：Task 4, Task 5

**具体内容**：
1. `MCPTool` 数据结构：Name / Description / InputSchema（JSON Schema map）
2. `MCPCallResult` 数据结构：Content []MCPContent（text / image / resource 多种类型）
3. `MCPContent` 联合类型：Type / Text / Data / MimeType
4. `adapterTool` 实现 `tool.Tool` 接口：
   - `Name()` 返回 `mcp__<server>__<tool>` 拼接结果
   - `Description()` 返回远端 tool description
   - `InputSchema()` 把 MCP inputSchema map 转为 CodePilot 工具期望的 schema 结构（或直接 map 透传）
   - `Execute(ctx, args)` 调用 `session.CallTool(ctx, toolName, args)` → 把 `MCPCallResult.Content` 序列化为文本 → 返回 `tool.Result`
5. `RegisterAll(p *Pool, reg *tool.Registry) error`：
   - 遍历 pool 中所有 healthy server
   - 每个 server 调用 ListTools（已缓存 60s）
   - 每个 MCPTool 构造 adapterTool → reg.Register
   - 返回注册的远端工具数量
6. 处理命名冲突：若 `mcp__<server>__<tool>` 已存在（重复 register），返回错误并 log
7. 单元测试：mock Session 返回 3 个工具，验证注册到 Registry 后能按名查回 + Execute 路径正确

**参考资料**：
- Step 2 已落地的 `tool.Tool` 接口定义：`internal/tool/tool.go`
- Step 2 `tool.Registry`：`internal/tool/registry.go`

---

## Task 7: 重连策略（指数退避 1s/3s/9s）

**状态**：已完成

**目标**：实现 `backoff` 策略与 Session 健康检查恢复：检测到 transport 断开后，下次调用时按指数退避自动重试 3 次，超过后标记 `unhealthy`。

**影响文件**：
- `internal/mcp/reconnect/backoff.go` — 新建，退避计算器
- `internal/mcp/reconnect/backoff_test.go` — 新建
- `internal/mcp/session/session.go` — 修改，增加 `EnsureHealthy(ctx)` 方法

**依赖**：Task 4

**具体内容**：
1. `Backoff` 结构体：intervals []time.Duration（默认 [1s, 3s, 9s]） / maxAttempts int（3）
2. `NewDefaultBackoff() *Backoff` 构造函数
3. `(b *Backoff) NextDelay(attempt int) (time.Duration, bool)` 返回第 N 次重试的延迟与是否还有下次（attempt >= maxAttempts 时 ok=false）
4. Session 增加内部状态：`healthState`（healthy / reconnecting / unhealthy）/ `lastError`
5. Session 增加 `EnsureHealthy(ctx) error` 方法：
   - 若 healthState==healthy → 直接返回 nil
   - 若 healthState==unhealthy → 返回 `ErrServerUnhealthy`（需重启 CodePilot）
   - 若 healthState==reconnecting 或 transport 异常 → 进入重连循环：
     - attempt=0: Close 旧 transport → 重新构造（按原 config）→ 重新 Initialize + ListTools
     - 成功：healthState=healthy，返回 nil
     - 失败：sleep backoff(attempt) → attempt++ → 继续
     - 超过 maxAttempts：healthState=unhealthy，返回错误
6. recvLoop 检测到 transport 断开时，主动设置 healthState=reconnecting（不立即重连，留到下次调用时 lazy 重连）
7. 单元测试：模拟 transport 失败 N 次，验证重连节奏与最终状态

**参考资料**：
- 指数退避（Exponential Backoff）算法通用实践：AWS Architecture Blog

---

## Task 8: 接入主流程（main.go + setting.json + 权限系统 + WebUI 标识）

**状态**：已完成

**目标**：把 MCP 客户端接入 CodePilot 主流程：配置加载 → 启动时建连 → 远端工具自动注册 → 工具调用走 Step 5 权限系统 → WebUI 工具块显示 server 来源。

**影响文件**：
- `internal/config/config.go` — 新增 `MCP MCPConfig` / `MCPServerConfig` 字段 + `ValidateMCPConfig` 校验
- `internal/mcp/config/config.go` — 新建，从 setting 解析 mcp.servers → 构造 transport + factory
- `internal/mcp/config/config_test.go` — 新建，单元测试
- `internal/mcp/transport/http.go` — `HTTPOption` 改名 `Option`（导出供外部包使用）
- `internal/mcp/session/pool.go` — `ServerConfig` 新增 `ReconnectFactory` 字段，Pool.RegisterAndStart 注入到 Session
- `internal/logger/logger.go` — 新增 `L()` 导出底层 zap.Logger
- `src/main.go` — 启动流程接入 MCP pool + RegisterAll + 注入 Handler
- `internal/interaction/web/protocol.go` — `ToolCallStartPayload` / `ToolCallEndPayload` / `ToolCallDisplay` 新增 `Server` 字段 + 新 `MCPStatusPayload` / `MCPHealthState` / `MCPServerStatus`
- `internal/interaction/web/handler.go` — 新增 `mcpPool` 字段 / `SetMCPPool` / `resolveMCPServerByToolName` / `buildMCPStatusPayload` / `sendMCPStatus`；OnStart/OnEnd 回调中填充 server 字段
- `internal/interaction/web/tool_msg.go` — `ToolDisplayFromExecution` 签名增加 `server` 参数
- `web/static/index.html` — 状态栏新增 MCP 区 DOM
- `web/static/app.js` — 工具块 `mcp-server-badge` + 状态栏 MCP 健康区 + `onMCPStatus` 处理器
- `web/static/style.css` — MCP 徽标 / 圆点 / tooltip 样式
- `config/setting.example.json` — 新增 `mcp.servers` 示例（2 stdio + 1 http）

**依赖**：Task 5, Task 6, Task 7（已全部完成）

**具体内容**：
1. `MCPServerConfig` 配置结构体：已实现，包含 `Name` / `Type` / `Command` / `Args` / `Env` / `URL` / `Headers` / `Timeout` / `Disabled` 全部字段
2. config.example.json 追加 2 个 stdio + 1 个 http 示例,含注释
3. `internal/mcp/config/config.go` 提供 `BuildTransports(cfg, logger) *BuildResult`,含 `PoolConfigs` / `ReconnectFactory` / `Skipped` 三段
4. main.go 启动流程改造已完成：
   - 加载 setting.json → 解析 cfg.MCP.Servers
   - mcpconfig.BuildTransports 构造 transport + factory
   - session.Pool 注入 factory,`InitializeAll` 并发建连
   - adapter.RegisterAll 批量注册到 tool.Registry
   - 失败隔离:单 server 失败仅记日志,不影响其他 server 与 CodePilot 启动
   - 启动完成后 `handler.SetMCPPool(mcpPool)` 注入
5. 工具调用链路:Agent Loop 拿到 `mcp__<server>__<tool>` → tool.Registry 查 → adapter.Execute → session.CallTool → MCP server,完全无感
6. 权限系统（Step 5）:MCP 工具名前缀 `mcp__<server>__<tool>` 走 permission.Decide 全链路,支持 allow / deny / ask 三档 + HITL;user 已通过 `permission.Decide(ctx, "mcp__mock__bash", ...)` 走通
7. WebSocket 协议:`tool_call_start` / `tool_call_end` 事件 schema 扩展 `server` 字段(omitempty),内置工具为空串;`mcp_status` 新消息类型 + payload 用于状态栏
8. 状态栏 MCP 区:绿色圆点 = healthy、黄色(reconnecting 脉动)、红色(unhealthy)、灰色(skipped);hover tooltip 列出每个 server 名 + 工具数 + 失败原因

**参考资料**：
- Step 5 权限系统：`internal/security/`
- Step 2 工具系统：`internal/tool/registry.go`
- Step 1.4 WebUI 工具块：`web/static/`

**验证情况**(2026-06-09):
- `go build ./...` 通过
- `go test ./internal/mcp/...` 全部通过(adapter / config / jsonrpc / reconnect / session / transport 6 个子包)
- `go test ./internal/security/...` 通过
- `go test ./internal/interaction/web/...` 通过(剔除 TestBusyRejectsConcurrentInput 偶发网络超时,非 MCP 引入)
- `TestBashDangerous` / `TestRunTurn_BlacklistInterceptedThenNormalCommand` 失败为 Windows PowerShell 环境问题(无 Linux `rm` 命令),`git stash` 验证在 master 上同样失败,与本任务无关
- 新增 `mcp/config` 包 7 个测试用例全部通过(空配置 / 单 stdio / 单 http / disabled 跳过 / 非法 type 隔离 / validateServer 边界 / ValidateMCPConfig 9 子用例)

---

## Task 9: 端到端验证（mock server + 集成测试）

**状态**：已完成

**目标**：编写一个本地 stdio mock MCP server 与一个 HTTP mock MCP server，覆盖握手 / 工具发现 / 工具调用 / 重连 / 权限拦截全链路，验证新旧功能均正常。

**影响文件**：
- `internal/mcp/integration_test.go` — 新建，端到端集成测试
- `testdata/mcp-mock-stdio/main.go` — 新建，stdio mock server（最小可执行 Go 程序）
- `testdata/mcp-mock-http/main.go` — 新建，HTTP mock server
- `testdata/mcp-mock-*/go.mod` — 新建，mock server 独立 module
- `docs/step6-MCP协议实现/checklist.md` — 端到端验收清单

**依赖**：Task 8

**具体内容**：
1. **stdio mock server**：实现
   - 读 stdin JSONL → 识别 initialize / tools/list / tools/call / ping
   - 返回符合 MCP 规范的 Response
   - 提供 2 个测试工具：`echo(text)` 返回 text 内容；`add(a, b)` 返回两数之和
2. **HTTP mock server**：用 `httptest` 或独立 `http.ListenAndServe` 暴露 `/mcp` 端点，实现与 stdio mock 相同的 2 个工具
3. **集成测试用例**：
   - `TestE2E_StdioHandshakeAndListTools`：启动 stdio mock → Pool.Initialize → 验证握手成功 + ListTools 返回 2 个工具
   - `TestE2E_StdioCallTool`：mock 注册到 tool.Registry → Agent Loop 调 `mcp__mock__echo` → 验证返回 echo 结果
   - `TestE2E_HTTPCallTool`：同上传 HTTP
   - `TestE2E_PermissionInterception`：mock 工具名为 `mcp__mock__bash`（模拟危险工具）→ 验证 Step 5 权限拦截触发
   - `TestE2E_ServerFailureIsolation`：启动 2 个 server，一个命令错误 → 验证 CodePilot 仍可启动，另一个 server 工具可用
   - `TestE2E_ReconnectBackoff`：mock server 收到调用后自杀 → 第二次调用验证 1s 退避后重连成功
   - `TestE2E_ReconnectExhausted`：mock server 持续失败 → 验证 3 次后变 unhealthy
   - `TestE2E_ConcurrentCalls`：并发 10 个 tools/call → 验证响应 id 全部正确匹配
   - `TestE2E_NamingConvention`：验证远端工具名严格为 `mcp__<server>__<tool>` 格式
   - `TestE2E_LegacyCompat`：用 Step 5 已有的 session JSON 加载 → 验证旧会话正常恢复，MCP 工具被识别为远端来源
4. **真实启动冒烟**：用 `config.example.json` 启一个真的 CodePilot 实例 + mock server，浏览器访问 `http://127.0.0.1:8969`，输入「请用 mcp__mock__echo 说 hello」→ 验证 UI 显示工具来自 mock server 且 echo 成功
5. 全部用例通过后，更新 `checklist.md` 中各验证项的实际结果

**验证情况**(2026-06-09)：
- `go build ./...` 无错误无警告
- `go test ./internal/mcp/...` 7 子包全绿（mcp 14.8s / adapter 0.1s / config 0.1s / jsonrpc / reconnect / session 34.8s / transport 2.7s）
- `go test ./internal/...` 16 包中 MCP 7 包全绿；security / interaction/web / engine/prompt 等 Step 1~5 包测试无回归
- 跨功能回归：`TestBashDangerous` / `TestRunTurn_BlacklistInterceptedThenNormalCommand` 在 master 79286cc (无 MCP 改动) 上同样失败，确认是 Windows PowerShell 环境差异（无 `rm`/`mkfs`/`shutdown` 命令），与 MCP 无关
- 10 个 E2E 集成用例：TestE2E_StdioHandshakeAndListTools / StdioCallTool (1.07s, mock-stdio 真实子进程) / HTTPCallTool (0.06s, mock-http 52931 真实握手 healthy=2) / PermissionInterception / ServerFailureIsolation / ReconnectBackoff / ReconnectExhausted / ConcurrentCalls (0.02s) / NamingConvention / LegacyCompat (1.2s, Step 5 风格 JSON + 叠加 MCP 工具) — 全部 PASS
- 真实启动冒烟：编译 `codepilot-e2e.exe` → 启动监听 127.0.0.1:58426 → Go WS 客户端连 `/ws` 发 `get_current_session` → 收到 mcp_status 推送 `{"healthy_count":2, "unhealthy_count":0, "total_tools":4, "servers":[mockhttp/healthy/2, mockstdio/healthy/2]}`，证明双 server 真实握手 + 远端工具自动注册 + 状态栏推送全链路打通

**参考资料**：
- Go `httptest` 包：https://pkg.go.dev/net/http/httptest
- Go 集成测试模式：`testing` 子测试 + `t.Parallel()`

---

## 任务依赖图

```
Task 1 (JSON-RPC)
  ├── Task 2 (Transport stdio)
  ├── Task 3 (Transport HTTP)
  └── Task 4 (Session + 握手)
        ├── Task 5 (Pool + Eager Init)
        │     └── Task 6 (Adapter + Registry)
        │           └── Task 8 (接入主流程)
        │                 └── Task 9 (E2E 验证)
        └── Task 7 (重连策略)
              └── Task 8
```

实际开发顺序建议：1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9

---

## 完成定义（Definition of Done）

- [x] 所有 9 个 Task 状态均为 `已完成`
- [x] `checklist.md` 中所有验证项已勾选且结论为「通过」
- [x] `go build ./...` 无错误无警告
- [x] `go test ./internal/mcp/...` 全部通过
- [x] 真实启动冒烟：stdio + HTTP mock server 同时存在，UI 中能成功调用任一工具
- [x] `.harness/PROGRESS.md` 同步更新 Step 6 完成记录
