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

// ---- Step 4: SystemPrompt 相关测试 ----

// TestSystemPromptIsEmpty 验证空 SP 判定。
func TestSystemPromptIsEmpty(t *testing.T) {
	tests := []struct {
		name string
		sp   SystemPrompt
		want bool
	}{
		{"全空", SystemPrompt{}, true},
		{"只有 SystemBlocks 但空 slice", SystemPrompt{SystemBlocks: nil}, true},
		{"只有 LeadUserMessage 但空字符串", SystemPrompt{LeadUserMessage: ""}, true},
		{"有 SystemBlocks", SystemPrompt{SystemBlocks: []SystemBlock{{Text: "x"}}}, false},
		{"有 LeadUserMessage", SystemPrompt{LeadUserMessage: "y"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sp.IsEmpty(); got != tt.want {
				t.Errorf("IsEmpty() = %v, 期望 %v", got, tt.want)
			}
		})
	}
}

// TestNewSystemPromptFromText 验证便捷构造函数。
func TestNewSystemPromptFromText(t *testing.T) {
	// 空字符串返回零值
	sp := NewSystemPromptFromText("")
	if !sp.IsEmpty() {
		t.Errorf("空字符串应返回 IsEmpty=true 的 SP，实际: %+v", sp)
	}

	// 非空字符串构造单段可缓存 SP
	sp = NewSystemPromptFromText("you are an assistant")
	if sp.IsEmpty() {
		t.Fatal("非空字符串应返回非空 SP")
	}
	if len(sp.SystemBlocks) != 1 {
		t.Fatalf("应构造 1 段 SystemBlock，实际 %d 段", len(sp.SystemBlocks))
	}
	if sp.SystemBlocks[0].Text != "you are an assistant" {
		t.Errorf("Text = %q, 期望 %q", sp.SystemBlocks[0].Text, "you are an assistant")
	}
	if !sp.SystemBlocks[0].Cacheable {
		t.Error("默认 Cacheable 应为 true")
	}
}
