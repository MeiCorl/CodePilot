package web

import (
	"bytes"
	"encoding/json"
	"strings"
)

// 工具消息渲染相关的辅助函数。集中放在 tool_msg.go 是为了与 protocol.go
// 的"消息信封定义"职责分离：protocol.go 定义 WS 协议结构，本文件定义
// 从 llm.ContentBlock / ToolExecutionEvent 等"业务数据"到"前端展示摘要"
// 的转换逻辑，供 handler.sendSessionLoaded / handler.runStream 复用。
//
// 摘要策略：
//   - Input: 用 json.Indent 折叠到 1 行紧凑 JSON（去除首尾空白），最大 200 字符
//   - Output: UTF-8 安全截断到 500 字符，超出追加 "..."
//   - 截断点都按 rune 计算（不用 byte），避免中文等多字节字符被截成乱码

// InputSummaryMaxLen 输入参数摘要的最大字符数（按 rune 计）。
const InputSummaryMaxLen = 200

// OutputSummaryMaxLen 输出结果摘要的最大字符数（按 rune 计）。
const OutputSummaryMaxLen = 500

// SummarizeInput 把 LLM 传入的 JSON 参数压缩为单行展示串。
//
// 实现策略：尝试用 json.Indent("") 标准化，然后去除首尾空白。
// 原始 JSON 已经是合法对象时直接压缩；非合法 JSON（空、null、字符串）按
// bytes.TrimSpace 后原样返回（前端在 <pre> 中仍能完整展示）。
func SummarizeInput(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, input); err != nil {
		// 压缩失败（如 null、空对象外的非法 JSON）时回退到原文
		return truncateByRune(strings.TrimSpace(string(input)), InputSummaryMaxLen)
	}
	compacted := strings.TrimSpace(buf.String())
	return truncateByRune(compacted, InputSummaryMaxLen)
}

// SummarizeOutput 把工具执行输出截断到 OutputSummaryMaxLen 字符。
// 超长时追加 "..." 标记，方便用户识别"还有更多内容"。
func SummarizeOutput(output string) string {
	return truncateByRune(output, OutputSummaryMaxLen)
}

// ToolDisplayFromExecution 把 ToolExecutionEvent 转为前端展示用的 ToolCallDisplay。
//
// ToolCallDisplay 与 ToolCallEndPayload 字段几乎一致，独立类型是
// 为了让 session_loaded 中的历史工具消息能够脱离"时间戳 / Input RawMessage"
// 这些"实时事件"语义——历史消息只关心最终态。
//
// Step 8 接入 MCP：server 参数标识工具的远端来源。传空串时表示"内置工具"
// (即不展示 server 徽标),由调用方在持 h.mcpPool 引用时按 toolName 解析后传入。
func ToolDisplayFromExecution(toolUseID, name, input, output string, isError bool, durationMs int64, status, server string) ToolCallDisplay {
	return ToolCallDisplay{
		ID:         toolUseID,
		Name:       name,
		Input:      input,
		Output:     output,
		IsError:    isError,
		DurationMs: durationMs,
		Status:     status,
		Server:     server,
	}
}

// truncateByRune 按 rune 截断文本并在超出时追加 "..."。
// 实现细节：先 []rune 转换再截取，避免多字节字符被截断到中间字节产生乱码。
// 注意 "..." 本身不计入 maxLen——内部按 maxLen-3 计算。
func truncateByRune(s string, maxLen int) string {
	if maxLen <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
