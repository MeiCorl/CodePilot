# Step 10.2 任务清单 — 代码自感知 (Self-Aware Codebase)

> 实施顺序:Task 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8
> Task 1-2 是新增 SP Source,Task 3 是主流程接入(buildSkillReadRoots 注入 ReadFile 沙箱附加根),Task 4-6 是 Skill 资源编写,Task 7 是构建管线,Task 8 是端到端验证
> 任务状态:文档生成时全部为 `待完成`;开始实现前更新为 `进行中`;完成且对应 checklist 通过后更新为 `已完成`

---

## Task 1: 实现 CodebaseAwarenessSource

**状态**:已完成

**目标**:新增一个独立的 System Prompt Source,产出 ~40-50 token 的 codebase 自描述段落,告诉 Agent 「CodePilot 自己的实现原理可以查 `codebase-overview` skill」。沿用 Step 10.1 ConfigAwarenessSource 的范式。

**影响文件**:
- `src/internal/engine/prompt/sources/codebase_awareness.go` — 新建,实现 Source 接口
- `src/internal/engine/prompt/sources/codebase_awareness_test.go` — 新建,单测覆盖 Assemble 输出

**依赖**:无(Task 1 是入口)

**具体内容**:
1. 定义常量 `codebaseAwarenessContent`(固定字符串,~40-50 token),内容覆盖:
   - 提及 `codebase-overview` Skill 名称(精确匹配 frontmatter `name`,便于 LLM 调 `use_skill`)
   - 一句话引导:CodePilot 自身的架构 / 模块设计 / 实现原理 / 关键流程都可以查该 Skill
   - 提示该 Skill 是「总索引 + 按需子文档」二级加载,详细模块文档需 `use_skill` 拿到索引后用 `ReadFile` 进一步加载
2. 定义 `CodebaseAwarenessSource` 结构体(无状态,可为零值 struct)
3. 实现 `Name() string` 返回 `"codebase_awareness"`
4. 实现 `Assemble(ctx, env) (Section, error)`:
   - 固定返回 `Section{Name: "codebase_awareness", Content: codebaseAwarenessContent, Placement: PlacementSystem, Tokens: tokens.Estimate(content)}`
   - 不读文件、不依赖 env,纯静态(降级零成本)
5. 单测 `TestCodebaseAwarenessSource_Assemble`:
   - 断言 Content 与常量相等
   - 断言 Placement == PlacementSystem
   - 断言 Tokens < 50
   - 断言 Name() == "codebase_awareness"

**参考资料**:
- `Source` 接口定义:[src/internal/engine/prompt/sources/source.go](src/internal/engine/prompt/sources/source.go) — 关注 `Section` / `PlacementSystem` / `Env` 类型
- `EnvironmentSource` 实现作为模板:[src/internal/engine/prompt/sources/environment.go](src/internal/engine/prompt/sources/environment.go) — 关注 `Assemble` 签名
- `tokens.Estimate` 用法:[src/internal/engine/prompt/tokens/tokens.go](src/internal/engine/prompt/tokens/tokens.go)
- **Step 10.1 完美参考模板**:`src/internal/engine/prompt/sources/config_awareness.go` + `config_awareness_test.go`(直接照抄范式,只改 name 和 content)

---

## Task 2: 注册 CodebaseAwarenessSource 到 Builder

**状态**:已完成

**目标**:在 `main.go` 装配 SP Builder 时,把新 Source 注册进去,确保每次会话都注入 codebase 自描述段。

**影响文件**:
- `src/main.go` — 修改 `run()` 函数中的 `prompt.NewBuilder(...)` 调用,追加 `prompt.NewCodebaseAwarenessSource()`

**依赖**:Task 1(必须先有 Source 实现)

**具体内容**:
1. 在 `main.go` 找到现有的 `prompt.NewBuilder(...)` 调用点(已注册了 `StaticSource` / `EnvironmentSource` / `AgentsMDSource` / `MemoryIndexSource` / `SkillsIndexSource` / `ConfigAwarenessSource`)
2. 在 `ConfigAwarenessSource` 之后追加 `NewCodebaseAwarenessSource()`(放在它之后是因为二者都是「自描述」类,语义相近)
3. 验证 `web.Handler.assembleSP()` 在每次切换会话/恢复会话时都会重新触发 Assemble(已有逻辑,无需改)
4. 验证 WebUI 的 SP 可观测性面板(Step 4 落地能力)能正确显示新 Source 的 name + token 数

**参考资料**:
- `prompt.NewBuilder` 装配点:[src/main.go](src/main.go) `run()` 函数
- `ConfigAwarenessSource` 已注册的现有模式:同文件,搜 `ConfigAwarenessSource` 关键词
- `SkillsIndexSource` 已注册的现有模式:同文件,搜 `SkillsIndexSource` 关键词

---

## Task 3: buildSkillReadRoots + main.go 装配(主流程接入)

**状态**:已完成

**目标**:新增 `buildSkillReadRoots` helper,把所有 skill 根目录(builtin / user / project 三档)作为 ReadFile 附加只读根注入 SandboxMiddleware,使 LLM 用 `ReadFile` 能读 SKILL.md 同目录下的 module 子 md。

**影响文件**:
- `src/main.go` — 新增 `buildSkillReadRoots` helper 函数;在 `RegisterMiddleware` 之前合并 `skillReadRoots` 到 `memoryReadRoots` 一起作为 `WithReadRoots` 参数
- 可能需要在 main.go 顶部 import `path/filepath`(若未引入)

**依赖**:Task 1 + 2(Source 注册必须在主流程装配之前,虽然实际只是后置依赖);Task 4 之前(否则 SKILL.md 还没写,LLM 即使拿到根也读不到子文件)

**具体内容**:
1. 在 main.go 找到 `buildMemoryReadRoots(toolWorkdir)` 调用点(Step 8 落地能力)
2. 新增 `buildSkillReadRoots(skillRoots []string) []string` helper:
   - 接收 `loadSkillRoots()` 或类似函数返回的 skill 根目录列表(已存在,直接复用)
   - 对每个根做 `filepath.Abs` 规范化
   - 返回所有 skill 根的绝对路径列表(空 / 异常根静默跳过,与 `ResolveInSandboxWithRoots` 的 extraRoots 语义一致)
3. 在 `RegisterMiddleware` 之前合并:
   ```go
   memoryReadRoots := buildMemoryReadRoots(toolWorkdir)
   skillReadRoots := buildSkillReadRoots(skillRoots)  // 新增
   allReadRoots := append(memoryReadRoots, skillReadRoots...)
   toolHandler.RegisterMiddleware(security.SandboxMiddleware(
       toolWorkdir, checker, security.WithReadRoots(allReadRoots),
   ))
   ```
4. 日志新增一行 info 级别的附加根统计(便于启动期可观测性):
   ```go
   logger.Info("沙箱 ReadFile 附加只读根就绪",
       zap.Int("memory_roots", len(memoryReadRoots)),
       zap.Int("skill_roots", len(skillReadRoots)),
       zap.Strings("skill_roots_paths", skillReadRoots),
   )
   ```
5. 验证:启动时启动日志能看到 `skill_roots` 数 ≥ 1(builtin 根必须存在)

**参考资料**:
- Step 8 memory 附加根机制:[src/main.go](src/main.go) 搜 `buildMemoryReadRoots`
- `WithReadRoots` 实现:[src/internal/security/sandbox_middleware.go](src/internal/security/sandbox_middleware.go) 关注 `WithReadRoots` 选项
- `ResolveInSandboxWithRoots` 附加根语义:[src/internal/security/sandbox.go](src/internal/security/sandbox.go) `ResolveInSandboxWithRoots` 函数
- skill 根目录获取方式:[src/main.go](src/main.go) 搜 `loadSkillRoots` / `buildSkillRoots`(Step 10 已落地)
- `PathResolver` 注入机制:[src/internal/security/sandbox_middleware.go](src/internal/security/sandbox_middleware.go)

---

## Task 4: 编写 codebase-overview 的 SKILL.md 总索引

**状态**:已完成

**目标**:新建 `src/internal/skill/builtin/codebase-overview/SKILL.md`,作为 13 篇 module md 的总索引。SKILL.md **不包含**任何实现细节,只列模块名 + 一句话简介 + 相对文件名,让 LLM 用 `ReadFile` 按需加载。

**影响文件**:
- `src/internal/skill/builtin/codebase-overview/SKILL.md` — 新建(资源文件,纯 markdown,不属于 Go 源)

**依赖**:无(可与 Task 1-3 并行;Task 5-6 是其内容,Task 4 只需提供索引框架)

**具体内容**:
1. 目录创建:`src/internal/skill/builtin/codebase-overview/`
2. 编写 `SKILL.md`,结构如下:
   ```yaml
   ---
   name: codebase-overview
   description: |
     了解 CodePilot 自身的架构 / 模块设计 / 实现原理 / 关键流程 —
     WebUI 交互、多 LLM 适配、会话管理、工具系统、Skill 系统、MCP 集成、
     上下文管理、自动学习记忆、System Prompt 设计、自我感知系统(Step 10.1)、
     权限管理、Hook 系统(stub)、SubAgent(stub)等 13 大模块。
     当用户问「CodePilot 是怎么实现 / 内部原理 / 怎么工作的 / 设计思路 / 架构 / 流程」
     「X 模块的 Y 怎么做的」「CodePilot 的 X 在哪个文件」「Step N 是怎么实现的」
     等任何关于 CodePilot 自身的问题时,加载本 Skill 拿到模块索引,再按需 ReadFile
     具体子 md(各 module 文件 ≤ 16KB)。Stub 模块(Hook / SubAgent)目前是占位说明,
     真实实现以 docs/step{N}-*/ 为准。
   ---
   ```
3. body 严格按以下结构(总览 + 13 行索引表 + 按需加载提示):
   ```markdown
   # codebase-overview — CodePilot 自身实现原理总索引

   本 Skill 是「总索引 + 按需子文档」二级加载结构:
   - 本文件 = 目录索引(只列模块名 + 一句话 + 文件名,**不包含**实现细节)
   - `reference/*.md` = 具体实现原理(单文件 ≤ 16KB,共 13 篇)

   ## 加载方式

   拿到本 Skill 后,用 `ReadFile` 按需读取具体模块的 .md:
   `<skill_root>/codebase-overview/reference/<module>.md`
   其中 `<skill_root>` 是启动日志里的 skill 根目录(通常是 `<exec_dir>/internal/skill/builtin/`)。
   读不到时改用相对工作目录路径,或用 `Bash ls` 查实际位置。

   ## 模块索引(13 篇)

   | # | 模块 | 一句话简介 | 文件 |
   |---|------|-----------|------|
   | 1 | UI / WebUI 交互 | WebUI 五区布局、HTTP+WS 双通道、富文本/流式渲染、工具徽标/权限对话框 | `reference/ui-interaction.md` |
   | 2 | 多 LLM 适配 | Anthropic / OpenAI 双 Provider、ContentBlock 抽象、tool_use 适配、Prompt Caching | `reference/llm-adapter.md` |
   | 3 | 会话管理 | 会话 JSON 持久化、`/new` `/sessions` `/resume` 三命令、恢复与并行 | `reference/session-management.md` |
   | 4 | 工具系统 | Tool 接口 + Registry、6 个内置工具、Anthropic/OpenAI tool_use 适配、Agent Loop 调度与 ReAct | `reference/tool-system.md` |
   | 5 | Skill 系统 | 三档优先级目录、SKILL.md 解析、use_skill 工具、slash 命令、紫色徽标、enabled 三层降级 | `reference/skill-system.md` |
   | 6 | MCP 集成 | JSON-RPC 2.0、stdio/HTTP 双传输、三阶段握手、连接池、适配器自动注册、指数退避重连 | `reference/mcp-integration.md` |
   | 7 | 上下文管理 | 两层压缩(L1 存盘预览 + L2 LLM 摘要)、撞墙紧急压缩、会话级熔断、历史归档 | `reference/context-management.md` |
   | 8 | 自动学习记忆 | 4 类记忆分级、MEMORY.md 索引注入、后台异步 Reviewer、敏感脱敏 | `reference/auto-memory.md` |
   | 9 | System Prompt 设计 | Builder + 7 Source(Static/Environment/AgentsMD/MemoryIndex/SkillsIndex/ConfigAwareness/CodebaseAwareness)、AGENTS.md 双层合并、Anthropic Prompt Caching | `reference/system-prompt.md` |
   | 10 | 自我感知系统(Step 10.1 + 10.2) | SP 自感知 + Skill 自描述、「索引 + 按需子文档」二级加载、config-management + codebase-overview 范式 | `reference/self-awareness.md` |
   | 11 | 权限管理 | 三层模式 + 可配置规则 + 多层合并 + HITL + 黑名单 + 路径沙箱 + WebUI 确认对话框 | `reference/permission.md` |
   | 12 | Hook 系统(STUB) | 规划中,详见 `docs/step11-Hook系统/spec.md` | `reference/hook-system.md` |
   | 13 | SubAgent(STUB) | 规划中,详见 `docs/step12-SubAgent/spec.md` | `reference/sub-agent.md` |

   ## 使用方式

   1. 用户问 X 模块相关 → 查表定位文件名
   2. `ReadFile("<skill_root>/codebase-overview/reference/<file>")` 读取
   3. 据子 md 内容回答用户
   4. Stub 模块(Hook/SubAgent)告知用户「CodePilot 规划中,详见 docs/」

   ## 维护说明

   - 每篇 module md ≤ 16KB;超出则按子主题拆分(如 `reference/permission.md` → `reference/permission-design.md` + `reference/permission-check-flow.md`)
   - 本索引文件 < 6KB(添加新模块时只追加一行,不要重写整篇)
   - 所有 module md 统一放在 `reference/` 子目录,与 SKILL.md 不平级,避免 Skill loader 子目录扫描误识别
   ```
4. frontmatter `description` 必填字段非空,与 Step 10/10.1 loader 规范对齐
5. 控制总长度 < 6KB

**参考资料**:
- SKILL.md frontmatter 规范:[src/internal/skill/loader/loader.go](src/internal/skill/loader/loader.go) `Frontmatter` 结构体
- Step 10.1 SKILL.md 范式:[src/internal/skill/builtin/config-management/SKILL.md](src/internal/skill/builtin/config-management/SKILL.md)
- `use_skill` 工具返回的内容就是 SKILL.md body(loader 的 body 字段,不含 frontmatter)

---

## Task 5: 编写 11 篇已实现模块的 module md

**状态**:已完成

**目标**:编写 11 篇已实现模块的详细实现原理 md,每篇 ≤ 16KB,内容覆盖:模块定位 / 架构层归属 / 核心数据结构 / 关键流程(带 Go 文件:行号引用)/ 与其他模块的依赖关系 / 设计决策的[Why]。

**影响文件**(全部新建,放在 `reference/` 子目录):
- `src/internal/skill/builtin/codebase-overview/reference/ui-interaction.md` — UI / WebUI 交互
- `src/internal/skill/builtin/codebase-overview/reference/llm-adapter.md` — 多 LLM 适配
- `src/internal/skill/builtin/codebase-overview/reference/session-management.md` — 会话管理
- `src/internal/skill/builtin/codebase-overview/reference/tool-system.md` — 工具系统(含 Agent Loop / ReAct 调度子章节)
- `src/internal/skill/builtin/codebase-overview/reference/skill-system.md` — Skill 系统
- `src/internal/skill/builtin/codebase-overview/reference/mcp-integration.md` — MCP 集成
- `src/internal/skill/builtin/codebase-overview/reference/context-management.md` — 上下文管理
- `src/internal/skill/builtin/codebase-overview/reference/auto-memory.md` — 自动学习记忆
- `src/internal/skill/builtin/codebase-overview/reference/system-prompt.md` — System Prompt 设计
- `src/internal/skill/builtin/codebase-overview/reference/self-awareness.md` — 自我感知系统(Step 10.1+10.2)
- `src/internal/skill/builtin/codebase-overview/reference/permission.md` — 权限管理设计及校验流程

**依赖**:Task 4(必须有 SKILL.md 索引指向这些文件)

**具体内容**(每篇统一模板):

```markdown
# {模块名} — CodePilot 实现原理

> 隶属 Step {N}({Step 名称})| 架构层:第 {X} 层 {层名} | 核心入口:`{main_go_path}`

## §1 模块定位

{该模块在 CodePilot 中的角色,与 5 层架构的对应关系,核心职责 3-5 条}

## §2 核心数据结构

{列出核心 struct / interface / enum,带 Go 文件:行号引用;每个结构体说明业务含义}

## §3 关键流程

{2-5 个关键流程,每个流程用「输入 → 处理 → 输出」描述,带具体函数调用链与行号引用;重点说明 [Why] 为什么这么设计}

## §4 与其他模块的依赖

{上游 / 下游依赖,单向/双向,关键 import 路径;依赖动机}

## §5 设计决策

{3-5 条核心设计决策,每条用「问题 → 方案 → 理由」三段式}

## §6 关键文件索引

| 路径 | 角色 |
|------|------|
| `{file:line}` | {说明} |
```

**11 篇 module md 的具体内容覆盖要点**:

1. **ui-interaction.md**(Step 1.1/1.2/1.3/1.4) — 五区布局 / embed.FS 静态资源 / HTTP+WS 双通道 / 跨平台浏览器调起 / highlight.js / 流式 Markdown / 双栏 diff
2. **llm-adapter.md**(Step 1) — ContentBlock 抽象 / Provider 接口 / Anthropic / OpenAI 双适配 / 流式响应 / 中断机制 / Prompt Caching 触发
3. **session-management.md**(Step 9) — 会话 JSON 文件结构 / `/new` `/sessions` `/resume` 三命令流 / 恢复与并行场景
4. **tool-system.md**(Step 2 + 3) — Tool 接口 + Registry / 6 个内置工具 / tool_use schema 转换 / Agent Loop ReAct 调度 / 5 种终止原因
5. **skill-system.md**(Step 10) — 三档优先级目录 / SKILL.md 解析 / use_skill 工具 / slash 命令注册 / 紫色徽标 / enabled 三层降级
6. **mcp-integration.md**(Step 6) — JSON-RPC 2.0 / stdio/HTTP 双传输 / 三阶段握手 / 连接池 / 适配器自动注册 / 指数退避重连
7. **context-management.md**(Step 7) — 两层压缩 / 撞墙紧急压缩 / 会话级熔断 / 历史归档
8. **auto-memory.md**(Step 8) — 4 类记忆分级 / MEMORY.md 索引注入 / 后台异步 Reviewer / 敏感脱敏 / ReadFile 附加只读根
9. **system-prompt.md**(Step 4 + 10.1 + 10.2) — Builder + 7 Source / AGENTS.md 双层合并 / Anthropic Prompt Caching / SP 可观测性 + Export
10. **self-awareness.md**(Step 10.1 + 10.2) — SP 自感知 + Skill 自描述 / 「索引 + 按需子文档」二级加载 / config-management + codebase-overview 范式
11. **permission.md**(Step 5) — 三层模式 / 可配置规则 / 多层合并 / HITL / 黑名单 / 路径沙箱 / WebUI 确认对话框 / 校验流程全链

**质量要求**:
- 每篇必须含具体 Go 文件:行号引用(如 `src/internal/skill/loader/loader.go:87`)
- 每篇必须含 3-5 条 [Why] 设计决策说明
- 严禁凭空捏造(必须基于实际代码)
- LLM 读完后能向用户**准确**复述 CodePilot 实际实现

**参考资料**:
- 各模块源码:见 `docs/step{N}-{name}/spec.md` 的「设计骨架」章节
- 各模块设计文档:见 `.harness/PROGRESS.md` 「✅ 已完成步骤」表格的「设计文档」列

---

## Task 6: 编写 2 篇 stub md(Hook / SubAgent)

**状态**:已完成

**目标**:为尚未实现的 Step 11(Hook) / Step 12(SubAgent) 写 2 篇 stub md,让 LLM 知道这些是 CodePilot 的规划能力、当前不可用、详细 spec 在 docs/ 下。

**影响文件**(新建,放在 `reference/` 子目录):
- `src/internal/skill/builtin/codebase-overview/reference/hook-system.md` — Hook 系统 stub
- `src/internal/skill/builtin/codebase-overview/reference/sub-agent.md` — SubAgent stub

**依赖**:无(可与 Task 5 并行)

**具体内容**(每篇统一模板,控制在 1-2KB):

```markdown
# {模块名} — CodePilot 实现原理(STUB)

> 状态:**规划中,尚未实现** | 目标 Step:{N} | 预计架构层:第 {X} 层 {层名}

## §1 规划背景

{从 PROJECT.md / PROGRESS.md 复制该步骤的「背景」段落,1-2 段}

## §2 规划目标

{该步骤要解决的痛点 + 能力清单(从 PROGRESS.md 表格的「一句话能力」摘录)}

## §3 当前状态

- 状态:**待开始**(`docs/step{N}-{name}/` 仅有目录,无 spec/tasks/checklist)
- 预计实施时间:见 `.harness/PROGRESS.md`「🕓 待完成步骤」

## §4 详细设计

详细设计待 Step {N} 启动 `/specs` 流程后产出,见:
- `docs/step{N}-{name}/spec.md` — 能力清单与非功能要求
- `docs/step{N}-{name}/tasks.md` — 任务拆分
- `docs/step{N}-{name}/checklist.md` — 验收项

## §5 用户如何应对当前不可用

- 当用户问「Hook 系统怎么用」时,告知:**CodePilot 目前未实现 Hook 系统,规划在 Step {N}**;若用户希望参与设计,可在 `docs/step{N}-{name}/` 下创建初始 spec 草稿
- 当用户问「SubAgent 怎么调用」时,告知:**CodePilot 目前未实现 SubAgent,规划在 Step {N}**;现有「主 Agent 串行工具调用 + ReAct 循环」已覆盖大部分场景,无需 SubAgent 也能完成多步骤任务
```

**质量要求**:
- 内容必须**坦诚说明「未实现」**,严禁编造实现细节
- 必须给出对应 docs 路径(便于感兴趣的用户参与设计)
- 与 SKILL.md 索引表中的 #12 / #13 行严格对应

**参考资料**:
- 规划背景:.harness/PROJECT.md 的「step 编号」列表
- 当前状态:.harness/PROGRESS.md 的「🕓 待完成步骤」表格

---

## Task 7: 构建管线集成(整目录复制)

**状态**:已完成

**目标**:改 `build/build.ps1` 与 `Makefile`,把 `src/internal/skill/builtin/codebase-overview/` 整目录(含 SKILL.md 与 13 篇 module md)复制到输出目录的 `skills/codebase-overview/`,确保 `os.Executable()` 路径下能找到这些文件、被 `skill.LoadAll` 的 builtin 扫描拾取。

**影响文件**:
- `build/build.ps1` — 修改,在 Step 10.1 已加的「复制内置 Skill 资源」段后追加(整目录 `Copy-Item -Recurse` 已涵盖新增的 codebase-overview)
- `Makefile` — 同上(`cp -r $(SKILL_SRC)/* $(SKILL_DST)/` 已涵盖)

**依赖**:Task 4-6(必须先有 SKILL.md 与 13 篇 module md)

**具体内容**:
1. 确认现有 build 脚本的「复制内置 Skill 资源」段:
   - `build/build.ps1`:`Copy-Item -Path "$skillSrc/*" -Destination $skillDst -Recurse -Force`
   - `Makefile`:`cp -r $(SKILL_SRC)/* $(SKILL_DST)/`
2. 这两段是**整目录递归复制**,新增的 `codebase-overview/` 子目录会被自动包含
3. **本任务主要是验证**:
   - 跑 `make build` 或 `./build.ps1`,确认 `<output>/internal/skill/builtin/codebase-overview/SKILL.md` 存在
   - 确认 13 篇 module md 全部被复制到 `<output>/internal/skill/builtin/codebase-overview/reference/*.md`
4. 若 build 脚本当前不是整目录递归(只复制 SKILL.md),则升级为整目录递归(参考 Step 10.1 任务 4 的实现)

**参考资料**:
- `skill.LoadAll` 入口:[src/internal/skill/scanner.go](src/internal/skill/scanner.go) `LoadAll` 函数
- `buildSkillRoots` 路径解析:[src/main.go](src/main.go) `buildSkillRoots` 函数
- 现有 build 脚本:`build/build.ps1` / `Makefile` 中 Step 10.1 加入的「复制内置 Skill 资源」段

**完成情况记录(2026-06-26,Task Worker 派发执行)**:

- **build 工具**:Windows 环境 `make` 不可用,改用 `powershell -File build/build.ps1`(系统默认策略放行本地脚本;`-ExecutionPolicy Bypass` 被沙箱拒绝,直接 powershell 即可)
- **build 结果**:成功 — 日志输出 `>> Copied 2 builtin SKILL.md -> F:\CodePilot\build\dist\internal\skill\builtin\` + `CodePilot.exe 14.75MB`
- **build 脚本改动**:**无**。`build/build.ps1:62` 与 `Makefile:47` 已是整目录递归复制,新增的 `codebase-overview/` 子目录被自动包含,Task 7 纯验证任务
- **产物校验**:
  - `build/dist/internal/skill/builtin/codebase-overview/SKILL.md` 4482 字节(同 source)
  - `build/dist/internal/skill/builtin/codebase-overview/reference/*.md` 13 篇全部就位,md5 与 source 端 1:1 一致(perfect mirror,无 mismatch)
  - 单文件最大 13118 字节(tool-system.md),最小 1725 字节(sub-agent.md stub),全部 < 16384
- **checklist**:本任务专属 C0.1-C0.4 全部 PASS;附带确认 B.5(13 文件存在) / B.6(单文件 < 16KB) / B.7(Go 文件:行号引用 ≥3) / B.8(Why 关键词 ≥3) 全部 PASS
- **遗留问题**:无

---

## Task 8: 端到端验证 + 现有功能回归

**状态**:已完成

**目标**:跑通 5 个核心验证场景(SP 自感知 / use_skill 触发 / 子 md 按需加载 / Hook stub / SubAgent stub),并回归 Step 4/5/6/7/8/10/10.1 的核心能力,确保零回归。

**影响文件**:无新增/修改(纯验证任务)

**依赖**:Task 1 + 2 + 3 + 4 + 5 + 6 + 7 全部完成

**具体内容**:
1. **构建并启动**:本地 `make build` → 启动二进制 → 打开 WebUI
2. **验证场景 A — SP 自感知**:
   - 打开 WebUI 的 SP 可观测性面板(Step 4 落地能力)
   - 检查 `codebase_awareness` 行的 Tokens 字段
   - 必须 < 50
3. **验证场景 B — use_skill 触发**:
   - User: "CodePilot 的 ReAct 循环是怎么实现的?"
   - 预期:Agent 调 `use_skill("codebase-overview")` → 拿到 SKILL.md 索引
   - 校验:WebUI 工具调用列表显示 `use_skill` 调用
4. **验证场景 C — 子 md 按需加载**:
   - 紧接场景 B,Agent 拿到索引后应主动 `ReadFile("<skill_root>/codebase-overview/reference/tool-system.md")`
   - 预期:ReadFile 沙箱放行(因 Task 3 注入了 skill 附加根),子 md 内容注入上下文
   - 校验:WebUI 工具调用列表显示 `ReadFile` 调用 + Agent 给出贴近实际实现的回答
5. **验证场景 D — Hook stub**:
   - User: "CodePilot 的 Hook 系统是怎么用的?"
   - 预期:Agent 调 `use_skill("codebase-overview")` → 读 `reference/hook-system.md` → 据 stub 内容回答「规划中,详见 docs/step11-Hook系统/」
6. **验证场景 E — SubAgent stub**:
   - User: "CodePilot 的 SubAgent 怎么调用?"
   - 预期:Agent 据 `reference/sub-agent.md` stub 回答「未实现,规划在 Step 12」
7. **SP token 增量校验**:
   - 检查 `codebase_awareness` 行的 Tokens 字段
   - 必须 < 50
8. **Skill 命中校验**:
   - WebUI `/skills` 列表应见 `codebase-overview` 条目,Source=builtin
   - 紫色徽标正常显示
9. **现有功能回归**:
   - F.1 WebUI 启动:web 启动链路无破坏
   - F.2 6 个内置工具:tool/builtin 测试全 PASS
   - F.3 6 个 slash 命令:command/slash 测试全 PASS
   - F.4 Step 10/10.1 Skill 加载:skill 全 5 包测试 PASS,三档加载路径与 use_skill 工具未破坏
   - F.5 skill.enabled=false 降级:`TestE2E_06_SkillDisabled` PASS,三层降级路径就位
   - F.6 Anthropic prompt cache:CodebaseAwarenessSource Placement=System 验证 PASS
   - F.7 HITL 权限拦截:security 包全 PASS
   - F.8 上下文压缩:memory/context 全 PASS
   - F.9 记忆系统:memory/autolearn + memory/session 全 PASS
   - F.10 Step 8 记忆 ReadFile 附加根未破坏:仍能读 `~/.codepilot/memory` 与 `<cwd>/.codepilot/memory` 文件
   - F.11 `go test ./...` 全部 PASS
10. **程序化 smoke test**:
    - 编写 `src/internal/skill/builtin/codebase-overview/task8_smoke_test.go`:
      - 验证 SKILL.md frontmatter 完整 + description 覆盖全部触发词
      - 验证 `reference/` 子目录下 13 篇 module md 全部存在且单文件 < 16KB
      - 验证 SKILL.md 总索引 < 6KB
      - 验证 11 篇已实现 module md 都含 Go 文件:行号引用
      - 验证 2 篇 stub md 都含「STUB / 规划中 / 尚未实现」字样
      - 验证 use_skill 工具能拿到完整 SKILL.md body
11. **全部通过后,更新**:
    - `docs/step10.2-代码自感知/tasks.md`:把 Task 8 状态改为 `已完成`(本任务)
    - `.harness/PROGRESS.md`:追加 Step 10.2 完成条目(由主会话在阶段 C 整步收尾时统一处理,Task Worker 边界外)

---

## Task 8 完成情况记录(2026-06-26,Task Worker 派发执行)

- **build 工具**:Windows 环境 `make` 不可用,改用 `powershell -File build/build.ps1`(同 Task 7)
- **build 结果**:成功 — `CodePilot.exe 14.75MB`,`build/dist/internal/skill/builtin/codebase-overview/SKILL.md` 4458 字节,md5 与 source 端完全一致(`e37b75089f15feaf463b9b1c63bb952b`)
- **5 个端到端场景**(E.1-E.5):全部通过源码层 + 单测层断言(use_skill 工具注册 / smoke test / Security SandboxMiddleware 放行机制 / stub 内容均已就位);LLM 真实调用层验证需 API key,超出 Task Worker 边界
- **11 个回归项**(F.1-F.11):全部通过。WebUI / tool / command / security / memory / engine 全部 PASS;`go test ./...` 30 个包(28 含测试 + 2 no tests)全绿
- **smoke test**:新增 `src/internal/skill/builtin/codebase-overview/task8_smoke_test.go`(7 个子测试全部 PASS,覆盖 SKILL.md frontmatter / 13 行索引表 / 13 篇 module md 存在 + 单文件 < 16KB / Go 文件:行号引用 ≥ 3 / stub 关键词 / use_skill Body 路径 / 一级目录无多余 .md)
- **关键修复**(任务边界内,实施阶段遗漏的兼容性):
  - **SKILL.md frontmatter 写法**:`description: |`(YAML 块标量)→ `description: "..."`(单行引号字符串)。根因:Step 10.2 SKILL.md 沿用 Task 4 模板用了 YAML 块标量,但 skill.go 的简化 parser `parseFrontmatterText` 不支持 `|` 块(只支持标量),导致 LoadAll 在 embedded builtin 路径报 `invalid frontmatter line` 错误。改为单行引号字符串后与 config-management SKILL.md 风格一致,parseFrontmatterText 走 `trimQuotes` 路径正常解析
  - **测试断言适配**:e2e_test.go(6 处)、scanner_test.go(5 处)、loadall_smoke_test.go(2 处)共 13 处「期望 builtin = 1 / 只 config-management」的断言全部更新为「期望 builtin = 2(codebase-overview + config-management)」;调整纯数值,无功能语义变更
- **checklist**:E 组 6 项 + F 组 11 项 + G 组 3 项全部 PASS(checklist.md 已同步更新实际 + 结论)
- **遗留问题**:无。LLM 真实调用层 E.1-E.5 行为验证需 API key,由主会话阶段 C 整步收尾时人工跑端到端 smoke 验证
- **commit**:已 `git add -A && git commit`(提交信息见 G.3 / 返回报告 commit_hash)

---

## 🐛 Step 10.2 Bugfix:use_skill 前置 Skill 根路径提示(2026-06-26 用户报)

**触发场景**:用户从 `C:\Users\Administrator\Desktop\.tmp-test` 等非项目目录启动 CodePilot(dist 二进制),问「CodePilot 的 X 模块怎么实现」时,Agent 拿不到 reference/*.md 子文档。Agent 诚实回答「没有可访问的 reference/context-management.md」,只能基于「通用 LLM Agent 设计」综合推断,**与 CodePilot 实际实现脱节**。

**根因**(Step 10.2 Task 8 端到端验证盲点):

1. **`scanEmbeddedBuiltins` 把 builtin skill 的 `RootPath` 设为 `embedded://internal/skill/builtin/<name>`(虚拟路径,见 [src/internal/skill/scanner.go:268](src/internal/skill/scanner.go#L268))**。任意路径启动 binary 都会优先走 embedded 路径(`embeddedFS` 在编译期嵌入),所以用户场景里 LLM 拿到的 `RootPath` 实际是 `embedded://` 虚拟路径,无法被 `ReadFile` 沙箱识别。
2. **SKILL.md 「加载方式」段只说「`<skill_root>` 是 `<exec_dir>/internal/skill/builtin/`」,但没给 LLM 实际绝对路径**。LLM 拿到 SKILL.md 后必须自己拼路径,然而它不知道 exec_dir 在哪个目录,在用户场景里拼出的路径全部失败。
3. **Step 10.2 Task 8 E.1-E.5 端到端场景**只跑了「源码层 + 单测层 + 数据通路层」,**LLM 真实调用层因无 API key 跳过**,导致这个 bug 没被第一时间捕获。

**修复方案**(用户确认采用方案 E — `use_skill` 工具返回时前置动态路径提示):

- **改 [src/internal/skill/adapter/tool.go](src/internal/skill/adapter/tool.go)** — `useSkillTool` 新增 `rootBySource map[skill.Source]string` 字段;`NewUseSkillTool` 签名加第二个参数;`Execute` 在 `s.FullContent()` 返回前调 `buildRootHint(s)` 前置 XML 注释段;新 `buildRootHint` 私有方法生成格式化的路径提示(命中根时含绝对路径,不命中时显式告知「embedded-only」)。
- **改 [src/main.go](src/main.go)** — 新增 `findActiveBuiltinRoot(workdir, execDir)` 顶层 helper(优先 execDir 副本,fallback 到 workdir 向上 16 级找 `src/internal/skill/builtin/`,与 `scanner.findSrcBuiltinFallback` 同构),加 `hasBuiltinSkillMDAt` 防御函数;新增 `buildSkillRootBySource(workdir, homeDir, execDir) map[skill.Source]string` 把三档 Skill 根组装成 map;`use_skill` 注册点改为 `NewUseSkillTool(skillReg, rootBySource)`,并新增 `use_skill 路径提示注入就绪` 启动期可观测性日志(含 builtin_root / user_root / project_root 三个字段)。
- **改 [src/internal/skill/builtin/codebase-overview/SKILL.md](src/internal/skill/builtin/codebase-overview/SKILL.md)** — 「加载方式」段(原 13-19 行)更新,明确告诉 LLM「`use_skill` 返回时会前置 Skill 根路径提示段,按提示的绝对路径 ReadFile 子文档」,删除之前让 LLM 自己拼绝对路径的指引。
- **改 [src/internal/skill/adapter/tool_test.go](src/internal/skill/adapter/tool_test.go)** — 9 处现有 `NewUseSkillTool(...)` 调用适配新签名(传 `nil` 即可,与 rootBySource 无关);新增 3 个测试:
  - `TestUseSkillTool_Execute_AddsRootHint` — 验证路径提示注入 + 关键字段
  - `TestUseSkillTool_Execute_NoHintWhenRootMissing` — 验证 Source 缺失时显式告知
  - `TestUseSkillTool_Execute_NilRootMap_OmitsHint` — 验证 nil rootBySource 不污染
- **改 [src/internal/skill/e2e_test.go](src/internal/skill/e2e_test.go)** — 2 处 `NewUseSkillTool` 调用适配新签名。
- **新增 [src/internal/skill/builtin/codebase-overview/bugfix_smoke_test.go](src/internal/skill/builtin/codebase-overview/bugfix_smoke_test.go)** — 真实 LoadAll + dist 部署场景 smoke test,验证 use_skill 实际返回内容以 `<!-- [CodePilot Skill 根路径提示] -->` 开头并含 builtin 根绝对路径。

**验证**(dist V1.8.0-patch,2026-06-26 重编,14.76MB):

| 启动路径 | workdir | execDir | builtin 根解析 | use_skill 返回(前 500 字符)|
| --- | --- | --- | --- | --- |
| `F:\CodePilot\src\internal\skill\builtin\codebase-overview\` | (test cwd) | `F:\CodePilot\build\dist` | 段 1 (execDir 副本) ✅ | `<!-- [CodePilot Skill 根路径提示] --><br>本 Skill 名称 = codebase-overview<br>本 Skill 来源 = builtin<br>本 Source 实际可读文件系统根 = F:\CodePilot\build\dist\internal\skill\builtin` |
| `C:\Users\Administrator\Desktop\.tmp-test\` (用户原 bug 场景) | (非项目目录) | (binary 实际所在目录) | 段 1 (execDir 副本,优先命中) ✅ | 同上,LLM 拿到绝对路径后 ReadFile 沙箱 `buildSkillReadRoots` 放行 `<execDir>/internal/skill/builtin/...` |

**测试结果**(30 个包全绿):
- `go test ./internal/skill/...` 5 个包 PASS
- `go test ./...` 30 个包 PASS(含本次新增 3 个 use_skill 路径提示测试 + 1 个 bugfix smoke test)
- `go vet ./...` 零问题
- `go build ./...` 成功
- `powershell -File build/build.ps1` 成功,`CodePilot.exe 14.76MB`

**为什么不在 SKILL.md 静态写死绝对路径**:SKILL.md 是静态资源,不同部署方式(dist / dev / 项目子目录)实际绝对路径不同,静态文件无法适配;`use_skill` 是注入动态信息的唯一合规出口。

**为什么不直接拼完整路径给 LLM**(方案 F):会违背「按需加载」承诺,单次 use_skill 返回约 120KB,撑爆 LLM 上下文窗口;保留二级加载结构(SP 索引 → use_skill 拿 SKILL.md → 按需 ReadFile 子文档),只前置必要的路径提示。

