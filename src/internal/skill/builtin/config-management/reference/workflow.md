## §10 改写工作流

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
| `hook.entries[]` | 重启后 WebUI 状态栏 hooks 子项、日志中的 hook 触发记录 |
| 顶层 LLM 参数 | 重启后 WebUI 头部 ctx 进度条按新 `context_window_size` 计算 |
| `model` / `api_key` | 重启后首次 LLM 请求成功 = 生效 |

### 是否需要重启(汇总)

- **需要重启**: `mcp.*` / `hook.*` / `compaction.*` / `memory.*` / `skill.*` / `tools.*` / 顶层 LLM 参数
- **无需重启**: `permissions.rules[]` (运行时追加)

---
