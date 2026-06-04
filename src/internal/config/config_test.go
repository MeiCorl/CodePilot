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
	if cfg.ToolExecutionTimeoutSeconds != defaultToolExecutionTimeoutSec {
		t.Errorf("ToolExecutionTimeoutSeconds 默认值错误: 期望 %d, 实际 %d",
			defaultToolExecutionTimeoutSec, cfg.ToolExecutionTimeoutSeconds)
	}
	if cfg.ToolWorkingDirectory != "" {
		t.Errorf("ToolWorkingDirectory 默认值应为空字符串, 实际 %q", cfg.ToolWorkingDirectory)
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
	if cfg.ToolExecutionTimeoutSeconds != 30 {
		t.Errorf("ToolExecutionTimeoutSeconds 默认值错误: 期望 30, 实际 %d", cfg.ToolExecutionTimeoutSeconds)
	}
	if cfg.ToolWorkingDirectory != "" {
		t.Errorf("ToolWorkingDirectory 默认值应为空, 实际 %q", cfg.ToolWorkingDirectory)
	}
	if len(cfg.Tools.Enabled) != 0 {
		t.Errorf("Tools.Enabled 默认应为空, 实际 %v", cfg.Tools.Enabled)
	}
}

// TestLoadFromPathWithToolsConfig 验证 tools 段被正确解析。
func TestLoadFromPathWithToolsConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	content, _ := json.Marshal(map[string]any{
		"provider":                      "anthropic",
		"model":                         "claude-sonnet-4-20250514",
		"api_key":                       "sk-test",
		"max_tokens":                    4096,
		"tools":                         map[string]any{"enabled": []string{"ReadFile", "Bash"}},
		"tool_execution_timeout_seconds": 5,
		"tool_working_directory":        "f:/CodePilot",
	})
	os.WriteFile(cfgPath, content, 0644)

	cfg, err := LoadFromPath(cfgPath)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if got, want := cfg.Tools.Enabled, []string{"ReadFile", "Bash"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Tools.Enabled 错误: 期望 %v, 实际 %v", want, got)
	}
	if cfg.ToolExecutionTimeoutSeconds != 5 {
		t.Errorf("ToolExecutionTimeoutSeconds 错误: 期望 5, 实际 %d", cfg.ToolExecutionTimeoutSeconds)
	}
	if cfg.ToolWorkingDirectory != "f:/CodePilot" {
		t.Errorf("ToolWorkingDirectory 错误: 期望 f:/CodePilot, 实际 %q", cfg.ToolWorkingDirectory)
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
