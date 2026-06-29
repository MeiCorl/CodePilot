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
