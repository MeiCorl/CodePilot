// Package config 负责 CodePilot 的配置文件加载与校验。
// 配置文件路径为 ~/.codepilot/setting.json，包含 LLM 供应商、模型、密钥等信息。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config 是 CodePilot 的全局配置结构体。
// 字段与 ~/.codepilot/setting.json 一一对应。
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
	// Timeout 为请求超时秒数，默认 180
	Timeout int `json:"timeout,omitempty"`
	// MaxRetries 为最大重试次数，默认 2
	MaxRetries int `json:"max_retries,omitempty"`
	// Tools 控制工具系统的启用与开关
	Tools ToolsConfig `json:"tools"`
	// ToolExecutionTimeoutSeconds 为单次工具执行的超时秒数，默认 30
	ToolExecutionTimeoutSeconds int `json:"tool_execution_timeout_seconds,omitempty"`
	// ToolWorkingDirectory 为工具系统的沙箱根目录；留空则取进程启动时的工作目录
	ToolWorkingDirectory string `json:"tool_working_directory,omitempty"`
	// MaxAgentLoopIterations 为 Agent Loop 最大迭代次数，默认 50。
	// 一次迭代 = 一次 LLM 调用 + 可能的工具执行；达到上限后注入提示让模型优雅收尾。
	MaxAgentLoopIterations int `json:"max_agent_loop_iterations,omitempty"`
	// ContextWindowSize 为模型上下文窗口总大小（token 数），默认 200000。
	// 用于 AgentLoop 溢出检查和前端状态栏展示。切换不同模型时可按需调整。
	ContextWindowSize int `json:"context_window_size,omitempty"`
	// ContextSafetyMargin 为上下文安全余量（token 数），默认 4096。
	// 当剩余 token 低于此值时，Agent Loop 注入提示让模型总结当前进展并回复用户。
	ContextSafetyMargin int `json:"context_safety_margin,omitempty"`
	// Permissions 为权限系统配置，控制工具调用的安全策略。
	// 留空等效于 mode=default 且无自定义规则，向后兼容旧配置。
	Permissions PermissionsConfig `json:"permissions,omitempty"`
	// MCP 为 MCP（Model Context Protocol）客户端配置，控制外部工具服务器连接。
	// 留空等效于未启用 MCP,CodePilot 仅暴露 6 个内置工具,不影响 Step 1~5 已有的功能。
	MCP MCPConfig `json:"mcp,omitempty"`
}

// MCPConfig 是 MCP 客户端的整体配置段,对应 setting.json 中 "mcp" 对象。
//
// 设计要点:
//   - Servers 为单个 server 列表(逐步替代 setting.json 旧的平铺 mcp.servers 写法),
//     当前 Task 8 直接使用本字段
//   - HandshakeTimeoutSeconds 为单 server 握手超时,留空回退到 30s
//   - ListToolsCacheTTLSeconds 为 ListToolsCached 的 TTL,留空回退到 60s
type MCPConfig struct {
	// Servers 为已声明的 MCP server 列表,启动时由 main.go 并发建连。
	// 单 server 失败仅记日志,不影响其他 server 与 CodePilot 启动。
	Servers []MCPServerConfig `json:"servers,omitempty"`
	// HandshakeTimeoutSeconds 单 server 握手超时(Connect+Initialize+ListTools 总耗时)。
	// 0 等效于 30s,与 setting.json 工具执行超时默认值对齐。
	HandshakeTimeoutSeconds int `json:"handshake_timeout_seconds,omitempty"`
	// ListToolsCacheTTLSeconds tools/list 缓存时长,0 等效于 60s。
	// 在 Agent Loop 高频会话刷新场景下减少远端 RPC,过长则 server 端动态新增工具感知延迟。
	ListToolsCacheTTLSeconds int `json:"list_tools_cache_ttl_seconds,omitempty"`
}

// MCPServerConfig 是单个 MCP server 的配置结构,对应 setting.json 中
// mcp.servers[] 单元素。
//
// 字段说明(按 MCP 2025-03-26 规范 + Anthropic 官方 Claude Desktop 实践):
//   - Type: 传输类型,合法值 "stdio" / "http"
//   - Command/Args/Env: stdio 用,启动子进程
//   - URL/Headers: http 用,Streamable HTTP 端点
//   - Timeout: 单次 RPC 超时(秒),0 视为 30s
//   - Disabled: 临时禁用,启动时跳过该 server(不报错)
type MCPServerConfig struct {
	// Name server 唯一标识(用于查找/日志/WebUI 状态栏)。
	// 必填,重复时启动期记 warn 跳过同名后者。
	Name string `json:"name"`
	// Type 传输类型: "stdio" / "http"。
	Type string `json:"type"`
	// Command stdio 用,要启动的可执行文件路径(必填,Type=stdio 时校验)。
	Command string `json:"command,omitempty"`
	// Args stdio 用,命令行参数。
	Args []string `json:"args,omitempty"`
	// Env stdio 用,注入到子进程环境变量(同名键覆盖父进程 os.Environ 值)。
	Env map[string]string `json:"env,omitempty"`
	// URL http 用,Streamable HTTP 端点 URL(必填,Type=http 时校验)。
	URL string `json:"url,omitempty"`
	// Headers http 用,额外请求头(如 Authorization 透传 Bearer Token 等)。
	Headers map[string]string `json:"headers,omitempty"`
	// Timeout 单次 RPC 超时(秒),0 视为 30s。
	Timeout int `json:"timeout,omitempty"`
	// Disabled 临时禁用,启动时跳过该 server(不建连、不报错)。
	Disabled bool `json:"disabled,omitempty"`
}

// ToolsConfig 是工具系统的配置项。
//
// Enabled 列表为空时视为"启用全部已注册工具"；否则按 Name 白名单过滤。
// 未在白名单中、但已注册的工具既不会发给 LLM，也不会被 ToolHandler 执行。
type ToolsConfig struct {
	// Enabled 为启用的工具名白名单，Name 必须与 Tool.Name() 一致
	Enabled []string `json:"enabled,omitempty"`
}

// PermissionsConfig 是权限系统的配置项，对应 setting.json 中 "permissions" 对象。
// 支持在全局配置（~/.codepilot/setting.json）和项目级配置（<cwd>/.codepilot/setting.json）中声明。
type PermissionsConfig struct {
	// Mode 为权限模式：strict / default / permissive。空字符串等效于 "default"。
	Mode string `json:"mode,omitempty"`
	// Rules 为自定义规则列表，按列表顺序匹配，命中第一条即返回。
	Rules []RuleConfig `json:"rules,omitempty"`
}

// RuleConfig 对应 setting.json 中单条权限规则的 JSON 结构。
type RuleConfig struct {
	// Tool 为目标工具名（大驼峰，如 Bash、WriteFile），"*" 匹配所有工具。
	Tool string `json:"tool"`
	// Pattern 为参数匹配模式（路径 glob 或 Bash 命令前缀），"*" 匹配所有参数。
	Pattern string `json:"pattern"`
	// Action 为命中后的动作：allow / deny / ask。
	Action string `json:"action"`
	// Reason 为可选的可读说明，用于日志和 HITL 对话框展示。
	Reason string `json:"reason,omitempty"`
}

// 合法的供应商列表
var supportedProviders = map[string]bool{
	"anthropic": true,
	"openai":    true,
}

const (
	defaultTimeout                 = 180
	defaultMaxRetries              = 2
	defaultToolExecutionTimeoutSec = 30
	defaultMaxAgentLoopIterations  = 50
	defaultContextWindowSize       = 200000
	defaultContextSafetyMargin     = 4096
	defaultMaxTokens               = 16384
)

// Load 从 ~/.codepilot/setting.json 加载配置文件。
// 加载后填充默认值并校验必填字段和合法值。
// 文件不存在时返回友好提示错误。
func Load() (*Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("获取用户主目录失败: %w", err)
	}

	configPath := filepath.Join(homeDir, ".codepilot", "setting.json")
	return LoadFromPath(configPath)
}

// LoadFromPath 从指定路径加载配置文件，供测试使用。
func LoadFromPath(configPath string) (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("配置文件不存在: %s\n请创建配置文件，可参考项目根目录 config/setting.example.json", configPath)
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
	if c.MaxTokens == 0 {
		c.MaxTokens = defaultMaxTokens
	}
	if c.Timeout == 0 {
		c.Timeout = defaultTimeout
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = defaultMaxRetries
	}
	if c.ToolExecutionTimeoutSeconds == 0 {
		c.ToolExecutionTimeoutSeconds = defaultToolExecutionTimeoutSec
	}
	if c.MaxAgentLoopIterations == 0 {
		c.MaxAgentLoopIterations = defaultMaxAgentLoopIterations
	}
	if c.ContextWindowSize == 0 {
		c.ContextWindowSize = defaultContextWindowSize
	}
	if c.ContextSafetyMargin == 0 {
		c.ContextSafetyMargin = defaultContextSafetyMargin
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
	if c.MaxAgentLoopIterations < 0 {
		return fmt.Errorf("配置校验失败: max_agent_loop_iterations 不能为负数")
	}
	if c.ContextWindowSize < 0 {
		return fmt.Errorf("配置校验失败: context_window_size 不能为负数")
	}
	if c.ContextSafetyMargin < 0 {
		return fmt.Errorf("配置校验失败: context_safety_margin 不能为负数")
	}

	// MCP 配置校验:仅校验关键字段,具体传输类型在 mcp/config.BuildTransports 阶段构造
	if err := ValidateMCPConfig(&c.MCP); err != nil {
		return err
	}
	return nil
}

// ValidateMCPConfig 校验 mcp.servers 中每条 server 声明的最小合法性。
//
// 导出以方便 mcp/config 包的测试调用;运行时由 c.validate() 内部自动调用。
//
// 不在此处构造 transport(避免 config 包依赖 transport 包),仅做"键值存在"和
// "type 合法"两层校验;详细校验(如 stdio 的 command 路径是否存在)由
// mcp/config.BuildTransports 在启动期完成。
func ValidateMCPConfig(m *MCPConfig) error {
	if m == nil || len(m.Servers) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(m.Servers))
	for i, s := range m.Servers {
		if s.Disabled {
			continue // 禁用的 server 跳过校验
		}
		if s.Name == "" {
			return fmt.Errorf("配置校验失败: mcp.servers[%d].name 不能为空", i)
		}
		if _, dup := seen[s.Name]; dup {
			return fmt.Errorf("配置校验失败: mcp.servers[%d].name=%q 重复声明", i, s.Name)
		}
		seen[s.Name] = struct{}{}

		switch s.Type {
		case "stdio":
			if s.Command == "" {
				return fmt.Errorf("配置校验失败: mcp.servers[%d] (name=%q) type=stdio 必须填写 command", i, s.Name)
			}
		case "http":
			if s.URL == "" {
				return fmt.Errorf("配置校验失败: mcp.servers[%d] (name=%q) type=http 必须填写 url", i, s.Name)
			}
		case "":
			return fmt.Errorf("配置校验失败: mcp.servers[%d] (name=%q) 缺少 type(必须是 stdio/http)", i, s.Name)
		default:
			return fmt.Errorf("配置校验失败: mcp.servers[%d] (name=%q) 不支持的 type=%q(仅支持 stdio/http)", i, s.Name, s.Type)
		}
	}
	return nil
}
