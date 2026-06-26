# Step 10.2 — 代码自感知 (Self-Aware Codebase)

## 背景

Step 10.1 解决了 **配置自感知** —— Agent 知道自己有哪些配置项、配置怎么改。

但仍有**第二类自感知盲区** —— CodePilot **作为软件系统本身**的设计与实现原理,Agent 完全不知道。当用户问「CodePilot 是怎么实现 ReAct 循环的?」「MCP 客户端是 stdio 还是 HTTP?」「Skill 系统怎么发现 SKILL.md?」这类问题时,Agent 只能**靠通用 LLM 知识 + 猜测** 回答,容易给出与实际代码不符的答案。

**根本原因**:

1. **没有结构化「自描述」**：System Prompt 不含 CodePilot 自身的架构信息;没有任何 Skill 承担"描述 CodePilot 自身代码"的角色;
2. **缺乏按需加载机制**：即使写了完整架构说明,塞进 SP 会撑爆上下文;塞进单个 SKILL.md 又会让无关请求也加载全部内容;
3. **无法触达源码目录**：`src/internal/skill/builtin/` 等 Skill 资源目录在工作目录(cwd)之外,即使 Skill 索引指向具体子文件,LLM 用 `ReadFile` 也会被路径沙箱拦截。

**主流解法参考**：Claude Code 等成熟 Agent 的 `claude.md` 系统 —— 多文件结构化,LLM 按需 `ReadFile` 子模块文档。本步骤采用同一范式:Skill 内部也做「索引 + 按需子文档」分层。

## 目标用户

- CodePilot **所有用户**(零成本受益):当用户问「CodePilot 怎么做的」时,Agent 回答贴近实际代码而不是泛泛而谈
- **CodePilot 二次开发者**:需要快速理解各模块设计时可让 Agent 解释
- **AI 协作场景**:让 LLM 在做 CodePilot 相关的代码改动时,能基于实际实现给出建议

## 能力清单

1. **SP 自感知(Codebase Awareness)** — Agent 每次会话都能感知到「CodePilot 自己的实现原理可以查 `codebase-overview` skill」,SP 增量 < 50 token
2. **Skill 按需加载** — 用户问「CodePilot 是怎么实现 X 的」时,Agent 主动调 `use_skill("codebase-overview")` 拿到完整模块索引
3. **目录索引 + 按需子文档(二级按需加载)** — `codebase-overview/SKILL.md` 是「目录索引」,只列各模块文件名 + 一句话简介 + 相对路径;**不包含**任何实现细节。LLM 拿到索引后用 `ReadFile` 按需加载具体模块的子 md
4. **覆盖已实现 11 大模块** + **2 篇 stub** —— 覆盖 UI交互 / 多LLM适配 / 会话管理 / 工具系统 / Skill系统 / MCP集成 / 上下文管理 / 持久化记忆 / System Prompt设计 / 自我感知系统(Step 10.1)/ 权限管理 + 预留 Hook / SubAgent 2 篇 stub
5. **可触达沙箱外 Skill 资源目录** —— 复用 Step 8 `WithReadRoots` 机制,把 builtin/user/project 三档 skill 根目录作为 ReadFile 附加只读根,LLM 可读 SKILL.md 同目录下的子 md 文件
6. **现有功能零回归** —— WebUI、/skills 列表、Step 10/10.1 Skill 加载、Step 8 记忆 ReadFile 附加根、Anthropic prompt cache、`skill.enabled=false` 降级路径、Step 5 权限拦截全部不受影响
7. **不依赖 skill.enabled 开关的基础感知** —— 即使用户设 `skill.enabled=false`,SP 自描述段落仍生效(指向 Skill 不可用是已知降级,符合 Step 10/10.1 范式)

## 非功能要求

- **SP token 增量 < 50 token** —— codebase 自描述段必须精简;实现细节**全部进 Skill 子文档**,SKILL.md 只做目录索引(总索引 + 各模块一句话 + 文件名)
- **每篇 module md < 16KB** —— 单模块文档控制在 16KB 以内,避免 ReadFile 一次性返回过大;超出则按需拆分(如 `permission.md` 拆为 `permission-design.md` + `permission-check-flow.md`)
- **SKILL.md 总索引 < 6KB** —— 13 个模块 × 一句话简介 + 总览 ≈ 5KB
- **Skill 命中率高** —— frontmatter `description` 广覆盖「CodePilot 是怎么实现 / 内部原理 / 设计思路 / 工作机制 / 架构 / 流程」等所有相关问题模式,确保 LLM 在收到相关请求时高概率调 `use_skill`
- **零新依赖** —— 复用 Step 10/10.1 Skill 系统、Step 4 SP Builder、Step 8 记忆 ReadFile 附加根机制、Step 2 文件工具;不引入任何第三方包
- **遵循 5 层架构** —— 新增 `CodebaseAwarenessSource` 属第 2 层引擎层,新建的 `codebase-overview` Skill 属第 3 层工具层;`buildSkillReadRoots` helper 注入 main.go 装配链

## 设计骨架

```
src/internal/engine/prompt/sources/
  codebase_awareness.go                    ← 新增:CodebaseAwarenessSource(Source 接口,静态 ~40-50 token)

src/internal/skill/builtin/
  codebase-overview/
    SKILL.md                               ← 新建:总索引(只列模块名 + 一句话 + 文件名,~5KB)
    reference/                             ← 新建:子目录,放 13 篇 module md(避免与 SKILL.md 平级)
      ui-interaction.md                    ← 新建:UI/WebUI 交互实现原理
      llm-adapter.md                       ← 新建:多 LLM 适配实现原理
      session-management.md                ← 新建:会话管理实现原理
      tool-system.md                       ← 新建:工具系统实现原理
      skill-system.md                      ← 新建:Skill 系统实现原理
      mcp-integration.md                   ← 新建:MCP 集成实现原理
      context-management.md                ← 新建:上下文管理实现原理
      auto-memory.md                       ← 新建:持久化记忆实现原理
      system-prompt.md                     ← 新建:System Prompt 设计实现原理
      self-awareness.md                    ← 新建:自我感知系统(Step 10.1/10.2)实现原理
      permission.md                        ← 新建:权限管理设计及校验流程
      hook-system.md                       ← 新建:Hook 系统(stub,内容是「规划中,详见 docs/step11-Hook系统/」)
      sub-agent.md                         ← 新建:SubAgent(stub,内容是「规划中,详见 docs/step12-SubAgent/」)

src/main.go                                ← 修改:(1) 注册 CodebaseAwarenessSource 到 Builder (2) buildSkillReadRoots helper 把三档 skill 根目录作为 ReadFile 附加只读根注入
src/internal/engine/prompt/sources/...     ← 修改:可能需要 export NewCodebaseAwarenessSource 构造器

build/
  build.ps1                                ← 修改:确保 codebase-overview/ 整目录 SKILL.md 与 module md 都打到 exe-dir/skills/
  Makefile                                 ← 同上

docs/step10.2-代码自感知/
  spec.md                                  ← 本文档
  tasks.md                                 ← 任务清单
  checklist.md                             ← 验证清单

.harness/PROGRESS.md                       ← 步骤完成后追加新条目
```

**关键交互流**(用户问「CodePilot 是怎么实现 ReAct 循环的」):

```
User: CodePilot 的 ReAct 循环是怎么实现的?
  ↓
[SP 含 codebase_awareness 段,LLM 知道:CodePilot 自身实现 → 看 codebase-overview skill]
  ↓
Agent: use_skill("codebase-overview")
  ↓
[SKILL.md 注入上下文,内容是 13 个模块的目录索引]
  ↓
Agent 看到「Agent Loop / ReAct 循环」在 step3,索引指向 tool-system.md
  ↓
Agent: ReadFile("<skill_root>/codebase-overview/reference/tool-system.md")
  ↓
[Step 8 + 本步骤的附加只读根机制,ReadFile 沙箱放行]
  ↓
[module md 注入上下文,含 ReAct 循环的完整实现原理]
  ↓
Agent: 据此回答用户
```

**ReAct 循环归属决策**(本文档阶段确定,实施时按此):

- ReAct 循环是**第 2 层 引擎层**的核心,但实现上由第 3 层 Tool 调度,实际归类建议放 `tool-system.md` 的"Agent Loop 调度"章节(子章节,放最相关的层)
- 也可单独一篇 `agent-loop.md`,但粒度太细增加维护成本;**最终归在 `tool-system.md` 内的"§X Agent Loop 调度与 ReAct"子章节**

## Out of Scope(本步骤不做)

- **代码内嵌跳转链接 / 锚点解析** —— LLM 读子 md 后自行用 `Grep` 找相关 Go 源文件即可,不在 SKILL.md 内嵌 `path:line` 链接(会随重构失效)
- **多语言 SDK 适配** —— 本 Skill 只描述 CodePilot 自身,不论及 Anthropic/OpenAI SDK 内部实现(如有需要,文档会指向外部参考)
- **自动同步代码变更** —— module md 写完后不与 Go 源文件强绑定;代码改动后由开发者手动更新 module md(后续可加 CI 漂移检查,本步骤不做)
- **WebUI 面板渲染 module md** —— 本步骤不在 WebUI 加专门的"架构查看器";module md 只在 LLM 上下文里生效
- **跨会话记忆 module md** —— 每次会话按需加载,不做会话级缓存
- **Step 11/12 真实实现** —— 本步骤只写 2 篇 stub md,等 Step 11/12 落地后由对应步骤补充真实内容

## 与现有功能的关系

| 依赖 | 来源 | 关系 |
|------|------|------|
| `Source` 接口 + `Builder` 装配 | Step 4 | 新增一个 Source,注册到 Builder 链尾(在 `ConfigAwarenessSource` 后) |
| `Skill` 数据层 + `Registry` + `use_skill` 工具 | Step 10 | Skill 自动被 `LoadAll` 扫到,经 `use_skill` 加载 |
| 三档优先级目录(builtin/user/project) | Step 10 | 复用既有 `SourceBuiltin` 加载路径;**且**把三档根目录同时作为 ReadFile 附加只读根(新增) |
| `WithReadRoots` 附加只读根机制 | Step 8 | 复用 `buildMemoryReadRoots` 的同款模式,新增 `buildSkillReadRoots` helper |
| `ReadFile` 沙箱 | Step 2 | LLM 读子 md 的执行抓手;附加根放行后 LLM 可读 SKILL.md 同目录子文件 |
| `ConfigAwarenessSource` 范式 | Step 10.1 | 严格对齐:静态常量 + Source 接口 + Builder 装配链 |
| `Anthropic Prompt Caching` | Step 4 | SP 自描述段必须能进 cache 段(`Placement=System`) |
| 未来 Step 11/12 的 module md | Hook / SubAgent | 本步骤预留 2 篇 stub,后续步骤实施时填充真实内容 |
