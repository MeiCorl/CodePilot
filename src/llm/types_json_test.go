package llm

import (
	"encoding/json"
	"testing"
)

// TestMessageMarshalJSON 验证 Message 的 JSON 序列化输出格式。
func TestMessageMarshalJSON(t *testing.T) {
	msg := Message{
		Role:    RoleUser,
		Content: []ContentBlock{NewTextBlock("hello")},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}

	// 验证 JSON 结构包含 type 鉴别字段
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("解析序列化结果失败: %v", err)
	}
	if raw["role"] != "user" {
		t.Fatalf("role 应为 'user'，实际为 %v", raw["role"])
	}
	content, ok := raw["content"].([]interface{})
	if !ok || len(content) != 1 {
		t.Fatalf("content 应为长度 1 的数组")
	}
	block, ok := content[0].(map[string]interface{})
	if !ok {
		t.Fatal("content[0] 应为对象")
	}
	if block["type"] != "text" {
		t.Fatalf("type 应为 'text'，实际为 %v", block["type"])
	}
	if block["text"] != "hello" {
		t.Fatalf("text 应为 'hello'，实际为 %v", block["text"])
	}
}

// TestMessageUnmarshalJSON 验证 Message 的 JSON 反序列化。
func TestMessageUnmarshalJSON(t *testing.T) {
	jsonStr := `{"role":"assistant","content":[{"type":"text","text":"world"}]}`
	var msg Message
	if err := json.Unmarshal([]byte(jsonStr), &msg); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if msg.Role != RoleAssistant {
		t.Fatalf("role 应为 assistant，实际为 %s", msg.Role)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("content 应有 1 个 block")
	}
	if msg.Content[0].Type() != ContentBlockTypeText {
		t.Fatalf("block 类型应为 text")
	}
	if msg.Content[0].ToText() != "world" {
		t.Fatalf("block 文本应为 'world'，实际为 '%s'", msg.Content[0].ToText())
	}
}

// TestMessageRoundTrip 验证序列化后再反序列化的完整性。
func TestMessageRoundTrip(t *testing.T) {
	original := Message{
		Role: RoleUser,
		Content: []ContentBlock{
			NewTextBlock("第一段"),
			NewTextBlock("第二段"),
		},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}

	var restored Message
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}

	if restored.Role != original.Role {
		t.Fatalf("role 不一致")
	}
	if len(restored.Content) != len(original.Content) {
		t.Fatalf("content 数量不一致: 期望 %d，实际 %d", len(original.Content), len(restored.Content))
	}
	for i, block := range restored.Content {
		if block.ToText() != original.Content[i].ToText() {
			t.Fatalf("content[%d] 文本不一致: 期望 '%s'，实际 '%s'", i, original.Content[i].ToText(), block.ToText())
		}
	}
}

// TestMessageUnmarshalUnsupportedType 验证不支持的内容块类型报错。
func TestMessageUnmarshalUnsupportedType(t *testing.T) {
	jsonStr := `{"role":"user","content":[{"type":"image","url":"http://example.com/img.png"}]}`
	var msg Message
	err := json.Unmarshal([]byte(jsonStr), &msg)
	if err == nil {
		t.Fatal("不支持的类型应返回错误")
	}
}

// TestMessageMarshalEmptyContent 验证空 Content 的序列化。
func TestMessageMarshalEmptyContent(t *testing.T) {
	msg := Message{
		Role:    RoleSystem,
		Content: []ContentBlock{},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("序列化空 Content 失败: %v", err)
	}

	var restored Message
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("反序列化空 Content 失败: %v", err)
	}
	if len(restored.Content) != 0 {
		t.Fatalf("空 Content 反序列化后应为空数组")
	}
}

// ---- ToolUseBlock / ToolResultBlock 相关测试 ----

// TestToolUseBlockType 验证 ToolUseBlock 的类型标识与文本表示。
func TestToolUseBlockType(t *testing.T) {
	input := json.RawMessage(`{"file_path":"main.go"}`)
	block := NewToolUseBlock("call_001", "read_file", input)
	if block.Type() != ContentBlockTypeToolUse {
		t.Errorf("Type 错误: %s", block.Type())
	}
	got := block.ToText()
	if got != "tool_use(read_file, id=call_001)" {
		t.Errorf("ToText 错误: %s", got)
	}
}

// TestToolResultBlockType 验证 ToolResultBlock 的类型标识与文本表示。
func TestToolResultBlockType(t *testing.T) {
	ok := NewToolResultBlock("call_001", "file content", false)
	if ok.Type() != ContentBlockTypeToolResult {
		t.Errorf("Type 错误: %s", ok.Type())
	}
	if ok.ToText() != "file content" {
		t.Errorf("成功结果 ToText 错误: %s", ok.ToText())
	}

	fail := NewToolResultBlock("call_002", "file not found", true)
	if fail.ToText() != "error: file not found" {
		t.Errorf("失败结果 ToText 错误: %s", fail.ToText())
	}
}

// TestMessageRoundTripToolUse 验证 ToolUseBlock 序列化往返保留 id/name/input。
func TestMessageRoundTripToolUse(t *testing.T) {
	input := json.RawMessage(`{"file_path":"src/main.go","offset":10}`)
	orig := Message{
		Role: RoleAssistant,
		Content: []ContentBlock{
			NewTextBlock("我需要读取文件"),
			NewToolUseBlock("call_abc", "read_file", input),
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}

	// 校验序列化结果包含 type 字段与必要子字段
	s := string(data)
	for _, want := range []string{
		`"type":"text"`,
		`"type":"tool_use"`,
		`"id":"call_abc"`,
		`"name":"read_file"`,
		`"file_path":"src/main.go"`,
	} {
		if !contains(s, want) {
			t.Errorf("序列化结果缺少字段 %s: %s", want, s)
		}
	}

	var got Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal 失败: %v", err)
	}
	if len(got.Content) != 2 {
		t.Fatalf("Content 长度错误: %d", len(got.Content))
	}
	tub, ok := got.Content[1].(*ToolUseBlock)
	if !ok {
		t.Fatalf("Content[1] 应为 *ToolUseBlock, 实际 %T", got.Content[1])
	}
	if tub.ID != "call_abc" {
		t.Errorf("ID 错误: %s", tub.ID)
	}
	if tub.Name != "read_file" {
		t.Errorf("Name 错误: %s", tub.Name)
	}
	if string(tub.Input) != string(input) {
		t.Errorf("Input 错误: %s", tub.Input)
	}
}

// TestMessageRoundTripToolResult 验证 ToolResultBlock 序列化往返保留 tool_use_id/content/is_error。
func TestMessageRoundTripToolResult(t *testing.T) {
	orig := Message{
		Role: RoleUser,
		Content: []ContentBlock{
			NewToolResultBlock("call_abc", "main.go 第一行内容", false),
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}

	s := string(data)
	for _, want := range []string{
		`"type":"tool_result"`,
		`"tool_use_id":"call_abc"`,
		`"content":"main.go 第一行内容"`,
		`"is_error":false`,
	} {
		if !contains(s, want) {
			t.Errorf("序列化结果缺少字段 %s: %s", want, s)
		}
	}

	var got Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal 失败: %v", err)
	}
	trb, ok := got.Content[0].(*ToolResultBlock)
	if !ok {
		t.Fatalf("Content[0] 应为 *ToolResultBlock, 实际 %T", got.Content[0])
	}
	if trb.ToolUseID != "call_abc" {
		t.Errorf("ToolUseID 错误: %s", trb.ToolUseID)
	}
	if trb.Content != "main.go 第一行内容" {
		t.Errorf("Content 错误: %s", trb.Content)
	}
	if trb.IsError {
		t.Error("IsError 应为 false")
	}
}

// TestMessageRoundTripMixedAssistant 验证 assistant 消息同时含 text + tool_use，常见一次 LLM 响应。
func TestMessageRoundTripMixedAssistant(t *testing.T) {
	orig := Message{
		Role: RoleAssistant,
		Content: []ContentBlock{
			NewTextBlock("我来读取文件。"),
			NewToolUseBlock("call_001", "read_file", json.RawMessage(`{"file_path":"a.go"}`)),
			NewToolUseBlock("call_002", "read_file", json.RawMessage(`{"file_path":"b.go"}`)),
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}
	var got Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal 失败: %v", err)
	}
	if len(got.Content) != 3 {
		t.Fatalf("Content 长度错误: %d", len(got.Content))
	}
	if got.Content[0].ToText() != "我来读取文件。" {
		t.Errorf("Content[0] 文本错误: %s", got.Content[0].ToText())
	}
	for i, expectedID := range []string{"call_001", "call_002"} {
		tub, ok := got.Content[i+1].(*ToolUseBlock)
		if !ok {
			t.Fatalf("Content[%d] 应为 *ToolUseBlock", i+1)
		}
		if tub.ID != expectedID {
			t.Errorf("Content[%d] ID 错误: %s", i+1, tub.ID)
		}
	}
}

// TestMessageUnmarshalToolResultError 验证 IsError=true 的 tool_result 正确往返。
func TestMessageUnmarshalToolResultError(t *testing.T) {
	orig := Message{
		Role: RoleUser,
		Content: []ContentBlock{
			NewToolResultBlock("call_xyz", "permission denied", true),
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}
	var got Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal 失败: %v", err)
	}
	trb := got.Content[0].(*ToolResultBlock)
	if !trb.IsError {
		t.Error("IsError 应为 true")
	}
	if trb.Content != "permission denied" {
		t.Errorf("Content 错误: %s", trb.Content)
	}
}

// contains 字符串包含判断，测试代码保持极简不引 strings 包。
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
