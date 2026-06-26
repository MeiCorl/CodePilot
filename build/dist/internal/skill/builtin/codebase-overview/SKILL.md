---
name: codebase-overview
description: "了解 CodePilot 自身的架构 / 模块设计 / 实现原理 / 关键流程 — WebUI 交互、多 LLM 适配、会话管理、工具系统、Skill 系统、MCP 集成、上下文管理、自动学习记忆、System Prompt 设计、自我感知系统(Step 10.1)、权限管理、Hook 系统(stub)、SubAgent(stub)等 13 大模块。当用户问「CodePilot 是怎么实现 / 内部原理 / 怎么工作的 / 设计思路 / 架构 / 流程」「X 模块的 Y 怎么做的」「CodePilot 的 X 在哪个文件」「Step N 是怎么实现的」等任何关于 CodePilot 自身的问题时,加载本 Skill 拿到模块索引,再按需 ReadFile 具体子 md(各 module 文件 ≤ 16KB)。Stub 模块(Hook / SubAgent)目前是占位说明,真实实现以 docs/step{N}-*/ 为准。"
---

# codebase-overview — CodePilot 自身实现原理总索引

本 Skill 是「总索引 + 按需子文档」二级加载结构:

- 本文件 = 目录索引(只列模块名 + 一句话 + 文件名,**不包含**实现细节)
- `reference/*.md` = 具体实现原理(单文件 ≤ 16KB,共 13 篇)

## 加载方式

拿到本 Skill 后(用 `use_skill("codebase-overview")`),**tool 返回的最前面会有一段 XML 注释形式的「Skill 根路径提示」**,里面包含 3 条**完整可复制**的真实路径示例(以 `reference\context-management.md` / `reference\permission.md` / `reference\tool-system.md` 为例)。

直接复制提示中的示例路径,只替换文件名段(例如把 `context-management.md` 换成下方「模块索引」表里的目标文件名),其它部分一字不动。

**禁止事项**(实测翻车过的):

- 不要写 `ReadFile("<root>/<skill>/<module>")` 这种占位符伪代码 — LLM 会把 `<module>` 当字面拼进去,ReadFile 失败
- 不要用 `find / ls / dir` 等 Bash 命令搜索子文档
- 拼路径时严格用 `\`(反斜杠),不要用 `/`(正斜杠)

沙箱 ReadFile 附加只读根已注入,ReadFile 调用不会被路径限制拦截。

## 模块索引(13 篇)

| # | 模块 | 一句话简介 | 文件 |
|---|------|-----------|------|
| 1 | UI / WebUI 交互 | WebUI 五区布局、HTTP+WS 双通道、富文本/流式渲染、工具徽标/权限对话框 | `reference/ui-interaction.md` |
| 2 | 多 LLM 适配 | Anthropic / OpenAI 双 Provider、ContentBlock 抽象、tool_use 适配、Prompt Caching | `reference/llm-adapter.md` |
| 3 | 会话管理 | 会话 JSON 持久化、`/new` `/sessions` `/resume` 三命令、恢复与并行 | `reference/session-management.md` |
| 4 | 工具系统 | Tool 接口 + Registry、6 个内置工具、Anthropic/OpenAI tool_use 适配、Agent Loop 调度与 ReAct | `reference/tool-system.md` |
| 5 | Skill 系统 | 三档优先级目录、SKILL.md 解析、use_skill 工具、slash 命令、紫色徽标、enabled 三层降级 | `reference/skill-system.md` |
| 6 | MCP 集成 | JSON-RPC 2.0、stdio/HTTP 双传输、三阶段握手、连接池、适配器自动注册、指数退避重连 | `reference/mcp-integration.md` |
| 7 | 上下文管理 | 两层压缩(L1 存盘预览 + L2 LLM 摘要)、撞墙紧急压缩、会话级熔断、历史归档 | `reference/context-management.md` |
| 8 | 自动学习记忆 | 4 类记忆分级、MEMORY.md 索引注入、后台异步 Reviewer、敏感脱敏 | `reference/auto-memory.md` |
| 9 | System Prompt 设计 | Builder + 7 Source(Static/Environment/AgentsMD/MemoryIndex/SkillsIndex/ConfigAwareness/CodebaseAwareness)、AGENTS.md 双层合并、Anthropic Prompt Caching | `reference/system-prompt.md` |
| 10 | 自我感知系统(Step 10.1 + 10.2) | SP 自感知 + Skill 自描述、「索引 + 按需子文档」二级加载、config-management + codebase-overview 范式 | `reference/self-awareness.md` |
| 11 | 权限管理 | 三层模式 + 可配置规则 + 多层合并 + HITL + 黑名单 + 路径沙箱 + WebUI 确认对话框 | `reference/permission.md` |
| 12 | Hook 系统(STUB) | 规划中,详见 `docs/step11-Hook系统/spec.md` | `reference/hook-system.md` |
| 13 | SubAgent(STUB) | 规划中,详见 `docs/step12-SubAgent/spec.md` | `reference/sub-agent.md` |

## 使用方式

1. 用户问 X 模块相关 → 查表定位文件名
2. `ReadFile("<skill_root>/codebase-overview/reference/<file>")` 读取
3. 据子 md 内容回答用户
4. Stub 模块(Hook/SubAgent)告知用户「CodePilot 规划中,详见 docs/」

## 维护说明

- 每篇 module md ≤ 16KB;超出则按子主题拆分(如 `reference/permission.md` → `reference/permission-design.md` + `reference/permission-check-flow.md`)
- 本索引文件 < 6KB(添加新模块时只追加一行,不要重写整篇)
- 所有 module md 统一放在 `reference/` 子目录,与 SKILL.md 不平级,避免 Skill loader 子目录扫描误识别
- 新增模块时:在 `src/internal/skill/builtin/codebase-overview/reference/` 加文件 → 在本表追加一行 → 不动 frontmatter
