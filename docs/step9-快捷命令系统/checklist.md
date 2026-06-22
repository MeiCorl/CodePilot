# Step 9 - 快捷命令系统 Checklist

> 本 checklist 为回顾式验收记录。由于 Step 9 不新增业务代码，验证来源为现有代码、既有测试和历史步骤 checklist。

- [x] `/` 输入可唤出快捷命令候选
  - 预期：输入框以 `/` 开头且不含空格时展示命令候选；候选可通过键盘或鼠标选择
  - 实际：`SLASH_COMMANDS`、`openSlashDropdown`、`applySlashCompletion` 已实现候选过滤、上下键选择、Enter/Tab 执行或补全；Step 1.1 checklist 已记录 Playwright 验证
  - 结论：通过

- [x] `/new` 可创建新会话
  - 预期：命令触发后保存当前会话，创建新的空会话，并向前端回推 `session_loaded`
  - 实际：前端 `/new` 发送 `new_session`；后端 `handleNewSession` 保存当前会话、创建新会话、重置上下文并回推会话加载结果；`TestNewSessionCreatesAndSavesCurrent` 已覆盖
  - 结论：通过

- [x] `/sessions` 可查看历史会话
  - 预期：命令触发后以表格视图展示最近历史会话，包含 ID、时间、消息数和预览信息
  - 实际：前端 `/sessions` 打开表格视图并发送 `list_sessions` 的 table 模式；后端 `handleListSessions` 支持 table 模式按创建时间降序返回最近会话；`TestListSessionsTableMode` 已覆盖
  - 结论：通过

- [x] `/resume <id>` 可恢复历史会话
  - 预期：输入会话 ID 前缀后恢复唯一匹配会话；无匹配或多匹配时返回明确错误
  - 实际：前端 `onSendClicked` 识别 `/resume` 前缀并发送 `resume_session`；后端 `handleResumeSession` 支持前缀匹配、无匹配和歧义错误；`TestResumeSessionPrefixMatch`、`TestResumeSessionNotFound`、`TestResumeSessionAmbiguous` 已覆盖
  - 结论：通过

- [x] `/clear` 可清空当前会话上下文
  - 预期：保留当前 session_id，清空消息历史，刷新上下文用量，并清理会话关联的工具结果归档
  - 实际：前端 `/clear` 发送 `clear_session`；后端 `handleClearSession` 重置当前会话、截断消息、清理工具结果归档并回推 `session_loaded`
  - 结论：通过

- [x] `/compact` 可手动触发上下文压缩
  - 预期：命令触发后进入 compacting 状态，执行手动压缩，推送 `compaction_event` 并刷新上下文用量
  - 实际：前端 `/compact` 发送 `compact`；后端 `handleCompact` / `runManualCompact` 执行手动压缩；`handler_compact_test.go` 已覆盖禁用、summary、none、light+summary 等链路
  - 结论：通过

- [x] `/dump` 可导出当前会话快照
  - 预期：命令触发后导出当前会话上下文与 System Prompt 快照，成功返回导出文件路径，失败返回错误
  - 实际：前端 `/dump` 发送 `dump`；后端 `handleDump` 生成 `dump.json` 与 `dump.md` 并推送 `dump_result`；`dump_test.go` 已覆盖 JSON round-trip、Markdown 渲染、文件覆盖写与字段组装
  - 结论：通过

- [x] 快捷命令不会误送入 LLM
  - 预期：内部命令应转换为 WebSocket 业务消息，普通文本才进入 `user_input`
  - 实际：带 `exec` 的命令在 `applySlashCompletion` 中直接执行；`/resume <id>` 在 `onSendClicked` 中提前拦截；其余普通输入才发送 `user_input`
  - 结论：通过

- [x] 命令执行具备并发保护
  - 预期：流式响应或压缩进行中，可能改写会话历史的命令不得并发执行造成状态错乱
  - 实际：`/compact` 与 `/dump` 复用 `stream.tryAcquire` busy 保护；普通输入流式状态下前端也会阻止再次发送
  - 结论：通过

- [x] 端到端验收
  - 预期：用户可以在 WebUI 中通过 `/` 候选完成会话管理、上下文压缩和调试导出，不需要走 Agent Loop
  - 实际：Step 1.1、Step 7 与当前代码测试共同覆盖了前端候选、会话命令、压缩命令和 dump 导出；本步骤不新增代码，仅完成状态归档
  - 结论：通过
