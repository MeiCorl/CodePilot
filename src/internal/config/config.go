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
	// Skill 为 Skill 系统配置（Step 10），控制 Skill 三档加载的总开关与单文件
	// 正文截断上限。留空（零值）时由 setDefaults 填充默认值；总开关默认开启，
	// enabled=false 时 main.go 完全跳过 Skill 加载（不调 LoadAll、不注册
	// use_skill 工具、不注入 SkillsIndexSource、不注册 Skill 类 slash 命令），
	// 但 /skills client 命令仍注册（前端可拉到空 Skill 列表）。
	Skill SkillConfig `json:"skill,omitempty"`
	// Hook 为 Hook 系统配置（Step 11），控制 Hook 引擎的总开关与 entries 列表。
	// 留空（零值）时由 setDefaults 填充默认值；总开关默认开启，
	// enabled=false 时 Hook 引擎完全跳过（不加载配置、不注册事件、不影响主流程）。
	// 不存在 hooks 段时等同于 enabled=true + entries=[]（零配置安全降级）。
	Hook HookConfig `json:"hook,omitempty"`
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

// SkillConfig 是 Skill 系统（Step 10）的配置段，对应 setting.json 中 "skill" 对象。
//
// 字段语义与默认值：
//   - Enabled：Skill 系统总开关。默认 true；显式设 false 时 main.go 完全跳过
//     Skill 加载（不调 LoadAll、不注册 use_skill 工具、不注入 SkillsIndexSource、
//     不注册 Skill 类 slash 命令）；但 /skills client 命令仍注册（前端拉到空
//     列表不影响主流程）。使用 *bool 指针以区分「未配置（→默认 true）」与
//     「显式关闭（false）」——Go 的 bool 零值是 false，若用值类型将无法表达
//     「默认开启」，与 CompactionConfig / MemoryConfig 同理。
//   - MaxSkillSizeBytes：单 Skill 的 SKILL.md 正文（不含 frontmatter）大小上限
//     （字节）。0 等效于 65536（64KB），由 applySkillDefaults 填充；通过该字段
//     截断超大 Skill 避免 use_skill 工具一次返回的 tool_result 撑爆上下文。
type SkillConfig struct {
	// Enabled 为 Skill 系统总开关；nil 视为 true（默认开启），通过 IsEnabled() 访问。
	Enabled *bool `json:"enabled,omitempty"`
	// MaxSkillSizeBytes 为单 SKILL.md 正文截断阈值（字节），0 等效 65536（64KB）。
	MaxSkillSizeBytes int `json:"max_skill_size_bytes,omitempty"`
}

// IsEnabled 返回 Skill 总开关的最终生效值：未配置（nil）时默认 true，
// 否则取显式设置。调用方（main.go 装配 LoadAll / use_skill 工具 / SkillsIndexSource）
// 统一通过本方法读取，避免到处判 nil。
func (s SkillConfig) IsEnabled() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}

// HookConfig 是 Hook 系统（Step 11）的配置段，对应 setting.json 中 "hook" 对象。
//
// 字段语义与默认值：
//   - Enabled：Hook 引擎总开关。默认 true；显式设 false 时 Hook 引擎完全跳过
//     （不加载配置、不注册事件、不影响主流程）。使用 *bool 指针以区分
//     「未配置（→默认 true）」与「显式关闭（false）」——Go 的 bool 零值是 false，
//     若用值类型将无法表达「默认开启」，与 CompactionConfig / MemoryConfig /
//     SkillConfig 同理。
//   - Entries：单条 hook 配置数组。空切片表示零配置安全降级（Hook 引擎存在但空跑，
//     启动耗时增加 < 5ms）。数组顺序敏感——同事件内按数组顺序执行。
//
// 多层合并：项目级 <cwd>/.codepilot/setting.json 与全局 ~/.codepilot/setting.json
// 沿用 Step 5/8/10 的「字段级合并」语义：
//   - Enabled：项目级显式设置（非 nil）时覆盖全局，否则沿用全局；
//   - Entries：项目级非空时整体替换（项目级 entries 完全覆盖全局 entries），
//     为空时沿用全局——这是「数组替换」而非「数组拼接」，与 Step 5 权限的「规则追加」
//     不同，原因是 hook 列表属于「事件触发器」而非「白名单规则」，整体替换语义
//     更贴近用户对「项目级 hook」的直觉。
type HookConfig struct {
	// Enabled 为 Hook 系统总开关；nil 视为 true（默认开启），通过 IsEnabled() 访问。
	Enabled *bool `json:"enabled,omitempty"`
	// Entries 为单条 hook 配置数组，顺序敏感。可为空切片（等同于零配置降级）。
	Entries []HookEntryConfig `json:"entries,omitempty"`
}

// IsEnabled 返回 Hook 总开关的最终生效值：未配置（nil）时默认 true，
// 否则取显式设置。调用方（hook.LoadFromConfig / hook.Engine.New）通过本方法
// 读取，避免到处判 nil。
func (h HookConfig) IsEnabled() bool {
	if h.Enabled == nil {
		return true
	}
	return *h.Enabled
}

// HookEntryConfig 是单条 hook 配置，对应 setting.json 中 hook.entries[] 单元素。
//
// 字段语义：
//   - Name：hook 唯一标识（同事件内可重复，按数组顺序执行；同名仅便于排错）。
//   - Event：触发事件名，必须 12 类事件之一（程序启动/退出/压缩/错误/会话开始/结束/
//     轮次开始/结束/工具前后/消息前后），由 ValidateHookConfig 校验。
//   - Condition：触发条件，可选。JSON.RawMessage 透传：保留原始结构以便 hook
//     matcher 包后续按需解析（leaf/all/any 三层结构）。nil / 空对象均视为「永远匹配」。
//   - Action：动作定义，必填。包含 Type（command/http/prompt/agent 四选一）+ type-specific
//     子字段（同样用 JSON.RawMessage 透传，由 hook/executor 包解析）。
//   - Async：异步执行标志。true 时 Engine 在 goroutine 中执行,不阻塞主 Agent Loop;
//     false 时同步阻塞,默认超时由 Engine 配置。
//   - Once：单会话内一次性触发标志。true 时同 sessionID + Name 第二次起跳过 + 记 debug 日志。
type HookEntryConfig struct {
	// Name 为 hook 唯一标识，便于排错与 once 追踪；同事件内可重复。
	Name string `json:"name"`
	// Event 为触发事件名，必须 12 类事件之一。
	Event string `json:"event"`
	// Condition 为可选触发条件（all/any/leaf），nil/空对象视为永远匹配。
	// 用 RawMessage 透传原始 JSON 以便 matcher 包解析。
	Condition *json.RawMessage `json:"condition,omitempty"`
	// Action 为动作定义，必填（Type 必须 command/http/prompt/agent 之一）。
	Action HookActionConfig `json:"action"`
	// Async 为异步执行标志，默认 false。
	Async bool `json:"async,omitempty"`
	// Once 为单会话一次性触发标志，默认 false。
	Once bool `json:"once,omitempty"`
}

// HookActionConfig 是单条 hook 的动作定义，对应 setting.json 中 hook.entries[].action。
//
// Type-specific 字段（Command / WorkingDir / Env / Timeout / Method / URL /
// Headers / Body / Text / As / Prompt / MaxIterations / AllowTools）统一用
// json.RawMessage 透传，由 hook/executor 包按 Type 分别反序列化。
// [Why RawMessage] 4 种 action 的字段差异巨大（command 用 command/working_dir/env/timeout；
// http 用 method/url/headers/body/timeout；prompt 用 text/as；agent 用 prompt/
// max_iterations/allow_tools/timeout），若用强类型字段会把 4 套字段塞进同一 struct，
// 序列化时互相干扰且不利于后续新增 action 类型。RawMessage 延迟解析到 executor 阶段，
// config 包只校验 Type 合法性。
type HookActionConfig struct {
	// Type 为动作类型，必填，必须 command/http/prompt/agent 之一。
	Type string `json:"type"`
	// Raw 透传 type-specific 字段原始 JSON，由 hook/executor 包按 Type 分支解析。
	// 序列化为 action 对应的内联对象（即展开在 action 节点下，而非嵌套 raw 字段）。
	// [Why 暴露字段] 使用 MarshalJSON/UnmarshalJSON 把 Raw 平铺进 action 对象，
	// 与 Type 同层（避免 setting.json 里出现 action: { type, raw: {...} 这种丑陋结构）。
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON 让 HookActionConfig 接受「type 与 type-specific 字段同层」的 JSON 结构。
//
// 输入示例（来自 setting.json）:
//   {
//     "type": "command",
//     "command": "echo $TOOL_INPUT_FILE_PATH",
//     "working_dir": "",
//     "env": { "NO_COLOR": "1" },
//     "timeout": "10s"
//   }
//
// [Why 自定义 Unmarshal] 标准库对带 `json:"-"` 字段的 struct 无能为力——
// 若只声明 Type 字段并把其它字段透传为 Raw,标准库会丢掉 type-specific 部分。
// 这里用「先反序列化为 map,提取 type 后把剩余部分整体打包成 Raw」的两步走,
//保证 Executor 阶段拿到完整的 type-specific 原始 JSON。
func (a *HookActionConfig) UnmarshalJSON(data []byte) error {
	// 用临时 struct 接收 type，避免 type 字段被吃进 Raw
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	a.Type = probe.Type
	// 把「整个原始 JSON」存入 Raw，由 executor 按 Type 自行再解析（保留 type 字段,
	// 方便 executor 用 map 方式访问或整体 json.Unmarshal 到专用结构体）
	a.Raw = append(a.Raw[:0], data...)
	return nil
}

// MarshalJSON 把 HookActionConfig 序列化为「type 与 type-specific 字段同层」结构。
//
// 若 Raw 为空（用户代码直接构造 HookActionConfig{Type: "command"} 未填充 Raw）,
// 仅输出 {"type":"command"}；若 Raw 非空,直接把 Raw 写出即可（因为 UnmarshalJSON
// 已经把原始 JSON 完整存进 Raw,「type」字段在 Raw 中也存在,不会丢）。
func (a HookActionConfig) MarshalJSON() ([]byte, error) {
	if len(a.Raw) == 0 {
		// 零 Raw 场景：构造一个只含 type 的最小 JSON 对象
		return json.Marshal(struct {
			Type string `json:"type"`
		}{Type: a.Type})
	}
	return a.Raw, nil
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

	// ---- Step 10 Skill 系统默认值 ----
	// 与 Compaction / Memory 同模式：数值字段「==0 填默认」，Enabled 是 *bool
	// 用下方布尔常量取址填充，以表达「未配置 → 默认开启」。
	defaultSkillEnabled          = true
	defaultSkillMaxSizeBytes     = 64 * 1024 // 单 SKILL.md 正文截断阈值（64KB）

	// ---- Step 11 Hook 系统默认值 ----
	// 与 Compaction / Memory / Skill 同模式：Enabled 是 *bool，用下方布尔常量
	// 取址填充，以表达「未配置 → 默认开启」。Entries 不需要默认常量（nil 即「空配置降级」）。
	defaultHookEnabled = true
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
	applySkillDefaults(&c.Skill)
	applyHookDefaults(&c.Hook)
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

// applySkillDefaults 为 Skill 配置段填充默认值。
//
// 单独抽成函数以便 config 包测试直接调用（无需构造完整 Config），与
// applyCompactionDefaults / applyMemoryDefaults 风格保持一致。
// 数值字段沿用「==0 填默认」的既有模式；Enabled 作为 *bool，nil 时填默认 true，
// 从而区分「用户未配置（→开启）」与「用户显式 enabled=false（→关闭）」。
func applySkillDefaults(s *SkillConfig) {
	if s == nil {
		return
	}
	if s.Enabled == nil {
		on := defaultSkillEnabled
		s.Enabled = &on
	}
	if s.MaxSkillSizeBytes == 0 {
		s.MaxSkillSizeBytes = defaultSkillMaxSizeBytes
	}
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

// applyHookDefaults 为 Hook 配置段填充默认值。
//
// 单独抽成函数以便 config 包测试直接调用（无需构造完整 Config），与
// applyCompactionDefaults / applyMemoryDefaults / applySkillDefaults 风格保持一致。
// [Why 只填 Enabled 不填 Entries] HookConfig 不像 Skill/Memory/Compaction
// 那样有「数值阈值字段」需要默认填充——hook 列表是用户自定义数组，nil 本身就
// 是合法的「零配置安全降级」状态（Engine 空跑），不需要再填空切片。
// 因此本函数只负责 Enabled 指针的 nil → 默认 true 填充。
func applyHookDefaults(h *HookConfig) {
	if h == nil {
		return
	}
	if h.Enabled == nil {
		on := defaultHookEnabled
		h.Enabled = &on
	}
}

// MergeHooks 合并全局与项目级 hook 配置，返回填好默认值的最终生效配置。
//
// 多层合并机制沿用 Step 5 权限 / Step 8 记忆 / Step 10 Skill 的「项目级覆盖全局」语义，
// 区别在于 hook.entries 是数组类型，做【整体替换】而非列表拼接：
//   - Enabled：项目级显式设置（非 nil）时覆盖全局，否则沿用全局；
//   - Entries：项目级显式设置（len > 0）时整体替换全局 entries，空时沿用全局。
//
// [Why Entries 整体替换而非拼接] hook 列表是「事件触发器」——同事件内多 hook 按顺序
// 执行，配置数组顺序敏感；若拼接，项目级追加的 hook 会出现在全局 hook 之后，破坏
// 「项目级 hook 优先级最高」的直觉，且容易出现「全局 hook 提前返回 + 项目级 hook
// 后置触发」的语义混乱。整体替换语义让用户能完全在项目级定义 hook 而无需担心
// 全局 hook 干扰，符合 Claude Code / Cursor 等同类产品的项目级覆盖行为。
//
// [Why 必须传原始解析值] 同 MergeMemory：合并依据「是否显式配置」判断覆盖——
// Enabled 用 *bool 的 nil、Entries 用 len() == 0 识别「该层未配置此项」。
// 因此调用方必须传入 JSON 解析后、【尚未 applyHookDefaults】的原始值；若传入已
// 填默认的值（Enabled 被填成默认 true），会被误判为「显式配置」而错误覆盖。
// 本函数内部在合并完成后调用 applyHookDefaults 填充最终默认值，调用方无需再填。
//
// 参数 global 为全局 hook 配置原始值，project 为项目级原始值（可为零值，表示无项目级配置）。
func MergeHooks(global, project HookConfig) HookConfig {
	merged := global
	if project.Enabled != nil {
		merged.Enabled = project.Enabled
	}
	if len(project.Entries) > 0 {
		merged.Entries = project.Entries
	}
	applyHookDefaults(&merged)
	return merged
}

// ValidateHookConfig 校验 hook 配置的合法性。
//
// 导出以方便 hook 包的测试调用；运行时由 c.validate() 内部自动调用。
//
// 校验内容：
//   - 每条 entry 的 Name 非空（用于排错与 once 追踪）；
//   - 每条 entry 的 Event 必须 12 类事件之一（program_start / program_exit /
//     compact / error / session_start / session_end / iteration_start /
//     iteration_end / pre_tool_use / post_tool_use / pre_message / post_message）；
//   - 每条 entry 的 Action.Type 必须 command/http/prompt/agent 之一；
//   - HookConfig 整体允许 Enabled=nil（applyHookDefaults 会填默认），Entries=nil（空降级）。
//
// [Why 不校验 Action type-specific 字段] 4 种 action 的字段差异巨大（command 用
// command/working_dir/env/timeout；http 用 method/url/headers/body/timeout 等），
// config 包不解析 action 内层（HookActionConfig 用 RawMessage 透传），由 hook/executor
// 包按 Type 分别反序列化时再校验。config 包只把「Type 是否合法」这一最小门槛守住。
func ValidateHookConfig(h *HookConfig) error {
	if h == nil {
		return nil
	}
	for i, e := range h.Entries {
		if e.Name == "" {
			return fmt.Errorf("配置校验失败: hook.entries[%d].name 不能为空", i)
		}
		if !isValidHookEvent(e.Event) {
			return fmt.Errorf("配置校验失败: hook.entries[%d] (name=%q) event=%q 非法(必须 12 类事件之一)",
				i, e.Name, e.Event)
		}
		switch e.Action.Type {
		case "command", "http", "prompt", "agent":
			// 合法 Type，type-specific 字段由 executor 阶段校验
		case "":
			return fmt.Errorf("配置校验失败: hook.entries[%d] (name=%q) action.type 不能为空", i, e.Name)
		default:
			return fmt.Errorf("配置校验失败: hook.entries[%d] (name=%q) action.type=%q 非法(仅支持 command/http/prompt/agent)",
				i, e.Name, e.Action.Type)
		}
	}
	return nil
}

// 12 类合法事件名常量集合——与 spec §A 表格一一对应。
// 单独维护一份而非 import hook 包（避免 config → hook 的反向依赖；hook 包可以依赖 config）。
var validHookEvents = map[string]bool{
	"program_start":    true,
	"program_exit":     true,
	"compact":          true,
	"error":            true,
	"session_start":    true,
	"session_end":      true,
	"iteration_start":  true,
	"iteration_end":    true,
	"pre_tool_use":     true,
	"post_tool_use":    true,
	"pre_message":      true,
	"post_message":     true,
}

// isValidHookEvent 判断事件名是否在 12 类合法事件集合内。
// 单独抽成函数便于 ValidateHookConfig 复用与未来扩展（若需支持自定义事件，
// 可在此处改为查外部 EventRegistry）。
func isValidHookEvent(event string) bool {
	return validHookEvents[event]
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

	// Hook 配置校验:Name 非空 + Event 合法 + Action.Type 合法
	if err := ValidateHookConfig(&c.Hook); err != nil {
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
