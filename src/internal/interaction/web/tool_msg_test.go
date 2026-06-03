package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MeiCorl/CodePilot/src/llm"
)

// TestSummarizeInput_PrettyJSON 验证合法 JSON 被压缩为单行展示串。
func TestSummarizeInput_PrettyJSON(t *testing.T) {
	raw := json.RawMessage(`{ "file_path": "src/main.go",  "offset": 10 }`)
	got := SummarizeInput(raw)
	if got == "" {
		t.Fatal("期望非空摘要")
	}
	// 压缩后不应再有多余空白
	if strings.Contains(got, "\n") {
		t.Errorf("摘要应为单行，实际: %q", got)
	}
	// 必须保留关键字段
	if !strings.Contains(got, `"file_path"`) || !strings.Contains(got, `"src/main.go"`) {
		t.Errorf("摘要应保留参数内容: %q", got)
	}
}

// TestSummarizeInput_Empty 验证空输入返回空串。
func TestSummarizeInput_Empty(t *testing.T) {
	if got := SummarizeInput(nil); got != "" {
		t.Errorf("nil 输入应返回空，实际: %q", got)
	}
	if got := SummarizeInput(json.RawMessage("")); got != "" {
		t.Errorf("空 RawMessage 应返回空，实际: %q", got)
	}
}

// TestSummarizeInput_Truncate 验证超长 JSON 被截断。
func TestSummarizeInput_Truncate(t *testing.T) {
	// 构造 > 200 rune 的 JSON 字符串
	long := strings.Repeat("a", 300)
	raw := json.RawMessage(`{"data":"` + long + `"}`)
	got := SummarizeInput(raw)
	if len(got) == 0 || len(got) > InputSummaryMaxLen+3 {
		t.Errorf("截断长度异常: len=%d, max=%d", len(got), InputSummaryMaxLen)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("超长应追加 '...'，实际: %q", got)
	}
}

// TestSummarizeOutput_Truncate 验证超长输出被截断。
func TestSummarizeOutput_Truncate(t *testing.T) {
	long := strings.Repeat("a", 600)
	got := SummarizeOutput(long)
	if len(got) == 0 || len(got) > OutputSummaryMaxLen+3 {
		t.Errorf("截断长度异常: len=%d, max=%d", len(got), OutputSummaryMaxLen)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("超长应追加 '...'，实际: %q", got)
	}
}

// TestSummarizeOutput_NoTruncate 验证未超长输出原样返回。
func TestSummarizeOutput_NoTruncate(t *testing.T) {
	s := "short output"
	if got := SummarizeOutput(s); got != s {
		t.Errorf("短输出应原样返回: got=%q, want=%q", got, s)
	}
}

// TestSummarizeOutput_MultibyteSafe 验证中文字符按 rune 截断,不被截成乱码。
func TestSummarizeOutput_MultibyteSafe(t *testing.T) {
	// 800 个汉字 = 800 rune,远大于 500 字符限制
	s := strings.Repeat("中", 800)
	got := SummarizeOutput(s)
	// 解码回 rune 后长度应在 500~503
	runes := []rune(got)
	if len(runes) < OutputSummaryMaxLen {
		t.Errorf("截断后 rune 长度应 >= %d, 实际: %d", OutputSummaryMaxLen, len(runes))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("超长应追加 '...', 实际: %q", got)
	}
}

// TestBuildChatMessages_TextOnly 验证纯文本消息按原样转换。
func TestBuildChatMessages_TextOnly(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("hi")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("hello")}},
	}
	out := buildChatMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("应生成 2 条 ChatMessage, 实际: %d", len(out))
	}
	if out[0].Role != "user" || out[0].Content != "hi" {
		t.Errorf("第 1 条不匹配: %+v", out[0])
	}
	if out[1].Role != "assistant" || out[1].Content != "hello" {
		t.Errorf("第 2 条不匹配: %+v", out[1])
	}
}

// TestBuildChatMessages_ToolUseWithResult 验证 tool_use + tool_result 配对生成 ToolCall。
func TestBuildChatMessages_ToolUseWithResult(t *testing.T) {
	tu := &llm.ToolUseBlock{ID: "call-1", Name: "read_file", Input: json.RawMessage(`{"file_path":"a.go"}`)}
	tr := &llm.ToolResultBlock{ToolUseID: "call-1", Content: "file content", IsError: false}
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("请读 a.go")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{tu}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{tr}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("已读完")}},
	}
	out := buildChatMessages(msgs)
	// 期望：user text + assistant tool_call + assistant text = 3 条
	if len(out) != 3 {
		t.Fatalf("应生成 3 条, 实际 %d: %+v", len(out), out)
	}
	if out[1].ToolCall == nil {
		t.Fatalf("第 2 条应为 ToolCall, 实际: %+v", out[1])
	}
	tc := out[1].ToolCall
	if tc.ID != "call-1" || tc.Name != "read_file" {
		t.Errorf("ToolCall 元数据错误: %+v", tc)
	}
	if tc.Output != "file content" {
		t.Errorf("ToolCall.Output = %q, 期望 %q", tc.Output, "file content")
	}
	if tc.IsError {
		t.Error("ToolCall.IsError 应为 false")
	}
	if tc.Status != "completed" {
		t.Errorf("ToolCall.Status = %q, 期望 completed", tc.Status)
	}
}

// TestBuildChatMessages_ToolError 验证工具错误时 ToolCall.IsError=true / status=error。
func TestBuildChatMessages_ToolError(t *testing.T) {
	tu := &llm.ToolUseBlock{ID: "e1", Name: "bash", Input: json.RawMessage(`{"command":"rm -rf /"}`)}
	tr := &llm.ToolResultBlock{ToolUseID: "e1", Content: "dangerous command", IsError: true}
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{tu}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{tr}},
	}
	out := buildChatMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("应仅生成 1 条 ToolCall 消息, 实际 %d", len(out))
	}
	tc := out[0].ToolCall
	if !tc.IsError {
		t.Error("IsError 应为 true")
	}
	if tc.Status != "error" {
		t.Errorf("Status = %q, 期望 error", tc.Status)
	}
}

// TestBuildChatMessages_ToolUseNoResult 验证未配对的 ToolUse 被标为 error。
func TestBuildChatMessages_ToolUseNoResult(t *testing.T) {
	tu := &llm.ToolUseBlock{ID: "orphan", Name: "echo", Input: json.RawMessage(`{}`)}
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{tu}},
	}
	out := buildChatMessages(msgs)
	if len(out) != 1 || out[0].ToolCall == nil {
		t.Fatalf("应生成 1 条带 ToolCall 的消息: %+v", out)
	}
	if out[0].ToolCall.Status != "error" {
		t.Errorf("未配对 ToolUse 应 status=error, 实际: %q", out[0].ToolCall.Status)
	}
}

// TestBuildChatMessages_AssistantTextAndTool 验证 assistant 同时含 text + tool_use 时拆为两条。
func TestBuildChatMessages_AssistantTextAndTool(t *testing.T) {
	tu := &llm.ToolUseBlock{ID: "x", Name: "echo", Input: json.RawMessage(`{}`)}
	tr := &llm.ToolResultBlock{ToolUseID: "x", Content: "ok", IsError: false}
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			llm.NewTextBlock("我来读文件"),
			tu,
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{tr}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("好了")}},
	}
	out := buildChatMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("期望 3 条(text + ToolCall + text), 实际 %d: %+v", len(out), out)
	}
	if out[0].Content != "我来读文件" || out[0].ToolCall != nil {
		t.Errorf("第 1 条应为 text 消息: %+v", out[0])
	}
	if out[1].Content != "" || out[1].ToolCall == nil {
		t.Errorf("第 2 条应为 ToolCall 消息: %+v", out[1])
	}
	if out[2].Content != "好了" || out[2].ToolCall != nil {
		t.Errorf("第 3 条应为二次 LLM text: %+v", out[2])
	}
}

// TestIsOnlyToolResults 验证只含 ToolResultBlock 的判断。
func TestIsOnlyToolResults(t *testing.T) {
	if !isOnlyToolResults([]llm.ContentBlock{&llm.ToolResultBlock{}}) {
		t.Error("全 ToolResultBlock 应为 true")
	}
	if isOnlyToolResults([]llm.ContentBlock{llm.NewTextBlock("hi")}) {
		t.Error("含 TextBlock 应为 false")
	}
	if isOnlyToolResults([]llm.ContentBlock{&llm.ToolResultBlock{}, llm.NewTextBlock("x")}) {
		t.Error("混合 TextBlock + ToolResultBlock 应为 false")
	}
	if isOnlyToolResults([]llm.ContentBlock{}) {
		t.Error("空数组应为 false")
	}
}

// TestMapToolEventStatus 验证 conversation 内部枚举到 web 端常量的映射。
func TestMapToolEventStatus(t *testing.T) {
	cases := map[string]string{
		"running":   "running",
		"completed": "completed",
		"error":     "error",
		"aborted":   "aborted",
		"unknown":   "error", // 未知值兜底
	}
	for in, want := range cases {
		if got := mapToolEventStatus(in); got != want {
			t.Errorf("mapToolEventStatus(%q) = %q, 期望 %q", in, got, want)
		}
	}
}
