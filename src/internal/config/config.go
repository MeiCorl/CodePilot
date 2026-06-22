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
	// Compaction 为上下文压缩配置（Step 7），控制两层压缩策略的各项阈值与总开关。
	// 留空（零值）时由 setDefaults 填充默认值；总开关默认开启，enabled=false 可整体
	// 降级为纯滑动窗口，兼容 Step 1~6 行为。
	Compaction CompactionConfig `json:"compaction,omitempty"`
	// Memory 为自动学习记忆配置（Step 8），控制记忆系统的总开关与索引注入阈值。
	// 留空（零值）时由 setDefaults 填充默认值；总开关默认开启，enabled=false 可整体
	// 降级为无记忆状态（Source 不注入、Reviewer 不触发），兼容 Step 1~7 行为。
	Memory MemoryConfig `json:"memory,omitempty"`
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

// CompactionConfig 是上下文压缩（Step 7）的配置段，对应 setting.json 中 "compaction" 对象。
//
// 两层压缩策略：
//   - 第一层「轻量预防」：单条工具结果体积超阈值时存盘 + 替换为预览（ToolResultThreshold）；
//   - 第二层「重量兜底」：整体历史逼近窗口时做结构化摘要（AutoTriggerMargin 触发）。
//
// 字段语义与默认值：
//   - Enabled：压缩总开关。默认 true；显式设 false 时整体降级为纯滑动窗口。
//     使用 *bool 指针以区分「未配置（→默认 true）」与「显式关闭（false）」——
//     Go 的 bool 零值是 false，若用值类型将无法表达「默认开启」。
//   - ToolResultThreshold：工具结果存盘阈值（token）。单个工具结果超此值、或单条消息
//     内多个工具结果合计超此值时触发存盘 + 预览替换。默认 5120。
//   - PreviewTokens：预览头部保留长度（token），存盘后内存中保留的截断预览大小。默认 500。
//   - AutoTriggerMargin：第二层自动触发余量（token）。剩余 token ≤ 此值且未熔断时触发摘要。默认 20000。
//   - ManualTargetMargin：第二层手动触发目标余量（token）。用户主动 /compact 时允许压到只留此余量
//     （比自动更激进，因用户主动要压）。默认 3000。
//   - KeepRecentTokens：近期原文保留量（token），摘要后尾部保留的原文窗口。默认 10000。
//   - KeepRecentMinMessages：近期原文最少保留条数，与 KeepRecentTokens 取较大者作为实际保留量。默认 5。
//   - BreakerThreshold：熔断阈值（摘要连续失败次数），达到后本会话停止自动压缩（允许手动重试）。默认 3。
type CompactionConfig struct {
	// Enabled 为压缩总开关；nil 视为 true（默认开启），通过 IsEnabled() 访问。
	Enabled *bool `json:"enabled,omitempty"`
	// ToolResultThreshold 为工具结果存盘阈值（token），默认 5120。
	ToolResultThreshold int `json:"tool_result_threshold,omitempty"`
	// PreviewTokens 为预览头部保留长度（token），默认 500。
	PreviewTokens int `json:"preview_tokens,omitempty"`
	// AutoTriggerMargin 为第二层自动触发余量（token），默认 20000。
	AutoTriggerMargin int `json:"auto_trigger_margin,omitempty"`
	// ManualTargetMargin 为第二层手动触发目标余量（token），默认 3000。
	ManualTargetMargin int `json:"manual_target_margin,omitempty"`
	// KeepRecentTokens 为近期原文保留量（token），默认 10000。
	KeepRecentTokens int `json:"keep_recent_tokens,omitempty"`
	// KeepRecentMinMessages 为近期原文最少保留条数，默认 5。
	KeepRecentMinMessages int `json:"keep_recent_min_messages,omitempty"`
	// BreakerThreshold 为熔断阈值（摘要连续失败次数），默认 3。
	BreakerThreshold int `json:"breaker_threshold,omitempty"`
}

// IsEnabled 返回压缩总开关的最终生效值：未配置（nil）时默认 true，否则取显式设置。
// 调用方（main.go 装配、协调器判定）统一通过本方法读取，避免到处判 nil。
func (c CompactionConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// MemoryConfig 是自动学习记忆（Step 8）的配置段，对应 setting.json 中 "memory" 对象。
//
// 记忆系统分两路消费本配置：
//   - 索引注入侧（memory Source）：IndexMaxLines / IndexMaxBytes 控制合并后的两级
//     MEMORY.md 索引注入 LeadUserMessage 时的体积上限，超限截断防撑爆上下文；
//   - 后台回顾侧（Reviewer）：Enabled 总开关控制是否触发回顾 LLM 调用。
//
// 字段语义与默认值：
//   - Enabled：记忆总开关。默认 true；显式设 false 时整体降级为无记忆状态
//     （Source 不注入索引、Reviewer 不触发回顾）。
//     使用 *bool 指针以区分「未配置（→默认 true）」与「显式关闭（false）」——
//     Go 的 bool 零值是 false，若用值类型将无法表达「默认开启」，与 CompactionConfig 同理。
//   - IndexMaxLines：索引注入的行数上限。合并后的索引文本超过此行数时截断。默认 200。
//   - IndexMaxBytes：索引注入的字节上限。截断后的文本超过此字节数时再按字节截断。默认 25KB。
//   - ReviewModel：回顾 LLM 专用模型（预留字段）。首版固定复用主 provider/主模型，
//     不实现运行时热切换；此处仅作配置占位，便于后续扩展（见 spec Out of Scope）。
type MemoryConfig struct {
	// Enabled 为记忆总开关；nil 视为 true（默认开启），通过 IsEnabled() 访问。
	Enabled *bool `json:"enabled,omitempty"`
	// IndexMaxLines 为索引注入的行数上限，默认 200。
	IndexMaxLines int `json:"index_max_lines,omitempty"`
	// IndexMaxBytes 为索引注入的字节上限，默认 25KB（25600 字节）。
	IndexMaxBytes int `json:"index_max_bytes,omitempty"`
	// ReviewModel 为回顾专用模型预留字段，首版不启用热切换。
	ReviewModel string `json:"review_model,omitempty"`
}

// IsEnabled 返回记忆总开关的最终生效值：未配置（nil）时默认 true，否则取显式设置。
// 调用方（main.go 装配 Source/Reviewer）统一通过本方法读取，避免到处判 nil。
func (m MemoryConfig) IsEnabled() bool {
	if m.Enabled == nil {
		return true
	}
	return *m.Enabled
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

	// ---- Step 7 上下文压缩默认值 ----
	// 数值字段在 setDefaults 里用「==0 填默认」模式；Enabled 是 *bool，
	// 用下方的布尔常量取址填充，以表达「未配置 → 默认开启」。
	defaultCompactionEnabled             = true
	defaultCompactionToolResultThreshold = 5120  // 工具结果存盘阈值（5K token）
	defaultCompactionPreviewTokens       = 500   // 预览头部保留（token）
	defaultCompactionAutoTriggerMargin   = 20000 // 第二层自动触发余量（token）
	defaultCompactionManualTargetMargin  = 3000  // 第二层手动触发目标余量（token）
	defaultCompactionKeepRecentTokens    = 10000 // 近期原文保留量（token）
	defaultCompactionKeepRecentMinMsgs   = 5     // 近期原文最少保留条数
	defaultCompactionBreakerThreshold    = 3     // 熔断阈值（连续失败次数）

	// ---- Step 8 自动学习记忆默认值 ----
	// 与 Compaction 同模式：数值字段「==0 填默认」，Enabled 是 *bool 用下方布尔常量取址填充，
	// 以表达「未配置 → 默认开启」。
	defaultMemoryEnabled       = true
	defaultMemoryIndexMaxLines = 200       // 索引注入行数上限
	defaultMemoryIndexMaxBytes = 25 * 1024 // 索引注入字节上限（25KB）
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
	applyCompactionDefaults(&c.Compaction)
	applyMemoryDefaults(&c.Memory)
}

// applyCompactionDefaults 为 Compaction 配置段填充默认值。
//
// 单独抽成函数以便 config 包测试直接调用（无需构造完整 Config）。
// 数值字段沿用「==0 填默认」的既有模式；Enabled 作为 *bool，nil 时填默认 true，
// 从而区分「用户未配置（→开启）」与「用户显式 enabled=false（→关闭）」。
func applyCompactionDefaults(c *CompactionConfig) {
	if c == nil {
		return
	}
	if c.Enabled == nil {
		on := defaultCompactionEnabled
		c.Enabled = &on
	}
	if c.ToolResultThreshold == 0 {
		c.ToolResultThreshold = defaultCompactionToolResultThreshold
	}
	if c.PreviewTokens == 0 {
		c.PreviewTokens = defaultCompactionPreviewTokens
	}
	if c.AutoTriggerMargin == 0 {
		c.AutoTriggerMargin = defaultCompactionAutoTriggerMargin
	}
	if c.ManualTargetMargin == 0 {
		c.ManualTargetMargin = defaultCompactionManualTargetMargin
	}
	if c.KeepRecentTokens == 0 {
		c.KeepRecentTokens = defaultCompactionKeepRecentTokens
	}
	if c.KeepRecentMinMessages == 0 {
		c.KeepRecentMinMessages = defaultCompactionKeepRecentMinMsgs
	}
	if c.BreakerThreshold == 0 {
		c.BreakerThreshold = defaultCompactionBreakerThreshold
	}
}

// applyMemoryDefaults 为 Memory 配置段填充默认值。
//
// 单独抽成函数以便 config 包测试与 MergeMemory 直接调用（无需构造完整 Config）。
// 数值字段沿用「==0 填默认」的既有模式；Enabled 作为 *bool，nil 时填默认 true，
// 从而区分「用户未配置（→开启）」与「用户显式 enabled=false（→关闭）」。
func applyMemoryDefaults(m *MemoryConfig) {
	if m == nil {
		return
	}
	if m.Enabled == nil {
		on := defaultMemoryEnabled
		m.Enabled = &on
	}
	if m.IndexMaxLines == 0 {
		m.IndexMaxLines = defaultMemoryIndexMaxLines
	}
	if m.IndexMaxBytes == 0 {
		m.IndexMaxBytes = defaultMemoryIndexMaxBytes
	}
	// ReviewModel 为预留字段，首版不填默认（空串即「复用主 provider/主模型」）。
}

// MergeMemory 合并全局与项目级 memory 配置，返回填好默认值的最终生效配置。
//
// 多层合并机制沿用 Step 5 权限系统的「项目级覆盖全局」语义（见 security.LoadPermissions），
// 区别在于 memory 为标量配置，故做【字段级覆盖】而非列表拼接：
//   - Enabled：项目级显式设置（非 nil）时覆盖全局，否则沿用全局；
//   - IndexMaxLines / IndexMaxBytes：项目级显式设置（非 0）时覆盖全局，否则沿用全局；
//   - ReviewModel：项目级显式设置（非空）时覆盖全局，否则沿用全局。
//
// [Why 必须传原始解析值] 合并依据「是否显式配置」判断覆盖——Enabled 用 *bool 的 nil、
// 数值用 0、字符串用空串来识别「该层未配置此项」。因此调用方必须传入 JSON 解析后、
// 【尚未 applyMemoryDefaults】的原始值；若传入已填默认的值，默认值会被误判为「显式配置」
// 而错误覆盖（如项目级未配 enabled 被 setDefaults 填成默认 true，会覆盖全局显式 false）。
// 本函数内部在合并完成后调用 applyMemoryDefaults 填充最终默认值，调用方无需再填。
//
// 参数 global 为全局 memory 配置原始值，project 为项目级原始值（可为零值，表示无项目级配置）。
func MergeMemory(global, project MemoryConfig) MemoryConfig {
	merged := global
	if project.Enabled != nil {
		merged.Enabled = project.Enabled
	}
	if project.IndexMaxLines != 0 {
		merged.IndexMaxLines = project.IndexMaxLines
	}
	if project.IndexMaxBytes != 0 {
		merged.IndexMaxBytes = project.IndexMaxBytes
	}
	if project.ReviewModel != "" {
		merged.ReviewModel = project.ReviewModel
	}
	applyMemoryDefaults(&merged)
	return merged
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
