# Step 1.4 — WebUI 工具展示优化 · 任务拆分

## Task 1: 后端 FileDiff 数据模型 + 进程内 Store

**状态**：已完成

**目标**：定义 `FileDiff` 数据结构与线程安全的内存 Store，提供 set/get 方法与容量保护。

**影响文件**：
- `src/internal/interaction/web/file_diff_store.go` — 新建
- `src/internal/interaction/web/file_diff_store_test.go` — 新建

**依赖**：无

**具体内容**：
1. 定义 `FileDiff` 结构：`ToolUseID / FilePath / Before / After / Language / UpdatedAt`。
2. 定义 `FileDiffStore`：`map[string]FileDiff` + `sync.RWMutex`；提供 `Set(toolUseID, diff) bool` / `Get(toolUseID) (FileDiff, bool)` / `Delete(toolUseID)`。
3. 容量保护：单条 `Before+After` 字节数超过 2 MB 时 `Set` 返回 `false` 并打 warn 日志（不写入）。
4. 语言识别：后端按文件后缀识别有限语言集合（`.go`/`.md`/`.json`/`.xml`/`.py`/`.ts`/`.js`/`.css`/`.yaml`/`.yml`/`.html`/`.sql`/`.sh`），其他传空字符串表示纯文本。
5. 单测：覆盖并发 set/get、容量上限拒绝、未识别后缀返回空 Language。

**参考资料**：
- `src/internal/tool/builtin/write_file.go` 已有 WorkingDirectory 模式。

---

## Task 2: WriteFile / EditFile 接入 diff 采集

**状态**：已完成

**目标**：让两个工具在执行成功后把 before/after 写入 `FileDiffStore`。

**影响文件**：
- `src/internal/tool/builtin/write_file.go` — 改
- `src/internal/tool/builtin/edit_file.go` — 改
- `src/internal/tool/builtin/register.go` — 改：在 `NewWriteFileTool` / `NewEditFileTool` 注入 DiffStore 字段

**依赖**：Task 1

**具体内容**：
1. `WriteFileTool` 与 `EditFileTool` 增加 `DiffStore *web.FileDiffStore` 字段（**可为 nil**，store 为 nil 时跳过 Set 不 panic）。
2. `WriteFile`：在 `os.WriteFile` 之前 `os.ReadFile` 已存在的旧内容（如有；不存在则 `Before=""`），写入成功后构造 `FileDiff` 并 Set。
3. `EditFile`：复用已有的 `original` 变量作为 `Before`，`newContent` 作为 `After`，执行成功后 Set。
4. `FilePath` 用 `absPath`（已通过沙箱解析）。
5. `Language` 通过后缀识别函数计算（与 Store 内同一份逻辑，可下沉到 `file_diff_store.go` 作为 `DetectLanguage(path string) string`）。
6. 单测：注入 mock store 后能查到 diff；store 为 nil 时不 panic；写入失败时不影响主流程返回值。

**参考资料**：
- `src/internal/tool/builtin/edit_file.go:84` 已有 `original := string(content)`。

---

## Task 3: WebSocket `get_file_diff` 协议 + 路由处理

**状态**：已完成

**目标**：新增 `get_file_diff` 客户端消息 + `file_diff` 服务端消息 + 路由处理。

**影响文件**：
- `src/internal/interaction/web/protocol.go` — 改：新增 `GetFileDiffPayload` / `FileDiffPayload` / `MsgTypeGetFileDiff` / `MsgTypeFileDiff`
- `src/internal/interaction/web/handler.go` — 改：dispatch 路由表新增分支

**依赖**：Task 1

**具体内容**：
1. `GetFileDiffPayload`：`{ tool_use_id: string }`
2. `FileDiffPayload`：`{ tool_use_id, found, reason, file_path, language, before, after }`
3. `reason` 取值：`"not_found"`（store 查不到） / `"too_large"`（Store 拒绝写入，理论上不会出现但保留分支） / `""`（成功）。
4. 鉴权：复用现有 `isAuthenticated` 校验。
5. 单元测试：found / not_found 两种回包路径。

**参考资料**：
- `src/internal/interaction/web/handler.go:700-702` `sendToolCallEnd` 模式可仿照。

---

## Task 4: 前端引入 diff-match-patch + 弹窗样式

**状态**：已完成

**目标**：在 `static/vendor` 引入 diff-match-patch 库（自包含），新增弹窗与行级高亮 CSS。

**影响文件**：
- `src/internal/interaction/web/static/vendor/diff-match-patch.min.js` — 新建（拷贝 diff-match-patch 1.0.5 min 版）
- `src/internal/interaction/web/static/index.html` — 改：head 追加 `<script src="vendor/diff-match-patch.min.js">`
- `src/internal/interaction/web/static/style.css` — 改：新增 `.diff-modal` / `.diff-grid` / `.diff-line-add` / `.diff-line-del` / `.diff-line-ctx` 样式

**依赖**：无

**具体内容**：
1. 使用 diff-match-patch 1.0.5（Google 官方维护）作为内嵌 vendor 资源。
2. CSS 设计：固定宽度 80% 视口、最大高度 80vh、左右两列等宽、行高 1.5、行号列固定宽。
3. 配色与 Step 1.2 highlight 主题 token 对齐（删除红/新增绿/未变白底）。
4. 弹窗默认居中、Esc 关闭、点击遮罩关闭、关闭后销毁 DOM。

**参考资料**：
- `src/internal/interaction/web/static/vendor/highlight-theme.css` 配色 token。
- https://github.com/google/diff-match-patch 官方仓库。

---

## Task 5: 前端弹窗组件 + 双栏 diff 渲染 + highlight.js 高亮

**状态**：已完成

**目标**：实现 `openFileDiffModal(toolUseId)`：通过 WS 拉取 diff 数据并渲染弹窗，含语法高亮。

**影响文件**：
- `src/internal/interaction/web/static/app.js` — 改

**依赖**：Task 3 + Task 4

**具体内容**：
1. 注册后端 `file_diff` 消息回调，存入 `state._fileDiffCallbacks` 队列。
2. `openFileDiffModal(toolUseId)`：
   - 弹窗骨架（loading 态）插入 DOM；
   - `ws.send({type:'get_file_diff', payload:{tool_use_id: toolUseId}})`；
   - 10 秒超时未回 → 关闭 loading 显示错误。
3. 收到回包：`found=false` → 显示 `reason` 文案；`found=true` → 进入渲染流程。
4. 行级 diff：用 `new diff_match_patch().diff_main(before, after); diff_cleanupSemantic(diffs);` 拿到 `[op, text][]`。
5. 按 `op` 拆行：相等（白底） / 插入（仅显示在右栏，绿底） / 删除（仅显示在左栏，红底）。
6. 高亮：对 `Before` / `After` 全文调用 `hljs.highlight(language, content, {ignoreIllegals:true})`，未识别语言时回退纯文本 `<pre>`。
7. 弹窗头部：文件名 + 工具名 + 关闭按钮。
8. 错误处理：网络断开 / JSON 解析失败 / `tool_use_id` 不匹配 → 弹窗内统一提示。
9. 单元测试：在 `e2e_integration_test.go` 框架下新增 case：模拟 WriteFile 工具调用结束后，前端 ws.get_file_diff 能拿到非空 payload。

**参考资料**：
- 现有 `enhanceCodeBlocks` 高亮实现可复用其 hljs 上下文与 DOMPurify 处理。
- diff-match-patch API 文档（行级 diff 推荐用 `diff_linesToChars_` / `diff_charsToLines_` 提升大文本性能，本任务先用 `diff_main`，如性能不足再优化）。

---

## Task 6: 工具块「查看改动」按钮 + 接入主流程

**状态**：已完成

**目标**：在 `WriteFile` / `EditFile` 完成态工具块头部新增"查看改动"按钮，点击触发弹窗；后端启动时把 `FileDiffStore` 注入到工具构造器。

**影响文件**：
- `src/internal/interaction/web/static/app.js` — 改：`appendToolStartNode` + `updateToolEndNode` + 历史会话恢复分支
- `src/internal/tool/builtin/register.go` — 改：注册工具时把 `FileDiffStore` 实例注入
- `src/internal/runtime/console/console.go`（或对应 runtime 入口） — 改：构造 `FileDiffStore` 单例并传入工具

**依赖**：Task 2 + Task 5

**具体内容**：
1. `appendToolStartNode` 头部追加 `.message-tool-actions` 容器（初始为空）。
2. `updateToolEndNode` 中若 `toolName ∈ {WriteFile, EditFile}` 且 `status==='completed'`，向 `.message-tool-actions` 插入"查看改动"按钮。
3. 按钮点击：`stopPropagation()` 防止触发 header 的折叠 toggle；调用 `openFileDiffModal(toolUseId)`。
4. 历史会话恢复分支（`m.tool_call` 存在）：根据 `m.tool_call.name` 决定是否插入按钮。
5. runtime 启动时构造 `web.NewFileDiffStore()`，注入到 `NewWriteFileTool` / `NewEditFileTool`。
6. 单测：补一个 `register_test.go` case，验证 store 注入后 WriteFile/EditFile 工具能 Set。

**参考资料**：
- `src/internal/interaction/web/static/app.js:867-957` `appendToolStartNode`。
- `src/internal/interaction/web/static/app.js:961-...` `updateToolEndNode`。
- `src/internal/interaction/web/static/app.js:722-738` 历史会话工具块恢复路径。

---

## Task 7: 端到端验证

**状态**：已完成

**目标**：覆盖核心场景、错误场景与边界条件；同步更新 `checklist.md`。

**影响文件**：
- `docs/step1.4-WebUI工具展示优化/checklist.md` — 同步更新
- `src/internal/interaction/web/task6_test.go` — 新增 5 个 Task 6 e2e 用例

**依赖**：Task 1–6

**具体内容**：
1. 启动 WebUI，会话内执行 `WriteFile` / `EditFile`。
2. 工具块头部出现"查看改动"按钮。
3. 点击按钮弹出双栏 diff，新增绿、删除红、未变白。
4. `.go` / `.md` / `.json` / `.xml` 文件分别能看到 hljs 高亮。
5. `EditFile` 多次修改同一文件 → 每次都是独立弹窗，不合并。
6. `EditFile` 未找到 old_string 报错 → 工具块不应出现"查看改动"按钮。
7. 进程重启后加载历史会话 → 工具块无按钮 / 按钮点击显示 reason。
8. 大文件（> 2 MB）→ 拉取得到 `reason="too_large"`。
9. Esc / 点击遮罩 / 关闭按钮 三种关闭方式均生效。
10. 现有 Step 2/3/4 工具调用流程不回归。

**实际完成情况**：
- 新增 5 个 e2e 集成测试到 `task6_test.go`：`TestStep1_4_ViewDiffButton_EndToEnd_WriteFile` / `EditFile` / `NotFoundOnMissingID` / `NilStore` / `MultipleParallel`，全部 PASS
- 真实启动服务做 HTTP 资源探测（/、/app.js、/style.css、/vendor/diff-match-patch.min.js 全部 200，size 正确）
- WS 协议冒烟：`get_file_diff` not_found / empty_tool_use_id 两条分支回包正确
- `checklist.md` 全部 24 项通过，1 项保留"待人工跑浏览器确认"（控制台无 error）
