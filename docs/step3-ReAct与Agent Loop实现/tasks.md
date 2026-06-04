# Step 3 — Tasks

> 本文档列出实现 Agent Loop 的所有任务，按依赖顺序排列。
> 每个任务可在一次专注会话内完成。

---

## Task 1: LLM 层 — StreamChunk 支持多个 ToolUseBlock

**状态**：已完成

**目标**：将 `StreamChunk` 从只支持单个 `ToolUse` 扩展为支持多个 `ToolUses` 切片，并更新两个 Provider 的 `doStream` 方法使其正确传递所有累积的 tool_use 块。

**影响文件**：
- `src/llm/types.go` — 修改 `StreamChunk` 结构体
- `src/llm/anthropic.go` — 修改 `doStream` 中 tool_use 累积和发送逻辑
- `src/llm/openai.go` — 修改 `doStream` 中 tool_use 累积和发送逻辑
- `src/internal/engine/conversation/manager.go` — 修改 `runOneLLM` 以适配新的多 ToolUse 字段

**依赖**：无

**具体内容**：

1. **修改 `StreamChunk` 结构体**（`src/llm/types.go`）：
   - 新增字段 `ToolUses []ToolUseBlock`（切片类型）
   - 保留旧字段 `ToolUse *ToolUseBlock` 但标记为 deprecated，临时兼容（后续 Task 清理）
   - 在 `StreamChunk` 上添加辅助方法 `HasToolUse() bool` 和 `AllToolUses() []ToolUseBlock`，兼容新旧字段

2. **修改 Anthropic Provider**（`src/llm/anthropic.go`）：
   - `doStream` 中 `pendingToolUses` 已按 index 累积，当前只取最后一个赋给 `lastToolUse`（第 256 行）
   - 修改为：遍历 `pendingToolUses` map，按 index 升序排列后构造 `[]ToolUseBlock` 切片
   - 最终 Done chunk 同时设置 `ToolUse`（取第一个，兼容旧逻辑）和 `ToolUses`（全部）

3. **修改 OpenAI Provider**（`src/llm/openai.go`）：
   - `doStream` 中 `pending` map 已按 index 累积，当前只取最小 index 的一个（第 303-322 行）
   - 修改为：遍历 `pending` map，按 index 升序排列后构造 `[]ToolUseBlock` 切片
   - 最终 Done chunk 同时设置 `ToolUse` 和 `ToolUses`

4. **修改 `runOneLLM`**（`src/internal/engine/conversation/manager.go`）：
   - 将内部 `pendingToolUse *llm.ToolUseBlock` 改为 `pendingToolUses []llm.ToolUseBlock`
   - chunk 处理时追加到切片而非覆盖
   - `RunOneTurnResult` 的 `ToolUse` 字段保留兼容，`ToolUses` 新增为切片
   - 添加辅助方法 `HasToolUse() bool` 和 `AllToolUses() []ToolUseBlock`

5. **向后兼容**：确保所有现有调用方（`RunTurn` 中检查 `firstTurn.ToolUse == nil`）仍然正常工作

**参考资料**：
- Anthropic `doStream` 中 `pendingToolUses` map 累积逻辑：`src/llm/anthropic.go:211-258`
- OpenAI `doStream` 中 `pending` map 累积逻辑：`src/llm/openai.go:248-322`
- `RunOneTurnResult` 结构体定义：`src/internal/engine/conversation/manager.go:313-322`

---

## Task 2: 工具层 — ToolHandler 批量执行与并行/串行策略

**状态**：已完成

**目标**：为 `ToolHandler` 新增 `ExecuteBatch` 方法，接收多个 `ToolUseBlock`，根据工具权限分类后按策略执行（只读并行、写入/执行串行），全部完成后返回对应的 `ToolResultBlock` 切片。

**影响文件**：
- `src/internal/engine/conversation/tool_handler.go` — 新增 `ExecuteBatch` 方法
- `src/internal/engine/conversation/tool_handler_test.go` — 新增测试（如有）

**依赖**：无

**具体内容**：

1. **新增 `ExecuteBatch` 方法**（`tool_handler.go`）：
   - 方法签名：`ExecuteBatch(ctx context.Context, toolUses []llm.ToolUseBlock) []llm.ToolResultBlock`
   - 执行策略：
     a. 将 `toolUses` 按对应工具的 `Permission()` 分为两组：`readOnly`（PermRead）和 `writeExec`（PermWrite / PermExec）
     b. 查 Registry 获取每个 tool_use 对应的工具实例，未找到的标记为 `IsError=true` 的 ToolResult
     c. **并行组**：使用 `sync.WaitGroup` 或 `errgroup` 并行执行所有只读工具
     d. **串行组**：按原始顺序逐个执行写入/执行工具
     e. 合并两组结果，按原始 tool_use 顺序排列返回
   - 每个工具执行仍复用现有 `Execute` 单工具方法（含 OnStart/OnEnd 回调、超时、panic 恢复）

2. **并行执行细节**：
   - 使用 `sync.WaitGroup` + goroutine 并行执行只读工具
   - 每个并行工具共享同一个 `ctx`，用户中断时全部取消
   - 并行工具的结果收集到切片中，通过索引或 ToolUseID 关联回原始顺序

3. **错误隔离**：
   - 单个工具失败不影响同批次其他工具执行
   - 失败工具的结果标记 `IsError=true`，内容为错误描述

**参考资料**：
- 现有 `Execute` 方法：`src/internal/engine/conversation/tool_handler.go:134-165`
- `Tool.Permission()` 枚举定义：`src/tool/tool.go:13-36`

---

## Task 3: 配置层 — 新增 Agent Loop 配置项

**状态**：已完成

**目标**：在 `Config` 结构体中新增 Agent Loop 相关配置字段，提供合理的默认值和校验。

**影响文件**：
- `src/internal/config/config.go` — 新增配置字段、默认值和校验

**依赖**：无

**具体内容**：

1. **新增配置字段**（`Config` 结构体）：
   - `MaxAgentLoopIterations int`：最大迭代次数，默认 25，`json:"max_agent_loop_iterations,omitempty"`
   - `ContextSafetyMargin int`：上下文安全余量（token 数），当剩余 token 低于此值时触发优雅终止，默认 4096，`json:"context_safety_margin,omitempty"`

2. **默认值填充**（`setDefaults` 方法）：
   - `MaxAgentLoopIterations` 默认 25
   - `ContextSafetyMargin` 默认 4096

3. **校验**（`validate` 方法）：
   - `MaxAgentLoopIterations` 必须 > 0（若非零值时）
   - `ContextSafetyMargin` 必须 >= 0

**参考资料**：
- `Config` 结构体：`src/internal/config/config.go:14-35`
- `setDefaults` 方法：`src/internal/config/config.go:98-108`

---

## Task 4: 核心引擎 — RunTurn 重构为 AgentLoop

**状态**：已完成

**目标**：将 `RunTurn`（两轮 LLM 硬编码）重构为 `AgentLoop`（可循环迭代的 ReAct 引擎），实现迭代计数、上下文溢出检查、优雅中断、工具错误反馈、迭代上限保护等完整逻辑。

**影响文件**：
- `src/internal/engine/conversation/agent_loop.go` — **新建**，AgentLoop 核心逻辑
- `src/internal/engine/conversation/manager.go` — 修改，`RunTurn` 委托给 AgentLoop，保留旧接口作为包装

**依赖**：Task 1（多 ToolUse 支持）、Task 2（批量执行）、Task 3（配置）

**具体内容**：

1. **新建 `agent_loop.go`**：
   - 定义 `AgentLoopConfig` 结构体（MaxIterations、ContextSafetyMargin、ContextWindowSize）
   - 定义 `AgentLoopResult` 结构体替代 `TurnResult`：
     - `FinalText string`：最终回复文本
     - `Iterations int`：实际执行迭代数
     - `TotalToolCalls int`：总工具调用次数
     - `Aborted bool`：是否被中断
     - `Error error`：不可恢复错误
     - `StopReason string`：终止原因枚举（completed / max_iterations / context_overflow / aborted / error）
   - 定义 `IterationEvent` 结构体，通过 TurnHooks 中的新回调 `OnIterationStart(iteration int, maxIterations int)` 向外推送迭代进度

2. **`AgentLoop` 方法实现**（`agent_loop.go`）：
   - 核心循环逻辑：
     ```
     for iteration := 1; iteration <= maxIterations; iteration++ {
         1. 检查 ctx.Done() → 优雅中断
         2. 检查上下文 token → 溢出时注入提示并终止
         3. 通知上层 OnIterationStart
         4. 调 runOneLLM → 获取 LLM 响应
         5. 处理错误/中断
         6. 无 tool_use → 写 assistant 文本到 history → break（任务完成）
         7. 有 tool_use(s) → 写 assistant tool_use 消息到 history
         8. 调 ExecuteBatch → 获取 tool_results
         9. 写 user tool_result 消息到 history
         10. 继续下一轮迭代
     }
     if 达到上限 {
         注入提示让模型总结当前进展
         调 runOneLLM 获取最终回复
     }
     ```
   - **上下文溢出处理**：每次迭代前调 `RemainingTokens()`，若低于 `ContextSafetyMargin`：
     - 向 history 追加一条 user 消息：「上下文空间即将耗尽，请立即总结当前进展并回复用户」
     - 再调一次 `runOneLLM` 获取最终回复
     - 设置 StopReason 为 `context_overflow`
   - **达到迭代上限处理**：循环正常退出但达到上限时：
     - 向 history 追加一条 user 消息：「已达到最大迭代次数限制（N次），请总结当前进展并回复用户」
     - 再调一次 `runOneLLM` 获取最终回复
     - 设置 StopReason 为 `max_iterations`
   - **优雅中断**：在循环顶部和 `runOneLLM` 返回后检查 `ctx.Done()`，中断时：
     - 已完成的迭代结果已写入 history（自然保留）
     - 设置 StopReason 为 `aborted`
     - 直接返回当前 AgentLoopResult

3. **修改 `manager.go`**：
   - 保留 `RunTurn` 方法签名作为兼容包装，内部构造 `AgentLoopConfig` 后调用 `AgentLoop`
   - `TurnResult` 从 `AgentLoopResult` 转换
   - 清理 Task 1 中标记 deprecated 的 `ToolUse` 单字段引用

4. **TurnHooks 扩展**：
   - 新增 `OnIterationStart func(iteration int, maxIterations int)` 回调
   - 新增 `OnLoopDone func(result AgentLoopResult)` 回调，循环结束时触发

**参考资料**：
- 现有 `RunTurn` 实现：`src/internal/engine/conversation/manager.go:244-310`
- `RemainingTokens` 方法：`src/internal/engine/conversation/manager.go:108-115`
- `ToolHandler.Execute` 方法：`src/internal/engine/conversation/tool_handler.go:134-165`

---

## Task 5: 交互层 — handler.go 适配 AgentLoop

**状态**：已完成

**目标**：更新 WebUI handler 层，适配 AgentLoop 的新接口和新事件类型，使前端能正确展示多轮迭代的工具调用和迭代进度。

**影响文件**：
- `src/internal/interaction/web/handler.go` — 修改 `runStream` 方法，适配新 hooks
- `src/internal/interaction/web/protocol.go` — 新增迭代进度事件类型

**依赖**：Task 4（AgentLoop 核心逻辑）

**具体内容**：

1. **修改 `runStream` 方法**（`handler.go`）：
   - 更新 `TurnHooks` 构造，新增 `OnIterationStart` 回调
   - `OnIterationStart` 回调：向 WebSocket 发送 `agent_iteration` 事件（包含当前迭代数和最大迭代数）
   - 适配 `AgentLoopResult`（替代旧的 `TurnResult`），更新退出原因判断：
     - 新增 `max_iterations` 和 `context_overflow` 两种 reason
   - 工具调用回调（`toolHandler.OnStart/OnEnd`）无需修改，每轮迭代的工具执行自然会触发

2. **新增协议类型**（`protocol.go`）：
   - 新增 `MsgTypeAgentIteration = "agent_iteration"` 消息类型
   - 新增 `AgentIterationPayload` 结构体：`Current int`、`Max int`
   - 新增 `StreamReasonMaxIterations = "max_iterations"` 和 `StreamReasonContextOverflow = "context_overflow"` 常量

3. **状态更新优化**：
   - 在 `OnIterationStart` 时发送 `status_update("thinking")`，明确告知前端进入新一轮推理
   - 每轮工具执行完成后、下一轮 LLM 调用前，也发送 `status_update("thinking")`

**参考资料**：
- `runStream` 方法：`src/internal/interaction/web/handler.go:172-262`
- 协议类型定义：`src/internal/interaction/web/protocol.go`

---

## Task 6: 接入主流程 — 整合所有改动到主入口

**状态**：已完成

**目标**：将 AgentLoop 所有组件整合到主入口，确保新旧功能无缝衔接，会话持久化格式向后兼容。

**影响文件**：
- `cmd/codepilot/main.go` — 传递新配置字段
- `src/internal/interaction/web/server.go` — 确认 handler 初始化时传递新配置

**依赖**：Task 1-5 全部完成

**具体内容**：

1. **主入口传参**：
   - 将 `Config.MaxAgentLoopIterations` 和 `Config.ContextSafetyMargin` 传递到 `AgentLoopConfig`
   - 确认 `contextWindowSize` 已在 handler 初始化时传入

2. **会话持久化兼容性检查**：
   - 验证 AgentLoop 产生的多轮 `tool_use` / `tool_result` 消息能正确序列化到 session JSON
   - 验证恢复会话后 `buildChatMessages` 能正确渲染多轮工具调用链
   - 验证旧的会话文件（Step 2 产生的单轮工具调用）在新代码下能正常加载和展示

3. **清理 deprecated 代码**：
   - 移除 `StreamChunk.ToolUse` 旧字段（如有临时兼容逻辑）
   - 移除 `RunOneTurnResult.ToolUse` 旧字段
   - 确保 `TurnResult` 转换自 `AgentLoopResult` 后对外接口不变

**参考资料**：
- 主入口：`cmd/codepilot/main.go`
- 会话持久化：`src/internal/memory/session/session.go`
- `buildChatMessages` 消息回放：`src/internal/interaction/web/handler.go:583-656`

---

## Task 7: 端到端验证

**状态**：已完成

**目标**：全面验证 AgentLoop 的各种场景，确保新旧功能均正常工作。

**影响文件**：
- 无新增/修改文件，仅验证

**依赖**：Task 6

**具体内容**：

1. **基础循环场景**：
   - 用户输入「读取 src/hello.go 的内容并告诉我它有几个函数」→ Agent 应调用 ReadFile → 分析后直接回复（单轮循环）
   - 用户输入「读取 src/hello.go 并在末尾添加一个 main 函数」→ Agent 应调用 ReadFile → WriteFile → 回复完成（两轮循环）

2. **多工具并行场景**：
   - 用户输入「同时读取 src/a.go 和 src/b.go 的内容」→ 模型应返回两个 ReadFile tool_use → 并行执行 → 结果反馈 → 模型回复

3. **迭代上限场景**：
   - 模拟或构造一个会导致模型反复调用工具的场景，验证达到 25 次上限后能优雅收尾

4. **上下文溢出场景**：
   - 在配置中设置极小的 ContextSafetyMargin，验证上下文不足时能注入提示并终止

5. **中断场景**：
   - Agent 执行到中间某轮工具调用时用户点击停止，验证已完成的消息保留在历史中
   - 中断后恢复会话，验证历史消息完整渲染

6. **工具错误反馈场景**：
   - 用户输入「读取 /nonexistent/file.txt」→ ReadFile 失败 → 错误作为 ToolResult 反馈 → 模型自主回复错误信息

7. **向后兼容**：
   - 加载 Step 2 产生的旧会话文件，验证消息正常渲染
   - 验证单轮无工具调用的对话仍然正常工作
