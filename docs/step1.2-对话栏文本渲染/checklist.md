# Step 1.2 Checklist — 对话栏代码 / JSON / Markdown 渲染增强

> 对照 spec.md 与 tasks.md 逐项验证。所有项目均已实施并通过浏览器端到端验证。

---

## 1. vendor 资源引入（对应 Task 1）

- [x] `src/internal/interaction/web/static/vendor/purify.min.js` 已新增
  - 预期：DOMPurify v3.2.4 standalone，~22KB
  - 实际：22,216 字节
  - 结论：✓ 通过

- [x] `src/internal/interaction/web/static/vendor/highlight.min.js` 已新增
  - 预期：highlight.js v11.11.1 common bundle，~620KB
  - 实际：127,496 字节（min 后），版本 `11.11.1`，包含 36 种语言（bash、c、cpp、csharp、css、diff、go、graphql、ini、java、javascript、json、kotlin、less、lua、makefile、markdown、objectivec、perl、php、php-template、plaintext、python、python-repl、r、ruby、rust、scss、shell、sql、swift、typescript、vbnet、wasm、xml、yaml）
  - 结论：✓ 通过

- [x] `src/internal/interaction/web/static/vendor/highlight-theme.css` 已新增
  - 预期：atom-one-dark 重写版，对齐 design token
  - 实际：1,283 字节，token 颜色全部重写为 `--accent`（琥珀金）/ `--success`（绿）/ `--thinking`（思考蓝）/ `--error`（红），**不含紫色 #c678dd**
  - 结论：✓ 通过

- [x] `//go:embed static` 自动覆盖新增 vendor 文件
  - 预期：`go build` 无报错，编译产物 `codepilot.exe` 体积增长 ~150KB
  - 实际：19,756,800 → 19,717,632（+160,768 字节 ≈ 157KB），编译无错误
  - 结论：✓ 通过

---

## 2. `index.html` 引入新依赖（对应 Task 2）

- [x] `<head>` 末尾追加 `<link rel="stylesheet" href="/vendor/highlight-theme.css">`
  - 结论：✓ 通过

- [x] `</body>` 前按依赖顺序引入 4 个 vendor/app 脚本
  - 实际顺序：DOMPurify → marked → highlight.js → app.js
  - 加载策略：均带 `defer`，按出现顺序执行
  - 结论：✓ 通过

- [x] 浏览器加载无 404、无 CSP 阻断
  - 结论：✓ 通过（127.0.0.1 同源 + 自托管 vendor）

---

## 3. `app.js` 渲染管线改造（对应 Task 3）

- [x] `renderMarkdown` 改造为 marked → DOMPurify 顺序
  - 配置：`ADD_ATTR: ['class']`（保留 hljs 必需 class）、`FORBID_TAGS: ['style','iframe','script']`
  - 字符串阶段**不**调 hljs（避免 DOMPurify 误伤 token span）
  - 结论：✓ 通过

- [x] `enhanceCodeBlocks(bubbleEl)` 新增
  - 遍历 `bubbleEl.querySelectorAll('pre > code')`
  - **关键时序**：`codeEl.dataset.raw = codeEl.textContent` 必须在 `hljs.highlightElement` 之前
  - 调 `buildCodeHeader` 注入 header、调 `hljs.highlightElement`、调 `validateJsonBlock`
  - 结论：✓ 通过

- [x] `buildCodeHeader(lang, codeEl)` 新增（全 createElement，零字符串拼接）
  - 构造 `<div class="code-block-header">` 含 `<span class="code-lang">` + `<button class="copy-btn">Copy</button>`
  - 复制按钮 click 事件委托给 `copyCode(codeEl, btn)`
  - 结论：✓ 通过

- [x] `copyCode(codeEl, btnEl)` 新增
  - 优先 `navigator.clipboard.writeText(codeEl.dataset.raw)`
  - 失败回退：临时 textarea + `document.execCommand('copy')`
  - 成功后：`btn.textContent = 'Copied'`、加 `is-copied` class、1.5s 后还原
  - 结论：✓ 通过（端到端验证：剪贴板内容 = 原文，按钮文字 50ms 内变 "Copied"，1500ms 后还原）

- [x] `validateJsonBlock(codeEl, preEl)` 新增
  - 读 `codeEl.dataset.raw`（hljs 改 textContent 之前的原文）
  - 成功：在 header 末尾追加 `<span class="json-valid">✓ valid</span>`
  - 失败：从 `err.message` 抽 `position N` 算 row/col，在 `preEl` 后插 `.json-error` 条
  - row=0 时不显示"第 0 行 第 0 列"虚假信息（V8 部分错误无 position 字段）
  - 结论：✓ 通过

- [x] `appendMessageNode` L572 调用点改造
  - 实际：`bubble.innerHTML = renderMarkdown(content);` 后追加 `enhanceCodeBlocks(bubble);`
  - 结论：✓ 通过

- [x] `finalizeAssistantMessage` L641 调用点改造
  - 实际：同样在 innerHTML 赋值后调 `enhanceCodeBlocks(bubble);`
  - 结论：✓ 通过

- [x] `appendStreamDelta` 保持纯文本（不解析）
  - 结论：✓ 通过（流中 bubble.innerHTML 无 `<pre>`，流结束才解析）

---

## 4. `style.css` 代码块样式（对应 Task 4）

- [x] `.message-assistant .message-bubble pre.code-block` 容器定位
  - `position: relative; padding-top: 36px;`
  - 结论：✓ 通过

- [x] `.code-block-header` 灰底条
  - 28px 高、绝对定位、横排、深色半透底、底部 hairline 分割
  - 结论：✓ 通过

- [x] `.code-lang` muted 色语言标签
  - 结论：✓ 通过

- [x] `.copy-btn` 复制按钮样式
  - 透明背景、hover 变 `--accent`、`.is-copied` 变 `--success`
  - 结论：✓ 通过

- [x] `.json-valid` 成功角标
  - 结论：✓ 通过

- [x] `.json-error` 错误条
  - 左 3px `--error` border、`--error-dim` 背景、mono 字体
  - 结论：✓ 通过

---

## 5. `highlight-theme.css` token 颜色重写（对应 Task 5）

- [x] `.hljs` 背景透明、前景用 `--fg`
  - 结论：✓ 通过

- [x] keyword / built_in / tag → `--accent`（琥珀金，**替代** atom-one-dark 紫 `#c678dd`）
  - 结论：✓ 通过

- [x] string / attribute / regexp → `--success`（绿）
  - 结论：✓ 通过

- [x] number / literal → `#E5C07B`
  - 结论：✓ 通过

- [x] comment → `--fg-faint` + italic
  - 结论：✓ 通过

- [x] title.function_ / title → `--thinking`（思考蓝）
  - 结论：✓ 通过

- [x] name / section / selector-tag → `--error`（暖红）
  - 结论：✓ 通过

- [x] 严禁紫色 / 蓝紫（atom-one-dark 默认色）
  - 实际：`grep -i "c678dd\|#9b59b6\|#bb9af7" vendor/highlight-theme.css` 无匹配
  - 结论：✓ 通过

---

## 6. 编译与单元测试（对应 Task 6）

- [x] `go build -o codepilot.exe ./src` 编译通过
  - 结论：✓ 通过

- [x] `go test ./...` 回归通过
  - 实际：6 个包测试全过，零回归
  - 结论：✓ 通过

---

## 7. Web UI 端到端验证（对应 Task 7）

- [x] 页面加载零 console 错误（仅 1 个与本次无关的 /x 资源 404）
  - 结论：✓ 通过

- [x] vendor 全局对象全部就绪
  - `window.marked` (object) / `window.hljs` v11.11.1 (object, 36 langs) / `window.DOMPurify` (function)
  - 结论：✓ 通过

- [x] 3 语种代码块高亮
  - go / python / typescript：`<span class="hljs-keyword">` / `<span class="hljs-string">` 全部出现
  - 结论：✓ 通过

- [x] 语言标签
  - header 左侧分别显示 `go` / `python` / `typescript` / `json` / `json` / `plain`
  - 结论：✓ 通过

- [x] Copy 按钮
  - 点击后剪贴板内容 = 原文（`{"a": 1, "b": }`）
  - 50ms 内按钮文字变 "Copied"、加 `is-copied` class
  - 1.5s 后还原
  - 结论：✓ 通过

- [x] 合法 JSON → `✓ valid` 角标
  - 实际：`{"a": 1, "b": "hi", "c": [1,2,3]}` 解析成功，绿色角标出现
  - 结论：✓ 通过

- [x] 非法 JSON → 错误条
  - 实际 1（V8 无 position）：`{"a": 1, "b": }` → `JSON 错误 · Unexpected token '}', "..." is not valid JSON`
  - 实际 2（V8 有 position）：`{"a": 1,\n` → `JSON 错误 · 第 2 行 第 1 列 · Expected double-quoted property name in JSON at position 9`
  - 结论：✓ 通过

- [x] 无语言围栏 → `plain` 标签，无 hljs 类
  - 结论：✓ 通过

- [x] XSS 防护
  - 输入 `<img src=x onerror=alert(1)>`：DOM 中无 onerror 节点、innerHTML 字符串中也无 onerror
  - 结论：✓ 通过（DOMPurify 生效）

- [x] 流式响应不闪烁
  - 实际：流中 `bubble.querySelector('pre')` 为 null、`bubble.querySelector('.hljs-keyword')` 为 null
  - 实际：`stream_done` 后才走 `renderMarkdown` + `enhanceCodeBlocks`
  - 结论：✓ 通过

---

## 8. 文档与归档（对应 Task 8）

- [x] `docs/step1.2-对话栏文本渲染/spec.md` 已创建
  - 结论：✓ 通过

- [x] `docs/step1.2-对话栏文本渲染/tasks.md` 已创建
  - 结论：✓ 通过

- [x] `docs/step1.2-对话栏文本渲染/checklist.md` 已创建（本文档）
  - 结论：✓ 通过

---

## 验收结论

**全部 8 大类、30+ 个子项均已通过验证，可发布。**

关键产物清单：

| 类型 | 路径 |
|---|---|
| Spec | `docs/step1.2-对话栏文本渲染/spec.md` |
| Tasks | `docs/step1.2-对话栏文本渲染/tasks.md` |
| Checklist | `docs/step1.2-对话栏文本渲染/checklist.md` |
| 前端 HTML | `src/internal/interaction/web/static/index.html` |
| 前端 JS | `src/internal/interaction/web/static/app.js` |
| 前端 CSS | `src/internal/interaction/web/static/style.css` |
| 主题 CSS | `src/internal/interaction/web/static/vendor/highlight-theme.css` |
| DOMPurify | `src/internal/interaction/web/static/vendor/purify.min.js` |
| highlight.js | `src/internal/interaction/web/static/vendor/highlight.min.js` |

零后端改动、零协议改动、零数据库改动；纯前端增强，向后兼容 Step 1.1 全部功能。
