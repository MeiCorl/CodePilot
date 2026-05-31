// Package tui 实现 CodePilot 的终端用户界面。
// 基于 Bubble Tea 框架，提供对话交互、流式输出、Markdown 渲染等能力。
package tui

// StreamChunkMsg 携带流式响应的一个文本片段，触发 View 更新。
type StreamChunkMsg struct {
	Content string
}

// StreamDoneMsg 表示流式响应已正常结束。
type StreamDoneMsg struct{}

// StreamErrorMsg 表示流式响应过程中发生了错误。
type StreamErrorMsg struct {
	Err error
}

// clearCopyNotifMsg 用于在超时后清除复制成功的浮动通知
type clearCopyNotifMsg struct{}

// clearSelectionHighlightMsg 用于在复制完成后延时清除选择区域的高亮背景，
// 让用户有短暂时间看到自己选中并复制了哪些内容
type clearSelectionHighlightMsg struct{}
