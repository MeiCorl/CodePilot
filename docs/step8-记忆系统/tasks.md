# Step 8 — 记忆系统：Tasks

> **任务状态管理规则**：开始某任务前，先将状态更新为「进行中」；编码 + 对应 checklist 验证项全部通过后，更新为「已完成」。每完成一个任务等待用户确认再开始下一个。

---

## Task 1: 记忆类型与存储层（types + store）

**状态**：已完成（17 个单元测试 + go vet 全绿）

**目标**：建立记忆系统的数据模型与文件持久化抽象，提供"读索引 / 写记忆文件 / 刷新索引 / 原子写 / 路径逃逸防护"能力，作为 Source 与 Reviewer 的共同底座。

**影响文件**：

- `src/internal/memory/autolearn/types.go` — 新建，定义记忆类型枚举、记忆记录结构、索引行结构
- `src/internal/memory/autolearn/store.go` — 新建，实现文件存储与索引读写
- `src/internal/memory/autolearn/store_test.go` — 新建，单元测试

> 实现备注：`src/internal/memory/` 下已有 `context/`（Step 7）与 `session/` 子包，故自动记忆新建平级子包 `autolearn/`，与架构层「自动记忆」组件对应。frontmatter 序列化复用已有依赖 `gopkg.in/yaml.v3`。

**依赖**：无（本步骤底座）

**具体内容**：

1. 在 `types.go` 定义 4 类记忆枚举 `MemoryType`：`user_preference` / `user_feedback` / `project_knowledge` / `reference`，并标注每类归属存储域（用户级 / 项目级）。
2. 定义单条记忆记录结构（type / title / content / created_at / updated_at 时间戳 / 文件名 slug）。
3. 定义 MEMORY.md 索引行结构：解析 `- [type](file.md)——简介` 格式；按 4 类分块组织。
4. 在 `store.go` 实现存储抽象：
  - 路径解析：用户级 `~/.codepilot/memory/`、项目级 `<cwd>/.codepilot/memory/`；
  - `isSafeName` 校验：文件名仅允许 `[a-z0-9-]`、长度上限（如 48），防路径逃逸；
  - 读索引：解析 MEMORY.md 为分块索引结构（文件缺失视为空索引，不报错）；
  - 写记忆文件：frontmatter（type/title/created_at/updated_at）+ 正文；
  - 刷新索引行：新增 / 更新 / 删除某条索引行后，重新渲染整份 MEMORY.md；
  - **原子写入**：写临时文件 + `os.Rename`，避免并发或崩溃损坏。
5. 提供"测试时注入 home / cwd 路径"的构造方式（参考 `AgentsMDSource` 的 `HomeDirForTest` 模式）。
6. 单元测试覆盖：索引读写往返、原子写、路径逃逸拒绝、frontmatter 渲染、文件缺失降级。

**参考资料**：

- 存储与索引模式参照 `src/internal/engine/prompt/sources/agents_md.go`（`loadFile` / `mergeSections` / `resolvePaths`，约 `agents_md.go:85-150`）
- 原子写参照 Step 7 `messages.jsonl` 重写（临时文件 + rename）
- 路径逃逸防护参照 Step 7 `ToolResultStore.isSafeName`

---

## Task 2: memory Source 索引注入

**状态**：已完成（7 个 MemoryIndexSource 单元测试全绿 + autolearn 17 旧测试无回归 + go vet 干净；environment git 测试 1 个 Windows TempDir 清理 flaky 与本步骤无关）

**目标**：实现 `prompt.Source` 接口的 memory Source，会话启动时合并用户级 + 项目级两个 MEMORY.md 索引，截断后作为 LeadUserMessage 注入上下文，与 AGENTS.md 注入同构。

**影响文件**：

- `src/internal/engine/prompt/sources/memory_index.go` — 新建，`MemoryIndexSource` 实现 `sources.Source` 接口，依赖 `autolearn.Store` 读两级索引注入 `<memory_index>`
- `src/internal/engine/prompt/sources/memory_index_test.go` — 新建，单元测试
- `src/internal/memory/autolearn/store.go` — 修改，新增公开 `RenderEntries` 供 Source 复用渲染（DRY）
- `src/internal/engine/prompt/builder.go` — 不改动（Source 注册切换在 Task 7 主流程完成）

**依赖**：Task 1（需要 store 读索引）

> 实现备注（架构分层修正）：`autolearn`（记忆层，下层）不得 import `sources`（引擎层，上层），否则违反「下层禁止依赖上层」。故索引注入 Source 归 `sources` 包、依赖 `autolearn.Store` 读数据（上层→下层，合规）。现有 `sources/memory.go`（Step 4 的 `MemoryProvider`/`MemorySource`/`NoopMemoryProvider` Recall-based 占位，为 RAG 预留）本步骤**保留不动**——避免破坏 main.go 编译；Task 7 接入主流程时用 `MemoryIndexSource` 替换其注册并清理占位。

**具体内容**：

1. 实现 `Source` 接口（`Name()="memory"`、`Assemble(ctx, env) (Section, error)`），Placement 固定为 `PlacementUserMessage`。
2. `Assemble` 逻辑：读取用户级 + 项目级两个 MEMORY.md → 合并（项目级在前，更相关）→ 渲染为带外层标签（如 `<memory_index>`）的文本 → 模板变量替换 → 估算 token。
3. **体积上限**：注入内容限制 200 行或 25KB（常量定义，后续 Task 6 接入配置覆盖），超限按行/字节截断并打 `warn` 日志，标注截断前后的行数/字节数。
4. 任一索引文件缺失不报错，对应侧视为空；路径解析失败降级为空 Section。
5. 单元测试覆盖：双索引合并、单边缺失、超限截断 + 日志、空索引降级、Placement 正确。

**参考资料**：

- Source 接口定义：`src/internal/engine/prompt/sources/source.go`（`Source` interface，约 `source.go:117`，`Placement`/`Section` 结构）
- 同构参照 `AgentsMDSource.Assemble`：`src/internal/engine/prompt/sources/agents_md.go`（约 `agents_md.go:85-120`）
- Builder 注册位置：`src/internal/engine/prompt/builder.go`（`NewBuilder` 约 `builder.go:42`，注释已预留 memory Source）

---

## Task 3: ReadFile 沙箱放行 memory 目录

**状态**：已完成（security 包 13 个白名单单元测试 + autolearn 3 个根函数测试全绿，既有用例零回归，go vet/build 干净；builtin/TestBashDangerous 为 Step 5 遗留 Windows flaky，与本步骤无关）

> 实现备注（方案细化）：
>
> - 沙箱放行采用 **functional option**（`security.WithReadRoots`）而非改 `SandboxMiddleware` 签名——对既有 11 处 `SandboxMiddleware(workdir, provider)` 调用点（main.go + 既有单测）零侵入，符合高扩展原则。
> - 核心新增 `ResolveInSandboxWithRoots(path, sandboxDir, extraRoots)`，`ResolveInSandbox` 委托之（`extraRoots=nil`，行为零变化）；`IsPathOutsideSandbox`（权限层用）**故意不感知附加根**，保证「沙箱放行 ≠ 权限绕过」的双层语义。
> - 附加根**仅对 PermRead 工具**（ReadFile/Glob/Grep）生效，PermWrite/PermExec 仍仅认 workdir——纵深防御，memory 只能经 `autolearn.Store` 受控写入。
> - **ReadFile 工具侧无需改动**：沙箱放行发生在 SandboxMiddleware 层，PathResolver 注入链路不变，`read_file.go` 从 ctx 取 absPath 的逻辑原样复用。
> - 新增 `autolearn.UserMemoryRoot`/`ProjectMemoryRoot` 计算根，与 `Store` 落盘目录同源（`TestMemoryRootsMatchStoreLayout` 守护）；`main.go` 用 `buildMemoryReadRoots` 计算两级根并经 `WithReadRoots` 注入。

**目标**：放宽 ReadFile 路径沙箱，放行 `~/.codepilot/memory/` 与 `<cwd>/.codepilot/memory/` 两个目录，使 LLM 能根据索引读取具体记忆文件全文；读取仍走 `permission.Decide` 权限链路。

**影响文件**：

- `src/internal/security/sandbox.go` — 修改，增加 memory 目录白名单判断
- `src/internal/tool/builtin/readfile.go`（或等价路径） — 确认 ReadFile 走沙箱放行后的解析路径
- `src/internal/security/sandbox_test.go` — 新增/补充测试

**依赖**：无（可与 Task 1/2 并行）

**具体内容**：

1. 在沙箱层新增"memory 允许根目录"判断：解析 `~/.codepilot/memory/` 与 `<cwd>/.codepilot/memory/` 为允许读取的额外根，目标路径落在这些根下时视为沙箱内。
2. 保持 `ResolveInSandbox` 仍做真实路径 resolve + working_directory 范围校验，在其之上叠加 memory 白名单分支，形成"working_directory ∪ memory 目录"的可读范围。
3. **权限不绕过**：沙箱放行仅解除路径限制，ReadFile 仍照常调用 `permission.Decide`，可被 allow/deny/ask 规则与三层模式控制（Strict 模式下跨 working_directory 的用户级 memory 读取仍走 Ask/Deny 分支，由模式决定）。
4. 单元测试覆盖：用户级 memory 路径放行、项目级 memory 路径放行、非 memory 的 `.codepilot` 子路径仍被拦截（如 `.codepilot/sessions/`）、路径逃逸（`../`）拒绝。

**参考资料**：

- 沙箱现状：`src/internal/security/sandbox.go`（`IsPathOutsideSandbox` / `ResolveInSandbox`）
- 权限三层模式：`src/internal/security/`（Step 5，`permission.Decide`）
- 双层防护语义参照 Step 5「路径沙箱策略化 + 工具内硬兜底」

---

## Task 4: 回顾专用 prompt 模板 + 敏感脱敏

**状态**：已完成（17 个单元测试 + go vet/build 全绿，Task 1 旧测试零回归）

**目标**：设计回顾 LLM 专用的 prompt 模板（指令其按四分类判断、比对待覆盖的已有索引项、输出结构化决策、禁止记录敏感凭证），并提供敏感信息正则脱敏兜底。

**影响文件**：

- `src/internal/memory/autolearn/prompt.go` — 新建，回顾 prompt 模板 + 结构化决策 schema + JSON 解析降级
- `src/internal/memory/autolearn/sanitizer.go` — 新建，敏感信息正则脱敏兜底
- `src/internal/memory/autolearn/prompt_test.go` — 新建，单元测试

> 实现备注（落包位置修正）：tasks.md 原写 `src/internal/memory/prompt.go`（包根），但 Task 1 已将记忆核心实现落到平级子包 `autolearn/`（因 `memory/` 下已有 `context/`（Step 7）与 `session/` 子包，自动记忆按架构层组件建平级子包）。autolearn 的 package 注释亦明确「prompt.go（Task 4）：回顾专用 prompt 模板；sanitizer.go（Task 4）：敏感信息脱敏」。故 Task 4 三文件统一落 `autolearn/`，与 Task 1 的 types.go/store.go 同包，便于直接复用 `MemoryType`/`IsValidType`/`normalizeSlug`/`isSafeSlug`，也为 Task 5 的 reviewer.go（同包）预留拼装位。

**依赖**：Task 1（需要类型定义）

**具体内容**：

1. 设计回顾 prompt 模板，包含：
  - 角色与任务说明（回顾本轮对话，判断是否有值得长期记住的信息）；
  - **4 类记忆定义与归属域**（偏好/反馈→用户级；项目知识/参考→项目级）；
  - **已有索引比对上下文槽位**（注入当前 MEMORY.md 索引，要求 LLM 决策新建 or 覆盖已有文件，给出目标文件名 slug）；
  - **本轮对话快照槽位**（用户输入 + 最终回复 + 工具调用名摘要）；
  - 输出格式约束（结构化 JSON：每条决策含 action=new/update、type、slug、title、summary、content）；
  - **敏感约束**：明确禁止记录 API key / 密码 / token / 凭证等，遇到时跳过；
  - "无值得记忆信息时返回空数组"的明确指令。
2. 定义结构化输出的 Go 结构与解析逻辑，对非法 JSON / 字段缺失做防御性降级。
3. `sanitizer.go` 实现可选正则脱敏（API key 模式、`password=`、Bearer token 等），作为 prompt 约束的兜底。
4. 单元测试覆盖：prompt 渲染、JSON 解析（正常/非法/空数组）、脱敏命中。

**参考资料**：

- 结构化输出参照 Step 7 第二层摘要 prompt（强制 JSON / `<draft>` 剥离 / 禁工具）
- CodePilot 结构化 LLM 输出模式：`src/internal/engine/conversation/`（摘要压缩相关）

---

## Task 5: 后台异步回顾器（reviewer）

**状态**：已完成（`reviewer.go` ~380 行 + `reviewer_test.go` 24 个测试用例全绿；`go vet`/`go build ./...` 干净；memory 全家桶 autolearn/context/session 零回归）

**目标**：实现后台异步回顾器。监听 `OnLoopDone`，满足智能节流条件时，用本轮对话快照做**独立无状态 LLM 调用**（不回写主对话历史），解析"新建/覆盖"决策，通过 store 写文件刷索引。

**影响文件**：

- `src/internal/memory/autolearn/reviewer.go` — 新建，回顾器主体（落 autolearn 子包，与 prompt/store/types 同包，直接复用同包未导出的 reviewSystemPrompt / renderReviewUserPrompt / parseReviewDecisions）
- `src/internal/memory/autolearn/reviewer_test.go` — 新建

> 实现备注（架构分层与依赖决策）：
>
> - **落包位置**：tasks.md 原写 `src/internal/memory/reviewer.go`（包根），但 Task 1/4 已把记忆核心（types/store/prompt/sanitizer）统一落到平级子包 `autolearn/`，且 types.go 包注释明确「reviewer.go（Task 5）：后台异步回顾器」归本包。reviewer 需复用大量同包未导出符号（`reviewSystemPrompt` 常量、`renderReviewUserPrompt`/`parseReviewDecisions` 函数），落 autolearn 包最自然，故三文件统一落 `autolearn/`。
> - **不依赖 conversation 包（关键架构约束）**：autolearn 属记忆层（第 4 层），conversation 属引擎层（第 2 层）。若 reviewer 直接 import conversation 取 `AgentLoopResult`/`StopReason`，将构成「下层依赖上层」违规。故定义 autolearn 自有的 `ReviewRequest` 结构（SessionID/Completed/UserInput/FinalReply/ToolCallNames）作为解耦接口——节流判断用 `Completed bool` 而非 `StopReason` 枚举，由接入层（Task 7 handler，引擎/交互层）负责把 `conversation.AgentLoopResult` 适配为 `ReviewRequest`。autolearn 包仅依赖 `llm`（底座）与 `logger`（横切），架构合规。
> - **不持有 ConversationManager 引用**：reviewer 天然不回写主 history——它只持有 provider + store，独立构造回顾 messages 调 LLM，主对话历史对它不可见（设计即保证「不污染上下文」）。
> - **独立 ctx**：异步回顾用 `logger.WithSession(context.Background(), sessionID)` 派生（不随主请求 ctx 取消 + 日志仍路由到会话目录），叠加 `ReviewTimeout`（默认 60s）防 goroutine 泄漏。
> - **per-session 串行 + drop 策略**：`map[sessionID]struct{}` inflight 标记，同会话上一回顾未完成时新请求直接丢弃 + warn（首版简单策略，避免排队堆积）；落盘索引并发由 store.mu 兜底。
> - **测试可观测性**：暴露 `Wait()`（基于 WaitGroup）供测试同步等待异步回顾完成，做断言；生产代码不调用。

**依赖**：Task 1（store）、Task 4（prompt + 解析）

**具体内容**：

1. 实现 `Reviewer` 结构，持有 provider 引用、store 引用、配置。
2. **智能节流判断**：仅当 `AgentLoopResult.StopReason == "completed"` 且本轮用户输入非空且非纯闲聊（简单启发：用户输入长度/关键词过滤）时触发；`aborted` / `error` / `max_iterations` / `context_overflow` 直接跳过。
3. **构造本轮快照**：从本轮历史中提取用户输入 + 最终回复 + 工具调用名摘要（不含入参出参全文），读取当前两级 MEMORY.md 索引，填充 prompt 槽位。
4. **独立无状态 LLM 调用**：构造独立 `messages`（system=回顾 prompt，user=本轮快照），直接调用 provider 的无状态接口，**绝不调用 `ConversationManager.RunTurn/RunAgentLoop`**，确保不回写主 history。
5. 解析 LLM 结构化决策 → 对每条决策：`new` 则新建记忆文件 + 新增索引行；`update` 则覆盖已有文件 + 更新索引行简介；通过 store 落盘 + 刷新 MEMORY.md（原子写）。
6. **异步与隔离**：用独立 `context.Background()` 派生（不随主请求 ctx 取消，避免主请求结束就中断回顾）；goroutine 内 `defer recover` 兜底 panic；任何失败静默降级 + 结构化日志（sessionID / 触发原因 / 决策条数 / 失败原因）。
7. **per-session 串行**：用 mutex 或单飞机制保证同一会话的回顾请求串行，避免并发回顾互相覆盖索引；若上一个回顾进行中，新请求可排队或丢弃（首版选简单策略并注释）。
8. 单元测试覆盖：节流条件（completed 触发 / 异常终止跳过 / 空输入跳过）、不回写主 history（断言 history 长度不变）、new/update 两种决策落盘 + 索引刷新、LLM 失败静默降级、panic recover、并发串行。

**参考资料**：

- 触发钩子：`src/internal/engine/conversation/agent_loop.go`（`AgentLoopHooks.OnLoopDone`，约 `agent_loop.go:75`；`AgentLoopResult.StopReason`，约 `agent_loop.go:53`）
- 无状态 LLM 调用：`llm.Provider` 接口（`StreamChat` / 非会话式调用），参照 Step 7 摘要压缩的独立 LLM 调用方式
- 主 history 不可污染的语义参照 Step 4「LeadUserMessage 不进滑动窗口」隔离思路

---

## Task 6: 配置驱动（setting.json memory 段）

**状态**：已完成（`config.go` 新增 `MemoryConfig` + `MergeMemory`；`config_memory_test.go` 11 测试 + `memory_index_test.go` 4 个 Task 6 测试全绿；`go build`/`vet`/`test` config·sources·autolearn 三包干净；既有测试零回归）

**目标**：`setting.json` 新增 `memory` 配置段，支持整体开关、索引阈值覆盖等；多层配置合并（全局 + 项目级）沿用既有机制。

**影响文件**：

- `src/internal/config/config.go` — 修改，新增 `MemoryConfig` 结构 + 默认值 + `IsEnabled()` + `MergeMemory()` 合并函数
- `src/internal/engine/prompt/sources/memory_index.go` — 修改，`MemoryIndexSource` 接收 `MemoryIndexOptions`（可注入 maxLines/maxBytes + enabled 开关）替换硬编码常量
- `src/internal/memory/autolearn/reviewer.go` — 修改，`ReviewerConfig` 注释同步（enabled 短路 Task 5 已实现，config 映射点明确）
- `config/setting.example.json` — 修改，补充 `memory` 配置段示例

> 实现备注（落包与设计决策）：
>
> - **MemoryConfig 与 CompactionConfig 同构**：`Enabled *bool` 区分「未配置（→默认 true）」与「显式关闭（false）」，`applyMemoryDefaults` 沿用「==0 填默认」+ `*bool` 取址填充模式；`IsEnabled()` 统一访问，与 Step 7 完全一致。
> - **MergeMemory 字段级覆盖**：沿用 Step 5 `security.LoadPermissions`「项目级覆盖全局」语义，但 memory 为标量配置故做【字段级覆盖】（项目级显式项覆盖全局同名项，未配项沿用全局，全未配填默认）。关键契约：**必须传未 `applyMemoryDefaults` 的原始解析值**——否则项目级未配字段会被 setDefaults 填成默认值，误判为「显式配置」而错误覆盖全局显式值（如项目级未配 enabled 被填成默认 true，覆盖全局显式 false）。`TestMergeMemory_ProjectUnsetKeepsGlobalEnabledFalse` 守护此正确性。
> - **架构纯净度**：`autolearn` 包保持「仅依赖 llm + logger」，**不 import config**；`sources.MemoryIndexSource` 用基础类型 `MemoryIndexOptions` 接收阈值，同样不耦合 config 包。两处均由 Task 7 接入层（main.go）负责 `config.MemoryConfig → 组件配置` 适配。
> - **enabled=false 三层短路**：config 层 `IsEnabled()` 识别 → Source `Assemble` 首判 `!s.enabled` 返回空 → Reviewer `OnLoopDone` 首判 `!r.cfg.Enabled` 不触发；Task 7 main.go 把 `IsEnabled()` 同时映射到 Source `opts.Enabled` 与 `ReviewerConfig.Enabled`，wire 完整降级链路。
> - **main.go 接入留 Task 7**：本步骤严格不动 main.go（Task 6 影响文件不含 main.go）。当前 [main.go:175](../../src/main.go#L175) 的 `LoadPermissions(cfg, nil)` 项目级加载第二参数仍为 nil——项目级 setting.json 的启动加载缺口（permissions + memory 共用）统一在 Task 7「接入主流程」补齐，保持一致性。

**依赖**：Task 2、Task 5

**具体内容**：

1. 新增 `Memory` 配置结构：`enabled`（默认 true）、`index_max_lines`（默认 200）、`index_max_bytes`（默认 25KB）、`review_model`（预留字段，首版不启用热切换）。
2. 配置合并沿用 Step 5 多层合并机制（全局 `~/.codepilot/setting.json` + 项目级 `<cwd>/.codepilot/setting.json`）。
3. `enabled=false` 时：Source 注入短路返回空、Reviewer 不触发，整体降级为无记忆状态。
4. Source 与 Reviewer 从配置读取阈值替换硬编码常量。
5. 单元测试覆盖：默认值、配置覆盖、enabled=false 降级、多层合并优先级。

**参考资料**：

- 配置体系：`src/internal/config/`（参照现有 setting.json 结构）
- 多层合并：Step 5 全局 + 项目级 + 会话级合并机制（`src/internal/security/` permission 配置合并）

---

## Task 7: 接入主流程

**状态**：已完成（main.go 接线 + handler.go OnLoopDone 钩子 + 清理 Step 4 占位 memory.go；`go build ./...`/`go vet` 干净；`**go test ./...` 全量 22 包全绿、零失败**）

> 实现备注（接入细节与架构决策）：
>
> - **main.go**：抽取 `buildMemoryRoots(toolWorkdir) (userRoot, projectRoot)` 作为记忆路径【唯一计算入口】——`autolearn.Store` 落盘目录、`SandboxMiddleware` 附加只读根（Task 3）、`MemoryIndexSource` 读取来源、`Reviewer` 写入目标全部从此派生，保证「记忆实际目录/沙箱放行范围/注入来源」三者同源不漂移；原 `buildMemoryReadRoots` 改为基于它过滤空根。
> - **enabled=false 三层短路落地**：main.go 用 `cfg.Memory.IsEnabled()` 同时映射到 `MemoryIndexOptions.Enabled` 与 `ReviewerConfig.Enabled`，wire 起 Task 6 设计的「config.IsEnabled → Source Assemble 短路 → Reviewer OnLoopDone 短路」完整降级链路；两者无条件构造（即便关闭），由内部 Enabled 短路，避免到处加 nil 判断。
> - **占位清理**：删除 Step 4 的 `sources/memory.go`（RAG 预留 Recall-based `MemoryProvider`/`MemorySource`/`NoopMemoryProvider`），全仓无其它引用；`builder_test.go` 改用 `newTestMemoryIndexSource`（真实落盘 MEMORY.md）替代 `stubMemoryProvider`，并删除与新「静默降级」契约矛盾的 `TestBuilder_RealFourSources_MemoryProviderError`（新 Source 不再向 Builder 上抛错误，降级行为由 `memory_index_test.go` 覆盖）。
> - **handler.go 接入**：①新增 `reviewer *autolearn.Reviewer` 字段 + `SetReviewer` setter；②`runStream` 签名加 `userInput string`（从 `handleUserInput` 的 `p.Text` 透传），并在持锁快照 `sessionID` + `historyBefore`（`conv.MessageCount()`，用户消息已入 history）；③`OnLoopDone` 闭包内把 `conversation.AgentLoopResult` 适配为 `autolearn.ReviewRequest`——`Completed = result.StopReason == StopReasonCompleted`、`FinalReply = result.FinalText`、`ToolCallNames = h.collectTurnToolCallNames(historyBefore)`（去重保序仅工具名）；④新增 `collectTurnToolCallNames` 从本轮新增 history 提取 tool_use 名摘要（不含入参出参，控成本 + 防敏感）。
> - **架构合规**：handler（交互/引擎层）适配 `AgentLoopResult → ReviewRequest`，autolearn（记忆层）零依赖 conversation 包（Task 5 既定约束延续）；`runStream → OnLoopDone → Reviewer.OnLoopDone` 复用既有 AgentLoopHooks 透传通道（`fireLoopDone` @ agent_loop.go），不新增钩子链路，对既有增量保存逻辑零侵入（`saveCurrentSession` 调用保留）。

**目标**：将记忆系统整合到主项目入口——构造 memory 依赖、注册 memory Source 到 Builder、将 Reviewer 挂载到 `OnLoopDone` 钩子并接通智能节流。

**影响文件**：

- `src/main.go` — 修改，构造 store / source / reviewer，注册 Source，挂载钩子
- `src/internal/interaction/web/handler.go` — 修改，在会话 RunTurn/RunAgentLoop 装配 `OnLoopDone` 回调触发 Reviewer
- `src/internal/engine/conversation/manager.go` — 确认 `AgentLoopHooks` 透传链路

**依赖**：Task 2（Source）、Task 5（Reviewer）、Task 6（配置）

**具体内容**：

1. `main.go` 启动时：构造 `memory.Store`（用户级 + 项目级根）→ 构造 `memory.Source` → 注册到 `prompt.Builder`（紧跟 `agents_md` 之后）→ 构造 `memory.Reviewer`（注入 provider、store、配置）。
2. `handler.go` 在发起 RunTurn/RunAgentLoop 时，装配 `AgentLoopHooks.OnLoopDone`：调用 `Reviewer.OnLoopDone(result)`，后者内部做节流判断 + 异步派发。
3. 确认 `AgentLoopHooks` 从 handler → manager → agent_loop 的透传完整（`OnLoopDone` 在 `fireLoopDone` 处触发，见 `agent_loop.go:358`）。
4. 配置 `enabled=false` 时：不注册 Source、Reviewer 的 `OnLoopDone` 直接 return。
5. 确认 SP 可观测性（Step 4 状态栏 SP 区域）能展示 memory Source 的 token 小计（Stats 自动包含）。
6. 启动冒烟：程序正常启动、无 memory 目录时不报错、SP 注入含 `<memory_index>` 段。

**参考资料**：

- Source 注册：`src/main.go`（`NewAgentsMDSource` 注册处，追加 `memory.NewSource`）
- 钩子装配：`src/internal/interaction/web/handler.go`（RunTurn/RunAgentLoop 调用处）
- 钩子触发：`src/internal/engine/conversation/agent_loop.go`（`fireLoopDone` 约 `agent_loop.go:358`）
- SP Stats 自动包含：`src/internal/engine/prompt/builder.go`（`Assemble` 填充 Stats）

---

## Task 8: 端到端验证

**状态**：已完成（跨组件 e2e 5 用例 + handler 层 e2e 3 用例全绿；`go test ./...` 全量 22 包零回归；真实进程启动冒烟通过）

> 实现备注（落包调整）：tasks.md 原计划的 `src/internal/memory/e2e_test.go` 中 `memory` 包根目前没有任何 `.go` 源文件，Go 不允许目录仅存在 external test 文件（会编译报 no non-test Go files）。故跨组件 e2e 实际落 `src/internal/memory/autolearn/e2e_test.go`（`package autolearn_test`，external test package）——autolearn 是记忆系统核心实现包，从此处以外部使用者视角发起 reviewer + store + source + sandbox + ReadFile 的跨组件串联最自然，与 Task 4/5 的「落包修正」先例一致。handler 层 e2e 落 `src/internal/interaction/web/handler_memory_e2e_test.go`（复用既有 testRig/mockProvider）。checklist Task 8 同步记录该落包调整。

**目标**：端到端验证记忆系统的完整闭环——回顾触发 → 记忆生成/更新 → 索引刷新 → 跨会话召回注入 → 按需读取详情；并回归 Step 1~7 无破坏。

**影响文件**：

- `src/internal/memory/e2e_test.go` — 新建，跨组件 e2e
- `src/internal/interaction/web/handler_memory_e2e_test.go` — 新建，handler 层 e2e（真实 Server + WS + 真实 Reviewer）

**依赖**：Task 7

**具体内容**：

1. **回顾闭环 e2e**：构造一轮 completed 终止的对话 → Reviewer 异步触发 → 断言生成对应类型记忆文件 + MEMORY.md 索引行正确 → 断言主 history 未被污染。
2. **去重/更新 e2e**：先有一条"缩进 4 空格"偏好 → 再触发一轮"改为 2 空格"的回顾 → 断言覆盖同一文件而非新增、索引行简介更新、无冗余。
3. **跨会话召回 e2e**：上轮生成记忆后 → 新会话启动 → 断言 memory Source 注入的 LeadUserMessage 含对应索引行。
4. **按需读取 e2e**：LLM 经 ReadFile 读取用户级 memory 文件 → 断言沙箱放行 + 权限链路生效。
5. **节流 e2e**：aborted / 纯闲聊输入 → 断言 Reviewer 不触发、无记忆生成。
6. **异常降级 e2e**：回顾 LLM 返回非法 JSON / 文件写入失败 → 断言静默降级 + 日志、主流程不受影响。
7. **回归**：`go test ./...` 确认 Step 1~7 零回归（排除已知 Windows 平台 flaky）。
8. **真实启动冒烟**：启动程序 → 模拟一轮对话 → 观察 memory 目录生成文件 + MEMORY.md + 日志。

**参考资料**：

- e2e 模式参照 Step 6 / Step 7（真实 HTTP Server + WS + 真实组件）
- 真实启动冒烟参照 Step 6 `codepilot-e2e.exe` 模式

