package llm

import (
	"context"
	"testing"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/config"
)

// newTestOpenAIProvider 创建测试用 OpenAIProvider（不调用真实 API）
func newTestOpenAIProvider() *OpenAIProvider {
	cfg := &config.Config{
		Provider:   "openai",
		Model:     "gpt-4o",
		APIKey:    "sk-test-key",
		MaxTokens: 1024,
		Timeout:   5,
		MaxRetries: 1,
	}
	return NewOpenAIProvider(cfg)
}

// TestOpenAIConvertMessages 验证消息格式转换
func TestOpenAIConvertMessages(t *testing.T) {
	p := newTestOpenAIProvider()

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
}

// TestOpenAIConvertMessagesEmpty 验证空消息列表转换
func TestOpenAIConvertMessagesEmpty(t *testing.T) {
	p := newTestOpenAIProvider()
	params := p.convertMessages([]Message{})
	if len(params) != 0 {
		t.Errorf("空消息列表转换后应为空, 实际长度 %d", len(params))
	}
}

// TestOpenAIProviderInit 验证客户端初始化不 panic
func TestOpenAIProviderInit(t *testing.T) {
	p := newTestOpenAIProvider()
	if p == nil {
		t.Fatal("Provider 不应为 nil")
	}
	if p.model != "gpt-4o" {
		t.Errorf("model 错误: 期望 gpt-4o, 实际 %s", p.model)
	}
	if p.maxTokens != 1024 {
		t.Errorf("maxTokens 错误: 期望 1024, 实际 %d", p.maxTokens)
	}
}

// TestOpenAIProviderWithBaseURL 验证自定义 BaseURL 不 panic
func TestOpenAIProviderWithBaseURL(t *testing.T) {
	cfg := &config.Config{
		Provider:  "openai",
		Model:    "gpt-4o",
		APIKey:   "test-key",
		MaxTokens: 1024,
		BaseURL:  "https://my-proxy.example.com/openai",
	}
	p := NewOpenAIProvider(cfg)
	if p == nil {
		t.Fatal("Provider 不应为 nil")
	}
}

// TestOpenAICtxCancel 验证 ctx 取消时流式请求终止
func TestOpenAICtxCancel(t *testing.T) {
	p := newTestOpenAIProvider()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	messages := []Message{
		{Role: RoleUser, Content: []ContentBlock{NewTextBlock("test")}},
	}

	ch, err := p.StreamChat(ctx, "system prompt", messages)
	if err != nil {
		t.Fatalf("StreamChat 返回错误: %v", err)
	}

	select {
	case chunk := <-ch:
		if !chunk.Done {
			t.Log("收到非 Done chunk:", chunk.Content)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时未收到响应")
	}
}

// TestOpenAIShouldRetry 验证重试判断逻辑
func TestOpenAIShouldRetry(t *testing.T) {
	p := newTestOpenAIProvider()

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
