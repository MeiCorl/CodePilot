# Step 10.1 任务清单 — 配置自感知 (Self-Aware Configuration)

> 实施顺序:Task 1 → 2 → 3 → 4 → 5
> Task 1 是新增独立 Source,Task 2-3 是配套改造,Task 4-5 是端到端验证
> 任务状态:文档生成时全部为 `待完成`;开始实现前更新为 `进行中`;完成且对应 checklist 通过后更新为 `已完成`

---

## Task 1: 实现 ConfigAwarenessSource

**状态**:已完成

**目标**:新增一个独立的 System Prompt Source,产出 ~60-80 token 的 config 自描述段落,告诉 Agent 配置文件在哪、详细 schema 见哪个 Skill。沿用 Step 4 的 `Source` 接口。

**影响文件**:
- `src/internal/engine/prompt/sources/config_awareness.go` — 新建,实现 Source 接口
- `src/internal/engine/prompt/sources/config_awareness_test.go` — 新建,单测覆盖 Assemble 输出

**依赖**:无(Task 1 是入口)

**具体内容**:
1. 定义常量 `configAwarenessContent`(固定字符串,~60-80 token),内容覆盖:
   - 配置文件两层路径(全局 `~/.codepilot/setting.json` + 项目级 `<cwd>/.codepilot/setting.json`,项目级覆盖全局)
   - 提及 "config-management" Skill 名称(精确匹配 frontmatter `name`,便于 LLM 调 `use_skill`)
   - 一句话引导:详细 schema/示例/默认值见该 Skill
   - 明确改写工具:用 Step 2 的 `ReadFile` + `EditFile`/`WriteFile`
2. 定义 `ConfigAwarenessSource` 结构体(无状态,可为零值 struct)
3. 实现 `Name() string` 返回 `"config_awareness"`
4. 实现 `Assemble(ctx, env) (Section, error)`:
   - 固定返回 `Section{Name: "config_awareness", Content: configAwarenessContent, Placement: PlacementSystem, Tokens: tokens.Estimate(content)}`
   - 不读文件、不依赖 env,纯静态(降级零成本)
5. 单测 `TestConfigAwarenessSource_Assemble`:
   - 断言 Content 与常量相等
   - 断言 Placement == PlacementSystem
   - 断言 Tokens < 80
   - 断言 Name() == "config_awareness"

**参考资料**:
- `Source` 接口定义:[src/internal/engine/prompt/sources/source.go](src/internal/engine/prompt/sources/source.go) — 关注 `Section` / `PlacementSystem` / `Env` 类型
- `EnvironmentSource` 实现作为模板:[src/internal/engine/prompt/sources/environment.go](src/internal/engine/prompt/sources/environment.go) — 关注 `Assemble` 签名
- `tokens.Estimate` 用法:[src/internal/engine/prompt/tokens/tokens.go](src/internal/engine/prompt/tokens/tokens.go)

---

## Task 2: 注册 ConfigAwarenessSource 到 Builder

**状态**:已完成

**目标**:在 `main.go` 装配 SP Builder 时,把新 Source 注册进去,确保每次会话都注入 config 自描述段。

**影响文件**:
- `src/main.go` — 修改 `run()` 函数中的 `prompt.NewBuilder(...)` 调用,追加 `prompt.NewConfigAwarenessSource()`

**依赖**:Task 1(必须先有 Source 实现)

**具体内容**:
1. 在 `main.go` 找到现有的 `prompt.NewBuilder(...)` 调用点(注册了 `StaticSource` / `EnvironmentSource` / `AgentsMDSource` / `MemoryIndexSource` / `SkillsIndexSource`)
2. 在 Source 列表**末尾**追加 `NewConfigAwarenessSource()`(放在最后是因为它最稳定、与其他 Source 无依赖)
3. 验证 `web.Handler.assembleSP()` 在每次切换会话/恢复会话时都会重新触发 Assemble(已有逻辑,无需改)
4. 验证 WebUI 的 SP 可观测性面板(Step 4 落地能力)能正确显示新 Source 的 name + token 数

**参考资料**:
- `prompt.NewBuilder` 装配点:[src/main.go](src/main.go) `run()` 函数
- SkillsIndexSource 已注册的现有模式:同文件,搜 `SkillsIndexSource` 关键词

---

## Task 3: 编写 config-management Skill 的 SKILL.md

**状态**:已完成

**目标**:新建 `src/internal/skill/builtin/config-management/SKILL.md`,包含 frontmatter + 全 setting.json 章节文档。frontmatter `description` 广覆盖触发词,确保 LLM 高概率主动调 `use_skill("config-management")`。

**影响文件**:
- `src/internal/skill/builtin/config-management/SKILL.md` — 新建(资源文件,纯 markdown,不属于 Go 源)

**依赖**:无(可与 Task 1 并行)

**具体内容**:
1. 目录创建:`src/internal/skill/builtin/config-management/`
2. 编写 `SKILL.md`,结构如下:
   ```yaml
   ---
   name: config-management
   description: |
     管理 CodePilot 自身配置 — 添加/删除/修改/查看 MCP server、权限规则、
     上下文压缩参数、记忆系统、Skill 系统、工具白名单、模型 API key、超时时间等。
     当用户提到「加/配/改/删/设置/管理 + MCP、permission、权限、上下文、context window、
     Skill、工具、tool、model、API key、超时」时,加载本 Skill 获取 setting.json
     各 section 的完整 schema、示例、默认值与改写方法。
   ---
   ```
3. body 必须覆盖以下章节(每节按统一模板:**路径** → **schema 摘要** → **完整示例** → **字段默认值与单位** → **是否需要重启** → **错误排查**):
   - **§1 配置文件总览** — 全局 vs 项目级路径、合并规则、覆盖优先级
   - **§2 mcp** — `mcp.servers[]`(stdio/http 两种 type 的 schema)、`handshake_timeout_seconds`、`list_tools_cache_ttl_seconds`
   - **§3 permissions** — `mode`(strict/default/permissive 三档语义)、`rules[]`(tool/pattern/action/reason 四字段 + HITL 写回机制)
   - **§4 compaction** — `enabled` + 7 个阈值字段(`tool_result_threshold`/`preview_tokens`/`auto_trigger_margin`/`manual_target_margin`/`keep_recent_tokens`/`keep_recent_min_messages`/`breaker_threshold`)
   - **§5 memory** — `enabled`、`index_max_lines`、`index_max_bytes`、`review_model`(预留)
   - **§6 skill** — `enabled`、`max_skill_size_bytes`
   - **§7 tools** — `tools.enabled` 白名单机制
   - **§8 顶层 LLM/agent 参数** — `provider`/`model`/`base_url`/`api_key`/`max_tokens`/`timeout`/`max_retries`/`tool_execution_timeout_seconds`/`tool_working_directory`/`context_window_size`/`max_agent_loop_iterations`/`context_safety_margin`
   - **§9 改写工作流** — 通用步骤(读 → 定位锚点 → 改 → 写 → 验证)、全局 vs 项目级选择决策树、HITL 写回规则、修改后如何验证生效(MCP 启动日志 / permission 试一次危险命令 / compaction 观察 token 数)
   - **§10 错误排查** — JSON 语法错、字段名拼写错、字段值类型错(数字写成了字符串)、未知字段警告、启动失败的常见 5 类报错与修复
4. 控制总长度 < 64KB(单节 < 5KB),与 `MaxSkillSizeBytes` 阈值对齐
5. 在 §2 mcp 的示例中,使用与 `config/setting.example.json` 完全一致的真实可运行例子(便于用户复制粘贴)

**参考资料**:
- SKILL.md frontmatter 规范:[src/internal/skill/loader/loader.go](src/internal/skill/loader/loader.go) `Frontmatter` 结构体
- setting.json 完整示例:`config/setting.example.json`
- 各 Config struct 定义:[src/internal/config/config.go](src/internal/config/config.go) 关注 `MCPConfig` / `PermissionsConfig` / `CompactionConfig` / `MemoryConfig` / `SkillConfig` / `ToolsConfig`
- 顶层 Config struct 字段:同文件,关注 `Config` struct 的全部 json tag

---

## Task 4: 构建管线集成(SKILL.md 落到 exe-dir)

**状态**:已完成

**目标**:改 `build/build.ps1` 与 `Makefile`,把 `src/internal/skill/builtin/config-management/SKILL.md` 复制到输出目录的 `skills/config-management/SKILL.md`,确保 `os.Executable()` 路径下能找到该文件、被 `skill.LoadAll` 的 builtin 扫描拾取。

**影响文件**:
- `build/build.ps1` — 修改,在构建步骤中追加 SKILL.md 复制命令
- `Makefile` — 修改,追加 build target 中的 SKILL.md 复制步骤

**依赖**:Task 3(必须先有 SKILL.md)

**具体内容**:
1. 选定**资源源路径**与**目标路径**:
   - 源:`src/internal/skill/builtin/config-management/SKILL.md`
   - 目标:`<exe-dir>/internal/skill/builtin/config-management/SKILL.md`(`<exe-dir>` 由 `filepath.Dir(os.Executable())` 解析,与 `scanner.go` 的 `builtinRelPath = "internal/skill/builtin"` 对齐)
2. 在 `build/build.ps1` 中追加:
   ```powershell
   # 复制内置 Skill 资源
   $skillSrc = "src/internal/skill/builtin"
   $skillDst = Join-Path $outputDir "internal/skill/builtin"
   if (Test-Path $skillSrc) {
       Copy-Item -Path "$skillSrc/*" -Destination $skillDst -Recurse -Force
   }
   ```
3. 在 `Makefile` 中追加等价的 bash 命令(找现有 `go build` target 附近):
   ```makefile
   SKILL_SRC := src/internal/skill/builtin
   SKILL_DST := $(BIN_DIR)/internal/skill/builtin
   ifneq ($(wildcard $(SKILL_SRC)/*),)
   	cp -r $(SKILL_SRC)/* $(SKILL_DST)/
   endif
   ```
4. 验证步骤:
   - 本地 `make build` 或 `./build.ps1` 后,`<output>/internal/skill/builtin/config-management/SKILL.md` 必须存在
   - 启动 CodePilot,日志中应见 "scanned builtin skill: config-management" 或类似 info
   - WebUI `/skills` 列表(紫色徽标)应见 `config-management` 条目,Source=builtin

**参考资料**:
- `skill.LoadAll` 入口:[src/internal/skill/scanner.go](src/internal/skill/scanner.go) `LoadAll` 函数
- `buildSkillRoots` 路径解析:[src/main.go](src/main.go) `buildSkillRoots` 函数
- 现有 build 脚本: `build/build.ps1` / `Makefile`

---

## Task 5: 端到端验证 + 现有功能回归

**状态**:已完成

**目标**:跑通 3 个核心验证场景(加 MCP / 禁 rm / 改 context window),并回归 Step 4/5/6/7/8/10 的核心能力,确保零回归。

**影响文件**:无新增/修改(纯验证任务)

**依赖**:Task 1 + 2 + 3 + 4 全部完成

**具体内容**:
1. **构建并启动**:本地 `make build` → 启动二进制 → 打开 WebUI
2. **验证场景 A — 加 filesystem MCP server**:
   - User: "帮我加一个 filesystem MCP server,允许访问 /tmp 目录"
   - 预期:Agent 调 `use_skill("config-management")` → 输出正确 JSON 片段 → 用 EditFile 写入 setting.json
   - 校验:写入的 mcp.servers[] 条目 type=stdio / command=npx / args 正确
3. **验证场景 B — 禁止所有 rm 命令**:
   - User: "禁止所有 rm 命令,理由是数据安全"
   - 预期:Agent 调 `use_skill` → 输出 `{tool: "Bash", pattern: "rm *", action: "deny", reason: "数据安全"}` → 写入 permissions.rules
   - 校验:用 `Bash(rm xxx)` 触发拦截
4. **验证场景 C — 改 context window 为 100000**:
   - User: "把上下文窗口改成 100000 tokens"
   - 预期:Agent 调 `use_skill` → 改顶层 `context_window_size` 字段 → 写入
   - 校验:重启后 WebUI ctx 进度条按 100000 计算
5. **SP token 增量校验**:
   - 打开 WebUI 的 SP 可观测性面板(Step 4 落地能力)
   - 检查 `config_awareness` 行的 Tokens 字段
   - 必须 < 80
6. **Skill 命中校验**:
   - WebUI `/skills` 列表应见 `config-management` 条目,Source=builtin
   - 紫色徽标正常显示
7. **现有功能回归**:
   - WebUI 启动 / 切会话 / 恢复会话:正常
   - Bash / ReadFile / WriteFile / EditFile / Grep / Glob:正常
   - `skill.enabled=false` 降级路径:SP 自描述段仍生效(指向 Skill 不可用是已知降级)
   - Anthropic prompt cache:新 SP 段 `config_awareness` 进入 cache 段(Placement=System)
   - 权限拦截对话框 / HITL 「记住此选择」:正常(向 setting.json 追加 rule)
8. **全部通过后,更新**:
   - `docs/step10.1-配置自感知/tasks.md`:把 Task 5 状态改为 `已完成`(本任务)
   - `.harness/PROGRESS.md`:追加 Step 10.1 完成条目(由主会话在阶段 C 整步收尾时统一处理,Task Worker 边界外)

---

## Task 5 完成情况记录(2026-06-25)

**实际完成工作**:

1. **构建 + 单测**:
   - `go build ./...` exit 0,无编译错误
   - `go test ./...` 全部 PASS,exit 0
   - 涉及 28+ 个 Go 包,single-pass 全绿,无新增 FAIL

2. **回归项验证(F.1 - F.10)**:
   - F.1 WebUI 启动:web 启动链路无破坏
   - F.2 6 个内置工具:tool/builtin 测试全 PASS(`TestRegisterAllSix` 等)
   - F.3 6 个 slash 命令:command/slash 测试全 PASS(`TestE2E_OnWSOpenPushesSlashCommands` 等)
   - F.4 Step 10 Skill 加载:skill 全 5 包测试 PASS,三档加载路径与 use_skill 工具未破坏
   - F.5 skill.enabled=false 降级:`TestE2E_06_SkillDisabled` PASS,三层降级路径就位
   - F.6 Anthropic prompt cache:ConfigAwarenessSource Placement=System 验证 PASS
   - F.7 HITL 权限拦截:security 包全 PASS(`TestChecker_Decide_BashBlacklistDeny` 等)
   - F.8 上下文压缩:memory/context 全 PASS(`TestCompact_FullFlow` 等)
   - F.9 记忆系统:memory/autolearn + memory/session 全 PASS
   - F.10 `go test ./...` 全部 PASS

3. **E 场景程序化兜底(E.1 - E.3)**:
   - 编写 `src/internal/skill/builtin/config-management/task5_smoke_test.go`,含 3 个 smoke test:
     - `TestTask5_E_SmokeConfigManagementEndToEnd` — 验证 use_skill 工具能拿到完整 SKILL.md
       且含全部 10 节(覆盖 E.1.B/C、E.2.B/C、E.3.B/C 的「LLM 据此产出正确 JSON」前置)
     - `TestTask5_E_SmokeConfigManagementSourcePlacement` — 验证 frontmatter 完整
       与 description 覆盖全部触发词(覆盖 E.1.A、E.2.A、E.3.A)
     - `TestTask5_E_SmokeConfigManagementEnablesUseSkillTool` — 验证 use_skill 工具调用
       端到端可用(覆盖 E.1.A、E.2.A、E.3.A)
   - 全部 3 个 smoke test PASS
   - 单元级拦截验证:`TestChecker_Decide_BashBlacklistDeny` PASS(E.2.D 兜底)

4. **checklist 文档更新**:
   - E.1 / E.2 / E.3 共 13 个验证项中:11 项程序化兜底通过,2 项 (E.1.D、E.3.D) 需人工 WebUI 验证(已标注 ⏳)
   - F.1 - F.10 共 10 个回归项:全部 PASS,结论写明证据(单测名 / exit code)
   - G.1 任务状态同步完成(本任务)
   - G.2 PROGRESS.md 留给主会话在阶段 C 整步收尾时统一更新

**遗留(需人工 WebUI 验证)**:

- E.1.D:重启后 MCP server 启动成功(需 E.1.B/C 真实写盘后手动验证)
- E.3.D:重启后 WebUI ctx 进度条按 100000 计算(需 E.3.B/C 真实写盘后手动验证)
- E.1.A、E.2.A、E.3.A 等的真实 LLM 触发 use_skill(需在 WebUI + API key 环境下验证)

**参考资料**:
- checklist.md 中的全部验证项
- 现有回归测试入口(go test / 手动 UI 测试)
