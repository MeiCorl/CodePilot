# CodePilot

**一个通过Claude Code Vibe Coding实现的AI Coding Agent，** 通过 Web UI 与 Agent 交互，自主调用工具完成代码编写、文件操作、命令执行等复杂任务。**(虽然无法与Claude Code等主流Coding Agent相提并论，但用作转入AI Agent开发练手，可帮助加强理解如何从零实现一个AI Agent）**

[Go Version](https://go.dev/)
[License](LICENSE)

---

## 📖 项目背景

CodePilot 是一个从零构建的终端 AI Coding Agent（类似 Claude Code / Cursor Agent），使用 Go 语言实现。它以 Web UI 作为主要交互入口，通过 Agent Loop 循环与 LLM 交互并调用内置工具，自主完成用户提出的编程任务。

项目的核心目标是构建一个 **高性能、高可用、高扩展、高安全** 的 AI Agent 系统，采用 5 层垂直分层架构，层间通过标准接口解耦，便于独立演进与功能扩展。

## ✨ 功能特性

### 已支持功能


| 功能                           | 说明                                                                                                   |
| ---------------------------- | ---------------------------------------------------------------------------------------------------- |
| **双 LLM 协议支持**               | Anthropic（Claude）+ OpenAI（GPT）双 Provider 适配，统一通过 `ContentBlock` 抽象交互                                 |
| **Web UI 交互**                | 基于 HTTP + WebSocket 的全双工通信，深色主题界面，自动调起浏览器                                                            |
| **流式 Markdown 实时渲染**         | LLM 输出实时解析为格式化 HTML，支持标题、列表、代码块、表格等元素                                                                |
| **代码语法高亮**                   | 基于 highlight.js 的 18+ 语言自动语法高亮，含代码块语言标签与一键复制                                                         |
| **ReAct 推理循环**               | 思考→决策→行动→观察的迭代循环，直到 LLM 认为任务完成                                                                       |
| **多工具并行调用**                  | 一次 LLM 响应中包含多个工具调用时，按权限分组并行执行                                                                        |
| **内置工具集**                    | `ReadFile`（读文件）、`WriteFile`（写文件）、`EditFile`（编辑文件）、`Bash`（命令执行）、`Glob`（文件查找）、`Grep`（内容搜索）             |
| **工具块「查看改动」双栏 diff**         | WriteFile/EditFile 完成态工具块头部「查看改动」按钮，点击弹出双栏 diff 弹窗（Before/After 全文 + 行级 + 词级高亮）                      |
| **迭代上限保护**                   | 默认最大 50 次迭代，达到上限后注入提示让模型优雅收尾                                                                         |
| **上下文窗口管理**                  | Token 溢出保护，空间不足时自动提示模型总结当前进展                                                                         |
| **分层 System Prompt**         | `Builder` 模式组装 4 个 Source（static / environment / agents_md / memory），行为规约、环境上下文、AGENTS.md 合并、自动记忆接入位 |
| **Anthropic Prompt Caching** | SystemBlocks 多段带 `cache_control: ephemeral, ttl=5m` 标记，第二轮起命中服务端缓存降低成本与延迟                            |
| **SP 可观测性**                  | WebUI 状态栏显示 SP 总 token 估算 + 4 层 Source 小计 tooltip，开发者模式一键导出完整 SP 快照                                  |
| **AGENTS.md 双层合并**           | 全局 `~/.codepilot/AGENTS.md` + 项目级 `<cwd>/AGENTS.md` 按 H2 段解析，项目级同名段完全覆盖全局                            |
| **三层权限模式**                   | `strict`（严格）/ `default`（默认）/ `permissive`（放行）三档档位，运行时可通过状态栏下拉即时切换                                    |
| **可配置允许/拒绝/询问规则**            | 在 `setting.json` 中按「工具名 + 参数模式」声明 `allow` / `deny` / `ask`，支持路径 glob 与 Bash 命令前缀匹配                   |
| **多层配置合并**                   | 全局 + 项目级 + 会话级规则按优先级合并                                                                               |
| **人在回路（HITL）确认**             | 工具执行前通过 WebSocket 暂停 Agent Loop 等待用户确认，支持本次 / 本会话 / 永久三种授权范围                                         |
| **危险命令黑名单**                  | Bash 硬拦截（不可被配置绕过）：`rm -rf /`、`mkfs`、远程脚本下载执行（curl|sh / wget|bash）等                                   |
| **路径沙箱**                     | 工具内部 `ResolveInSandbox` 硬兜底 + 策略层 `IsPathOutsideSandbox` 双层防护                                        |
| **MCP 协议（Model Context Protocol）** | JSON-RPC 2.0 + stdio / Streamable HTTP 双传输，动态发现外部工具服务器，自动注册为 `mcp__<server>__<tool>` 命名工具，支持指数退避重连（1s/3s/9s） |
| **MCP WebUI 可观测**              | 工具块紫色 server 来源徽标 + 状态栏 MCP 健康区（绿/黄/红/灰四色圆点 + hover tooltip）                                         |
| **会话持久化**                    | 多会话管理 + JSON 持久化，支持会话恢复与历史回放                                                                         |
| **优雅中断**                     | 用户中断时保留已完成迭代的消息，支持后续恢复                                                                               |
| **异步日志系统**                   | 基于 zap 的文件日志，支持日志轮转                                                                                  |
| **跨平台支持**                    | Windows / macOS / Linux 自动调起浏览器，Windows 支持终端窗口自动隐藏                                                   |


### 计划支持功能


| 功能           | 所属阶段    | 说明                                        |
| ------------ | ------- | ----------------------------------------- |
| **高级上下文管理**  | Step 7  | 上下文压缩（摘要）、缓存策略，优化 token 利用率               |
| **记忆系统**     | Step 8  | 自动记忆用户偏好与项目约定，跨会话持久化（System Prompt 已留接入位） |
| **快捷命令系统**   | Step 9  | `/help`、`/clear`、`/init` 等斜杠命令，快速触发操作     |
| **Skill 系统** | Step 10 | 可插拔技能模块，封装复杂工作流为可复用技能                     |
| **Hook 系统**  | Step 11 | 工具执行前后的钩子机制，支持日志、拦截、过滤                    |
| **SubAgent** | Step 12 | 子代理系统，支持并行调度、上下文隔离与结果回传                   |


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

---

## 📁 项目结构

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
│   │   │   memory/                      # 记忆层（第 4 层）
│   │   ├── memory/                      # 记忆层（第 4 层）
│   │   │   ├── context/
│   │   │   │   └── window.go            #   滑动窗口上下文管理
│   │   │   └── session/
│   │   │       └── session.go           #   会话管理器，创建/恢复/JSON 持久化
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

## 🚀 快速开始

### 环境要求

- **Go 1.26+**
- 支持的操作系统：Windows 10+、macOS、Linux

### 构建

```bash
# 克隆项目
git clone https://github.com/MeiCorl/CodePilot.git
cd CodePilot

# 编译
go build -o codepilot.exe ./src
```

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

编辑 `~/.codepilot/setting.json`，填入你的 API Key：

```json
{
    "provider": "anthropic",
    "model": "claude-sonnet-4-20250514",
    "api_key": "sk-ant-your-api-key-here",
    "max_tokens": 16384,
    "timeout": 180,
    "max_retries": 2,
    "tools": {
        "enabled": ["read_file", "write_file", "bash", "glob", "grep"]
    }
}
```

**配置项说明：**


| 参数                               | 说明                             | 默认值      |
| -------------------------------- | ------------------------------ | -------- |
| `provider`                       | LLM 供应商：`anthropic` 或 `openai` | 必填       |
| `model`                          | 模型名称                           | 必填       |
| `base_url`                       | API 基础地址（支持代理/私有部署）            | 官方默认     |
| `api_key`                        | API 密钥                         | 必填       |
| `max_tokens`                     | 单次回复最大 token 数                 | `16384`  |
| `timeout`                        | 请求超时（秒）                        | `180`    |
| `max_retries`                    | 最大重试次数                         | `2`      |
| `tools.enabled`                  | 启用的工具列表                        | 全部内置工具   |
| `tool_execution_timeout_seconds` | 工具执行超时（秒）                      | `30`     |
| `tool_working_directory`         | 工具工作目录（为空则使用进程 cwd）            | 进程 cwd   |
| `context_window_size`            | 上下文窗口大小（token 数）               | `200000` |
| `max_agent_loop_iterations`      | Agent Loop 最大迭代次数              | `50`     |
| `context_safety_margin`          | 上下文安全余量（token 数）               | `4096`   |
| `permissions.mode`               | 权限模式：`strict` / `default` / `permissive` | `default` |
| `permissions.rules`              | 自定义权限规则列表（详见下方说明）              | `[]`     |
| `mcp.servers`                    | MCP 外部工具服务器列表（详见下方说明）          | `[]`     |


### 运行

```bash
# 启动 CodePilot（自动分配端口并打开浏览器）
./codepilot.exe
```

启动后，CodePilot 会：

1. 加载配置并初始化 LLM Provider
2. 启动 HTTP + WebSocket 服务（绑定 `127.0.0.1`，端口自动分配）
3. 自动打开默认浏览器访问 Web UI
4. 在后台静默运行，等待交互

浏览器关闭后，CodePilot 会自动检测并优雅退出。也可在启动终端按 `Ctrl+C` 手动退出。

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

### 权限模式速查


| 模式            | icon | 工具行为             | 越界路径 | 适用场景             |
| ------------- | ---- | ---------------- | ---- | ---------------- |
| 严格 strict     | 🔒   | 读放行，写/执行需确认      | 拒绝   | 处理陌生项目、批量改动前谨慎评估 |
| 默认 default    | 🛡   | 读/写放行，Bash 执行需确认 | 需确认  | 日常开发（推荐起始档位）     |
| 放行 permissive | 🔓   | 除黑名单外全部自动放行      | 放行   | 高度信任的本地项目、自动化批处理 |


> 切换是 **运行时内存态**，重启 CodePilot 后回到 `setting.json` 中配置的档位。若需永久切换档位，编辑 `~/.codepilot/setting.json` 或 `<cwd>/.codepilot/setting.json` 中的 `permissions.mode`。

### 权限规则配置

在 `setting.json` 的 `permissions.rules` 中按需声明自定义规则，每条规则包含 `tool`（工具名）、`pattern`（参数匹配模式）、`action`（动作）三个字段：

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

### MCP 服务器配置

在 `setting.json` 的 `mcp.servers` 中声明外部工具服务器，支持 stdio 和 HTTP 两种传输方式：

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

- stdio 类型：通过子进程 stdin/stdout 通信，适合本地工具服务器
- http 类型：通过 Streamable HTTP 协议通信，适合远程服务
- 单 server 失败不影响其他 server 和 CodePilot 启动
- MCP 工具自动注册为 `mcp__<server>__<tool>` 命名，走完整权限链路

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

> 当前最新版本 **V1.2.0** · 最近更新 **2026-06-09** · 进行中 **Step 6 — MCP 协议实现（8/9 Task 完成）**
> 详细进度见 [.harness/PROGRESS.md](.harness/PROGRESS.md)

```
[█████████████████████████░░░] ~82% 完成（9 步已完成，Step 6 进行中）

✅ Step 1    — LLM 打通
✅ Step 1.1  — UI 界面重构（TUI → WebUI）
✅ Step 1.2  — 对话栏富文本渲染增强
✅ Step 1.3  — WebUI 流式渲染
✅ Step 1.4  — WebUI 工具展示优化（双栏 diff 弹窗）
✅ Step 2    — 工具系统集成
✅ Step 3    — ReAct 与 Agent Loop 实现
✅ Step 4    — System Prompt 设计
✅ Step 5    — 权限系统设计（运行时档位切换 + HITL + 危险命令黑名单 + 路径沙箱）
🔧 Step 6    — MCP 协议实现（8/9，JSON-RPC + stdio/HTTP + 连接池 + 适配器 + 重连）
⏳ Step 7    — 上下文管理
⏳ Step 8    — 记忆系统
⏳ Step 9    — 快捷命令系统
⏳ Step 10   — Skill 系统
⏳ Step 11   — Hook 系统
⏳ Step 12   — SubAgent
```

---

## 📄 许可证

[MIT License](LICENSE)