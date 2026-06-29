## §9 顶层 LLM / Agent 参数

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
