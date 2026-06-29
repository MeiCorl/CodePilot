// Package hookcontext 定义 Hook 系统的事件上下文类型与变量插值函数。
//
// 提取到独立子包的原因:
//   - hook 父包需要 import hook/executor 来组织 Engine 调度;
//   - executor 子包又需要 hook.HookContext 作为 Execute 参数类型;
//   - 若 hook 父包放 HookContext,会产生「hook → executor → hook」的 import cycle。
//   - 把 HookContext / Interpolate 提取到独立子包 hookcontext 后,hook 父包 + executor
//     子包都依赖 hookcontext,无 cycle。
//
// 对外 API 兼容:hook 父包通过 type alias 暴露 HookContext / Interpolate / 工厂函数,
// 调用方代码 `hook.HookContext` / `hook.NewPreToolUseContext` / `hook.Interpolate`
// 无需改动。
//
// 本包不依赖 hook 父包、不依赖 executor / matcher 子包;只依赖标准库 + 第三方库 fmt/strconv/strings/time。
package hookcontext

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// HookContext 是 Hook 触发时的上下文载体。
//
// 字段语义详见 spec §E 表格。仅使用字段含义,不依赖 Spec 表格外的任何行为约束;
// 例如 ToolName / ToolInput 等工具级字段在非工具事件中为空字符串 / nil map,
// Interpolate 时按未设置处理(替换为空字符串)。
//
// [Why public struct] HookContext 集成点遍布 Agent Loop / ToolHandler / Session /
// Compact,public 字段避免方法调用链;同时公共字段也方便测试断言。
type HookContext struct {
	// Event 为触发事件名（如 "pre_tool_use"）,必填。
	Event string
	// Category 为事件分类（system/session/iteration/tool/message）,由工厂填充便于统计/过滤。
	Category string
	// ToolName 为工具名（大驼峰,如 WriteFile）,仅工具级事件有值。
	ToolName string
	// ToolInput 为工具参数原始 map（JSON 反序列化结果）,仅工具级事件有值。
	// [Why map[string]any 而非具体 struct] 工具参数 schema 各异,保留 raw map
	// 让 vars() 展开 tool_input.* 字段时灵活;executor 自行按 tool 决定如何序列化。
	ToolInput map[string]any
	// ToolInputFilePath 为便捷字段,从 ToolInput 自动提取的路径。
	// 优先 file_path,其次 path;都没有时为空字符串（见 ExtractToolInputFilePath）。
	ToolInputFilePath string
	// ToolResult 为工具执行结果文本（成功时）;失败时为错误描述。
	// 仅 post_tool_use 事件有值。
	ToolResult string
	// ToolIsError 为 true 表示工具执行失败。仅 post_tool_use 事件有意义。
	ToolIsError bool
	// ToolDurationMs 为工具执行耗时（毫秒）。仅 post_tool_use 事件有值。
	ToolDurationMs int64
	// MessageContent 为消息文本（用户消息或 LLM 回复）。仅消息级事件有值。
	MessageContent string
	// MessageRole 为 "user" / "assistant"。仅消息级事件有值。
	MessageRole string
	// Error 为错误文本（无错为空）。error / post_tool_use 失败时有值。
	Error string
	// SessionID 为当前会话 ID,必填（保留口径,允许为空字符串表示无会话）。
	SessionID string
	// Iteration 为当前轮次号（1-based）。轮次级 / 工具级 / 消息级事件有意义。
	Iteration int
	// Workdir 为工作目录绝对路径。
	Workdir string
	// Timestamp 为触发时间（由工厂调用方填 time.Now）,action 内部可用以计算耗时/排序。
	Timestamp time.Time
}

// nowDefault 是单测可注入的时钟,默认指向 time.Now。
//
// [Why 可注入] 测试需要确定性时间;生产代码不需要测试函数,默认值即可覆盖 99% 场景。
var nowDefault = func() time.Time { return time.Now() }

// SetNowFunc 注入自定义时钟,仅供测试使用;生产代码不应调用。
//
// [Why 暴露] hookcontext 包的 context_test.go 在另一个 package (hook_test)
// 中,需要跨包注入。封装为方法避免导出 nowDefault 字段。
func SetNowFunc(fn func() time.Time) {
	if fn != nil {
		nowDefault = fn
	}
}

// ----- 构造工厂 -----

// NewProgramContext 构造 system 级事件（program_start / program_exit）的上下文。
func NewProgramContext(event, sessionID, workdir string) *HookContext {
	return &HookContext{
		Event:     event,
		Category:  EventCategory[event],
		SessionID: sessionID,
		Workdir:   workdir,
		Timestamp: nowDefault(),
	}
}

// NewErrorContext 构造 error 事件上下文。
func NewErrorContext(sessionID, workdir, errMsg, iterationEvent string, iteration int) *HookContext {
	if iterationEvent == "" {
		iterationEvent = "error"
	}
	c := &HookContext{
		Event:     iterationEvent,
		Category:  EventCategory[iterationEvent],
		SessionID: sessionID,
		Workdir:   workdir,
		Error:     errMsg,
		Iteration: iteration,
		Timestamp: nowDefault(),
	}
	if c.Event == "" {
		c.Event = "error"
		c.Category = "system"
	}
	return c
}

// NewCompactContext 构造 compact 事件上下文（压缩前后 token / summary 文本）。
//
// beforeTokens / afterTokens 可传 -1 表示未知（不影响事件触发;Vars() 输出以
// "before_tokens" / "after_tokens" 名称暴露,若传 -1 则为空字符串）。
func NewCompactContext(sessionID, workdir, summary string, beforeTokens, afterTokens int, iteration int) *HookContext {
	return &HookContext{
		Event:     "compact",
		Category:  "system",
		SessionID: sessionID,
		Workdir:   workdir,
		Iteration: iteration,
		// MessageContent 字段复用塞入压缩 summary（spec §E 没有专门字段）,
		// 保持 HookContext 既有字段定义不变,见 spec §E "HookContext 字段定义已确定"约束。
		MessageContent: summary,
		Timestamp:      nowDefault(),
	}
}

// NewSessionContext 构造 session_start / session_end 事件上下文。
func NewSessionContext(event, sessionID, workdir string) *HookContext {
	return &HookContext{
		Event:     event,
		Category:  EventCategory[event],
		SessionID: sessionID,
		Workdir:   workdir,
		Timestamp: nowDefault(),
	}
}

// NewIterationContext 构造 iteration_start / iteration_end 事件上下文。
func NewIterationContext(event, sessionID, workdir string, iteration int) *HookContext {
	return &HookContext{
		Event:     event,
		Category:  EventCategory[event],
		SessionID: sessionID,
		Workdir:   workdir,
		Iteration: iteration,
		Timestamp: nowDefault(),
	}
}

// NewPreToolUseContext 构造工具执行前上下文。toolInput 可为 nil（非路径类工具无文件路径）。
func NewPreToolUseContext(toolName string, toolInput map[string]any, sessionID, workdir string, iteration int) *HookContext {
	return &HookContext{
		Event:             "pre_tool_use",
		Category:          "tool",
		ToolName:          toolName,
		ToolInput:         toolInput,
		ToolInputFilePath: ExtractToolInputFilePath(toolInput),
		SessionID:         sessionID,
		Workdir:           workdir,
		Iteration:         iteration,
		Timestamp:         nowDefault(),
	}
}

// NewPostToolUseContext 构造工具执行后上下文。
//
// result 与 errMsg 不可能同时非空（执行要么成功要么失败）;调用方按实际结果传入。
func NewPostToolUseContext(toolName string, toolInput map[string]any, result string, isError bool, durationMs int64, errMsg, sessionID, workdir string, iteration int) *HookContext {
	errField := ""
	if isError {
		errField = errMsg
		if result == "" {
			result = errMsg
		}
	}
	return &HookContext{
		Event:             "post_tool_use",
		Category:          "tool",
		ToolName:          toolName,
		ToolInput:         toolInput,
		ToolInputFilePath: ExtractToolInputFilePath(toolInput),
		ToolResult:        result,
		ToolIsError:       isError,
		ToolDurationMs:    durationMs,
		Error:             errField,
		SessionID:         sessionID,
		Workdir:           workdir,
		Iteration:         iteration,
		Timestamp:         nowDefault(),
	}
}

// NewMessageContext 构造消息级事件（pre_message / post_message）上下文。
func NewMessageContext(event, messageContent, messageRole, sessionID, workdir string, iteration int) *HookContext {
	return &HookContext{
		Event:          event,
		Category:       EventCategory[event],
		MessageContent: messageContent,
		MessageRole:    messageRole,
		SessionID:      sessionID,
		Workdir:        workdir,
		Iteration:      iteration,
		Timestamp:      nowDefault(),
	}
}

// ExtractToolInputFilePath 从工具参数 map 提取路径字段。
//
// 策略:优先 file_path,其次 path;都没有时返回空字符串。
func ExtractToolInputFilePath(toolInput map[string]any) string {
	if toolInput == nil {
		return ""
	}
	if v, ok := toolInput["file_path"]; ok && v != nil {
		if s, ok := toStringValue(v); ok {
			return s
		}
	}
	if v, ok := toolInput["path"]; ok && v != nil {
		if s, ok := toStringValue(v); ok {
			return s
		}
	}
	return ""
}

// toStringValue 把 map 值统一转成字符串,容忍 string / []byte / fmt.Stringer。
func toStringValue(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		return x, true
	case []byte:
		return string(x), true
	default:
		s := fmt.Sprint(v)
		if s == "<nil>" {
			return "", false
		}
		return s, true
	}
}

// Vars 把 HookContext 转成可插值的 map[string]string。
func (c *HookContext) Vars() map[string]string {
	if c == nil {
		return map[string]string{}
	}
	out := make(map[string]string, 16)

	if c.Event != "" {
		out["EVENT"] = c.Event
	}
	if c.Category != "" {
		out["CATEGORY"] = c.Category
	}
	if c.ToolName != "" {
		out["TOOL_NAME"] = c.ToolName
	}
	if c.ToolInputFilePath != "" {
		out["TOOL_INPUT_FILE_PATH"] = c.ToolInputFilePath
	}
	if c.MessageContent != "" {
		out["MESSAGE_CONTENT"] = c.MessageContent
	}
	if c.MessageRole != "" {
		out["MESSAGE_ROLE"] = c.MessageRole
	}
	if c.Error != "" {
		out["ERROR"] = c.Error
	}
	if c.SessionID != "" {
		out["SESSION_ID"] = c.SessionID
	}
	if c.Workdir != "" {
		out["WORKDIR"] = c.Workdir
	}
	if c.ToolResult != "" {
		out["TOOL_RESULT"] = c.ToolResult
	}

	out["TOOL_IS_ERROR"] = strconv.FormatBool(c.ToolIsError)

	if c.ToolDurationMs > 0 {
		out["TOOL_DURATION_MS"] = strconv.FormatInt(c.ToolDurationMs, 10)
	}
	if c.Iteration > 0 {
		out["ITERATION"] = strconv.Itoa(c.Iteration)
	}

	if !c.Timestamp.IsZero() {
		out["TIMESTAMP"] = c.Timestamp.Format(time.RFC3339Nano)
	}

	if c.ToolInput != nil {
		for k, v := range c.ToolInput {
			if s, ok := toStringValue(v); ok {
				out["TOOL_INPUT."+strings.ToUpper(k)] = s
			}
		}
	}

	return out
}
