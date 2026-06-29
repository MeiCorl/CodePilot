// Package hookcontext — 12 类事件常量 + 分类映射 + 校验。
//
// 同样为避免 hook ↔ executor import cycle,把事件常量提到本独立子包;
// hook 父包通过 var alias 暴露 EventProgramStart / EventPreToolUse 等。
package hookcontext

// 12 类事件常量(spec §A 表格顺序)。
const (
	EventProgramStart = "program_start"
	EventProgramExit  = "program_exit"
	EventCompact      = "compact"
	EventError        = "error"

	EventSessionStart = "session_start"
	EventSessionEnd   = "session_end"

	EventIterationStart = "iteration_start"
	EventIterationEnd   = "iteration_end"

	EventPreToolUse  = "pre_tool_use"
	EventPostToolUse = "post_tool_use"

	EventPreMessage  = "pre_message"
	EventPostMessage = "post_message"
)

// 5 类 Category 枚举值。
const (
	CategorySystem    = "system"
	CategorySession   = "session"
	CategoryIteration = "iteration"
	CategoryTool      = "tool"
	CategoryMessage   = "message"
)

// AllEvents 按 spec §A 表格顺序列出全部 12 类事件名。
var AllEvents = []string{
	EventProgramStart,
	EventProgramExit,
	EventCompact,
	EventError,
	EventSessionStart,
	EventSessionEnd,
	EventIterationStart,
	EventIterationEnd,
	EventPreToolUse,
	EventPostToolUse,
	EventPreMessage,
	EventPostMessage,
}

// EventCategory 把每个事件名映射到所属分类(5 类)。
var EventCategory = map[string]string{
	EventProgramStart:   CategorySystem,
	EventProgramExit:    CategorySystem,
	EventCompact:        CategorySystem,
	EventError:          CategorySystem,
	EventSessionStart:   CategorySession,
	EventSessionEnd:     CategorySession,
	EventIterationStart: CategoryIteration,
	EventIterationEnd:   CategoryIteration,
	EventPreToolUse:     CategoryTool,
	EventPostToolUse:    CategoryTool,
	EventPreMessage:     CategoryMessage,
	EventPostMessage:    CategoryMessage,
}

var validEventSet = func() map[string]bool {
	m := make(map[string]bool, len(AllEvents))
	for _, e := range AllEvents {
		m[e] = true
	}
	return m
}()

// IsValidEvent 返回字符串是否在 12 类合法事件集合内。
func IsValidEvent(s string) bool {
	return validEventSet[s]
}
