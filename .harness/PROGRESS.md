# CodePilot 项目进度

> 本文档记录 CodePilot 项目的整体实现进度，每完成一步功能开发后须同步更新本文档。
>
> - 计划全景与系统架构见 [HARNESS.md](./HARNESS.md)
> - 各步骤详细 spec / tasks / checklist 见 `docs/{step_n-idea_name}/` 目录
> - **维护规约**：每次 `sdd-run` 或 `specs` 技能完成一个步骤的全部 Task 后，必须在 [📊 总览](#-总览)、[✅ 已完成步骤](#-已完成步骤) 与 [🕓 待完成步骤](#-待完成步骤) 三处同步更新

---

## 📊 总览


| 指标     | 数值                                                    |
| ------ | ----------------------------------------------------- |
| 计划总步骤数 | 12（含子步骤后实际更多）                                         |
| 已完成步骤数 | 6（Step 1 / Step 1.1 / Step 1.2 / Step 1.3 / Step 2 / Step 3）    |
| 当前最新版本 | V1.0.5                                                |
| 下一步骤   | Step 4 — System Prompt 设计                             |
| 最近更新   | 2026-06-04                                            |


进度条：

```
[█████████████░░░░░░░░░░░░░░░░] 6/12 步骤完成（50%）
```

---

## ✅ 已完成步骤

### Step 1 — LLM 打通（V1.0.0）

- **完成时间**：见 commit `afc80d9` `Release V1.0.0: 打通LLM,支持Ahthropic和OpenAI协议...`
- **设计文档**：[docs/step1-LLM打通/](../docs/step1-LLM打通/)
- **Task 完成数**：12 / 12
- **核心交付能力**：
  1. Anthropic（Claude）+ OpenAI（GPT）双 Provider 适配，统一通过 `ContentBlock` 抽象交互
  2. 配置文件驱动（`~/.codepilot/config.json`）：模型、API 地址、密钥、超时、重试等
  3. 基于滑动窗口的简单上下文管理（预留 System Prompt 空间）
  4. 多会话管理 + 会话 JSON 持久化（`~/.codepilot/sessions/`）
  5. 异步文件日志系统（`~/.codepilot/logs/`）
  6. 流式响应 + 中断
- **遗留备注**：Task 9 的 Bubble Tea TUI 界面已在 Step 1.1 中被 WebUI 完全替换

### Step 1.1 — UI 界面重构：TUI → WebUI（V1.0.1）

- **完成时间**：见 commit `a54be70` `Release V1.0.1: 重构UI界面,使用Web页面代替TUI交互`
- **设计文档**：[docs/step1.1-UI界面重构/](../docs/step1.1-UI界面重构/)
- **Task 完成数**：10 / 10
- **核心交付能力**：
  1. 彻底移除 Bubble Tea / Lipgloss / Glamour / Bubbles 等 TUI 依赖
  2. Go `embed.FS` 嵌入前端静态资源 → 零构建步骤
  3. HTTP + WebSocket（gorilla/websocket）全双工通信，仅绑定 `127.0.0.1:8969`
  4. 跨平台自动调起默认浏览器（Windows / macOS / Linux）
  5. WebUI 五大区域：顶部信息栏 / 左侧会话历史 / 中间对话流 / 底部输入栏 / 状态指示
  6. 深色编辑式美学（参考 Linear / Vercel / Raycast 风格）
  7. 最小可用斜杠命令：`/new`、`/sessions`、`/resume <id>`，输入 `/` 弹出下拉候选

### Step 1.2 — 对话栏富文本渲染增强（V1.0.2）

- **完成时间**：见 commit `fe891be` `Release V1.0.2: 增强UI富文本渲染能力`
- **设计文档**：[docs/step1.2-对话栏文本渲染/](../docs/step1.2-对话栏文本渲染/)
- **Task 完成数**：8 / 8
- **核心交付能力**：
  1. highlight.js v11.11.1 自动语法高亮（go / js / ts / py / json / sql / yaml 等 18+ 语言）
  2. 代码块顶部 header：语言标签 + 一键复制按钮（Copy → Copied 反馈）
  3. JSON 块智能校验：合法显示 `✓ valid` 角标；非法显示错误行列号
  4. DOMPurify v3.2.4 XSS 防护，剔除 `<script>` / `<iframe>` / `on`* 等危险标记
  5. 流中 chunk 纯文本展示，`stream_done` 后一次性 marked → DOMPurify → enhanceCodeBlocks，避免半截代码闪烁
  6. highlight 主题 token 颜色与设计系统对齐（琥珀金 keyword / 思考蓝函数名 / 绿色字符串）

### Step 2 — 工具系统集成（V1.0.3）

- **完成时间**：见 commit `27ee859` `Release V1.0.3: 工具系统集成（内置ReadFile、WriteFile、Grep、Glob、Bash等5个基本工具）`
- **设计文档**：[docs/step2-工具系统集成/](../docs/step2-工具系统集成/)
- **Task 完成数**：9 / 9
- **核心交付能力**：
  1. 统一 `Tool` 接口 + `Registry` 集中注册机制，新增工具仅需 `init()` 中注册一行
  2. 5 个内置基础工具：`ReadFile` / `WriteFile` / `Bash` / `Glob` / `Grep`
  3. ContentBlock 扩展：新增 `ToolUseBlock` / `ToolResultBlock` 两类内容块
  4. Anthropic 协议适配：tools 数组、`tool_use` / `tool_result` 原生转换
  5. OpenAI 协议适配：function_calling、`tool_calls` / `role=tool` 消息转换
  6. **单轮闭环**：LLM 一次 `tool_use` → 执行 → `tool_result` → LLM 二次回复 → 把控制权交回用户（多轮 ReAct 留到 Step 3）
  7. 安全兜底：路径沙箱（resolve 真实路径 + working_directory 范围校验）+ Bash 危险命令黑名单（`rm -rf /`、`mkfs`、`shutdown` 等）
  8. WebUI 工具执行展示：`tool_call_start` / `tool_call_end` 事件流，左侧图标栏 + 折叠区域，与用户/助手消息视觉区分
  9. 工具执行超时（默认 30s，`tool_execution_timeout_seconds` 可覆盖）+ 审计日志
  10. 会话持久化兼容：`tool_use` / `tool_result` 可序列化到 session JSON，恢复会话后完整渲染工具调用链

### Step 3 — ReAct 与 Agent Loop 实现（V1.0.4）

- **完成时间**：2026-06-04
- **设计文档**：[docs/step3-ReAct与Agent Loop实现/](../docs/step3-ReAct与Agent%20Loop实现/)
- **Task 完成数**：7 / 7
- **核心交付能力**：
  1. ReAct 循环引擎：将「LLM 推理 → 工具调用 → 结果反馈」升级为可循环迭代的 AgentLoop，直到 LLM 认为任务完成或触发终止条件
  2. 多工具并行调用：`StreamChunk` 支持多个 `ToolUseBlock`，`ExecuteBatch` 按权限分组执行（只读并行、写入/执行串行）
  3. 迭代上限保护：默认最大 25 次迭代（可配置），达到上限后注入提示让模型优雅收尾
  4. 上下文 token 溢出保护：每次迭代前检查剩余 token，空间不足时注入提示让模型总结当前进展
  5. 优雅中断与进度保留：用户中断时保留已完成迭代的所有消息到会话历史，支持后续恢复
  6. 工具错误智能反馈：工具执行失败时将错误信息反馈给 LLM，由 LLM 自主决定重试或换策略
  7. `AgentLoopHooks` 回调机制：`OnIterationStart`（迭代进度推送）+ `OnLoopDone`（循环结束通知）
  8. WebUI 迭代进度事件：`agent_iteration` WebSocket 事件 + `status_update("thinking")` 状态切换
  9. 5 种终止原因枚举：completed / max_iterations / context_overflow / aborted / error，前端可区分展示
  10. 会话持久化向后兼容：多轮 tool_use/tool_result 消息正确序列化，Step 2 旧会话在新代码下正常加载

### Step 1.3 — WebUI 流式渲染（V1.0.5）

- **完成时间**：2026-06-04
- **设计文档**：[docs/step1.3-WebUI流式渲染/](../docs/step1.3-WebUI流式渲染/)
- **Task 完成数**：6 / 6
- **核心交付能力**：
  1. 流式 Markdown 实时渲染：LLM 输出的每个 delta 经 marked + DOMPurify 解析后立即渲染为格式化 HTML，用户实时看到标题、列表、加粗、链接、表格等元素
  2. 未闭合代码块预处理（`closeOpenFences`）：自动检测并补全未闭合的围栏标记，确保代码块在流式中即时创建容器
  3. 防抖合并渲染（80ms）：高频 delta 合并后统一渲染，长文本不卡顿
  4. 首个 delta 立即渲染：用户无感知延迟，响应即现
  5. 流结束后最终增强：`enhanceCodeBlocks` 追加 hljs 语法高亮、代码块 header（语言标签 + Copy 按钮）、JSON 校验，最终渲染质量与 Step 1.2 一致
  6. DOMPurify 安全防护持续有效：流式过程中每次 `innerHTML` 更新均经过 DOMPurify 过滤
  7. 完整的边界场景处理：工具调用兼容、中断内容保留、会话切换状态清理、空响应安全跳过

---

## 🕓 待完成步骤

> 下列步骤按 [HARNESS.md](./HARNESS.md) 计划顺序排列，开始下一步前请先用 `/specs` 触发需求澄清并生成 spec / tasks / checklist 三文档。


| 编号  | 步骤名                   | 所属架构层 | 状态    | 计划目录                             |
| --- | --------------------- | ----- | ----- | -------------------------------- |
| 4   | System Prompt 设计      | 引擎层   | ⏳ 待开始 | `docs/step4-System Prompt设计/`    |
| 5   | 权限系统设计                | 安全层   | ⏳ 待开始 | `docs/step5-权限系统设计/`             |
| 6   | MCP 协议实现              | 工具层   | ⏳ 待开始 | `docs/step6-MCP协议实现/`            |
| 7   | 上下文管理                 | 记忆层   | ⏳ 待开始 | `docs/step7-上下文管理/`              |
| 8   | 记忆系统                  | 记忆层   | ⏳ 待开始 | `docs/step8-记忆系统/`               |
| 9   | 快捷命令系统                | 交互层   | ⏳ 待开始 | `docs/step9-快捷命令系统/`             |
| 10  | Skill 系统              | 交互层   | ⏳ 待开始 | `docs/step10-Skill系统/`           |
| 11  | Hook 系统               | 工具层   | ⏳ 待开始 | `docs/step11-Hook系统/`            |
| 12  | SubAgent              | 工具层   | ⏳ 待开始 | `docs/step12-SubAgent/`          |


---

## 🧭 架构层覆盖度

按 [HARNESS.md](./HARNESS.md) 5 层架构统计各层当前已落地组件：


| 架构层       | 已落地                                                    | 待落地                                         |
| --------- | ----------------------------------------------------- | ------------------------------------------- |
| 第 1 层：交互层 | WebUI（HTTP + WebSocket + 富文本渲染 + 流式 Markdown 实时渲染）                      | 完整命令系统（Step 9）、Skill 系统（Step 10）            |
| 第 2 层：引擎层 | 对话管理 + Agent Loop（ReAct 循环迭代 + 多工具并行 + 迭代上限 + 溢出保护）、System Prompt 雏形 | 完整 System Prompt（Step 4）                    |
| 第 3 层：工具层 | 工具抽象 + Registry + 5 内置工具 + 路径沙箱 + Bash 黑名单 + 批量执行 | MCP（Step 6）、Hook（Step 11）、SubAgent（Step 12） |
| 第 4 层：记忆层 | 会话持久化、上下文滑动窗口                                        | 高级上下文管理（Step 7）、自动记忆（Step 8）                |
| 第 5 层：安全层 | 路径沙箱、Bash 危险命令黑名单                                    | 完整权限系统（Step 5，含 HITL 确认）                    |


---

## 📌 更新规约

本文档由 `specs`  技能在每完成一个步骤的全部 Task 后自动维护，要求：

1. **触发时机**：某个步骤的 `tasks.md` 中所有 Task 状态均更新为 `已完成`，且 `checklist.md` 全部验证通过
2. **更新内容**：
  - [📊 总览](#-总览)：已完成步骤数、当前最新版本、下一步骤、最近更新日期
  - [✅ 已完成步骤](#-已完成步骤)：追加一个新章节，包含完成时间、commit、设计文档链接、Task 数、核心交付能力
  - [🕓 待完成步骤](#-待完成步骤)：删除已完成的对应行
  - [🧭 架构层覆盖度](#-架构层覆盖度)：根据新增能力将相应组件从「待落地」迁到「已落地」
3. **commit 信息**：若新步骤已 release，引用 `git log --oneline` 中的 commit hash 与 message
4. **日期格式**：完成时间统一使用 `YYYY-MM-DD`

