---
name: config-management
description: "管理 CodePilot 自身配置 — 添加 / 删除 / 修改 / 查看 MCP server、权限规则、上下文压缩阈值、记忆系统、Skill 系统、工具白名单、模型 API key、超时时间、上下文窗口大小等。当用户提到「加 / 配 / 改 / 删 / 设置 / 管理 / 开启 / 关闭 + MCP、permission / 权限、上下文 / context window / 压缩、Skill / 技能、工具 / tool、model / 模型 / API key / base_url / 超时 / timeout / retries / 工作目录 / working directory」等任意配置场景,加载本 Skill 获取 setting.json 各 section 的完整 JSON schema、示例、默认值、是否需要重启、改写工作流与错误排查指引。改写一律使用 ReadFile + EditFile / WriteFile,全局与项目级两层路径均可写入。"
---

# config-management — CodePilot 自身配置管理

本 Skill 描述 CodePilot 全部配置项 (`setting.json`) 的结构、默认值与改写方法。
加载本 Skill 后,请遵循 §9「改写工作流」完成配置变更;遇到字段语义歧义时回到
对应章节查阅 schema。

---

## §1 配置文件总览

### 路径说明

CodePilot 支持两层配置文件,**项目级覆盖全局**(同名字段项目级优先):

| 层级 | 路径 | 适用场景 |
|------|------|---------|
| 全局 | `~/.codepilot/setting.json` | 跨项目生效的偏好(如默认模型、通用权限规则、所有项目都要用的 MCP) |
| 项目级 | `<cwd>/.codepilot/setting.json` | 当前项目专属配置(如项目专属 MCP、项目级压缩阈值) |

`<cwd>` 是 CodePilot 启动时的工作目录(通常就是用户运行 `codepilot` 命令所在目录)。

### 合并规则

- **标量字段**(string / int / bool):项目级非零值覆盖全局;
- **对象/数组字段**(`permissions.rules[]`、`mcp.servers[]`):项目级数组**整体替换**全局数组,不做元素级合并;
- **省略字段**:沿用另一层(全局有就沿用全局,全局无则用项目级)。

### 覆盖优先级(从高到低)

```
项目级 setting.json  >  全局 setting.json  >  内置默认值
```

### 全局 vs 项目级 决策树

```
用户提到「这个项目 / 当前仓库 / 这里」            → 改项目级
用户提到「所有项目 / 全局 / 默认 / 以后都用」      → 改全局
用户措辞模糊(如「帮我加个 MCP」「配个权限」)       → 主动用 AskUserQuestion 询问
用户未表态 + 单会话临时改即可                     → 询问一次后按用户选择写入
```

### 完整示例

```json
{
  "provider": "anthropic",
  "model": "claude-sonnet-4-20250514",
  "api_key": "sk-ant-xxx"
}
```

### 字段默认值与单位

所有字段均有内置默认值(详见各 section);`setting.json` 不存在的字段自动填默认。

### 是否需要重启

**需要重启 CodePilot 进程**才能生效(配置仅在启动时 Load 一次)。

### 错误排查

- 启动报错 `配置文件不存在: <path>` → 在该路径手动创建 `setting.json`,可复制 `config/setting.example.json` 作为起点;
- 启动报错 `解析配置文件失败(请检查 JSON 格式)` → 多半是 JSON 语法错(见 §10)。

---

## §2 mcp — MCP 客户端配置

### 路径说明

`mcp` 段控制 CodePilot 连接外部 MCP server,放在全局或项目级 setting.json 均可。

### JSON schema 摘要

```jsonc
{
  "mcp": {
    "servers": [MCPServerConfig, ...],   // server 列表
    "handshake_timeout_seconds": 30,     // 握手超时(秒)
    "list_tools_cache_ttl_seconds": 60   // tools/list 缓存 TTL(秒)
  }
}

// MCPServerConfig (type=stdio)
{
  "name": "filesystem",                  // 必填,server 唯一标识
  "type": "stdio",                       // 必填,合法值 stdio / http
  "command": "npx",                      // stdio 必填,可执行文件
  "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
  "env": {"KEY": "value"},               // 可选,注入到子进程环境
  "timeout": 30,                         // 单次 RPC 超时(秒)
  "disabled": false                      // true=跳过该 server
}

// MCPServerConfig (type=http)
{
  "name": "remote-mcp",
  "type": "http",                        // 必填
  "url": "https://example.com/mcp",      // http 必填,Streamable HTTP 端点
  "headers": {"Authorization": "Bearer xxx"},
  "timeout": 30,
  "disabled": false
}
```

### 完整示例(与 `config/setting.example.json` 完全一致,可复制粘贴)

```json
{
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
        "timeout": 30
      },
      {
        "name": "github",
        "type": "stdio",
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-github"],
        "env": {
          "GITHUB_PERSONAL_ACCESS_TOKEN": "ghp_replace_with_your_token"
        },
        "timeout": 60,
        "disabled": false
      },
      {
        "name": "remote-mcp",
        "type": "http",
        "url": "https://example.com/mcp",
        "headers": {
          "Authorization": "Bearer your-token-here"
        },
        "timeout": 30
      }
    ]
  }
}
```

### 字段默认值与单位

| 字段 | 默认 | 单位 | 说明 |
|------|------|------|------|
| `handshake_timeout_seconds` | 30 | 秒 | Connect + Initialize + ListTools 总耗时上限 |
| `list_tools_cache_ttl_seconds` | 60 | 秒 | tools/list RPC 结果缓存时长 |
| `MCPServerConfig.timeout` | 30 | 秒 | 单次 RPC 超时 |
| `MCPServerConfig.disabled` | false | — | true=启动时跳过该 server |

### 是否需要重启

**需要重启**。新增 / 修改 / 删除 server 后必须重启才会重新建连。

### 错误排查

- 启动报错 `mcp.servers[N].name 不能为空` → 检查每条 server 的 `name` 字段;
- 启动报错 `type=stdio 必须填写 command` → stdio 类型必须给 `command`;
- 启动报错 `type=http 必须填写 url` → http 类型必须给 `url`;
- 启动报错 `缺少 type(必须是 stdio/http)` → 补 `type` 字段;
- 启动报错 `不支持的 type=X` → 改为 `stdio` 或 `http`;
- 启动报错 `name=X 重复声明` → 同一层 setting.json 中 server name 必须唯一。

---

## §3 permissions — 权限系统配置

### 路径说明

`permissions` 段定义工具调用的白名单 / 黑名单 / HITL 规则,可放全局或项目级。

### JSON schema 摘要

```jsonc
{
  "permissions": {
    "mode": "default",                    // strict | default | permissive
    "rules": [                            // 自定义规则列表,按顺序匹配,命中即返回
      {
        "tool": "Bash",                   // 工具名(大驼峰)或 "mcp__*__*"; "*"=全部
        "pattern": "rm *",                // 参数匹配 glob / 命令前缀; "*"=全部
        "action": "deny",                 // allow | deny | ask
        "reason": "禁止 rm 删除命令"      // 可选,可读说明,HITL 对话框展示
      }
    ]
  }
}
```

### 完整示例

```json
{
  "permissions": {
    "mode": "default",
    "rules": [
      {"tool": "Bash", "pattern": "git *", "action": "allow", "reason": "Git 安全放行"},
      {"tool": "Bash", "pattern": "go test *", "action": "allow", "reason": "测试放行"},
      {"tool": "Bash", "pattern": "rm *", "action": "deny", "reason": "禁止删除"},
      {"tool": "Bash", "pattern": "sudo *", "action": "deny", "reason": "禁止提权"},
      {"tool": "mcp__*__*", "pattern": "*", "action": "ask", "reason": "MCP 工具需确认"}
    ]
  }
}
```

### 字段默认值与单位

| 字段 | 默认 | 说明 |
|------|------|------|
| `mode` | `default` | 三档:`strict`(严,默认全 ask)、`default`(中,内置工具默认 allow)、`permissive`(宽,默认全 allow) |
| `rules[].tool` | 无 | 大驼峰工具名 `Bash` / `WriteFile` 等;`mcp__<server>__<tool>` 格式匹配 MCP 工具;`*` 通配 |
| `rules[].pattern` | 无 | glob / 前缀匹配;`rm *` 匹配所有以 `rm` 开头的 Bash 命令 |
| `rules[].action` | 无 | `allow`(放行)/ `deny`(拒绝)/ `ask`(弹出 HITL 对话框) |
| `rules[].reason` | 空 | 仅展示用,无业务逻辑 |

### HITL 写回机制

当用户在 WebUI 权限确认对话框点击**「记住此选择」**时,Step 5 的 handler 会
**自动**向当前 setting.json 的 `permissions.rules[]` 追加一条对应规则(放行或拒绝),
无需 Agent 手动写入。Agent 在改写规则时无需模拟这条流程 — 用户下次同操作直接放行。

### 是否需要重启

**不需要**。规则变更在下次工具调用时即时生效(运行时追加)。

### 错误排查

- 启动报错 `action 必须是 allow/deny/ask` → 检查 `action` 拼写;
- 规则没生效 → 检查 `rules` 数组顺序(命中第一条即返回),把更具体的规则放前面;
- pattern 没匹配到 → 确认是 glob 还是前缀(`rm *` 是前缀;`*.go` 是 glob);Bash 命令
  推荐用前缀模式 `git *`、`go test *`。

---

## §4 compaction — 上下文压缩配置

### 路径说明

`compaction` 段控制 Step 7 的两层上下文压缩策略阈值与总开关。

### JSON schema 摘要

```jsonc
{
  "compaction": {
    "enabled": true,                      // 总开关,*bool,nil 视为 true
    "tool_result_threshold": 8192,        // 工具结果存盘阈值(token)
    "preview_tokens": 500,                // 预览头部保留长度(token)
    "auto_trigger_margin": 13000,         // 第二层自动触发余量(token)
    "manual_target_margin": 3000,         // 手动触发目标余量(token)
    "keep_recent_tokens": 10000,          // 近期原文保留量(token)
    "keep_recent_min_messages": 5,        // 近期原文最少保留条数
    "breaker_threshold": 3                // 熔断阈值(连续摘要失败次数)
  }
}
```

### 完整示例

```json
{
  "compaction": {
    "enabled": true,
    "tool_result_threshold": 8192,
    "preview_tokens": 500,
    "auto_trigger_margin": 13000,
    "manual_target_margin": 3000,
    "keep_recent_tokens": 10000,
    "keep_recent_min_messages": 5,
    "breaker_threshold": 3
  }
}
```

### 字段默认值与单位

| 字段 | 默认 | 单位 | 说明 |
|------|------|------|------|
| `enabled` | true | — | 总开关;`false` 时整体降级为纯滑动窗口 |
| `tool_result_threshold` | 5120 | token | 单条工具结果超此值触发存盘 + 预览替换 |
| `preview_tokens` | 500 | token | 存盘后内存中保留的截断预览大小 |
| `auto_trigger_margin` | 20000 | token | 剩余 token ≤ 此值时自动触发 L2 摘要 |
| `manual_target_margin` | 3000 | token | `/compact` 时允许压到的目标余量 |
| `keep_recent_tokens` | 10000 | token | 摘要后尾部保留的原文窗口 |
| `keep_recent_min_messages` | 5 | 条 | 与 `keep_recent_tokens` 取较大者 |
| `breaker_threshold` | 3 | 次 | 摘要连续失败此次数后本会话停止自动压缩 |

### 是否需要重启

**需要重启**。压缩阈值在启动期一次性读入,运行期不重读。

### 错误排查

- 想关闭压缩但没生效 → 确认 `enabled` 是 `false` 且没有 `omitempty` 干扰(JSON 中显式写 `false` 不会被吞);
- 想用更激进的压缩 → 把 `auto_trigger_margin` 调小(比如 `8000`),把 `keep_recent_tokens` 调小(比如 `4000`);
- 想观察效果 → 看 WebUI 头部 ctx 进度条 + 启动日志中的 `[compaction] triggered` 字样。

---

## §5 memory — 自动学习记忆配置

### 路径说明

`memory` 段控制 Step 8 自动学习记忆的总开关与索引注入阈值。

### JSON schema 摘要

```jsonc
{
  "memory": {
    "enabled": true,                      // 总开关,*bool,nil 视为 true
    "index_max_lines": 200,               // 索引注入行数上限
    "index_max_bytes": 25600,             // 索引注入字节上限
    "review_model": ""                    // 回顾专用模型(预留字段)
  }
}
```

### 完整示例

```json
{
  "memory": {
    "enabled": true,
    "index_max_lines": 200,
    "index_max_bytes": 25600,
    "review_model": ""
  }
}
```

### 字段默认值与单位

| 字段 | 默认 | 单位 | 说明 |
|------|------|------|------|
| `enabled` | true | — | `false` 时 Source 不注入 MEMORY.md、Reviewer 不触发回顾 |
| `index_max_lines` | 200 | 行 | 合并后索引文本超此行数截断 |
| `index_max_bytes` | 25600 (25 KB) | 字节 | 截断后再按字节二次截断 |
| `review_model` | "" (空) | — | 预留字段;首版固定复用主 provider/主模型 |

### 是否需要重启

**需要重启**。`enabled` 变更需要重启才能影响 Source / Reviewer 的初始化;阈值变更亦同。

### 错误排查

- 想完全关闭记忆 → `enabled: false`,重启后 MEMORY.md 不再被注入;
- 索引被截断丢失了部分记忆 → 调大 `index_max_lines` 和 `index_max_bytes`;
- `review_model` 写了但没生效 → 当前版本固定复用主模型,字段保留供后续扩展。

---

## §6 skill — Skill 系统配置

### 路径说明

`skill` 段控制 Step 10 Skill 系统的总开关与单 Skill 正文截断上限。

### JSON schema 摘要

```jsonc
{
  "skill": {
    "enabled": true,                      // 总开关,*bool,nil 视为 true
    "max_skill_size_bytes": 65536         // 单 SKILL.md 正文截断阈值(字节)
  }
}
```

### 完整示例

```json
{
  "skill": {
    "enabled": true,
    "max_skill_size_bytes": 65536
  }
}
```

### 字段默认值与单位

| 字段 | 默认 | 单位 | 说明 |
|------|------|------|------|
| `enabled` | true | — | `false` 时 main.go 完全跳过 Skill 加载(不调 LoadAll、不注册 use_skill 工具、不注入 SkillsIndexSource) |
| `max_skill_size_bytes` | 65536 (64 KB) | 字节 | 单 SKILL.md 正文超过此值将被截断(避免 use_skill 工具返回撑爆上下文) |

### 是否需要重启

**需要重启**。`enabled` 影响 main.go 启动期装配逻辑。

### 错误排查

- 设 `enabled: false` 后 WebUI `/skills` 仍可见列表 → 确认已重启;该开关在启动期生效;
- Skill 被截断 → 调大 `max_skill_size_bytes`,或精简 Skill 正文(推荐);
- 注意:`enabled: false` 时,**config-management Skill 也不可用**,但 SP 自描述段
  仍会引导 Agent 知道「配置文件在哪」(指向 Skill 不可用是已知降级)。

---

## §7 tools — 工具白名单

### 路径说明

`tools` 段控制 LLM 可见的工具白名单,可放全局或项目级。

### JSON schema 摘要

```jsonc
{
  "tools": {
    "enabled": ["ReadFile", "Bash"]       // 工具名白名单;Name 必须与 Tool.Name() 一致
  }
}
```

### 完整示例

```json
{
  "tools": {
    "enabled": ["ReadFile", "WriteFile", "EditFile", "Bash", "Glob", "Grep"]
  }
}
```

### 字段默认值与单位

| 字段 | 默认 | 说明 |
|------|------|------|
| `tools.enabled` | `[]` 或省略 = 启用全部 | 空数组视为「全部启用」;非空数组按 Name 白名单过滤 |

白名单外的工具既不会发给 LLM,也不会被 ToolHandler 执行 — 等效于「隐藏 + 禁用」。

### 是否需要重启

**需要重启**。工具列表在启动期装配,变更后必须重启。

### 错误排查

- 工具调不到 → 确认 `tools.enabled` 包含该工具的 `Name`(大小写敏感,如 `ReadFile` 不能写成 `readfile`);
- 写错工具名会被静默忽略 → 看启动日志中的 `[tool] registered` 列表,确认实际注册的工具名;
- 想临时禁用某个危险工具 → 把它从 `enabled` 列表移除(无需改权限规则)。

---

## §8 顶层 LLM / Agent 参数

### 路径说明

顶层字段(无 section 包裹)是 LLM 调用与 Agent Loop 的核心参数。

### JSON schema 摘要

```jsonc
{
  "provider": "anthropic",                // 合法值: anthropic | openai
  "model": "claude-sonnet-4-20250514",    // 模型名称
  "base_url": "",                         // 自定义 API 地址(留空用供应商默认)
  "api_key": "sk-ant-xxx",                // API 密钥(必填)
  "max_tokens": 16384,                    // 单次最大输出 token
  "timeout": 180,                         // 请求超时(秒)
  "max_retries": 2,                       // 最大重试次数
  "tool_execution_timeout_seconds": 30,   // 单次工具执行超时(秒)
  "tool_working_directory": "",           // 工具沙箱根目录(留空取 cwd)
  "context_window_size": 200000,          // 模型上下文窗口总大小(token)
  "max_agent_loop_iterations": 50,        // Agent Loop 最大迭代次数
  "context_safety_margin": 4096           // 上下文安全余量(token)
}
```

### 完整示例

```json
{
  "provider": "anthropic",
  "model": "claude-sonnet-4-20250514",
  "base_url": "",
  "api_key": "sk-ant-your-api-key-here",
  "max_tokens": 16384,
  "timeout": 180,
  "max_retries": 2,
  "tool_execution_timeout_seconds": 30,
  "tool_working_directory": "",
  "context_window_size": 200000,
  "max_agent_loop_iterations": 50,
  "context_safety_margin": 4096
}
```

### 字段默认值与单位

| 字段 | 默认 | 单位 | 说明 |
|------|------|------|------|
| `provider` | (必填) | — | 合法值 `anthropic` / `openai` |
| `model` | (必填) | — | 模型名称,如 `claude-sonnet-4-20250514` / `gpt-4o` |
| `base_url` | "" | — | 留空使用供应商默认地址;OpenAI 兼容代理可填自定义 URL |
| `api_key` | (必填) | — | API 密钥 |
| `max_tokens` | 16384 | token | 单次 LLM 输出上限 |
| `timeout` | 180 | 秒 | 单次 LLM 请求超时 |
| `max_retries` | 2 | 次 | 失败重试次数 |
| `tool_execution_timeout_seconds` | 30 | 秒 | 单次工具执行超时 |
| `tool_working_directory` | "" | — | 工具沙箱根目录;空 = 进程启动时的工作目录 |
| `context_window_size` | 200000 | token | Agent Loop 溢出检查与前端状态栏展示 |
| `max_agent_loop_iterations` | 50 | 次 | 一次 LLM 调用 + 工具执行 = 1 迭代;达到上限注入提示让模型优雅收尾 |
| `context_safety_margin` | 4096 | token | 剩余 token < 此值时 Agent Loop 注入总结提示 |

### 是否需要重启

**全部需要重启**。LLM / Agent 参数在启动期一次性读入。

### 错误排查

- 启动报错 `provider 不能为空` / `不支持的供应商` → 补 `provider` 字段或改成 `anthropic` / `openai`;
- 启动报错 `model 不能为空` → 补 `model` 字段;
- 启动报错 `api_key 不能为空` → 补 `api_key` 字段;
- 启动报错 `max_tokens 必须大于 0` → 改非零正数;
- 启动报错 `context_window_size 不能为负数` → 改成正整数;
- 想换 OpenAI 兼容代理 → 填 `base_url` + `provider=openai` + 对应 `api_key` + `model`;
- WebUI ctx 进度条不变化 → 已重启?该值仅启动期读取。

---

## §9 改写工作流

### 通用五步流程

```
1. 读    → ReadFile <目标 setting.json>,确认存在并定位锚点
2. 定位  → 用 EditFile 时先 grep 找到要插入/替换的位置;WriteFile 时先 cat 全文
3. 改    → 构造完整 JSON 片段(单字段不要缺逗号、不要漏引号)
4. 写    → EditFile(增量)或 WriteFile(全量);WriteFile 前可用 ReadFile 备份
5. 验证  → 重启 CodePilot,按字段类型观察生效信号(见下方)
```

### 全局 vs 项目级 选择决策树

```
用户措辞                → 写入位置
─────────────────────────────────────
"这个项目 / 当前仓库"    → <cwd>/.codepilot/setting.json
"所有项目 / 全局默认"    → ~/.codepilot/setting.json
"我自己的机器"           → ~/.codepilot/setting.json
"团队约定 / 团队规范"    → <cwd>/.codepilot/setting.json(随项目分发)
未表态 + 模糊            → AskUserQuestion 主动询问,不要默认全局
```

不明确时**必须主动询问**,避免误写到错误层级造成跨项目污染。

### HITL 写回规则

- WebUI 权限对话框中点**「记住此选择」** → Step 5 handler 自动向**当前 setting.json**
  追加一条 rule,**Agent 无需手动写入**;
- Agent 改写规则时,不要模拟 HITL 写回流程,直接用 EditFile 追加到 `permissions.rules[]` 即可。

### 修改后如何验证生效

| 字段类型 | 验证信号 |
|----------|---------|
| `mcp.servers[]` | 重启后启动日志 `[mcp] connected: <name> healthy=ok tools=N` |
| `permissions.rules[]` | 不需重启;立即用对应工具/命令试一次(HITL 或拦截生效) |
| `compaction.*` | 重启后观察 WebUI 头部 ctx 进度条 + 启动日志 |
| `memory.*` | 重启后 WebUI SP 可观测性面板中 `memory_index` 段 + MEMORY.md 注入行数 |
| `skill.*` | 重启后 WebUI `/skills` 列表变化(关闭后为空) |
| `tools.enabled` | 重启后 WebUI 工具下拉列表与 LLM 工具调用列表 |
| 顶层 LLM 参数 | 重启后 WebUI 头部 ctx 进度条按新 `context_window_size` 计算 |
| `model` / `api_key` | 重启后首次 LLM 请求成功 = 生效 |

### 是否需要重启(汇总)

- **需要重启**: `mcp.*` / `compaction.*` / `memory.*` / `skill.*` / `tools.*` / 顶层 LLM 参数
- **无需重启**: `permissions.rules[]` (运行时追加)

---

## §10 错误排查

### 常见 5 类报错与修复

#### 1. JSON 语法错

**报错信息**:`解析配置文件失败(请检查 JSON 格式): <yaml 错误>`

**常见原因**:
- 末尾漏逗号 / 多余逗号(最后一个字段后写了 `,`);
- 引号未闭合(`"api_key": "sk-ant-xxx` 漏右引号);
- 字符串里包含未转义的双引号(应写 `\"`)。

**修复**:用 IDE / `python -m json.tool` / `jq .` 校验语法。

#### 2. 字段名拼写错

**报错信息**:`配置校验失败: XXX 不能为空` 或「字段不生效但启动未报错」

**常见原因**:
- `api_key` 写成 `apiKey` / `apikey`;
- `context_window_size` 写成 `contextWindowSize` / `context-window-size`;
- `tool_execution_timeout_seconds` 拼写错;
- MCP server 的 `handshake_timeout_seconds` 写成 `handshakeTimeout`。

**修复**:对照本 Skill 各 section 的 `JSON schema 摘要` 核对字段名(JSON 字段名用 snake_case,不是 camelCase)。

#### 3. 字段值类型错

**报错信息**:启动未报但行为异常 / JSON parse 阶段隐式失败

**常见原因**:
- 数字写成了字符串:`"max_tokens": "16384"` ❌ → `"max_tokens": 16384` ✓
- 布尔写成了字符串:`"enabled": "true"` ❌ → `"enabled": true` ✓
- 数组写成了字符串:`"enabled": "ReadFile,Bash"` ❌ → `"enabled": ["ReadFile", "Bash"]` ✓

**修复**:数字不加引号、布尔不加引号、数组用 `[]` 包裹。

#### 4. 未知字段警告

**行为**:CodePilot 当前版本对未知字段**静默忽略**(不报错也不警告)。

**风险**:把 `permisions` 写成 `permissions`(少了 s) → 启动成功但权限规则不生效,
只能靠行为异常反推。

**修复**:改写完成后用 `jq . ~/.codepilot/setting.json` 重新查看文件结构,确认关键 section
(`provider` / `api_key` / `mcp` / `permissions` / `compaction` 等)都在顶层或子层正确位置。

#### 5. 启动失败的常见 5 类报错

| 报错 | 原因 | 修复 |
|------|------|------|
| `配置文件不存在: <path>` | 路径无文件 | 复制 `config/setting.example.json` 到目标路径 |
| `解析配置文件失败(请检查 JSON 格式)` | JSON 语法错 | 用 IDE / `jq` 校验修复 |
| `配置校验失败: provider 不能为空` | 顶层 `provider` 缺失 | 补 `"provider": "anthropic"` |
| `配置校验失败: 不支持的供应商 "X"` | provider 不是合法值 | 改为 `anthropic` / `openai` |
| `配置校验失败: api_key 不能为空` | `api_key` 缺失 | 补 `api_key` 字段 |
| `配置校验失败: mcp.servers[N] (name=X) type=stdio 必须填写 command` | stdio 缺 command | 补 `command` 字段 |
| `配置校验失败: mcp.servers[N] (name=X) 缺少 type(必须是 stdio/http)` | type 缺失 | 加 `"type": "stdio"` / `"http"` |

### 排查工作流

```
启动报错
  ├─ 含 "配置文件不存在"        → 创建文件,复制 setting.example.json
  ├─ 含 "解析配置文件失败"       → JSON 语法错,用 jq / IDE 校验
  ├─ 含 "配置校验失败: <字段>"  → 对照 §10 第 5 类报错表修复
  ├─ 含 "mcp.servers[N]"        → 跳到 §2 mcp 错误排查
  └─ 无报错但行为异常          → 跳到 §10 第 4 类(未知字段)检查字段名拼写
```

### 工具快捷

```bash
# 校验 JSON 语法
jq . ~/.codepilot/setting.json

# 定位字段(看实际生效的配置)
jq '.mcp.servers | length' ~/.codepilot/setting.json
jq '.permissions.rules' ~/.codepilot/setting.json

# 备份再改写
cp ~/.codepilot/setting.json ~/.codepilot/setting.json.bak
```