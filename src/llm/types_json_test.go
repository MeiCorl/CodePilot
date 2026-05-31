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
