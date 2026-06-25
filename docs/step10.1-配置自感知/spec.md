# Step 10.1 — 配置自感知 (Self-Aware Configuration)

## 背景

CodePilot 启动后**完全不知道自己作为软件系统的元信息**(配置文件位置、JSON schema、扩展点、运行机制)。当用户提"帮我加个 filesystem MCP server"、"禁止 rm 命令"、"把上下文窗口改成 100000"等请求时,Agent 不知道:

- 配置文件在哪(全局 `~/.codepilot/setting.json` vs 项目级 `<cwd>/.codepilot/setting.json`)
- JSON schema 各 section 字段含义与默认值
- 哪些字段运行时可改、哪些要重启
- 改错了启动报错的排查路径

**根本原因**:System Prompt 完全没提自身配置体系;Skill 系统(Step 10)虽支持按需加载,但没有任何 Skill 承担"描述 Agent 自身"的角色。

**主流解法参考**:Claude Code 等成熟 Agent 通过结构化、按需加载的「自身能力描述」解决——既不污染常驻 SP,又把"关于自己"的知识放在 Agent 找得到的地方。

## 目标用户

- CodePilot **所有用户**(零成本受益,无需任何配置即可体验"Agent 知道怎么改自己")
- 尤其中高级用户:需要扩展 MCP、调权限规则、改上下文窗口、加 Skill、换模型 API key 等场景

## 能力清单

1. **SP 自感知** — Agent 每次会话都能感知到「自己可以通过编辑 setting.json 修改自身配置」,且明确知道配置文件的两层路径(全局 vs 项目级)以及「详细 schema 见哪个 Skill」
2. **按需加载 config-management Skill** — 用户提出「加/删/改/查配置」类请求时,Agent 主动调 `use_skill("config-management")` 加载完整 schema 文档
3. **全 setting.json 覆盖** — Skill 内容覆盖 `mcp` / `permissions` / `compaction` / `memory` / `skill` / `tools` 六个 section **+ 顶层 LLM/agent 参数**的:路径说明、JSON schema 摘要、完整示例、字段默认值与单位、「是否需要重启」标注、写错后的启动报错排查指引
4. **全局/项目级智能选择** — Agent 根据用户措辞自动选择改全局还是项目级;不明确时**主动询问用户**;Skill 文档明确说明两层级的合并规则与覆盖语义
5. **安全改写** — Agent 使用现有 `WriteFile` / `EditFile` 工具(Step 2);写入前用 `ReadFile` 确认文件存在并定位锚点;写入后告知用户「如何验证生效」(MCP: 看启动日志;permission: 试一次危险命令;compaction: 观察 token 数)
6. **不依赖 skill.enabled 开关的基础感知** — 即使用户设 `skill.enabled=false`,SP 自描述段落仍生效(指向 Skill 不可用是降级,符合 SPEC 「优雅降级」原则)

## 非功能要求

- **SP token 增量 < 80 token** — config 自描述段落必须精简;详细 schema **全部进 Skill** 不得进 SP
- **Skill 命中率高** — frontmatter `description` 广覆盖「加/删/改/查/管理 + MCP/权限/上下文/Skill/工具/模型/API key/超时」等所有常见配置场景,确保 LLM 在收到相关请求时高概率调 `use_skill`
- **Skill 正文 < 64KB** — 与 Skill 系统既定阈值(`MaxSkillSizeBytes` 默认 64KB)对齐,单 section 控制 < 5KB;超出则按 Step 10 的截断策略处理
- **零新依赖** — 复用 Step 10 Skill 系统、Step 4 SP Builder、Step 2 文件工具;不引入任何第三方包
- **现有功能零回归** — WebUI、/skills 列表、Step 10 Skill 加载流程、Anthropic prompt cache、`skill.enabled=false` 降级路径、Step 7 上下文管理全部不受影响
- **遵循 5 层架构 + Skill 适配层分层** — 新增的 `ConfigAwarenessSource` 属第 2 层引擎层,新建的 `config-management` Skill 属第 3 层工具层,二者无循环依赖

## 设计骨架

```
src/internal/engine/prompt/sources/
  config_awareness.go              ← 新增:ConfigAwarenessSource 实现(Source 接口)

src/internal/skill/builtin/
  config-management/
    SKILL.md                       ← 新增:Skill 资源文件(随构建复制到 exe-dir/skills/)

src/main.go                         ← 修改:注册 ConfigAwarenessSource 到 Builder

build/
  build.ps1                        ← 修改:确保 config-management/SKILL.md 被打包到输出目录
  Makefile                         ← 同上

docs/step10.1-配置自感知/
  spec.md                          ← 本文档
  tasks.md                         ← 任务清单
  checklist.md                     ← 验证清单

.harness/PROGRESS.md              ← 步骤完成后追加新条目
```

**关键交互流**(加 filesystem MCP 场景):

```
User: 帮我加一个 filesystem MCP server
  ↓
[SP 含 config 自描述段,LLM 知道:加配置 → 看 config-management skill]
  ↓
Agent: use_skill("config-management")
  ↓
[Skill 完整正文注入上下文,含 mcp section schema + 示例 + 改全局/项目级说明]
  ↓
Agent: 读 ~/.codepilot/setting.json(用 ReadFile)
  ↓
Agent: 用 EditFile 追加到 mcp.servers[] (或询问用户改哪个层级)
  ↓
Agent: 告诉用户「重启后生效,启动日志应看到 mcp 客户端连接成功」
```

## Out of Scope(本步骤不做)

- **`config_reload` 工具** — 实现部分字段(如 `permission.mode` / `permission.rules` / `tools.enabled`)的运行时热生效
- **写入时 JSON schema 校验** — 在 `WriteFile` 路径上加一层校验,防止 Agent 写错格式启动失败
- **从 Go struct tag 自动生成 SKILL.md** — 解决「代码改了 schema、Skill 文档没同步」的漂移问题
- **WebUI `/config` 通用设置面板** — 把 Skill 内容渲染为可视化表单
- **跨会话的"上一次配置决策"记忆** — 每次都按用户措辞判断,不持久化偏好

## 与现有功能的关系

| 依赖 | 来源 | 关系 |
|------|------|------|
| `Source` 接口 + `Builder` 装配 | Step 4 | 新增一个 Source,注册到 Builder 链尾 |
| `Skill` 数据层 + `Registry` + `use_skill` 工具 | Step 10 | Skill 自动被 `LoadAll` 扫到,经 `use_skill` 加载 |
| 三档优先级目录(builtin/user/project) | Step 10 | 复用既有 `SourceBuiltin` 加载路径 |
| `WriteFile` / `EditFile` / `ReadFile` 工具 | Step 2 | Agent 改 setting.json 的执行抓手 |
| `PermissionsConfig` 规则运行时追加 | Step 5 | Skill 文档需说明「HITL 对话框'记住此选择'会自动写回」 |
| `Anthropic Prompt Caching` | Step 4 | SP 自描述段必须能进 cache 段(`Placement=System`) |
| 未来 Step 11/12 的自描述 | Hook / SubAgent | 本步骤立下「Agent 自描述」范式,新步骤只需往 Skill 加章节 |
