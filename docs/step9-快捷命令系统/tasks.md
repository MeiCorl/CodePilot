# Step 9 - 快捷命令系统 Tasks

> 本步骤为回顾式补记。快捷命令能力已在 Step 1.1、Step 7 以及后续调试能力迭代中分散落地；本文件用于把原计划 Step 9 标记为已完成，并明确后续不在当前阶段追加重构。

## Task 1: 盘点已落地命令入口

**状态**：已完成

**目标**：确认当前 WebUI 中已经存在的快捷命令清单与触发路径。

**影响文件**：
- `src/internal/interaction/web/static/app.js` - 已实现 `/` 下拉候选与命令执行映射
- `src/internal/interaction/web/static/style.css` - 已实现快捷命令下拉样式
- `src/internal/interaction/web/static/index.html` - 已展示快捷命令提示文案

**依赖任务**：无

**具体内容**：
1. 盘点 `SLASH_COMMANDS` 中的命令项。
2. 确认带 `exec` 的命令可直接发送 WebSocket 消息。
3. 确认 `/resume <id>` 这类带参数命令由发送逻辑识别后转为恢复会话消息。
4. 确认可通过输入 `/`、上下键、Enter、Tab、鼠标点击进行候选选择。

**参考资料**：
- `src/internal/interaction/web/static/app.js` 中 `SLASH_COMMANDS`
- `src/internal/interaction/web/static/app.js` 中 `onSendClicked`
- `src/internal/interaction/web/static/app.js` 中 `openSlashDropdown` / `applySlashCompletion`

## Task 2: 盘点后端命令协议与路由

**状态**：已完成

**目标**：确认快捷命令没有直接进入 LLM，而是被转换为明确的 WebSocket 业务消息。

**影响文件**：
- `src/internal/interaction/web/protocol.go` - 已定义命令相关消息类型与 payload
- `src/internal/interaction/web/handler.go` - 已注册命令消息处理器

**依赖任务**：Task 1

**具体内容**：
1. 确认会话类命令消息：`list_sessions`、`new_session`、`resume_session`、`clear_session`。
2. 确认上下文命令消息：`compact`。
3. 确认导出命令消息：`dump`。
4. 确认 handler router 将消息分发到独立处理器。
5. 确认命令处理失败时返回 `stream_error` 或专用结果消息。

**参考资料**：
- `src/internal/interaction/web/protocol.go` 中消息常量
- `src/internal/interaction/web/handler.go` 中 `Register`

## Task 3: 会话类快捷命令归档

**状态**：已完成

**目标**：确认 `/new`、`/sessions`、`/resume <id>`、`/clear` 已覆盖当前会话管理需求。

**影响文件**：
- `src/internal/interaction/web/handler.go` - 已实现会话类命令处理
- `src/internal/memory/session/` - 已提供会话持久化与摘要能力
- `src/internal/interaction/web/static/app.js` - 已提供前端触发入口

**依赖任务**：Task 2

**具体内容**：
1. `/new` 保存当前会话并创建空会话。
2. `/sessions` 拉取历史会话并以表格视图展示。
3. `/resume <id>` 支持会话 ID 前缀匹配，处理无匹配和多匹配错误。
4. `/clear` 保留当前 session_id，清空消息、刷新上下文用量，并清理工具结果归档。

**参考资料**：
- `src/internal/interaction/web/handler.go` 中 `handleNewSession`
- `src/internal/interaction/web/handler.go` 中 `handleListSessions`
- `src/internal/interaction/web/handler.go` 中 `handleResumeSession`
- `src/internal/interaction/web/handler.go` 中 `handleClearSession`

## Task 4: 上下文与调试类快捷命令归档

**状态**：已完成

**目标**：确认 `/compact` 与 `/dump` 已作为快捷命令接入主流程，满足当前上下文管理与调试快照需求。

**影响文件**：
- `src/internal/interaction/web/handler.go` - 已实现 `/compact` 与 `/dump` 对应处理
- `src/internal/interaction/web/dump.go` - 已实现会话快照导出
- `src/internal/memory/context/` - 已提供上下文压缩能力
- `src/internal/interaction/web/static/app.js` - 已提供前端触发入口与结果提示

**依赖任务**：Task 2

**具体内容**：
1. `/compact` 触发手动上下文压缩，推送 `compaction_event` 并刷新上下文用量。
2. `/compact` 与普通输入共享 busy 保护，避免并发改写历史。
3. `/dump` 导出当前会话上下文与 System Prompt 快照为本地文件。
4. `/dump` 成功后推送 `dump_result`，失败时推送明确错误。

**参考资料**：
- `src/internal/interaction/web/handler.go` 中 `handleCompact`
- `src/internal/interaction/web/handler.go` 中 `handleDump`
- `src/internal/interaction/web/dump.go`
- `src/internal/interaction/web/handler_compact_test.go`
- `src/internal/interaction/web/dump_test.go`

## Task 5: 接入主流程

**状态**：已完成

**目标**：确认快捷命令入口已接入 WebUI 主流程，且不会破坏普通用户输入、流式响应和状态栏可观测性。

**影响文件**：
- `src/internal/interaction/web/static/app.js` - 已在输入发送、下拉候选、状态提示中接入命令
- `src/internal/interaction/web/handler.go` - 已在 WebSocket 主路由中接入命令处理器

**依赖任务**：Task 1、Task 2、Task 3、Task 4

**具体内容**：
1. 前端根据命令类型选择直接执行或补全输入。
2. 普通文本输入仍按 `user_input` 进入 Agent Loop。
3. 会话切换、清空、压缩、导出等命令执行后刷新对应 UI 状态。
4. 流式进行中阻止命令与输入造成并发冲突。

**参考资料**：
- `src/internal/interaction/web/static/app.js` 中输入绑定与 WebSocket 消息发送逻辑
- `src/internal/interaction/web/handler.go` 中 `streamState` 互斥控制

## Task 6: 端到端验证

**状态**：已完成

**目标**：确认快捷命令相关能力已有自动化验证或历史 checklist 验证记录支撑。

**影响文件**：
- `src/internal/interaction/web/handler_test.go` - 已覆盖会话类命令消息
- `src/internal/interaction/web/handler_compact_test.go` - 已覆盖 `/compact` 链路
- `src/internal/interaction/web/dump_test.go` - 已覆盖 `/dump` 导出核心逻辑
- `docs/step1.1-UI界面重构/checklist.md` - 已记录 `/` 下拉候选验证
- `docs/step7-上下文管理/checklist.md` - 已记录 `/compact` 验证

**依赖任务**：Task 5

**具体内容**：
1. 会话列表、新建会话、恢复会话的 WebSocket e2e 已有测试覆盖。
2. `/compact` 从 WebSocket 消息到压缩事件推送已有 handler 层 e2e 覆盖。
3. `/dump` 的快照组装、JSON/Markdown 渲染与文件写入已有单元测试覆盖。
4. `/` 候选下拉在 Step 1.1 checklist 中已有 Playwright 验证记录。
5. 当前 Step 9 不新增代码，因此不追加新的回归测试。

**参考资料**：
- `src/internal/interaction/web/handler_test.go`
- `src/internal/interaction/web/handler_compact_test.go`
- `src/internal/interaction/web/dump_test.go`
- `docs/step1.1-UI界面重构/checklist.md`
- `docs/step7-上下文管理/checklist.md`
