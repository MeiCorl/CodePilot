// Package hook — 12 类事件常量 + 分类映射的对外别名层(实现见 hookcontext)。
//
// 同样为打破 import cycle 而迁出到 hookcontext 子包;本文件通过 var alias
// 重新暴露 Event 常量 + AllEvents / EventCategory / IsValidEvent,调用方
// `hook.EventPreToolUse` / `hook.IsValidEvent(...)` 写法不变。
package hook

import "github.com/MeiCorl/CodePilot/src/internal/hookcontext"

// 12 类事件常量(spec §A 表格顺序)。
const (
	EventProgramStart = hookcontext.EventProgramStart
	EventProgramExit  = hookcontext.EventProgramExit
	EventCompact      = hookcontext.EventCompact
	EventError        = hookcontext.EventError

	EventSessionStart = hookcontext.EventSessionStart
	EventSessionEnd   = hookcontext.EventSessionEnd

	EventIterationStart = hookcontext.EventIterationStart
	EventIterationEnd   = hookcontext.EventIterationEnd

	EventPreToolUse  = hookcontext.EventPreToolUse
	EventPostToolUse = hookcontext.EventPostToolUse

	EventPreMessage  = hookcontext.EventPreMessage
	EventPostMessage = hookcontext.EventPostMessage
)

// 5 类 Category 枚举值。
const (
	CategorySystem    = hookcontext.CategorySystem
	CategorySession   = hookcontext.CategorySession
	CategoryIteration = hookcontext.CategoryIteration
	CategoryTool      = hookcontext.CategoryTool
	CategoryMessage   = hookcontext.CategoryMessage
)

// AllEvents 透传 hookcontext 版本。
var AllEvents = hookcontext.AllEvents

// EventCategory 透传 hookcontext 版本。
var EventCategory = hookcontext.EventCategory

// IsValidEvent 透传 hookcontext 版本。
var IsValidEvent = hookcontext.IsValidEvent
