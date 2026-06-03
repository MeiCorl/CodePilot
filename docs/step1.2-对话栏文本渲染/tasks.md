# Step 1.2 Tasks — 对话栏代码 / JSON / Markdown 渲染增强

---

## Task 1: 引入 vendor 资源（DOMPurify + highlight.js + 暗色主题）

**状态**：已完成

**目标**：在 `static/vendor/` 下新增 3 个文件，并确认 `//go:embed` 可正常打包。

**影响文件**：
- `src/internal/interaction/web/static/vendor/purify.min.js` — 新增
- `src/internal/interaction/web/static/vendor/highlight.min.js` — 新增
- `src/internal/interaction/web/static/vendor/highlight-theme.css` — 新增

**依赖**：无

**具体内容**：
1. 下载 DOMPurify v3.2.4 standalone：
   ```
   curl -L https://cdn.jsdelivr.net/npm/dompurify@3.2.4/dist/purify.min.js \
     -o src/internal/interaction/web/static/vendor/purify.min.js
   ```
2. 下载 highlight.js v11.11.1 common bundle：
   ```
   curl -L https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11.11.1/build/highlight.min.js \
     -o src/internal/interaction/web/static/vendor/highlight.min.js
   ```
3. 下载 atom-one-dark 主题 CSS 并重命名：
   ```
   curl -L https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11.11.1/build/styles/atom-one-dark.min.css \
     -o src/internal/interaction/web/static/vendor/highlight-theme.css
   ```
4. 验证 `//go:embed static` 自动覆盖（无需改 server.go / router.go）
5. 校验文件大小：DOMPurify ~22KB、highlight.js ~620KB、theme ~1.5KB

**参考资料**：
- DOMPurify 官方：https://github.com/cure53/DOMPurify
- highlight.js 官方：https://highlightjs.org/

---

## Task 2: 修改 `index.html` 引入新依赖

**状态**：已完成

**目标**：在 `<head>` 引入 highlight 主题 CSS，在 `</body>` 前按依赖顺序引入 vendor JS。

**影响文件**：
- `src/internal/interaction/web/static/index.html`

**依赖**：Task 1

**具体内容**：
1. 在 `<link rel="stylesheet" href="/style.css">` 之后追加：
   ```html
   <link rel="stylesheet" href="/vendor/highlight-theme.css">
   ```
2. 在 `</body>` 前的 `<script src="/vendor/marked.min.js" defer></script>` 之前/之后调整顺序（defer 仍按出现顺序执行）：
   ```html
   <script src="/vendor/purify.min.js" defer></script>
   <script src="/vendor/marked.min.js" defer></script>
   <script src="/vendor/highlight.min.js" defer></script>
   <script src="/app.js" defer></script>
   ```
3. 加载顺序：DOMPurify → marked → highlight.js → app.js（app.js 使用前 3 个的全局对象）

**参考资料**：HTML `<script defer>` 顺序保证

---

## Task 3: 改造 `app.js` 渲染管线

**状态**：已完成

**目标**：改造 `renderMarkdown` 走 marked → DOMPurify 顺序；新增 4 个代码块处理函数；在两个调用点串起来。

**影响文件**：
- `src/internal/interaction/web/static/app.js`

**依赖**：Task 2

**具体内容**：
1. **改造 `renderMarkdown`（L651-660）**：
   - 在 `window.marked.parse(text)` 后调 `window.DOMPurify.sanitize(raw, { ADD_ATTR: ['class'], FORBID_TAGS: ['style', 'iframe', 'script'] })`
   - 不在字符串阶段调 hljs
2. **新增 `enhanceCodeBlocks(bubbleEl)`**：
   - 遍历 `bubbleEl.querySelectorAll('pre > code')`
   - 从 `class` 抽 `language-xxx`，无则 lang = `'plain'`
   - **关键**：`codeEl.dataset.raw = codeEl.textContent` 必须在 `hljs.highlightElement` 之前
   - 给 `pre` 加 `code-block` class
   - 调 `buildCodeHeader(lang, codeEl)` 插入 header
   - lang 非 plain 时调 `window.hljs.highlightElement(codeEl)`
   - lang === 'json' 时调 `validateJsonBlock(codeEl, preEl)`
3. **新增 `buildCodeHeader(lang, codeEl)`**（全 createElement，零字符串拼接）：
   - 返回 `<div class="code-block-header">`，含 `<span class="code-lang">` + `<button class="copy-btn">Copy</button>`
   - click 事件委托给 `copyCode(codeEl, btn)`
4. **新增 `copyCode(codeEl, btnEl)`**（async）：
   - 优先 `navigator.clipboard.writeText(codeEl.dataset.raw)`
   - 失败回退：临时 `<textarea>` + `document.execCommand('copy')`
   - 成功后 `btn.textContent = 'Copied'`，加 `is-copied` class，1.5s 还原
5. **新增 `validateJsonBlock(codeEl, preEl)`**：
   - 读 `codeEl.dataset.raw`（hljs 改 textContent 之前的原文）
   - `JSON.parse` 成功：在 `preEl.querySelector('.code-block-header')` 末尾追加 `<span class="json-valid">✓ valid</span>`
   - 失败：从 `err.message` 抽 `position N`，算 row/col，在 `preEl` 后插一个 `.json-error` 条，**不替换**高亮
6. **修改调用点**：
   - `appendMessageNode` L572：`bubble.innerHTML = renderMarkdown(content);` 后追加 `enhanceCodeBlocks(bubble);`
   - `finalizeAssistantMessage` L641：同样在 innerHTML 赋值后调 `enhanceCodeBlocks(bubble);`
   - `appendStreamDelta` L633：保持 `bubble.textContent = state._streamingBuffer;`，**不解析**

**关键约束**：
- `codeEl.dataset.raw` 必须在 `hljs.highlightElement` 之前赋值（hljs 改 textContent 后原文丢失）
- JSON 校验角的"成功"和"失败"是互斥分支：成功 → header 角标；失败 → preEl 后的 .json-error 条
- 复制按钮 / 语言标签全部用 createElement，**不要**用 innerHTML 字符串拼接（避免二次 XSS 风险）

**参考资料**：
- DOMPurify API：`DOMPurify.sanitize(dirty, options)`
- highlight.js API：`hljs.highlightElement(element)` 自动读 class 中的 `language-xxx`
- MDN：`navigator.clipboard.writeText` / `document.execCommand('copy')`

---

## Task 4: 修改 `style.css` 增加代码块样式

**状态**：已完成

**目标**：在 L656 之后追加代码块 header、复制按钮、JSON 错误条样式。

**影响文件**：
- `src/internal/interaction/web/static/style.css`

**依赖**：Task 3

**具体内容**：
1. 追加 `.message-assistant .message-bubble pre.code-block { position: relative; padding-top: 36px; }`
2. 追加 `.code-block-header`（绝对定位、灰底、flex 横排、hairline 分割）
3. 追加 `.code-lang`（muted 色、小写、letter-spacing 0.05em）
4. 追加 `.copy-btn`（透明按钮、hover 变 accent、`.is-copied` 变 success）
5. 追加 `.json-valid`（success 色、紧贴 copy 按钮）
6. 追加 `.json-error`（error 色块、左 border、紧贴 pre 下方）

**关键约束**：
- 所有色值用 design token（`--fg-muted` / `--accent` / `--success` / `--error` / `--bg-hover`），**不**引入新色
- 不使用 emoji

**参考资料**：现有 design token（`style.css:7-74`）

---

## Task 5: 改写 `highlight-theme.css` 对齐 design token

**状态**：已完成

**目标**：将 atom-one-dark 的 `body.hljs` / `.hljs` 背景改为透明，并将所有 token 颜色重写为琥珀金主题色。

**影响文件**：
- `src/internal/interaction/web/static/vendor/highlight-theme.css`

**依赖**：Task 1

**具体内容**：
1. 覆盖 `:root { --hljs-bg: transparent; }`
2. 重写 `.hljs { background: var(--hljs-bg) !important; color: var(--fg); }`
3. 重写 token：
   - `.hljs-keyword, .hljs-built_in, .hljs-tag` → `var(--accent)`（琥珀金）
   - `.hljs-string, .hljs-attr` → `var(--success)`（绿）
   - `.hljs-number, .hljs-literal` → `#E5C07B`
   - `.hljs-comment` → `var(--fg-faint)`，italic
   - `.hljs-title.function_, .hljs-title` → `var(--thinking)`（思考蓝）
   - `.hljs-variable, .hljs-name` → `var(--fg)`
   - `.hljs-symbol, .hljs-operator` → `var(--accent-bright)`

**关键约束**：
- 严禁保留 atom-one-dark 默认的 `#c678dd`（紫）；如有该色，全部替换
- 严禁引入亮色背景；`<pre>` 背景由 `.code-block` 样式提供

**参考资料**：design token（`style.css:7-74`）

---

## Task 6: 编译验证

**状态**：已完成

**目标**：通过 `go build` 确认 `//go:embed static` 把新 vendor 文件正确打包，无编译错误。

**影响文件**：无（仅编译）

**依赖**：Task 1-5

**具体内容**：
1. `go build -o codepilot.exe ./src` 确认无编译错误
2. `go test ./...` 跑现有 web 包 4 个测试文件，确认零回归

---

## Task 7: Web UI 端到端验证

**状态**：已完成

**目标**：通过浏览器 MCP 跑 codepilot.exe，验证所有验收用例。

**影响文件**：无

**依赖**：Task 6

**具体内容**：
1. 启动 `codepilot.exe`，从 stdout 取真实端口
2. playwright MCP 打开 URL，确认无 JS 报错（`browser_console_messages level=error`）
3. 逐项验证：
   - 3 语种代码块（go / python / typescript）→ 高亮 span 出现、header 显示语言名
   - Copy 按钮 → 点击后变 `Copied`，1.5s 后还原；`navigator.clipboard.readText()` 与原文一致
   - 合法 JSON → `✓ valid` 角标出现
   - 非法 JSON → `.json-error` 条出现，含「第 X 行 第 Y 列」+ parse 错误信息
   - 无语言围栏 → header 显示 `plain`，无 hljs 类
   - XSS 注入 → DOM 中无 `<img onerror>` 节点
   - 流式不闪烁 → 流中 `bubble.innerHTML.length === 0`，`stream_done` 后才非空

**参考资料**：playwright MCP 工具集

---

## Task 8: 写 checklist.md 与落档

**状态**：已完成

**目标**：完成实施后，按 HARNESS 规范把验收清单写到 `docs/step1.2-对话栏文本渲染/checklist.md`，与 step1 / step1.1 一致。

**影响文件**：
- `docs/step1.2-对话栏文本渲染/checklist.md` — 新建

**依赖**：Task 7

**具体内容**：
1. 按功能点 / 验收点逐条列 checklist
2. 每条标注 ✓ 实施完成 / ⏳ 待验证
3. 与 `docs/step1.1-UI界面重构/checklist.md` 风格保持一致
