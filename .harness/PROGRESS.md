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
| 已完成步骤数 | 18（Step 1 / Step 1.1 / Step 1.2 / Step 1.3 / Step 1.4 / Step 2 / Step 3 / Step 4 / Step 5 / Step 6 / Step 7 / Step 8 / Step 9 / Step 9.1 / Step 10 / Step 10.1 / Step 10.2 / Step 11） |
| 当前最新版本 | V1.9.0                                                |
| 进行中步骤  | —                                                    |
| 下一步骤   | Step 12 — SubAgent（需先 `/specs` 触发需求澄清）                      |
| 最近更新   | 2026-06-29（Step 11 Hook 系统完成:12 类事件 + 条件匹配 + command/http/prompt/agent action + Agent Loop/ToolHandler/Session/Compact 集成 + config-management/codebase-overview 补充 Hook 文档）|

进度条：

```
[██████████████████████████████████████░] 11/12 主线步骤完成（Step 1-11 已完成 + Step 9.1 / Step 10.1 / Step 10.2 子步骤已落地，Step 12 待开始）
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
| 10.1   | **配置自感知（V1.7.0）**                               | 2026-06-25 | 新增 `ConfigAwarenessSource`(~78 token)把「改配置 → 加载 config-management Skill」写入常驻 SP；新建 config-management builtin Skill,采用「总索引(< 6KB)+ reference 按需子文档」覆盖 setting.json 6+ section + 顶层 LLM 参数 + 改写工作流 + 错误排查；`build.ps1` / `Makefile` 把 Skill 资源目录落到 exe-dir/internal/skill/builtin/ | [docs](../docs/step10.1-配置自感知/)                                     |
| 10.2   | **代码自感知（V1.8.0）**                               | 2026-06-26 | 新增 `CodebaseAwarenessSource`(~48 token)把「CodePilot 自身原理 → 加载 codebase-overview skill」写入常驻 SP；新建 codebase-overview builtin Skill,「总索引(< 6KB)+ 按需子文档(< 16KB/篇)」二级加载 12 篇已实现模块 md + 1 篇 stub(SubAgent)；`buildSkillReadRoots` 把 builtin/user/project 三档 skill 根作为 ReadFile 沙箱附加只读根,使 LLM 可读 SKILL.md 同目录子文件 | [docs](../docs/step10.2-代码自感知/)                                     |
| 11     | **Hook 系统（V1.9.0）**                               | 2026-06-29 | 12 类生命周期事件 + 条件 DSL + command/http/prompt/agent 四类 action；HookEngine 支持 once/async/Stats/Shutdown；已接入 Agent Loop / ToolHandler / Session / Compact / WebUI 状态栏，并补充 `config-management` Hook 配置说明与 `codebase-overview` Hook 实现导览 | [docs](../docs/step11-Hook系统/)                                     |

**架构层覆盖度速览**（5 层）：

| 架构层       | 已落地组件                                                                       | 待落地                          |
| --------- | --------------------------------------------------------------------------- | ---------------------------- |
| 第 1 层：交互层 | WebUI（HTTP + WS + 富文本 + 流式渲染 + SP 可观测 + 双栏 diff + 权限对话框 + MCP/Skill 徽标）       | —                            |
| 第 2 层：引擎层 | 对话管理 + Agent Loop（ReAct 迭代）+ System Prompt（Builder + 8 Source + Anthropic 缓存）   | —                            |
| 第 3 层：工具层 | 6 内置工具 + 路径沙箱 + Bash 黑名单 + MCP 客户端 + 快捷命令系统 + **Skill 系统（Step 10）+ config-management 自感知 Skill（Step 10.1）+ codebase-overview 代码自感知 Skill（Step 10.2）+ Hook 系统（Step 11）+ config-management Hook 配置说明 + codebase-overview Hook 实现导览**              | SubAgent（Step 12） |
| 第 4 层：记忆层 | 会话持久化 + 高级上下文管理（两层压缩 / 熔断 / 紧急压缩）+ 自动学习记忆（4 类分级 / 独立 LLM 回顾 / 敏感脱敏）                | —                            |
| 第 5 层：安全层 | 权限系统（三层模式 + 可配置规则 + HITL + 黑名单 + 路径沙箱 + buildSkillReadRoots 附加根放行）                | —                            |

---

## 🕓 待完成步骤

> 下列步骤按 [PROJECT.md](./PROJECT.md) 计划顺序排列，开始下一步前请先用 `/specs` 触发需求澄清并生成 spec / tasks / checklist 三文档。

| 编号  | 步骤名       | 所属架构层 | 状态    | 计划目录                       |
| --- | --------- | ----- | ----- | -------------------------- |
| 12  | SubAgent  | 工具层   | ⏳ 待开始 | `docs/step12-SubAgent/`    |

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




