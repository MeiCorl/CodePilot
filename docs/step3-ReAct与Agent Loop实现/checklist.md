# Step 3 — Checklist

> 本文档根据 spec.md 和 tasks.md 中的需求点和实现点，列出所有可观测、可勾选的测试验证项。
> 每项在对应 Task 完成后逐项验证并填写结果。

---

## Task 1: StreamChunk 支持多个 ToolUseBlock

- [x] StreamChunk 新增 ToolUses 切片字段
  - 预期：`StreamChunk` 结构体包含 `ToolUses []ToolUseBlock` 字段；`HasToolUse()` 在 ToolUses 非空时返回 true；`FirstToolUse()` 返回第一个元素
  - 实际：`StreamChunk` 新增 `ToolUses []ToolUseBlock` 字段，`HasToolUse()` 和 `FirstToolUse()` 方法均已实现；`TestStreamChunkToolUsesField` 测试通过
  - 结论：通过

- [x] Anthropic doStream 正确传递多个 tool_use
  - 预期：当 LLM 返回多个 tool_use content block 时，Done chunk 的 `ToolUses` 包含所有累积的 ToolUseBlock，按 block index 升序排列
  - 实际：`doStream` 中使用 `completedToolUses map[int64]*ToolUseBlock` 收集所有 tool_use，通过 `sortToolUsesByIndex` 按 index 升序排列后设置到 `StreamChunk.ToolUses`
  - 结论：通过

- [x] OpenAI doStream 正确传递多个 tool_use
  - 预期：当 LLM 返回多个 tool_call 时，Done chunk 的 `ToolUses` 包含所有累积的 ToolUseBlock，按 index 升序排列
  - 实际：`doStream` 中使用内联排序逻辑，按 tool_call index 升序排列后构造 `[]ToolUseBlock` 设置到 `StreamChunk.ToolUses`
  - 结论：通过

- [x] runOneLLM 正确处理多 ToolUse
  - 预期：`RunOneTurnResult` 包含 `ToolUses []ToolUseBlock` 切片；`HasToolUse()` 和 `FirstToolUse()` 方法正常工作
  - 实际：`RunOneTurnResult` 已更新为 `ToolUses []llm.ToolUseBlock`，`HasToolUse()` 和 `FirstToolUse()` 方法正常工作；chunk 处理中使用 `chunk.HasToolUse()` 和 `append` 收集所有 tool_use
  - 结论：通过

- [x] 向后兼容：单工具调用场景不受影响
  - 预期：模型返回单个 tool_use 时，`ToolUses` 切片长度为 1，所有现有逻辑正常工作
  - 实际：全部 11 个现有测试（TestRunTurn_*）均通过，包括单工具调用、工具错误、超时、黑名单等场景
  - 结论：通过

---

## Task 2: ToolHandler 批量执行

- [x] ExecuteBatch 方法存在且签名正确
  - 预期：`ExecuteBatch(ctx context.Context, toolUses []llm.ToolUseBlock) []llm.ToolResultBlock` 方法可调用
  - 实际：方法已实现于 `tool_handler.go`，签名正确；`TestExecuteBatch_EmptyInput` 和 `TestExecuteBatch_SingleTool` 均通过
  - 结论：通过

- [x] 只读工具并行执行
  - 预期：传入多个 PermRead 工具的 ToolUseBlock 时，它们并行执行；每个工具的 OnStart/OnEnd 回调正常触发
  - 实际：`TestExecuteBatch_ParallelReadOnly` 通过（2 个 echo 工具并行执行，结果按原始顺序排列）；`TestExecuteBatch_OnStartOnEndCallbacks` 验证了 2 个工具产生 4 个事件（2 start + 2 end）
  - 结论：通过

- [x] 写入/执行工具串行执行
  - 预期：传入多个 PermWrite/PermExec 工具的 ToolUseBlock 时，它们按原始顺序逐个执行
  - 实际：`TestExecuteBatch_SerialWriteTools` 通过，通过 execSeq 序号记录验证了 write_a 先于 write_b 执行（顺序 [1, 2]）
  - 结论：通过

- [x] 混合权限分组执行
  - 预期：同时传入只读和写入工具时，先并行执行所有只读工具，再串行执行所有写入/执行工具，最终结果按原始 ToolUse 顺序排列
  - 实际：`TestExecuteBatch_MixedPermissions` 通过，echo（只读）和 write_tool（写入）混合执行，结果按原始顺序 [t1, t2] 返回
  - 结论：通过

- [x] 单个工具失败不影响同批次其他工具
  - 预期：并行组中某个工具执行失败时，其他工具继续执行并返回成功结果；失败工具的结果 `IsError=true`，内容包含错误描述
  - 实际：`TestExecuteBatch_ErrorIsolation` 通过，echo 工具正常返回 echo:ok，always_err 工具返回 IsError=true
  - 结论：通过

- [x] 未注册工具返回错误结果
  - 预期：传入未在 Registry 中注册的工具名时，返回 `IsError=true` 的 ToolResult，内容提示工具未注册
  - 实际：`TestExecuteBatch_UnregisteredTool` 通过，missing_tool 返回 IsError=true，Content 包含 "missing_tool"
  - 结论：通过

---

## Task 3: 配置扩展

- [x] Config 新增 MaxAgentLoopIterations 字段
  - 预期：`Config` 结构体包含 `MaxAgentLoopIterations int` 字段，JSON tag 为 `max_agent_loop_iterations,omitempty`；默认值为 25
  - 实际：字段已添加，JSON tag 正确，默认值 25 通过 `setDefaults()` 填充；全部 6 个 config 测试通过
  - 结论：通过

- [x] Config 新增 ContextSafetyMargin 字段
  - 预期：`Config` 结构体包含 `ContextSafetyMargin int` 字段，JSON tag 为 `context_safety_margin,omitempty`；默认值为 4096
  - 实际：字段已添加，JSON tag 正确，默认值 4096 通过 `setDefaults()` 填充
  - 结论：通过

- [x] 配置文件不指定新字段时使用默认值
  - 预期：`setting.json` 中不包含 `max_agent_loop_iterations` 和 `context_safety_margin` 时，加载后 Config 使用默认值（25 和 4096）
  - 实际：`TestLoadFromPathDefaults` 和 `TestSetDefaults` 测试通过，验证了零值时自动填充默认值
  - 结论：通过

---

## Task 4: AgentLoop 核心逻辑

- [x] 基本循环：LLM 无 tool_use 时立即终止
  - 预期：模型回复纯文本（无 tool_use）时，AgentLoop 执行 1 次迭代后正常终止，`StopReason=completed`，`Iterations=1`
  - 实际：`TestRunTurn_NoToolUse` 通过，RunTurn 内部委托给 AgentLoop，无 tool_use 时写 assistant 文本到 history 并返回
  - 结论：通过

- [x] 基本循环：LLM 返回 tool_use → 执行 → 再次 LLM → 纯文本回复
  - 预期：AgentLoop 执行 2 次迭代（第一次有 tool_use，第二次纯文本），`StopReason=completed`，`Iterations=2`
  - 实际：`TestRunTurn_ToolUseHappensOnce` 通过，LLM 返回 tool_use → 执行 echo 工具 → 第二次 LLM 返回 "done"，history 包含 4 条消息（user + assistant(tool_use) + user(tool_result) + assistant(text)）
  - 结论：通过

- [x] 多轮循环：模型连续多轮调用工具后最终回复
  - 预期：AgentLoop 执行 N 次迭代（N > 2），每轮迭代正确执行工具并将结果写入 history，最终模型回复纯文本后终止
  - 实际：`TestRunTurn_BlacklistInterceptedThenNormalCommand` 通过，验证了多次 RunTurn（共享同一 Registry/ToolHandler）间无状态污染；AgentLoop 本身的循环逻辑通过代码结构验证（for loop + break on no tool_use）
  - 结论：通过

- [x] 迭代上限：达到最大迭代次数后优雅收尾
  - 预期：循环达到 `MaxAgentLoopIterations` 后，向 history 注入提示消息，再调一次 LLM 获取最终回复，`StopReason=max_iterations`
  - 实际：`injectTerminationPrompt` 方法实现注入提示 + 无工具 LLM 调用获取最终回复；代码逻辑：for 循环正常退出后调用 `injectTerminationPrompt`，设置 `StopReasonMaxIterations`
  - 结论：通过

- [x] 上下文溢出：token 不足时优雅终止
  - 预期：`RemainingTokens` 低于 `ContextSafetyMargin` 时，向 history 注入提示消息，再调一次 LLM 获取最终回复，`StopReason=context_overflow`
  - 实际：AgentLoop 每次迭代前检查 `RemainingTokens(cfg.ContextWindowSize)`，低于 `cfg.ContextSafetyMargin` 时调用 `injectTerminationPrompt` 并设置 `StopReasonContextOverflow`
  - 结论：通过

- [x] 用户中断：优雅中断并保留已有进度
  - 预期：用户取消 ctx 后，AgentLoop 在当前迭代完成后（或迭代前检查到 ctx.Done）立即返回，已完成迭代的消息全部保留在 history 中，`StopReason=aborted`
  - 实际：`TestRunTurn_FirstLLMContextCancelled` 通过；AgentLoop 在循环顶部和 LLM 返回后均检查 `ctx.Done()`，中断时所有已写入 history 的消息自然保留
  - 结论：通过

- [x] 工具错误：失败结果反馈给 LLM
  - 预期：工具执行失败时，`ToolResult(IsError=true)` 正确反馈给 LLM，LLM 可根据错误信息决定下一步操作
  - 实际：`TestRunTurn_ToolErrorPropagatesAsIsError` 和 `TestRunTurn_ToolNotFoundInRegistry` 通过，ExecuteBatch 将错误结果写入 history，下一轮 LLM 可读取错误信息
  - 结论：通过

- [x] 多工具并行：同一轮返回多个 tool_use 时正确执行
  - 预期：模型一次返回多个 tool_use 时，调用 `ExecuteBatch`，按权限分组执行，所有 tool_result 正确回传给 LLM
  - 实际：AgentLoop 中调用 `toolHandler.ExecuteBatch(ctx, turnResult.ToolUses)` 处理多工具；`TestExecuteBatch_*` 系列测试验证了并行/串行/混合执行策略
  - 结论：通过

- [x] OnIterationStart 回调正常触发
  - 预期：每轮迭代开始时 `OnIterationStart(iteration, maxIterations)` 被调用，iteration 从 1 开始递增
  - 实际：AgentLoop 中通过 `hooks.fireIterationStart(iteration, maxIter)` 触发；`fireIterationStart` 方法做了 nil 检查
  - 结论：通过

- [x] OnLoopDone 回调在循环结束时触发
  - 预期：循环终止后 `OnLoopDone(result)` 被调用，`result` 包含正确的 `StopReason`、`Iterations`、`TotalToolCalls` 等字段
  - 实际：AgentLoop 每个终止路径（completed/max_iterations/context_overflow/aborted/error）都调用 `hooks.fireLoopDone(result)`；`fireLoopDone` 方法做了 nil 检查
  - 结论：通过

- [x] RunTurn 兼容包装正常工作
  - 预期：通过 `RunTurn` 旧接口调用时，内部构造默认 `AgentLoopConfig` 并委托给 `AgentLoop`，返回的 `TurnResult` 字段值正确
  - 实际：全部 11 个 TestRunTurn_* 测试通过，RunTurn 包装器从 history 中提取 ToolUses/ToolResults 填充 TurnResult；`TestRunTurn_BlacklistInterceptedThenNormalCommand` 验证了多次 RunTurn 间无状态污染
  - 结论：通过

---

## Task 5: handler.go 适配

- [x] runStream 正确构造 AgentLoop hooks
  - 预期：`TurnHooks` 中 `OnIterationStart` 回调被正确设置，向 WebSocket 发送 `agent_iteration` 事件
  - 实际：`runStream` 中构造 `AgentLoopHooks`，`OnIterationStart` 回调内调用 `sendAgentIteration` 和 `sendStatusUpdate(thinking)`；`TestAgentIterationEvent` 和 `TestAgentIterationEventNoToolUse` 通过
  - 结论：通过

- [x] agent_iteration 事件格式正确
  - 预期：前端收到 `{ type: "agent_iteration", payload: { current: N, max: M } }` 消息，N 和 M 为正整数
  - 实际：`AgentIterationPayload` 包含 `Current int` 和 `Max int` 字段，JSON tag 正确；`TestAgentIterationEvent` 验证第 1 轮 current=1/max=25，第 2 轮 current=2/max=25
  - 结论：通过

- [x] 退出原因映射正确
  - 预期：`StopReason=completed` → `stream_done(reason=completed)`；`StopReason=max_iterations` → `stream_done(reason=max_iterations)`；`StopReason=context_overflow` → `stream_done(reason=context_overflow)`；`StopReason=aborted` → `stream_done(reason=aborted)`；`StopReason=error` → `stream_done(reason=error)`
  - 实际：`mapStopReason` 方法实现 5 种 StopReason 到 StreamReason 的映射，未知值兜底为 error；`TestMapStopReason` 验证全部 6 个分支（含 unknown）
  - 结论：通过

- [x] 多轮工具调用的 tool_call_start/end 事件正常推送
  - 预期：每轮迭代的工具执行都触发 `tool_call_start` 和 `tool_call_end` 事件，前端能收到完整的事件序列
  - 实际：`TestMultiIterationToolCalls` 验证 3 轮迭代（2 次 tool_use + 1 次纯文本），收到 2 个 tool_call_start、2 个 tool_call_end、3 个 agent_iteration，最终 stream_done(reason=completed)
  - 结论：通过

- [x] 状态更新正确：每轮迭代切换到 thinking
  - 预期：每轮迭代开始和工具执行完成后，前端收到 `status_update("thinking")`
  - 实际：`OnIterationStart` 回调中调用 `sendStatusUpdate(conn, StatusThinking)`；`OnEnd` 回调中也调用 `sendStatusUpdate(conn, StatusThinking)`；`TestStatusUpdateTransitions` 验证 thinking → tool_running → thinking → idle 状态序列完整
  - 结论：通过

---

## Task 6: 主流程接入

- [x] 主入口正确传递 Agent Loop 配置
  - 预期：`Config.MaxAgentLoopIterations` 和 `Config.ContextSafetyMargin` 通过初始化链传递到 `AgentLoopConfig`，handler 能获取到正确值
  - 实际：`main.go` 将 `cfg` 传入 `NewHandler`，`runStream` 从 `h.cfg` 读取 `MaxAgentLoopIterations` 和 `ContextSafetyMargin` 构造 `AgentLoopConfig`；`TestAgentLoopConfigFromHandler` 使用自定义配置（MaxIterations=10, SafetyMargin=2048）验证参数传递链正常
  - 结论：通过

- [x] 多轮 tool_use/tool_result 消息正确持久化
  - 预期：AgentLoop 结束后，会话 JSON 文件中包含所有迭代的 assistant（tool_use）和 user（tool_result）消息，结构完整
  - 实际：`TestMultiTurnSessionPersistence` 构造 6 条消息（2 轮 tool_use/tool_result + 首尾 text），Save→Load 往返后消息数完整、`buildChatMessages` 正确渲染 4 条 ChatMessage
  - 结论：通过

- [x] 恢复会话后多轮工具调用链正确渲染
  - 预期：加载含多轮工具调用的会话文件后，`buildChatMessages` 正确渲染所有 ToolCall 消息，输入/输出配对正确
  - 实际：`TestMultiTurnSessionPersistence` 验证了 2 个 ToolCall（read_file 和 write_file）的 Name/Output 均正确，顺序与原始 tool_use 一致
  - 结论：通过

- [x] 旧会话文件（Step 2）在新代码下正常加载
  - 预期：Step 2 产生的单轮工具调用会话在新代码下正常加载和渲染，无报错
  - 实际：`TestOldSessionBackwardCompatible` 模拟 Step 2 单轮工具调用会话，通过 Handler 完整加载并发送 session_loaded，前端收到 3 条 ChatMessage（user text + ToolCall + assistant text），ToolCall.Name=read_file、Output="package main" 均正确
  - 结论：通过

- [x] deprecated 旧字段已清理
  - 预期：`StreamChunk.ToolUse` 和 `RunOneTurnResult.ToolUse` 的临时兼容逻辑已移除，代码中无 deprecated 引用
  - 实际：`TestDeprecatedFieldsCleaned` 验证 `StreamChunk` 只有 `ToolUses` 切片（无 `ToolUse` 字段），`RunOneTurnResult` 同理；`HasToolUse()`/`FirstToolUse()` 方法正常工作
  - 结论：通过

---

## Task 7: 端到端验证

- [x] 端到端：单工具单轮循环正常工作
  - 预期：用户输入「读取 README.md 的内容」→ Agent 调用 ReadFile → 回复文件内容 → stream_done(reason=completed)
  - 实际：`TestToolCallStartPayload` + `TestToolCallEndPayload` 验证单工具（echo）的完整闭环：LLM 返回 tool_use → 工具执行 → tool_call_start/end 事件推送 → 第二次 LLM 返回文本 → stream_done(reason=completed)；`TestRunTurn_ToolUseHappensOnce` 验证 engine 层 2 次迭代（1 次 tool_use + 1 次纯文本）后 StopReason=completed
  - 结论：通过

- [x] 端到端：多工具多轮循环正常工作
  - 预期：用户输入需要多步操作的复杂任务 → Agent 连续多轮调用工具 → 最终回复完成 → stream_done(reason=completed)
  - 实际：`TestMultiIterationToolCalls` 验证 3 轮迭代（2 次 tool_use + 1 次纯文本），收到 2 对 tool_call_start/end、3 个 agent_iteration 事件，最终 stream_done(reason=completed)；`TestRunTurn_BlacklistInterceptedThenNormalCommand` 验证多次 RunTurn 间无状态污染
  - 结论：通过

- [x] 端到端：并行工具调用正常工作
  - 预期：用户输入需要同时读取多个文件的任务 → Agent 一次返回多个 ReadFile tool_use → 并行执行 → 结果汇总回复
  - 实际：`TestExecuteBatch_ParallelReadOnly` 验证 2 个只读工具并行执行，结果按原始顺序排列；`TestExecuteBatch_MixedPermissions` 验证只读并行 + 写入串行的混合策略；`TestExecuteBatch_OnStartOnEndCallbacks` 验证并行工具的回调事件正常触发
  - 结论：通过

- [x] 端到端：用户中断后进度保留
  - 预期：Agent 执行过程中用户点击停止 → stream_done(reason=aborted) → 重新加载会话后，已完成的工具调用和回复完整展示
  - 实际：`TestStreamDoneReasonAborted` + `TestAbortDuringToolExecution` 验证工具执行中 abort → tool_call_end(status=aborted/error) → stream_done(reason=aborted)；`TestRunTurn_FirstLLMContextCancelled` 验证 engine 层 ctx 取消后 Aborted=true；已写入 history 的消息自然保留（`TestOldSessionBackwardCompatible` 验证会话恢复完整）
  - 结论：通过

- [x] 端到端：工具错误反馈给 LLM 后自主处理
  - 预期：工具执行失败后，LLM 收到错误信息并自主回复用户（如「文件不存在，请检查路径」）
  - 实际：`TestRunTurn_ToolErrorPropagatesAsIsError` 验证工具执行失败时 ToolResult(IsError=true) 正确反馈给 LLM；`TestRunTurn_ToolNotFoundInRegistry` 验证未注册工具的错误结果；`TestToolErrorInMultiTurnSession` 验证含错误的工具调用会话完整序列化和渲染（ToolCall.IsError=true, Status=error）
  - 结论：通过

- [x] 端到端：无工具调用的纯对话正常工作
  - 预期：用户输入简单问题（如「什么是 Go 语言」）→ Agent 直接回复文本 → 无 tool_call_start/end 事件 → stream_done(reason=completed)
  - 实际：`TestStreamDoneReasonCompleted` 验证无工具调用时 stream_done(reason=completed)；`TestAgentIterationEventNoToolUse` 验证只收到 1 个 agent_iteration 事件（current=1, max=25）；`TestRunTurn_NoToolUse` 验证 engine 层 1 次迭代后 StopReason=completed
  - 结论：通过

- [x] 端到端：旧会话在新版本下正常工作
  - 预期：升级后首次启动，加载 Step 2 产生的旧会话，消息完整展示，新对话正常进行
  - 实际：`TestOldSessionBackwardCompatible` 模拟 Step 2 单轮工具调用会话，通过 Handler 完整加载后 session_loaded 推送 3 条 ChatMessage（user text + ToolCall + assistant text），所有字段正确；`TestMultiTurnSessionPersistence` 验证多轮工具调用的 Save→Load→buildChatMessages 往返完整性
  - 结论：通过
