# 114Step 4 — System Prompt 设计 / Tasks

> 本文档回答「怎么做、按什么顺序做、每步动什么文件」。所有任务状态初始为「待完成」，开始实现时改为「进行中」，完成时改为「已完成」。

---

## Task 1: 搭建 prompt 模块骨架与核心数据结构

**状态**：已完成

**目标**：在 `src/internal/engine/prompt/` 下建立模块入口、Source 接口、Builder 骨架；定义最终输出的 `SystemPrompt` 结构（system 字符串 + 起始 user 消息 + 各层 token 统计 + cache 区间标记）。

**影响文件**：

- `src/internal/engine/prompt/builder.go` — 新建，Builder 主体
- `src/internal/engine/prompt/sources/source.go` — 新建，Source 接口 + SystemPrompt 结构体
- `src/internal/engine/prompt/README.md` — 新建，模块说明与扩展指南

**依赖**：无

**具体内容**：

1. 定义 `Source` 接口：`Name() string` + `Assemble(ctx context.Context, env Env) (Section, error)`
2. 定义 `Section` 结构：`{Name, Content, Placement (System|UserMessage), Tokens int}`
3. 定义 `SystemPrompt` 结构：`{SystemBlocks []SystemBlock, LeadUserMessage string, Stats []SourceStat, TotalTokens int}`，其中 `SystemBlock = {Text string, Cacheable bool}`
4. 定义 `Env` 结构（环境参数）：`{ OS, CWD, GitStatus, Date, Version, StaticOverrides map[string]string }`
5. 实现 `Builder`：注册多个 Source，按顺序调用 `Assemble`，分组为 `SystemBlocks` 与 `LeadUserMessage`
6. 实现 `Builder.Assemble(ctx, env) (SystemPrompt, error)`，整体流程串联
7. 单元测试：空 Source 列表、单个 Source、混合 Placement 的合并

**参考资料**：

- `src/llm/provider.go:30` — `StreamChat` 当前签名（`systemPrompt string` 单参数）
- `src/internal/engine/prompt/` — Step 1 留的空目录
- 模板参考 Anthropic Prompt Caching 文档：[https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching](https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching)

---

## Task 2: 实现静态 System Prompt 与环境上下文 Source

**状态**：已完成

**目标**：实现两个最基础的 Source——`static.go`（硬编码 5 个子模块：角色/行为准则/代码质量规范/工具使用原则/安全边界）和 `environment.go`（OS + CWD + Git 状态）。

**影响文件**：

- `src/internal/engine/prompt/sources/static.go` — 新建，静态 SP
- `src/internal/engine/prompt/sources/environment.go` — 新建，环境采集
- `src/internal/engine/prompt/template.go` — 新建，模板变量渲染
- `src/internal/engine/prompt/tokens/estimate.go` — 新建，token 估算

**依赖**：Task 1

**具体内容**：

1. `static.go`：以 Go 原始字符串字面量硬编码 5 个子模块，每个子模块一个常量；用 `<system_role>`、`<behavior_principles>`、`<code_quality>`、`<tool_usage>`、`<safety_boundary>` 五段 XML 风格标签包裹（方便 LLM 解析边界）
2. `static.go`：支持 `Env.StaticOverrides` 按子模块名覆盖（用于将来开发者模式注入实验内容）
3. `environment.go`：调用 `runtime.GOOS` 获得 OS；调用 `os.Getwd()` 获得 CWD；调用 `os/exec` 执行 `git rev-parse --abbrev-ref HEAD` + `git status --porcelain` + `git log -1 --oneline`（带 1s 超时，失败时降级为 "unknown"）；所有命令错误**不向上抛**，降级为可读字符串
4. `environment.go`：工作目录必须 resolve 真实路径（`filepath.EvalSymlinks`），与 tool/safety/path.go 保持一致
5. `template.go`：实现 `Render(text string, env Env) string`，识别并替换 `{{OS}}`、`{{CWD}}`、`{{GIT_BRANCH}}`、`{{GIT_DIRTY}}`、`{{DATE}}`、`{{VERSION}}`（VERSION 从 build flag 注入，缺省 "dev"）
6. `tokens/estimate.go`：实现 `Estimate(text string) int`（按 `len([]rune(text)) / 2` 近似，介于 1/3 与 1/4 之间的折中）
7. 单元测试：每个 Source 单独跑；环境采集在临时 git 仓库中跑；模板渲染覆盖所有变量

**参考资料**：

- `src/internal/tool/safety/path.go` — resolve 真实路径的实现风格参考
- `src/internal/runtime/console/` — 跨平台 OS 获取参考
- `src/internal/config/config.go` — VERSION 注入方式（ldflags）

---

## Task 3: 实现 AGENTS.md 加载与合并 Source

**状态**：已完成

**目标**：实现 `agents_md.go` Source——加载全局 `~/.codepilot/AGENTS.md` 与项目级 `<cwd>/AGENTS.md`，按**项目级优先**策略合并同名段落，输出为 LeadUserMessage（placement = UserMessage）。

**影响文件**：

- `src/internal/engine/prompt/sources/agents_md.go` — 新建，AGENTS.md 加载与合并
- `src/internal/engine/prompt/sources/agents_md_test.go` — 新建，单元测试

**依赖**：Task 1（Source 接口）、Task 2（环境上下文提供 CWD）

**具体内容**：

1. 全局路径：`~/.codepilot/AGENTS.md`（`os.UserHomeDir()` + 拼路径）
2. 项目路径：`<cwd>/AGENTS.md`（项目根标识符，按配置 working_directory 而非任意 cwd）
3. 解析：支持 H2（`##` ）作为段落分隔，每个 H2 段为 `{name, body}`；段落以标题名为 key
4. 合并策略：项目级同名段落**完全覆盖**全局（不做合并拼接），不同名段落按文件中出现顺序追加；先列全局独有段、再列项目级独有段
5. 文件缺失处理：任一文件不存在不报错，对应侧记空
6. 大小限制：单文件最大 64KB（`io.LimitReader`），超过截断并打 warning 日志
7. 输出：合并后的 Markdown 文本，外面包 `<project_instructions>` 标签；Placement = UserMessage
8. 单元测试：仅有全局、仅有项目、两者都有且冲突、两者都没、文件超限、空文件

**参考资料**：

- ClaudeCode `CLAUDE.md` 加载机制（双层合并）
- Codex / Cursor 项目的 `AGENTS.md` 约定

---

## Task 4: 实现自动记忆占位与 Builder 串联

**状态**：已完成

**目标**：实现 `memory.go` Source（空实现 + 接口预留）+ 接入 `Builder.Assemble` 完整管线（注册所有 4 个 Source、按 Placement 分组、产出 `SystemPrompt`）。

**影响文件**：

- `src/internal/engine/prompt/sources/memory.go` — 新建，记忆占位
- `src/internal/engine/prompt/builder.go` — 修改，串联 4 个 Source
- `src/internal/engine/prompt/builder_test.go` — 新建，集成测试

**依赖**：Task 2、Task 3

**具体内容**：

1. `memory.go`：定义 `MemoryProvider` 接口（`Recall(ctx, query) ([]string, error)`），本步骤提供 `NoopMemoryProvider`（永远返回空）；Builder 默认注册 Noop，Step 8 替换为真实实现
2. `Builder.Assemble`：注册顺序 `static → environment → agents_md → memory`（前 2 个 placement=System，后 2 个 placement=UserMessage）
3. `Builder.Assemble`：把 System placement 的多段 `Section` 拼成 `SystemBlocks`（按 Source 顺序）；UserMessage placement 合并为单条 `LeadUserMessage`（空则不创建）
4. `Builder.Assemble`：累计每段 tokens（`tokens/estimate.go`），写入 `Stats` 与 `TotalTokens`
5. 配置开关：`config.json` 中 `system_prompt.enabled = false` 时，Builder 跳过所有 Source，返回空 `SystemPrompt`
6. 集成测试：端到端跑一次 `Assemble`，验证最终结构字段、顺序、token 估算

**参考资料**：

- `src/internal/config/config.go` — 配置结构定义位置

---

## Task 5: 改造 LLM Provider 与 ConversationManager 接入新管线

**状态**：已完成

**目标**：让 `StreamChat` 接收新 `SystemPrompt` 结构（不再只是 string）；Anthropic Provider 把 SystemBlocks 切片为带 `cache_control` 的多段；OpenAI Provider 把 system 字符串 + LeadUserMessage 拼到 messages 最前；ConversationManager 把 LeadUserMessage 作为不可裁剪的「首条 user 消息」。

**影响文件**：

- `src/llm/types.go` — 修改，扩展 SystemPrompt 公共类型
- `src/llm/provider.go` — 修改，Provider.StreamChat 签名变化
- `src/llm/anthropic.go` — 修改，system 多段 + cache_control
- `src/llm/openai.go` — 修改，system 字符串 + 拼 messages
- `src/internal/engine/conversation/manager.go` — 修改，识别 LeadUserMessage

**依赖**：Task 4

**具体内容**：

1. `types.go`：定义 `SystemPrompt` 公共类型（与 prompt 包的同构体；或把 prompt 包的类型导出到 llm 包，避免循环依赖）
2. `provider.go`：`StreamChat` 第 2 个参数从 `systemPrompt string` 改为 `sp llm.SystemPrompt`
3. `anthropic.go`：构造请求时把 `sp.SystemBlocks` 拆为多段 `system` 内容，前 N-1 段带 `{"type": "ephemeral", "cache_control": {"type": "ephemeral"}}`（注：Anthropic 实际为 `{"type": "ephemeral"}`），最后一段不标记；LeadUserMessage 追加到 messages 首部
4. `openai.go`：把 SystemBlocks 拼为单个 system 字符串；LeadUserMessage 追加到 messages 首部
5. `manager.go`：暴露 `SetLeadUserMessage(text string)` 与 `IsLeadUserMessage(idx int) bool`；滑动窗口裁剪时**永远保留**首条 LeadUserMessage
6. 单元测试：Anthropic 端构造请求后检查 system 字段是数组且每段都有正确 cache 标记；OpenAI 端检查 system + 首条 user 消息

**参考资料**：

- `src/llm/anthropic.go` 现有 StreamChat 实现
- `src/llm/types.go` 现有 Message / ContentBlock 类型
- Anthropic SDK prompt caching：[https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching](https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching)
- OpenAI prompt caching 文档（仅部分模型支持）

---

## Task 6: 接入主流程 + WebUI 可观测性 + 端到端验证

**状态**：已完成

**目标**：在 WebUI Handler 启动会话时调用一次 `Builder.Assemble` 并缓存结果（同一会话内不变）；状态栏推送 SP token 估算；开发者模式提供导出按钮；端到端跑通 Anthropic 缓存命中验证。

**影响文件**：

- `src/internal/interaction/web/handler.go` — 修改，会话启动时组装 SP
- `src/internal/interaction/web/status.go` — 修改，状态栏推送 SP token
- `src/internal/interaction/web/protocol.go` — 修改，新增 `dev_export_sp` WebSocket 消息
- `web/index.html`（或对应前端文件）— 修改，开发者模式按钮 + 状态栏显示
- `src/internal/engine/prompt/builder_test.go` — 补充端到端测试

**依赖**：Task 5

**具体内容**：

1. `handler.go`：在 `NewSession` / `ResumeSession` 流程中调用 `prompt.NewBuilder(...).Assemble(ctx, env)`，把结果存到 `Session.sp`（新增字段）
2. `handler.go`：每次 `StreamChat` 调用时把 `Session.sp` 透传给 Provider
3. `status.go`：每次状态更新事件携带 `sp_total_tokens` 与 `sp_breakdown`（各 Source 的 token 数）
4. `protocol.go`：新增 `dev_export_sp` 消息类型（双向：前端请求 → 后端返回完整 SP JSON；含 system 字符串 + leadUserMessage + 各段 tokens + cache 区间）
5. 前端：状态栏新增 SP 区域，鼠标悬停显示各层小计；设置面板加开发者模式开关，开启后显示「Export SP」按钮
6. 端到端：手动启动 WebUI，发送 2 轮对话，检查第二轮 Anthropic 返回的 `usage.cache_read_input_tokens > 0`
7. 兼容性验证：恢复 Step 3 留下的旧 session JSON 加载后正常运转，LLM 行为无异常

**参考资料**：

- `src/internal/interaction/web/handler.go` 现有 StreamChat 调用点
- `src/internal/memory/session/session.go` — 会话 JSON 序列化格式
- `src/internal/interaction/web/protocol.go` — 现有 WebSocket 消息类型
- WebUI 状态栏现有实现（在 web/ 目录中）

---

## 任务状态总览


| Task | 名称                                          | 状态  |
| ---- | ------------------------------------------- | --- |
| 1    | 搭建 prompt 模块骨架与核心数据结构                       | 已完成 |
| 2    | 实现静态 System Prompt 与环境上下文 Source            | 已完成 |
| 3    | 实现 AGENTS.md 加载与合并 Source                   | 已完成 |
| 4    | 实现自动记忆占位与 Builder 串联                        | 已完成 |
| 5    | 改造 LLM Provider 与 ConversationManager 接入新管线 | 已完成 |
| 6    | 接入主流程 + WebUI 可观测性 + 端到端验证                  | 待完成 |


