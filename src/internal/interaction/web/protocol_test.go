package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestEncodeDecodeRoundTrip 验证 Message 信封的编码/解码 round-trip。
func TestEncodeDecodeRoundTrip(t *testing.T) {
	payload, err := json.Marshal(UserInputPayload{Text: "hello"})
	if err != nil {
		t.Fatalf("构造 payload 失败: %v", err)
	}
	original := Message{Type: MsgTypeUserInput, Payload: payload}

	data, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode 失败: %v", err)
	}

	decoded, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode 失败: %v", err)
	}
	if decoded.Type != original.Type {
		t.Errorf("Type = %q，期望 %q", decoded.Type, original.Type)
	}
	if string(decoded.Payload) != string(original.Payload) {
		t.Errorf("Payload = %s，期望 %s", decoded.Payload, original.Payload)
	}
}

// TestEncodeRejectsEmptyType 验证空 type 消息被拒绝。
func TestEncodeRejectsEmptyType(t *testing.T) {
	_, err := Encode(Message{Type: ""})
	if err == nil {
		t.Fatal("期望空 type 返回错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "类型不能为空") {
		t.Errorf("错误应包含 '类型不能为空'，实际: %v", err)
	}
}

// TestDecodeRejectsEmptyType 验证解码空 type 时返回错误。
func TestDecodeRejectsEmptyType(t *testing.T) {
	_, err := Decode([]byte(`{"type":"","payload":{}}`))
	if err == nil {
		t.Fatal("期望空 type 返回错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "type 字段不能为空") {
		t.Errorf("错误应包含 'type 字段不能为空'，实际: %v", err)
	}
}

// TestDecodeRejectsInvalidJSON 验证解码非法 JSON 时返回错误。
func TestDecodeRejectsInvalidJSON(t *testing.T) {
	_, err := Decode([]byte(`{this is not json`))
	if err == nil {
		t.Fatal("期望非法 JSON 返回错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "解码 WebSocket 消息失败") {
		t.Errorf("错误应包含 '解码 WebSocket 消息失败'，实际: %v", err)
	}
}

// TestEncodePayload 验证 EncodePayload 构造完整消息。
func TestEncodePayload(t *testing.T) {
	data, err := EncodePayload(MsgTypeStreamChunk, StreamChunkPayload{Delta: "abc"})
	if err != nil {
		t.Fatalf("EncodePayload 失败: %v", err)
	}
	decoded, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode 失败: %v", err)
	}
	p, err := AsPayload[StreamChunkPayload](decoded)
	if err != nil {
		t.Fatalf("AsPayload 失败: %v", err)
	}
	if p.Delta != "abc" {
		t.Errorf("Delta = %q，期望 %q", p.Delta, "abc")
	}
}

// TestEncodePayloadRejectsEmptyType 验证 EncodePayload 空 type 报错。
func TestEncodePayloadRejectsEmptyType(t *testing.T) {
	_, err := EncodePayload("", StreamChunkPayload{})
	if err == nil {
		t.Fatal("期望空 type 返回错误，实际为 nil")
	}
}

// TestEncodePayloadNil 验证 EncodePayload 接受 nil payload。
func TestEncodePayloadNil(t *testing.T) {
	data, err := EncodePayload(MsgTypeListSessions, nil)
	if err != nil {
		t.Fatalf("EncodePayload(nil) 失败: %v", err)
	}
	decoded, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode 失败: %v", err)
	}
	if decoded.Type != MsgTypeListSessions {
		t.Errorf("Type = %q，期望 %q", decoded.Type, MsgTypeListSessions)
	}
	// nil payload 编码后为 JSON "null"，解码后 Payload 字段长度为 4
	if string(decoded.Payload) != "null" {
		t.Errorf("Payload = %q，期望 %q", decoded.Payload, "null")
	}
}

// TestAsPayloadEmpty 验证 AsPayload 在 Payload 为空时返回零值。
func TestAsPayloadEmpty(t *testing.T) {
	msg := Message{Type: MsgTypeListSessions}
	p, err := AsPayload[SessionListPayload](msg)
	if err != nil {
		t.Fatalf("AsPayload 失败: %v", err)
	}
	if len(p.Sessions) != 0 {
		t.Errorf("Sessions 期望为空，实际 %v", p.Sessions)
	}
}

// TestAsPayloadInvalidJSON 验证 AsPayload 在 payload 非法 JSON 时报错。
func TestAsPayloadInvalidJSON(t *testing.T) {
	msg := Message{
		Type:    MsgTypeUserInput,
		Payload: json.RawMessage(`{not json`),
	}
	_, err := AsPayload[UserInputPayload](msg)
	if err == nil {
		t.Fatal("期望非法 JSON 返回错误，实际为 nil")
	}
}

// TestAsPayloadTypeMismatch 验证 AsPayload 在 payload 类型不匹配时返回零值/错误。
func TestAsPayloadTypeMismatch(t *testing.T) {
	msg := Message{
		Type:    MsgTypeUserInput,
		Payload: json.RawMessage(`{"text": 123}`), // 数字而非字符串
	}
	p, err := AsPayload[UserInputPayload](msg)
	if err == nil {
		t.Fatal("期望类型不匹配返回错误，实际为 nil")
	}
	if p.Text != "" {
		t.Errorf("Text 期望空字符串，实际 %q", p.Text)
	}
}

// TestSessionListPayload 验证 SessionListPayload 序列化结构。
func TestSessionListPayload(t *testing.T) {
	now := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	payload := SessionListPayload{
		Sessions: []SessionSummary{
			{ID: "abc123", UpdatedAt: now, MessageCount: 5, Preview: "你好"},
		},
	}
	data, err := EncodePayload(MsgTypeSessionList, payload)
	if err != nil {
		t.Fatalf("EncodePayload 失败: %v", err)
	}
	if !strings.Contains(string(data), `"updated_at":"2026-06-02T10:00:00Z"`) {
		t.Errorf("JSON 应包含格式化后的时间，实际: %s", data)
	}
	if !strings.Contains(string(data), `"preview":"你好"`) {
		t.Errorf("JSON 应包含中文预览，实际: %s", data)
	}
}

// TestChatMessageRole 验证 ChatMessage role 字段正确序列化。
func TestChatMessageRole(t *testing.T) {
	payload := SessionLoadedPayload{
		SessionID: "s1",
		Messages: []ChatMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
	}
	data, err := EncodePayload(MsgTypeSessionLoaded, payload)
	if err != nil {
		t.Fatalf("EncodePayload 失败: %v", err)
	}
	if !strings.Contains(string(data), `"role":"user"`) {
		t.Errorf("JSON 应包含 user role，实际: %s", data)
	}
	if !strings.Contains(string(data), `"role":"assistant"`) {
		t.Errorf("JSON 应包含 assistant role，实际: %s", data)
	}
}
