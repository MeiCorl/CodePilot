# Step 9 - 快捷命令系统

## 背景

CodePilot 原计划在 Step 9 独立实现快捷命令系统。但在 Step 1.1 WebUI 重构、Step 7 上下文管理以及后续调试能力补强过程中，项目已经逐步落地了用户可见的斜杠命令入口、前端候选下拉、WebSocket 命令消息与后端命令处理器。当前阶段继续单独重构为完整命令框架的收益有限，因此本步骤采用回顾式补记：将已完成的快捷命令能力归档为 Step 9 完成状态，后续如出现命令数量扩张、插件化命令或 Skill 命令注册需求，再单独设计优化。

## 目标用户

1. 通过 WebUI 使用 CodePilot 的普通用户。
2. 需要快速管理会话、清空上下文、手动压缩或导出调试快照的开发者用户。
3. 后续接入 Skill、Hook、SubAgent 时需要复用快捷命令入口的系统开发者。

## 能力清单

1. 支持输入 `/` 唤出快捷命令候选下拉。
2. 支持通过键盘或鼠标选择可直接执行的命令。
3. 支持 `/new` 创建新会话。
4. 支持 `/sessions` 以表格视图查看历史会话。
5. 支持 `/resume <id>` 通过会话 ID 前缀恢复历史会话。
6. 支持 `/clear` 清空当前会话上下文并同步清理相关会话产物。
7. 支持 `/compact` 手动触发上下文压缩。
8. 支持 `/dump` 导出当前会话上下文与 System Prompt 快照。
9. 支持将快捷命令转为独立 WebSocket 业务消息，避免误送入 LLM 对话。
10. 支持后端对会话类、压缩类、导出类命令返回可观测结果或明确错误。

## 非功能性要求

1. 命令执行不得阻塞普通对话主流程。
2. 与流式响应、手动压缩等互斥操作共用已有 busy 保护。
3. 命令失败要通过结构化错误或结果消息反馈给前端。
4. 已有命令入口要保持向后兼容，不能破坏 Step 1 至 Step 8 已落地能力。
5. 当前阶段不强制引入新的命令注册表抽象，避免为了补文档而制造无收益重构。

## 设计骨架

```text
WebUI 输入框
  -> SLASH_COMMANDS 候选列表
  -> 可执行命令直接发送 WebSocket 消息
  -> 带参数命令在前端解析后发送 WebSocket 消息
  -> handler.go 中的 Router 分发到对应处理器
  -> 会话管理 / 上下文压缩 / dump 导出等既有模块执行
  -> session_loaded / session_list / compaction_event / dump_result / stream_error 回推前端
```

关键落点：

1. 前端命令候选与执行入口位于 `src/internal/interaction/web/static/app.js`。
2. WebSocket 协议消息定义位于 `src/internal/interaction/web/protocol.go`。
3. 后端命令路由与处理器位于 `src/internal/interaction/web/handler.go`。
4. `/dump` 的导出实现位于 `src/internal/interaction/web/dump.go`。
5. `/compact` 复用 Step 7 的上下文压缩协调器。
6. 会话类命令复用已有 `SessionManager` 与会话持久化链路。

## Out of Scope（本步骤不做）

1. 不新增完整命令注册表或插件化命令框架。
2. 不设计命令权限模型，权限仍沿用对应业务操作已有约束。
3. 不实现 `/help` 的完整命令帮助页。
4. 不把 Skill、Hook、SubAgent 的未来命令提前纳入本步骤。
5. 不对已经稳定运行的前后端路由做无必要重构。
