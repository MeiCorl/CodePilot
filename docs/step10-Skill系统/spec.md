# Step 10 — Skill 系统

## 背景

CodePilot 当前已具备 LLM 交互、工具调用、权限控制、MCP 协议、上下文管理、自动记忆与快捷命令系统,具备了一个 AI Coding Agent 的核心骨架。但仍缺一类**用户自定义的能力模块**——Skill:开发者可以为自己的项目写一个 Skill(比如「按本仓库的代码规范自动重构」「批量为新模块生成单测」),把工作流、脚本、知识封装到 SKILL.md 中,既能让 LLM 在需要时主动调用,也能让用户通过斜杠命令手动触发。

参考 Claude Code / Codex / Cursor 等主流 Agent 的做法,Skill 在业界已形成事实标准:
- 定义形式:目录型,以 `SKILL.md` 为主入口,内含 `name` / `description` 的 YAML frontmatter,正文使用 markdown
- 目录可放 `scripts/`(可执行脚本)、`reference/`(参考资料)、`assets/`(静态资源),SKILL.md 通过相对路径引用
- 渐进式披露:LLM 默认只看到 `name + description` 的元数据索引,需要时再按需加载完整内容,避免污染 system prompt

本步骤的目标:为 CodePilot 实现**完整兼容主流约定的 Skill 系统**,三档优先级(项目级 / 用户级 / 内置),自动注册为 slash 命令,自动索引到 LLM system prompt,支持 LLM 通过 `use_skill` 工具按需加载完整内容,UI 上有可视化呈现。

## 目标用户

1. **项目级 Skill 作者**——为某个仓库写项目专属工作流(规范约束、领域操作),放在 `<cwd>/.codepilot/skills/<skill-name>/SKILL.md`,跟随项目分发
2. **用户级 Skill 作者**——给自己写跨项目通用的工作流(常用模板、习惯约束),放在 `~/.codepilot/skill/<skill-name>/SKILL.md`,跨项目生效
3. **CodePilot 终端用户**——通过 `/<skill-name>` 直接触发,或在对话中让 LLM 主动调用
4. **内置 Skill 维护者**(预留扩展点)——CodePilot 自身后续可内置官方 Skill,通过 `builtin` 目录分发,目前为空
5. **WebUI 观察者**——看到 Skill 调用的过程与结果,类似工具调用

## 能力清单

### A. Skill 发现与加载

1. **三档优先级扫描**:
   - **项目级**(最高):`<cwd>/.codepilot/skills/<skill-name>/SKILL.md`(`skill` 复数,`skills` 目录)
   - **用户级**:`~/.codepilot/skill/<skill-name>/SKILL.md`(`skill` 单数,`skill` 目录)
   - **内置级**(最低,预留):`<exec>/internal/skill/builtin/<skill-name>/SKILL.md`,本步骤不内置任何 Skill,只保留扩展能力
2. **目录型 Skill 加载**:每个 Skill 是一个目录,主文件为 `SKILL.md`,可包含 `scripts/` / `reference/` / `assets/` 子目录(仅做存在性识别与路径解析,本步骤不执行这些子目录中的文件)
3. **SKILL.md 解析**:
   - YAML frontmatter 必填字段:`name`(唯一标识)/ `description`(一句话用途)
   - frontmatter 可选字段:`args`(用户参数 schema,占位用)、`allowed-tools`(可调用工具白名单,留 Step 11 接入)
   - 正文为 markdown,作为 Skill 的完整说明/指令,LLM 按需加载
4. **同名冲突处理**:项目级 skill 与用户级同名时**项目级完全覆盖**(用户级被屏蔽,不注册);同级别同名(如两个项目级 skill 同名)启动期**直接报错并退出**,由用户改名后再启动
5. **加载失败隔离**:单个 Skill 解析失败(YAML 错误、缺 SKILL.md、目录不可读)时记录 warn 日志并跳过该 Skill,不影响其他 Skill 加载与程序启动

### B. Skill 作为 Slash 命令

1. **自动注册**:启动期扫描完成后,每个加载成功的 Skill 自动以 `name` 字段为命令名注册到 `slash.Registry`(`/` 前缀由注册时补上),`name` 冲突时由 §A.4 规则预先消解
2. **SlashCommand 接口实现**:Skill 命令的 `Name()` 返回 `/<skill-name>`,`Description()` 返回 frontmatter 的 description,`Category()` 返回 `"skill"`,`Execute()` 按下面 §B.3 规则执行
3. **三种触发模式**:
   - **LLM 主动调用**:LLM 通过 `use_skill` 工具发起,完整 SKILL.md 内容作为 `tool_result` 返回
   - **用户命令触发**:`/<skill-name>` slash 命令(无参)→ 把完整 SKILL.md 内容作为 LeadUserMessage 追加到下一轮 user 消息
   - **用户命令带参**:`/<skill-name> <args>` → 同上,但 LeadUserMessage 末尾追加 `<user_args>...</user_args>` 段,供 Skill 内指令引用
4. **`/skills` 列表命令**:新增 `client` 类 slash 命令,纯前端消费,展示当前已加载的 Skill 列表(分项目级 / 用户级 / 内置级三组,每组显示 `name` + `description` + 来源路径)

### C. 渐进式披露

1. **索引注入**:`prompt.Builder` 新增 `SkillsIndexSource`,会话启动时把已加载 Skill 的 `name` + `description` + 来源级别(`project` / `user` / `builtin`)作为 LeadUserMessage 注入,LLM 默认只知道 Skill 列表,看不到完整内容
2. **按需加载**:LLM 通过 `use_skill` 工具调用某个 Skill 时,工具读取 SKILL.md 完整内容(frontmatter + 正文)作为 `tool_result` 返回给 LLM,LLM 据此理解 Skill 的工作流并执行
3. **大体积截断**:单个 SKILL.md 正文(去除 frontmatter)超过 64KB 时只取前 64KB + 截断提示,避免单次 tool_result 过大;完整内容可通过 Skill 目录的 `reference/` 子文件分页引用(本步骤不实现 reference 子文件渲染)

### D. `use_skill` 工具

1. **注册**:在 `tool.Registry` 注册一个 `use_skill` 工具,Input schema: `{ "skill_name": "<string>" }`(必填)
2. **执行逻辑**:
   - 按 `skill_name` 在已加载 Skill 中查找(项目级优先,用户级兜底,内置级最后)
   - 找到:读取 SKILL.md 完整内容,作为 `ToolResultBlock{Content: ..., IsError: false}` 返回
   - 找不到:返回 `ToolResultBlock{Content: "skill not found: <name>", IsError: true}`,LLM 自主决策重试/换名
3. **权限**:走 `permission.Decide` 全链路,工具名 `use_skill` 默认 `allow`(只读工具,无副作用)
4. **可观测性**:执行时复用现有 `tool_call_start` / `tool_call_end` 事件流,UI 上与 `ReadFile` / `Grep` 等只读工具视觉一致;新增紫色 `skill: <skill-name>` 徽标区分 Skill 来源

### E. UI 呈现

1. **Skill 工具块**:LLM 调用 `use_skill` 时,前端工具块头部显示紫色「skill: <name>」徽标,点击可展开看完整 SKILL.md 渲染(与现有工具块折叠/展开交互一致)
2. **`/` 候选下拉**:Skill 命令(由 `slash_commands` 推送)在候选下拉中带紫色「skill」分类标签,与内置 session/context/debug 类目视觉区分
3. **`/skills` 列表面板**:`/skills` 触发后弹出模态框,按项目级 / 用户级 / 内置级三组 tab 展示,每条显示 `name` + `description` + 源路径;内置级为「(无内置 skill)」时显示空状态
4. **状态栏可观测性**(可选):SP 区域下拉新增 `skills` 子项,显示已加载 Skill 数量与 token 估算(与 memory 索引展示风格一致)

## 非功能要求

1. **架构分层**:Skill 系统归属**第 3 层 工具层**,作为可插拔的"复合能力模块",与 `tool.Tool` / `slash.SlashCommand` 同层;不破坏现有 5 层架构
2. **包边界**:
   - `src/internal/skill/` 主包:定义 `Skill` 类型 / `Loader` / `Registry` / `Scanner`,纯业务逻辑,不 import web
   - `src/internal/skill/builtin/` 内置 Skill 目录(本步骤为空目录 + README 占位)
   - `src/internal/skill/loader/` SKILL.md 解析器(frontmatter + markdown),不依赖其他 skill 子包
   - `src/internal/skill/adapter/` slash 命令适配器(把 Skill 适配成 `slash.SlashCommand`)、tool 适配器(把 Skill 列表适配成 `use_skill` 工具的 schema)
   - 现有包 import 列表严格保持,新增依赖单向:`tool` / `slash` / `prompt` / `web` → 可 import `skill`;`skill` → 不 import 任何上层
3. **兼容性**:
   - 现有 6 条 slash 命令(/new / sessions / resume / clear / compact / dump)行为完全不变
   - 现有 6 个内置工具(ReadFile / WriteFile / EditFile / Bash / Glob / Grep)行为完全不变
   - Step 1~9 已有功能(权限 / 上下文 / 记忆 / MCP / 缓存)零回归
4. **性能**:Skill 扫描与解析在启动期一次性完成,运行期 `use_skill` 调用为内存读 + YAML 解析(已预解析缓存),无磁盘 I/O 抖动
5. **配置**:`setting.json` 新增 `skill` 段:`{ "enabled": true, "max_skill_size_bytes": 65536 }`,全局与项目级字段级合并
6. **零 Skill 启动兼容**:既无项目级也无用户级 Skill 时,系统正常启动,`/skills` 显示「暂无 Skill」空状态,`use_skill` 工具仍注册但调用即报 not_found
7. **可观测性**:Skill 加载日志(每条 Skill 加载成功 / 失败 / 跳过原因,带 sessionID / 路径 / 来源级别),运行期 `use_skill` 调用日志(执行耗时 / 内容大小 / 来源级别),`/skills` 输出通过 `dev_export_sp` 风格的 WS 消息(可选)

## 设计骨架

```text
┌────────────────────────────────────────────────────────────────────┐
│                    启动期(一次性扫描与装配)                             │
│                                                                     │
│   <cwd>/.codepilot/skills/      (项目级,最高)                       │
│      ├── skill-a/SKILL.md  ─┐                                       │
│      ├── skill-b/SKILL.md   │  Loader.Scan()                       │
│      └── skill-c/           │  (YAML 解析 + 同名校验 + 来源标记)     │
│          ├── SKILL.md  ─────┤                                       │
│          ├── scripts/        │                                      │
│          └── reference/      │                                      │
│                             ▼                                      │
│   ~/.codepilot/skill/       (用户级)                                │
│      ├── skill-d/SKILL.md  ─┐  Loader.Scan()                       │
│      └── skill-e/SKILL.md   │  (项目级已注册则跳过)                  │
│                             ▼                                      │
│   <exec>/internal/skill/builtin/ (内置级,本步骤空)                  │
│                             ▼                                      │
│                   ┌─────────────────────┐                           │
│                   │ skill.Registry      │                           │
│                   │  - List()           │                           │
│                   │  - Get(name)        │                           │
│                   │  - 来源优先级表       │                           │
│                   └──────────┬──────────┘                           │
│                              │                                     │
│       ┌──────────────────────┼──────────────────────┐              │
│       ▼                      ▼                      ▼              │
│ slash.Adapter         tool.Adapter         prompt.SkillIndexSource │
│ (Skill → SlashCommand) (注册 use_skill 工具)  (name+desc 注入)       │
│       │                      │                      │              │
│       ▼                      ▼                      ▼              │
│ /<skill-name> 触发    LLM 主动调用 use_skill    LLM 看到 skill 列表   │
│ (LeadUserMessage     (完整 SKILL.md 内容        (只读元数据,           │
│  追加 + args)         作为 tool_result)         按需加载)            │
└────────────────────────────────────────────────────────────────────┘
```

关键模块:

| 模块                      | 职责                                                            | 关键导出                                |
| ----------------------- | ------------------------------------------------------------- | ----------------------------------- |
| `skill/loader.go`       | SKILL.md 文件扫描 + YAML frontmatter 解析 + markdown 正文加载                  | `Loader`, `Scan()`, `ParseFile()`   |
| `skill/skill.go`        | `Skill` 类型 + `Source` 枚举(project/user/builtin) + 完整内容加载            | `Skill`, `Source`, `FullContent()`  |
| `skill/registry.go`     | 三档合并的注册表,项目级完全覆盖用户级,同级别同名报错                                 | `Registry`, `Register()`, `List()`  |
| `skill/builtin/`        | 内置 Skill 目录(本步骤空 + README 占位)                                       | `ScanBuiltin()`                      |
| `skill/adapter/slash.go`| `Skill → slash.SlashCommand` 适配器,`Category="skill"`                    | `AsSlashCommand(s *Skill)`          |
| `skill/adapter/tool.go` | `use_skill` 工具实现,Input: `{skill_name}`,Output: 完整内容或 not_found          | `NewUseSkillTool(r *Registry)`      |
| `skill/sources/`        | `prompt.Source` 实现,`SkillsIndexSource` 把 name+description 注入到 SP      | `NewSkillsIndexSource(r)`           |
| `skill/scanner.go`      | 顶层 `LoadAll(workdir, homeDir, execDir) (*Registry, error)` 装配入口 | `LoadAll()`                          |

## Out of Scope(本步骤不做)

1. **Skill 引用 scripts/ 脚本执行**:SKILL.md 正文中 `scripts/foo.sh` 形式的引用本步骤**只做存在性识别**,不实际执行;后续 Step 11 Hook 系统或独立 Skill 运行时再实现
2. **Skill 沙箱隔离**:Skill 加载/调用的路径访问、脚本执行不引入新沙箱;Skill 内若调 `Bash` / `ReadFile` 走现有 Step 5 权限系统
3. **Skill 热加载/卸载**:本步骤只支持启动期一次性扫描,运行期不监听 Skill 目录变化(后续可由 `auto-specs` 风格的 watcher 扩展)
4. **Skill 嵌套引用**(`@skill-name`):SKILL.md 正文不解析 `@other-skill` 跨 Skill 引用语法
5. **Skill 版本管理**:不识别 SKILL.md 的 `version` 字段;冲突解决只看 name
6. **Skill 远程分发**:不实现从 URL/Git 仓库拉取 Skill,本步骤只支持本地目录
7. **Skill 调用统计/计费**:不记录 Skill 调用次数、token 消耗
8. **WebUI Skill 内容编辑/创建**:本步骤 Skill 文件由用户手写,WebUI 只展示与触发,不做创建/编辑 UI
9. **LLM 主动调用 vs 用户命令触发的差异化 UI**:两种触发模式共用同一个 `use_skill` 工具块呈现(都通过 `tool_call_start/end` 事件流);后续若需要区分(比如用户手动触发时标"已应用"按钮)再扩展
10. **`/skills` 输出嵌入到会话历史**:`/skills` 面板是临时模态框,关闭后不写入 session JSON
