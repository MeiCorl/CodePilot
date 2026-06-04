# Step 1.3 — WebUI 流式渲染 · 任务清单

## Task 1: 新增流式渲染核心函数

**状态**：已完成

**目标**：实现流式 Markdown 渲染的核心逻辑，包括防抖机制、未闭合代码块预处理、marked + DOMPurify 渲染管线。

**影响文件**：
- `src/internal/interaction/web/static/app.js` — 修改，在 Markdown 渲染区域新增函数

**依赖**：无

**具体内容**：

1. 新增 `closeOpenFences(text)` 函数：检测文本中未闭合的围栏代码块（`` ``` ``），自动在末尾补上闭合标记。确保 `marked.parse` 对不完整代码块也能生成 `<pre><code>` 容器
   - 检测逻辑：统计 `` ``` `` 出现次数，奇数个则末尾补 `\n``` `
   - 检测时需考虑代码块标记可能跟有语言标识（`` ```go ``），以及可能的缩进围栏（`~~~`）
2. 新增 `streamingRenderMarkdown(text)` 函数：流式渲染专用 Markdown 解析
   - 调用 `closeOpenFences` 预处理文本
   - 调用 `marked.parse` 解析
   - 调用 `DOMPurify.sanitize` 过滤，过滤规则与 `renderMarkdown` 一致（`ADD_ATTR: ['class']`, `FORBID_TAGS: ['style', 'iframe', 'script']`）
   - 返回安全 HTML 字符串
3. 新增防抖渲染机制：
   - 在 `state` 中新增 `_streamingRenderTimer` 用于存储 `setTimeout` ID
   - 防抖间隔设为 80ms（在 50-100ms 范围内，平衡实时性与性能）
   - 新增 `scheduleStreamingRender()` 函数：清空旧 timer，设置新 timer，timer 到期后调用 `executeStreamingRender()`
   - 新增 `executeStreamingRender()` 函数：取 `state._streamingBuffer`，调用 `streamingRenderMarkdown`，更新 bubble 的 `innerHTML`，触发自动滚动
   - 新增 `flushStreamingRender()` 函数：立即清空 timer 并执行渲染，用于关键节点（如 finalize 时确保最后一次渲染完成）

**参考资料**：
- 现有 `renderMarkdown` 函数位于 `app.js` 行 898-912
- 现有 `appendStreamDelta` 函数位于 `app.js` 行 866-880

---

## Task 2: 改造 appendStreamDelta 使用流式渲染

**状态**：已完成

**目标**：将 `appendStreamDelta` 从纯文本追加改为防抖驱动的流式 Markdown 渲染，用户在流式过程中即可看到格式化的内容。

**影响文件**：
- `src/internal/interaction/web/static/app.js` — 修改

**依赖**：Task 1（依赖 `scheduleStreamingRender` 函数）

**具体内容**：

1. 修改 `appendStreamDelta(delta)` 函数：
   - 保留现有的流式消息节点创建逻辑（检查 `state._streamingWrap`，不存在则创建）
   - 保留 `state._streamingBuffer += delta` 缓冲追加
   - **移除** `bubble.textContent = state._streamingBuffer` 这行纯文本赋值
   - **新增**：调用 `scheduleStreamingRender()` 触发防抖渲染
2. 首个 delta 到达时的特殊处理：
   - 首个 delta 时直接调用 `flushStreamingRender()` 立即渲染，不等防抖（让用户尽快看到响应开始）
   - 后续 delta 走防抖逻辑
   - 可通过检测 `state._streamingRenderTimer === null && !state._streamingWrap.querySelector('.message-bubble').innerHTML` 来判断是否是首个 delta（或新增一个 `state._streamingFirstChunk` 标志位）
3. 确保自动滚动逻辑 `scrollToBottomIfNeeded()` 在 `executeStreamingRender` 中调用，而不是在 `appendStreamDelta` 中

**参考资料**：
- `appendStreamDelta` 位于 `app.js` 行 866-880
- `scrollToBottomIfNeeded` 在现有代码中已有实现

---

## Task 3: 改造 finalizeAssistantMessage 适配流式渲染

**状态**：已完成

**目标**：改造流结束后的固化逻辑，复用流式渲染已生成的内容，仅追加最终增强（hljs 高亮、代码块 header、JSON 校验）。

**影响文件**：
- `src/internal/interaction/web/static/app.js` — 修改

**依赖**：Task 2（`appendStreamDelta` 已改造完毕）

**具体内容**：

1. 修改 `finalizeAssistantMessage()` 函数：
   - 在函数开头调用 `flushStreamingRender()` 确保最后一次渲染完成（清空防抖 timer 并立即渲染）
   - **替换** `bubble.innerHTML = renderMarkdown(text)` 为：直接对当前 bubble 内容执行 `enhanceCodeBlocks(bubble)`
   - 因为流式过程中已经通过 `streamingRenderMarkdown` 完成了 marked + DOMPurify 渲染，不需要再用 `renderMarkdown` 重新渲染
   - 但需要兜底：如果 `state._streamingWrap` 存在但 bubble 内容为空（极端情况），仍然走 `renderMarkdown` 渲染
2. 保持以下逻辑不变：
   - `state.messages.push({ role: 'assistant', content: text })` 消息固化
   - `state._streamingWrap = null` 和 `state._streamingBuffer = ''` 状态清理
   - `scrollToBottomIfNeeded()` 最终滚动
3. 新增状态清理：将 `state._streamingRenderTimer` 置为 `null`

**参考资料**：
- `finalizeAssistantMessage` 位于 `app.js` 行 882-893
- `enhanceCodeBlocks` 位于 `app.js` 行 1022-1049

---

## Task 4: 流式渲染样式优化

**状态**：已完成

**目标**：为流式渲染过程增加视觉优化，确保代码块在流式过程中有基本的样式表现，内容过渡平滑。

**影响文件**：
- `src/internal/interaction/web/static/style.css` — 修改，新增流式渲染相关样式
- `src/internal/interaction/web/static/app.js` — 修改（可选），为流式中的代码块添加标识 class

**依赖**：Task 3（流式渲染逻辑已就绪）

**具体内容**：

1. 流式过程中代码块的基础样式：
   - 流式中 marked 已经生成了 `<pre><code>` 结构，现有 `.code-block` 样式会生效
   - 但 `enhanceCodeBlocks` 中的 header（语言标签 + 复制按钮）尚未添加，需要确保代码块在无 header 时也有合理的圆角和间距
   - 检查现有 `.code-block` 样式是否依赖 `buildCodeHeader` 生成的 DOM 结构，如有则补充无 header 时的兜底样式
2. 内容更新的过渡效果（可选）：
   - 为 `.message-bubble` 添加 `overflow: hidden` 防止重渲染时的闪烁
   - 不建议添加 `transition` 动画（会导致每次内容更新都有延迟感）
3. 确认流式渲染过程中的字体、颜色、间距与 `stream_done` 后的最终渲染结果视觉一致

**参考资料**：
- 现有代码块样式在 `style.css` 中
- `buildCodeHeader` 在 `app.js` 行 960-998

---

## Task 5: 接入主流程与边界场景处理

**状态**：已完成

**目标**：确保新的流式渲染管线与 Agent Loop、工具调用展示、中断恢复等现有流程完全兼容。

**影响文件**：
- `src/internal/interaction/web/static/app.js` — 修改，处理边界场景

**依赖**：Task 4（样式已优化）

**具体内容**：

1. 工具调用兼容：
   - `tool_call_start` 和 `tool_call_end` 事件在 Agent Loop 中穿插于文本 delta 之间
   - 确认工具调用的 DOM 节点（`.tool-card`）不会因为流式渲染的 `innerHTML` 更新被覆盖
   - 工具调用节点在当前设计中是独立的 `appendMessageNode`，与助手消息节点分离，理论上不受影响，但需实际验证
2. 中断（abort）场景处理：
   - 用户点击中断按钮后，`onStreamError` 会被触发
   - 确认 `onStreamError` 中的 `hideThinking()` 和错误卡片渲染不会导致流式渲染状态泄漏
   - 在 `onStreamError` 中增加 `flushStreamingRender()` 调用，确保已接收内容被正确渲染
   - 中断后应调用 `finalizeAssistantMessage` 将已接收内容固化为完整消息（当前代码是否已如此？需检查）
3. Agent Loop 多轮迭代场景：
   - 每轮迭代的 LLM 回复都会通过 `onStreamChunk` 推送 delta
   - 确认多轮迭代间 `state._streamingWrap` / `_streamingBuffer` / `_streamingRenderTimer` 的状态正确重置
   - 验证首轮迭代完成、工具执行、次轮迭代开始的完整流程中，流式渲染不产生残留节点
4. 空响应兜底：
   - 如果 LLM 没有文本输出（仅有工具调用），`state._streamingWrap` 可能为 `null`，`finalizeAssistantMessage` 应安全跳过

**参考资料**：
- `onStreamError` 位于 `app.js` 行 251-258
- `onStreamDone` 位于 `app.js` 行 241-249
- 工具调用渲染相关函数在 `app.js` 中搜索 `tool_call` 或 `tool-card`

---

## Task 6: 端到端验证

**状态**：已完成

**目标**：全面验证流式渲染在新消息、历史消息、工具调用、中断恢复等场景下的表现，确保无回归。

**影响文件**：
- 无新增/修改，仅验证

**依赖**：Task 5（所有边界场景已处理）

**具体内容**：

1. 按 `checklist.md` 逐项验证
2. 重点关注以下场景：
   - 短回复（纯文本段落）
   - 包含代码块的回复（Go / JS / Python 等多语言）
   - 包含表格的回复
   - 长回复（2000+ token）
   - Agent Loop 多轮迭代（LLM → 工具 → LLM → 完成）
   - 用户中断后恢复
   - 历史会话加载后消息显示
3. 验证完成后更新 `checklist.md` 各项结果

**参考资料**：
- `checklist.md` 在本文档同级目录
