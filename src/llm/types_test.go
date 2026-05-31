package llm

import (
	"testing"
)

// TestTextBlockType 验证 TextBlock 的类型标识和文本返回
func TestTextBlockType(t *testing.T) {
	block := NewTextBlock("hello")
	if block.Type() != ContentBlockTypeText {
		t.Errorf("Type() 错误: 期望 %s, 实际 %s", ContentBlockTypeText, block.Type())
	}
	if block.ToText() != "hello" {
		t.Errorf("ToText() 错误: 期望 hello, 实际 %s", block.ToText())
	}
}

// TestNewTextBlock 验证 NewTextBlock 返回正确的 ContentBlock 接口类型
func TestNewTextBlock(t *testing.T) {
	block := NewTextBlock("test")
	_, ok := block.(*TextBlock)
	if !ok {
		t.Error("NewTextBlock 未返回 *TextBlock 类型")
	}
}

// TestMessageContentBlockArray 验证 Message 的 Content 字段为 ContentBlock 数组
func TestMessageContentBlockArray(t *testing.T) {
	msg := Message{
		Role:    RoleUser,
		Content: []ContentBlock{NewTextBlock("hello")},
	}
	if len(msg.Content) != 1 {
		t.Fatalf("Content 长度错误: 期望 1, 实际 %d", len(msg.Content))
	}
	if msg.Content[0].ToText() != "hello" {
		t.Errorf("Content[0] 文本错误: 期望 hello, 实际 %s", msg.Content[0].ToText())
	}
	if msg.Role != RoleUser {
		t.Errorf("Role 错误: 期望 %s, 实际 %s", RoleUser, msg.Role)
	}
}

// TestStreamChunkFields 验证 StreamChunk 结构体字段
func TestStreamChunkFields(t *testing.T) {
	chunk := StreamChunk{Content: "hi", Done: false}
	if chunk.Content != "hi" {
		t.Errorf("Content 错误: 期望 hi, 实际 %s", chunk.Content)
	}
	if chunk.Done {
		t.Error("Done 应为 false")
	}
	if chunk.Err != nil {
		t.Error("Err 应为 nil")
	}
}

// TestRoleConstants 验证角色常量值
func TestRoleConstants(t *testing.T) {
	if RoleSystem != "system" {
		t.Errorf("RoleSystem 错误: 期望 system, 实际 %s", RoleSystem)
	}
	if RoleUser != "user" {
		t.Errorf("RoleUser 错误: 期望 user, 实际 %s", RoleUser)
	}
	if RoleAssistant != "assistant" {
		t.Errorf("RoleAssistant 错误: 期望 assistant, 实际 %s", RoleAssistant)
	}
}
