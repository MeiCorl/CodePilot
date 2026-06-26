# CodePilot 项目进度

> 本文档记录 CodePilot 项目的整体实现进度，每完成一步功能开发后须同步更新本文档。
>
> - 计划全景与系统架构见 [PROJECT.md](./PROJECT.md)
> - 各步骤详细 spec / tasks / checklist 见 `docs/{step_n-idea_name}/` 目录
> - **维护规约**：每次 `sdd-run` 或 `specs` 技能完成一个步骤的全部 Task 后，必须在 [📊 总览](#-总览)、[✅ 已完成步骤](#-已完成步骤) 与 [🕓 待完成步骤](#-🕓-待完成步骤) 三处同步更新

---

## 📊 总览

| 指标     | 数值                                                    |
| ------ | ----------------------------------------------------- |
| 计划总步骤数 | 12（含子步骤后实际更多）                                         |
| 已完成步骤数 | 17（Step 1 / Step 1.1 / Step 1.2 / Step 1.3 / Step 1.4 / Step 2 / Step 3 / Step 4 / Step 5 / Step 6 / Step 7 / Step 8 / Step 9 / Step 9.1 / Step 10 / Step 10.1 / Step 10.2） |
| 当前最新版本 | V1.8.0                                                |
| 进行中步骤  | —                                                    |
| 下一步骤   | Step 11 — Hook 系统（需先 `/specs` 触发需求澄清）                      |
| 最近更新   | 2026-06-26（**bugfix 重做**: use_skill 提示中 `<module>` 占位符导致 LLM 把字面尖括号拼进 file_path,改用 3 条完整真实路径示例 + 明确禁止事项;详见 [Step 10.2 Bugfix 重做](#-step-102-bugfix-重做说明use_skill-提示中-module-占位符翻车)）|

进度条：

```
[████████████████████████████████████░░] 10/12 主线步骤完成（Step 1-9 已完成 + Step 9.1 子步骤完成 + Step 10 + Step 10.1 已落地，Step 11-12 待开始）
```

---

## ✅ 已完成步骤

> 每步的 Task 数 / 核心交付能力 / 验证用例详见 `docs/step{n}/` 目录下的 `tasks.md` 与 `checklist.md`。

| #      | 步骤（版本）                                       | 完成时间       | 一句话能力                                                                                       | 设计文档                                                                |
| ------ | --------------------------------------------- | ---------- | -------------------------------------------------------------------------------------------- | ------------------------------------------------------------------- |
| 1      | LLM 打通（V1.0.0）                                | —          | Anthropic + OpenAI 双 Provider 适配，统一 `ContentBlock` 抽象；配置驱动 / 会话 JSON 持久化 / 异步日志 / 流式响应 + 中断         | [docs](../docs/step1-LLM打通/)                                          |
| 1.1    | UI 界面重构 TUI→WebUI（V1.0.1）                     | —          | 移除 Bubble Tea 全家桶；`embed.FS` + HTTP/WS + 跨平台浏览器调起；五区布局 + 深色美学 + 最小可用斜杠命令                         | [docs](../docs/step1.1-UI界面重构/)                                         |
| 1.2    | 对话栏富文本渲染（V1.0.2）                              | —          | highlight.js v11 18+ 语言；代码块 header + Copy；JSON 智能校验；DOMPurify XSS 防护；流式结束后一次性 enhance              | [docs](../docs/step1.2-对话栏文本渲染/)                                        |
| 1.3    | WebUI 流式渲染（V1.0.5）                             | 2026-06-04 | 流式 Markdown 实时渲染 + 未闭合围栏自动补齐 + 80ms 防抖合并 + 首个 delta 立即出现 + 流结束后最终增强                              | [docs](../docs/step1.3-WebUI流式渲染/)                                        |
| 1.4    | WebUI 工具展示优化（V1.0.7）                           | 2026-06-07 | WriteFile/EditFile 头部「查看改动」按钮 + 双栏 diff 弹窗 + diff-match-patch 行级 diff + 按后缀 hljs + 进程内 FileDiffStore      | [docs](../docs/step1.4-WebUI工具展示优化/)                                       |
| 2      | 工具系统集成（V1.0.3）                                 | —          | 统一 `Tool` 接口 + Registry；5 个内置工具（ReadFile/WriteFile/Bash/Glob/Grep）；Anthropic/OpenAI tool_use 适配；路径沙箱 + Bash 黑名单 | [docs](../docs/step2-工具系统集成/)                                           |
| 3      | ReAct 与 Agent Loop（V1.0.4）                      | 2026-06-04 | ReAct 循环迭代 + 多工具并行 + 迭代上限 + 溢出保护 + 优雅中断 + 工具错误回灌 + 5 种终止原因枚举                                  | [docs](../docs/step3-ReAct与Agent%20Loop实现/)                              |
| 4      | System Prompt 设计（V1.0.6）                        | 2026-06-06 | Builder + 4 Source（static/environment/agents_md/memory）；AGENTS.md 双层合并；Anthropic Prompt Caching；SP 可观测性 + Export | [docs](../docs/step4-System%20Prompt设计/)                                  |
| 5      | 权限系统设计                                         | 2026-06-07 | 三层模式 + 可配置规则 + 多层配置合并 + HITL 三种授权范围 + 黑名单 + 路径沙箱策略化 + WebUI 权限确认对话框                            | [docs](../docs/step5-权限系统设计/)                                           |
| 6      | MCP 协议实现（V1.3.0）                               | 2026-06-09 | JSON-RPC 2.0 + stdio/HTTP 双传输 + 三阶段握手 + 连接池 + 适配器自动注册 + 指数退避重连 + 真实启动冒烟 healthy=2 tools=4        | [docs](../docs/step6-MCP协议实现/)                                           |
| 7      | 上下文管理（V1.4.0）                                  | 2026-06-16 | 两层压缩（L1 工具结果存盘预览 + L2 LLM 摘要）+ 撞墙紧急压缩 + 会话级熔断 + 历史归档 + 全阈值可配置                                  | [docs](../docs/step7-上下文管理/)                                            |
| 8      | 自动学习记忆（V1.5.0）                                 | 2026-06-18 | 4 类记忆分级存储 + MEMORY.md 索引注入召回 + 后台异步回顾独立 LLM 通道 + 敏感信息双层防护 + 配置驱动 + ReadFile 沙箱白名单按需读取              | [docs](../docs/step8-记忆系统/)                                             |
| 9      | 快捷命令系统（V1.5.0，**回顾式补记**）                       | 2026-06-22 | 6 条内置命令（`/new` `/sessions` `/resume` `/clear` `/compact` `/dump`）+ 候选下拉 + 业务消息路由 + 调试导出           | [docs](../docs/step9-快捷命令系统/)                                           |
| 9.1    | Slash 命令注册后端化（V1.5.0）                          | 2026-06-23 | `SlashCommand` 接口 + `Registry` 注册表 + 6 条 builtin 委托既有 handler + WS Open 推送 `slash_commands` + WebUI 零硬编码 | [docs](../docs/step9.1-Slash注册后端化/)                                      |
| 10     | **Skill 系统（V1.6.0）**                             | 2026-06-24 | 三档优先级目录型 Skill + SKILL.md 解析 + `use_skill` 工具按需加载 + 自动注册为 slash 命令 + 渐进式披露 + `/skills` 列表面板 + 紫色徽标 + `enabled=false` 三层降级 | [docs](../docs/step10-Skill系统/)                                          |
| 10.1   | **配置自感知（V1.7.0）**                               | 2026-06-25 | 新增 `ConfigAwarenessSource`(~78 token)把「改配置 → 加载 config-management Skill」写入常驻 SP；新建 config-management builtin Skill 覆盖 setting.json 6+ section + 顶层 LLM 参数 + 改写工作流 + 错误排查(< 64KB)；`build.ps1` / `Makefile` 把 SKILL.md 落到 exe-dir/skills/ | [docs](../docs/step10.1-配置自感知/)                                     |
| 10.2   | **代码自感知（V1.8.0）**                               | 2026-06-26 | 新增 `CodebaseAwarenessSource`(~48 token)把「CodePilot 自身原理 → 加载 codebase-overview skill」写入常驻 SP；新建 codebase-overview builtin Skill,「总索引(< 6KB)+ 按需子文档(< 16KB/篇)」二级加载 11 篇已实现模块 md + 2 篇 stub(Hook/SubAgent)；`buildSkillReadRoots` 把 builtin/user/project 三档 skill 根作为 ReadFile 沙箱附加只读根,使 LLM 可读 SKILL.md 同目录子文件 | [docs](../docs/step10.2-代码自感知/)                                     |

**架构层覆盖度速览**（5 层）：

| 架构层       | 已落地组件                                                                       | 待落地                          |
| --------- | --------------------------------------------------------------------------- | ---------------------------- |
| 第 1 层：交互层 | WebUI（HTTP + WS + 富文本 + 流式渲染 + SP 可观测 + 双栏 diff + 权限对话框 + MCP/Skill 徽标）       | —                            |
| 第 2 层：引擎层 | 对话管理 + Agent Loop（ReAct 迭代）+ System Prompt（Builder + 7 Source + Anthropic 缓存）   | —                            |
| 第 3 层：工具层 | 6 内置工具 + 路径沙箱 + Bash 黑名单 + MCP 客户端 + 快捷命令系统 + **Skill 系统（Step 10）+ config-management 自感知 Skill（Step 10.1）+ codebase-overview 代码自感知 Skill（Step 10.2）**              | Hook（Step 11）、SubAgent（Step 12） |
| 第 4 层：记忆层 | 会话持久化 + 高级上下文管理（两层压缩 / 熔断 / 紧急压缩）+ 自动学习记忆（4 类分级 / 独立 LLM 回顾 / 敏感脱敏）                | —                            |
| 第 5 层：安全层 | 权限系统（三层模式 + 可配置规则 + HITL + 黑名单 + 路径沙箱 + buildSkillReadRoots 附加根放行）                | —                            |

---

## 🕓 待完成步骤

> 下列步骤按 [PROJECT.md](./PROJECT.md) 计划顺序排列，开始下一步前请先用 `/specs` 触发需求澄清并生成 spec / tasks / checklist 三文档。

| 编号  | 步骤名       | 所属架构层 | 状态    | 计划目录                       |
| --- | --------- | ----- | ----- | -------------------------- |
| 11  | Hook 系统   | 工具层   | ⏳ 待开始 | `docs/step11-Hook系统/`      |
| 12  | SubAgent  | 工具层   | ⏳ 待开始 | `docs/step12-SubAgent/`    |

---

## 🛠 Step 10.1 扫尾说明：内置 Skill 三段式 fallback

**触发场景**：用户在 `build/dist` 启动 CodePilot 时,`/skills` 模态框的「内置级」tab 能看到 `config-management`;但把同一个 binary 复制到其他路径(例如 `f:\CodePilot\`)启动,「内置级」tab 为空。复现后用户报 bug。

**根因**:

1. **embedded 路径在老 binary 中是空的**。`src/internal/skill/builtin/builtin.go` 中的 `//go:embed */SKILL.md` 是 V1.7.0(Step 10.1)新增的逻辑;Step 10 提交的 `build/dist/CodePilot.exe` 编译时,`builtin.go` 还是旧版(只定义了 `DirName` 常量,无 `//go:embed`、无 `Embedded()` 函数),`embeddedFS` 嵌入的 entries 为 0。
2. **exeDir 路径只在 dist 启动时有效**。Step 10.1 加了 `Makefile`/`build.ps1` 把 `SKILL.md` 复制到 `<dist>/internal/skill/builtin/`。但当 binary 被复制到其他路径启动时,`<execDir>/internal/skill/builtin/` 目录不存在,该 fallback 静默 return nil。
3. **结果**:老 binary 启动在 dist 时(两条路有一路通)能看到内置 skill;启动在其他路径时(两条路全失败)`/skills` 内置栏为空,且无任何 warn 日志,用户无法定位。

**修复方案**(`src/internal/skill/scanner.go`):

- `LoadAll` 的内置级加载从「单段 `scanLevel`」改为「**三段式 fallback**」:
  1. **embedded 路径** — `scanEmbeddedBuiltins` 读 `embeddedFS`(新 binary 编译时 SKILL.md 在源码目录,自动嵌入);
  2. **exeDir 路径** — `scanLevelWithOptions(<execDir>/internal/skill/builtin, SourceBuiltin, SkipDuplicateSameSource: true)`(保留 release 模式 dist 副本加载);
  3. **workdir-relative src 路径(新增)** — `findSrcBuiltinFallback(workdir)` 从 workdir 向上 16 级找 `src/internal/skill/builtin/`(项目标准 layout),找到第一个含至少一个 SKILL.md 子目录的即返回;命中后再走 `scanLevelWithOptions(SkipDuplicateSameSource: true)`,与前两段重复的 entry 跳过。
- 三段「或」关系,任意一段成功即加载;三段全失败时**显式 warn**(`skill 内置级加载为空 (embedded / exeDir / src fallback 全部未命中)`,带 workdir / exec_dir / 实际扫描路径),用户可据此定位是「重新 `make build`」还是「项目无 src 目录」。

**为什么不在 builtin 包用 `runtime.Caller(0)`**:Go 编译为 binary 后 `runtime.Caller` 返回的是虚拟路径,不会指向真实文件系统,无法用作 fallback 路径;只有「workdir 向上找 `src/`」这种基于约定的查找在 dev/release 两种模式下都能用。

**验证**(`f:\CodePilot\build\dist\CodePilot.exe` V1.7.0-patch,2026-06-25 18:39 重编):

| 启动路径 | workdir | exec_dir | 命中段 | /skills 内置栏 | Skill count 日志 |
| --- | --- | --- | --- | --- | --- |
| `f:\CodePilot\` | `F:\CodePilot` | `F:\CodePilot` | 段 1 (embed) + 段 3 (workdir fallback) | ✅ 显示 config-management | `count: 4` |
| `f:\CodePilot\build\dist\` | `F:\CodePilot\build\dist` | `F:\CodePilot\build\dist` | 段 1 (embed) + 段 2 (exeDir) + 段 3 (workdir fallback) | ✅ 显示 config-management | `count: 4` |

**附带的清理**:

- 清理 IDE/工具残留的 5 个「带数字 ID 后缀」的临时文件(`*.go.[19 位数字]`,如 `builtin.go.4588911273121966770`)。这些文件不影响 Go embed(`*` 只匹配子目录),但污染 git status。
- 在 `.gitignore` 加入 `*.[0-9]{19}` 规则,防止后续继续污染。

---

## 🐛 Step 10.2 Bugfix 扫尾说明：use_skill 前置 Skill 根路径提示

**触发场景**:用户从 `C:\Users\Administrator\Desktop\.tmp-test` 等非项目目录启动 CodePilot(dist 二进制),问「CodePilot 的 X 模块怎么实现」时,Agent 拿不到 `reference/*.md` 子文档。Agent 诚实回答「没有可访问的 `reference/context-management.md`」,只能基于「通用 LLM Agent 设计」综合推断,**与 CodePilot 实际实现脱节**。

**根因**(Step 10.2 Task 8 端到端验证盲点):

1. **`scanEmbeddedBuiltins` 把 builtin skill 的 `RootPath` 设为 `embedded://internal/skill/builtin/<name>`**(虚拟路径,见 `src/internal/skill/scanner.go:268`)。任意路径启动 binary 都会优先走 embedded 路径(`embeddedFS` 在编译期嵌入),所以用户场景里 LLM 拿到的 `RootPath` 实际是 `embedded://` 虚拟路径,无法被 `ReadFile` 沙箱识别。
2. **SKILL.md 「加载方式」段只说「`<skill_root>` 是 `<exec_dir>/internal/skill/builtin/`」,但没给 LLM 实际绝对路径**。LLM 拿到 SKILL.md 后必须自己拼路径,然而它不知道 `exec_dir` 在哪个目录,在用户场景里拼出的路径全部失败。
3. **Step 10.2 Task 8 E.1-E.5 端到端场景**只跑了「源码层 + 单测层 + 数据通路层」,**LLM 真实调用层因无 API key 跳过**,导致这个 bug 没被第一时间捕获。

**修复方案**(用户确认采用方案 E — `use_skill` 工具返回时前置动态路径提示):

- **改 `src/internal/skill/adapter/tool.go`** — `useSkillTool` 新增 `rootBySource map[skill.Source]string` 字段;`NewUseSkillTool` 签名加第二个参数;`Execute` 在 `s.FullContent()` 返回前调 `buildRootHint(s)` 前置 XML 注释段;新 `buildRootHint` 私有方法生成格式化的路径提示(命中根时含绝对路径,不命中时显式告知「embedded-only」)
- **改 `src/main.go`** — 新增 `findActiveBuiltinRoot(workdir, execDir)` 顶层 helper(优先 execDir 副本,fallback 到 workdir 向上 16 级找 `src/internal/skill/builtin/`,与 `scanner.findSrcBuiltinFallback` 同构),加 `hasBuiltinSkillMDAt` 防御函数;新增 `buildSkillRootBySource(workdir, homeDir, execDir) map[skill.Source]string` 把三档 Skill 根组装成 map;`use_skill` 注册点改为 `NewUseSkillTool(skillReg, rootBySource)`,并新增 `use_skill 路径提示注入就绪` 启动期可观测性日志
- **改 `src/internal/skill/builtin/codebase-overview/SKILL.md`** — 「加载方式」段更新,明确告诉 LLM「`use_skill` 返回时会前置 Skill 根路径提示段,按提示的绝对路径 ReadFile 子文档」,删除之前让 LLM 自己拼绝对路径的指引
- **改 `src/internal/skill/adapter/tool_test.go`** — 9 处现有 `NewUseSkillTool(...)` 调用适配新签名(传 `nil`);新增 3 个测试: `TestUseSkillTool_Execute_AddsRootHint` / `TestUseSkillTool_Execute_NoHintWhenRootMissing` / `TestUseSkillTool_Execute_NilRootMap_OmitsHint`
- **改 `src/internal/skill/e2e_test.go`** — 2 处 `NewUseSkillTool` 调用适配新签名
- **新增 `src/internal/skill/builtin/codebase-overview/bugfix_smoke_test.go`** — 真实 LoadAll + dist 部署场景 smoke test,验证 use_skill 实际返回内容以 `<!-- [CodePilot Skill 根路径提示] -->` 开头并含 builtin 根绝对路径

**验证**(dist V1.8.0-patch,2026-06-26 重编,14.76MB):

| 启动路径 | workdir | execDir | builtin 根解析 | use_skill 返回(前 500 字符) |
| --- | --- | --- | --- | --- |
| `F:\CodePilot\src\internal\skill\builtin\codebase-overview\` (test cwd) | test cwd | `F:\CodePilot\build\dist` | 段 1 (execDir 副本) ✅ | `<!-- [CodePilot Skill 根路径提示] --> 本 Skill 名称 = codebase-overview 本 Skill 来源 = builtin 本 Source 实际可读文件系统根 = F:\CodePilot\build\dist\internal\skill\builtin` |
| `C:\Users\Administrator\Desktop\.tmp-test\` (用户原 bug 场景) | (非项目目录) | (binary 实际所在目录) | 段 1 (execDir 副本,优先命中) ✅ | 同上,LLM 拿到绝对路径后 ReadFile 沙箱 `buildSkillReadRoots` 放行 `<execDir>/internal/skill/builtin/...` |

**测试结果**(30 个包全绿):
- `go test ./internal/skill/...` 5 个包 PASS
- `go test ./...` 30 个包 PASS(含本次新增 3 个 use_skill 路径提示测试 + 1 个 bugfix smoke test)
- `go vet ./...` 零问题
- `go build ./...` 成功
- `powershell -File build/build.ps1` 成功,`CodePilot.exe 14.76MB`

**为什么不在 SKILL.md 静态写死绝对路径**:SKILL.md 是静态资源,不同部署方式(dist / dev / 项目子目录)实际绝对路径不同,静态文件无法适配;`use_skill` 是注入动态信息的唯一合规出口。

**为什么不直接拼完整路径给 LLM**(方案 F):会违背「按需加载」承诺,单次 `use_skill` 返回约 120KB,撑爆 LLM 上下文窗口;保留二级加载结构(SP 索引 → use_skill 拿 SKILL.md → 按需 ReadFile 子文档),只前置必要的路径提示。

---

## 🐛🐛 Step 10.2 Bugfix 重做说明:use_skill 提示中 `<module>` 占位符翻车

**触发场景**:上一版 Bugfix(commit `74c2eb6`)重新发布后,用户在 dist binary 实际跑「CodePilot 的 X 模块怎么实现」,**LLM 看到完整 XML 提示段**(确认 path hint 已前置),但 ReadFile 子文档仍然失败。Agent 拿不到 `reference/*.md` 内容,只能基于「通用 LLM Agent 设计」综合推断。

**根因**(用户报现象「LLM 看到完整 XML 提示」后定位):

1. **旧版提示用的是「表达式 + 占位符」语法**,关键这一行:
   ```
   例如本 Skill 的 reference 子文档:ReadFile("F:\CodePilot\build\dist\internal\skill\builtin\codebase-overview\reference\<module>.md")
   ```
2. **LLM 把 `<module>` 当字面拼进 ReadFile.file_path**,而不是替换成 `context-management` / `permission` 等实际模块名。
3. **更深的次生问题**:`use_skill` 提示里同时给了 `ReadFile("<root>" + "/" + Skill 名 + "/" + 子文件相对路径)` 这种伪代码表达式,LLM 在中文语境下未必按插值语义理解,直接当字面串拼接。
4. **结果**:LLM 发出的 ReadFile.file_path 是 `...\reference\<module>.md` 这种字面带尖括号的路径 → 沙箱放行(字符串合法) → `os.Open` 失败(`<module>.md` 文件不存在) → ReadFile 返回 error → LLM 看到错误就放弃了。
5. **第一版 bugfix 的盲点**:`bugfix_smoke_test.go` 只断言「hint 含 builtin 根路径」,不检查提示措辞是否让 LLM 能正确解析,所以这种「提示存在但措辞误导」的 bug 第一次没被捕获。

**修复方案**(用户确认 LLM 拿到完整 XML 提示后定位):

**改 `src/internal/skill/adapter/tool.go` 的 `buildRootHint`** — 完全替换旧版「伪代码表达式 + 占位符」提示,新版格式:

```
<!--
[CodePilot Skill 根路径提示 — Step 10.2 Bugfix 重做]
本 Skill 名称 = codebase-overview
本 Skill 来源 = builtin
本 Source 实际可读文件系统根 = F:\CodePilot\build\dist\internal\skill\builtin

【读取该 Skill 子文档的方式 — 直接复制下面的真实路径示例】
ReadFile 工具的 file_path 参数必须是完整绝对路径,不能含有任何尖括号或花括号占位符。
直接复制下面的示例路径,只把最后那段文件名替换成你想读的实际子文档名,其它部分一字不动:

  F:\CodePilot\build\dist\internal\skill\builtin\codebase-overview\reference\context-management.md
  F:\CodePilot\build\dist\internal\skill\builtin\codebase-overview\reference\permission.md
  F:\CodePilot\build\dist\internal\skill\builtin\codebase-overview\reference\tool-system.md

【禁止事项 — 上一版提示在此处翻车,务必不要犯同样的错】
- 不要写任何形式的占位符表达式(尖括号里夹文字、花括号里夹文字、Skill 名做变量等)
- 不要把多个路径段用 + 号拼接写进 tool input — tool input 只接 file_path 一个完整字符串
- 不要用 find / ls / dir 等 Bash 命令搜索子文档(本 Skill 的 SKILL.md 模块索引表已经列出全部子文件名)
- 复制示例路径后,只需把最后那段 reference 后的文件名替换成「模块索引」表里的目标子文档名

【沙箱放行确认】这个根目录已经被沙箱放行为 ReadFile 附加只读根,ReadFile 调用不会被路径限制拦截,可以放心使用绝对路径。
-->

# Skill: codebase-overview
...
```

**关键设计原则**:
- 3 条**完整可复制**的真实路径示例(根 + Skill 名 + 子文件名,反斜杠全写死),LLM 只需替换最后那段文件名,其它部分一字不动;
- 明确列出 4 条禁止事项,把上一版翻车的具体错误(占位符伪代码)用大白话写进提示;
- 提示**自身**也禁止出现任何 `<...>` 或 `{...}` 占位符(连「拼路径模板」这种描述性占位符都不允许),从根本上消除 LLM 误读的诱因;
- 沙箱放行确认段重复一次,消除 LLM 「绝对路径会被沙箱拦」的疑虑。

**改 `src/internal/skill/builtin/codebase-overview/SKILL.md` 的「加载方式」段** — 同步改成「直接复制提示中的示例路径,只替换文件名段」,明确禁止 `ReadFile("<root>/<skill>/<module>")` 这种伪代码。

**新增防回归测试 `src/internal/skill/adapter/tool_test.go::TestUseSkillTool_Hint_NoAngularBracketsPlaceholder`** — 断言 hint 块中:
1. **不含**任何 `<module>` / `<skill>` / `<root>` / `{filename}` / `{module}` 等占位符字面;
2. 含**至少 3 条**以 `\<skill-name>\reference\` 开头的完整真实路径示例;
3. 含「禁止 / 占位符 / 不要」这类反误用关键词。

未来谁改回占位符语法,CI 立即失败。

**验证**(dist V1.8.0-patch-2,2026-06-26 重编,14.76MB):

| 测试 | 结果 |
| --- | --- |
| `go test ./internal/skill/...` | 5 个包 PASS(原 4 个 + 新增 1 个 `TestUseSkillTool_Hint_NoAngularBracketsPlaceholder`) |
| `go test ./...` | 30 个包 PASS(其中 1 个 flaky 的 `TestBusyRejectsConcurrentInput` 重跑通过) |
| `go vet ./...` | 零问题 |
| `go build ./...` | 成功 |
| `powershell -File build/build.ps1` | 成功,`CodePilot.exe 14.76MB` |
| `bugfix_smoke_test` 实际 hint 输出 | 干净无占位符,3 条真实路径示例齐全 ✅ |

**教训**(留给未来):
- 提示措辞 = **LLM 行为的隐形契约**,措辞含糊(尤其混用占位符 + 表达式语法)会被 LLM 当字面执行;
- 验证「提示存在」不够,必须验证「提示内容**真的能让 LLM 按预期行为**」 — 后续 bugfix smoke test 应在断言中加「不含误导性符号」一类反向断言,作为防回归网。

---

## 📌 更新规约

本文档由 `specs` 技能在每完成一个步骤的全部 Task 后自动维护，要求：

1. **触发时机**：某个步骤的 `tasks.md` 中所有 Task 状态均更新为 `已完成`，且 `checklist.md` 全部验证通过
2. **更新内容**：
  - [📊 总览](#-总览)：已完成步骤数、当前最新版本、下一步骤、最近更新日期
  - [✅ 已完成步骤](#-已完成步骤)：在表格中追加一行（步骤 / 完成时间 / 一句话能力 / 设计文档链接）
  - [🕓 待完成步骤](#-🕓-待完成步骤)：删除已完成的对应行
  - **架构层覆盖度速览表**：根据新增能力将相应组件从「待落地」迁到「已落地」
3. **commit 信息**：若新步骤已 release，引用 `git log --oneline` 中的 commit hash 与 message
4. **日期格式**：完成时间统一使用 `YYYY-MM-DD`
5. **简化原则**：每步的 Task 数、核心交付能力详细条目、验证用例清单等**不进入本文档**，详见各步骤 `docs/step{n}/` 目录下的 `tasks.md` 与 `checklist.md`，避免本文档无限膨胀占用上下文
