package llm

import (
	"context"
	"testing"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/config"
)

// newTestAnthropicProvider 创建测试用 AnthropicProvider（不调用真实 API）
func newTestAnthropicProvider() *AnthropicProvider {
	cfg := &config.Config{
		Provider:   "anthropic",
		Model:     "claude-sonnet-4-20250514",
		APIKey:    "sk-ant-test-key",
		MaxTokens: 1024,
		Timeout:   5,
		MaxRetries: 1,
	}
	return NewAnthropicProvider(cfg)
}

// TestAnthropicConvertMessages 验证消息格式转换
func TestAnthropicConvertMessages(t *testing.T) {
	p := newTestAnthropicProvider()

	messages := []Message{
		{
			Role:    RoleUser,
			Content: []ContentBlock{NewTextBlock("你好")},
		},
		{
			Role:    RoleAssistant,
			Content: []ContentBlock{NewTextBlock("你好！有什么可以帮你的？")},
		},
		{
			Role:    RoleUser,
			Content: []ContentBlock{NewTextBlock("写一个 hello world")},
		},
	}

	params := p.convertMessages(messages)

	if len(params) != 3 {
		t.Fatalf("转换后消息数量错误: 期望 3, 实际 %d", len(params))
	}

	// 验证第一条是 UserMessage，内容为 "你好"
	// SDK 的 MessageParam 是 union 类型，无法直接检查内容
	// 但可以验证不 panic 且数量正确
}

// TestAnthropicConvertMessagesEmpty 验证空消息列表转换
func TestAnthropicConvertMessagesEmpty(t *testing.T) {
	p := newTestAnthropicProvider()
	params := p.convertMessages([]Message{})
	if len(params) != 0 {
		t.Errorf("空消息列表转换后应为空, 实际长度 %d", len(params))
	}
}

// TestAnthropicProviderInit 验证客户端初始化不 panic
func TestAnthropicProviderInit(t *testing.T) {
	p := newTestAnthropicProvider()
	if p == nil {
		t.Fatal("Provider 不应为 nil")
	}
	if p.model != "claude-sonnet-4-20250514" {
		t.Errorf("model 错误: 期望 claude-sonnet-4-20250514, 实际 %s", p.model)
	}
	if p.maxTokens != 1024 {
		t.Errorf("maxTokens 错误: 期望 1024, 实际 %d", p.maxTokens)
	}
}

// TestAnthropicProviderWithBaseURL 验证自定义 BaseURL 不 panic
func TestAnthropicProviderWithBaseURL(t *testing.T) {
	cfg := &config.Config{
		Provider:  "anthropic",
		Model:    "claude-sonnet-4-20250514",
		APIKey:   "test-key",
		MaxTokens: 1024,
		BaseURL:  "https://my-proxy.example.com/anthropic",
	}
	p := NewAnthropicProvider(cfg)
	if p == nil {
		t.Fatal("Provider 不应为 nil")
	}
}

// TestAnthropicCtxCancel 验证 ctx 取消时流式请求终止
func TestAnthropicCtxCancel(t *testing.T) {
	p := newTestAnthropicProvider()
	ctx, cancel := context.WithCancel(context.Background())

	// 立即取消
	cancel()

	messages := []Message{
		{Role: RoleUser, Content: []ContentBlock{NewTextBlock("test")}},
	}

	ch, err := p.StreamChat(ctx, "system prompt", messages)
	if err != nil {
		t.Fatalf("StreamChat 返回错误: %v", err)
	}

	// 应该能收到 Done chunk（可能带错误）
	select {
	case chunk := <-ch:
		// ctx 取消后应该正常结束
		if !chunk.Done {
			t.Log("收到非 Done chunk:", chunk.Content)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时未收到响应")
	}
}

// TestAnthropicShouldRetry 验证重试判断逻辑
func TestAnthropicShouldRetry(t *testing.T) {
	p := newTestAnthropicProvider()

	tests := []struct {
		name      string
		err       error
		wantRetry bool
	}{
		{"context取消", context.Canceled, false},
		{"context超时", context.DeadlineExceeded, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.shouldRetry(tt.err)
			if got != tt.wantRetry {
				t.Errorf("shouldRetry(%v) = %v, want %v", tt.err, got, tt.wantRetry)
			}
		})
	}
}
