# CodePilot

一个用 Go 从零实现的 AI Coding Agent（类 Claude Code / Cursor Agent）。通过 Web UI 与 Agent 交互，由 ReAct 循环驱动 LLM 自主调用工具，完成代码编写、文件操作、命令执行等编程任务。

> **项目初衷**：作为转入 AI Agent 开发的练手项目，通过从零实现一个完整的 Coding Agent，深入理解 Agent Loop、工具系统、上下文管理、权限控制、MCP 协议等核心机制。虽不及 Claude Code 等主流产品，但胜在结构清晰、可读可改。

[Go Version](https://go.dev/) · [License](LICENSE)

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

### 🧠 上下文与会话

- **两层上下文压缩**：L1 工具结果预览化 + L2 整体历史摘要，无需用户感知（机制详见「核心机制详解」）
- **熔断与紧急压缩**：摘要连续失败 3 次触发会话级熔断；撞墙时紧急压缩兜底一次
- **会话持久化**：append-only JSONL（`messages.jsonl` + `meta.json`），支持会话恢复与历史回放
- **跨项目隔离**：按 workdir basename 分目录存放会话，跨项目天然隔离
- **优雅中断**：中断时保留已完成迭代 + 写入 `abortMarker`，LLM 后续轮次能"理解"前序已取消，支持恢复

### 📝 提示词体系

- **分层 System Prompt**：`Builder` 模式组装 4 个 Source（static / environment / agents_md / memory）
- **AGENTS.md 双层合并 + `@include`**：全局 + 项目级按 H2 段合并；支持 `@path.md` 引用其他 markdown 文件并自动展开（含 4 重安全保护）
- **Anthropic Prompt Caching**：SystemBlocks 多段带 `cache_control` 标记，第二轮起命中服务端缓存降本提速
- **SP 可观测性**：状态栏显示总 token 估算 + 4 层 Source 小计 tooltip，开发者模式一键导出完整 SP 快照

### 🔒 安全权限

- **三层权限模式**：`strict` / `default` / `permissive`，运行时状态栏下拉即时切换
- **可配置规则**：`allow` / `deny` / `ask`，支持路径 glob 与 Bash 命令前缀匹配，多层配置合并
- **HITL 确认**：命中 `ask` 规则时暂停 Agent Loop，支持本次 / 本会话 / 永久三种授权范围
- **危险命令黑名单 + 路径沙箱**：Bash 硬拦截（不可被配置绕过）+ 双层路径越界防护

### 🔌 扩展能力

- **MCP 协议**：JSON-RPC 2.0 + stdio / Streamable HTTP 双传输，自动注册外部工具为 `mcp__<server>__<tool>`，指数退避重连（1s/3s/9s）
- **MCP 可观测**：工具块紫色 server 来源徽标 + 状态栏 MCP 健康区（绿/黄/红/灰四色圆点）
- **`/dump` 会话导出**：一键把当前会话完整历史 + System Prompt 导出为 `dump.json` / `dump.md`
- **跨平台**：Windows / macOS / Linux 自动调起浏览器，Windows 支持终端窗口自动隐藏

### 计划支持功能

| 功能           | 所属阶段    | 说明                                        |
| ------------ | ------- | ----------------------------------------- |
| **记忆系统**     | Step 8  | 自动记忆用户偏好与项目约定，跨会话持久化（System Prompt 已留接入位） |
| **快捷命令系统**   | Step 9  | `/help`、`/clear`、`/init` 等斜杠命令，快速触发操作     |
| **Skill 系统** | Step 10 | 可插拔技能模块，封装复杂工作流为可复用技能                     |
| **Hook 系统**  | Step 11 | 工具执行前后的钩子机制，支持日志、拦截、过滤                    |
| **SubAgent** | Step 12 | 子代理系统，支持并行调度、上下文隔离与结果回传                   |

---

## 🚀 快速开始

### 环境要求

- **Go 1.26+**
- 支持的操作系统：Windows 10+、macOS、Linux

### 构建与运行

```bash
# 克隆项目
git clone https://github.com/MeiCorl/CodePilot.git
cd CodePilot

# 编译
go build -o codepilot.exe ./src

# 启动（自动分配端口并打开浏览器）
./codepilot.exe
```

启动后 CodePilot 会依次：加载配置并初始化 LLM Provider → 启动 HTTP + WebSocket 服务（绑定 `127.0.0.1`，端口自动分配）→ 自动打开默认浏览器访问 Web UI → 在后台静默运行。浏览器关闭后自动检测并优雅退出，也可在启动终端按 `Ctrl+C` 手动退出。

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
    }
}
```

> 💡 除 `provider`、`model`、`api_key`（必填）外，其余字段均可省略——省略时使用下文默认值，CodePilot 开箱即用。

#### 配置项参考

##### LLM 基础

| 参数           | 说明                                                | 默认值  |
| -------------- | --------------------------------------------------- | ------ |
| `provider`     | LLM 供应商：`anthropic` 或 `openai`                  | 必填   |
| `model`        | 模型名称（如 `claude-sonnet-4-20250514`、`gpt-4o`）  | 必填   |
| `base_url`     | API 基础地址（支持代理/私有部署/兼容网关）            | 官方默认 |
| `api_key`      | API 密钥                                            | 必填   |
| `max_tokens`   | 单次回复最大输出 token 数                            | `16384` |
| `timeout`      | LLM 请求超时（秒）                                   | `180`  |
| `max_retries`  | 可重试错误的最大重试次数                              | `2`    |

##### 工具与执行

| 参数                               | 说明                                                                 | 默认值     |
| ---------------------------------- | -------------------------------------------------------------------- | ---------- |
| `tools.enabled`                    | 启用工具白名单，工具名须与 `Tool.Name()` 一致（**大驼峰**：`ReadFile`/`WriteFile`/`EditFile`/`Bash`/`Glob`/`Grep`）；空数组=启用全部 | 全部内置工具 |
| `tool_execution_timeout_seconds`   | 单次工具执行超时（秒）                                                | `30`       |
| `tool_working_directory`           | 工具沙箱根目录；留空则使用进程启动时的工作目录                          | 进程 cwd   |

##### 上下文窗口

| 参数                       | 说明                                                         | 默认值    |
| -------------------------- | ------------------------------------------------------------ | --------- |
| `context_window_size`      | 模型上下文窗口总大小（token 数），用于溢出检查与状态栏展示      | `200000`  |
| `max_agent_loop_iterations`| Agent Loop 最大迭代次数（一次迭代 = 一次 LLM 调用 + 可能的工具执行） | `50`      |
| `context_safety_margin`    | 上下文安全余量（token 数），剩余低于此值时注入提示让模型总结收尾 | `4096`    |

##### 权限

| 参数                 | 说明                                                       | 默认值     |
| -------------------- | ---------------------------------------------------------- | ---------- |
| `permissions.mode`   | 权限模式：`strict` / `default` / `permissive`              | `default`  |
| `permissions.rules`  | 自定义规则列表，每条含 `tool` / `pattern` / `action` / 可选 `reason` | `[]`       |

**三种权限模式速查**：

| 模式            | icon | 工具行为             | 越界路径 | 适用场景             |
| ------------- | ---- | ---------------- | ---- | ---------------- |
| 严格 strict     | 🔒   | 读放行，写/执行需确认      | 拒绝   | 处理陌生项目、批量改动前谨慎评估 |
| 默认 default    | 🛡   | 读/写放行，Bash 执行需确认 | 需确认  | 日常开发（推荐起始档位）     |
| 放行 permissive | 🔓   | 除黑名单外全部自动放行      | 放行   | 高度信任的本地项目、自动化批处理 |

> 模式切换是**运行时内存态**，重启 CodePilot 后回到 `setting.json` 中配置的档位。若需永久切换，编辑全局或项目级 `setting.json` 中的 `permissions.mode`。

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

| 参数                               | 说明                                              | 默认值 |
| ---------------------------------- | ------------------------------------------------- | ------ |
| `mcp.servers`                      | 外部工具服务器列表（stdio / http 两种传输）          | `[]`   |
| `mcp.handshake_timeout_seconds`    | 单个 server 握手总耗时上限（秒，含 Initialize + ListTools） | `30`   |
| `mcp.list_tools_cache_ttl_seconds` | `tools/list` 结果缓存时长（秒）                     | `60`   |

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

| 参数                                 | 说明                                                   | 默认值  |
| ------------------------------------ | ------------------------------------------------------ | ------- |
| `compaction.enabled`                 | 压缩总开关；`false` 整体降级为纯滑动窗口                | `true`  |
| `compaction.tool_result_threshold`   | 工具结果存盘阈值（token），超过则 L1 存盘 + 预览替换    | `5120`  |
| `compaction.preview_tokens`          | L1 预览头部保留长度（token）                            | `500`   |
| `compaction.auto_trigger_margin`     | L2 自动触发余量（token），剩余 ≤ 此值且未熔断时触发摘要 | `20000` |
| `compaction.manual_target_margin`    | `/compact` 手动触发的目标余量（token）                  | `3000`  |
| `compaction.keep_recent_tokens`      | L2 摘要后尾部保留的近期原文（token）                    | `10000` |
| `compaction.keep_recent_min_messages`| 近期原文最少保留条数（与上一项取较大者）                | `5`     |
| `compaction.breaker_threshold`       | 摘要连续失败次数达到此值后会话级熔断                    | `3`     |

**调参建议**：

- **窗口较小的模型**（如 GPT-4o mini 128K）：把 `context_window_size` 调小、`auto_trigger_margin` 相应下调（如 13000），避免频繁触发 L2 摘要
- **想保留更多近期上下文**：调大 `keep_recent_tokens` / `keep_recent_min_messages`
- **压缩太激进/太保守**：`tool_result_threshold` 控制单个工具结果何时被预览化，调大则更多原文留在上下文（更准但更费 token），调小则更省
- **完全关闭压缩**：`"enabled": false`（其余字段被忽略，降级为纯滑动窗口）
- **摘要反复失败被熔断**：会话内自动 L2 暂停，可手动输入 `/compact` 重置重试一次

> 两层压缩的工作机制（L1 预览化 / L2 摘要 / 熔断 / 紧急压缩）详见后文「核心机制详解 · 上下文压缩」。

### 使用说明

1. **开始对话**：在 Web UI 底部输入框输入你的需求，按回车发送
2. **工具调用**：Agent 会根据需要自动调用 ReadFile、WriteFile、Bash 等工具完成任务
3. **查看改动**：WriteFile/EditFile 工具完成态会在工具块头部出现「查看改动」按钮，点击弹出双栏 diff 弹窗
4. **权限模式切换**：点击状态栏 `permission` 区域弹出 3 选 1 下拉（严格/默认/放行），切换后立即生效，无需重启
5. **权限确认（HITL）**：当工具调用命中 `ask` 规则时，Agent Loop 会暂停并弹出确认对话框，可选「拒绝 / 本次允许 / 本会话允许 / 永久允许」
6. **中断操作**：点击输入框旁的取消按钮可中断当前 Agent Loop
7. **会话管理**：
   - 左侧会话列表可查看历史会话
   - 输入 `/new` 创建新会话
   - 输入 `/sessions` 查看所有会话
   - 输入 `/resume <id>` 恢复指定会话
   - 输入 `/compact` 手动压缩当前会话上下文（历史摘要化）
   - 输入 `/dump` 把当前会话上下文 + System Prompt 导出到会话目录下的 `dump.json` / `dump.md`

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
│   │   │       │   └── memory.go        #       自动记忆接入位（Step 8 接入）
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

> 当前最新版本 **V1.4.0** · 最近更新 **2026-06-16** · 进行中 **—**（11/12 步骤完成，下一步 Step 8 记忆系统）
> 详细进度见 [.harness/PROGRESS.md](.harness/PROGRESS.md)

```
[███████████████████████████████] 11/12 步骤完成（~92%，Step 8 待开始）

✅ Step 1    — LLM 打通（双 Provider + 流式）
✅ Step 1.1  — UI 界面重构（TUI → WebUI）
✅ Step 1.2  — 对话栏富文本渲染增强
✅ Step 1.3  — WebUI 流式渲染
✅ Step 1.4  — WebUI 工具展示优化（双栏 diff 弹窗）
✅ Step 2    — 工具系统集成（Tool/Registry/Builtin）
✅ Step 3    — ReAct 与 Agent Loop 实现（多轮迭代 + 工具错误回灌 + 优雅中断）
✅ Step 4    — System Prompt 设计（Builder + 4 Source + 模板变量）
✅ Step 5    — 权限系统设计（运行时档位切换 + HITL + 危险命令黑名单 + 路径沙箱）
✅ Step 6    — MCP 协议实现（JSON-RPC + stdio/HTTP + 连接池 + 适配器 + 重连）
✅ Step 7    — 上下文管理（两层压缩 L1 工具结果预览化 + L2 整体摘要 + 熔断 + 紧急压缩）
⏳ Step 8    — 记忆系统（自动记忆用户偏好与项目约定，跨会话持久化）
⏳ Step 9    — 快捷命令系统
⏳ Step 10   — Skill 系统
⏳ Step 11   — Hook 系统
⏳ Step 12   — SubAgent
```

---

## 📄 许可证

[MIT License](LICENSE)
