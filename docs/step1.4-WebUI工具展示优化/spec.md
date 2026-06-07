# Step 1.4 — WebUI 工具展示优化

## 背景

- **现状**：WebUI 工具块（Step 2 落地）支持展开/折叠查看 `arguments` 与 `output`，但对 `WriteFile` / `EditFile` 这类"写文件"工具，用户只能从 output 看到一句简略摘要（如"已编辑 xxx（替换了 X 行 → Y 行）"），看不到具体改了哪几行。
- **痛点**：审阅 Agent 修改时，开发者需要重新打开 IDE 或 `git diff` 才能核对改动细节，闭环成本高、注意力被切碎。
- **目标**：在 WebUI 工具块上为 `WriteFile` / `EditFile` 提供"查看改动"入口，在弹窗中以左右双栏 diff 的形式直观展示本次修改，并附带语法高亮。

## 目标用户

- 开发者：在 WebUI 中通过自然语言驱动 Agent 修改代码后，需要快速核对改动是否正确、希望停留在 WebUI 中完成审阅。
- 代码审阅者：希望快速扫一眼某个工具调用对文件做了什么（加/删/改）。

## 能力清单

1. `WriteFile` / `EditFile` 执行完成后，对应工具块在"完成态"显示"查看改动"按钮；其他工具不显示。
2. 点击"查看改动"按钮，前端通过 WebSocket 按 `tool_use_id` 向后端请求本次调用的文件 diff（before/after 内容）。
3. 后端在工具执行时即把文件 diff 写入进程内存储，**不持久化**到 session JSON。
4. 弹窗以左右双栏展示 before / after 全文，中间以行级背景色标记新增（绿底）、删除（红底）、未变（白底）。
5. 对结构化文档（`.md`、`.go`、`.json`、`.xml`、`.py`、`.ts`、`.js`、`.css`、`.yaml`、`.html`、`.sql`、`.sh` 等）按文件后缀识别语言后调用 highlight.js 进行语法高亮。
6. 同一文件被多次修改时，每次工具调用对应一个独立 diff 弹窗，不做合并。
7. 弹窗支持点击遮罩关闭、按 Esc 关闭、显示文件名与工具名头部。

## 非功能要求

- **diff 数据生命周期**：仅保留在内存，进程退出即丢失；旧会话在重启后点击"查看改动"会得到"暂无改动预览"提示，不报错。
- **并发安全**：`WriteFile` / `EditFile` 并行执行时，按 `tool_use_id` 互不干扰；前端弹窗可同时打开多个不串扰。
- **内存上限**：单条 `Before+After` 容量上限 2 MB，超过则丢弃该条记录并在拉取时返回 `reason="too_large"`。
- **安全性**：弹窗内容经 DOMPurify 过滤，避免 XSS；高亮后的 HTML 不可注入。
- **性能**：弹窗打开后渲染 < 200 ms（10 KB 级别文件）。
- **兼容性**：不影响现有 `tool_call_start` / `tool_call_end` 事件流与历史会话加载（Step 2/3/4 的协议保持不变）。

## 设计骨架

```
src/internal/
├── interaction/web/
│   ├── file_diff_store.go            # 新增：进程内 FileDiff 存储中心
│   ├── file_diff_store_test.go       # 新增：单测
│   ├── protocol.go                   # 改：新增 get_file_diff / file_diff 协议
│   ├── handler.go                    # 改：dispatch 路由表新增分支
│   └── static/
│       ├── vendor/
│       │   └── diff-match-patch.min.js  # 新增：自包含 vendor 资源
│       ├── app.js                    # 改：工具块按钮 + 弹窗组件 + diff 渲染
│       ├── style.css                 # 改：弹窗样式 + 行级高亮配色
│       └── index.html                # 改：挂载 diff-match-patch.min.js
└── tool/builtin/
    ├── write_file.go                 # 改：执行后写入 diff store
    └── edit_file.go                  # 改：执行后写入 diff store
```

## Out of Scope（本步骤不做）

- 工具范围不扩展到 `Bash` / `Glob` / `Grep` / `ReadFile`。
- 不会对修改做"同一文件多工具调用"的聚合。
- 不会持久化 diff 到 session JSON。
- 不会引入更复杂的 diff 算法（如 Myers 增量、单词级 diff），统一使用 diff-match-patch 库的行级 diff。
- 不会在工具块上加版本对比、版本回滚。
- 不会对未变更行做折叠/展开切换（首版仅左右双栏静态展示）。
- 不修改 Session 持久化协议。
