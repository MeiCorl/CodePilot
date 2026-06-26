# Skill 系统 — CodePilot 实现原理

> 隶属 Step 10（Skill 系统）| 架构层:第 3 层 工具层 | 核心入口:`src/internal/skill/scanner.go`

## §1 模块定位

Skill 系统位于第 3 层 工具层,是「可插拔的工作流能力模块」—— 将复杂工作流(如代码审查、测试生成、配置管理)封装为可复用的 Skill,用户通过 `/<skill-name>` 触发,LLM 通过 `use_skill` 工具按需加载。

- **三档优先级目录**:`builtin / user / project`,后注册的覆盖先注册的(`project > user > builtin`)
- **SKILL.md 解析**:YAML frontmatter(`name / description / args / allowed-tools`)+ Markdown 正文
- **`use_skill` 工具**(`src/internal/skill/adapter/tool.go`)— LLM 按需调用,加载 Skill 完整内容到上下文
- **slash 命令注册**:Skill 加载后通过 `skilladapter.AsSlashCommand` 注册为 `/<name>` slash 命令
- **紫色徽标**:Category=`skill` 时前端在候选下拉加紫色「skill」标签,工具块头部加紫色「skill: <name>」徽标
- **`enabled=false` 三层降级**:`skill.enabled=false` 时 `/skills` 列表为空、`use_skill` 工具不可用、slash 命令不注册

## §2 核心数据结构

- `Skill`(`src/internal/skill/skill.go`)— 字段含 `Name / Description / Args / AllowedTools / Source / RootPath / body`
- `Frontmatter`(`src/internal/skill/loader/loader.go`)— YAML 头结构,字段 `Name / Description / Args / AllowedTools []string`
- `Source` 常量— `SourceProject(1) / SourceUser(2) / SourceBuiltin(3)`,数值越小优先级越高
- `Registry`(`src/internal/skill/registry.go`)— 内存合并注册表,字段 `byName map[string]*Skill / order []string / mu sync.RWMutex`
- `ErrSkillConflict`(registry.go)— 同级别同名冲突错误,含 `Name / ExistingSource`
- `LoadAll`(`src/internal/skill/scanner.go`)— 三档(内置 → 用户 → 项目)扫描与合并入口
- `LoadIssue`(scanner.go)— 加载期 issue(非 fatal),含 `Path / Err / Source`
- `scanOptions`(scanner.go)— 扫描选项,`SkipDuplicateSameSource: true` 避免重复条目
- `slash.SlashCommand`(`src/internal/command/slash/command.go`)— Step 9.1 命令接口,Skill 适配器实现
- `skillCmd`(`src/internal/skill/adapter/slash.go`)— Skill → SlashCommand 适配器
- `useSkillTool`(`src/internal/skill/adapter/tool.go`)— `use_skill` 工具实现,返回 `tool.Tool` 接口;`src/internal/skill/adapter/tool.go` 整体是 Step 10 `use_skill` 工具实现

## §3 关键流程

### 3.1 三档加载(`LoadAll`)

`LoadAll(workdir, homeDir, execDir, maxBytes) (*Registry, []LoadIssue, error)`(`scanner.go`)流程:

1. **内置级三段式 fallback**(Step 10.1 修复后):
   - 段 1:`scanEmbeddedBuiltins(reg, maxBytes, &issues)`(`scanner.go`)读 `//go:embed */SKILL.md` 的 `embeddedFS`
   - 段 2:`scanLevelWithOptions(filepath.Join(execDir, builtinRelPath), SourceBuiltin, maxBytes, ...)`(`scanner.go`)读 `<execDir>/internal/skill/builtin/`
   - 段 3:`findSrcBuiltinFallback(workdir)` 向上找 `src/internal/skill/builtin/`,找到后调 `scanLevelWithOptions(SkipDuplicateSameSource:true)`(`scanner.go`)
2. **用户级**:`scanLevel(reg, filepath.Join(homeDir, userSkillsDir), SourceUser, ...)`(`scanner.go`)读 `~/.codepilot/skills/`
3. **项目级**:`scanLevel(reg, filepath.Join(workdir, projectSkillsDir), SourceProject, ...)`(`scanner.go`)读 `<cwd>/.codepilot/skills/`
4. **可观测性**:三段 fallback 都没加载到任何内置 Skill 时 `logger.L().Warn(...)`(`scanner.go`)显式提示

[Why] 三段式 fallback:**Why** 老 binary 编译时 `//go:embed */SKILL.md` 是空(源码目录无 SKILL.md)+ dist 副本丢失 + 用户在项目根启动二进制 → 三段全失败。显式 warn 让用户能定位是「重新 `make build`」还是「项目布局异常」。

### 3.2 SKILL.md 解析

`loader.ParseFile(path) (*Skill, error)`(`src/internal/skill/loader/loader.go`):

1. `os.ReadFile(path)` 读文件
2. `splitFrontmatter(raw, path)`(`loader.go`)按 `---` 拆 YAML 头 + 正文;首行非 `---` 或未闭合均返回 `*ErrMissingFrontmatter`
3. `yaml.Unmarshal` 解析 frontmatter;YAML 错误返回 `*ErrYAML`(unwrap 后是 yaml.v3 原始错误)
4. `validateFrontmatter(fm, path)`(`loader.go`)校验 `name / description` 非空,缺失返回 `*ErrMissingField`
5. `skill.NewSkill(...)` 构造 `*Skill`(Source 默认填 SourceProject,scanner 按目录覆盖)

[Why] Source 默认填 SourceProject:**Why** loader 阶段不区分目录来源,scanner 负责在注册时按目录覆盖;loader 与 source 决策解耦便于测试。

### 3.3 Registry 合并规则

`Registry.Register(s *Skill) error`(`src/internal/skill/registry.go`)冲突规则:

- **未发现同名** → 正常注册,append 到 `order`
- **同名且同 Source** → 返回 `*ErrSkillConflict{Name, ExistingSource}`
- **同名且不同 Source** → 比较优先级数值:
  - `existing.Source < s.Source`(已注册的高优先级)→ 返回冲突错误
  - `existing.Source > s.Source`(新注册的高优先级,如 user→project)→ silent skip 覆盖

[Why] 不重排 order 在覆盖路径:**Why** List/ListBySource 按 order 渲染,若把 project 移到 user 之后会导致 SP 索引顺序漂移;保留 user 的位置只是让 user 的 Name 仍占一个 order 槽但 byName 已指向 project。

### 3.4 Skill → Slash 命令注册

`skilladapter.RegisterAll(r *slash.Registry, skills []*skill.Skill, h LeadMessageInjector)`(`src/internal/skill/adapter/slash.go`):

1. 遍历 `skills`,对每个 Skill 调 `AsSlashCommand(s, h)` 构造 `skillCmd`
2. `r.Register(cmd)` 把 slash 命令注册到 `slash.Registry`
3. 重复名错误收集到 `errs []error` 返回(不立即失败,让用户一次性看到所有冲突)

`skillCmd.Category() = "skill"`(`slash.go`),前端 `applySlashCompletion` 识别后:
- 候选下拉条目左侧加紫色「skill」标签
- 工具块头部加紫色「skill: <name>」徽标(`handler.go` 内部 tool_call_start 推送)

### 3.5 `use_skill` 工具按需加载

`useSkillTool.Execute(ctx, input) (string, error)`(`src/internal/skill/adapter/tool.go`)流程:

1. 解析入参 `{"skill_name": "<name>"}`
2. `t.registry.Get(name)` 查 Skill,未找到返回 `*ErrSkillNotFound`
3. 检查 `t.cfg.MaxSkillSizeBytes`(默认 64KB),超过则截断并打 warn
4. 返回 Skill 的 `FullContent()`(`body` + frontmatter 重新拼回)

[Why] 与 slash 路径不同,`use_skill` 走 `FullContent()`:**Why** slash 路径是用户手动触发,用 `Body()`(缓存版)避免每次 Execute 二次读盘;`use_skill` 是 LLM 按需加载,「最新内容」语义优先。

### 3.6 `enabled=false` 三层降级

main.go 装配时若 `cfg.Skill.Enabled = false`:

1. **加载层**:`LoadAll` 不被调用,`skill.Registry` 为 nil(后续 ListBySource 返回空)
2. **SP 层**:`SkillsIndexSource.Assemble`(`src/internal/skill/sources/skills_index.go`)检测 registry 为空,返回空 Section
3. **命令层**:`skilladapter.RegisterAll` 不被调用,slash 命令不出现
4. **WebUI 层**:`/skills` 模态框为空,候选下拉中无 Skill 条目

[Why] 不统一加 `if !enabled return` 散落判断:**Why** 各层独立降级更清晰;LoadAll 不调即「根本没有 Skill 数据」,上层各组件看到 nil/empty 自然降级。

## §4 与其他模块的依赖

- **上游**(Skill 模块依赖):
  - `internal/command/slash`(`src/internal/command/slash/`)— Slash 命令注册中心
  - `internal/interaction/web/handler`(`src/internal/interaction/web/handler.go`)— `skillProvider.ListBySource` 调用供 `/skills` 模态框
- **下游被依赖**:
  - `internal/engine/prompt/sources/skills_index.go`(`SkillsIndexSource`)— 把 Skill 列表注入 SP LeadUserMessage
  - `main.go`(`prompt.NewBuilder(...)`)— 装配 SkillsIndexSource + ConfigAwarenessSource + CodebaseAwarenessSource
  - `main.go`(`buildSkillReadRoots`)— 把 builtin/user/project 三档 skill 根目录作为 ReadFile 附加只读根注入(Step 10.2 Task 3 落地)

## §5 设计决策

### 决策 1:三档优先级目录 + Silent Skip 覆盖

- **问题**:多档 Skill 来源(builtin 不可改、user 个人偏好、project 项目约定)冲突时如何仲裁
- **方案**:`Source` 数值越小优先级越高;`Register` 时 silent skip 覆盖(silent skip 不报 error,只换 byName 指针)
- **理由**:**Why** silent skip 是「默认期望行为」而非「异常」——项目级覆盖用户级是 spec 明确约定;返回 error 反而误导用户去重命名 Skill

### 决策 2:SKILL.md YAML frontmatter + Markdown body

- **问题**:Skill 元数据(name / description / args / allowed-tools)+ 长正文(具体工作流指令)如何组织
- **方案**:YAML frontmatter(受控标量字段)+ Markdown 正文(无 YAML 转义)
- **理由**:**Why** frontmatter 受控字段可强校验(loader.validateFrontmatter);正文不进 YAML 避免转义 bug + 让人可直接编辑 md 文件

### 决策 3:`use_skill` 工具 + slash 命令双触发路径

- **问题**:Skill 需要让用户能手动触发(斜杠命令)+ LLM 能按需加载(工具)
- **方案**:Skill 加载后同时注册到 `slash.Registry` 与 `tool.Registry`(作为 `use_skill` 的可选项)
- **理由**:**Why** slash 命令适合「用户主动发起」;`use_skill` 适合「LLM 决策加载」;两路径独立不冲突

### 决策 4:紫色「skill」徽标(Category 标识)

- **问题**:Skill 触发的工具与内置工具在 UI 上如何区分
- **方案**:`Category = "skill"`,前端识别后加紫色徽标
- **理由**:**Why** 颜色是最高效的视觉区分;用户扫一眼就能识别「这条是 Skill 触发的」

### 决策 5:`enabled=false` 三层独立降级(无统一开关判断)

- **问题**:Skill 系统关闭时如何优雅降级
- **方案**:各层独立降级(LoadAll 不调 / SkillsIndexSource 返回空 / RegisterAll 不调),无散落的 `if !enabled` 判断
- **理由**:**Why** 各层独立降级更清晰;上层组件看到 nil/empty 自然降级,无需知道 Skill 是否启用

## §6 关键文件索引

| 路径 | 角色 |
|------|------|
| `src/internal/skill/skill.go` | `Skill` 数据结构定义 |
| `src/internal/skill/scanner.go` | `LoadAll` 三档扫描入口 |
| `src/internal/skill/scanner.go` | 内置级三段式 fallback |
| `src/internal/skill/scanner.go` | 启动期可观测性 warn |
| `src/internal/skill/loader/loader.go` | `ParseFile` SKILL.md 解析 |
| `src/internal/skill/loader/loader.go` | `splitFrontmatter` frontmatter 拆分 |
| `src/internal/skill/registry.go` | `Registry` 内存合并注册表 |
| `src/internal/skill/registry.go` | `Register` 冲突规则 |
| `src/internal/skill/sources/skills_index.go` | `SkillsIndexSource` SP 索引注入 |
| `src/internal/skill/adapter/tool.go` | `NewUseSkillTool` `use_skill` 工具 |
| `src/internal/skill/adapter/slash.go` | `skillCmd` Skill → Slash 适配 |
| `src/internal/skill/adapter/slash.go` | `Category = "skill"` 紫色徽标 |
| `src/internal/skill/adapter/slash.go` | `RegisterAll` 批量注册 |
| `src/internal/skill/adapter/client.go` | `SkillsListCmd` `/skills` client 类 |
| `src/internal/config/config.go` | `SkillConfig` 配置结构 |
| `src/internal/skill/builtin/codebase-overview/SKILL.md` | Step 10.2 总索引 |
| `src/main.go` | `prompt.NewBuilder` 装配 SkillsIndexSource 等 |