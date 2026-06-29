# System Prompt 设计 — CodePilot 实现原理

> 隶属 Step 4（System Prompt 设计）+ 10.1（配置自感知）+ 10.2（代码自感知）| 架构层:第 2 层 引擎层 | 核心入口:`src/internal/engine/prompt/builder.go`

## §1 模块定位

System Prompt(SP)设计位于第 2 层 引擎层,负责系统提示词的组装、模板渲染、动态注入,把多个 Source 各自产出的内容按 Placement 分组后拼成最终结构化 SP 供 LLM Provider 使用。

- **Builder 装配**(`builder.go`)— 注册多个 Source,按 Placement 分发到 SystemBlocks 或 LeadUserMessage
- **7 个 Source**(Step 4 + 10.1 + 10.2 累加):Static / Environment / AgentsMD / MemoryIndex / SkillsIndex / ConfigAwareness / CodebaseAwareness
- **AGENTS.md 双层合并**(`agents_md.go`)— 全局 `~/.codepilot/AGENTS.md` + 项目 `<cwd>/AGENTS.md`,按 H2 段落合并,项目级同名段覆盖全局
- **Anthropic Prompt Caching** — Placement=System 的段默认 `Cacheable=true`,在最后一个 cacheable 段打 `cache_control: ephemeral`
- **SP 可观测性 + Export** — `Builder.Assemble` 填充 `Stats []SourceStat` + `TotalTokens`,WebUI SP 面板展示;`/dump` 命令导出 dump.json + dump.md

## §2 核心数据结构

- `Source` interface(`src/internal/engine/prompt/sources/source.go`)— `Name() string + Assemble(ctx, env) (Section, error)`
- `Placement`(source.go)— `PlacementSystem / PlacementUserMessage`,决定 Section 进入 LLM 请求的哪个位置
- `Section`(source.go)— Source 产出的「一段」内容,字段 `Name / Content / Placement / Tokens`
- `SystemBlock`(source.go)— 进入 LLM 请求 system 字段的内容,字段 `Text / Cacheable`
- `SourceStat`(source.go)— 单个 Source 的 token 开销统计,字段 `Name / Tokens`
- `SystemPrompt`(source.go)— Builder.Assemble 最终产物,字段 `SystemBlocks / LeadUserMessage / Stats / TotalTokens`
- `Builder`(`builder.go`)— 装配器,字段 `sources []Source / enabled bool`,`enabled=false` 时 Assemble 短路返回零值
- `Env`(`template.Env`,source.go 别名)— Source 接收的输入环境参数,字段含 `OS / CWD / GitStatus / Date / Version / AgentsMDPath`
- `StaticSource`(`sources/static.go`)— 静态规则(CodePilot 自身角色、工具使用、风格规约等)
- `EnvironmentSource`(`sources/environment.go`)— 环境上下文(OS / CWD / Git 状态)
- `AgentsMDSource`(`sources/agents_md.go`)— AGENTS.md 双层合并
- `MemoryIndexSource`(`sources/memory_index.go`)— 记忆索引注入
- `SkillsIndexSource`(`src/internal/skill/sources/skills_index.go`)— Skill 列表索引注入
- `ConfigAwarenessSource`(`sources/config_awareness.go`)— Step 10.1 配置自感知(静态常量,无 IO)
- `CodebaseAwarenessSource`(`sources/codebase_awareness.go`)— Step 10.2 代码自感知(静态常量,无 IO)

## §3 关键流程

### 3.1 Builder 装配链(`NewBuilder`)

`prompt.NewBuilder(srcs ...Source) *Builder`(`builder.go`)按注册顺序追加,`main.go` 装配:

```go
prompt.NewBuilder(
    sources.NewStaticSource(),                              // 1
    sources.NewEnvironmentSource(),                         // 2
    sources.NewAgentsMDSource(),                            // 3
    sources.NewMemoryIndexSource(store, opts),              // 4
    skillSources.NewSkillsIndexSource(skillReg),            // 5
    sources.NewConfigAwarenessSource(),                     // 6 (Step 10.1)
    sources.NewCodebaseAwarenessSource(),                   // 7 (Step 10.2)
)
```

`Builder.Assemble(ctx, env) (SystemPrompt, error)`(`builder.go`)流程:

1. **disabled 短路**:`enabled=false` 直接返回零值 SystemPrompt(不开销)
2. **ctx 中途检查**:避免 Source 内长耗时操作阻塞取消信号
3. **顺序调用 Source.Assemble**:失败立即返回 wrap `source "X" 装配失败` 错误
4. **Placement 分发**:
   - `PlacementSystem` 且 Content 非空 → 追加到 `SystemBlocks`(`Cacheable=true`)
   - `PlacementUserMessage` 且 Content 非空 → 加入 `userParts` 切片
5. **合并 LeadUserMessage**:`strings.Join(userParts, "\n\n")`
6. **填充 Stats + TotalTokens**:即使 Section 为空也保留条目,便于 WebUI 区分「这个 Source 没启用」与「没注册这个 Source」

[Why] enabled=false 短路而非「注册空列表」:**Why** enabled=false 与「不注册 Source」语义不同——前者连 Sources 列表都不会读取(完全短路),后者会调用 0 个 Source 但 TotalTokens=0。诊断日志可据此快速识别关闭原因。

### 3.2 AGENTS.md 双层合并(`AgentsMDSource`)

`AgentsMDSource.Assemble`(`agents_md.go`)流程:

1. **resolvePaths**(agents_md.go):`globalPath = <home>/.codepilot/AGENTS.md`、`projectPath = <cwd>/AGENTS.md`
2. **loadFile** 双侧加载:任一缺失降级为空;任一文件超 64KB 时截断并 warn(不影响加载)
3. **按 H2 段落合并**(`mergeSections`):项目级同名段覆盖全局同名段,无 H2 时整段视作单一 unnamed 段
4. **renderSections + wrapProjectInstructions**:拼为 Markdown,外层包 `<project_instructions>` 标签
5. **模板变量替换**(`template.Render`):`{{VERSION}} / {{DATE}}` 等

Placement=UserMessage,因为 AGENTS.md 可能很长,塞进 System 字段会稀释注意力。

[Why] 按 H2 段落合并而非「文件全文拼接」:**Why** 段级粒度合并让用户能「项目级只覆盖某个 section 而保留其他 section」;全文拼接会让项目级完全替换全局,失去合并语义。

### 3.3 EnvironmentSource 环境采集

`EnvironmentSource.Assemble`(`environment.go`)流程:

1. 优先用 `env.OS`(handler 预采样);空时 `runtime.GOOS` 兜底
2. 优先用 `env.CWD`;空时现场 `collectCWD`(软链解析)
3. Git 状态:优先用 `env.GitStatus`;空时 `collectGitStatus(cwd)`(`environment.go`)
4. `collectGitStatus` 跑 `git rev-parse --is-inside-work-tree` + `rev-parse --abbrev-ref HEAD` + `status --porcelain` + `log -1 --oneline`,每条命令 1s 超时
5. 拼为 XML 风格结构化文本(environment.go):`<environment>OS / CWD / Git branch / Git status / Date / CodePilot version</environment>`

[Why] 任何错误降级为可读字符串:**Why** 环境信息缺失不应阻塞会话启动;Git 命令超时 1s 也只 warn 不影响主流程。

### 3.4 Anthropic Prompt Caching 触发

`Builder.Assemble` 中 `Placement=System` 的 Section 都标记 `Cacheable=true`(`builder.go`),Provider 在拼 system 字段时:

- `AnthropicProvider.buildAnthropicSystemText(blocks)`(anthropic.go)把 blocks 按 Cacheable 切片
- 在最后一个 `Cacheable=true` 的段打 `cache_control: {type: ephemeral}`
- Anthropic SDK 自动处理缓存命中与计费

[Why] 默认全部 Cacheable:**Why** System 字段的内容通常是稳定的(静态规则、环境上下文、自感知段),变化频率低;让 Provider 自行决定命中区,Source 不感知缓存细节

### 3.5 7 个 Source 总览

| # | Source | Placement | Token 量级 | 何时启用 |
|---|--------|-----------|-----------|---------|
| 1 | StaticSource | System | 2-4K | 总启用 |
| 2 | EnvironmentSource | System | 100-300 | 总启用 |
| 3 | AgentsMDSource | UserMessage | 0-2K | AGENTS.md 存在时 |
| 4 | MemoryIndexSource | UserMessage | 0-25K | `memory.enabled=true` 且 store 非 nil |
| 5 | SkillsIndexSource | UserMessage | 0-1K | `skill.enabled=true` 且 registry 非空 |
| 6 | ConfigAwarenessSource | System | ~75 | 总启用(纯静态常量) |
| 7 | CodebaseAwarenessSource | System | ~48 | 总启用(纯静态常量) |

[Why] ConfigAwareness / CodebaseAwareness 用纯静态常量(无 IO 无 env):**Why** 与 Step 10.1 范式对齐——零成本降级,即便 IO/env 全坏也能正常注入;`skill.enabled=false` 时仍生效,Agent 至少知道「Skill 不可用」。

## §4 与其他模块的依赖

- **上游**(SP 模块依赖):
  - `internal/memory/autolearn.Store`(MemoryIndexSource)— 读取两级记忆索引
  - `internal/skill.Registry`(SkillsIndexSource)— 读取 Skill 列表
  - `internal/engine/prompt/template`(`src/internal/engine/prompt/template/`)— 模板变量替换
  - `internal/engine/prompt/tokens`(`src/internal/engine/prompt/tokens/`)— token 估算
  - `internal/llm.Provider`(接收 SystemPrompt)— Anthropic / OpenAI 据此构造 system 字段
- **下游被依赖**:
  - `internal/interaction/web/handler`(`assembleSP` + WebUI SP 面板)— 每会话切换 / 恢复时重新组装
  - `main.go`(`prompt.NewBuilder(...)`)— 装配链入口
  - `/dump` 命令— 导出 dump.json + dump.md 含 SP 快照

## §5 设计决策

### 决策 1:Builder + Source 接口

- **问题**:如何让多个子系统(静态 / 环境 / AGENTS.md / 记忆 / Skill)各自产出一段 SP,而不互相耦合
- **方案**:`Source` interface + `Builder` 顺序装配 + Placement 分发
- **理由**:**Why** Source 是「单一职责」产出单元,Builder 是「聚合编排」;新增子系统只需实现 Source 接口并在 NewBuilder 注册,零改既有 Source

### 决策 2:Placement 枚举(System / UserMessage)

- **问题**:SP 内容有的是稳定指令(适合 cache),有的是动态上下文(适合每轮引用),混在一起会注意力稀释
- **方案**:`Placement=System`(稳定,可缓存)+ `PlacementUserMessage`(动态,合并为 LeadUserMessage)
- **理由**:**Why** Anthropic Prompt Caching 按 system 段标记;动态内容塞 UserMessage 位置便于多轮迭代引用;两条路径互不干扰

### 决策 3:AGENTS.md 按 H2 段级合并

- **问题**:全局 + 项目级 AGENTS.md 如何合并?全文拼接会让项目级完全替换全局
- **方案**:按 H2 段落合并,项目级同名段覆盖全局同名段
- **理由**:**Why** 段级粒度让用户能「项目级只覆盖某个 section 而保留其他 section」;这是 git config / vimrc 等成熟工具的合并范式

### 决策 4:Config/Codebase Awareness 用纯静态常量

- **问题**:自感知类 Source 要不要读文件 / 调 LLM 注入更详细的内容?
- **方案**:Step 10.1 + 10.2 用纯静态字符串常量,零 IO 零 env;详细 schema / 架构说明进 Skill 按需加载
- **理由**:**Why** SP token 增量 < 80 token(spec 非功能要求);纯静态保证「即便 IO/env 全坏也能注入」;详细说明按需加载避免一次性撑爆 SP

### 决策 5:disabled 短路而非「注册空列表」

- **问题**:`enabled=false` 时如何避免空跑所有 Source 的开销?
- **方案**:Builder 持 `enabled bool`,`enabled=false` 时 Assemble 立即返回零值 SystemPrompt
- **理由**:**Why** 与「不注册任何 Source」语义不同——前者连 Sources 列表都不会读取,后者 TotalTokens=0 但仍遍历;诊断日志能据此快速识别关闭原因

## §6 关键文件索引

| 路径 | 角色 |
|------|------|
| `src/internal/engine/prompt/builder.go` | `Builder` 装配器 |
| `src/internal/engine/prompt/builder.go` | `NewBuilder` 构造函数 |
| `src/internal/engine/prompt/builder.go` | `Assemble` 顺序调用 Source |
| `src/internal/engine/prompt/builder.go` | SystemBlock 标记 Cacheable=true |
| `src/internal/engine/prompt/sources/source.go` | `Placement` 枚举 |
| `src/internal/engine/prompt/sources/source.go` | `Section` Source 产出 |
| `src/internal/engine/prompt/sources/source.go` | `SystemPrompt` 最终产物 |
| `src/internal/engine/prompt/sources/source.go` | `Source` 接口定义 |
| `src/internal/engine/prompt/sources/static.go` | `StaticSource` 静态规则 |
| `src/internal/engine/prompt/sources/environment.go` | `EnvironmentSource` 环境上下文 |
| `src/internal/engine/prompt/sources/environment.go` | `collectGitStatus` Git 状态采集 |
| `src/internal/engine/prompt/sources/agents_md.go` | `AgentsMDSource` AGENTS.md 双层合并 |
| `src/internal/engine/prompt/sources/agents_md.go` | `resolvePaths` 计算两级路径 |
| `src/internal/engine/prompt/sources/memory_index.go` | `MemoryIndexSource` 记忆索引注入 |
| `src/internal/engine/prompt/sources/config_awareness.go` | `ConfigAwarenessSource` 配置自感知 |
| `src/internal/engine/prompt/sources/codebase_awareness.go` | `CodebaseAwarenessSource` 代码自感知 |
| `src/internal/skill/sources/skills_index.go` | `SkillsIndexSource` Skill 索引 |
| `src/main.go` | `prompt.NewBuilder(...)` 装配链入口 |