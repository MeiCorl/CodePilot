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
