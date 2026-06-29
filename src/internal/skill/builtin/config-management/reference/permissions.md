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
