---
name: config-management
description: "管理 CodePilot 自身配置(setting.json)的总索引。用于添加、删除、修改、查看 MCP server、permissions 权限规则、Hook 钩子、上下文压缩、记忆系统、Skill 系统、工具白名单、模型/API key/base_url、超时、上下文窗口、Agent 循环参数等配置；当用户提到加/配/改/删/设置/管理/开启/关闭 MCP、permission/权限、hook/hooks/钩子、event/action/condition、compaction/context window/压缩、memory/记忆、skill/技能、tools/tool、model/模型/API key/base_url/timeout/retries/working directory 等配置场景时使用。本 Skill 只提供简介和索引；详细 JSON schema、默认值、示例、重启要求和排错说明按需读取 reference/*.md。改写配置一律使用 ReadFile + EditFile/WriteFile。"
---

# config-management — CodePilot 配置管理总索引

本 Skill 是 `setting.json` 的轻量入口索引：

- `SKILL.md` 只保留配置域导航、读取规则和改写原则。
- `reference/*.md` 保存各模块的 JSON schema、示例、默认值、是否需要重启和错误排查。
- 回答或改写配置前，只读取用户问题涉及的 reference 文件，避免一次加载全部配置细节。

## 加载方式

拿到本 Skill 后，先根据下方索引定位 reference 文件；如果 `use_skill` 返回了 Skill 根路径提示，复制提示里的真实路径，只替换 `reference\<file>` 文件名部分。

读取细节时使用 `ReadFile`，不要用 shell 搜索子文档；改写配置时使用 `ReadFile` + `EditFile`/`WriteFile`，不要凭空重写整份 JSON。

## 配置文件位置

| 层级 | 路径 | 适用场景 |
|------|------|---------|
| 全局 | `~/.codepilot/setting.json` | 跨项目默认偏好、通用 MCP、通用权限规则 |
| 项目级 | `<cwd>/.codepilot/setting.json` | 当前仓库专属配置、团队随项目分发的规则 |

选择原则：用户说“当前项目/这个仓库/这里”时改项目级；说“所有项目/全局/默认/以后都用”时改全局；措辞模糊时先询问写入位置。

## 模块索引

| # | 配置域 | 何时读取 | 文件 |
|---|--------|----------|------|
| 1 | 配置文件总览 | 路径、合并规则、全局与项目级选择 | `reference/overview.md` |
| 2 | MCP | 新增、修改、禁用 stdio/http MCP server 或调整握手/缓存超时 | `reference/mcp.md` |
| 3 | permissions | 配置 allow/deny/ask 规则、HITL 写回、权限模式 | `reference/permissions.md` |
| 4 | compaction | 调整上下文压缩阈值、关闭压缩、排查压缩触发 | `reference/compaction.md` |
| 5 | memory | 开关自动学习记忆、调整 MEMORY.md 索引注入上限 | `reference/memory.md` |
| 6 | skill | 开关 Skill 系统、调整单个 SKILL.md 截断阈值 | `reference/skill.md` |
| 7 | tools | 设置 LLM 可见工具白名单、隐藏或禁用工具 | `reference/tools.md` |
| 8 | hook | 配置 Hook 事件、condition DSL、command/http/prompt/agent action | `reference/hook.md` |
| 9 | 顶层 LLM / Agent 参数 | 修改 provider/model/api_key/base_url/token/timeout/context window/迭代上限 | `reference/llm-agent.md` |
| 10 | 改写工作流 | 需要实际编辑 setting.json 时读取，确认读写、验证和重启流程 | `reference/workflow.md` |
| 11 | 错误排查 | JSON 语法、字段拼写、类型错误、启动校验失败 | `reference/troubleshooting.md` |

Hook 的源码实现与调度链路属于代码功能感知，详见 `codebase-overview/reference/hook-system.md`；本 Skill 只负责 Hook 配置写法。

## 改写原则

1. 先读取目标 `setting.json`，确认当前结构和写入层级。
2. 只修改用户要求的配置域；不顺手重排、不格式化无关字段、不删除未知字段。
3. 新增复杂 section 前，先读取对应 reference 文件中的 schema 和完整示例。
4. `permissions.rules[]` 运行期生效；其余主要配置通常需要重启，具体以对应 reference 为准。
5. 改完后给出验证信号，例如 MCP 连接日志、Hook 状态栏、工具白名单变化或首次 LLM 请求成功。

## 维护说明

- 入口文件保持轻量，只追加索引项，不放大段 schema 或完整示例。
- 详细说明统一放在 `reference/` 一层目录下，避免深层跳转。
- 新增配置域时：添加 `reference/<module>.md` → 在模块索引追加一行 → 保持 frontmatter 的触发词覆盖该配置域。
