// Package config 负责 CodePilot 的配置文件加载与校验。
// 配置文件路径为 ~/codepilot/config.json，包含 LLM 供应商、模型、密钥等信息。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config 是 CodePilot 的全局配置结构体。
// 字段与 ~/codepilot/config.json 一一对应。
type Config struct {
	// Provider 为 LLM 供应商名称，合法值："anthropic"、"openai"
	Provider string `json:"provider"`
	// Model 为模型名称，如 "claude-sonnet-4-20250514"、"gpt-4o"
	Model string `json:"model"`
	// BaseURL 为模型 API 地址，留空使用供应商默认地址
	BaseURL string `json:"base_url,omitempty"`
	// APIKey 为模型访问密钥
	APIKey string `json:"api_key"`
	// MaxTokens 为单次最大输出 token 数
	MaxTokens int `json:"max_tokens"`
	// Timeout 为请求超时秒数，默认 60
	Timeout int `json:"timeout,omitempty"`
	// MaxRetries 为最大重试次数，默认 2
	MaxRetries int `json:"max_retries,omitempty"`
}

// 合法的供应商列表
var supportedProviders = map[string]bool{
	"anthropic": true,
	"openai":    true,
}

const (
	defaultTimeout    = 60
	defaultMaxRetries = 2
)

// Load 从 ~/.codepilot/config.json 加载配置文件。
// 加载后填充默认值并校验必填字段和合法值。
// 文件不存在时返回友好提示错误。
func Load() (*Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("获取用户主目录失败: %w", err)
	}

	configPath := filepath.Join(homeDir, ".codepilot", "config.json")
	return LoadFromPath(configPath)
}

// LoadFromPath 从指定路径加载配置文件，供测试使用。
func LoadFromPath(configPath string) (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("配置文件不存在: %s\n请创建配置文件，可参考项目根目录 config/config.example.json", configPath)
		}
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败（请检查 JSON 格式）: %w", err)
	}

	// 填充默认值
	cfg.setDefaults()

	// 校验配置
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// setDefaults 为可选字段设置默认值。
func (c *Config) setDefaults() {
	if c.Timeout == 0 {
		c.Timeout = defaultTimeout
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = defaultMaxRetries
	}
}

// validate 校验配置项的合法性。
func (c *Config) validate() error {
	if c.Provider == "" {
		return fmt.Errorf("配置校验失败: provider 不能为空")
	}
	if !supportedProviders[c.Provider] {
		return fmt.Errorf("配置校验失败: 不支持的供应商 \"%s\"，当前支持: anthropic, openai", c.Provider)
	}
	if c.Model == "" {
		return fmt.Errorf("配置校验失败: model 不能为空")
	}
	if c.APIKey == "" {
		return fmt.Errorf("配置校验失败: api_key 不能为空")
	}
	if c.MaxTokens <= 0 {
		return fmt.Errorf("配置校验失败: max_tokens 必须大于 0")
	}
	return nil
}
