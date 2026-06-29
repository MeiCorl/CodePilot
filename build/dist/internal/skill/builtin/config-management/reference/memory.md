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
