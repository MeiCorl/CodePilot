package llm

import (
	"testing"

	"github.com/MeiCorl/CodePilot/src/internal/config"
)

// TestNewProviderAnthropic 验证工厂函数按配置返回 Anthropic 实例
func TestNewProviderAnthropic(t *testing.T) {
	cfg := &config.Config{
		Provider: "anthropic",
		Model:   "claude-sonnet-4-20250514",
		APIKey:  "test",
	}
	p, err := NewProvider(cfg)
	if err != nil {
		t.Fatalf("创建 Anthropic Provider 失败: %v", err)
	}
	if _, ok := p.(*AnthropicProvider); !ok {
		t.Error("未返回 *AnthropicProvider 类型")
	}
}

// TestNewProviderOpenAI 验证工厂函数按配置返回 OpenAI 实例
func TestNewProviderOpenAI(t *testing.T) {
	cfg := &config.Config{
		Provider: "openai",
		Model:   "gpt-4o",
		APIKey:  "test",
	}
	p, err := NewProvider(cfg)
	if err != nil {
		t.Fatalf("创建 OpenAI Provider 失败: %v", err)
	}
	if _, ok := p.(*OpenAIProvider); !ok {
		t.Error("未返回 *OpenAIProvider 类型")
	}
}

// TestNewProviderUnsupported 验证不支持的供应商返回错误
func TestNewProviderUnsupported(t *testing.T) {
	cfg := &config.Config{
		Provider: "gemini",
		Model:   "gemini-pro",
		APIKey:  "test",
	}
	_, err := NewProvider(cfg)
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
}

// TestProviderInterface 验证两个适配器均实现 Provider 接口
func TestProviderInterface(t *testing.T) {
	cfg := &config.Config{APIKey: "test"}
	var _ Provider = NewAnthropicProvider(cfg)
	var _ Provider = NewOpenAIProvider(cfg)
}
