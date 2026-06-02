# Step 13 Tasks — Web 交互界面重构

---

## Task 1: 彻底删除 TUI 代码与依赖

**状态**：已完成

**目标**：移除 Step 1 引入的 Bubble Tea TUI 体系，使项目为后续 Web 改造扫清障碍。

**影响文件**：
- `src/internal/interaction/tui/` — 整目录删除（app.go / logo.go / statusbar.go / message.go）
- `go.mod` / `go.sum` — 移除 bubbletea / bubbles / glamour / lipgloss 依赖
- `src/main.go` — 移除对 `tui` 包的引用（占位修改，后续 Task 7 重写）

**依赖**：无

**具体内容**：
1. 删除 `src/internal/interaction/tui/` 整个目录
2. 执行 `go mod tidy` 清理 `go.mod` / `go.sum` 中残留的 bubbletea / bubbles / glamour / lipgloss
3. 修改 `src/main.go`，注释或移除 TUI 启动代码，临时保留 `fmt.Println` 占位以确保编译通过
4. 执行 `go build ./...` 确认无编译错误、无 TUI 包残留引用

**参考资料**：
- Go Modules 清理：`go mod tidy`
- 验证包无引用：`grep -r "tui" src/`

---

## Task 2: Web 服务器 + WebSocket 骨架

**状态**：已完成

**目标**：搭建 HTTP 服务骨架，监听 8969 端口，支持静态资源托管（embed.FS）与 WebSocket 升级，实现连接管理。

**影响文件**：
- `src/internal/interaction/web/server.go` — 新建，HTTP 服务与路由
- `src/internal/interaction/web/websocket.go` — 新建，WebSocket 连接管理
- `src/internal/interaction/web/static/` — 新建目录，存放前端资源（首期仅放占位 index.html）
- `go.mod` — 新增 `github.com/gorilla/websocket` 依赖

**依赖**：Task 1

**具体内容**：
1. 引入 `gorilla/websocket` 库（执行前用 context7 查询最新版本与 API）
2. 在 `static/` 目录下创建占位 `index.html`（仅显示 "CodePilot Web" 字样用于验证静态托管）
3. 在 `server.go` 中实现 `Server` 结构体，持有监听地址、logger 引用、embed.FS 句柄
4. 使用 `//go:embed static` 指令将 static 目录嵌入二进制
5. 注册 HTTP 路由：
   - `GET /` → 返回嵌入的 `index.html`
   - `GET /static/*` 或类似路径 → 返回对应静态文件
   - `GET /ws` → 升级为 WebSocket 连接
6. 实现 `http.Server`，配置监听 `127.0.0.1:8969`，使用 `http.ListenAndServe` 启动
7. 在 `websocket.go` 中实现 `ConnectionManager`：
   - 维护 `map[*websocket.Conn]struct{}` 与 `sync.RWMutex`
   - 提供 `Add/Remove/Broadcast` 方法（广播用于后续 Task 4 推送状态）
   - 实现 `handleWS(w, r)` 升级 HTTP 连接为 WebSocket，校验 Origin 防止跨站劫持
8. 实现 `Server.Start(ctx) error` 与 `Server.Shutdown(ctx) error` 用于优雅启停
9. 端口被占用时返回明确错误信息，提示用户检查并退出
10. 编写单元测试：模拟 HTTP 请求验证 `/` 返回 index.html、模拟 WS 握手验证连接升级

**参考资料**：
- `gorilla/websocket` 最新 API（用 context7 查询）
- Go 1.16+ embed：`//go:embed` 指令、`embed.FS`
- Go 标准库 `net/http` 的 `FileServer` 与 `http.ServeFile`

---

## Task 3: WebSocket 消息协议定义与路由分发

**状态**：已完成

**目标**：定义客户端/服务端 WebSocket 消息 JSON Schema，实现统一编解码与消息路由分发。

**影响文件**：
- `src/internal/interaction/web/protocol.go` — 新建，消息类型与编解码
- `src/internal/interaction/web/router.go` — 新建，消息路由分发

**依赖**：Task 2

**具体内容**：
1. 在 `protocol.go` 中定义消息格式（统一为 JSON）：
   - 客户端→服务端类型：`user_input`、`list_sessions`、`new_session`、`resume_session`、`abort_stream`
   - 服务端→客户端类型：`stream_chunk`、`stream_done`、`stream_error`、`session_list`、`session_loaded`、`status_update`、`context_usage`
2. 定义基础信封 `Message` 结构：`Type`（string）、`Payload`（json.RawMessage）
3. 为每个消息类型定义具体 payload 结构体（用户输入、chunk 内容、会话摘要等）
4. 实现 `Encode(msg Message) ([]byte, error)` 与 `Decode(data []byte) (Message, error)`
5. 在 `router.go` 中实现 `Router`：
   - 持有 `map[string]HandlerFunc`（消息类型 → 处理函数）
   - 提供 `Register(typ string, h HandlerFunc)` 与 `Route(msg Message, conn *websocket.Conn) error`
   - 收到未知类型时记录日志并发送错误消息
6. 修改 `websocket.go` 的连接 read loop，调用 `router.Route` 分发消息
7. 编写单元测试：编解码、路由分发的正确性

**参考资料**：
- Go 标准库 `encoding/json`、`json.RawMessage`
- gorilla/websocket 读写消息 API

---

## Task 4: 业务 handler 实现

**状态**：已完成

**目标**：把 WebSocket 消息路由到 ConversationManager / SessionManager / Provider，实现完整业务流。

**影响文件**：
- `src/internal/interaction/web/handler.go` — 新建，所有消息 handler

**依赖**：Task 3

**具体内容**：
1. 在 `handler.go` 中定义 `Handler` 结构体，持有：
   - Provider 实例
   - ConversationManager 实例
   - SessionManager 实例
   - Config（用于读取模型名）
   - ConnectionManager 实例（用于状态广播）
2. 实现各 handler：
   - `handleUserInput(conn, payload)`：
     - 从 payload 取出用户输入文本，调用 `convMgr.AddUserMessage` 添加到上下文
     - 调用 `Provider.StreamChat` 获取流 channel
     - 启动 goroutine 从 channel 读取 chunk，逐个发送 `stream_chunk` 消息
     - 完成后发送 `stream_done`，调用 `sessMgr.Save` 持久化会话
     - 同时启动 goroutine 监听 `abort_stream` 消息，收到后调用 cancelFunc 终止 Provider 请求
   - `handleListSessions(conn)`：调用 `sessMgr.ListSessions()`，将结果封装为 `session_list` 消息发送
   - `handleNewSession(conn)`：保存当前会话（若有），创建新 Session，重置 convMgr，发送 `session_loaded` 消息
   - `handleResumeSession(conn, id)`：支持 ID 前缀匹配，调用 `sessMgr.Load` 加载目标会话，重置 convMgr 并注入历史消息，发送 `session_loaded`
   - `handleAbortStream(conn)`：调用当前流式请求的 cancelFunc（若无正在进行的请求则忽略）
3. 实现 `ctx` 状态机：每个连接维护 `cancelFunc context.CancelFunc` 与 `sync.Mutex`，确保同一时刻只有一个流式请求
4. 错误处理：所有 handler 内错误通过 `stream_error` 消息返回给客户端，不影响连接
5. 编写单元测试：使用 mock Provider 验证流式 chunk 正确转发

**参考资料**：
- Step 1 已有的 `ConversationManager`、`SessionManager` 接口
- `context.WithCancel` 用于中断流式请求
- Step 1 Task 9 中 TUI 的 `cancelFunc` 使用模式

---

## Task 5: 前端 UI 框架 + 深色编辑式美学

**状态**：已完成

**目标**：实现页面 HTML 骨架与 CSS 样式，遵循 frontend-design skill 的深色编辑式美学方向（参考 Linear / Vercel / Raycast 风格）。

**影响文件**：
- `src/internal/interaction/web/static/index.html` — 新建，页面结构
- `src/internal/interaction/web/static/style.css` — 新建，样式（深色编辑式美学）
- `src/internal/interaction/web/static/vendor/` — 新建，第三方库目录（marked.js 等，源码嵌入）

**依赖**：Task 2

**具体内容**：
1. **调用 frontend-design skill**：在 `static/` 目录下设计页面时调用 `.harness/skills/frontend-design/SKILL.md` 的设计原则，提交"深色编辑式 / 极简主义"美学方向
2. **字体选型**（避免 Inter / Roboto / Arial）：
   - Display 字体（标题、LOGO）：推荐 Berkeley Mono / GT America Mono / Söhne Mono / JetBrains Mono
   - Body 字体：推荐 Inter Display / Switzer / Söhne / Geist
   - 通过 Google Fonts 或 fontsource 引入（避免本地打包字体文件）
3. **配色**（CSS 变量统一管理）：
   - 背景：深炭黑（如 `#0A0A0B` 或 `#0D0E10`）
   - 前景：暖白（如 `#E8E8E8`）
   - 强调色：选择单一品牌色（避免紫色渐变），推荐琥珀橙、电气蓝或翡翠绿
   - 分割线：低饱和灰（如 `#1F2024`）
   - 代码块背景：略亮深色（如 `#131316`）
4. **布局骨架**（CSS Grid）：
   - 整体三区：顶部信息栏（高 56px）、中部双栏（左侧 280px 会话栏 + 右侧自适应主区）、底部输入栏
   - 主区又分：消息流（flex:1 可滚动）+ 输入栏（高 140px）
5. **首页实现**：
   - 顶部 LOGO + 名称 + 版本号 + 开源地址 + 工作空间路径
   - 左侧会话历史列表（空状态时显示 "尚无历史会话"）
   - 中间消息流（空状态时显示欢迎文案 + 快捷命令提示）
   - 底部输入框（textarea）+ 右侧状态信息（模型名、ctx 剩余百分比、Agent 状态）
6. **响应式适配**：保证 1280px+ 桌面端最佳；768-1280px 自适应；<768px 暂不优化
7. **无 JS 也能展示基础页面**（渐进增强）：未连接 WebSocket 时显示加载占位

**参考资料**：
- `.harness/skills/frontend-design/SKILL.md` 设计原则
- CSS Grid / Flexbox 布局
- Google Fonts 引入方法（`<link>` 或 `@import`）
- 优秀参考：Dribbble "Linear UI"、Vercel 官网、Raycast Store

---

## Task 6: 前端交互逻辑实现

**状态**：已完成

**目标**：实现 WebSocket 客户端、Markdown 渲染、`/` 命令下拉候选、状态栏实时更新、流式渲染等核心交互。

**影响文件**：
- `src/internal/interaction/web/static/app.js` — 新建，前端逻辑
- `src/internal/interaction/web/static/vendor/marked.min.js` — 新建，引入 marked.js 源码（v12+）

**依赖**：Task 3, Task 5

**具体内容**：
1. **WebSocket 客户端**：
   - 页面加载时建立 `ws://localhost:8969/ws` 连接
   - 实现重连机制：断连后每 3s 尝试重连，最多 10 次
   - 提供 `sendMessage(type, payload)` 与 `onMessage(type, handler)` API
2. **消息渲染**：
   - 用户消息：右对齐，气泡样式，浅色背景
   - 助手消息：左对齐，撑满宽度，Markdown 渲染
   - 流式过程中助手消息以打字机效果逐字追加，完成后转为静态
3. **Markdown 渲染**：
   - 引入 marked.js（v12+）作为 `vendor/marked.min.js`，在 index.html 通过 `<script>` 加载
   - 配置 GFM 模式、代码块语法高亮（引入 highlight.js 或自实现简单高亮）
   - 流式 chunk 到达时先按纯文本追加，待 stream_done 后再做一次完整渲染（避免半截 Markdown 标记）
4. **`/` 快捷命令下拉候选**：
   - 监听 textarea 的 `input` 事件，检测是否以 `/` 开头且未含空格
   - 弹出下拉列表，列出候选命令（本步骤仅 `/new`、`/sessions`、`/resume <id>`）
   - 键盘 ↑↓ 选择、Enter 补全、Esc 关闭
   - 选择 `/resume ` 时进入参数输入模式（弹出二级提示 "输入会话 ID 前缀"）
5. **状态栏实时更新**：
   - 监听 `status_update` 消息，更新 Agent 状态（空闲 / 思考中 / 错误）
   - 监听 `context_usage` 消息，更新 ctx 剩余百分比
   - 每次流式 chunk 到达后本地估算 token 使用量并更新
6. **历史会话加载**：
   - 页面加载时发送 `list_sessions`，收到 `session_list` 后渲染左侧列表
   - 点击会话项发送 `resume_session`，收到 `session_loaded` 后重渲染中间区域
7. **中断流式**：
   - 输入框右侧（流式进行中）显示"停止"按钮，点击发送 `abort_stream`
8. **优雅体验细节**：
   - 流式过程中自动滚动到底部（用户手动滚动后停止自动滚动）
   - 错误消息以红色卡片展示在最新消息下方
   - 输入框禁用 Enter 换行（Shift+Enter 才换行），单行直接发送
9. **浏览器开发者工具警告检查**：确保 console 无 error

**参考资料**：
- `marked.js` 文档（用 context7 查询）
- WebSocket 浏览器 API：`new WebSocket(url)`、`send()`、`onmessage`
- CSS `position: sticky` 与 `overflow-y: auto` 实现消息流滚动
- 键盘事件：`keydown` 的 `key` / `code` 字段

---

## Task 7: 浏览器自动打开（跨平台）

**状态**：已完成

**目标**：实现跨平台调用系统默认浏览器打开 `http://localhost:8969`。

**影响文件**：
- `src/internal/interaction/web/browser.go` — 新建，跨平台打开浏览器

**依赖**：Task 2

**具体内容**：
1. 在 `browser.go` 中实现 `OpenURL(url string) error`：
   - Windows：`rundll32 url.dll,FileProtocolHandler <url>`（通过 `cmd /c start` 或 `exec.Command`）
   - macOS：`open <url>`（通过 `exec.Command`）
   - Linux：`xdg-open <url>`（通过 `exec.Command`）
2. 通过 `runtime.GOOS` 判定平台
3. 打开失败时仅返回错误，不 panic；调用方决定如何处理（打印警告 vs 中断）
4. 编写单元测试：仅验证命令构造正确（不实际打开浏览器），使用 `exec.Command` 的 mock 模式

**参考资料**：
- Go 标准库 `runtime.GOOS`（值：`windows` / `darwin` / `linux`）
- Go 标准库 `os/exec`

---

## Task 8: 接入主流程

**状态**：已完成

**目标**：将 Web 服务、配置加载、Provider 初始化、会话恢复等步骤串联到 `main.go`，形成完整启动链路与优雅退出。

**影响文件**：
- `src/main.go` — 重写，启动流程编排

**依赖**：Task 2, Task 3, Task 4, Task 6, Task 7

**具体内容**：
1. 在 `main()` 中按顺序：
   - `logger.Init()`（失败时 fallback 到 stdout）
   - `config.Load()`
   - `llm.NewProvider(cfg)`
   - `session.NewSessionManager(...)`
   - 创建 `Handler` 实例，注入 Provider / SessionManager / Config
   - 创建 `web.Server` 实例，注入 Handler / ConnectionManager
   - 启动 server：`go server.Start(ctx)`（或同步启动）
   - 调用 `web.OpenURL("http://localhost:8969")`，失败时打印警告
   - 等待 `SIGINT` / `SIGTERM` 触发优雅退出
2. 优雅退出：
   - 收到信号后调用 `server.Shutdown(ctx)` 关闭 HTTP 服务（关闭前 WebSocket 广播 `stream_done` 通知前端）
   - 调用 `sessMgr.Save` 持久化当前会话
   - 调用 `logger.Sync()` 刷新日志
   - `os.Exit(0)`
3. 异常处理：
   - 配置文件不存在或格式错误：友好提示后退出
   - Provider 初始化失败：提示后退出
   - 端口 8969 占用：打印明确错误后退出
4. 验证：`go build ./...` 通过，单一二进制可直接运行

**参考资料**：
- Go 信号处理：`signal.Notify` 监听 `os.Interrupt` / `syscall.SIGTERM`
- `context.WithCancel` 用于主流程取消
- `http.Server.Shutdown` 用于优雅关闭 HTTP 服务

---

## Task 9: 端到端验证

**状态**：已完成

**目标**：在本地实际运行 codepilot 二进制并打开浏览器，验证 spec 中的所有能力清单与 checklist 中的所有验收点。

**影响文件**：
- 无新文件，验证已有功能

**依赖**：Task 1 ~ Task 8 全部完成

**具体内容**：
1. 启动 `codepilot` 命令，验证：
   - 终端输出启动日志，浏览器自动打开
   - 顶部栏显示 LOGO / 名称 / 版本号 / 开源地址 / 工作空间路径
   - 左侧会话栏展示历史会话（如有）或显示空状态
   - 中间区域显示欢迎文案
   - 底部状态栏显示模型名、ctx 100%、状态"空闲"
2. 发送消息并验证：
   - 流式 chunk 逐字渲染，状态栏切换"思考中"
   - Markdown 正确渲染（代码块、列表、粗体）
   - 完成后状态栏切回"空闲"，ctx 百分比下降
   - 会话文件已写入 `~/.codepilot/sessions/`
3. 中断流式：流式过程中点击停止按钮，验证：
   - 流立即终止，已输出部分保留
   - 状态栏切回"空闲"
4. 切换会话：
   - 点击左侧历史会话，验证主区域切换为目标会话历史
   - 状态栏 ctx 百分比同步更新
5. `/` 快捷命令：
   - 输入 `/` 弹出候选列表
   - 输入 `/new` 触发新建会话
   - 输入 `/sessions` 弹出系统提示（Web 版不支持，等同回车发送）
6. 错误场景：
   - 配置错误 API Key 后启动，发送消息验证错误以红色卡片展示，用户可继续输入
7. 优雅退出：
   - 关闭浏览器标签后，后端进程继续运行
   - 终端 Ctrl+C 触发后端退出，验证会话已保存
8. 跨平台：分别在 Windows / macOS / Linux（或至少 Windows + Linux）验证浏览器自动打开
9. 对照 `checklist.md` 逐项验证并记录结果

**参考资料**：
- `checklist.md` 全部验收项

---

## Task 10: step1.1 体验小优化（5 项）

**状态**：已完成

**目标**：在端到端验证后收集到的 5 个体验细节一次性打磨，让 Web 交互更接近 Claude Code 的体感。

**影响文件**：
- `src/main.go` — 启动时 `os.Getwd()` 获取 workdir 并注入 Handler
- `src/internal/interaction/web/handler.go` — 新增 `workdir` 字段、`handleClearSession`、`Register` 注册 `clear_session`
- `src/internal/interaction/web/protocol.go` — 新增 `MsgTypeClearSession` 常量；`SessionLoadedPayload` 增加 `Workdir` 字段
- `src/internal/interaction/web/handler_test.go` — 所有 `NewHandler` 调用补充 `workdir` 参数
- `src/internal/interaction/web/static/index.html` — 顶部 LOGO 栏调整
- `src/internal/interaction/web/static/app.js` — slash 命令支持 exec、thinking 占位、消息头像
- `src/internal/interaction/web/static/style.css` — sidebar 背景色、消息布局、头像、thinking 动画

**依赖**：Task 1 ~ Task 9

**具体内容**：

1. **顶部 LOGO 栏微调**
   - 版本号 `v0.1.0` → `v1.0.1`
   - github 链接前加前缀 `开源地址:`（新增 `.topbar-meta-label` 灰底标签样式）
   - workspace-path 前加前缀 `当前工作路径:`
   - 修复工作路径显示：之前是硬编码 `~/CodePilot`，改为后端在启动时 `os.Getwd()` 拿到真实工作目录，通过 `SessionLoadedPayload.Workdir` 字段透传至前端
   - `MainHeaderTitle` 顶部对应 JS 同步：onSessionLoaded 拿到 workdir 后写入 `#workspace-path`

2. **会话栏背景色增加区分度**
   - `.sidebar` 背景从 `--bg` 调整为 `--bg-elevated`，与主区 `--bg` 形成明显分层
   - 同步给 `.sidebar-header` 加 `background: var(--bg-elevated)` 保证 header 区域不被父级穿透

3. **消息头像（类似微信）**
   - `.message` 容器从 `flex-direction: column` 改为 `flex-direction: row` + `align-items: flex-start`
   - `.message-user` 加 `flex-direction: row-reverse`（头像在气泡右侧），`.message-assistant` 默认头像在气泡左侧
   - Agent 头像：金色圆形 + "CP" 文字（与 LOGO 一致）
   - User 头像：蓝色圆形（`#5A8DEE`）+ "U" 文字
   - 头像直径 28px，气泡 `min-height: 28px` 避免首条流式时高度抖动

4. **Thinking 动态效果**
   - 3 个圆点 + "Thinking…" 文字的 `.thinking-indicator`
   - `@keyframes thinkingBounce`：30% 时 translateY(-4px) + opacity 1，其它 0.35
   - 第二个点 `animation-delay: 0.15s`、第三个 `0.30s`，形成波浪感
   - 触发：用户点击 Send 立即 `showThinking()`（state.expectingAssistant = true）
   - 移除时机：
     - 首个 `stream_chunk` 到达时（onStreamChunk 中检查 expectingAssistant）
     - `stream_done` / `stream_error` 兜底移除
     - 后端 `status_update(thinking)` 在 expectingAssistant 为 true 时也兜底补 showThinking

5. **/clear 快捷命令**
   - `SLASH_COMMANDS` 数组增加 `/clear`，带 `exec: () => sendWS(MsgType.ClearSession, {})`
   - `openSlashDropdown` 中点击回调改为传 entry 对象（不再传 cmd 字符串）
   - `applySlashCompletion` 兼容 entry 对象：若 `entry.exec` 是函数则执行（适用于 /clear），否则走原补全逻辑
   - keydown 中 Enter/Tab 选中时也按 entry 对象传给 applySlashCompletion
   - 空状态提示文案更新：`/new · /sessions · /resume · /clear`

**后端新增消息类型**：
- `MsgTypeClearSession = "clear_session"`：客户端 → 服务端
- `handleClearSession` 实现：保留当前 session_id，把 `current.Messages` 置空、`conv.Reset(nil)`、Save 落盘覆盖、推送 `session_loaded` 让前端同步刷新
- 与 `handleNewSession` 的差异：不清空 session_id，左侧历史不新增条目，仅清空当前会话的上下文

**验收点**：
- [x] 顶栏版本号展示 V1.0.1
- [x] 顶栏 github 链接前缀为 "开源地址:"
- [x] 顶栏工作路径前缀为 "当前工作路径:"，且为启动 CodePilot 时的真实工作目录
- [x] 左侧会话栏底色与主区有明显分层
- [x] 用户消息右对齐 + U 头像；Agent 消息左对齐 + CP 头像
- [x] 发送消息到首个 stream_chunk 之间显示 Thinking 动画
- [x] `/` 候选下拉中含 `/clear`
- [x] 点击 `/clear` 后主区消息清空、CURRENT SESSION 计数清零、左侧历史不新增条目
- [x] `go build ./...` 通过
- [x] `go test ./...` 全量通过
