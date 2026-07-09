# CodePilot

一个用 Go 从零实现的 AI Coding Agent（类 Claude Code / Cursor Agent）。通过 Web UI 与 Agent 交互，由 ReAct 循环驱动 LLM 自主调用工具，完成代码编写、文件操作、命令执行等编程任务。

> **项目初衷**：作为转入 AI Agent 开发的练手项目，通过从零实现一个完整的 Coding Agent，深入理解 Agent Loop、工具系统、上下文管理、权限控制、MCP 协议等核心机制。虽不及 Claude Code 等主流产品，但胜在结构清晰、可读可改。

[Go Version](https://go.dev/) · [License](LICENSE)

---

## 概览

![CodePilot overview](key_results/overview.png)

目前 CodePilot 已经打通了从 WebUI 交互、Agent Loop、工具调用、权限控制、MCP 接入、上下文压缩、自动记忆、Slash 命令、Skill 系统、「配置/代码自感知」到 Hook 系统的完整主链路（最新版本 V1.9.1）。上图展示了当前系统能力与架构落地情况。

---

## 📖 项目背景

CodePilot 是一个从零构建的终端 AI Coding Agent（类似 Claude Code / Cursor Agent），使用 Go 语言实现。它以 Web UI 作为主要交互入口，通过 Agent Loop 循环与 LLM 交互并调用内置工具，自主完成用户提出的编程任务。

项目的核心目标是构建一个 **高性能、高可用、高扩展、高安全** 的 AI Agent 系统，采用 5 层垂直分层架构，层间通过标准接口解耦，便于独立演进与功能扩展。

---

## ✨ 功能特性

> 按能力域分组概览，各项的配置项与底层机制详见后文「快速开始」与「核心机制详解」章节。

### 🤖 智能引擎

- **双 LLM 协议**：Anthropic（Claude）+ OpenAI（GPT）双 Provider 适配，统一通过 `ContentBlock` 抽象交互
- **ReAct 推理循环**：思考→决策→行动→观察的多轮迭代，直到 LLM 认为任务完成
- **多工具并行调用**：单次响应含多个工具调用时，按权限分组并行执行
- **迭代上限保护**：默认最大 50 次迭代，达上限注入提示让模型优雅收尾

### 🌐 交互体验

- **Web UI**：HTTP + WebSocket 全双工通信，深色主题界面，自动调起浏览器
- **流式 Markdown 实时渲染**：LLM 输出实时解析为格式化 HTML（标题 / 列表 / 代码块 / 表格）
- **代码语法高亮**：基于 highlight.js 的 18+ 语言自动高亮，含语言标签与一键复制
- **双栏 diff 弹窗**：WriteFile / EditFile 完成态「查看改动」按钮，弹出 Before/After 全文 + 行级高亮
- **项目文件栏**：右侧只读文件浏览器支持根目录/子目录浏览、上级与面包屑导航、文本文件预览、Markdown/JSON/XML/代码高亮，以及路径越界/二进制/大文件保护

### 🧠 上下文与会话

- **两层上下文压缩**：L1 工具结果预览化 + L2 整体历史摘要，无需用户感知（机制详见「核心机制详解」）
- **熔断与紧急压缩**：摘要连续失败 3 次触发会话级熔断；撞墙时紧急压缩兜底一次
- **会话持久化**：append-only JSONL（`messages.jsonl` + `meta.json`），支持会话恢复与历史回放
- **跨项目隔离**：按 workdir basename 分目录存放会话，跨项目天然隔离
- **优雅中断**：中断时保留已完成迭代 + 写入 `abortMarker`，LLM 后续轮次能"理解"前序已取消，支持恢复

### 🧠 自动学习记忆

- **4 类记忆分级存储**：`user_preference` / `user_feedback`（用户级，跨项目生效）+ `project_knowledge` / `reference`（项目级，跟随项目）；单条记忆独立 md 文件 + YAML frontmatter，`MEMORY.md` 按 4 类分块索引
- **MEMORY.md 索引注入召回**：会话启动合并用户级 + 项目级两个索引，外层包 `<memory_index>` 作为 LeadUserMessage 注入（与 AGENTS.md 同构）；体积上限 200 行 / 25KB 截断
- **后台异步回顾器**：监听 `AgentLoop.OnLoopDone`，智能节流（仅 `completed` + 实质输入触发）+ per-session 串行 + panic recover + 全链路静默降级
- **独立 LLM 通道**：回顾用独立 `context.Background()` 派生 + ReviewTimeout(60s)，自带本轮快照做独立 LLM 调用，**绝不回写主对话历史**
- **LLM 比对索引去重 / 更新**：回顾 prompt 注入当前两级索引，由 LLM 决策 new / update；update 覆盖同 slug + 保留 CreatedAt；虚构 slug 被跳过防误覆盖
- **敏感信息双层防护**：回顾 prompt 硬性约束禁止记录密钥 / token / 凭证 + sanitizer 正则兜底脱敏（高熵凭证 / Bearer / 键值对口令三类独立模板）
- **ReadFile 沙箱白名单**：附加只读根（仅 PermRead 放行 memory 目录，PermWrite / PermExec 仍仅认 workdir）；「沙箱放行 ≠ 权限绕过」双层语义
- **三层降级**：`setting.json` 中 `memory.enabled=false` → config 短路 → Source Assemble 短路 → Reviewer OnLoopDone 短路

### 📝 提示词体系

- **分层 System Prompt**：`Builder` 模式组装 8 个 Source（static / environment / agents_md / memory / skill / config / codebase / hook）
- **AGENTS.md 双层合并 +** `@include`：全局 + 项目级按 H2 段合并；支持 `@path.md` 引用其他 markdown 文件并自动展开（含 4 重安全保护）
- **Anthropic Prompt Caching**：SystemBlocks 多段带 `cache_control` 标记，第二轮起命中服务端缓存降本提速
- **SP 可观测性**：状态栏显示总 token 估算 + Source 小计 tooltip，开发者模式一键导出完整 SP 快照

### 🔒 安全权限

- **三层权限模式**：`strict` / `default` / `permissive`，运行时状态栏下拉即时切换
- **可配置规则**：`allow` / `deny` / `ask`，支持路径 glob 与 Bash 命令前缀匹配，多层配置合并
- **HITL 确认**：命中 `ask` 规则时暂停 Agent Loop，支持本次 / 本会话 / 永久三种授权范围
- **危险命令黑名单 + 路径沙箱**：Bash 硬拦截（不可被配置绕过）+ 双层路径越界防护

### 🔌 扩展能力

- **MCP 协议**：JSON-RPC 2.0 + stdio / Streamable HTTP 双传输，自动注册外部工具为 `mcp__<server>__<tool>`，指数退避重连（1s/3s/9s）
- **MCP 可观测**：工具块紫色 server 来源徽标 + 状态栏 MCP 健康区（绿/黄/红/灰四色圆点）
- `/dump` **会话导出**：一键把当前会话完整历史 + System Prompt 导出为 `dump.json` / `dump.md`
- **跨平台**：Windows / macOS / Linux 自动调起浏览器，Windows 支持终端窗口自动隐藏
- **Hook 系统（V1.9.0）**：12 类生命周期事件 + 条件 DSL + command/http/prompt/agent 四类 action；支持 `once` / `async` / 统计 / Shutdown，已接入 Agent Loop、工具调用、会话、压缩与 WebUI 状态栏

### 🧩 Skill 系统（可插拔能力模块）

- **三档优先级扫描**：项目级 `<cwd>/.codepilot/skills/<name>/SKILL.md`（最高）> 用户级 `~/.codepilot/skill/<name>/SKILL.md` > 内置级 `<exec>/internal/skill/builtin/`（最低，预留）；项目级**完全覆盖**用户级同名，同级别同名启动期直接报错退出
- **目录型 Skill + SKILL.md 解析**：YAML frontmatter 必填 `name` / `description`，可选 `args` / `allowed-tools`；正文为 markdown，按需被 LLM 加载（不被默认注入 system prompt）
- **3 种触发模式**：① LLM 通过 `use_skill` 工具主动调用（返回完整 SKILL.md 作为 tool_result）② 用户 `/<skill-name>` 无参触发（SKILL.md 作为 LeadUserMessage 追加到下一轮 user 消息）③ `/<skill-name> <args>` 带参触发（末尾追加 `<user_args>` 段供 Skill 内指令引用）
- `use_skill` **工具**：Input schema `{ "skill_name": "<string>" }`，三档查找（项目级 → 用户级 → 内置级），未命中返回 `IsError=true` + `skill not found: <name>` 给 LLM 自主决策；权限默认 `allow`（只读工具）
- `/skills` **列表面板**：弹出模态框按项目级 / 用户级 / 内置级三组 tab 展示，每条显示 `name` + `description` + 源路径，三档全空时显示「暂无 Skill」空状态
- **渐进式披露**：`SkillsIndexSource` 把已加载 Skill 的 `name` + `description` + 来源级别作为 LeadUserMessage 注入，**只暴露元数据索引不暴露正文**，LLM 按需通过 `use_skill` 工具拉全文，避免污染 system prompt
- **紫色 Skill 视觉徽标**：工具块头部紫色 `skill: <name>` 徽标 + 候选下拉紫色「skill」分类标签，与 MCP server 来源徽标风格一致
- **三层降级**：`setting.json` 中 `skill.enabled=false` → `LoadAll` 短路 → `use_skill` 不注册 → SkillsIndexSource 不注入 → Skill slash 命令不注册，等价「未启用 Skill」
- **配置自感知（V1.7.0）**：`ConfigAwarenessSource` 把「改 setting.json → 加载 config-management 内置 Skill」写入常驻 SP（≈78 token）；`config-management` 覆盖 6+ section 全部字段、顶层 LLM 参数、改写工作流、错误排查；用户在对话中问「怎么改 X 配置」Agent 能自助给到可复制的 JSON 片段
- **代码自感知（V1.8.0）**：`CodebaseAwarenessSource` 把「CodePilot 自身原理 → 加载 codebase-overview 内置 Skill」写入常驻 SP（≈48 token）；`codebase-overview` 内置 Skill 采用「总索引（< 6KB）+ 按需子文档（< 16KB/篇）」二级加载,覆盖 12 篇已实现模块 md + 1 篇 stub（SubAgent），LLM 问到「CodePilot 的 X 模块怎么实现」能基于真实代码回答而非通用设计
- `use_skill` **路径提示注入**：`use_skill` 工具返回时前置 `<Skill 根路径提示>` 段，告知 LLM 当前 Skill 实际可读文件系统根的绝对路径 + 完整可复制的真实路径示例 + 禁止事项（避免 LLM 把尖括号占位符 `<module>` 字面拼进 file_path）；`buildSkillReadRoots` 把 builtin / user / project 三档 Skill 根作为 ReadFile 沙箱附加只读根，使 LLM 可读 SKILL.md 同目录子文件

### 计划支持功能

| 功能           | 所属阶段    | 说明                      |
| ------------ | ------- | ----------------------- |
| **SubAgent** | Step 12 | 子代理系统，支持并行调度、上下文隔离与结果回传 |

---

## 🚀 快速开始

CodePilot 提供 **两种使用方式**：

- **方式 A · 下载预编译二进制（Windows 推荐）**：免 Go 环境，下载即用
- **方式 B · 从源码构建（跨平台）**：需要 Go 1.26+，可自定义/调试

---

### 方式 A：下载预编译二进制（Windows 推荐）

> 适用：**Windows 10 / 11**，**零依赖**（不需要 Go、Python、Node 任何工具链）。

#### 1. 下载

下载根目录下的 `CodePilot.exe`（≈ 15 MB，带 LOGO 图标）即可——内置 Skill 资源已通过 Go `//go:embed` 编译进 exe，**单文件即可独立运行**

把它放到任意目录（比如 `D:\Tools\CodePilot\`）即可。

#### 2. 配置模型 API Key

首次运行前，创建配置文件 `~/.codepilot/setting.json`（`~` 在 Windows 下为 `C:\Users\<你的用户名>\`）：

```powershell
# PowerShell：创建配置目录
New-Item -ItemType Directory -Force -Path "$env:USERPROFILE\.codepilot"

# 复制示例配置（以 Anthropic Claude 为例）
Copy-Item config\setting.example.json "$env:USERPROFILE\.codepilot\setting.json"
```

用任意编辑器打开 `~/.codepilot/setting.json`，把 `api_key` 字段填上你自己的值：

```json
{
    "provider": "anthropic",
    "model": "claude-sonnet-4-20250514",
    "api_key": "sk-ant-xxxxxxxxxxxxxxxxxxxx",
    "base_url": "",
    "max_tokens": 16384
}
```

> 💡 也可以使用 OpenAI（GPT）Provider：把 `provider` 改为 `"openai"`、`model` 改为 `"gpt-4o"`、填入 OpenAI 的 `api_key` 即可，其余字段可保持默认。完整配置示例见 [完整配置示例](#完整配置示例)。

#### 3. 启动

双击 `CodePilot.exe` 即可。CodePilot 会自动：

1. 加载 `~/.codepilot/setting.json` 并初始化 LLM Provider
2. 启动 HTTP + WebSocket 服务（绑定 `127.0.0.1`，端口自动分配）
3. 自动调起默认浏览器打开 Web UI
4. 后台静默运行；浏览器关闭后自动检测并优雅退出

也可在 PowerShell / CMD 中启动，便于观察启动日志：

```powershell
cd <CodePilot.exe 所在目录>
.\CodePilot.exe
```

按 `Ctrl+C` 可手动退出。

#### 4. 升级到新版本

下载新版 `CodePilot.exe` 直接覆盖旧版即可（配置文件在 `~/.codepilot/setting.json`，**不会被覆盖**）。

#### 5. 可选：启用代码自感知 Skill 的子文档

如果你想让 Agent 在对话中按需查阅 `codebase-overview` 内置 Skill 的 `reference/*.md` 子文档（例如问「CodePilot 上下文管理怎么实现」时拿到逐模块的真实实现细节），需要额外下载发布包里的 `internal/skill/builtin/` 目录，放在 `CodePilot.exe` 同级位置：

```
<CodePilot.exe 所在目录>/
├── CodePilot.exe
└── internal/
    └── skill/
        └── builtin/
            ├── config-management/SKILL.md
            └── codebase-overview/
                ├── SKILL.md
                └── reference/*.md
```

**不下载也能用**——单文件 exe 内置的 SKILL.md 主体已足够让 Agent 知道「有配置自感知 / 代码自感知 Skill 可用」，只是子文档（`reference/*.md`）不会被 LLM 主动读取。如不需要这部分高级能力，跳过即可。

---

### 方式 B：从源码构建（跨平台）

适用：macOS / Linux 用户、想修改源码调试的开发者、需要自定义图标的场景。

#### 环境要求

- **Go 1.26+**
- （仅 Windows 打 LOGO 图标 exe 时）**Python ≥ 3.8** + `pip install pillow` + `go install github.com/akavel/rsrc@latest`

#### 构建与运行

```bash
# 克隆项目
git clone https://github.com/MeiCorl/CodePilot.git
cd CodePilot

# 标准构建（不带图标）
go build -o codepilot.exe ./src

# Windows 带 LOGO 图标构建（推荐，需 Python + rsrc）
make build          # 等价于: powershell -File build/build.ps1

# 启动（自动分配端口并打开浏览器）
./codepilot.exe
```

启动后 CodePilot 会依次：加载配置并初始化 LLM Provider → 启动 HTTP + WebSocket 服务（绑定 `127.0.0.1`，端口自动分配）→ 自动打开默认浏览器访问 Web UI → 在后台静默运行。浏览器关闭后自动检测并优雅退出，也可在启动终端按 `Ctrl+C` 手动退出。

> macOS / Linux 用户：`./codepilot.exe` 改为 `./codepilot`（Go 不会在非 Windows 平台加 `.exe` 后缀），构建命令无需 `make build`，标准 `go build` 即可。

### 配置

首次运行前，需要创建配置文件 `~/.codepilot/setting.json`：

```bash
# 创建配置目录
mkdir -p ~/.codepilot

# 使用 Anthropic（Claude）配置
cp config/setting.example.json ~/.codepilot/setting.json

# 或使用 OpenAI（GPT）配置
cp config/setting.example.openai.json ~/.codepilot/setting.json
```

**两层配置合并**：全局配置 `~/.codepilot/setting.json` + 项目级配置 `<项目根>/.codepilot/setting.json`（可选）。同名字段项目级覆盖全局；权限规则合并叠加。编辑后填入你的 API Key 即可。

#### 完整配置示例

下面是一份**包含全部配置段、字段齐全、可直接使用**的完整示例（以 Anthropic 为例；切换 OpenAI 只需把 `provider`/`model`/`api_key` 改掉）：

```json
{
    "provider": "anthropic",
    "model": "claude-sonnet-4-20250514",
    "base_url": "",
    "api_key": "sk-ant-your-api-key-here",
    "max_tokens": 16384,
    "timeout": 180,
    "max_retries": 2,

    "tools": {
        "enabled": ["ReadFile", "WriteFile", "EditFile", "Bash", "Glob", "Grep"]
    },
    "tool_execution_timeout_seconds": 30,
    "tool_working_directory": "",

    "context_window_size": 200000,
    "max_agent_loop_iterations": 50,
    "context_safety_margin": 4096,

    "permissions": {
        "mode": "default",
        "rules": [
            { "tool": "Bash", "pattern": "git *", "action": "allow", "reason": "Git 命令安全放行" },
            { "tool": "Bash", "pattern": "rm *", "action": "deny", "reason": "禁止 rm 删除命令" },
            { "tool": "mcp__*__*", "pattern": "*", "action": "ask", "reason": "MCP 外部工具需确认" }
        ]
    },

    "mcp": {
        "handshake_timeout_seconds": 30,
        "list_tools_cache_ttl_seconds": 60,
        "servers": [
            {
                "name": "filesystem",
                "type": "stdio",
                "command": "npx",
                "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
                "env": {},
                "timeout": 30,
                "disabled": false
            },
            {
                "name": "remote-api",
                "type": "http",
                "url": "https://example.com/mcp",
                "headers": { "Authorization": "Bearer your-token-here" },
                "timeout": 30
            }
        ]
    },

    "compaction": {
        "enabled": true,
        "tool_result_threshold": 5120,
        "preview_tokens": 500,
        "auto_trigger_margin": 20000,
        "manual_target_margin": 3000,
        "keep_recent_tokens": 10000,
        "keep_recent_min_messages": 5,
        "breaker_threshold": 3
    },

    "memory": {
        "enabled": true,
        "index_max_lines": 200,
        "index_max_bytes": 25600,
        "review_model": ""
    },

    "skill": {
        "enabled": true,
        "max_skill_size_bytes": 65536
    },

    "hook": {
        "enabled": true,
        "entries": [
            {
                "name": "auto-gofmt",
                "event": "post_tool_use",
                "condition": {
                    "all": [
                        { "field": "tool_name", "op": "eq", "value": "WriteFile" },
                        { "field": "tool_input.file_path", "op": "glob", "value": "*.go" }
                    ]
                },
                "action": {
                    "type": "command",
                    "command": "gofmt -w $TOOL_INPUT_FILE_PATH",
                    "working_dir": "",
                    "env": { "NO_COLOR": "1" },
                    "timeout": "10s"
                },
                "async": false,
                "once": false
            }
        ]
    }
}
```

> 💡 除 `provider`、`model`、`api_key`（必填）外，其余字段均可省略——省略时使用下文默认值，CodePilot 开箱即用。

#### 配置项参考

##### LLM 基础

| 参数            | 说明                                          | 默认值     |
| ------------- | ------------------------------------------- | ------- |
| `provider`    | LLM 供应商：`anthropic` 或 `openai`              | 必填      |
| `model`       | 模型名称（如 `claude-sonnet-4-20250514`、`gpt-4o`） | 必填      |
| `base_url`    | API 基础地址（支持代理/私有部署/兼容网关）                    | 官方默认    |
| `api_key`     | API 密钥                                      | 必填      |
| `max_tokens`  | 单次回复最大输出 token 数                            | `16384` |
| `timeout`     | LLM 请求超时（秒）                                 | `180`   |
| `max_retries` | 可重试错误的最大重试次数                                | `2`     |

##### 工具与执行

| 参数                               | 说明                                                                                                      | 默认值    |
| -------------------------------- | ------------------------------------------------------------------------------------------------------- | ------ |
| `tools.enabled`                  | 启用工具白名单，工具名须与 `Tool.Name()` 一致（**大驼峰**：`ReadFile`/`WriteFile`/`EditFile`/`Bash`/`Glob`/`Grep`）；空数组=启用全部 | 全部内置工具 |
| `tool_execution_timeout_seconds` | 单次工具执行超时（秒）                                                                                             | `30`   |
| `tool_working_directory`         | 工具沙箱根目录；留空则使用进程启动时的工作目录                                                                                 | 进程 cwd |

##### 上下文窗口

| 参数                          | 说明                                            | 默认值      |
| --------------------------- | --------------------------------------------- | -------- |
| `context_window_size`       | 模型上下文窗口总大小（token 数），用于溢出检查与状态栏展示              | `200000` |
| `max_agent_loop_iterations` | Agent Loop 最大迭代次数（一次迭代 = 一次 LLM 调用 + 可能的工具执行） | `50`     |
| `context_safety_margin`     | 上下文安全余量（token 数），剩余低于此值时注入提示让模型总结收尾           | `4096`   |

##### 权限

| 参数                  | 说明                                                      | 默认值       |
| ------------------- | ------------------------------------------------------- | --------- |
| `permissions.mode`  | 权限模式：`strict` / `default` / `permissive`                | `default` |
| `permissions.rules` | 自定义规则列表，每条含 `tool` / `pattern` / `action` / 可选 `reason` | `[]`      |

**权限校验顺序**：

1. `Bash` 命中危险命令黑名单时直接拒绝，任何模式与 `allow` 规则都不能绕过。
2. 命中 `permissions.rules` 显式规则时，按规则的 `allow` / `deny` / `ask` 决策；其中 `allow` 会直接放行，不再进入模式矩阵。
3. 未命中显式规则时，按下方权限模式矩阵兜底。

**权限模式矩阵**：

| 场景                                      | strict | default | permissive |
| --------------------------------------- | ------ | ------- | ---------- |
| 路径工具：沙箱内读（`ReadFile` / `Glob` / `Grep`） | allow  | allow   | allow      |
| 路径工具：沙箱内写（`WriteFile` / `EditFile`）     | ask    | allow   | allow      |
| 路径工具：沙箱外读                               | deny   | allow   | allow      |
| 路径工具：沙箱外写                               | deny   | ask     | allow      |
| 执行类工具（`Bash` / MCP 工具等，非黑名单）            | ask    | ask     | allow      |

> `Bash` 不会解析 shell 命令内部的 `cd` 路径；例如 `cd <workdir> && ls -la` 仍属于执行类工具，在 `default` 模式下会按上表走 `ask`。模式切换是**运行时内存态**，重启 CodePilot 后回到 `setting.json` 中配置的档位。若需永久切换，编辑全局或项目级 `setting.json` 中的 `permissions.mode`。

**自定义规则**：在 `permissions.rules` 中按需声明，每条规则包含 `tool`（工具名）、`pattern`（参数匹配模式）、`action`（动作）三个字段：

```json
"permissions": {
    "mode": "default",
    "rules": [
        { "tool": "Bash", "pattern": "git *", "action": "allow", "reason": "Git 命令安全放行" },
        { "tool": "WriteFile", "pattern": "*.go", "action": "allow", "reason": "Go 源文件写入放行" },
        { "tool": "Bash", "pattern": "rm *", "action": "deny", "reason": "禁止删除命令" },
        { "tool": "mcp__*__*", "pattern": "*", "action": "ask", "reason": "MCP 工具需确认" }
    ]
}
```

- `tool` 支持精确匹配（`Bash`）和通配符（`*` 匹配所有工具，`mcp__*__*` 匹配所有 MCP 工具）
- `pattern` 支持路径 glob（`*.go`、`/tmp/*`）和 Bash 命令前缀（`git *`）
- `action` 取值：`allow`（放行）/ `deny`（拒绝）/ `ask`（弹确认框）
- 规则按列表顺序匹配，命中第一条即返回

##### MCP 服务器

| 参数                                 | 说明                                            | 默认值  |
| ---------------------------------- | --------------------------------------------- | ---- |
| `mcp.servers`                      | 外部工具服务器列表（stdio / http 两种传输）                  | `[]` |
| `mcp.handshake_timeout_seconds`    | 单个 server 握手总耗时上限（秒，含 Initialize + ListTools） | `30` |
| `mcp.list_tools_cache_ttl_seconds` | `tools/list` 结果缓存时长（秒）                        | `60` |

在 `mcp.servers` 中声明外部工具服务器：

```json
"mcp": {
    "servers": [
        {
            "name": "filesystem",
            "type": "stdio",
            "command": "npx",
            "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
            "timeout": 30
        },
        {
            "name": "remote-api",
            "type": "http",
            "url": "https://example.com/mcp",
            "headers": { "Authorization": "Bearer your-token" },
            "timeout": 30
        }
    ]
}
```

- **stdio 类型**：通过子进程 stdin/stdout 通信，适合本地工具服务器
- **http 类型**：通过 Streamable HTTP 协议通信，适合远程服务
- 单 server 失败不影响其他 server 和 CodePilot 启动
- MCP 工具自动注册为 `mcp__<server>__<tool>` 命名，走完整权限链路

##### 上下文压缩

| 参数                                    | 说明                                | 默认值     |
| ------------------------------------- | --------------------------------- | ------- |
| `compaction.enabled`                  | 压缩总开关；`false` 整体降级为纯滑动窗口          | `true`  |
| `compaction.tool_result_threshold`    | 工具结果存盘阈值（token），超过则 L1 存盘 + 预览替换  | `5120`  |
| `compaction.preview_tokens`           | L1 预览头部保留长度（token）                | `500`   |
| `compaction.auto_trigger_margin`      | L2 自动触发余量（token），剩余 ≤ 此值且未熔断时触发摘要 | `20000` |
| `compaction.manual_target_margin`     | `/compact` 手动触发的目标余量（token）       | `3000`  |
| `compaction.keep_recent_tokens`       | L2 摘要后尾部保留的近期原文（token）            | `10000` |
| `compaction.keep_recent_min_messages` | 近期原文最少保留条数（与上一项取较大者）              | `5`     |
| `compaction.breaker_threshold`        | 摘要连续失败次数达到此值后会话级熔断                | `3`     |

**调参建议**：

- **窗口较小的模型**（如 GPT-4o mini 128K）：把 `context_window_size` 调小、`auto_trigger_margin` 相应下调（如 13000），避免频繁触发 L2 摘要
- **想保留更多近期上下文**：调大 `keep_recent_tokens` / `keep_recent_min_messages`
- **压缩太激进/太保守**：`tool_result_threshold` 控制单个工具结果何时被预览化，调大则更多原文留在上下文（更准但更费 token），调小则更省
- **完全关闭压缩**：`"enabled": false`（其余字段被忽略，降级为纯滑动窗口）
- **摘要反复失败被熔断**：会话内自动 L2 暂停，可手动输入 `/compact` 重置重试一次

> 两层压缩的工作机制（L1 预览化 / L2 摘要 / 熔断 / 紧急压缩）详见后文「核心机制详解 · 上下文压缩」。

##### 自动学习记忆

| 参数                       | 说明                                                        | 默认值     |
| ------------------------ | --------------------------------------------------------- | ------- |
| `memory.enabled`         | 自动学习记忆总开关；`false` 时 config → Source → Reviewer 三层降级为无记忆状态 | `true`  |
| `memory.index_max_lines` | 会话启动注入的 MEMORY.md 索引行数上限                                  | `200`   |
| `memory.index_max_bytes` | 会话启动注入的 MEMORY.md 索引字节上限（超阈值截断并 warn 日志）                  | `25600` |
| `memory.review_model`    | 回顾专用 LLM 模型名（留空 = 复用主 `model`）                            | `""`    |

**存储布局**（按 `setting.json` 中字段级合并：全局 + 项目级叠加）：

```
~/.codepilot/memory/                # 用户级（跨项目生效）
├── MEMORY.md                       #   索引（4 类分块：user_preference/user_feedback/...）
├── indent-style.md                 #   单条记忆（YAML frontmatter + 正文）
└── ...
<cwd>/.codepilot/memory/            # 项目级（跟随当前项目）
├── MEMORY.md
├── api-docs-link.md
└── ...
```

> 记忆的写入由后台异步回顾器自动完成，无需用户感知；ReadFile 工具经沙箱白名单按需读取单条记忆文件，避免把全文塞进上下文。详见后文「核心机制详解 · 自动学习记忆」。

##### Skill 系统

| 参数                           | 说明                                                                                                                | 默认值     |
| ---------------------------- | ----------------------------------------------------------------------------------------------------------------- | ------- |
| `skill.enabled`              | Skill 系统总开关；`false` 时 `LoadAll` 短路 → `use_skill` 不注册 → SkillsIndexSource 不注入 → Skill slash 命令不注册，三层降级为「未启用 Skill」 | `true`  |
| `skill.max_skill_size_bytes` | 单个 SKILL.md 文件正文上限（字节）；超出截断并打 warn 日志                                                                             | `65536` |

**三档存储布局**（启动期按优先级扫描，项目级覆盖用户级）：

```
<cwd>/.codepilot/skills/             # 项目级（最高优先级，跟随项目分发）
├── <skill-name>/
│   ├── SKILL.md                     #   YAML frontmatter + markdown 正文（必填）
│   ├── scripts/                     #   可执行脚本（占位，Skill 内按相对路径引用）
│   ├── reference/                   #   参考资料（大文件分页）
│   └── assets/                      #   静态资源
└── ...
~/.codepilot/skill/                  # 用户级（跨项目生效）
└── <skill-name>/SKILL.md
```

**SKILL.md 文件格式**：

```markdown
---
name: refactor-go-code              # 必填，唯一标识
description: 按本仓库代码规范重构 Go 源文件   # 必填，一句话用途
args: <file_path> [<scope>]          # 可选，用户参数 schema
allowed-tools:                      # 可选，可调用工具白名单
  - ReadFile
  - EditFile
---

# 重构 Go 源文件

工作流：
1. 用 ReadFile 读取目标文件
2. 识别不符合规范的位置
3. 用 EditFile 应用改动
...
```

> Skill 在对话中被使用时的完整工作机制（三档覆盖规则 / 渐进式披露 / 三种触发模式）详见后文「核心机制详解 · Skill 系统」。

##### Hook 系统

| 参数             | 说明                                                                          | 默认值    |
| -------------- | --------------------------------------------------------------------------- | ------ |
| `hook.enabled` | Hook 系统总开关；`false` 时跳过 Hook 引擎，不加载配置、不注册事件                                  | `true` |
| `hook.entries` | Hook 配置数组；每条包含 `name` / `event` / `condition` / `action` / `async` / `once` | `[]`   |

**支持的事件**：`program_start` / `program_exit` / `compact` / `error` / `session_start` / `session_end` / `iteration_start` / `iteration_end` / `pre_tool_use` / `post_tool_use` / `pre_message` / `post_message`。

**支持的 action**：`command`（本地命令）、`http`（HTTP 请求）、`prompt`（向当前轮 user 消息注入提示）、`agent`（轻量 LLM 子任务，Step 12 后升级为完整 SubAgent）。

```json
"hook": {
    "enabled": true,
    "entries": [
        {
            "name": "auto-gofmt",
            "event": "post_tool_use",
            "condition": {
                "all": [
                    { "field": "tool_name", "op": "eq", "value": "WriteFile" },
                    { "field": "tool_input.file_path", "op": "glob", "value": "*.go" }
                ]
            },
            "action": {
                "type": "command",
                "command": "gofmt -w $TOOL_INPUT_FILE_PATH",
                "timeout": "10s"
            },
            "async": false,
            "once": false
        }
    ]
}
```

> Hook 的事件、条件 DSL、变量插值和四类 action 详见后文「核心机制详解 · Hook 系统」。

### 使用说明

1. **开始对话**：在 Web UI 底部输入框输入你的需求，按回车发送
2. **工具调用**：Agent 会根据需要自动调用 ReadFile、WriteFile、Bash 等工具完成任务
3. **查看改动**：WriteFile/EditFile 工具完成态会在工具块头部出现「查看改动」按钮，点击弹出双栏 diff 弹窗
4. **浏览项目文件**：右侧项目文件栏可进入子目录、用面包屑返回上级，并以只读弹窗预览 Markdown、JSON、XML、代码和普通文本文件
5. **权限模式切换**：点击状态栏 `permission` 区域弹出 3 选 1 下拉（严格/默认/放行），切换后立即生效，无需重启
6. **权限确认（HITL）**：当工具调用命中 `ask` 规则时，Agent Loop 会暂停并弹出确认对话框，可选「拒绝 / 本次允许 / 本会话允许 / 永久允许」
7. **中断操作**：点击输入框旁的取消按钮可中断当前 Agent Loop
8. **会话管理**：
  - 左侧会话列表可查看历史会话
  - 输入 `/new` 创建新会话
  - 输入 `/sessions` 查看所有会话
  - 输入 `/resume <id>` 恢复指定会话
  - 输入 `/compact` 手动压缩当前会话上下文（历史摘要化）
  - 输入 `/dump` 把当前会话上下文 + System Prompt 导出到会话目录下的 `dump.json` / `dump.md`
9. **Skill 系统**：
  - 在 `/` 候选下拉中，所有 Skill 命令带有紫色 `skill` 分类标签
  - 输入 `/skills` 弹出列表面板，按项目级 / 用户级 / 内置级三组查看已加载 Skill
  - 输入 `/<skill-name>` 触发某个 Skill（无参）
  - 输入 `/<skill-name> <args>` 触发某个 Skill 并传递参数（追加到 Skill 指令末尾）
  - 让 LLM 在对话中自主调用 `use_skill` 工具加载 Skill 完整内容
  - 自定义 Skill：在 `<cwd>/.codepilot/skills/<name>/SKILL.md`（项目级）或 `~/.codepilot/skill/<name>/SKILL.md`（用户级）编写 YAML frontmatter + markdown 正文即可，**重启后自动加载**

---

## 🏗️ 系统架构

CodePilot 采用 **5 层垂直分层架构**，每层职责单一、高内聚，层间仅通过标准接口交互：

```
┌─────────────────────────────────────────────────────────────┐
│                    第 1 层：交互层（Interaction）              │
│   Web UI（HTTP + WebSocket）                                │
├─────────────────────────────────────────────────────────────┤
│                    第 2 层：引擎层（Engine）                   │
│   对话管理（Conversation）│ Agent Loop（ReAct）│ 提示词（Prompt）│
├─────────────────────────────────────────────────────────────┤
│                    第 3 层：工具层（Tool）                     │
│   内置工具集 │ 命令系统 │ Skill 技能 │ MCP 协议 │ Hook 钩子 │ SubAgent │
├─────────────────────────────────────────────────────────────┤
│                    第 4 层：记忆层（Memory）                   │
│   上下文管理 │ 会话管理 │ 自动记忆                              │
├─────────────────────────────────────────────────────────────┤
│                    第 5 层：安全层（Security）                 │
│   权限控制 │ 沙箱隔离 │ HITL 人工干预                          │
└─────────────────────────────────────────────────────────────┘
```

**依赖方向**：上层可调用下层接口，下层禁止依赖上层。安全层作为横切关注点可被任意层调用。

### 项目结构

```
CodePilot/
├── src/
│   ├── main.go                          # 程序入口，组装各组件并启动 Web 服务
│   ├── llm/                             # LLM 供应商抽象层（第 2 层依赖）
│   │   ├── provider.go                  #   Provider 接口定义与工厂方法
│   │   ├── anthropic.go                 #   Anthropic（Claude）协议适配（含 Prompt Caching）
│   │   ├── openai.go                    #   OpenAI（GPT）协议适配
│   │   ├── types.go                     #   统一类型定义（ContentBlock、Message、StreamChunk 等）
│   │   └── types_json.go                #   ContentBlock JSON 序列化辅助
│   ├── internal/
│   │   ├── config/                      # 配置管理
│   │   │   └── config.go                #   配置加载、校验与默认值
│   │   ├── engine/                      # 引擎层（第 2 层）
│   │   │   ├── conversation/            #   对话 + Agent Loop
│   │   │   │   ├── manager.go           #     对话管理器，协调 LLM 调用与消息流
│   │   │   │   ├── agent_loop.go        #     ReAct 循环引擎，多轮推理迭代
│   │   │   │   └── tool_handler.go      #     工具调用处理器，执行 + 结果回传 + FileDiff 写入
│   │   │   └── prompt/                  #   System Prompt 组装管线
│   │   │       ├── builder.go           #     顶层 Builder：按 Source 注册顺序组装 SystemPrompt
│   │   │       ├── README.md            #     模块设计说明与扩展指南
│   │   │       ├── sources/             #     Source 实现（每个 Source 产出一段内容）
│   │   │       │   ├── source.go        #       Source 接口与 SystemPrompt 结构
│   │   │       │   ├── static.go        #       静态 5 子模块（角色/行为/代码质量/工具/安全）
│   │   │       │   ├── environment.go   #       OS / CWD / Git 状态采集
│   │   │       │   ├── agents_md.go     #       全局 + 项目级 AGENTS.md 合并
│   │   │       │   ├── agents_md_include.go #   AGENTS.md @include 展开
│   │   │       │   ├── memory_index.go  #       4 类自动学习记忆 MEMORY.md 索引注入（Step 8）
│   │   │       │   ├── config_awareness.go  #   配置自感知 Source（Step 10.1）
│   │   │       │   ├── codebase_awareness.go #   代码自感知 Source（Step 10.2）
│   │   │       │   └── hooks_awareness.go   #   Hook 自感知 Source（Step 11）
│   │   │       ├── template/            #     模板变量渲染
│   │   │       │   ├── env.go           #       Env 结构（OS / CWD / Date / StaticOverrides）
│   │   │       │   └── render.go        #       {{OS}} / {{CWD}} / {{GIT_BRANCH}} 等替换
│   │   │       └── tokens/              #     token 估算（用于状态栏展示）
│   │   │           └── estimate.go      #       Source 文本 → token 数
│   │   ├── interaction/                 # 交互层（第 1 层）
│   │   │   └── web/                     #   WebUI（HTTP + WebSocket）
│   │   │       ├── server.go            #     HTTP 服务器与生命周期管理
│   │   │       ├── router.go            #     路由注册（静态资源 + WebSocket）
│   │   │       ├── websocket.go         #     WebSocket 连接管理
│   │   │       ├── handler.go           #     WebSocket 消息处理主入口（含 handleSetPermissionMode 等）
│   │   │       ├── protocol.go          #     消息协议定义（Message / Payload 类型）
│   │   │       ├── project_file.go      #     项目文件栏：目录列表 / 文件预览 / 路径安全校验
│   │   │       ├── tool_msg.go          #     工具调用相关消息构造
│   │   │       ├── file_diff_store.go   #     进程内 FileDiff 存储（WriteFile/EditFile diff 弹窗数据源）
│   │   │       ├── browser.go           #     跨平台浏览器调起
│   │   │       └── static/              #     Web UI 前端静态资源（Go embed 嵌入）
│   │   │           ├── index.html
│   │   │           ├── app.js           #     前端主逻辑（WS 客户端 / Markdown / 权限下拉等）
│   │   │           ├── style.css        #     深色编辑式设计系统
│   │   │           └── vendor/          #     第三方库（本地 vendored，零构建）
│   │   │               ├── marked.min.js        # Markdown 解析
│   │   │               ├── highlight.min.js     # 代码语法高亮
│   │   │               ├── purify.min.js        # XSS 过滤
│   │   │               ├── diff-match-patch.min.js  # 文件 diff
│   │   │               └── highlight-theme.css  # 代码高亮主题
│   │   ├── tool/                        # 工具层（第 3 层）
│   │   │   ├── tool.go                  #   Tool 接口定义 + 权限分级（PermRead/Write/Exec）
│   │   │   ├── tool_spec.go             #   工具规格定义（供 LLM function_calling 使用）
│   │   │   ├── registry.go              #   工具注册表，集中管理与查找
│   │   │   ├── context.go               #   tool.ToolUseID 跨调用 ctx 传递（WithToolUseID/ToolUseIDFromContext）
│   │   │   ├── file_diff.go             #   FileDiffSink 接口（web 侧实现反向注入）
│   │   │   └── builtin/                 #   内置工具实现
│   │   │       ├── register.go          #     工具注册入口（init 自动注册）
│   │   │       ├── read_file.go         #     ReadFile 工具
│   │   │       ├── write_file.go        #     WriteFile 工具（含 diff 写入）
│   │   │       ├── edit_file.go         #     EditFile 工具（含 diff 写入）
│   │   │       ├── bash.go              #     Bash 命令执行工具
│   │   │       ├── glob.go              #     Glob 文件查找工具
│   │   │       ├── grep.go              #     Grep 内容搜索工具
│   │   │       └── schema.go            #     工具参数 Schema 定义
│   │   ├── security/                    # 安全层（第 5 层，Step 5 整体迁移至此）
│   │   │   ├── policy.go                #   权限模式（strict/default/permissive）+ Action + Scope
│   │   │   ├── config.go                #   权限配置结构（setting.json 中 permissions 段）
│   │   │   ├── checker.go               #   Checker：硬安全预检 + 路径越界 + 规则匹配 + 档位默认策略
│   │   │   ├── interceptor.go           #   Interceptor：在工具执行前拦截，调用 Checker 并触发 HITL
│   │   │   ├── hitl.go                  #   HITL 回调类型定义
│   │   │   ├── sandbox.go               #   路径沙箱（ResolveInSandbox + IsPathOutsideSandbox）
│   │   │   ├── blacklist.go             #   Bash 危险命令黑名单（不可绕过硬拦截）
│   │   │   └── integration_test.go      #   端到端集成测试（92 个用例的合并入口）
│   │   ├── mcp/                         # MCP 协议客户端（第 3 层工具层，Step 6）
│   │   │   ├── jsonrpc/                 #   JSON-RPC 2.0 编解码（Request/Response/Notification + ID 生成器）
│   │   │   ├── transport/               #   传输抽象 + stdio + Streamable HTTP 实现
│   │   │   ├── session/                 #   会话管理（三阶段握手 + 连接池 + 缓存 + 健康检查）
│   │   │   ├── adapter/                 #   MCP Tool → CodePilot Tool 适配器 + 自动批量注册
│   │   │   ├── config/                  #   配置解析（setting.json mcp.servers → PoolConfig）
│   │   │   ├── reconnect/               #   指数退避重连策略（1s/3s/9s + unhealthy 标记）
│   │   │   ├── testdata/                #   Mock MCP Server（stdio + HTTP，用于集成测试）
│   │   │   └── integration_test.go      #   端到端集成测试
│   │   ├── skill/                       # Skill 系统（第 3 层工具层，Step 10）
│   │   │   ├── skill.go                 #   Skill 类型 + Source 三档枚举（project/user/builtin）
│   │   │   ├── registry.go              #   三档合并注册表（项目级覆盖用户级 + 同级同名报错）
│   │   │   ├── scanner.go               #   三档目录扫描器（LoadAll + LoadIssue）
│   │   │   ├── loader/                  #   SKILL.md 解析器（YAML frontmatter + markdown）
│   │   │   │   └── loader.go            #     ParseFile + maxBytes 截断
│   │   │   ├── builtin/                 #   内置 Skill 资源（Step 10.1 + 10.2，编译期 embed + dist 副本 + workdir fallback 三段加载）
│   │   │   │   ├── config-management/   #     配置自感知 Skill：6+ section + 顶层 LLM 参数 + 改写工作流 + 错误排查
│   │   │   │   │   └── SKILL.md
│   │   │   │   └── codebase-overview/    #     代码自感知 Skill：总索引 + 12 篇模块 md + 1 篇 stub（SubAgent）
│   │   │   │       ├── SKILL.md
│   │   │   │       └── reference/*.md
│   │   │   ├── adapter/                 #   Skill → SlashCommand / use_skill Tool 适配
│   │   │   │   ├── slash.go             #     AsSlashCommand + RegisterAll
│   │   │   │   ├── tool.go              #     use_skill 工具实现
│   │   │   │   └── client.go            #     SkillsListCmd（/skills client 命令）
│   │   │   ├── sources/                 #   prompt.Source 实现
│   │   │   │   └── skills_index.go      #     SkillsIndexSource：name+description 索引注入
│   │   │   └── e2e_test.go              #   端到端集成测试（7 跨包用例）
│   │   ├── hook/                        # Hook 系统（第 3 层工具层，Step 11）
│   │   │   ├── engine.go                #   HookEngine：调度、once/async、统计与 Shutdown
│   │   │   ├── event.go                 #   12 类生命周期事件枚举
│   │   │   ├── context.go               #   HookContext 与变量映射
│   │   │   ├── matcher/                 #   条件 DSL（eq/neq/glob/contains/all/any）
│   │   │   ├── executor/                #   command/http/prompt/agent 四类 action
│   │   │   └── integration/             #   Agent Loop / ToolHandler / Session / Compact 集成
│   │   ├── hookcontext/                 # Hook 上下文插值与事件数据结构
│   │   ├── command/                     # 快捷命令系统（第 3 层工具层，Step 9 + 9.1）
│   │   │   └── slash/                   #   SlashCommand 接口 + Registry + 6 条 builtin
│   │   │       ├── command.go           #     SlashCommand 接口（6 方法）
│   │   │       ├── registry.go          #     注册表（按 name 去重 + OnChange 回调）
│   │   │       ├── builtin.go           #     6 条内置命令（/new /sessions /resume /clear /compact /dump）
│   │   │       ├── command_test.go      #     16 个单测
│   │   │       └── e2e_test.go          #     4 个跨包 e2e（ws onOpen 推送 / list_slash_commands / /new 链路）
│   │   ├── memory/                      # 记忆层（第 4 层）
│   │   │   ├── context/                 #   上下文管理（Step 7）
│   │   │   │   ├── compactor.go         #     顶层协调器（L1 必跑 + L2 按需 + 熔断）
│   │   │   │   ├── light_compactor.go   #     L1 工具结果预览化（in-place + 落盘）
│   │   │   │   ├── summary_compactor.go #     L2 整体历史摘要（切分 + 摘要 + 归档 + 重写）
│   │   │   │   ├── tool_result_store.go #     工具结果外置存盘（幂等 O_EXCL）
│   │   │   │   ├── measure.go           #     统一 token 估算（CJK 2字/token + 消息 15 token 开销）
│   │   │   │   ├── preview.go           #     头部预览生成（按 rune 截断）
│   │   │   │   └── window.go            #     滑动窗口（⚠️ 当前未使用，Step 7 改用两层压缩）
│   │   │   └── session/
│   │   │       └── session.go           #     会话管理器（按项目分目录 + append-only JSONL）
│   │   │   └── autolearn/                #   自动学习记忆（Step 8）
│   │   │       ├── types.go              #     数据模型（4 类记忆 + 存储域 + Frontmatter）
│   │   │       ├── store.go              #     文件持久化（MEMORY.md 索引 + 原子写 + 路径逃逸防护）
│   │   │       ├── prompt.go             #     回顾专用 prompt 模板（独立 LLM 通道）
│   │   │       ├── sanitizer.go          #     敏感信息脱敏（凭证 / Bearer / 键值对口令三类模板）
│   │   │       └── reviewer.go           #     后台异步回顾器（per-session 串行 + panic recover）
│   │   ├── logger/                      # 日志系统
│   │   │   └── logger.go                #   基于 zap 的异步文件日志
│   │   └── runtime/                     # 运行时工具
│   │       └── console/
│   │           ├── console.go           #   控制台操作接口
│   │           ├── console_windows.go   #   Windows 终端窗口隐藏
│   │           └── console_other.go     #   其他平台 no-op
├── config/                              # 配置文件示例
│   ├── setting.example.json              #   Anthropic 配置示例
│   └── setting.example.openai.json       #   OpenAI 配置示例
├── docs/                                # 设计文档（按开发步骤组织）
├── .harness/                            # 内部规格 + 进度
│   ├── PROJECT.md                       #   系统架构与计划
│   ├── PROGRESS.md                      #   步骤完成情况
│   └── rules/                           #   架构 / 设计强制规范
├── go.mod
├── go.sum
└── README.md
```

> 说明：上述目录树省略了各包内的 `*_test.go` 单测文件（实际每个生产文件都有对应的 `_test.go` 覆盖单测与边界场景）。`src/internal/security/integration_test.go` 是合并的端到端集成测试入口。

---

## 🔧 核心机制详解

### 📝 AGENTS.md 与 `@include` 引用

AGENTS.md 支持用 `@relative/path.md` 引用其他 markdown 文件，CodePilot 会在启动时**自动展开**为被引用文件的内容。

**示例**（`F:\CodePilot\AGENTS.md`）：

```markdown
## code style
@docs/style.md

## testing
@docs/testing.md
```

**展开后**（LLM 实际看到的 LeadUserMessage）：

```markdown
## code style
<!-- included from docs/style.md -->
<docs/style.md 的完整内容>

## testing
<!-- included from docs/testing.md -->
<docs/testing.md 的完整内容>
```

**4 重安全保护**（设计动机：被引用文件视为项目敏感代码路径，必须防止意外泄露/撑爆上下文）：

| 防线       | 机制                                                  | 默认值  |
| -------- | --------------------------------------------------- | ---- |
| **路径沙箱** | 拒绝绝对路径（含 POSIX `/` 与 Windows 盘符 `C:\`）+ 拒绝 `..` 路径段 | —    |
| **循环检测** | 访问链追踪 map[A→B→A 立即停止，输出注释占位                         | —    |
| **深度上限** | 递归深度限制                                              | 5 层  |
| **大小截断** | 单文件超过限制截断并打 warn 日志                                 | 64KB |

**失败语义**：所有失败模式（文件不存在/路径逃逸/循环/超深）降级为 HTML 注释占位（如 `<!-- @path: file not found -->`），**不抛错、不阻塞会话启动**。

**不支持的语法**（按"非 .md 后缀不展开"规则原样保留为普通文本）：

- `@/etc/passwd`（绝对路径）
- `@~/file.md`（家目录简写）
- `@file.txt`（非 .md 后缀）

**路径基准**：相对 **AGENTS.md 所在目录**（非 CWD），跨项目移动整个目录树时引用不失效。

### 🧠 上下文压缩（L1 + L2）

CodePilot 在每次 LLM 请求前自动执行两层压缩，**无需用户感知**：

| 层级          | 触发时机                                    | 机制                                                                                          | 代价        |
| ----------- | --------------------------------------- | ------------------------------------------------------------------------------------------- | --------- |
| **L1 轻量预防** | 每次请求前必跑                                 | 单条消息内 tool_result 超 5120 token → 落盘到 `tool_results/<toolUseID>`，内存替换为 500 token 头部预览        | 本地 IO     |
| **L2 重量兜底** | 剩余 token ≤ 20000 / 用户手动 `/compact` / 撞墙 | `splitByTailTokens` 切分 → 调 LLM 生成摘要 → 早期原文归档到 `history_archive.jsonl` → 重写 `messages.jsonl` | 一次 LLM 调用 |

**关键不变量**：

- L1 替换规则完全由"超阈值"决定 → 同一历史每轮重跑结果一致 → **prompt cache 持续命中**
- L2 摘要失败时**不修改**内存 history（顺序：先摘要成功 → 才归档 → 才重写 jsonl）
- 摘要连续失败 3 次触发**会话级熔断**，停止自动 L2；用户手动 `/compact` 重置熔断给一次机会
- 撞墙时（`remaining < 4096`）调用 `EmergencyCompact`，用更激进目标余量 3000 兜底一次

**L2 切分点保护**：`alignSplitForToolPairs` 确保摘要边界不会把 `tool_use` 与其 `tool_result` 拆到不同侧，避免协议 400 错。

> 各阈值的配置项详见「快速开始 · 配置项参考 · 上下文压缩」。

### 🧠 自动学习记忆（4 类分级 + 后台回顾）

CodePilot 在每次 Agent Loop 结束后，自动由**独立 LLM 通道**回顾本轮对话，按 4 类记忆分级沉淀为独立 md 文件；新会话启动时把两级 `MEMORY.md` 索引注入 LeadUserMessage，让 Agent「想起」之前沉淀的偏好、反馈、项目知识与参考信息。

**4 类记忆分级**：

| 类型                  | 存储域                            | 何时沉淀                                 |
| ------------------- | ------------------------------ | ------------------------------------ |
| `user_preference`   | 用户级 `~/.codepilot/memory/`     | 用户明确表达的做事方式约定（如「缩进用 4 空格」），跨所有项目生效   |
| `user_feedback`     | 用户级 `~/.codepilot/memory/`     | 用户对 Agent 输出的纠正性反馈与正确做法              |
| `project_knowledge` | 项目级 `<cwd>/.codepilot/memory/` | 关于当前项目的技术架构、部署运维、内部约定等信息             |
| `reference`         | 项目级 `<cwd>/.codepilot/memory/` | 外部链接与资料（API 文档地址、内部 wiki 链接、DB 手册位置） |

**关键设计**：

- **后台异步 + 节流**：回顾器监听 `AgentLoop.OnLoopDone`，仅当 `completed` + 本轮有实质输入时才触发；aborted / error / max_iterations / context_overflow / 纯闲聊一律跳过
- **per-session 串行**：inflight map + drop 策略；同一会话多次结束只串行一次，避免覆盖竞态
- **独立 LLM 通道**：回顾用独立 `context.Background()` 派生 + `ReviewTimeout(60s)`，自带本轮快照（用户输入 + 最终回复 + 工具名摘要）做独立 LLM 调用；**Reviewer 不持有** `ConversationManager` **引用，编译期保证绝不回写主对话历史**
- **LLM 比对索引去重 / 更新**：回顾 prompt 注入当前两级 `MEMORY.md` 索引，由回顾 LLM 决策 new / update：
  - `new` → 新建 `<slug>.md` + 在 MEMORY.md 索引追加 `- [type](slug.md)——简介`
  - `update` → 覆盖同 slug 文件 + 刷新索引简介 + 保留 `CreatedAt`，消除冗余与自相矛盾
  - 虚构 slug（LLM 自创但索引中不存在）被跳过防误覆盖
- **敏感信息双层防护**：
  1. **Prompt 硬性约束**：明确禁止记录密钥 / 密码 / token / 凭证
  2. **Sanitizer 正则兜底**：对高熵凭证 / Bearer / 键值对口令三类独立 replace 模板（幂等），如 `sk-XXXX...` → `[REDACTED_API_KEY]`
- **ReadFile 沙箱白名单按需读取**：通过 `security.WithReadRoots` 注入附加只读根（仅 `PermRead` 放行 memory 目录，`PermWrite` / `PermExec` 仍仅认 workdir）；`IsPathOutsideSandbox` 故意不感知附加根，「**沙箱放行 ≠ 权限绕过**」双层语义，纵深防御
- **三层降级**：`memory.enabled=false` 时 `config.IsEnabled` → Source `Assemble` 短路 → Reviewer `OnLoopDone` 短路 → 整体降级为无记忆状态（不报错、不污染提示词）

**架构纯净度**：autolearn 包仅依赖 `llm` + `logger`，不 import `conversation` / `config`（用 `ReviewRequest` 解耦 `AgentLoopResult`、`ReviewerConfig` 解耦 config）；handler 层负责 `AgentLoopResult → ReviewRequest` 适配 + `config → 组件配置` wire。

> 详见设计文档 [docs/step8-记忆系统/](docs/step8-记忆系统/)。

### 🧩 Skill 系统（三档优先级 + 渐进式披露）

CodePilot 的 Skill 系统让开发者能为项目或自己写**可复用的能力模块**：把工作流、约定、知识封装到 `SKILL.md` 中，既能让 LLM 在需要时**主动按需加载完整内容**（避免污染 system prompt），也能让用户通过 `/<skill-name>` **斜杠命令手动触发**。

**三档优先级扫描**（启动期执行）：

| 来源级别 | 路径                                              | 优先级 | 典型场景                                                          |
| ---- | ----------------------------------------------- | --- | ------------------------------------------------------------- |
| 内置级  | `<exec>/internal/skill/builtin/<name>/SKILL.md` | 最低  | CodePilot 官方内置（V1.7+ 含 config-management / codebase-overview） |
| 用户级  | `~/.codepilot/skill/<name>/SKILL.md`            | 中   | 个人跨项目通用模板、习惯约定                                                |
| 项目级  | `<cwd>/.codepilot/skills/<name>/SKILL.md`       | 最高  | 项目专属工作流（代码规范、领域操作、部署脚本）                                       |

**冲突规则**：

- **项目级覆盖用户级同名**：项目级同名 Skill 加载时，用户级 Skill **silent skip**（不注册、不报错），让项目方可以放心覆盖个人习惯
- **同级别同名报错退出**：两个项目级或两个用户级同名 → 启动期直接 `*ErrSkillConflict` error 并退出进程，由用户改名后再启动
- **单个 Skill 解析失败 warn 跳过**：YAML 错误、缺 `SKILL.md`、目录不可读 → 记录 warn 日志并跳过该 Skill，**不影响其他 Skill 加载与程序启动**

**渐进式披露**（避免污染 system prompt）：

1. **会话启动 → 仅注入元数据索引**：`SkillsIndexSource` 把已加载 Skill 的 `name` + `description` + 来源级别（如 `[project] refactor-go-code 描述: 按本仓库代码规范重构 Go 源文件`）作为 LeadUserMessage 注入；LLM **只知道有哪些 Skill 可用**，看不到正文
2. **LLM 按需加载全文**：LLM 决定使用某个 Skill 时，调用 `use_skill(skill_name="<name>")` 工具，工具读取完整 SKILL.md（含 frontmatter 重组后的 markdown 标题）作为 `tool_result` 返回，LLM 据此理解工作流并执行
3. **64KB 截断保护**：单个 SKILL.md 正文（去除 frontmatter）超过 `max_skill_size_bytes`（默认 64KB）只取前 64KB + 截断提示，避免单次 `tool_result` 撑爆上下文窗口；完整内容可通过 Skill 目录的 `reference/` 子文件分页引用

**三种触发模式**：

| 触发方    | 入口                                    | 行为                                                                  |
| ------ | ------------------------------------- | ------------------------------------------------------------------- |
| LLM 主动 | `use_skill(skill_name="<name>")` 工具调用 | 完整 SKILL.md 作为 `tool_result` 返回，LLM 据此执行工作流                         |
| 用户无参   | `/<skill-name>` 斜杠命令                  | 完整 SKILL.md 作为 LeadUserMessage 追加到下一轮 user 消息，触发新一轮 LLM 推理          |
| 用户带参   | `/<skill-name> <args>` 斜杠命令           | 同上，LeadUserMessage 末尾追加 `<user_args>...</user_args>` 段供 Skill 内指令引用 |

**架构边界守住（单向依赖）**：

- `src/internal/skill/` 主包：**仅** 定义 `Skill` 类型 / `Loader` / `Registry` / `Scanner`，**不 import** web / command / slash / tool / prompt 任何上层
- `skill/adapter/`：命令/工具的适配层（预期内依赖上层）
- `tool` / `slash` / `prompt` / `web` 可 import `skill`；`skill` → 不 import 任何上层

> 详见设计文档 [docs/step10-Skill系统/](docs/step10-Skill系统/)。

### 🪝 Hook 系统（12 类事件 + 4 类 action）

Hook 系统让用户可以在 Agent 生命周期关键节点插入自动化动作，而不需要修改 CodePilot 源码。Hook 配置放在 `setting.json` 的 `hook` 段中，支持全局配置与项目级覆盖。

**事件覆盖**：系统级（`program_start` / `program_exit` / `compact` / `error`）、会话级（`session_start` / `session_end`）、轮次级（`iteration_start` / `iteration_end`）、工具级（`pre_tool_use` / `post_tool_use`）、消息级（`pre_message` / `post_message`）。

**条件 DSL**：支持 `eq` / `neq` / `glob` / `contains` 四种 leaf 条件，并可用 `all` / `any` 组合；字段可匹配 `event`、`tool_name`、`session_id`、`iteration` 以及 `tool_input.`*。

**四类 action**：

| 类型        | 用途               | 典型场景                                 |
| --------- | ---------------- | ------------------------------------ |
| `command` | 执行本地命令           | `WriteFile` 后对 `.go` 文件自动 `gofmt`    |
| `http`    | 发送 HTTP 请求       | Agent 完成任务后通知外部系统                    |
| `prompt`  | 向当前轮 user 消息注入提示 | 针对某类文件临时补充项目规范                       |
| `agent`   | 轻量 LLM 子任务       | 本期一次性 LLM 检查，Step 12 后升级为完整 SubAgent |

Hook 引擎默认错误隔离：panic / error / timeout 都只写日志，不中断主 Agent Loop；`async=true` 时异步执行，`once=true` 时同一会话内只触发一次。WebUI 状态栏会展示 Hook 配置数、触发数和失败数。

> 详见设计文档 [docs/step11-Hook系统/](docs/step11-Hook系统/)。

---

## 🛠️ 技术栈

| 组件              | 技术                                                                                                                    |
| --------------- | --------------------------------------------------------------------------------------------------------------------- |
| **后端语言**        | Go 1.26+                                                                                                              |
| **LLM SDK**     | [anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go) / [openai-go](https://github.com/openai/openai-go) |
| **Web 通信**      | HTTP + [gorilla/websocket](https://github.com/gorilla/websocket)                                                      |
| **前端渲染**        | 原生 HTML/CSS/JS（Go `embed.FS` 嵌入，零构建步骤）                                                                                |
| **Markdown 渲染** | [marked](https://marked.js.org/) + 实时流式解析                                                                             |
| **代码高亮**        | [highlight.js](https://highlightjs.org/) v11.11.1                                                                     |
| **安全防护**        | [DOMPurify](https://github.com/cure53/DOMPurify) v3.2.4 XSS 过滤                                                        |
| **日志**          | [zap](https://github.com/uber-go/zap) + [lumberjack](https://github.com/natefinch/lumberjack) 日志轮转                    |
| **数据存储**        | JSON 文件持久化（会话、配置）                                                                                                     |

---

## 📊 项目进度

> 当前最新版本 **V1.9.1** · 最近更新 **2026-07-09** · 进行中 **—**（11/12 主线步骤完成，下一步 Step 12 SubAgent）
> 详细进度见 [.harness/PROGRESS.md](.harness/PROGRESS.md)

```
[██████████████████████████████████████░] 11/12 主线步骤完成（Step 1-11 已完成 + Step 1.5 / Step 9.1 / Step 10.1 / Step 10.2 子步骤已落地，Step 12 待开始）

✅ Step 1     — LLM 打通（双 Provider + 流式）
✅ Step 1.1   — UI 界面重构（TUI → WebUI）
✅ Step 1.2   — 对话栏富文本渲染增强
✅ Step 1.3   — WebUI 流式渲染
✅ Step 1.4   — WebUI 工具展示优化（双栏 diff 弹窗）
✅ Step 1.5   — WebUI 项目文件栏（右侧目录浏览 + 只读文件预览 + Markdown/JSON/XML/代码高亮）
✅ Step 2     — 工具系统集成（Tool/Registry/Builtin）
✅ Step 3     — ReAct 与 Agent Loop 实现（多轮迭代 + 工具错误回灌 + 优雅中断）
✅ Step 4     — System Prompt 设计（Builder + 8 Source + 模板变量 + Anthropic 缓存切片）
✅ Step 5     — 权限系统设计（运行时档位切换 + HITL + 危险命令黑名单 + 路径沙箱）
✅ Step 6     — MCP 协议实现（JSON-RPC + stdio/HTTP + 连接池 + 适配器 + 重连）
✅ Step 7     — 上下文管理（两层压缩 L1 工具结果预览化 + L2 整体摘要 + 熔断 + 紧急压缩）
✅ Step 8     — 记忆系统（4 类自动学习记忆 + MEMORY.md 索引注入 + 后台异步回顾 + 敏感脱敏）
✅ Step 9     — 快捷命令系统（/new /sessions /resume /clear /compact /dump）
✅ Step 9.1   — Slash 命令注册后端化（SlashCommand 接口 + Registry + WS Open 主动推送）
✅ Step 10    — Skill 系统（三档优先级 + use_skill 工具按需加载 + 渐进式披露 + 紫色徽标）
✅ Step 10.1  — 配置自感知（ConfigAwarenessSource + config-management 内置 Skill，改 setting.json 自助）
✅ Step 10.2  — 代码自感知（CodebaseAwarenessSource + codebase-overview 内置 Skill，CodePilot 自身实现按需查阅）
✅ Step 11    — Hook 系统（12 类事件 + 条件 DSL + command/http/prompt/agent 四类 action）
⏳ Step 12    — SubAgent
```

---

## 📄 许可证

[MIT License](LICENSE)