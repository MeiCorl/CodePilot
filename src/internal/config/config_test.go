package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSetDefaults 验证可选字段使用默认值
func TestSetDefaults(t *testing.T) {
	cfg := &Config{
		Provider:   "anthropic",
		Model:     "claude-sonnet-4-20250514",
		APIKey:    "test-key",
		MaxTokens: 4096,
	}
	cfg.setDefaults()

	if cfg.Timeout != defaultTimeout {
		t.Errorf("Timeout 默认值错误: 期望 %d, 实际 %d", defaultTimeout, cfg.Timeout)
	}
	if cfg.MaxRetries != defaultMaxRetries {
		t.Errorf("MaxRetries 默认值错误: 期望 %d, 实际 %d", defaultMaxRetries, cfg.MaxRetries)
	}
}

// TestLoadFromPathSuccess 验证正常加载完整配置
func TestLoadFromPathSuccess(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	content, _ := json.Marshal(Config{
		Provider:   "openai",
		Model:     "gpt-4o",
		APIKey:    "sk-test",
		MaxTokens: 4096,
		Timeout:   30,
	})
	os.WriteFile(cfgPath, content, 0644)

	cfg, err := LoadFromPath(cfgPath)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.Provider != "openai" {
		t.Errorf("Provider 错误: 期望 openai, 实际 %s", cfg.Provider)
	}
	if cfg.Timeout != 30 {
		t.Errorf("Timeout 错误: 期望 30, 实际 %d", cfg.Timeout)
	}
}

// TestLoadFromPathDefaults 验证不填写可选字段时使用默认值
func TestLoadFromPathDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	// 不填写 Timeout 和 MaxRetries
	content, _ := json.Marshal(map[string]any{
		"provider":   "anthropic",
		"model":     "claude-sonnet-4-20250514",
		"api_key":   "sk-test",
		"max_tokens": 4096,
	})
	os.WriteFile(cfgPath, content, 0644)

	cfg, err := LoadFromPath(cfgPath)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.Timeout != 60 {
		t.Errorf("Timeout 默认值错误: 期望 60, 实际 %d", cfg.Timeout)
	}
	if cfg.MaxRetries != 2 {
		t.Errorf("MaxRetries 默认值错误: 期望 2, 实际 %d", cfg.MaxRetries)
	}
}

// TestLoadFromPathNotFound 验证文件不存在时的错误提示
func TestLoadFromPathNotFound(t *testing.T) {
	_, err := LoadFromPath("/nonexistent/path/config.json")
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	msg := err.Error()
	if len(msg) == 0 {
		t.Error("错误消息为空")
	}
}

// TestValidateUnsupportedProvider 验证不支持的供应商报错
func TestValidateUnsupportedProvider(t *testing.T) {
	cfg := &Config{
		Provider:   "gemini",
		Model:     "gemini-pro",
		APIKey:    "test",
		MaxTokens: 4096,
	}
	err := cfg.validate()
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
}
