# 自我感知系统 — CodePilot 实现原理

> 隶属 Step 10.1（配置自感知）+ Step 10.2（代码自感知）| 架构层:第 2 层 引擎层 + 第 3 层 工具层 | 核心入口:`src/internal/engine/prompt/sources/config_awareness.go` + `src/internal/skill/builtin/codebase-overview/SKILL.md`

## §1 模块定位

自我感知系统是 CodePilot 的「第二类自感知」—— 让 Agent 知道**自己作为软件系统**的设计与实现原理,当用户问「CodePilot 怎么做的」时,Agent 回答贴近实际代码而不是泛泛而谈。

- **SP 自感知** — `ConfigAwarenessSource`(~75 token,Step 10.1)+ `CodebaseAwarenessSource`(~48 token,Step 10.2)注入到 SP `codebase_awareness` / `config_awareness` 段
- **Skill 自描述** — `config-management` Skill(Step 10.1)+ `codebase-overview` Skill(Step 10.2),被 SP 自感知段指向
- **「索引 + 按需子文档」二级加载** — SKILL.md 是轻量目录索引;`config-management/reference/*.md` 保存配置细节,`codebase-overview/reference/*.md` 保存实现原理
- **可触达沙箱外 Skill 资源目录** — `buildSkillReadRoots`(Step 10.2 Task 3)把 builtin / user / project 三档 skill 根目录作为 ReadFile 附加只读根注入,LLM 可读 SKILL.md 同目录下的 module 子 md
- **`skill.enabled=false` 降级仍生效** — SP 自描述段是纯静态常量,即便 Skill 不可用 Agent 仍知道「Skill 不可用」

## §2 核心数据结构

- `ConfigAwarenessSource`(`src/internal/engine/prompt/sources/config_awareness.go`)— 配置自感知 Source,无状态 struct,固定返回 `configAwarenessContent` 常量
- `configAwarenessContent`(config_awareness.go)— 固定字符串:`~/.codepilot/setting.json + <cwd>/.codepilot/setting.json. ReadFile+EditFile/WriteFile; see skill "config-management".`
- `CodebaseAwarenessSource`(`src/internal/engine/prompt/sources/codebase_awareness.go`)— 代码自感知 Source,无状态 struct,固定返回 `codebaseAwarenessContent` 常量
- `codebaseAwarenessContent`(codebase_awareness.go)— 固定字符串:`<codebase_awareness>架构/实现:skill "codebase-overview";use_skill+ReadFile 子文档</codebase_awareness>`
- `Skill`(`src/internal/skill/skill.go`)— Skill 数据结构(详见 skill-system.md)
- `Frontmatter`(`src/internal/skill/loader/loader.go`)— SKILL.md YAML 头
- `codebase-overview/SKILL.md`(`src/internal/skill/builtin/codebase-overview/SKILL.md`)— 总索引文件(目录索引,不含实现细节)
- `codebase-overview/reference/*.md`— 14 篇 module md(13 已实现 + 1 stub)

## §3 关键流程

### 3.1 SP 自感知段注册

`main.go` 在 `prompt.NewBuilder(...)` 调用中按顺序追加:

```go
prompt.NewBuilder(
    sources.NewStaticSource(),                  // 1
    sources.NewEnvironmentSource(),             // 2
    sources.NewAgentsMDSource(),                // 3
    sources.NewMemoryIndexSource(...),          // 4
    skillSources.NewSkillsIndexSource(...),     // 5
    sources.NewConfigAwarenessSource(),         // 6 (Step 10.1)
    sources.NewCodebaseAwarenessSource(),       // 7 (Step 10.2)
)
```

ConfigAwarenessSource 与 CodebaseAwarenessSource 都放置在 Builder 链尾(其他 Source 之后),因为二者都是「自描述」类,语义相近。

[Why] 链尾而非链头:**Why** Builder 按顺序 Assemble,链尾保证这些自感知段作为「最后注入」的内容,被 LLM 视为「补充说明」而非「首要规则」。

### 3.2 「索引 + 按需子文档」二级加载

`config-management/SKILL.md` 与 `codebase-overview/SKILL.md` 都采用「总索引 + 按需子文档」二级加载结构。以下以 codebase-overview 为例:

1. **第一级(SKILL.md)**:目录索引,只列模块文件名 + 一句话简介,**不含**任何实现细节(< 6KB)
2. **第二级(reference/*.md)**:具体实现原理,单文件 ≤ 16KB,共 14 篇

LLM 调用流程:

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

[Why] 二级加载而非一级加载:**Why** module/reference md 总大小远超 64KB(单 Skill 截断上限);塞进 SKILL.md 会一次撑爆上下文,且无关请求也会加载全部内容。按需 ReadFile 让 LLM 只读相关模块。

### 3.3 沙箱外 Skill 资源目录可触达

`buildSkillReadRoots(skillWorkdir, skillHomeDir, skillExecDir)`(`src/main.go`)计算 Skill 附加只读根:

1. 三类根:`<workdir>/.codepilot/skills` + `<homeDir>/.codepilot/skills` + `<execDir>/internal/skill/builtin`
2. `filepath.Abs` 规范化 + 过滤空串
3. 合并到 `WithReadRoots(allReadRoots)` 参数,与 `buildMemoryReadRoots`(Step 8)合并

`SandboxMiddleware`(`src/internal/security/sandbox_middleware.go`)在 `perm == tool.PermRead && len(readRoots) > 0` 时调 `ResolveInSandboxWithRoots(pathStr, workdir, readRoots)`,把 Skill 根目录下的所有子文件视为合法可读范围。

[Why] 仅 PermRead 启用附加根:**Why** 「能读 SKILL.md」不等于「能写 SKILL.md」;写入仍走 workdir 沙箱,纵深防御。

### 3.4 `skill.enabled=false` 降级路径

`skill.enabled=false` 时:

- **SP 自感知段仍生效** — ConfigAwarenessSource / CodebaseAwarenessSource 是纯静态常量,无 IO 无 env,无降级路径
- **Skill 不可用** — `LoadAll` 不被调用,`skill.Registry` 为 nil
- **WebUI 提示** — `/skills` 列表为空,但 SP 自感知段仍在,Agent 知道「config-management / codebase-overview skill 不可用」

[Why] 自感知与 Skill 可用性解耦:**Why** 自感知段告诉 Agent「Skill 名称是什么、指向什么」;Skill 不可用时 Agent 至少知道「这个 Skill 存在但当前不可用」,可向用户解释或建议启用。

## §4 与其他模块的依赖

- **上游**(自我感知系统依赖):
  - `internal/skill.Registry`(config-management / codebase-overview 两个 Skill 被自动加载)
  - `internal/security.SandboxMiddleware`(附加只读根机制复用)
  - `internal/engine/prompt/sources/source.Source` 接口(自感知 Source 实现)
- **下游被依赖**:
  - `internal/engine/prompt.Builder`(装配链注册)
  - `internal/interaction/web/handler`(SP 可观测性面板展示自感知段 token 数)
  - LLM Agent(收到 SP 后调 `use_skill` + `ReadFile`)

## §5 设计决策

### 决策 1:SP 自感知段与 Skill 自描述解耦

- **问题**:Agent 需知道「CodePilot 自己的实现原理在哪里」;但 SP token 不能太多
- **方案**:SP 注入 ~50 token 的「指向 Skill」自描述段,详细 schema / 架构说明进 Skill 按需加载
- **理由**:**Why** 二级加载把 SP token 增量控制到 < 50 token(spec 非功能要求);Skill 是「按需加载」天然契合渐进式披露

### 决策 2:config-management 与 codebase-overview 同范式

- **问题**:两类自感知(配置 vs 代码)实现方式是否要差异化?
- **方案**:Step 10.1 / 10.2 严格对齐——静态常量 + 零值 struct + 纯静态 Assemble + Skill 名字精确匹配
- **理由**:**Why** 同范式降低理解成本;新增「第三类自感知」只需照搬范式

### 决策 3:「索引 + 按需子文档」二级加载

- **问题**:13 篇 module md 总大小远超单 Skill 上限;塞进 SKILL.md 会撑爆上下文
- **方案**:SKILL.md 只做目录索引(< 6KB),详细说明放 `reference/*.md`(单文件 ≤ 16KB),LLM 按需 ReadFile
- **理由**:**Why** 这是 Claude Code 等成熟 Agent 的 `claude.md` 范式;二级加载 + 渐进式披露把 token 消耗降到最小

### 决策 4:`skill.enabled=false` 降级仍生效

- **问题**:用户禁用 Skill 系统时,自感知段是否仍生效?
- **方案**:ConfigAwareness / CodebaseAwareness Source 是纯静态,无 IO 无 env;`skill.enabled=false` 时仍注入 SP
- **理由**:**Why** 自感知是「告诉 Agent Skill 存在」,与「Skill 当前可用」是两个独立维度;禁用 Skill 时 Agent 至少知道「这个 Skill 名是什么」可向用户解释

### 决策 5:沙箱外 Skill 资源目录仅 PermRead 放行

- **问题**:LLM 需要读 SKILL.md 同目录子 md,但不能让 WriteFile 直接写 Skill 目录
- **方案**:`SandboxMiddleware` 仅在 `perm == tool.PermRead` 时启用附加只读根;PermWrite / PermExec 仍仅认 workdir
- **理由**:**Why** 「能读 Skill 资源」不等于「能写 Skill 资源」;纵深防御防止 Skill 目录被 WriteFile 篡改

## §6 关键文件索引

| 路径 | 角色 |
|------|------|
| `src/internal/engine/prompt/sources/config_awareness.go` | `configAwarenessContent` 配置自感知常量 |
| `src/internal/engine/prompt/sources/config_awareness.go` | `ConfigAwarenessSource` 配置自感知 Source |
| `src/internal/engine/prompt/sources/codebase_awareness.go` | `codebaseAwarenessContent` 代码自感知常量 |
| `src/internal/engine/prompt/sources/codebase_awareness.go` | `CodebaseAwarenessSource` 代码自感知 Source |
| `src/internal/skill/builtin/codebase-overview/SKILL.md` | 总索引(目录索引,不含实现细节) |
| `src/internal/skill/builtin/codebase-overview/reference/*.md` | 14 篇 module md(13 已实现 + 1 stub) |
| `src/internal/skill/builtin/config-management/SKILL.md` | 配置管理总索引(Step 10.1) |
| `src/internal/skill/builtin/config-management/reference/*.md` | setting.json 各配置域细节 |
| `src/main.go` | `buildSkillReadRoots` Skill 附加只读根 |
| `src/main.go` | `prompt.NewBuilder` 装配自感知 Source |
| `src/internal/security/sandbox.go` | `ResolveInSandboxWithRoots` 附加只读根机制 |
| `src/internal/security/sandbox_middleware.go` | `SandboxMiddleware` 沙箱中间件 |
| `src/internal/security/sandbox_middleware.go` | PermRead 时启用附加根 |