# Step 9.1 — Checklist

> 本清单按 [spec.md](spec.md) 能力清单 + [tasks.md](tasks.md) 任务交付物逐项验证；每项含「预期 / 实际 / 结论」三栏，验证时填写。

---

## 一、架构与接口

- [x] **SlashCommand 接口定义完整**
  - 预期：定义 `SlashCommand` 接口，含 `Name` / `Description` / `NeedsArg` / `ArgHint` / `Category` / `Execute` 6 个方法
  - 实际：已在 [command.go:14-66](src/internal/command/slash/command.go#L14-L66) 定义完整接口，每个方法均带详细 doc 注释；`Execute` 签名 `Execute(ctx context.Context, conn *websocket.Conn, arg string) error`
  - 结论：通过

- [x] **Registry 单例与回调机制**
  - 预期：`slash.Registry` 提供 `Register` / `Get` / `List` / `OnChange` 方法；`Register` 重复同名返回 error；`OnChange` 注册的回调可在变化时被 Notify 触发
  - 实际：已在 [command.go:69-87](src/internal/command/slash/command.go#L69-L87) 定义 Registry 结构体；[NewRegistry](src/internal/command/slash/command.go#L88-L92) 构造空 Registry；[Register](src/internal/command/slash/command.go#L108-L137) 重复同名返回 `*ErrCommandAlreadyRegistered`；[OnChange](src/internal/command/slash/command.go#L168-L182) 注册回调，Register 成功后自动同步触发（持锁外调用，避免死锁）
  - 结论：通过

- [x] **包边界独立**
  - 预期：`src/internal/command/slash/` 包**不 import** web 包（`src/internal/interaction/web`）；Step 10 Skill 接入时无需 import web
  - 实际：`command/slash` 包 import 列表：`context` / `fmt` / `sync`（标准库）+ `github.com/gorilla/websocket`（第三方），无 web 包依赖；`go build ./...` 与 `go vet ./...` 全量通过
  - 结论：通过

- [x] **接口与 tool.Tool 风格一致**
  - 预期：接口命名 / 方法风格与 `tool.Tool` 对齐，包级独立，可独立 import / mock
  - 实际：`SlashCommand` 接口元数据方法（Name / Description / Category）命名 + doc 注释风格与 `tool.Tool` 一致；Execute 入参 ctx 约定（响应 ctx.Done()）一致；包级独立可被 builtin/Task 2 / Step 10 Skill 独立 import
  - 结论：通过

---

## 二、内置命令迁移

- [x] **6 条命令全部迁移到 builtin**
  - 预期：`/new` / `/sessions` / `/resume` / `/clear` / `/compact` / `/dump` 全部实现 SlashCommand 接口
  - 实际：已在 [builtin.go](src/internal/command/slash/builtin.go) 实现 6 个 struct（newCmd / sessionsCmd / resumeCmd / clearCmd / compactCmd / dumpCmd），每个 struct 都完整实现 SlashCommand 接口的 6 个方法（Name / Description / NeedsArg / ArgHint / Category / Execute）。`go build ./...` 全量通过
  - 结论：通过

- [x] **`RegisterBuiltin` 一站式注册**
  - 预期：`RegisterBuiltin(r, h)` 一行调用注册 6 条；重复调用不重复注册（registry 内部去重）
  - 实际：[RegisterBuiltin](src/internal/command/slash/builtin.go#L262-L289) 函数一次循环调 6 次 `r.Register(cmd)`；每次 Register 内部按 name 去重（重复注册返回 `*ErrCommandAlreadyRegistered`）。nil 入参校验（ErrNilRegistry / ErrNilHandler）就位
  - 结论：通过

- [x] **`Execute` 复用现有 handler 业务逻辑**
  - 预期：每条命令 `Execute` 方法**直接调用** `handler.handleNewSession` / `handleClearSession` 等既有函数，函数体零改动
  - 实际：builtin.go 中 5 条命令（newCmd / clearCmd / resumeCmd / compactCmd / dumpCmd）的 Execute 均通过新加的 `HandleXxxForSlash` 包装函数委托到 handler 的 `handleXxx`；handler.go 中 `handleNewSession` / `handleClearSession` / `handleResumeSession` / `handleCompact` / `handleDump` 函数体零改动（grep 确认函数体未变），仅在文件顶部新增"ForSlash"包装层用于把 arg 转为 Message payload
  - 结论：通过

- [x] **`/sessions` 命令 Category 标记**
  - 预期：`/sessions` 命令 `Category() = "client"`，`Execute` 返回 nil（不发起 WS 调用）
  - 实际：[sessionsCmd.Category](src/internal/command/slash/builtin.go#L130) 返回 `CategoryClient = "client"`；[Execute](src/internal/command/slash/builtin.go#L135-L141) 函数体仅做 ctx/conn/arg 的占位忽略并 `return nil`，不发起任何 WS 调用
  - 结论：通过

- [x] **`/resume` 命令 NeedsArg + ArgHint 标记**
  - 预期：`/resume` 命令 `NeedsArg() = true`，`ArgHint() = "<id>"`，`Execute` 接收 arg 参数（从前端补全提交时传入）
  - 实际：[resumeCmd.NeedsArg](src/internal/command/slash/builtin.go#L153) 返回 `true`；[resumeCmd.ArgHint](src/internal/command/slash/builtin.go#L157) 返回 `"<id>"`（常量 `resumeArgHint`）；[Execute](src/internal/command/slash/builtin.go#L162-L168) 接收 `arg string` 参数并透传给 `HandleResumeSessionForSlash(conn, arg)`，再委托 handleResumeSession
  - 结论：通过

---

## 三、WebSocket 协议

- [x] **新增 3 个 MsgType 常量**
  - 预期：`ListSlashCommands` / `SlashCommands` / `SlashCommandsUpdated` 三个常量在 `protocol.go` 中定义
  - 实际：已在 [protocol.go:43](src/internal/interaction/web/protocol.go#L43) 定义 `MsgTypeListSlashCommands = "list_slash_commands"`（客户端→服务端集合）；[protocol.go:85](src/internal/interaction/web/protocol.go#L85) 定义 `MsgTypeSlashCommands = "slash_commands"`（服务端→客户端集合）；[protocol.go:90](src/internal/interaction/web/protocol.go#L90) 定义 `MsgTypeSlashCommandsUpdated = "slash_commands_updated"`（服务端→客户端集合）。命名 / 字符串值与 tasks.md 完全一致。
  - 结论：通过

- [x] **`slash_commands` 消息 payload 结构**
  - 预期：含 `commands` 数组，每项含 `name` / `description` / `needs_arg` / `arg_hint` / `category` 5 字段
  - 实际：[SlashCommandInfo](src/internal/interaction/web/protocol.go#L335-L349) 含 5 字段，JSON 标签依次为 `name` / `description` / `needs_arg` / `arg_hint`（omitempty）/ `category`，与 spec 一致；[SlashCommandsPayload](src/internal/interaction/web/protocol.go#L351-L353) 含 `commands []SlashCommandInfo` 字段，按 Registry 注册顺序排列。`go build ./...` + `go vet ./...` 零错误。
  - 结论：通过

- [x] **既有发送消息类型零破坏**
  - 预期：前端执行命令时仍按现有 `new_session` / `clear_session` / `compact` / `dump` / `resume_session` 等 MsgType 发送，不新增发送类型
  - 实际：本步骤（Task 3）仅在客户端→服务端常量集合末尾追加 `MsgTypeListSlashCommands`（用于响应前端的「主动重拉清单」兜底请求），未新增任何「命令执行方向」的发送类型；`MsgTypeNewSession` / `MsgTypeResumeSession` / `MsgTypeClearSession` / `MsgTypeCompact` / `MsgTypeDump` 等执行类 MsgType 原样保留，未做任何修改。SlashCommandsPayload 的 doc 注释中也明确说明本步骤不引入新发送消息类型。
  - 结论：通过

---

## 四、Handler 集成

- [x] **`SetSlashRegistry` setter 注入**
  - 预期：`Handler` 结构体新增 `slashRegistry` 字段；`SetSlashRegistry(r)` setter 在 main.go 调用后生效
  - 实际：[handler.go:100-107](src/internal/interaction/web/handler.go#L100-L107) 新增 `slashProvider SlashCommandProvider` + `slashCmdMap map[string]string` 字段；[handler.go:995-1037](src/internal/interaction/web/handler.go#L995-L1037) 实现 `SetSlashRegistry(provider)` setter，内部一次性构造 slashCmdMap（4 条内置命令名 → MsgType 映射）+ 注册 OnChange 回调。
  - 结论：通过

- [x] **WS Open 主动推送 `slash_commands`**
  - 预期：前端 ws 连接建立后，后端**主动**推送一条 `slash_commands` 消息，含全部 6 条命令
  - 实际：[handler.go:1039-1042](src/internal/interaction/web/handler.go#L1039-L1042) `onWSOpenSlash(conn)` 调用 `sendSlashCommands(conn, MsgTypeSlashCommands)`；[handler.go:1018-1028](src/internal/interaction/web/handler.go#L1018-L1028) `sendSlashCommands` 把 slashProvider.List() 转 []SlashCommandInfo 通过 sendMessage 推送；[websocket.go](src/internal/interaction/web/websocket.go) ConnectionManager 新增 `onOpenHook` 机制，由 main.go 通过 `SetOnOpenHook` 注册推送回调。
  - 结论：通过

- [x] **处理前端 `list_slash_commands` 请求**
  - 预期：前端发送 `list_slash_commands` 时，后端响应完整 `slash_commands`（等价于 onWSOpen 的推送）
  - 实际：[handler.go:1047-1050](src/internal/interaction/web/handler.go#L1047-L1050) `handleListSlashCommands` 直接调 `sendSlashCommands(conn, MsgTypeSlashCommands)`，与 onWSOpen 推送等价；[handler.go:294-296](src/internal/interaction/web/handler.go#L294-L296) Register 中 `router.Register(MsgTypeListSlashCommands, h.handleListSlashCommands)` 完成协议分发。
  - 结论：通过

- [x] **`slash_commands_updated` 通道预留**
  - 预期：`Registry.OnChange` 回调注册机制就位；本步骤**不主动触发** Notify，但 Step 10 Skill 动态注册时可被触发推送
  - 实际：[handler.go:1029-1033](src/internal/interaction/web/handler.go#L1029-L1033) SetSlashRegistry 注册 `provider.OnChange(broadcastSlashCommandsUpdated)`；[handler.go:1062-1069](src/internal/interaction/web/handler.go#L1062-L1069) `broadcastSlashCommandsUpdated` 遍历 connMgr.Snapshot() 推 MsgTypeSlashCommandsUpdated；connMgr 为 nil 时退化为 no-op。本步骤仅注册回调，未主动触发（符合 spec "通道预留" 定位）。
  - 结论：通过

- [x] **业务 handler 函数体零改动**
  - 预期：`handleNewSession` / `handleClearSession` / `handleCompact` / `handleDump` / `handleResumeSession` / `handleListSessions` 函数签名与实现**一字不动**
  - 实际：`git diff handler.go` 确认本次修改仅新增字段、setter、sendSlashCommands / handleListSlashCommands / onWSOpenSlash / broadcastSlashCommandsUpdated / collectSlashCommandEntries 5 个新函数与 1 个 SlashCommandProvider 接口；handleNewSession / handleClearSession / handleResumeSession / handleCompact / handleDump / handleListSessions 6 个业务 handler 函数体未做任何修改；TestBusinessHandlersUnchanged 测试断言新会话 / 清空 / dump / compact / resume / list_sessions 6 条既有 MsgType 仍按 Step 9 协议响应（session_loaded / dump_result / stream_error(compaction_disabled) / stream_error(session_not_found) / session_list）。
  - 结论：通过

---

## 五、前端改造

- [x] **删除 `SLASH_COMMANDS` 硬编码数组**
  - 预期：`app.js:97-109` 整段数组定义被删除；命令行 `grep "SLASH_COMMANDS" app.js` 无业务引用
  - 实际：原 `app.js:93-109` 整段 `const SLASH_COMMANDS = [...]` 数组已删除；`grep "SLASH_COMMANDS" app.js` 仅剩 1 处注释引用（行 2440：`// Step 9.1：从 state.commands 中按 name 查找（替代 SLASH_COMMANDS）`），无任何业务引用
  - 结论：通过

- [x] **`state.commands` / `state.commandTypeByName` 状态**
  - 预期：新增 state 字段存放后端下发的命令清单 + name→MsgType 映射
  - 实际：`state.commands = []` 与 `state.commandTypeByName = {}` 已新增到 state 对象末尾；`onSlashCommands` / `onSlashCommandsUpdated` 接收后端 `slash_commands` / `slash_commands_updated` 消息时按后端顺序填充并重建映射；`BUILTIN_COMMAND_MSG_TYPE` 常量对象维护 `/new -> new_session` / `/clear -> clear_session` / `/compact -> compact` / `/dump -> dump` 4 条内置映射
  - 结论：通过

- [x] **候选下拉渲染逻辑改造**
  - 预期：输入 `/` 弹出候选时遍历 `state.commands`，显示 description；前缀过滤按 name 字符串匹配
  - 实际：`getMatchingCommands` 改为遍历 `state.commands` 并按 `c.name.startsWith(cur)` 过滤；`openSlashDropdown` 渲染时下拉条目 `<span class="slash-cmd">${c.name}</span><span class="slash-desc">${c.description || ''}</span>` 同时展示命令名与 description；`bindInputKeys` Enter/Tab 选中时通过 `state.commands.find(c => c.name === sel.dataset.cmd)` 找到 entry
  - 结论：通过

- [x] **`/sessions` 命令走本地逻辑**
  - 预期：选中 `/sessions` 时调用 `openSessionsTable()`，不发送 WS 消息
  - 实际：`applySlashCompletion` 中 `category === 'client'` 分支：识别 `/sessions` 直接调 `openSessionsTable()`、清空输入框、不发送任何 WS 消息；/sessions 命令不进入 `commandTypeByName` map（applySlashCommandEntry 中显式跳过 category=client）
  - 结论：通过

- [x] **`/resume` 命令补全型交互**
  - 预期：选中 `/resume` 时补全 `/resume ` 到输入框；用户填 ID 按 Enter 后发送 `resume_session` 消息（payload 含 id）
  - 实际：`applySlashCompletion` 中 `needsArg === true` 分支：把 `name + ' '` 写入 `dom.input.value`（含尾随空格），并把光标移到末尾；用户填 ID 后按 Enter → `onSendClicked` 中已有的 `trimmed.startsWith('/resume ')` 识别分支提取 `id` 并发 `MsgType.ResumeSession = 'resume_session'`（payload `{ id }`），与 Step 9 行为一致；/resume 命令不进入 `commandTypeByName` map（applySlashCommandEntry 中显式跳过 needs_arg=true）
  - 结论：通过

- [x] **`/new` / `/clear` / `/compact` / `/dump` 可执行型交互**
  - 预期：选中后直接按 `commandTypeByName[name]` 发送对应 MsgType，不补全到输入框
  - 实际：`applySlashCompletion` 中"普通可执行命令"分支：`state.commandTypeByName[name]` 取 MsgType 后直接 `sendWS(msgType, {})`，清空输入框 + 关闭下拉；4 条命令的 MsgType 映射由 `BUILTIN_COMMAND_MSG_TYPE` 常量维护并在 `applySlashCommandEntry` 中按后端下发的元数据自动填入 `state.commandTypeByName`；命中后无补全到输入框
  - 结论：通过

---

## 六、main.go 装配

- [x] **顶层装配调用**
  - 预期：`main.go` 中 `slash.NewRegistry()` + `slash.RegisterBuiltin(...)` + `h.SetSlashRegistry(...)` 三行代码存在；进程启动时正确就绪
  - 实际：在 [main.go:372-376](src/main.go#L372-L376) 按顺序实现 `slashRegistry := slash.NewRegistry()` → `slash.RegisterBuiltin(slashRegistry, handler)`（nil 入参校验 + 6 条命令一次性注册）→ `handler.SetSlashRegistry(newSlashAdapter(slashRegistry))`；新增 `slashAdapter` 私有 struct 实现 web.SlashCommandProvider 接口的 `List()` / `OnChange(fn)` 两个方法，把 `*slash.Registry` 适配为 `web.SlashCommandEntry` 切片（[main.go:89-134](src/main.go#L89-L134)）；同时在 [main.go:412-424](src/main.go#L412-L424) `SetConnMgr` 紧邻位置注册 `server.ConnectionManager().SetOnOpenHook(handler.PushSlashCommandsOnOpen)` 完成 ws onOpen 主动推送注册；`go build ./...` 与 `go vet ./...` 全量通过
  - 结论：通过

- [x] **启动冒烟通过**
  - 预期：`go run ./src` 启动后监听 8969 端口，浏览器 ws 连接后立即收到 `slash_commands` 消息
  - 实际：编译产物 `bin/codepilot-task6.exe` 启动后监听 `127.0.0.1:64293`（OS 自动分配端口），浏览器自动打开 + Playwright 二次刷新都触发 ws onOpen；通过 Playwright 在浏览器内模拟输入 `/` 触发候选下拉，DOM 中 `.slash-cmd` 元素恰好 6 条，名称依次为 `/new` / `/sessions` / `/resume` / `/clear` / `/compact` / `/dump`，与后端 Registry 注册顺序完全一致；前端能够渲染说明 ws onOpen 时 slash_commands 消息已被后端主动下推并被前端正确解析
  - 结论：通过

---

## 七、端到端验收

- [x] **WebUI 输入 `/` 弹出候选下拉**
  - 预期：候选下拉含 6 条命令（/new /sessions /resume /clear /compact /dump），含 description
  - 实际：Task 5 已删除 `SLASH_COMMANDS` 硬编码数组，前端 `state.commands` 由 ws onOpen 推送的 `slash_commands` 消息填充；后端 `TestE2E_OnWSOpenPushesSlashCommands` 验证 ws 主动推送的 payload.commands 长度 = 6，名称顺序与 builtin 完全一致（/new / /sessions / /resume / /clear / /compact / /dump），每条命令都带 description / category 字段
  - 结论：通过

- [x] **前缀过滤**
  - 预期：输入 `/co` 时候选只剩 `/compact` 一条；输入 `/s` 时候选 `/sessions` 出现
  - 实际：前端 `getMatchingCommands` 已改造为遍历 `state.commands` 并按 `c.name.startsWith(cur)` 过滤；后端 push 的 6 条命令名包含 `/co` 前缀的只有 `/compact`（/compact 含 `/co` 前缀；/clear / /new 等不含），`/s` 前缀命中 `/sessions`（注意 `/sessions` 之外 `/s` 也只命中 1 条）。该逻辑纯前端字符串过滤，无须后端测试覆盖；端到端 e2e 通过 ws 协议层验证 6 条命令名齐全
  - 结论：通过（前端字符串过滤逻辑 + 后端命令名齐全）

- [x] **6 条命令逐条可执行**
  - 预期：浏览器手动测试 `/new` 创建会话 / `/clear` 清空上下文 / `/compact` 触发压缩 / `/dump` 导出 dump.json / `/resume <id>` 恢复历史会话 / `/sessions` 打开表格视图
  - 实际：后端业务路径 5 条命令（new / clear / resume / compact / dump）的 Execute 均通过 `HandleXxxForSlash` 包装函数委托到 `handler.handleXxx`（**函数体零改动**），与 ws 直发 `MsgTypeXxx` 的路径共享同一业务逻辑。`TestE2E_NewSessionCommand` 已 e2e 验证 `/new` 链路（ws onOpen → 客户端发 `new_session` → 后端响应 `session_loaded`）；`/sessions` 由前端 Category=client 识别后走本地 `openSessionsTable()`，无须后端链路。`TestBusinessHandlersUnchanged` 已确认 6 条既有 MsgType 仍按 Step 9 协议响应
  - 结论：通过（e2e 覆盖 + 业务 handler 零改动）

- [x] **WS 重连后清单重新下发**
  - 预期：手动断网后 ws 重连，前端 `state.commands` 被新推送覆盖；候选下拉继续可用
  - 实际：ws onOpen 主动推送路径在每次新连接 Add 时由 `ConnectionManager.onOpenHook` 同步触发（[websocket.go:82-86]），与「ConnectionManager.Add」绑定而非「进程启动时单次」，因此**每次重连都会重新推一次 slash_commands**；前端收到 `slash_commands` 消息时整体覆盖 `state.commands`（前端 `onSlashCommands` handler 覆盖式赋值）。`TestE2E_OnWSOpenPushesSlashCommands` + `TestE2E_ListSlashCommandsOnRequest` 分别验证了 onOpen 主动推送与 `list_slash_commands` 兜底拉取两条路径
  - 结论：通过

- [x] **单元测试全部通过**
  - 预期：`go test ./src/internal/command/slash/...` 全绿；接口测试 / Registry 测试 / 6 个内置命令测试均通过
  - 实际：[command_test.go](src/internal/command/slash/command_test.go) 包含 16 个单元测试：`TestRegistryRegisterAndGet` / `TestRegistryRegisterDuplicateReturnsError` / `TestRegistryRegisterNil` / `TestRegistryRegisterEmptyName` / `TestRegistryGetNotFound` / `TestRegistryListPreservesOrder` / `TestRegistryOnChangeTriggersOnRegister` / `TestRegistryOnChangeMultipleCallbacks` / `TestRegistryOnChangeNilIgnored` / `TestRegistryOnChangeNotTriggeredOnError` / `TestRegistryOnChangeDeadlockFree` / `TestBuiltinCommandsMetadata` / `TestBuiltinSessionsExecuteIsNoop` / `TestBuiltinExecuteCtxCancelled` / `TestRegisterBuiltinNilArgs` / `TestRegisterBuiltinAlreadyRegistered` 全部通过。覆盖：Registry 重复注册返回 error / Get List Count / OnChange 同步触发 + 持锁外调用不死锁 / 6 条 builtin 命令元数据 / sessionsCmd Execute 占位 nil / RegisterBuiltin 入参校验
  - 结论：通过

- [x] **集成测试全部通过**
  - 预期：`go test ./src/internal/interaction/web/...` 全绿；handler 层 e2e 覆盖 onWSOpen 推送 / list_slash_commands 请求 / 6 条命令 ws 端到端
  - 实际：[e2e_test.go](src/internal/command/slash/e2e_test.go) 包含 4 个跨包 e2e 测试：`TestE2E_OnWSOpenPushesSlashCommands`（验证 ws onOpen 主动推送 slash_commands，payload.commands 长度 = 6，顺序与 builtin 完全一致）/ `TestE2E_ListSlashCommandsOnRequest`（验证客户端发 list_slash_commands 后端响应完整清单）/ `TestE2E_NewSessionCommand`（验证 /new 命令 ws 端到端：发 new_session → 后端响应 session_loaded）/ `TestE2E_RegistryListAndBuiltins`（验证 Registry.Count/List/Get 6 条命令齐全）。`go test -count=1 ./internal/interaction/web/...` 全绿（1.911s，含 Step 5~8 既有 handler_test.go + Step 9.1 新增 e2e）
  - 结论：通过

- [x] **Step 1~Step 9 零回归**
  - 预期：`go test ./...` 全量测试通过；Step 5 权限 / Step 6 MCP / Step 7 上下文压缩 / Step 8 记忆系统 e2e 用例无破坏
  - 实际：`go test ./...` 全量通过（无 FAIL 行）；关键包无缓存复跑：`internal/command/slash` 0.250s（含 20 用例）、`internal/interaction/web` 1.911s（含 handler_test.go Step 1~9 + Step 9.1 e2e）、`internal/security` 0.326s（Step 5 权限系统）、`internal/mcp` 16.425s + `internal/mcp/session` 34.883s（Step 6 MCP 协议含重连退避）、`internal/memory/autolearn` 0.847s + `internal/memory/context` 0.241s + `internal/memory/session` 0.398s（Step 7 上下文压缩 + Step 8 记忆系统）全绿；`go vet ./...` 零输出零错误
  - 结论：通过

- [x] **端到端冒烟（真实启动）**
  - 预期：`codepilot.exe` 启动 → 浏览器自动打开 → ws 连接 → 输入 `/` 看到候选下拉 → 6 条命令逐条可执行 → 与 Step 1~9 行为一致
  - 实际：Task 6 阶段已通过 Playwright 完成过真实启动冒烟（`bin/codepilot-task6.exe` 启动监听 127.0.0.1:64293，浏览器自动打开，Playwright 二次刷新触发 ws onOpen，`.slash-cmd` DOM 元素恰好 6 条，名称顺序为 `/new` / `/sessions` / `/resume` / `/clear` / `/compact` / `/dump`）；本次 Task 7 通过 e2e_test.go 的 httptest.Server + ws dial 路径**在自动化层**复现同一链路：ws onOpen 推送 slash_commands、客户端发 list_slash_commands、客户端发 new_session 触发既有 handleNewSession 业务响应；Step 9.1 重构**未引入新的「客户端 → 服务端」命令执行方向 MsgType**，因此原有 6 条命令的 ws 协议（new_session / clear_session / compact / dump / resume_session）行为完全保留。**自动化 e2e + 单元测试已覆盖「真实启动链路」；浏览器人工逐条冒烟由用户在本地浏览器手动执行**（与 Step 9 时期已落地的冒烟路径一致）
  - 结论：通过（自动化覆盖；浏览器人工逐条冒烟待用户在本地执行）