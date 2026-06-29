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
