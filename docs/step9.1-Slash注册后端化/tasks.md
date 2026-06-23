# Step 9.1 — Tasks

> 本步骤将 slash 命令的「定义位置与下发方式」改造为后端注册 + 启动下发，业务逻辑保持不变。

## Task 1: 新增 command/slash 包，定义 SlashCommand 接口与 Registry

**状态**：已完成

**目标**：建立 slash 命令的统一抽象层，独立于 web 包，为 Step 10 Skill 注册预留扩展点。

**影响文件**：
- `src/internal/command/slash/command.go` — 新建，定义接口与 Registry
- `src/internal/command/slash/doc.go` — 新建，包文档说明

**依赖**：无

**具体内容**：
1. 定义 `SlashCommand` 接口，方法集合：
   - `Name() string` — 命令名（含 `/`），如 `/new`
   - `Description() string` — 候选下拉中的描述
   - `NeedsArg() bool` — `true` 时选中后补全到输入框（不直接执行）
   - `ArgHint() string` — 参数提示，如 `<id>`；`NeedsArg=false` 时返回空字符串
   - `Category() string` — 分类标识，如 `session` / `context` / `skill` / `client`
   - `Execute(ctx context.Context, conn *websocket.Conn, arg string) error` — 命令执行
2. 定义 `Registry` 结构体：`sync.RWMutex` 保护的 `map[string]SlashCommand`，对外提供：
   - `Register(cmd SlashCommand) error` — 按 name 注册，重复注册返回错误
   - `Get(name string) (SlashCommand, bool)`
   - `List() []SlashCommand` — 返回当前所有命令（按注册顺序）
   - `OnChange(fn func())` — 注册变化回调（用于 WS 推送 slash_commands_updated），本步骤先实现回调机制但不触发
3. 包级导出：`NewRegistry() *Registry`
4. `doc.go` 写包级注释说明本包定位（slash 命令抽象层，与 `tool.Tool` 同构，专为 Step 10 Skill 注册命令准备）

**参考资料**：
- `src/internal/tool/builtin/read_file.go` 工具接口风格参考
- `src/internal/interaction/web/protocol.go` 现有 `Message` / MsgType 常量定义风格
- gorilla/websocket 当前 API：`Conn.WriteJSON(...)`

---

## Task 2: 把 6 条命令迁移到 builtin

**状态**：已完成

**目标**：把 Step 9 硬编码的 6 条命令按 SlashCommand 接口实现，handler.go 的 handleXxx 函数体**保持不动**，仅通过 Execute 委托调用。

**影响文件**：
- `src/internal/command/slash/builtin.go` — 新建，6 个内置命令实现

**依赖**：Task 1（接口已定义）

**具体内容**：
1. 每个命令以 struct 形式实现：
   - 持有 `*web.Handler` 引用（用于委托调用既有 handleXxx）或持有必要的依赖子集（provider / sessionManager / compactor / conv / dumpExecutor）
   - 重写 6 个接口方法
   - `Execute` 方法内**直接复用**现有 handleXxx 函数体（保持业务逻辑 0 改动）
2. 6 个命令：
   - `/new` — `Category="session"`，`NeedsArg=false`，委托 `handler.handleNewSession`
   - `/sessions` — `Category="client"`，`NeedsArg=false`，`Execute` 返回 nil（前端识别 Category 走本地逻辑）
   - `/resume` — `Category="session"`，`NeedsArg=true`，`ArgHint="<id>"`，委托 `handler.handleResumeSession`（参数从 arg 字段取）
   - `/clear` — `Category="session"`，`NeedsArg=false`，委托 `handler.handleClearSession`
   - `/compact` — `Category="context"`，`NeedsArg=false`，委托 `handler.handleCompact`
   - `/dump` — `Category="debug"`，`NeedsArg=false`，委托 `handler.handleDump`
3. 暴露 `RegisterBuiltin(r *Registry, h *web.Handler) error` 一站式注册 6 条命令

**参考资料**：
- `src/internal/interaction/web/handler.go` 现有 `handleNewSession` / `handleClearSession` / `handleResumeSession` / `handleCompact` / `handleDump` / `handleListSessions` 函数签名
- 注意 handler 中部分函数依赖 `*websocket.Conn`，Execute 方法签名需要保持兼容

---

## Task 3: protocol.go 增加 slash 命令下发相关的 3 个 MsgType

**状态**：已完成

**目标**：扩展 WebSocket 协议，新增命令清单请求 / 响应 / 更新事件 3 个消息类型。

**影响文件**：
- `src/internal/interaction/web/protocol.go` — 修改，新增 MsgType 常量

**依赖**：无（纯协议层扩展）

**具体内容**：
1. 在 `MsgType` 常量集合中新增：
   - `ListSlashCommands = "list_slash_commands"` — 前端请求命令清单
   - `SlashCommands = "slash_commands"` — 后端响应命令清单
   - `SlashCommandsUpdated = "slash_commands_updated"` — 后端主动推送命令变化
2. 定义命令清单 payload 结构体 `SlashCommandInfo`（在前端协议命名 `SlashCommandDTO`，后端结构体定义在 handler 包内即可）：
   - `name` string
   - `description` string
   - `needs_arg` bool
   - `arg_hint` string
   - `category` string
3. 响应 payload：`{ commands: SlashCommandInfo[] }`
4. 注释说明：本步骤不引入新发送消息类型，前端执行命令仍沿用现有 `new_session` / `clear_session` / `compact` / `dump` 等 MsgType

**参考资料**：
- `src/internal/interaction/web/protocol.go` 既有 MsgType 常量（如 `ListSessions` / `SessionList` 的命名风格）
- gorilla/websocket JSON 编解码

---

## Task 4: Handler 注入 Registry + WS Open 推送 + 处理 list_slash_commands 请求

**状态**：已完成

**目标**：让 handler 持有 slash Registry，在 WS 建立连接时主动推送当前命令清单；处理前端的 list_slash_commands 请求；预留 slash_commands_updated 推送通道。

**影响文件**：
- `src/internal/interaction/web/handler.go` — 修改，新增字段、注入方法、推送逻辑
- `src/internal/interaction/web/handler_test.go` — 修改或新增，覆盖推送与请求逻辑

**依赖**：Task 2（6 个内置命令已实现）+ Task 3（MsgType 已定义）

**具体内容**：
1. `Handler` 结构体新增字段：
   - `slashRegistry *slash.Registry`
   - `slashCmdMap map[string]string` — `command name -> existing MsgType`（用于前端执行时按 name 找发送类型）
2. `SetSlashRegistry(r *slash.Registry)` setter 注入。
3. `onWSOpen` 时（参考现有 `connectWS` 后端入口）：
   - 调 `r.List()` 拿到当前所有命令
   - 转 `SlashCommandInfo` 数组
   - 推送 `slash_commands` 消息给当前 conn
4. 处理 `list_slash_commands` 消息：等价于 onWSOpen 的推送逻辑（前端可在重连时再次拉取）。
5. 预留 `Registry.OnChange` 回调：注册时记录所有 conn，变化时遍历推 `slash_commands_updated`（本步骤只注册回调不主动调用，因为没有动态注册场景；Step 10 接入 Skill 后会被实际触发）。
6. `slashCmdMap` 在注入 Registry 后一次性建好：`{"/new": "new_session", "/clear": "clear_session", "/compact": "compact", "/dump": "dump"}`（`/resume` 因需要参数特殊处理，前端拿到 needsArg=true 时不再查 map）。
7. 业务 handler **不动**：`handleNewSession` 等函数体保持现有逻辑，本次只在 WS 入口做命令分发（list_slash_commands 走 list 推送，其他消息按 MsgType 走现有 router）。

**参考资料**：
- `src/internal/interaction/web/handler.go` 现有 `connectWS` / `handleServerMessage` / `router.Register` 逻辑
- `src/internal/interaction/web/handler.go` `SetMCPPool` setter 风格参考

---

## Task 5: 前端 app.js 移除 SLASH_COMMANDS 硬编码 + 接收下发

**状态**：已完成

**目标**：前端删除硬编码命令数组，改为接收后端下发的命令清单；下拉候选渲染与命令发送逻辑改造为按 name 路由。

**影响文件**：
- `src/internal/interaction/web/static/app.js` — 修改，移除硬编码 + 接收 / 渲染 / 发送

**依赖**：Task 4（后端推送已实现）

**具体内容**：
1. 删除 `SLASH_COMMANDS` 数组定义（`app.js:97-109` 整段）。
2. 新增 `state.commands = []` 存放后端下发的命令；`state.commandTypeByName = {}` 存放 `name -> MsgType` 映射。
3. 处理 `MsgType.SlashCommands` / `MsgType.SlashCommandsUpdated` 消息：
   - 更新 `state.commands`
   - 根据命令的 `category` 字段自动填 `state.commandTypeByName`：内置命令映射已知 4 条（new / clear / compact / dump），`/resume` 因 needsArg=true 由前端特殊处理；其他内置命令走 `MsgType.ListSessions` 等已有协议
   - 触发候选下拉重渲染
4. 候选下拉渲染逻辑（`renderSlashCandidates`）改为遍历 `state.commands`：
   - 仍按 name 字符串前缀过滤
   - 显示 description
   - needsArg=true 时不再有 exec 字段，选中后补全到输入框（用户继续填参数后按 Enter 提交）
   - needsArg=false 时选中直接按 `commandTypeByName[name]` 发送对应 MsgType
5. `/sessions` 命令（category=client）选中时走本地 `openSessionsTable()`，不发送 WS 消息。
6. `/resume` 命令（needsArg=true）选中后补全 `/resume ` 前缀到输入框，用户填 ID 按 Enter 后按 `MsgType.ResumeSession` 发送（payload 含 id）。
7. `onWSOpen` 时**不再主动**发 `list_slash_commands`，由后端 onWSOpen 主动推送即可；前端只在重连后等待推送（也可保留主动拉取作为兜底）。

**参考资料**：
- `src/internal/interaction/web/static/app.js:97-109` 现有 SLASH_COMMANDS 硬编码
- `src/internal/interaction/web/static/app.js` 现有 `MsgType` 常量定义
- 后端 `slashCmdMap` 设计：`/new -> new_session`, `/clear -> clear_session`, `/compact -> compact`, `/dump -> dump`

---

## Task 6: main.go 顶层装配 slash.Registry + 启动冒烟

**状态**：已完成

**目标**：在 main.go 顶层创建 slash Registry，注册 6 条内置命令，注入到 handler，确保进程启动即可用。

**影响文件**：
- `src/main.go` — 修改，新增 slash 装配代码

**依赖**：Task 2（builtin 已实现）+ Task 4（handler setter 已就绪）

**具体内容**：
1. 在 main.go 现有 handler 创建代码后，插入：
   - `slashRegistry := slash.NewRegistry()`
   - `slash.RegisterBuiltin(slashRegistry, h)` 注入全部 6 条命令
   - `h.SetSlashRegistry(slashRegistry)`
2. 在 `h.SetMCPPool(...)` 附近类似位置，保持装配顺序统一（handler setter 都集中在 h 创建后）。
3. 不引入新 logger / 配置项（slash 包不需要配置）。
4. 启动冒烟：
   - `go build` 通过
   - `go run ./src` 启动后监听 8969 端口
   - 浏览器 ws 连接后立刻收到 `slash_commands` 消息
   - 6 条命令全部能正常执行（手动测一遍 /new /clear /compact /dump /resume /sessions）

**参考资料**：
- `src/main.go` 现有 handler setter 调用风格（参考 `h.SetMCPPool(mcpPool)` 等）
- `src/internal/command/slash/builtin.go` `RegisterBuiltin` 函数签名

---

## Task 7: 端到端验证

**状态**：已完成

**目标**：按 checklist.md 逐项验证全部能力清单，确保 Step 1~Step 9 行为零回归。

**影响文件**：
- 无新增文件；可新建 `src/internal/command/slash/e2e_test.go` 跑跨包端到端

**依赖**：Task 1~6 全部完成

**具体内容**：
1. 单元测试：
   - `command/slash` 包内接口测试：每个 builtin 命令的 Name / Description / NeedsArg / ArgHint / Category 返回值正确
   - Registry 重复注册返回 error
   - Registry.OnChange 回调注册后能被 Notify 触发（本步骤可只测机制不测实际触发）
2. 集成测试：
   - 真实 HTTP Server + WS 连接：onWSOpen 收到 `slash_commands` 消息，含 6 条命令
   - 重新发 `list_slash_commands` 也能拿到完整清单
3. WebUI 端到端：
   - 输入 `/` 弹出候选下拉，含 6 条命令
   - 输入 `/co` 前缀过滤剩 `/compact` 一条
   - 选中 `/new` 触发新会话
   - 选中 `/clear` 清空上下文
   - 选中 `/compact` 手动压缩
   - 选中 `/dump` 导出 dump
   - 选中 `/sessions` 打开表格视图（不走 WS）
   - 选中 `/resume` 补全到输入框，填 ID 后提交恢复会话
4. 回归测试：
   - `go test ./...` 全部通过
   - Step 7 上下文压缩 e2e、Step 8 记忆系统 e2e、Step 5 权限系统 e2e 等历史用例零回归
5. 浏览器冒烟（人工）：
   - 启动 codepilot 进程，打开浏览器
   - 确认 ws 连上后立刻看到候选下拉可用（说明下发生效）
   - 6 条命令逐条手动测一遍
   - 重连 ws 后再次下发清单（前端 state.commands 被覆盖）

**参考资料**：
- checklist.md 全部验证项
- `src/internal/command/slash/handler_test.go`（如 Task 4 中新增）
- 现有 Step 5-8 e2e 用例模式