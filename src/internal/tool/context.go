package tool

import "context"

// toolUseIDKey 是 context 中携带 tool_use_id 的私有 key。
// 使用空 struct 类型避免与其他包的 ctx value key 冲突。
type toolUseIDKey struct{}

// WithToolUseID 返回一个新的 context，携带指定的 tool_use_id。
//
// 调用链：engine/conversation.ToolHandler.doExecute 在调用工具前
// 通过本函数把 toolUse.ID 注入 ctx；工具实现（如 WriteFile / EditFile）
// 在 Execute 中用 ToolUseIDFromContext 取出，从而在工具侧也能把
// 执行结果按 tool_use_id 关联起来（如写入 FileDiffStore）。
func WithToolUseID(ctx context.Context, id string) context.Context {
	if id == "" {
		// 空 id 不注入，避免下游误以为有 id
		return ctx
	}
	return context.WithValue(ctx, toolUseIDKey{}, id)
}

// ToolUseIDFromContext 从 ctx 中取出由 WithToolUseID 注入的 tool_use_id。
//
// 返回值：存在时 ok=true 且 id 非空；不存在或 id 为空时 ok=false。
// 工具实现应把"未拿到 id"视为正常分支（直接跳过依赖 id 的副作用），
// 不应返回 error——这只是一种关联信息，缺它不影响工具的核心行为。
func ToolUseIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v := ctx.Value(toolUseIDKey{})
	if v == nil {
		return "", false
	}
	id, ok := v.(string)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}
