# Step 1.2 — 对话栏代码 / JSON / Markdown 渲染增强

## 背景

Step 1.1 已完成 Web 化改造，助手消息使用 `marked.js` 渲染 Markdown。但当前对话栏（Chat Panel）针对**结构化富文本**呈现仍存在以下短板：

1. **代码块无语法高亮**：LLM 返回的 ``` 围栏代码（Go、Python、TypeScript、JSON、SQL 等）只是一段等宽字体灰底文本，30+ 常见语言无差别，可读性差。
2. **没有交互能力**：无法快速复制代码片段；无法一眼看出当前代码块属于哪种语言；JSON 块不会自动校验格式，写错的 JSON 与写对的 JSON 视觉无差异。
3. **存在 XSS 风险**：marked v15.0.12 已移除内置 sanitizer（v5+ 已删），`app.js` 中 `bubble.innerHTML = marked.parse(...)` 是直接注入，LLM 偶尔会输出 `<img src=x onerror=...>`、`<a href="javascript:...">` 等，浏览器会执行。
4. **设计 token 缺少结构化文本反馈色**：复制成功、JSON 校验通过、JSON 错误等状态在视觉上没有专门承接。

本次优化的目标是：**复用现有 marked.js 管线**，叠加 highlight.js 语法高亮、代码块交互（语言标签 + 复制按钮）、JSON 智能校验、DOMPurify XSS 防护，并把 token 颜色对齐到设计系统。流式响应期间保持纯文本展示，`stream_done` 后一次性高亮，避免半截代码闪烁。

## 目标用户

在浏览器中与 CodePilot 对话的开发者，主要场景：

- 让 CodePilot 写一段示例代码（任何语言），期望在对话栏中**直接看到带高亮的代码**，并**一键复制**
- 让 CodePilot 输出 JSON / YAML / SQL，期望**视觉上易读**，且 JSON 块**自动校验**格式
- 让 CodePilot 输出复杂 Markdown（标题、列表、表格、引用），期望样式统一、对比清晰
- LLM 偶发输出恶意 / 异常 HTML，期望**自动过滤**而不是执行

## 能力清单

1. **代码块语法高亮**：所有 ``` 围栏代码块（lang 或无 lang）调用 highlight.js v11.11.1 自动高亮。支持的常见语言至少包括：go、javascript、typescript、python、json、bash、sql、yaml、html、css、java、c、cpp、csharp、rust、ruby、php、markdown 等。无语言时不强行高亮，header 显示 `plain`
2. **代码块复制按钮**：每个代码块右上角（实际为顶部 header 右侧）放一个 `Copy` 按钮。点击后复制原文到剪贴板，按钮短暂变 `Copied`（1.5s 后还原）。剪贴板 API 不可用时回退到 `execCommand('copy')`
3. **语言标签**：代码块顶部 header 左侧显示当前语言（来自 ``` 后的 lang），无语言时显示 `plain`，字母小写
4. **JSON 智能校验**：仅 ` ```json` 块启用。合法时 header 右侧追加 `✓ valid` 角标（绿色）；不合法时在代码块下方追加红色错误条，文字包含「第 X 行 第 Y 列」与 `JSON.parse` 错误信息
5. **XSS 防护**：所有 `marked.parse()` 输出在 `innerHTML` 赋值前经 DOMPurify v3.2.4 sanitize，剔除 `<script>`、`<iframe>`、`<style>`、`on*` 事件属性等危险元素；同时保留 `class` 属性（hljs token 必需）
6. **流式响应保持纯文本**：流中 chunk 到达时仍只设 `bubble.textContent`（与现状一致），不在残缺 markdown 上做语法分析。`stream_done` 后一次性 marked → DOMPurify → enhanceCodeBlocks
7. **高亮 token 颜色对齐设计 token**：琥珀金强调 keyword、思考蓝函数名、绿色字符串、红色错误、暖白默认。**严禁**使用紫 / 蓝紫（atom-one-dark 默认色 `#c678dd`）

## 非功能要求

1. **性能**：`enhanceCodeBlocks` 必须在 `stream_done` 后单次调用；单个代码块 < 50KB 时 highlightElement < 50ms；总 vendor 增加 < 700KB（gzip 后约 220KB），`defer` 加载不阻塞首屏
2. **安全性**：仅监听 127.0.0.1 的服务，所有 vendor 资源同源加载（`/vendor/...`），不引入任何 CDN 外链；不引入 CSP meta（保持简洁）
3. **可维护性**：高亮、XSS 防护、复制、JSON 校验等逻辑全部以独立函数封装（`enhanceCodeBlocks` / `validateJsonBlock` / `copyCode` / `buildCodeHeader`），不污染现有 markdown 渲染主流程
4. **可扩展性**：未来如需新增语言支持（Rust / Kotlin / Swift 等），highlight.js common bundle 已内置，无需任何额外配置
5. **健壮性**：LLM 输出的 ``` 围栏可能未闭合或嵌套异常，marked v15 默认容错输出原文（不强抛）；highlight.js 识别不出语言时**不**强行加 hljs 类（fallback 到 plain）
6. **零构建步骤**：保留 Step 1.1 既定约束，HTML/CSS/JS 仍以源码形式 embed 到 Go 二进制，新增 vendor 文件后重新 `go build` 即可

## 设计骨架

### 文件结构

```
src/internal/interaction/web/static/
├── index.html                       # 新增 1 link + 1 script 引用
├── app.js                           # renderMarkdown 改造 + 4 个新函数 + 2 个调用点
├── style.css                        # 末尾追加代码块 header / 复制按钮 / JSON 错误样式
└── vendor/
    ├── marked.min.js                # 已有 v15.0.12
    ├── purify.min.js                # 新增 DOMPurify v3.2.4 standalone
    ├── highlight.min.js             # 新增 highlight.js v11.11.1 common bundle
    └── highlight-theme.css          # 新增 暗色主题（atom-one-dark 重命名 + token 重写）
```

### 前端代码块视觉

```
┌────────────────────────────────────────────────────────────┐
│  go                                                   Copy │  ← 28px 灰底 header
├────────────────────────────────────────────────────────────┤
│  package main                                              │
│                                                            │  ← 高亮区（pre.code-block）
│  import "net/http"                                         │     keyword 琥珀金
│                                                            │     string  绿
│  func main() {                                             │     func    思考蓝
│      http.HandleFunc("/", handler)                         │
│  }                                                         │
└────────────────────────────────────────────────────────────┘
```

### JSON 块错误状态

```
┌────────────────────────────────────────────────────────────┐
│  json                                         Copy         │
├────────────────────────────────────────────────────────────┤
│  {                                                         │
│    "a": 1,                                                 │
│    "b":                                                    │  ← 高亮仍保留（用户能看到原文）
│  }                                                         │
└────────────────────────────────────────────────────────────┘
JSON 错误 · 第 4 行 第 7 列 · Unexpected token } in JSON at position 22
```

### 渲染调用链

```
LLM stream_chunk (delta) ──► appendStreamDelta (L621)
                              └─ bubble.textContent = buffer        ← 流中纯文本

LLM stream_done ────────────► finalizeAssistantMessage (L637)
                              ├─ bubble.innerHTML = renderMarkdown(text)  ← marked → DOMPurify
                              └─ enhanceCodeBlocks(bubble)                ← hljs + 复制 + JSON 校验
                                  ├─ for each pre > code:
                                  │   ├─ codeEl.dataset.raw = textContent
                                  │   ├─ preEl.appendChild(buildCodeHeader)
                                  │   ├─ hljs.highlightElement(codeEl)  // 非 plain
                                  │   └─ validateJsonBlock(codeEl, preEl) // 仅 json
                                  └─ buildCodeHeader 返回 { lang label, copy btn }

会话切换/恢复 ────────────► renderAllMessages (L515)
                              └─ for each message:
                                  └─ appendMessageNode(role, content, false)
                                      └─ bubble.innerHTML = renderMarkdown(content)
                                          └─ enhanceCodeBlocks(bubble)
```

## Out of Scope（本步骤不做）

1. 代码块**行号**显示（体验增强，可后续）
2. 代码块**折叠 / 展开**（长代码优化）
3. 代码块**差异对比**（diff 渲染）
4. 自定义语言注册（仅用 highlight.js 内置）
5. highlight.js 主题切换（仅暗色，与 design token 对齐）
6. 流式中**增量高亮**（chunk 粒度不可控，体验反而更差）
7. 代码块内容**搜索 / 跳转**（编辑器能力，非对话栏职责）
8. 移动端代码块横向滚动优化（仅保证基本可用）
9. 会话导出（与代码块相关的 Markdown 下载，Step 9 之后）
10. 工具调用（Step 2）+ Agent Loop（Step 3）+ SubAgent（Step 12）

## Todo List（后续待实现）

1. 代码块行号
2. 长代码块折叠
3. highlight.js 主题切换（浅色 / 暗色）
4. 流式中"上一次成功的"代码块保留高亮
