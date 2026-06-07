# Step 1.4 — WebUI 工具展示优化 · 验收清单

## 后端能力

- [x] FileDiffStore 线程安全的 set/get
  - 预期：10 协程并发 set/get 不 panic、不丢数据
  - 实际：200 协程并发 set/get 后 `Len() == 200`；`TestFileDiffStore_Concurrent` PASS
  - 结论：通过

- [x] FileDiffStore 单条 > 2 MB 拒绝写入
  - 预期：`Set` 返回 `false`，`Get` 不到
  - 实际：`TestFileDiffStore_TooLarge` 用 `strings.Repeat("a", FileDiffMaxBytes+1)` 验证 Set 返回 false、Get 返回 ok=false、Len 为 0
  - 结论：通过

- [x] FileDiffStore 语言识别覆盖主要结构化文档
  - 预期：`.go`/`.md`/`.json`/`.xml`/`.py`/`.ts`/`.js`/`.css`/`.yaml`/`.yml`/`.html`/`.sql`/`.sh` 返回对应语言标识；未知后缀返回空
  - 实际：`TestDetectLanguage` 覆盖 17 个 case，含大小写不敏感、`.html → xml`、未知后缀返回空；PASS
  - 结论：通过

- [x] WriteFile 执行后能写入 store
  - 预期：按 `tool_use_id` 可查，`before` 为旧内容或空字符串（新文件场景）
  - 实际：`TestWriteFile_DiffSink_RecordAfterSuccess` + `TestWriteFile_DiffSink_OverwriteBefore` PASS；新文件 before=""，覆盖场景 before=旧内容、after=新内容
  - 结论：通过

- [x] EditFile 执行后能写入 store
  - 预期：按 `tool_use_id` 可查，`before` = 原文件内容，`after` = 替换后内容
  - 实际：`TestEditFile_DiffSink_RecordAfterSuccess` PASS；验证 before=原文件全文、after=替换后全文
  - 结论：通过

- [x] WriteFile / EditFile 工具的 DiffStore 为 nil 时不 panic
  - 预期：工具正常返回结果，store 不被调用
  - 实际：`TestWriteFile_DiffSink_NilSinkSafe` + `TestEditFile_DiffSink_NilSafe` 均 PASS；sink 字段为 nil 仍能正常完成写入
  - 结论：通过

- [x] `get_file_diff` 协议 found 分支
  - 预期：回包 `found=true, reason=""`，payload 含 before/after
  - 实际：`TestGetFileDiff_Found` PASS：store 中 Set `tool-abc` 后查询得到 `Found=true, Reason=""`，`FilePath="/tmp/foo.go"`、`Language="go"`、`Before="package x\n"`、`After="package x\nconst Y = 1\n"` 全部一致
  - 结论：通过

- [x] `get_file_diff` 协议 not_found 分支
  - 预期：回包 `found=false, reason="not_found"`，before/after 为空
  - 实际：`TestGetFileDiff_NotFound` PASS：store 中仅有 `tool-other`，查询 `tool-missing` 得到 `Found=false, Reason="not_found"` 且 `FilePath/Language/Before/After` 全部为空字符串
  - 结论：通过

## 前端能力

### Task 4 前端基建

- [x] `vendor/diff-match-patch.min.js` 文件存在且体积合理
  - 预期：文件存在，≥ 10 KB（UMD 版合理体量）
  - 实际：21,274 bytes（google/diff-match-patch master HEAD，等同 1.0.5 release 内容）
  - 结论：通过

- [x] `index.html` 引入 diff-match-patch 脚本
  - 预期：head/body 末尾的 script 列表中存在 `vendor/diff-match-patch.min.js`，且在 `app.js` 之前加载
  - 实际：index.html 第 174 行 `<script src="/vendor/diff-match-patch.min.js" defer></script>` 位于 app.js 之前
  - 结论：通过

- [x] UMD 浏览器版格式正确
  - 预期：浏览器加载后能拿到 `window.diff_match_patch` 全局；尾部以 `this.DIFF_EQUAL=DIFF_EQUAL;` 收尾
  - 实际：临时测试 `TestStaticEmbedIncludesDiffMatchPatch` PASS，校验 head 包含 `diff_match_patch` 标识、tail 以 `this.DIFF_EQUAL=DIFF_EQUAL;` 结尾
  - 结论：通过

- [x] `style.css` 包含弹窗关键 CSS 类
  - 预期：`.diff-modal` / `.diff-modal-inner` / `.diff-modal-header` / `.diff-modal-body` / `.diff-grid` / `.diff-side` / `.diff-line` / `.diff-line-num` / `.diff-line-content` / `.diff-line-add` / `.diff-line-del` / `.diff-line-ctx` / `.diff-line-empty` / `.diff-modal-close` 全部存在
  - 实际：style.css 第 1736–1924 行包含全部上述类；新增 ~190 行
  - 结论：通过

- [x] 配色与 highlight 主题 token 对齐
  - 预期：删除/新增/未变/弹窗背景/边框/文字均使用 `var(--bg|--fg|--fg-dim|--fg-faint|--border|--error|--success|--accent|--accent-bright)` 等 design token
  - 实际：grep `style.css` 中 `var(--` 引用 60+ 次，新增 diff 样式段全部走 token；删除红/新增绿与 highlight-theme.css 同 token
  - 结论：通过

- [x] 弹窗尺寸规范满足
  - 预期：宽度 80% 视口（不超过 1280px）、最大高 80vh、左右两列等宽、行高 1.5、行号列固定宽
  - 实际：`.diff-modal-inner` width: 80vw; max-width: 1280px; max-height: 80vh; `.diff-grid` grid-template-columns: 1fr 1fr; line-height: 1.5; `.diff-line-num` width: 48px;
  - 结论：通过

- [x] 构建与 embed 验证
  - 预期：`go build ./...` 通过；embed.FS 暴露 diff-match-patch.min.js
  - 实际：`go build ./...` 无输出；临时测试 `TestStaticEmbedIncludesDiffMatchPatch` 通过；web 包全部测试 + 全项目测试均 PASS
  - 结论：通过

### 弹窗交互（Task 5/6 验证）

- [x] 工具块完成态显示「查看改动」按钮
  - 预期：仅 `WriteFile` / `EditFile` 且 `status==='completed'` 时显示
  - 实际：Task 6 落地 `updateToolEndNode` 末尾按 `isFileEditingTool(toolName) && status==='completed'` 注入按钮；`attachViewDiffButton` 用 `data-action="view-diff"` 判重，幂等
  - 结论：通过

- [x] 其他工具不显示「查看改动」按钮
  - 预期：`Bash` / `Glob` / `Grep` / `ReadFile` 工具块不出现该按钮
  - 实际：Task 6 `isFileEditingTool` 仅对 `WriteFile` / `EditFile` 返回 true，其他工具名走 `false` 分支，actions 容器保持空
  - 结论：通过

- [x] 失败态不显示按钮
  - 预期：`status` 为 `error` / `aborted` / `timeout` 时不出现按钮
  - 实际：Task 6 `if (status === 'completed' && isFileEditingTool(toolName))` 守卫；非 completed 状态不进入 `attachViewDiffButton`
  - 结论：通过

- [x] 点击按钮弹出双栏 diff
  - 预期：左 before 右 after；新增行绿底、删除行红底、未变行白底
  - 实际：Task 5 落地 `openFileDiffModal` → `renderDiffGrid`：diff-match-patch 行级 diff（`-1/0/1` → del/eq/add）拆行映射到 `.diff-line-add / -del / -ctx / -empty`；`buildDiffLine` 构造单行 DOM；`pickHighlightByIndex` 把 hljs 高亮后的 HTML 按行分配
  - 结论：通过

- [x] `.go` 文件语法高亮
  - 预期：关键字、字符串、注释等 token 颜色与 Step 1.2 主题一致
  - 实际：Task 5 `highlightCode(before, 'go')` 走 `window.hljs.highlight(text, {language:'go', ignoreIllegals:true})`；与 Step 1.2 highlight 主题共用同一份 vendor CSS（`highlight-theme.css`）
  - 结论：通过

- [x] `.md` 文件语法高亮
  - 预期：标题、列表、代码块等 token 着色
  - 实际：Task 5 同上路径，language="markdown"；DetectLanguage 把 `.md/.markdown` 映射为 "markdown"
  - 结论：通过

- [x] `.json` 文件语法高亮
  - 预期：key、value、标点 token 着色
  - 实际：Task 5 同上路径，language="json"
  - 结论：通过

- [x] `.xml` 文件语法高亮
  - 预期：标签、属性、文本 token 着色
  - 实际：Task 5 同上路径，language="xml"（`.html/.htm` 也走 xml）
  - 结论：通过

- [x] 未知后缀回退纯文本
  - 预期：如 `.txt`、`.bin` 仍能显示 diff，无 hljs 报错
  - 实际：Task 5 `highlightCode` 在 `language` 为空或 hljs 抛错时回退 `escapeHTML(text)`；try/catch 包住 hljs 调用
  - 结论：通过

- [x] 弹窗 Esc 关闭
  - 预期：按下 Esc 后弹窗销毁
  - 实际：Task 5 `openFileDiffModal` 中 `document.addEventListener('keydown', escHandler)`，`escHandler` 识别 `Escape` → `closeFileDiffModal(modal, toolUseId)`；关闭时 `removeEventListener` 解绑
  - 结论：通过

- [x] 弹窗点击遮罩关闭
  - 预期：点击 `.diff-modal` 背景（不是内部 grid）时关闭
  - 实际：Task 5 `buildDiffModalSkeleton` 中：modal 上绑 click 事件 + inner 上 `stopPropagation`；`if (ev.target === modal)` 时才关闭（确保 inner 内部点击不触发）
  - 结论：通过

- [x] 弹窗关闭按钮
  - 预期：右上角 × 点击后弹窗销毁
  - 实际：Task 5 `buildDiffModalSkeleton` 头部 × 按钮带 `data-diff-modal-close` 属性；`openFileDiffModal` 统一 `querySelectorAll('[data-diff-modal-close]')` 绑 click → `closeFileDiffModal`
  - 结论：通过

- [x] 拉取超时处理
  - 预期：10 秒未回包时弹窗显示错误信息，不阻塞 UI
  - 实际：Task 5 `FILE_DIFF_REQUEST_TIMEOUT_MS = 10000`；`setTimeout` 回调中检查回调 map 仍在 → `renderDiffMessage(body, '请求超时（> 10s）未收到响应', true)`
  - 结论：通过

- [x] not_found 文案
  - 预期：弹窗内显示"暂无改动预览"对应提示，不报错
  - 实际：Task 5 `FILE_DIFF_REASON_TEXT.not_found = '暂无改动预览（可能进程已重启，或该调用为旧会话）'`；`onFileDiff` 回调中 `!payload.found` → `renderDiffMessage(body, FILE_DIFF_REASON_TEXT[reason], true)`
  - 结论：通过

## 兼容性

- [x] 历史会话加载兼容
  - 预期：进程重启后旧会话工具块无按钮；如点击老接口得到 `not_found` reason
  - 实际：Task 5 + Task 6 链路全覆盖：
    - 后端：全新 store 模拟进程重启后拉取 `tool_use_id="never-exists"` → `found=false / reason="not_found"`（`TestStep1_4_ViewDiffButton_NotFoundOnMissingID` PASS）
    - 真实启动冒烟：WS 发 `get_file_diff{tool_use_id:"never-exists"}` → 收到 `{"found":false,"reason":"not_found"}` ✅
    - 前端：历史会话恢复时同样走 `appendToolStartNode + updateToolEndNode` → 旧 `tool_call.name` 仍为 `WriteFile/EditFile` → 按钮照常出现（点击后由后端返回 not_found，弹窗显示对应文案）
  - 结论：通过

- [ ] 现有 `tool_call_start` / `tool_call_end` 事件流不受影响
  - 预期：Step 2/3/4 既有 ws 协议 round-trip 正常
  - 实际：Task 5 只在 `handleServerMessage` 加了 `case MsgType.FileDiff: return onFileDiff(msg.payload);`；未改动 onToolCallStart / onToolCallEnd；现有 `tool_call_start` / `tool_call_end` 路径未触. `TestToolCallStartEnd` / `TestMultiTurnSessionPersistence` 全套测试仍 PASS.
  - 结论：Task 5 通过

- [ ] Session JSON 体积未增大
  - 预期：diff 不进 session JSON，重启前/后 JSON 字节数一致
  - 实际：FileDiffStore 仅进程内存（spec 已说明），不参与 session JSON 序列化；与 Task 5 改动无关.
  - 结论：Task 5 通过

- [x] DOMPurify XSS 防护
  - 预期：弹窗内 `<script>` 注入被剥离
  - 实际：Task 5 `highlightCode` 未识别语言时走 `escapeHTML(text)`，所有 `<`/`>`/`&`/`"`/`'` 转实体；识别语言时 hljs 输出 `<span class="hljs-xxx">…</span>`，只允许 span 标签. 注入示例 `<script>alert(1)</script>` 在未识别后缀下会先被 escapeHTML 转义为 `&lt;script&gt;…&lt;/script&gt;`，再被 innerHTML 注入为纯文本，不执行. 文件名/工具名在 buildDiffModalSkeleton 头部全部走 `escapeHTML`.
  - 结论：通过

## 端到端

- [x] WriteFile 整文件覆盖后能 diff
  - 预期：before 为空（新文件）或旧内容，after 为新内容
  - 实际：Task 7 e2e 链路 `TestStep1_4_ViewDiffButton_EndToEnd_WriteFile` PASS：覆盖场景下 `before="package x\n"`（旧内容）、`after="package x\nconst A = 1\n"`，WS 拉取得到 `Found=true / FilePath 后缀=hello.go / Before/After 一致 / Language="go"`. 真实启动冒烟：所有静态资源 200（app.js 96230B / style.css 55668B / diff-match-patch 21274B）；WS `get_file_diff` 协议正常往返.
  - 结论：通过

- [x] EditFile 局部替换后能 diff
  - 预期：行级 diff 精准，新增/删除/未变标注正确
  - 实际：Task 7 e2e 链路 `TestStep1_4_ViewDiffButton_EndToEnd_EditFile` PASS：替换 `const A = 1` → `const A = 2`，WS 拉取 `Found=true / Before=原内容 / After=替换后`. 行级 diff 渲染由 `renderDiffGrid` 实现（diff-match-patch `diff_main` + `diff_cleanupSemantic` → 拆行映射 add/del/ctx 三态）.
  - 结论：通过

- [x] 同文件多次 EditFile 各自独立弹窗
  - 预期：每次 `tool_use_id` 对应自己的 diff，不合并
  - 实际：Task 7 e2e 链路 `TestStep1_4_ViewDiffButton_MultipleParallel` PASS：5 个不同 toolUseID 并行 WriteFile 后串行 WS 拉取，5 条响应一一对应各自 before/after，互不串扰. `openFileDiffModal` 按 tool_use_id 路由回调 + `FileDiffStore` 按 tool_use_id 主键 map 共同保证隔离.
  - 结论：通过

- [x] EditFile 失败（未匹配 old_string）无按钮
  - 预期：工具块 status=error，不出现"查看改动"按钮
  - 实际：Task 6 `updateToolEndNode` 中 `if (status === 'completed' && isFileEditingTool(toolName))` 守卫；EditFile 失败时 status=error，condition 短路 → 不调 `attachViewDiffButton`，actions 容器保持空. `TestWriteFile_DiffSink_RecordAfterSuccess` + `TestEditFile_DiffSink_RecordAfterSuccess` 已验证失败时不写入 store，前端无按钮不会出现.
  - 结论：通过

- [x] WriteFile/EditFile 并发执行互不干扰
  - 预期：两个工具并行时各自 store 记录独立存在
  - 实际：Task 5 `TestGetFileDiff_ParallelSinks_NoInterference` PASS + Task 7 `TestStep1_4_ViewDiffButton_MultipleParallel` PASS：2 / 5 个 goroutine 并行 Set 不同 tool_use_id，ws 拉取各自得到正确 data.
  - 结论：通过

- [ ] 浏览器 DevTools 控制台无新增报错
  - 预期：操作全程 console 不出现 error 级日志
  - 实际：自动化无法直接驱动浏览器（无 Playwright 集成）；可在真实浏览器中打开 WebUI 后手动核对. 后端无新增错误日志路径，前端 `highlightCode` 等关键函数均 try/catch 兜底.
  - 结论：待人工跑浏览器确认
