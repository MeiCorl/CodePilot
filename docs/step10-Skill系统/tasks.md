# Step 10 — Tasks

> 本步骤为 CodePilot 实现完整的 Skill 系统(项目级 / 用户级 / 内置级三档优先级),自动注册为 slash 命令,LLM 渐进式披露(name+description 索引 + use_skill 工具按需加载完整内容),UI 上与现有工具调用视觉一致(带紫色 skill 徽标)。

## Task 1: Skill 类型 + SKILL.md 解析器

**状态**: 已完成
**目标**: 定义 Skill 数据结构与 SKILL.md 文件格式解析能力(YAML frontmatter + markdown 正文),为后续扫描器与适配器提供基础。

**影响文件**:
- `src/internal/skill/doc.go` — 新建,包级文档
- `src/internal/skill/skill.go` — 新建,Skill 类型 + Source 枚举 + FullContent 方法
- `src/internal/skill/loader/loader.go` — 新建,YAML frontmatter 解析 + markdown 加载
- `src/internal/skill/loader/loader_test.go` — 新建,单测

**依赖**: 无

**具体内容**:
1. `src/internal/skill/skill.go` 定义:
   - `type Source int` 枚举 `SourceProject=1` / `SourceUser=2` / `SourceBuiltin=3`,带 `String() string` 方法(返回 `"project"` / `"user"` / `"builtin"`,供 SP 索引与 `/skills` 展示)
   - `type Skill struct { Name string; Description string; Args string; AllowedTools []string; Source Source; RootPath string; body string }`(`body` 私有,首字母小写,通过 `Body() string` 方法访问)
   - `func (s *Skill) FullContent() (string, error)` 方法:按 `RootPath/SKILL.md` 完整读文件,frontmatter 段重组成 `# Skill: <name>\n\n> <description>\n\n<args hint>\n\n`,正文紧随其后;读失败返回 error
   - `func (s *Skill) Body() string` 方法:返回 SKILL.md 完整正文(含 frontmatter 重新组装为 markdown 标题),供 `use_skill` 工具的 tool_result 与 slash 命令的 LeadUserMessage 使用
2. `src/internal/skill/loader/loader.go` 实现:
   - `type Frontmatter struct { Name string \`yaml:"name"\`; Description string \`yaml:"description"\`; Args string \`yaml:"args,omitempty"\`; AllowedTools []string \`yaml:"allowed-tools,omitempty"\` }`
   - `func ParseFile(path string) (*skill.Skill, error)`:读文件 → 解析 frontmatter(`---\n...\n---\n` 包裹的 YAML 段)→ 校验 `name` 与 `description` 必填 → 构造 `Skill{Source: SourceProject, RootPath: filepath.Dir(path), body: rawMarkdown}`
   - 解析错误时返回带文件路径的 error(供调用方定位)
3. `doc.go` 写包级注释,说明本包是 Skill 系统的"基础数据层",只关心 SKILL.md 格式与解析,不涉及 Registry / 适配器
4. `loader_test.go` 覆盖:合法 SKILL.md / 缺 name / 缺 description / 缺 frontmatter / YAML 错误 / 不存在的文件 / body 大小超过 maxBytes(64KB)截断 + warning

**参考资料**:
- `gopkg.in/yaml.v3` 当前 API(Find out via context7)
- `src/internal/memory/store.go` 现有 frontmatter 解析风格参考(若有)
- spec.md §A.3 SKILL.md 解析规则

---

## Task 2: Skill Scanner + Registry(三档合并 + 冲突规则)

**状态**: 已完成
**目标**: 实现项目级 / 用户级 / 内置级三档 Skill 目录扫描、解析、合并注册,严格按 spec.md §A.4 规则处理冲突(项目级覆盖用户级;同级别同名报错)。

**影响文件**:
- `src/internal/skill/scanner.go` — 新建,目录扫描与三档装配
- `src/internal/skill/registry.go` — 新建,合并注册表
- `src/internal/skill/builtin/builtin.go` — 新建,内置 Skill 目录扫描(本步骤始终返回空)
- `src/internal/skill/builtin/README.md` — 新建,占位说明
- `src/internal/skill/scanner_test.go` — 新建,单测覆盖三档冲突规则
- `src/internal/skill/registry_test.go` — 新建,单测覆盖 List/Get/AllSources

**依赖**: Task 1

**具体内容**:
1. `registry.go`:
   - `type Registry struct { mu sync.RWMutex; byName map[string]*Skill; order []string }`
   - `func (r *Registry) Register(s *Skill) error`:若 `byName` 已有同名 Skill 且来源级别 ≥ 待注册(项目级 ≥ 用户级 ≥ 内置级),则**跳过**并返回 nil(项目级覆盖用户级时,用户级 silent skip);若同级别同名(都是项目级或都是用户级)返回 `*ErrSkillConflict{Name, ExistingSource}` 错误(由 main.go 启动期检查决定是否 panic)
   - `func (r *Registry) Get(name string) (*Skill, bool)`:按 name 查找
   - `func (r *Registry) List() []*Skill`:按注册顺序返回(项目级 → 用户级 → 内置级)
   - `func (r *Registry) ListBySource(src Source) []*Skill`:按来源分组,供 `SkillsIndexSource` 与 `/skills` 面板使用
   - `func (r *Registry) Count() int`
2. `builtin/builtin.go`:
   - `func ScanBuiltin(execDir string) ([]*skill.Skill, error)`:扫描 `<execDir>/internal/skill/builtin/`,对每个子目录尝试 `loader.ParseFile`,失败 warn 跳过;本步骤此目录只有 `README.md` 占位,实际扫描结果为空切片
3. `scanner.go`:
   - `func LoadAll(workdir, homeDir, execDir string, maxBytes int) (*skill.Registry, []LoadIssue, error)`:执行三档扫描
     1. 先扫内置级(`ScanBuiltin`),按注册顺序调 `Register`,捕获 `ErrSkillConflict`(本步骤无内置,理论不会冲突)
     2. 再扫用户级(`filepath.Join(homeDir, ".codepilot", "skill")`),每个子目录 → `loader.ParseFile` → `Register`
     3. 最后扫项目级(`filepath.Join(workdir, ".codepilot", "skills")`),每个子目录 → `loader.ParseFile` → `Register`(项目级会 silent 覆盖用户级同名)
     4. 任一目录不存在 → 静默跳过(不报错)
     5. 同级别同名 → 记录到 `LoadIssue` 切片 + 立即返回 error(main.go 启动期决定处理策略)
   - `type LoadIssue struct { Path string; Err error; Source skill.Source }`:供上层日志展示
4. `scanner_test.go` 覆盖:
   - 三档独立加载(无冲突)
   - 项目级覆盖用户级同名(用户级 silent skip,不进 registry)
   - 两个项目级同名 → 返回 error
   - 两个用户级同名 → 返回 error
   - 目录不存在 → 静默跳过 + 空 registry
   - 单个 SKILL.md 解析失败(缺 name)→ warn 跳过,其他 Skill 正常加载
5. `registry_test.go` 覆盖:Register / Get / List / ListBySource / Count / 同名覆盖规则

**参考资料**:
- spec.md §A.4 冲突规则
- spec.md §A.5 加载失败隔离
- `src/internal/command/slash/command.go` Registry 设计风格参考
- `src/internal/memory/store.go` 多源存储风格参考

---

## Task 3: use_skill 工具实现

**状态**: 已完成
**目标**: 实现 LLM 主动调用的 `use_skill` 工具,Input schema 为 `{skill_name}`,执行时读取完整 SKILL.md 内容作为 tool_result 返回,UI 上走现有 tool_call_start/end 事件流(带紫色 skill 徽标在 Task 6 完成)。

**影响文件**:
- `src/internal/skill/adapter/tool.go` — 新建,use_skill 工具实现
- `src/internal/skill/adapter/tool_test.go` — 新建,单测

**依赖**: Task 1, Task 2

**具体内容**:
1. `adapter/tool.go`:
   - `type useSkillTool struct { registry *skill.Registry }`
   - 实现 `tool.Tool` 接口:
     - `Name()` 返回 `"use_skill"`
     - `Description()` 返回 `"按需加载 Skill 的完整内容到上下文中。Input: skill_name(Skill 名称,来自 /skills 列表或 system prompt 中的 Skill 索引段)"`
     - `InputSchema()` 返回 JSON Schema:`{ "type": "object", "properties": { "skill_name": { "type": "string", "description": "要加载的 Skill 名称" } }, "required": ["skill_name"] }`
     - `Execute(ctx, input) (string, error)`:从 input 解析 `skill_name` → `registry.Get(name)` → 若找到,调 `skill.FullContent()` 返回字符串;若找不到,返回 `("", fmt.Errorf("skill not found: %s", name))` 让 ToolHandler 包装为 `ToolResultBlock{IsError: true}`
   - `func NewUseSkillTool(r *skill.Registry) tool.Tool { return &useSkillTool{registry: r} }`
2. `adapter/tool_test.go` 覆盖:
   - 加载已注册 Skill → 完整内容返回
   - 不存在的 skill_name → 错误返回(由 ToolHandler 包装为 IsError=true)
   - 空 skill_name → 错误返回
   - ctx 取消 → 立刻返回 ctx.Err()
3. 工具注册时机:本任务只实现工具,实际注册到 `tool.Registry` 在 Task 7 main.go 装配时完成

**参考资料**:
- `src/internal/tool/builtin/read_file.go` 工具实现风格(返回值约定)
- `src/internal/tool/builtin/grep.go` 工具 InputSchema 风格参考
- `src/internal/tool/types.go` Tool 接口签名
- spec.md §D `use_skill` 工具规则

---

## Task 4: Slash 命令适配器(Skill → SlashCommand)

**状态**: 已完成
**目标**: 把每个加载成功的 Skill 自动注册为 slash 命令,`/<skill-name>`(无参)触发 → 把 Skill 完整内容作为 LeadUserMessage 追加;`/<skill-name> <args>` 触发 → 同上 + 末尾追加 `<user_args>` 段。Category 固定为 "skill"。

**影响文件**:
- `src/internal/skill/adapter/slash.go` — 新建,Skill → SlashCommand 适配器
- `src/internal/skill/adapter/slash_test.go` — 新建,单测

**依赖**: Task 1, Task 2

**具体内容**:
1. `adapter/slash.go`:
   - `type skillCmd struct { skill *skill.Skill; h LeadMessageInjector }`:`h` 是 handler 暴露的"追加 LeadUserMessage"接口(避免 adapter 直接依赖 conversation 包)
   - `func (c *skillCmd) Name() string { return "/" + c.skill.Name }`
   - `func (c *skillCmd) Description() string { return c.skill.Description }`
   - `func (c *skillCmd) NeedsArg() bool { return c.skill.Args != "" }`(只有 frontmatter 声明了 `args` 才需要补全)
   - `func (c *skillCmd) ArgHint() string { return c.skill.Args }`(直接展示 SKILL 的 args 提示)
   - `func (c *skillCmd) Category() string { return "skill" }`
   - `func (c *skillCmd) Execute(ctx, conn, arg) error`:调 `c.skill.Body()` 拿到完整内容 → 调 `c.h.InjectLeadUserMessage(content, arg)`(由 main.go 注入的 handler 接口实现,内容含 `# Skill: <name>` 标题 + description + 完整正文;若 arg 非空,末尾追加 `\n\n<user_args>\n<arg>\n</user_args>`)
   - `type LeadMessageInjector interface { InjectLeadUserMessage(content, userArg string) error }` 放 adapter 包内(最小接口)
   - `func AsSlashCommand(s *skill.Skill, h LeadMessageInjector) slash.SlashCommand { return &skillCmd{skill: s, h: h} }`
   - `func RegisterAll(r *slash.Registry, skills []*skill.Skill, h LeadMessageInjector) []error`:遍历 skills 调 `r.Register(AsSlashCommand(s, h))`,收集 error 返回(由 main.go 决定是否 panic);与 `slash.RegisterBuiltin` 风格一致
2. `slash_test.go` 覆盖:
   - Name / Description / NeedsArg(true & false)/ ArgHint / Category 各字段正确
   - Execute(content 注入正确,arg 为空时不追加 user_args)
   - Execute(arg 非空时追加 user_args 段)
   - Execute(ctx 取消 → 立即返回 ctx.Err())
   - RegisterAll 部分失败时收集 errors 但已注册的保留

**参考资料**:
- `src/internal/command/slash/builtin.go` builtin 命令实现风格
- `src/internal/command/slash/command.go` SlashCommand 接口
- `src/internal/engine/conversation/manager.go` SetLeadUserMessage 方法(供 InjectLeadUserMessage 实现参考)
- spec.md §B.3 触发模式

---

## Task 5: SkillsIndexSource(prompt 渐进式披露注入)

**状态**: 已完成
**目标**: 实现 `prompt.Source` 形式的 `SkillsIndexSource`,会话启动时把已加载 Skill 的 `name + description + 来源级别` 注入到 system prompt 的 LeadUserMessage 段,让 LLM 知道有哪些 Skill 可用;但**不暴露** SKILL.md 完整内容(完整内容只能通过 `use_skill` 工具按需加载)。

**影响文件**:
- `src/internal/skill/sources/skills_index.go` — 新建,SkillsIndexSource 实现
- `src/internal/skill/sources/skills_index_test.go` — 新建,单测

**依赖**: Task 1, Task 2

**具体内容**:
1. `sources/skills_index.go`:
   - `type SkillsIndexSource struct { registry *skill.Registry; maxLines int }`
   - 实现 `sources.Source` 接口:
     - `Name()` 返回 `"skills_index"`
     - `Assemble(ctx, env) (sources.Section, error)`:
       - 调 `r.registry.List()` 拿到所有 Skill
       - 按来源级别(项目级 → 用户级 → 内置级)分组,组装 markdown 文本:
         ```markdown
         <skills_index>
         以下是当前可用的 Skill 列表(渐进式披露:仅当 LLM 判定需要时才通过
         use_skill 工具加载完整内容):
         
         [project] skill-name-1
           描述: ...
         
         [user] skill-name-2
           描述: ...
         </skills_index>
         ```
       - 空 registry 时 Content 为空字符串(token 估算为 0)
       - 调 `tokens.Estimate(content)` 填 `Section.Tokens`
       - `Section.Placement = sources.PlacementUserMessage`(进 LeadUserMessage)
   - `func NewSkillsIndexSource(r *skill.Registry) *SkillsIndexSource { return &SkillsIndexSource{registry: r, maxLines: 200} }`
2. `skills_index_test.go` 覆盖:
   - 空 registry → Content="" Tokens=0
   - 单个 Skill → Content 包含 [project/user/builtin] + name + description
   - 多 Skill → 按来源级别排序(项目级在前)
   - 三档都有 → 三段都出现
   - 超 maxLines 截断(本测试不强制,留 warning 即可)

**参考资料**:
- `src/internal/engine/prompt/sources/source.go` Source 接口与 Section 结构
- `src/internal/memory/sources/memory_index.go`(若存在)MemoryIndexSource 注入风格
- spec.md §C 渐进式披露

---

## Task 6: /skills 列表命令 + WebUI 紫色徽标 + 状态栏

**状态**: 已完成
**目标**: 新增 `/skills` slash 命令(`Category="client"`,纯前端消费),弹出模态框按三档(项目级 / 用户级 / 内置级)分组展示 Skill 列表;WebUI 工具块新增紫色 `skill` 徽标;状态栏 SP 区域下拉新增 skills 子项显示已加载 Skill 数量。

**影响文件**:
- `src/internal/skill/adapter/client.go` — 新建,/skills client 命令实现
- `src/internal/interaction/web/handler.go` — 修改,handleSkills 路由(若需)+ SlashCommandProvider 适配器扩展
- `src/internal/interaction/web/static/app.js` — 修改,/skills 候选识别 + 模态框渲染 + 工具块 skill 徽标
- `src/internal/interaction/web/static/index.html` — 修改,skills 模态框 DOM(若需)
- `src/internal/interaction/web/static/style.css` — 修改,紫色 skill 徽标 + 模态框样式
- `src/main.go` — 修改,顶层装配 AsSlashCommand + RegisterAll(skill) → slash.Registry

**依赖**: Task 4, Task 5

**具体内容**:
1. `adapter/client.go`:
   - `type skillsListCmd struct {}`(无字段依赖,纯前端命令)
   - 实现 `SlashCommand` 接口:
     - `Name()` 返回 `"/skills"`
     - `Description()` 返回 `"列出当前系统支持的所有 Skill(区分项目级/用户级/内置级)"`
     - `NeedsArg()` 返回 `false`
     - `ArgHint()` 返回 `""`
     - `Category()` 返回 `"client"`(前端识别后走 `openSkillsTable` 本地逻辑,不发起 WS)
     - `Execute()` 返回 nil(永远不执行;Category=client 前端会拦截)
2. WebUI app.js 修改:
   - 候选下拉识别 `category==="skill"`:为候选条目加紫色 `skill` 标签前缀(视觉上与其他类别区分)
   - 候选下拉识别 `category==="client" && name==="/skills"`:选中后不发送 WS,直接调 `openSkillsTable()`
   - 新增 `openSkillsTable()`:向 WS 发 `list_skills` → 收到 `skills_list` payload → 弹出模态框
   - 模态框按三档 tab 渲染(`project` / `user` / `builtin`),每条显示 name + description + 源路径
   - 工具块渲染(已有 `updateToolEndNode`):当 `tool_name === "use_skill"` 时,在工具块头部加紫色 `skill: <skill_name>` 徽标
3. web 包 handler.go 修改:
   - 新增 MsgType 常量:`MsgTypeListSkills` / `MsgTypeSkillsList` 在 protocol.go
   - handleSkills(conn, msg):遍历 `h.skillProvider` 的 `ListBySource()` → 拼 `SkillsListPayload{Project: [], User: [], Builtin: []}` → 回推
   - `SkillProvider` 接口定义在 web 包(handler 不直接 import skill):
     ```go
     type SkillProvider interface {
         List() []SkillEntry
         ListBySource(source string) []SkillEntry
     }
     type SkillEntry struct { Name, Description, Source, Path string }
     ```
4. `main.go` 修改:
   - 顶层构造 `skill.Registry`(`skill.LoadAll(workdir, homeDir, execDir, maxBytes)`)
   - 构造 `web.SkillProvider` 适配器(把 `*skill.Registry` 投影为 `[]SkillEntry` 列表;类似现有 `slashAdapter`)
   - `handler.SetSkillProvider(provider)`(新增 setter,参考 `SetSlashRegistry` 风格)
5. `index.html` + `style.css`:新增 `skills-modal` DOM + 紫色 `--color-skill: #8b5cf6` CSS 变量
6. 状态栏 SP 下拉新增 `skills` 子项:
   - 复用 `dev_export_sp` WS 协议:`sp_total_tokens` payload 新增 `skills_count` 字段
   - 前端收到后渲染 `<div>skills: <b>{count}</b></div>`

**参考资料**:
- `src/internal/interaction/web/handler.go` SetSlashRegistry / SlashCommandProvider 风格
- `src/internal/interaction/web/static/app.js` 现有 /sessions client 类命令渲染逻辑
- `src/internal/interaction/web/protocol.go` 现有 MsgType 常量
- spec.md §E UI 呈现规则

---

## Task 7: 接入主流程(main.go 顶层装配)

**状态**: 已完成
**目标**: 在 main.go 启动流程中,按"配置加载 → Skill 扫描 → Registry 构造 → 工具/命令/SP 注入"的顺序完成 Skill 系统的全链路接入,确保 Skill 与现有 6 个内置工具、6 条内置命令、System Prompt 协同工作。

**影响文件**:
- `src/main.go` — 修改,顶层装配 Skill 系统
- `src/internal/config/config.go` — 修改(若需要),新增 `skill` 配置段
- `src/internal/config/config_test.go` — 修改(若需要),新增 skill 段测试

**依赖**: Task 2, Task 3, Task 4, Task 5, Task 6

**具体内容**:
1. `config.go` 新增 `SkillConfig`:
   ```go
   type SkillConfig struct {
       Enabled          bool `json:"enabled"`
       MaxSkillSizeBytes int `json:"max_skill_size_bytes"` // 默认 65536
   }
   ```
   - 默认 `Enabled=true`, `MaxSkillSizeBytes=65536`
   - 全局 + 项目级字段级合并(与现有 memory / compaction 一致)
2. `main.go` 装配流程(伪代码,具体行号以实际为准):
   ```go
   // 1. 解析 homeDir / workdir / execDir
   // 2. cfg.Skill.Enabled? 加载 Skill Registry
   if cfg.Skill.Enabled {
       skillReg, issues, err := skill.LoadAll(workdir, homeDir, execDir, cfg.Skill.MaxSkillSizeBytes)
       if err != nil { return fmt.Errorf("skill 加载失败: %w", err) }
       for _, iss := range issues { logger.Warn("skill 加载问题", zap.String("path", iss.Path), zap.Error(iss.Err)) }
   }
   // 3. 注册 use_skill 工具到 tool.Registry
   if skillReg != nil { toolReg.Register(skilladapter.NewUseSkillTool(skillReg)) }
   // 4. 注册 Skill slash 命令到 slash.Registry
   if skillReg != nil { skilladapter.RegisterAll(slashReg, skillReg.List(), leadInjector) }
   // 5. 注册 /skills client 命令
   slashReg.Register(&skilladapter.SkillsListCmd{})
   // 6. 注册 SkillsIndexSource 到 prompt.Builder
   if skillReg != nil {
       sources = append(sources, skillsources.NewSkillsIndexSource(skillReg))
   }
   // 7. handler.SetSkillProvider(skillProviderAdapter)
   ```
3. 配置文件(`setting.json`):
   - 顶层新增 `"skill": { "enabled": true, "max_skill_size_bytes": 65536 }` 段(向后兼容:缺失时走默认)
4. 错误处理:
   - 启动期 Skill 加载失败(`ErrSkillConflict`):记录错误并**退出进程**(避免运行期出现不可预期行为)
   - 启动期 Skill 加载 warn(单个解析失败):记录日志,继续启动
   - 运行期 `use_skill` 调用失败(Skill 不存在):返回 IsError=true 给 LLM,不退出
5. 配置可关闭:cfg.Skill.Enabled=false 时,**完全跳过** Skill 加载,不注册 use_skill 工具,不注入 SkillsIndexSource,SlashCommand 也不注册 Skill 命令(等价"未启用 Skill")

**参考资料**:
- `src/main.go` run() 函数完整流程(参考现有 slash / memory / mcp 装配顺序)
- `src/internal/config/config.go` 现有 MemoryConfig / CompactionConfig 风格
- spec.md §A §B §C §D §E 全部规则

---

## Task 8: 端到端验证

**状态**: 已完成
**目标**: 跨包集成验证:Skill 三档加载 + use_skill 工具调用 + slash 命令触发 + 渐进式披露注入 + WebUI 协议推送;并通过 Playwright 真实启动冒烟确认 UI 完整呈现。

**影响文件**:
- `src/internal/skill/e2e_test.go` — 新建,跨包集成测试
- `src/internal/skill/benchmark_test.go` — 新建,性能基准(可选)
- `docs/step10-Skill系统/validation.md` — 新建,端到端验证报告(可选)

**依赖**: Task 1~7 全部完成

**具体内容**:
1. **单元/集成测试**(覆盖 §1~6):
   - loader:合法 / 缺字段 / YAML 错误 / 不存在 / 超 maxBytes
   - registry:Register / Get / List / ListBySource / 同级同名报错
   - scanner:三档独立 / 项目级覆盖用户级 / 同级同名报错 / 目录不存在静默 / 单 Skill 失败不影响其他
   - use_skill tool:成功 / not found / 空 name / ctx 取消
   - slash 适配器:Name / Description / NeedsArg / ArgHint / Category / Execute(无参+有参) / ctx 取消
   - SkillsIndexSource:空 / 单 / 多 / 三档都有 / token 估算
2. **跨包 e2e**(5 用例,真实组装 + 真实 WS):
   - e2e_01_loading_three_levels:三档 Skill 真实加载,验证注册顺序、覆盖规则、List 输出
   - e2e_02_use_skill_via_tool:LLM tool_use 路径 → use_skill 工具执行 → 完整内容作为 tool_result 返回
   - e2e_03_slash_command_with_arg:`/<skill-name> <args>` 触发 → LeadUserMessage 注入正确
   - e2e_04_prompt_injection:SkillsIndexSource.Assemble → Builder.Assemble → SystemPrompt.LeadUserMessage 含 Skill 索引
   - e2e_05_ws_list_skills_protocol:真实 HTTP Server + WS,客户端发 `list_skills` → 服务端回 `skills_list` payload 正确(三档分组)
3. **配置可关闭**:cfg.Skill.Enabled=false → 全部降级(不注册 use_skill / 不注入 SP / 不注册 slash)
4. **零 Skill 启动兼容**:三档目录全空 → 启动正常,`/skills` 显示空状态,use_skill 调用即报 not_found
5. **Step 1~9 零回归**:`go test ./...` 全量通过,无破坏既有功能
6. **真实启动冒烟**(Playwright):
   - main.go 启动 → HTTP 资源 200 → WS 连接 → 收到 `slash_commands` 含 Skill 命令 + `client` 类 `/skills` → 收到 `skills_list` 真实数据
   - 截屏:候选下拉带紫色 skill 标签 + 工具块带 skill 徽标
7. **CLI 真实冒烟**(可选):
   - 准备 3 个测试 Skill(项目级 + 用户级 + 内置级各一个)
   - 启动 codepilot,WebUI 验证 Skill 命令触发

**参考资料**:
- `src/internal/skill/e2e_test.go` 新建(参考 `src/internal/command/slash/e2e_test.go` 风格)
- `docs/step6-MCP协议实现/` Task 9 E2E 冒烟报告格式参考
- spec.md 全部章节 + checklist.md 全部验证项
