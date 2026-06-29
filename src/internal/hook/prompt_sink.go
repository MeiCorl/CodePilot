// Package hook — PromptSink 接口定义 (spec §D.2 + Task 5 §2.4)。
//
// PromptSink 是 prompt action 与「对话上下文」之间的解耦点:Engine 在 prompt
// action 触发时拿到 Executor 渲染好的文本,调 AppendToCurrentMessage 把它注入
// 到「当前轮 user 消息尾部」(<system-reminder>...</system-reminder> 包裹),
// 由 PromptBuilder / ConversationManager 在下一轮 assemble 时一并输出。
//
// [Why interface 而非直接调 conversationManager]  hook 包是工具层组件,不
// 应反向依赖引擎层 / 会话层;通过 interface 把「注入位置」的策略交给上层
// 实现(由 Task 6 integration/prompt.go 提供具体实现)。Engine 只调
// AppendToCurrentMessage,无关心底层如何把 text 拼到 message list。
//
// sink 为 nil 时,Engine 走降级:warn log + skip(spec §F.2「sink 为 nil 时
// prompt action warn log + skip」)。这样 Hook 引擎在测试 / 早期启动期
// (PromptBuilder 尚未装配)也能安全运行。
package hook

// PromptSink 是 prompt action 的「文本注入」扩展点。
//
// 实现方需要在 EngineConfig.PromptSink 字段传入;Engine 收到 prompt action
// 渲染结果时调 AppendToCurrentMessage。
//
// 实现约束(由 Task 6 integration/prompt.go 满足):
//   - 文本用 <system-reminder>...</system-reminder> 包裹(LLM 明确感知规约边界);
//   - 拼到「当前轮的 user 消息尾部」;若 Agent Loop 已在本轮则拼到本轮末尾;
//   - 不进入 system 字段(不破坏 Anthropic prompt caching);
//   - 不修改历史 messages(仅影响下一轮 assemble 输出)。
//
// 返回 error 时 Engine 走 warn log 记录,不影响主 Agent Loop(与其它 action
// 的错误隔离语义一致)。
type PromptSink interface {
	// AppendToCurrentMessage 把 text 注入到当前轮的 user 消息尾部。
	//
	// 重复调用应按调用顺序拼接(同一轮可能有多个 prompt action 触发)。
	AppendToCurrentMessage(text string) error
}
