# Step 7 — 上下文管理：任务拆解

> 本文档把 spec.md 的能力拆解为可执行任务。任务按依赖顺序排列，每个任务标注状态、目标、影响文件、依赖、具体内容与参考资料定位。
>
> **状态管理规则**：开始某任务前先将状态更新为「进行中」；编码完成且对应 checklist 验证项全部通过后更新为「已完成」。每完成一个任务等待用户确认再开始下一个。

---

## Task 1: 上下文度量基建 + 配置扩展

**状态**：已完成

**目标**：为压缩决策提供细粒度的 token 度量能力，并把压缩相关阈值纳入配置体系（可覆盖默认值）。

**影响文件**：

- `src/internal/memory/context/measure.go` — 新建，细粒度 token 度量
- `src/internal/memory/context/measure_test.go` — 新建，度量单测
- `src/internal/engine/conversation/manager.go` — 修改，`estimateTextTokens` / `isCJK` 改为复用 measure 包，避免重复逻辑
- `src/internal/config/config.go` — 修改，新增 `Compaction` 配置段与默认值

**依赖**：无

**具体内容**：

1. 在 `memory/context/measure.go` 提供细粒度度量函数（把 `manager.go:287` 的 `estimateTextTokens` 与 `manager.go:311` 的 `isCJK` 逻辑下沉到此处并导出）：
  - `EstimateTextTokens(text string) int`：CJK 2 字符/token、非 CJK 4 字符/token（与现有逻辑一致，保证不回归）。
  - `EstimateBlockTokens(block llm.ContentBlock) int`：单个 ContentBlock 的 token，按 `block.ToText()` 估算（TextBlock 取文本；ToolResultBlock 取 Content；ToolUseBlock 取 `name+id` 摘要而非完整 input，因为 input 已在请求结构里单独计）。
  - `EstimateMessageTokens(msg llm.Message) int`：单条消息 = 每条消息固定结构开销（沿用 `manager.go:181` 的 `messageOverhead=15`）+ 各 block 累加。
  - `EstimateMessagesTokens(msgs []llm.Message) int`：累计。
2. `manager.go` 的 `TokenEstimate()` 改为调用 measure 包的 `EstimateMessagesTokens`，删除内部重复的 `estimateTextTokens` / `isCJK`（保持对外行为不变，由现有 `manager_test.go` 守护回归）。
3. 在 `config.go` 新增压缩配置段（命名建议 `Compaction`，字段全部 `omitempty`，默认值在 `setDefaults()` 中填充）：
  - 工具结果存盘阈值（token，单条工具结果与单条消息内合计共用此阈值，默认 8K=8192）
  - 预览头部保留长度（token）
  - 第二层自动触发余量（token）
  - 第二层手动触发目标余量（token）
  - 近期原文保留量（token）与最少保留条数
  - 熔断阈值（连续失败次数）
  - 压缩总开关（`enabled`，默认 true，便于对比与降级）
4. `config.example.json` 同步追加 `compaction` 段示例并注释说明。

**参考资料**：

- 现有估算逻辑：[manager.go:287](../../src/internal/engine/conversation/manager.go#L287) `estimateTextTokens`、[manager.go:311](../../src/internal/engine/conversation/manager.go#L311) `isCJK`
- 现有 token 用量精确值来源：[manager.go:41](../../src/internal/engine/conversation/manager.go#L41) `lastInputTokens`、[types.go:128](../../src/llm/types.go#L128) `TokenUsage.InputTokens`
- 已有的粗估兜底：[estimate.go:24](../../src/internal/engine/prompt/tokens/estimate.go#L24) `Estimate`（rune/2，本任务不复用它，因其不分 CJK）
- 配置默认值填充模式参考：[config.go:188](../../src/internal/config/config.go#L188) `setDefaults`

---

## Task 2: 工具结果存盘子系统

**状态**：已完成

**目标**：实现工具结果落盘的独立子系统——以工具调用 ID 为文件名、存放在对应会话目录下的 `tool_results/` 子目录，写入幂等（文件已存在则跳过），并提供路径查询能力供预览尾注引用。

**影响文件**：

- `src/internal/memory/context/tool_result_store.go` — 新建，存盘子系统
- `src/internal/memory/context/tool_result_store_test.go` — 新建，单测（含幂等、并发、跨会话隔离）

**依赖**：无

**具体内容**：

1. 定义 `ToolResultStore` 结构体，持有「会话根目录」（即 `SessionManager.projectDir`）。
2. 核心方法：
  - `Save(sessionID, toolUseID, content string) (filePath string, skipped bool, err error)`：落盘到 `<projectDir>/<sessionID>/tool_results/<toolUseID>`。**幂等**：文件已存在则 `skipped=true` 直接返回，不重复写；不存在则 `O_CREATE|O_EXCL` 原子创建后写入（`O_EXCL` 保证并发下只写一次，失败方视为已存在）。
  - `Path(sessionID, toolUseID string) string`：纯计算预期路径，不触发 IO（供预览尾注使用，即使尚未落盘也能给出路径）。
  - `Exists(sessionID, toolUseID string) bool`：查询是否已落盘。
3. 路径构造复用 session 包的目录约定（`<projectDir>/<session_id>/`），`tool_results/` 作为其下的专用子目录——与 [session.go:9](../../src/internal/memory/session/session.go#L9) 注释中「`{session_id}` 一层目录为后续存放工具调用结果等内容预留空间」的设计预留对齐。
4. 写入失败不 panic，返回 err 由调用方决定降级（保留原文）。
5. 内容原样写入（纯文本，工具结果的 `Content` 字符串）；不做 JSON 包裹，便于 LLM 重读时 ReadFile 直接得到可读文本。

**参考资料**：

- 会话目录结构约定：[session.go:5](../../src/internal/memory/session/session.go#L5) 包注释、[session.go:671](../../src/internal/memory/session/session.go#L671) `sessionDirPath`
- `O_CREATE|O_EXCL` 幂等创建语义：Go 标准库 `os.OpenFile`
- 工具结果内容来源：[types.go:77](../../src/llm/types.go#L77) `ToolResultBlock.Content`、[types.go:79](../../src/llm/types.go#L79) `ToolUseID`

---

## Task 3: 第一层轻量预防压缩器

**状态**：已完成

**目标**：实现单条消息内工具结果的存盘 + 预览替换编排——单个工具结果超阈值即存盘替换为预览；单条消息内多个工具结果合计超阈值时，按体积从大到小依次存盘替换，直到合计降到阈值以下。利用「内存 in-place 替换 + 存盘幂等」天然保证 prompt cache 前缀稳定（一旦替换，后续每轮一致）。

**影响文件**：

- `src/internal/memory/context/preview.go` — 新建，预览生成（头部截断 + 路径尾注）
- `src/internal/memory/context/preview_test.go` — 新建，单测
- `src/internal/memory/context/light_compactor.go` — 新建，第一层压缩器
- `src/internal/memory/context/light_compactor_test.go` — 新建，单测

**依赖**：Task 1（度量）、Task 2（存盘子系统）

**具体内容**：

1. `preview.go`：
  - `BuildPreview(content, filePath string, previewTokens int) string`：截取 `content` 头部约 `previewTokens` 个 token 对应的字符数（用 measure 的估算反推字符数，按 rune 截断避免截断多字节字符），尾部拼接固定提示文案，格式形如「（完整结果已存盘：，需要时可用 ReadFile 重新读取准确内容）」。原文短于预览长度时不截断、不加尾注（直接返回原文，等价于不替换）。
2. `light_compactor.go`：定义 `LightCompactor`，持有 `ToolResultStore` 与配置阈值。
  - `Compact(messages []llm.Message, sessionID string) (changed bool, err error)`：遍历每条消息内的 `*llm.ToolResultBlock`：
    - 先对单个 block 估算 token，超「工具结果存盘阈值（默认 8K）」者：存盘（幂等）→ in-place 把 `block.Content` 替换为 `BuildPreview(...)`。
    - 再对单条消息内所有 tool_result block 合计 token，若超同一「工具结果存盘阈值（8K）」：按 block token 降序，依次存盘 + 替换，直到合计 ≤ 阈值（被预览替换后的 block 重新按预览长度计 token）。
    - 已是预览态（可由「内容是否以存盘提示文案结尾」或 store 的 `Exists` 判定）的 block 不重复处理，避免重复 IO。
  - **关键设计说明（写入注释）**：替换是对内存中 `*ToolResultBlock` 的 in-place 修改。由于「是否替换」完全由「token 是否超阈值」这一确定性规则决定（阈值不变则结果不变），每轮 API 请求前重跑得到一致结果——天然满足「一旦替换则后续每轮都替换；一旦不替换则后续都不替换」的缓存命中要求，**无需额外维护替换状态记录**。
  - 存盘失败时：该 block 降级为保留原文，记录 warn 日志，`changed` 仍按实际发生替换的情况返回，不让单点 IO 失败中断整轮压缩。
  - **日志（用户感知）**：实际发生替换时记 Info 日志（sessionID、tool_use_id、原 token、存盘路径、预览 token），并通过返回的 `CompactionResult` 让上层（Task 7）推送 UI 事件；未触发替换的轮次不打日志、不推送，避免噪音。
3. 持久化时序说明（写入注释）：工具结果原文在产生当轮已被 handler 追加到 `messages.jsonl`（append-only，原文）；压缩发生在下一轮 API 请求前、只改内存 history 的 Content 字段，**不回写 jsonl**。因此 jsonl 始终保留工具结果原文，会话恢复后重新加载原文、重新跑一次轻量预防（存盘幂等跳过）→ 内存再次变为预览态，自洽。

**参考资料**：

- in-place 替换的可行性：[agent_loop.go:247](../../src/internal/engine/conversation/agent_loop.go#L247) `resultContent` 追加的是 `&results[i]`（指针），history 持有同一指针，改 Content 即生效
- 工具结果在历史中的位置：[agent_loop.go:254](../../src/internal/engine/conversation/agent_loop.go#L254) 写入 user tool_result 消息
- append-only 持久化时机：[handler.go:436](../../src/internal/interaction/web/handler.go#L436) `saveCurrentSessionLocked`（仅追加 `history[persistedMsgCount:]`）

---

## Task 4: 第二层摘要压缩器 + 历史归档

**状态**：已完成

**目标**：实现整体对话逼近窗口上限时的结构化摘要压缩——调用 LLM 对较早历史生成 5 段式摘要，尾部保留约 1 万 token / 至少 5 条近期原文，早期原文归档落盘，内存替换为「摘要 + 近期原文」，补一条边界提示消息。

**影响文件**：

- `src/internal/memory/context/summary_compactor.go` — 新建，摘要压缩器 + 摘要 Prompt
- `src/internal/memory/context/summary_compactor_test.go` — 新建，单测（Prompt 构造、尾部切分、归档写入）
- `src/internal/memory/session/archive.go` — 新建，历史归档 + 活跃历史重写
- `src/internal/memory/session/archive_test.go` — 新建，单测

**依赖**：Task 1（度量）

**具体内容**：

1. `archive.go`（session 包）：
  - `ArchiveMessages(sessionID string, msgs []llm.Message) error`：把被压缩掉的早期原文消息追加写入 `<projectDir>/<sessionID>/history_archive.jsonl`（append-only，与 messages.jsonl 同格式逐行 JSON）。用于「原文也存盘」。
  - `RewriteActiveMessages(sessionID string, msgs []llm.Message) error`：把当前活跃对话（摘要 + 近期原文）**全量覆盖写**到 `messages.jsonl`（非 append-only，属于低频重组事件）。供压缩后让持久化与内存一致——恢复会话时加载到的就是「摘要 + 近期原文」，不会再次触发对同一段历史的重复摘要。
  - 注释说明：这是 session 包中唯一打破「纯 append-only」的路径，**仅由第二层摘要压缩调用**，频率极低；其余路径仍 append-only。
2. `summary_compactor.go`：
  - 摘要 Prompt（固定文案，写在常量中）：明确**禁止调用任何工具**；要求**先写分析草稿（`<draft>...</draft>`）再写正式摘要**，草稿会被程序丢弃、不进入最终上下文；正式摘要按 5 个固定部分组织——① 用户目标与意图 ② 已完成的工作 ③ 关键决策与结论 ④ 尚未解决的问题 / 待办 ⑤ 关键文件路径。Prompt 同时要求保留所有文件路径、函数名、约定、报错等关键信息。
  - `summarize(ctx, provider, toSummarize []llm.Message) (summary string, err error)`：构造请求（不传 toolSpecs → 强制禁工具），调用 `provider.StreamChat`（用一个不含工具的 SystemPrompt）消费完整流取文本；**剥离 `<draft>` 段**只保留正式摘要部分。
  - `splitByTailTokens(history []llm.Message, keepTokens, minKeep int) (toSummarize, keep []llm.Message)`：从尾部按 token 往回数保留近期原文，保留量取「约 1 万 token」与「至少 5 条消息」的较大者；切分点对齐到消息边界（不拆单条消息）；确保 `toSummarize` 不为空（否则无意义）。
  - `Compact(ctx, ...) (changed bool, err error)`：编排——切分 → 归档早期原文（`ArchiveMessages`）→ 调 LLM 生成摘要 → 构造一条 summary 消息（Role=User，Content 为摘要文本，带可识别前缀标记，如「[会话摘要] ...」）→ 内存 history 替换为 `[summary 消息] + keep` → `RewriteActiveMessages` 落盘 → 补一条边界提示 user 消息（「以上为历史摘要，若需要文件细节请用 ReadFile 重新读取，不要依据摘要脑补代码」）。
  - 摘要失败：返回 err，由协调器（Task 5）计入熔断，不修改 history。

**参考资料**：

- 「调 LLM 但禁工具」的既有范式：[agent_loop.go:296](../../src/internal/engine/conversation/agent_loop.go#L296) `injectTerminationPrompt` 传 `toolSpecs=nil`
- StreamChat 消费流的范式：[manager.go:505](../../src/internal/engine/conversation/manager.go#L505) `runOneLLM`
- 空的 SystemPrompt 判定：[types.go:234](../../src/llm/types.go#L234) `SystemPrompt.IsEmpty`
- messages.jsonl 全量重写的兜底参考：[session.go:365](../../src/internal/memory/session/session.go#L365) `TruncateMessages`（截断为 0，本任务在其基础上支持写入新内容）

---

## Task 5: 压缩协调器 + 熔断 + 手动触发

**状态**：已完成

**目标**：实现顶层 `Compactor` 协调器——每次 API 请求前编排「先轻量预防、再判断是否重量兜底」；维护会话级熔断状态（连续失败 3 次禁自动、允许手动）；提供手动触发入口；管理自动/手动两种安全余量。

**影响文件**：

- `src/internal/memory/context/compactor.go` — 新建，协调器
- `src/internal/memory/context/compactor_test.go` — 新建，单测（编排顺序、熔断、余量分级）
- `src/internal/memory/context/` 内既有组件联调

**依赖**：Task 3（轻量预防）、Task 4（摘要压缩）

**具体内容**：

1. 定义 `Compactor`，持有 `LightCompactor`、`SummaryCompactor`、配置、以及**会话级熔断状态**（`map[sessionID]int` 记连续失败次数 + `map[sessionID]bool` 记是否已熔断，`sync.Mutex` 保护）。
2. 核心方法 `Compact(ctx, manager *conversation.ConversationManager, provider, sessionID string, manual bool) (CompactionResult, error)`：
  - **第一层（每次都跑）**：对 manager 当前 history 跑 `LightCompactor.Compact`。
  - **第二层判定**：
    - 计算剩余 token（复用 `manager.GetContextUsage(windowSize).Remaining`，windowSize 取配置）。
    - 自动模式（`manual=false`）：仅当 `remaining ≤ 自动触发余量(13K)` 且**未熔断**时，跑 `SummaryCompactor.Compact`。
    - 手动模式（`manual=true`）：无视当前 remaining 立即跑 `SummaryCompactor.Compact`（即使剩余很高也允许，因用户主动要压）；熔断状态下手动触发**重置连续失败计数**给一次重试机会。
  - **熔断计数**：`SummaryCompactor.Compact` 成功 → 清零该会话失败计数；失败 → 计数 +1，达到阈值（3）置熔断标志、记录 warn；熔断后自动模式跳过第二层。
  - 返回 `CompactionResult`（描述本轮是否发生轻量替换 / 是否发生摘要、压缩前后 token 估算、是否触发熔断、层级 light/summary），供 WebUI 可观测性。
  - **日志（用户感知）**：每次编排记录结构化日志——第一层替换 block 数、第二层是否触发及判定依据（remaining vs 阈值）、压缩前后 token、是否熔断；Info 级，熔断 Warn 级。协调器是两层日志的统一出口。
3. 协调器对 manager 的访问：通过新增的 `ConversationManager` 方法暴露可变的 history 引用（见 Task 7 的 manager 改动），避免协调器直接 deep-copy 整段历史（开销大且改了不生效）。
4. 失败语义：第一层失败不阻断第二层判定；第二层失败返回 err 但不中断调用方主流程（调用方 runOneLLM 捕获后继续用当前 history 发请求）。

**参考资料**：

- 剩余 token 计算：[manager.go:252](../../src/internal/engine/conversation/manager.go#L252) `GetContextUsage`
- 熔断/计数状态机的并发保护参考：[interceptor.go](../../src/internal/security/interceptor.go) `pendingPermissions` + `pendingMu` 的模式

---

## Task 6: Provider 撞墙紧急压缩 + 重试

**状态**：已完成

**目标**：对 Provider 返回的上下文超长错误（`prompt_too_long` / HTTP 400）提供兜底——捕获后做一次更激进的紧急压缩（强制第二层，无视熔断与余量），然后用压缩后的历史重试一次请求，不让一次撞墙就丢掉用户最新输入。

**影响文件**：

- `src/internal/engine/conversation/manager.go` — 修改，`runOneLLM` 增加「撞墙 → 紧急压缩 → 重试一次」包装
- `src/internal/engine/conversation/manager_test.go` — 修改/新增，覆盖撞墙重试路径
- `src/llm/anthropic.go` / `src/llm/openai.go` — 可能修改，确保 `prompt_too_long` 错误可被识别（暴露状态码或错误类型判定）

**依赖**：Task 5（协调器）

**具体内容**：

1. 在 llm 包提供「是否上下文超长错误」的判定能力：
  - 现状：`anthropic.go:320` `shouldRetry` 中 `anthropic.Error.StatusCode == 400` 被判为不可重试直接返回。需暴露一个 `IsContextTooLongError(err error) bool`（基于 `anthropic.Error.StatusCode == 400` 且错误体含 `prompt_too_long` / `context length` 等关键字，或 API 返回的 error.type 判定）；OpenAI 侧对齐 `context_length_exceeded`。
  - 注意区分：普通 400（参数错误）≠ 上下文超长 400，判定要精确，避免对参数错误也触发压缩。
2. `runOneLLM` 包装重试：第一次 `StreamChat` 若返回的错误命中 `IsContextTooLongError`：
  - 调用协调器的「紧急压缩」（强制第二层摘要，无视熔断与余量阈值；若熔断则临时豁免一次）。
  - 用压缩后的 history 重新 `GetContext` 再 `StreamChat` 一次（**仅重试 1 次**，防止无限循环）。
  - 重试仍失败 → 返回原始错误（不吞异常），但此时历史已被压缩、用户最新输入仍在历史尾部未丢失。
3. 紧急压缩失败（如 LLM 不可用）：返回原始 `prompt_too_long` 错误，让上层正常上报。

**参考资料**：

- 现有错误判定与重试边界：[anthropic.go:320](../../src/llm/anthropic.go#L320) `shouldRetry`、[anthropic.go:330](../../src/llm/anthropic.go#L330) 状态码分支
- 超时/取消不可重试的范式：[anthropic.go:322](../../src/llm/anthropic.go#L322)
- runOneLLM 现有结构：[manager.go:505](../../src/internal/engine/conversation/manager.go#L505)

---

## Task 7: 接入主流程 + WebUI 可观测性

**状态**：已完成

**目标**：把压缩协调器接入引擎层主链路（每次 API 请求前跑压缩）与交互层（`/compact` 命令 + WebUI 按钮 + 压缩事件可观测性推送），并在 `main.go` 顶层完成装配。

**影响文件**：

- `src/internal/engine/conversation/manager.go` — 修改，`ConversationManager` 持有 `Compactor` 引用；新增暴露可变 history 的方法（如 `History()` / `ReplaceHistory()` 或直接让 Compactor 操作）；`runOneLLM` 在 `GetContext()` 前调用 `Compactor.Compact(manual=false)`
- `src/internal/interaction/web/handler.go` — 修改，`/compact` 命令路由 + WebSocket 压缩触发消息；压缩事件推送（`compaction_event`）；构造 `loopCfg` 时注入配置
- `src/internal/interaction/web/protocol.go` — 修改，新增压缩相关 WebSocket 消息类型
- `src/internal/interaction/web/static/`*（前端）— 修改，状态栏压缩按钮 + 压缩事件展示
- `src/main.go` — 修改，构造 `Compactor`（注入 `ToolResultStore` + session 目录 + 配置）并注入 `ConversationManager`
- `src/internal/config/config.go` — 确认 `Compaction` 段已就绪（Task 1）

**依赖**：Task 5（协调器）、Task 6（撞墙兜底）

**具体内容**：

1. **manager 接入**：`ConversationManager` 增加 `compactor` 字段与 setter；`runOneLLM` 开头（`manager.go:512` `messages := m.GetContext()` 之前）调用 `m.compactor.Compact(ctx, m, provider, sessionID, false)`（自动模式）；压缩后 `GetContext()` 自然返回压缩后视图。sessionID 由 handler 在 RunAgentLoop 前通过 ctx 或参数注入。
2. **handler 接入**：
  - `/compact` 命令：在现有斜杠命令路由处（参考 `/new`、`/sessions`、`/resume` 的最小实现）增加 `/compact` → 调用 `conv.compactor.Compact(ctx, conv, provider, sessionID, true)`（手动模式）→ 推送 `compaction_event` + `stream_done(reason=compacted)` 或专用消息。
  - WebSocket 消息：新增 `compact`（前端按钮 → 后端触发手动压缩）与 `compaction_event`（后端 → 前端，**覆盖两层**：携带层级 `light`/`summary`、压缩前后 token、是否熔断、替换/摘要条数）。
  - **第二层 UI 强提示**：收到 `compaction_event{level:"summary"}` 时弹 toast 或在对话流插入「已将 N 条历史压缩为摘要」标记节点，让用户明确感知重量级压缩。
  - **第一层 UI 轻量感知**：`compaction_event{level:"light"}` 只更新状态栏压缩计数/小标记，不弹强通知（第一层每次请求都可能跑，避免打扰）。
3. **WebUI 前端**：状态栏上下文用量区旁加「压缩」按钮（接近阈值时可高亮提示）；收到 `compaction_event` 时刷新用量展示并给一个轻量提示（如「已压缩：N 条历史 → 摘要」）。
4. **main.go 装配**：在构造 `ConversationManager`（`main.go:222` 之前的链路）后，构造 `ToolResultStore`（注入 `sessMgr` 的 projectDir）+ `Compactor`，通过 setter 注入 ConversationManager / Handler。配置缺失时用默认值，压缩总开关关闭时跳过装配（降级为纯滑动窗口，兼容旧版）。
5. 回归保护：确保 `runOneLLM` 的压缩调用对现有 AgentLoop 迭代无副作用——压缩发生在每轮 LLM 调用前，迭代间历史增长会自然触发再次压缩。

**参考资料**：

- 现有斜杠命令最小实现位置：搜索 handler 中 `/new` / `/resume` 的处理分支
- RunAgentLoop 调用点：[handler.go:424](../../src/internal/interaction/web/handler.go#L424)、loopCfg 构造 [handler.go:410](../../src/internal/interaction/web/handler.go#L410)
- main.go 装配链：[main.go:222](../../src/main.go#L222) `NewHandler`、[main.go:129](../../src/main.go#L129) `FileDiffStore` 注入范式（同样在顶层构造后 setter 注入）
- WebSocket 消息协议：[protocol.go](../../src/internal/interaction/web/protocol.go)

---

## Task 8: 端到端验证

**状态**：已完成

**目标**：通过端到端集成测试与真实启动冒烟，验证两层压缩、熔断、撞墙兜底、缓存稳定性、会话恢复全链路工作正常，且 Step 1~6 既有功能无回归。

**影响文件**：

- `src/internal/memory/context/e2e_test.go` — 新建，跨组件 e2e
- `src/internal/interaction/web/handler_compact_test.go` — 新建，handler 层 /compact 与压缩事件 e2e
- 复用既有 `codepilot-e2e` 真实启动冒烟范式（参考 Step 6 的 `codepilot-e2e.exe` + mock）

**依赖**：Task 7

**具体内容**：

1. **第一层 e2e**：构造含超大 tool_result 的 history → 跑 `Compactor.Compact(manual=false)` → 断言：超阈值的 tool_result 已被存盘（`tool_results/<id>` 存在且幂等）、内存 Content 已变为预览（含路径尾注）、未超阈值的保留原文、多轮重跑结果一致（缓存稳定）。
2. **第二层 e2e**：构造逼近窗口的长大对话 → 触发自动压缩 → 断言：摘要消息出现在 history 头部、尾部保留约 1 万 token / ≥5 条、早期原文已归档到 `history_archive.jsonl`、边界提示消息已补、`messages.jsonl` 重写为「摘要+近期」。
3. **熔断 e2e**：mock provider 让摘要连续失败 3 次 → 断言：第 3 次后自动模式跳过第二层；手动 `/compact` 仍可触发一次重试并重置计数。
4. **撞墙兜底 e2e**：mock provider 首次返回 `prompt_too_long` → 断言：触发紧急压缩、用压缩后历史重试 1 次、用户最新输入仍在历史尾部未丢失。
5. **会话恢复 e2e**：压缩后落盘 → 重新 Load 会话 → 断言：加载到「摘要+近期原文」活跃视图、不再重复摘要同一段历史、tool_results 文件被幂等跳过。
6. **真实启动冒烟**：`codepilot-e2e` 启动 + 构造长大对话 + 观察状态栏压缩按钮与 `compaction_event` 推送、压缩前后用量变化可见。
7. **回归**：跑全量 `go test ./...`，确认 Step 1~6 测试无破坏（重点：`manager_test`、`session_test`、`handler_test`、Step 6 的 MCP e2e）。

**参考资料**：

- 既有 e2e 冒烟范式：Step 6 的 `codepilot-e2e.exe` + mock-stdio / mock-http（docs/step6-MCP协议实现/）
- handler 层 e2e 测试范式：[handler_test.go](../../src/internal/interaction/web/handler_test.go)

