# Step 9.1 — Slash 命令注册后端化

## 背景

Step 9 把 `/new` / `/sessions` / `/resume` / `/clear` / `/compact` / `/dump` 这 6 条 slash 命令以**硬编码数组**的形式放在前端 [app.js](src/internal/interaction/web/static/app.js#L97-L109)。每新增一条命令都需要同步改前后端两处：前端补 SLASH_COMMANDS 条目，后端补 handleXxx + router.Register，存在易遗漏的双写风险。

更关键的是：**即将开始的 Step 10 Skill 系统要求 Skill 能够自动注册为 slash 命令**（按用户需求约定）。当前硬编码架构无法满足"插件式命令注册"诉求。

本次重构目标明确：
1. 把 slash 命令的**定义位置与下发方式**改造为后端注册 + 启动下发
2. 命令的**实际业务逻辑保持不变**——handleNewSession / handleClearSession / handleCompact / handleDump 等函数实现一字不动
3. 留好扩展点，Step 10 Skill 包只需 `import "command/slash"` + 实现 SlashCommand 接口即可零成本接入

## 目标用户

1. WebUI 终端用户——使用体验**不变**，仍输入 `/` 弹出候选列表
2. Step 10 Skill 系统开发者——按 SlashCommand 接口实现一行 Register 即可获得 slash 入口
3. Step 11 / Step 12 开发者——若 Hook / SubAgent 需要挂载命令，按同一规范注册即可

## 能力清单

1. 后端维护一个 `slash.Registry`，所有命令的元数据集中管理，单一事实来源。
2. 后端定义 `SlashCommand` 接口，字段：`Name` / `Description` / `NeedsArg` / `ArgHint` / `Category` / `Execute`，与 `tool.Tool` 风格一致。
3. 6 条现有命令全部迁移到注册表实现类，前端 `SLASH_COMMANDS` 数组**彻底删除**。
4. WebSocket 连接建立后，后端主动推送当前可用的 slash 命令清单（`slash_commands` 消息）。
5. 命令注册表发生变化时，后端支持推送 `slash_commands_updated` 事件（接口预留，本步骤只做空触发，动态注册留到 Step 10）。
6. `/sessions` 这种纯前端命令通过 `Category="client"` 标记与后端命令区分；前端识别后走本地逻辑（如 `openSessionsTable`）。
7. `/resume` 这种补全型命令通过 `NeedsArg=true` + `ArgHint="<id>"` 标记；前端选中后补全到输入框，用户填完参数后按 Enter 提交，提交时按对应 `commandType` 发送。
8. 保留 Step 9 全部已有行为：busy 保护、流式互斥、错误处理、HTTP 协议兼容。
9. SlashCommand 接口定义在独立 `src/internal/command/slash/` 包，避免依赖 web 层；Step 10 Skill 系统零成本接入。

## 非功能要求

1. **ws 协议零破坏**：现有 6 条命令的发送消息类型（`new_session` / `clear_session` / `compact` / `dump` 等）保持不变；新消息类型仅作增量。
2. **后端 handler 零破坏**：`handleNewSession` / `handleClearSession` / `handleCompact` / `handleDump` / `handleListSessions` / `handleResumeSession` 函数签名与实现不变。
3. **Step 1~Step 9 行为零回归**：ws 流程、busy 保护、权限系统、上下文压缩、记忆系统、MCP 健康状态等行为完全保留。
4. **包边界清晰**：`command/slash` 包**只依赖** `gorilla/websocket` 的 `*websocket.Conn` 类型 + context.Context + stdlib，不 import web 包；web 包依赖 command/slash 包。
5. **架构解耦**：Step 10 Skill 系统无需 import web 包即可注册 slash 命令。

## 设计骨架

```text
                ┌────────────────────────────────────┐
                │ src/internal/command/slash         │
                │ - SlashCommand 接口（6 字段）       │
                │ - Registry 注册表                  │
                │ - 6 个内置命令 builtin             │
                └────────────────┬───────────────────┘
                                 │ main.go 装配 + 注入 Handler
                                 ▼
                ┌────────────────────────────────────┐
                │ src/internal/interaction/web        │
                │ - protocol.go 新增 MsgType          │
                │ - handler.go 注入 Registry          │
                │   · onWSPushCommands (open 时推)    │
                │   · onSlashCommandRequest (响应)    │
                │   · registry.Notify → 推 updated    │
                └────────────────┬───────────────────┘
                                 │ WS push
                                 ▼
                ┌────────────────────────────────────┐
                │ WebUI app.js                        │
                │ - 移除 SLASH_COMMANDS 硬编码数组     │
                │ - state.commands 接收 slash_commands │
                │ - renderSlashCandidates 渲染下拉     │
                │ - sendSlashCommand 按 commandType   │
                └────────────────────────────────────┘
```

关键落点：
1. 新增包 `src/internal/command/slash/command.go`（接口 + Registry）
2. 新增 `src/internal/command/slash/builtin.go`（6 条内置命令）
3. 新增 `src/internal/command/slash/command_test.go`（单测）
4. 修改 `src/internal/interaction/web/protocol.go` 增加 3 个 MsgType
5. 修改 `src/internal/interaction/web/handler.go` 注入 Registry + 处理下发
6. 修改 `src/internal/interaction/web/static/app.js` 移除硬编码 + 接收下发
7. 修改 `src/main.go` 顶层装配 slash.Registry
8. `src/internal/interaction/web/static/index.html` / `style.css` 仅在前端展示样式有调整时小范围修改

## Out of Scope（本步骤不做）

1. **不实现 `/help` 完整帮助页**——本步骤只下发改建后的命令清单，不做文档化帮助。
2. **不引入命令权限模型**——权限仍沿用对应业务操作的已有约束（如 /compact 复用 Compactor 权限）。
3. **不实现命令搜索 / 分类筛选 UI**——前端按命令原顺序展示即可。
4. **不做 SlashCommand 动态热加载**——`slash_commands_updated` 推送通道预留，本步骤只做静态触发；Skill 动态注册在 Step 10 落地时再实现。
5. **不改 Step 10 Skill 系统实现**——本步骤只准备好接口与扩展点，Skill 包的具体实现留给 Step 10。
6. **不改会话恢复 / 命令历史**——slash 命令不进入会话历史（与 Step 9 保持一致），本步骤不修改此约束。