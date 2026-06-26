# MCP 集成 — CodePilot 实现原理

> 隶属 Step 6（MCP 协议实现）| 架构层:第 3 层 工具层 | 核心入口:`src/internal/mcp/session/pool.go`

## §1 模块定位

MCP(Model Context Protocol)集成位于第 3 层 工具层,通过 JSON-RPC 2.0 协议接入外部工具服务器,动态发现与调用工具,扩展 Agent 能力边界。

- **JSON-RPC 2.0** — 标准协议,支持 `initialize / initialized / tools/list / tools/call` 等方法
- **stdio / HTTP 双传输** — `src/internal/mcp/transport/stdio.go` + `src/internal/mcp/transport/http.go`
- **三阶段握手** — `initialize` → `notifications/initialized` → `tools/list`
- **连接池** — `Pool`(src/internal/mcp/session/pool.go:66)统一管理多个 MCP server session
- **适配器自动注册** — MCP `tools/list` 返回的工具自动注册到 `tool.Registry`(代码在 `src/internal/mcp/adapter/`)
- **指数退避重连** — `reconnect.Backoff` 默认 1s/3s/9s 三次重试(`src/internal/mcp/reconnect/backoff.go:51`)
- **WebUI MCP 徽标** — 三元状态:`mcpPool==nil` 未启用 / `Initializing()==true` 连接中 / 否则已就绪或已失败

## §2 核心数据结构

- `Transport`(`src/internal/mcp/transport/transport.go:39`)— 抽象接口,`Connect / Send / Recv / Close / IsAlive`
- `Message = []byte`(transport.go:22)— 单条 JSON-RPC 消息字节序列
- `ErrClosed / ErrNotConnected`(transport.go:27, 30)— Transport 已关闭 / 未连接错误
- `StdioConfig`(`src/internal/mcp/transport/stdio.go:15`)— stdio 传输配置,字段 `Command / Args / Env / Workdir / Stderr / CloseTimeout / MaxLineBytes`
- `stdioTransport`(stdio.go:40)— stdio 实现,内部 `cmd / stdin / scanner / done / alive / closed`
- `httpTransport`(`src/internal/mcp/transport/http.go:93`)— HTTP 实现,内部 `url / client / bearerToken / basicAuth / extraHeaders / timeout / sessionID / respCh`
- `Option(http.go)` — Functional Options 模式,`WithBearerToken / WithBasicAuth / WithHTTPHeader / WithHTTPTimeout / WithHTTPClient`
- `HTTPError`(http.go:71)— HTTP 状态码非 2xx 错误,字段 `StatusCode / Body`
- `Pool`(`src/internal/mcp/session/pool.go:66`)— 连接池,字段 `sessions / unhealthy / initializing atomic.Bool / closed atomic.Bool`
- `Session`(session/pool.go)— 单个 MCP server 会话,封装 Transport + JSON-RPC client
- `Backoff`(`src/internal/mcp/reconnect/backoff.go:32`)— 指数退避器,字段 `intervals []time.Duration`
- `NewDefaultBackoff`(backoff.go:51)— 默认 1s/3s/9s/0(共 4 次 attempts)

## §3 关键流程

### 3.1 三阶段握手

`Pool.RegisterAndStart`(`src/internal/mcp/session/pool.go:103`)流程:

1. **Connect**:调 `transport.Connect(ctx)` 启动 stdio 子进程(`stdio.go:73`)或 HTTP 请求探测(http.go Connect)
2. **Initialize**:发 `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"...","capabilities":{},"clientInfo":{...}}}`
3. **Initialized**:发 `notifications/initialized`(notification 无 id)
4. **ListTools**:发 `tools/list` 拿工具列表(JSON Schema)

[Why] 通知(`notifications/initialized`)用 notification 而非 request:**Why** MCP 协议规定 initialized 是单向通知,客户端无需等响应;若当 request 处理,服务端 response 会阻塞握手流程。

### 3.2 stdio Transport

`stdioTransport.Connect(ctx)`(`stdio.go:73`)流程:

1. `exec.CommandContext(ctx, "sh" -c ...)` 启动子进程(Windows 用 PowerShell)
2. `cmd.StdinPipe() / StdoutPipe()` 拿 stdin / stdout pipe
3. `bufio.NewScanner(stdout)` + `scanner.Buffer(64KB, MaxLineBytes)` 配 scanner
4. 启动后台 goroutine `go func() { cmd.Wait(); t.alive = false; close(t.done) }()` 等待子进程退出
5. 设置 `alive = true` 返回

`stdioTransport.Recv()`(`stdio.go:173`)用「短命 goroutine 跑 scanner.Scan() + select 等结果 / done」模式,避免 scanner 阻塞与 Close 死锁:

- 子进程退出时 `done` 通道关闭 → 立即返回 `io.EOF`
- scanner 读到数据 → 复制 bytes(避免 scanner 内部 buffer 复用导致 race)后返回

[Why] 短命 goroutine:**Why** scanner.Scan() 是阻塞 IO;若直接调 Recv 阻塞,Close 时死锁(等不到 scanner 返回)。短命 goroutine + done 通道让 Close 能强制打断。

### 3.3 HTTP Transport(MCP Streamable HTTP)

`httpTransport.Connect(ctx)`(http.go)HTTP 单次 POST 即获得完整 request-response,但 Transport 接口要求 Send/Recv 分开:

- **设计**:`Send` 内部发请求 + 读 body + 解析为消息,投递到内部 `respCh`;`Recv` 从 `respCh` 读
- 调用方必须按 Send → Recv 顺序串行调用,与 stdio 语义一致

`httpTransport.Recv()`(http.go:265)用「等 ch 出现 → 短超时读 → 重新检查」模式跟随 `t.respCh` 变化:

- 5ms 内没数据说明这条 ch 还没被 Send 填充,重新循环拿最新 ch(可能被 Send 重建)
- Recv 锁住旧 ch 引用,Send 重建的新 ch 永远没人读,会导致永久阻塞

[Why] 跟随 ch 变化而非锁固定 ch:**Why** Send 每次调用都会重建 respCh;若 Recv 锁住旧 ch,Send 重建的新 ch 永远没人读导致永久阻塞。短超时 + 重新检查是「让 Recv 始终跟最新 ch」的关键。

### 3.4 连接池管理

`Pool.InitializeAll`(调用入口)按 server 配置逐个调 `RegisterAndStart`:

- **成功**:`sessions[name] = session`
- **失败**:`unhealthy[name] = record`,WebUI MCP 徽标显示「失败」状态
- **连接中**:`Pool.initializing.Store(true)`,WebUI 显示「连接中…」
- **全部完成**:`Pool.initializing.Store(false)`

`Pool.Initializing() bool`(`pool.go:86`)用 `atomic.Bool.Load()` 而非读锁——与 `closed` 同风格,避免与 `sessions/unhealthy` 读写锁竞争。WebUI 高频轮询 mcp_status.Loading 字段时无锁竞争。

[Why] atomic.Bool 而非 mu:**Why** InitializeAll 的入口/出口 Store 是稀疏事件,WebUI 层高频只读 Load;锁会导致 WebUI 与 sessions map 读写竞争。

### 3.5 指数退避重连

`Session.EnsureHealthy()`(调用入口)持 `Backoff` 引用:

```go
for attempt := 0; ; attempt++ {
    delay, ok := b.NextDelay(attempt)
    if !ok { return ErrExhausted }
    select {
    case <-time.After(delay):
    case <-ctx.Done():
        return ctx.Err()
    }
    if err := tryReconnect(); err == nil { return nil }
}
```

`Backoff.NextDelay(attempt) (time.Duration, bool)`(`backoff.go:109`)按 `intervals[attempt]` 返回延迟:

- 默认 `[1s, 3s, 9s, 0]`(共 4 次 attempts):前 3 次失败按 1s/3s/9s 退避,第 4 次不 sleep 直接最终确认
- 第 4 次成功后即恢复;仍失败则返回 `ErrExhausted`,调用方标记 session 为永久 unhealthy

[Why] intervals 长度 4 而非 3:**Why** MaxAttempts=4 让「第 3 次退避后第 4 次成功」成为可能(如网络抖动场景),同时保留 spec 描述的「3 次后 unhealthy」语义。

### 3.6 适配器自动注册 MCP 工具

`mcp/adapter/registry.go` 把 MCP `tools/list` 返回的工具转 `tool.Tool` 接口实现:

1. 对每个 MCP tool 构造 `mcpToolAdapter`,字段含 `name / description / inputSchema / session / timeout`
2. `tool.Registry.Register(adapter)` 注册到全局 Registry
3. LLM 调 tool 时,`Execute` 把 `input` 转 JSON-RPC `tools/call` 发给 MCP server,响应回传为 `ToolResultBlock`

## §4 与其他模块的依赖

- **上游**(MCP 模块依赖):
  - `internal/tool.Registry`(`src/internal/tool/registry.go:12`)— MCP 工具注册目标
  - `internal/config`(MCP server 配置)— `~/.codepilot/setting.json` + `<cwd>/.codepilot/setting.json`
  - `internal/logger`(`src/internal/logger/`)— 连接池日志
- **下游被依赖**:
  - `internal/interaction/web/handler`(Pool 引用)— 启动期调 InitializeAll,运行期推送 MCP 状态变更事件
  - `main.go`(`src/main.go`)— Pool 装配 + 三阶段握手初始化

## §5 设计决策

### 决策 1:JSON-RPC 2.0 标准协议

- **问题**:MCP 是 Anthropic 推动的新协议,SDK 不成熟
- **方案**:手写 JSON-RPC 2.0 client,不复用第三方库
- **理由**:**Why** JSON-RPC 2.0 协议简单(单行 JSON + newline),手写 ~300 行即完整支持;第三方 SDK 引入额外依赖且与 Anthropic SDK 命名冲突

### 决策 2:stdio + HTTP 双传输统一抽象

- **问题**:stdio 与 HTTP 是 MCP 两种部署模式(本地工具 vs 远程服务),API 不一致
- **方案**:`Transport` interface(`transport.go:39`),`stdioTransport / httpTransport` 两种实现
- **理由**:**Why** 双实现让 Pool / Session 不感知传输细节;新增 WebSocket 等传输只需新写一个实现

### 决策 3:三段握手而非两段

- **问题**:MCP 协议要求 client → server 发 initialize(server 回 response)→ client 发 initialized 通知
- **方案**:`Connect → Initialize → Initialized → ListTools` 四步(虽 spec 说是「三阶段」)
- **理由**:**Why** initialized 是 notification 而非 request,client 收到 initialize response 后立即发通知,无需等响应;若当 request 处理会阻塞握手

### 决策 4:Backoff 长度 4 而非 3

- **问题**:spec 描述「1s/3s/9s 共 3 次 attempts」,但第 3 次退避后第 4 次成功场景需要支持
- **方案**:`intervals = [1s, 3s, 9s, 0]`,第 4 次 attempts 不 sleep 直接最终确认
- **理由**:**Why** MaxAttempts=4 让「3 次全失败但第 4 次恢复」成为可能(如网络抖动恢复);第 4 次 0 延迟保证「3 次后立即 unhealthy」的 spec 语义不被破坏

### 决策 5:三元 MCP 状态可观测

- **问题**:MCP 启动是异步(在后台 goroutine 跑 InitializeAll),WebUI 需知道当前是「未启用 / 连接中 / 已就绪或已失败」
- **方案**:`mcpPool==nil` → 未启用;`mcpPool.Initializing()==true` → 连接中;否则 → 已就绪或失败(失败从 unhealthy map 拿详情)
- **理由**:**Why** 三元状态比单一 bool 更精确;用户能在连接中阶段看到「MCP 正在连接…」loading 态,避免误以为 hang 死

## §6 关键文件索引

| 路径 | 角色 |
|------|------|
| `src/internal/mcp/transport/transport.go:39` | `Transport` 传输抽象接口 |
| `src/internal/mcp/transport/stdio.go:73` | `stdioTransport.Connect` 启动子进程 |
| `src/internal/mcp/transport/stdio.go:173` | `stdioTransport.Recv` 短命 goroutine 读 scanner |
| `src/internal/mcp/transport/http.go:93` | `httpTransport` HTTP 实现 |
| `src/internal/mcp/transport/http.go:265` | `httpTransport.Recv` 跟随 respCh 变化 |
| `src/internal/mcp/session/pool.go:66` | `Pool` 连接池 |
| `src/internal/mcp/session/pool.go:80` | `initializing atomic.Bool` 状态标识 |
| `src/internal/mcp/session/pool.go:103` | `RegisterAndStart` 同步拉起单个 server |
| `src/internal/mcp/reconnect/backoff.go:32` | `Backoff` 退避器 |
| `src/internal/mcp/reconnect/backoff.go:51` | `NewDefaultBackoff` 默认 1s/3s/9s |
| `src/internal/mcp/jsonrpc/` | JSON-RPC 2.0 client 实现 |
| `src/internal/mcp/adapter/registry.go` | MCP 工具自动注册到 tool.Registry |
| `src/internal/mcp/config/` | MCP server 配置解析 |