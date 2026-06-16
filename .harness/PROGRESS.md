# CodePilot 项目进度

> 本文档记录 CodePilot 项目的整体实现进度，每完成一步功能开发后须同步更新本文档。
>
> - 计划全景与系统架构见 [PROJECT.md](./PROJECT.md)
> - 各步骤详细 spec / tasks / checklist 见 `docs/{step_n-idea_name}/` 目录
> - **维护规约**：每次 `sdd-run` 或 `specs` 技能完成一个步骤的全部 Task 后，必须在 [📊 总览](#-总览)、[✅ 已完成步骤](#-已完成步骤) 与 [🕓 待完成步骤](#-待完成步骤) 三处同步更新

---

## 📊 总览


| 指标     | 数值                                                    |
| ------ | ----------------------------------------------------- |
| 计划总步骤数 | 12（含子步骤后实际更多）                                         |
| 已完成步骤数 | 11（Step 1 / Step 1.1 / Step 1.2 / Step 1.3 / Step 1.4 / Step 2 / Step 3 / Step 4 / Step 5 / Step 6 / Step 7）  |
| 当前最新版本 | V1.4.0                                                |
| 进行中步骤  | —                                                    |
| 下一步骤   | Step 8 — 记忆系统（需先 `/specs` 触发需求澄清）                       |
| 最近更新   | 2026-06-16                                            |


进度条：

```
[███████████████████████████████] 11/12 步骤完成（~92%，Step 8 待开始）
```

---

## ✅ 已完成步骤

### Step 1 — LLM 打通（V1.0.0）

- **完成时间**：见 commit `afc80d9` `Release V1.0.0: 打通LLM,支持Ahthropic和OpenAI协议...`
- **设计文档**：[docs/step1-LLM打通/](../docs/step1-LLM打通/)
- **Task 完成数**：12 / 12
- **核心交付能力**：
  1. Anthropic（Claude）+ OpenAI（GPT）双 Provider 适配，统一通过 `ContentBlock` 抽象交互
  2. 配置文件驱动（`~/.codepilot/setting.json`）：模型、API 地址、密钥、超时、重试等
  3. 基于滑动窗口的简单上下文管理（预留 System Prompt 空间）
  4. 多会话管理 + 会话 JSON 持久化（`~/.codepilot/sessions/`）
  5. 异步文件日志系统（`~/.codepilot/logs/`）
  6. 流式响应 + 中断
- **遗留备注**：Task 9 的 Bubble Tea TUI 界面已在 Step 1.1 中被 WebUI 完全替换

#### Step 1.1 — UI 界面重构：TUI → WebUI（V1.0.1）

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

#### Step 1.2 — 对话栏富文本渲染增强（V1.0.2）

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

#### Step 1.3 — WebUI 流式渲染（V1.0.5）

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

#### Step 1.4 — WebUI 工具展示优化（V1.0.7）

- **完成时间**：2026-06-07
- **设计文档**：[docs/step1.4-WebUI工具展示优化/](../docs/step1.4-WebUI工具展示优化/)
- **Task 完成数**：7 / 7
- **核心交付能力**：
  1. **「查看改动」按钮 + 双栏 diff 弹窗**：`WriteFile` / `EditFile` 完成态工具块头部新增琥珀金按钮，点击触发双栏 diff 弹窗（Before / After 全文 + 行级高亮），新增绿、删除红、未变白
  2. **diff-match-patch 行级 diff**：自包含 vendor 资源（21 KB UMD 版），`diff_main` + `diff_cleanupSemantic` → 按 op 拆行映射 `add / del / ctx` 三态，按行号分配
  3. **highlight.js 按文件后缀语法高亮**：覆盖 13 种语言（`go` / `markdown` / `json` / `xml` / `python` / `typescript` / `javascript` / `css` / `yaml` / `sql` / `bash` 等），未识别后缀回退纯文本
  4. **进程内 FileDiffStore**：`sync.RWMutex` 保护 `map[tool_use_id]FileDiff`；单条容量上限 2 MB（`Before+After` 字节数），超限拒绝写入并 warn；tool_use_id 为主键保证同文件多次编辑各自独立弹窗
  5. **WebSocket 协议 `get_file_diff` ↔ `file_diff`**：前端按钮点击 → ws 发 `get_file_diff{tool_use_id}` → 后端按 id 查 store → 回 `file_diff{tool_use_id, found, reason, file_path, language, before, after}`；reason 取 `not_found` / `too_large`
  6. **Consumer-side Interface 解耦**：`tool.FileDiffSink` 接口定义在 tool 包（最底座），web.FileDiffStore 自动满足，builtin 仅依赖抽象，**避免 web → builtin 反向依赖**；主流程在 `main.go` 顶层构造 `FileDiffStore` 单例后通过 `SetDiffSink` setter 注入 WriteFile/EditFile
  7. **diff 数据生命周期**：仅进程内存，不进 session JSON；进程重启后旧会话拉取得到 `reason="not_found"` 弹窗显示"暂无改动预览"对应文案，不报错
  8. **tool_use_id ctx 传递**：`tool.WithToolUseID(ctx, id)` / `ToolUseIDFromContext(ctx)` 让 engine 在调工具前把 id 注入 ctx，工具侧 recordDiff 不感知 engine 实现
  9. **历史会话兼容**：恢复分支沿用 `appendToolStartNode + updateToolEndNode` 同一套代码，旧 `tool_call.name` 为 WriteFile/EditFile 自动出现按钮（点击后由后端回 not_found 提示）
  10. **安全与性能**：弹窗内容全 DOMPurify/转义，无 `<script>` 注入路径；10s WS 超时兜底；Esc / 点击遮罩 / × 按钮三种关闭方式；并发 5 个 toolUseID 拉取互不串扰
  11. **端到端测试覆盖**：5 个 e2e 集成用例（WriteFile / EditFile / NotFound / NilStore / MultipleParallel）+ 真实启动冒烟（HTTP 资源 200、WS 协议往返正确）
  12. **失败态无按钮**：`updateToolEndNode` 中 `status === 'completed' && isFileEditingTool(toolName)` 守卫；error / aborted / timeout 不调 `attachViewDiffButton`

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

### Step 4 — System Prompt 设计（V1.0.6）

- **完成时间**：2026-06-06
- **完成 commit**：`c0df351` `Step 4 Task 6: 接入主流程 + WebUI 可观测性`
- **设计文档**：[docs/step4-System Prompt设计/](../docs/step4-System%20Prompt%E8%AE%BE%E8%AE%A1/)
- **Task 完成数**：6 / 6
- **核心交付能力**：
  1. **分层 System Prompt 体系**：`prompt` 模块 + `Builder` 模式，按注册顺序调用 4 个 Source（static / environment / agents_md / memory），按 Placement 分组为 `SystemBlocks`（进 system 字段）与 `LeadUserMessage`（进首条 user 消息）
  2. **静态 SP 5 子模块**（XML 风格 `<system_role>` / `<behavior_principles>` / `<code_quality>` / `<tool_usage>` / `<safety_boundary>`）：硬编码规约「先说再做」「引用 `file_path:line_number`」「不顺手越权优化」「用 ReadFile 代替 Bash cat」「破坏性操作前确认」「不绕过 git hook」「防注入」
  3. **环境上下文自动注入**：会话启动时一次性采集 OS（`runtime.GOOS`）+ CWD（resolve 真实路径）+ Git 状态（branch / dirty / 最近 commit），单条 `<environment>` XML 段拼入 system
  4. **AGENTS.md 双层合并**：全局 `~/.codepilot/AGENTS.md` + 项目级 `<cwd>/AGENTS.md` 按 H2 段解析，项目级同名段**完全覆盖**全局，单文件超 64KB 截断 + warning 日志；合并结果外层包 `<project_instructions>` 标签，以 LeadUserMessage 形式注入 messages 首部
  5. **模板变量插值**：`{{OS}}` / `{{CWD}}` / `{{GIT_BRANCH}}` / `{{GIT_DIRTY}}` / `{{DATE}}` / `{{VERSION}}` 在 Source 内部按需替换，未识别变量原样保留
  6. **Anthropic Prompt Caching**：`AnthropicProvider` 把 `sp.SystemBlocks` 拆为多段带 `cache_control: ephemeral, ttl=5m` 标记的 system 内容（前 N-1 段打标记，最后一段作为断点边界），第二轮起命中服务端缓存降低成本与延迟
  7. **OpenAI 协议适配**：`OpenAIProvider` 把 `SystemBlocks` 拼为单条 system-role 消息，`LeadUserMessage` 拼到 messages 首部
  8. **LeadUserMessage 不被滑动窗口裁剪**：`ConversationManager.SetLeadUserMessage` 把首条 user 消息放到 history 之外，`GetContext` 在窗口派生结果前拼接，天然处于窗口保护之外
  9. **WebUI 可观测性**：状态栏新增 `sp` 区域显示总 token 估算（紧凑格式 1.5k / 15k）+ 鼠标悬停 tooltip 显示 4 层 Source 小计；`context_usage` WebSocket 消息新增 `sp_total_tokens` / `sp_breakdown` 字段
  10. **开发者模式 Export SP**：双击 SP 区域唤出开发者面板 → 点击「Export SP」触发 `dev_export_sp` WebSocket 消息 → 后端回推完整 SP 快照（`SystemBlocks` 文本数组 + `LeadUserMessage` + `Stats` + `TotalTokens`）→ 前端模态框分 3 段折叠展示
  11. **配置可关闭**：`prompt.Builder.SetEnabled(false)` 短路所有 Source，返回空 SystemPrompt（保持与早期会话兼容）
  12. **会话恢复兼容**：System Prompt 不持久化到 session JSON，每次启动重新 assemble；旧 session（Step 3 时代含 tool_use/tool_result）正常加载与渲染（`TestStep4_LoadLegacySessionCompat` 覆盖）

### Step 5 — 权限系统设计

- **完成时间**：2026-06-07
- **设计文档**：[docs/step5-权限系统设计/](../docs/step5-权限系统设计/)
- **Task 完成数**：8 / 8
- **核心交付能力**：
  1. **三层权限模式**：`strict`（严格）/ `default`（默认）/ `permissive`（放行）三档切换，每档定义不同级别的自动放行与拦截策略
  2. **可配置的允许/拒绝/询问规则**：在 `setting.json` 中按「工具名 + 参数模式」声明 `allow` / `deny` / `ask` 动作，支持路径 glob 匹配和 Bash 命令前缀匹配
  3. **多层配置合并**：全局配置（`~/.codepilot/setting.json`）+ 项目级配置（`<cwd>/.codepilot/setting.json`）+ 会话级临时规则（内存），按优先级合并
  4. **人在回路（HITL）确认**：当规则未命中或命中 `ask` 时，通过 WebSocket `permission_request/response` 协议暂停 Agent Loop 等待用户确认
  5. **三种授权范围**：本次允许（OneTime）/ 本会话允许（Session，追加内存规则）/ 永久允许（Permanent，写入 `setting.json`）
  6. **权限拒绝优雅降级**：权限拒绝作为 `ToolResultBlock{IsError: true}` 返回给 LLM，LLM 可自主调整策略继续工作
  7. **危险命令黑名单增强**：迁移至 `internal/security/blacklist.go`，保留 8 条原有规则 + 新增 3 条远程脚本下载执行规则（`curl|sh` / `wget|bash` / `sudo` 变体），不可被配置绕过
  8. **路径沙箱策略化**：迁移至 `internal/security/sandbox.go`，新增 `IsPathOutsideSandbox` 查询函数，越界路径根据档位决策（Strict→Deny / Default→Ask / Permissive→Allow），工具内部 `ResolveInSandbox` 硬兜底保留形成双层防护
  9. **安全层统一归口**：原 `internal/tool/safety/` 包整体迁移至 `internal/security/`，统一管理策略模型、检查器、拦截器、黑名单、路径沙箱
  10. **WebUI 权限确认对话框**：展示工具名、参数摘要、触发原因 + 四按钮（拒绝/本次允许/本会话允许/永久允许）+ 60 秒倒计时 + 状态栏权限模式展示
  11. **92 个测试用例全部通过**：含 8 个端到端集成场景（默认模式/严格模式/自定义规则/多层配置/永久允许/黑名单/双层防护/向后兼容）+ 并发安全 + 性能基准 + 超时取消

### Step 6 — MCP 协议实现（V1.3.0）

- **完成时间**：2026-06-09
- **设计文档**：[docs/step6-MCP协议实现/](../docs/step6-MCP协议实现/)
- **Task 完成数**：9 / 9
- **核心交付能力**：
  1. **JSON-RPC 2.0 编解码层**：`Request` / `Response` / `Notification` 三核心结构体 + `MarshalRequest` / `UnmarshalMessage` + `crypto/rand` 全局唯一 ID 生成器
  2. **Transport 抽象 + stdio 传输**：`Transport` 接口（Connect/Send/Recv/Close/IsAlive）+ stdio 子进程传输（`os/exec` + JSONL stdin/stdout），支持 env 注入
  3. **Streamable HTTP 传输**：POST 请求/响应，支持 `application/json` + `text/event-stream` 双响应格式，Bearer/Basic Auth，`Mcp-Session-Id` 头部传播
  4. **Session 三阶段握手**：`Initialize`（protocolVersion="2025-03-26"）→ `NotifyInitialized` → `ListTools` / `CallTool`，基于 id 的异步 pending 映射实现请求-响应关联
  5. **多 server 连接池**：`Pool` 并发建连（`errgroup`）+ 失败隔离 + `ListToolsCached` 60s 缓存 + Session 复用
  6. **MCP Tool → CodePilot Tool 适配器**：`adapterTool` 实现 `tool.Tool` 接口，命名 `mcp__<server>__<tool>`，`RegisterAll` 批量注册到 `tool.Registry`，Agent 调用无感
  7. **指数退避重连**：1s / 3s / 9s 三次退避 → unhealthy 标记；重连成功恢复 healthy；lazy 重连（下次调用时触发）
  8. **主流程接入**：`main.go` 启动时 `BuildTransports` → `Pool.InitializeAll` → `adapter.RegisterAll` → `handler.SetMCPPool`；配置缺失时正常启动
  9. **权限系统集成**：MCP 工具名 `mcp__<server>__<tool>` 走 `permission.Decide` 全链路，支持 allow / deny / ask 规则匹配 + HITL 确认
  10. **WebUI 可观测性**：工具块紫色 `mcp: <server>` 徽标 + 状态栏 MCP 健康区（绿/黄/红/灰四色圆点）+ hover tooltip 展示 server 详情
  11. **配置驱动**：`setting.json` 新增 `mcp.servers[]` 段，支持 stdio（command/args/env）+ http（url/headers）+ disabled 跳过 + 超时配置
  12. **端到端冒烟（Task 9）**：10 个 E2E 集成用例（stdio 握手+ ListTools / stdio CallTool / HTTP CallTool / 权限拦截 / server 失败隔离 / 重连退避 / 重连耗尽 / 50 路并发 id 匹配 / 命名规范 / 历史会话兼容）全绿 + 真实启动冒烟（`codepilot-e2e.exe` 启动监听 58426 + mock-stdio 52931 + mock-http 双 server 真实握手 + WS 客户端收到 mcp_status `healthy=2 tools=4` 推送）+ 200+ 单元/集成测试通过 + 跨功能回归 Step 1~5 无破坏

### Step 7 — 上下文管理（V1.4.0）

- **完成时间**：2026-06-16（代码与端到端验证完成，待 release 提交）
- **设计文档**：[docs/step7-上下文管理/](../docs/step7-上下文管理/)
- **Task 完成数**：8 / 8
- **核心交付能力**：
  1. **两层压缩策略编排**：每次 API 请求前先跑第一层「轻量预防」（管单条消息内工具结果体积，纯本地估算无 LLM 调用），再判定是否需要第二层「重量兜底」（管累积历史长度，调 LLM 摘要）
  2. **第一层工具结果存盘 + 预览替换**：单个/合计超阈值（默认 8K）的 tool_result 落盘到 `<sessionID>/tool_results/<toolUseID>`，内存 in-place 替换为「头部约 500 token 预览 + 存盘路径尾注」；确定性规则 + 存盘幂等保证 prompt cache 前缀稳定
  3. **工具结果存盘子系统**：以 toolUseID 为文件名、`O_CREATE|O_EXCL` 原子幂等写入（并发下只写一次）、跨会话隔离、`isSafeName` 路径逃逸防护
  4. **第二层结构化摘要压缩**：逼近窗口（剩余 ≤ 13K）时调 LLM 生成 5 段式摘要（目标/进展/决策/待办/关键文件），强制禁工具 + `<draft>` 草稿剥离；尾部保留约 1 万 token / ≥5 条近期原文；补边界提示防脑补
  5. **历史原文归档 + 活跃历史重写**：被摘要的早期原文 append 到 `history_archive.jsonl`，`messages.jsonl` 原子重写为「摘要+近期」（session 包唯一非 append-only 低频重组路径，写临时文件 + rename）
  6. **压缩协调器 + 会话级熔断**：摘要连续失败 3 次即熔断（本会话停自动第二层），允许手动 `/compact` 重置重试；自动/手动两种安全余量分级
  7. **Provider 撞墙兜底**：`prompt_too_long` 精确识别 → 紧急压缩（无视余量/临时豁免熔断）→ 单次重试，保证用户最新输入不丢、不吞异常
  8. **用户原文保留 + 配置可覆盖**：用户原始消息原文保留不被摘要改写；`setting.json` 新增 `compaction` 段全阈值可覆盖，`enabled=false` 降级为纯滑动窗口
  9. **可观测性**：结构化日志（sessionID/层级/前后 token/熔断）+ WebUI 状态栏「压缩」按钮 + `/compact` 斜杠命令 + `compaction_event` 双层推送（summary 强提示 toast / light 轻量计数标记）+ 熔断状态可见
  10. **端到端验证（Task 8）**：5 个 context 包跨组件 e2e（真实 SessionManager + ToolResultStore 落盘：第一层/第二层/熔断/撞墙/会话恢复）+ 4 个 handler 层 e2e（真实 HTTP Server + WS + 真实 Compactor：/compact 往返 + compaction_event 推送）+ 5 个 runOneLLM 撞墙 e2e 全绿；`go test ./...` Step 1~6 零回归（3 个失败经 git stash 验证为 Windows PowerShell 平台问题 / flaky，与本步骤无关）

---

## 🕓 待完成步骤

> 下列步骤按 [PROJECT.md](./PROJECT.md) 计划顺序排列，开始下一步前请先用 `/specs` 触发需求澄清并生成 spec / tasks / checklist 三文档。


| 编号  | 步骤名                   | 所属架构层 | 状态      | 计划目录                             |
| --- | --------------------- | ----- | ------- | -------------------------------- |
| 8   | 记忆系统                  | 记忆层   | ⏳ 待开始   | `docs/step8-记忆系统/`               |
| 9   | 快捷命令系统                | 工具层   | ⏳ 待开始   | `docs/step9-快捷命令系统/`             |
| 10  | Skill 系统              | 工具层   | ⏳ 待开始   | `docs/step10-Skill系统/`           |
| 11  | Hook 系统               | 工具层   | ⏳ 待开始   | `docs/step11-Hook系统/`            |
| 12  | SubAgent              | 工具层   | ⏳ 待开始   | `docs/step12-SubAgent/`          |


---

## 🧭 架构层覆盖度

按 [PROJECT.md](./PROJECT.md) 5 层架构统计各层当前已落地组件：


| 架构层       | 已落地                                                    | 待落地                                         |
| --------- | ----------------------------------------------------- | ------------------------------------------- |
| 第 1 层：交互层 | WebUI（HTTP + WebSocket + 富文本渲染 + 流式 Markdown 实时渲染 + SP 可观测性 + 开发者模式 Export + 工具块「查看改动」双栏 diff 弹窗 + 权限确认对话框 + 状态栏权限模式展示 + MCP server 来源徽标 + MCP 健康状态区） | —            |
| 第 2 层：引擎层 | 对话管理 + Agent Loop（ReAct 循环迭代 + 多工具并行 + 迭代上限 + 溢出保护）、完整 System Prompt（Builder + 4 Source + 模板变量 + Anthropic 缓存切片） | —                                            |
| 第 3 层：工具层 | 工具抽象 + Registry + 6 内置工具（ReadFile/WriteFile/EditFile/Bash/Glob/Grep）+ 路径沙箱 + Bash 黑名单 + 批量执行 + 进程内 FileDiffStore + **MCP 客户端**（JSON-RPC 2.0 + stdio/HTTP 双传输 + Session 三阶段握手 + 连接池 + 适配器自动注册 + 指数退避重连 + 10 个 E2E 集成用例全绿 + 真实启动冒烟 healthy=2 tools=4） | 快捷命令系统（Step 9）、Skill 系统（Step 10）、Hook（Step 11）、SubAgent（Step 12） |
| 第 4 层：记忆层 | 会话持久化、上下文滑动窗口 + **高级上下文管理（Step 7）**（两层压缩：轻量预防工具结果存盘预览 + 重量摘要兜底 + 撞墙紧急压缩 + 会话级熔断 + 历史原文归档 + compaction 可观测性 + 全阈值可配置） | 自动记忆（Step 8）                |
| 第 5 层：安全层 | 完整权限系统（三层模式 + 可配置规则 + 多层配置合并 + HITL 确认 + 权限拦截器 + 危险命令黑名单增强 + 路径沙箱策略化 + 双层防护） | —                                            |


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

