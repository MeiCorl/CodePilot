# Step 1.3 — WebUI 流式渲染

## 背景

在 Step 1.2 中，我们为 WebUI 对话栏实现了 Markdown 富文本渲染能力（语法高亮、代码块增强、XSS 防护等），但渲染时机是在 `stream_done` 后一次性完成。流式响应过程中，用户看到的是未经格式化的 Markdown 原始标记（`#`、`**`、`` ``` `` 等），直到全部内容接收完毕才"瞬间"切换为渲染后的富文本，视觉体验割裂且粗糙。

主流 AI 助手（ChatGPT、Claude.ai 等）都支持流式 Markdown 渲染——LLM 的每个 delta 到达时，用户立即看到格式化的内容逐字展开，包括标题、列表、加粗、代码块等全部 Markdown 元素。本步骤的目标就是将 CodePilot 的 WebUI 对齐到这一体验标准。

## 目标用户

CodePilot 终端用户——在浏览器 WebUI 中与 Agent 交互的开发者。

## 能力清单

1. **流式 Markdown 实时渲染**：流式响应过程中，LLM 输出的每个 delta 到达后，已累积的文本立即通过 `marked` 解析并渲染为格式化的 HTML，用户实时看到标题、列表、加粗、链接、图片、表格等元素的格式化效果
2. **代码块实时容器创建**：一旦检测到代码块开始标记（` ```lang `），立即创建代码块容器（含语言标签、等宽字体），代码内容逐行追加到容器内，而非等待闭合标记
3. **防抖合并渲染**：高频 delta 到达时不逐个触发重渲染，而是通过防抖机制（约 50-100ms）合并多个 delta 后统一渲染，确保长文本场景下不卡顿
4. **全量重解析策略**：每次渲染时对累积的完整文本重新调用 `marked.parse`，保证 Markdown 解析的正确性（尤其是列表嵌套、表格、跨行标记等复杂结构）
5. **流结束后的最终增强**：`stream_done` 后对完整内容执行一次性增强——hljs 语法高亮、代码块 header（复制按钮）、JSON 校验等，确保最终渲染质量与 Step 1.2 一致
6. **DOMPurify XSS 防护持续有效**：流式渲染过程中每次 `innerHTML` 更新都经过 DOMPurify 过滤，安全防护不降级
7. **自动滚动跟随**：流式渲染过程中保持自动滚动到底部，用户手动上滑时暂停自动滚动
8. **历史消息兼容**：历史消息加载（`renderAllMessages`）保持现有的一次性渲染管线不变，仅流式过程使用新渲染管线

## 非功能要求

1. **性能**：长文本（2000+ token）场景下，流式渲染不应导致明显的 UI 卡顿或掉帧；防抖间隔在 50-100ms 之间，用户感知不到延迟
2. **正确性**：流式渲染的最终结果必须与一次性渲染（`stream_done` 后 `renderMarkdown` + `enhanceCodeBlocks`）完全一致
3. **安全**：流式过程中 DOMPurify 过滤规则与现有 `renderMarkdown` 一致，不允许 XSS 攻击面扩大
4. **兼容性**：不修改后端 WebSocket 协议和消息类型，仅修改前端渲染逻辑；不影响工具调用（`tool_call_start` / `tool_call_end`）的展示逻辑
5. **可维护性**：新增的流式渲染逻辑封装为独立函数，不与现有 `renderMarkdown` / `enhanceCodeBlocks` 耦合

## 设计骨架

```
src/internal/interaction/web/static/
├── app.js              — 修改：新增流式渲染函数，改造 appendStreamDelta / finalizeAssistantMessage
├── style.css           — 修改（可选）：新增流式渲染相关的过渡动画样式
└── vendor/             — 不变
```

核心变更集中在 `app.js` 中，涉及以下函数的改造和新函数的引入：

- **改造** `appendStreamDelta`：从 `textContent` 追加改为防抖 + 全量重解析 + `innerHTML` 更新
- **改造** `finalizeAssistantMessage`：保留最终的 `enhanceCodeBlocks` 增强，去除冗余的 `renderMarkdown`（流式中已渲染）
- **新增** `streamingRender`：流式渲染核心函数，封装防抖 + marked + DOMPurify 逻辑
- **新增** `flushStreamingRender`：立即触发渲染（用于防抖结束时或关键节点）

## Out of Scope（本步骤不做）

1. **后端协议变更**：不修改 WebSocket 消息类型、Payload 结构或推送频率
2. **工具调用展示优化**：`tool_call_start` / `tool_call_end` 的展示逻辑保持不变
3. **历史消息渲染管线变更**：`renderAllMessages` 中的渲染逻辑保持不变
4. **代码块行号显示**：本步骤不新增行号功能（可后续迭代）
5. **LaTeX 数学公式渲染**：不在本步骤范围内
6. **diff 高亮**：代码差异对比高亮不在本步骤范围内
7. **流式渲染过程中的光标动画**：如打字机光标效果，可在后续迭代中加入
