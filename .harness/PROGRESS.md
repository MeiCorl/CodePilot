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
| 已完成步骤数 | 16（Step 1 / Step 1.1 / Step 1.2 / Step 1.3 / Step 1.4 / Step 2 / Step 3 / Step 4 / Step 5 / Step 6 / Step 7 / Step 8 / Step 9 / Step 9.1 / Step 10 / Step 10.1） |
| 当前最新版本 | V1.7.0                                                |
| 进行中步骤  | —                                                    |
| 下一步骤   | Step 11 — Hook 系统（需先 `/specs` 触发需求澄清）                      |
| 最近更新   | 2026-06-25（**bugfix**: 内置 Skill 加载新增「workdir-relative src」三段式 fallback + 启动期可观测性 warn,任意路径启动均能看到 config-management；详见 [扫尾说明](#step101-扫尾说明内置-skill-三段式-fallback)）|

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

**架构层覆盖度速览**（5 层）：

| 架构层       | 已落地组件                                                                       | 待落地                          |
| --------- | --------------------------------------------------------------------------- | ---------------------------- |
| 第 1 层：交互层 | WebUI（HTTP + WS + 富文本 + 流式渲染 + SP 可观测 + 双栏 diff + 权限对话框 + MCP/Skill 徽标）       | —                            |
| 第 2 层：引擎层 | 对话管理 + Agent Loop（ReAct 迭代）+ System Prompt（Builder + 5 Source + Anthropic 缓存）   | —                            |
| 第 3 层：工具层 | 6 内置工具 + 路径沙箱 + Bash 黑名单 + MCP 客户端 + 快捷命令系统 + **Skill 系统（Step 10）+ config-management 自感知 Skill（Step 10.1）**              | Hook（Step 11）、SubAgent（Step 12） |
| 第 4 层：记忆层 | 会话持久化 + 高级上下文管理（两层压缩 / 熔断 / 紧急压缩）+ 自动学习记忆（4 类分级 / 独立 LLM 回顾 / 敏感脱敏）                | —                            |
| 第 5 层：安全层 | 权限系统（三层模式 + 可配置规则 + HITL + 黑名单 + 路径沙箱）                                          | —                            |

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
