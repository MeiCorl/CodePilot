// Package adapter 把远端 MCP server 暴露的工具适配为 CodePilot 原生
// tool.Tool 接口实现，使 Agent Loop / 权限系统 / WebUI 展示层对"远端工具"
// 与"内置工具"完全无感。
//
// 设计要点：
//   - 单个远端工具被包装成一个 adapterTool 实例，Name() 加 mcp__<server>__
//     前缀避免与内置工具/跨 server 工具重名
//   - InputSchema 直接透传远端 JSON Schema 字节（不做语义解析，server 怎么写
//     LLM 就怎么看，最大化兼容性）
//   - Permission 一律为 PermExec：MCP 工具能力完全黑盒，按最严格策略归类，
//     由 Step 5 权限系统按 `mcp__<server>__<tool>` 前缀粒度统一管控
//   - Execute 把入参原样转发给 Session.CallTool，把返回的 MCPCallResult.Content[]
//     折叠为单条纯文本（text 类型直接拼，image/resource 输出占位标记）
//   - MCPCallResult.IsError=true 时以 error 形式上抛，让 conversation manager
//     按统一规约封装为 ToolResultBlock{is_error=true} 反馈给 LLM
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/MeiCorl/CodePilot/src/internal/mcp/session"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// ToolNamePrefix 是 MCP 工具在 CodePilot 侧统一的命名前缀。
//
// 命名形如 `mcp__<server>__<remote_tool>`：
//   - 双下划线分隔，避免与远端工具名内部的下划线混淆
//   - server 名取自 setting.json 的 mcp.servers[*].name
//   - remote_tool 取自远端 tools/list 返回的 tool.name
//
// 该规则与 Anthropic 官方 Claude Desktop / Cursor 等客户端实践一致。
const ToolNamePrefix = "mcp__"

// nameSeparator 是 server 与 tool 之间的分隔符。
const nameSeparator = "__"

// ErrEmptyServerName 在构造 adapterTool 时 server 名缺失返回。
var ErrEmptyServerName = errors.New("mcp adapter: server 名不能为空")

// ErrEmptyToolName 在构造 adapterTool 时远端工具名缺失返回。
var ErrEmptyToolName = errors.New("mcp adapter: 远端工具名不能为空")

// ErrNilSession 在构造 adapterTool 时未传入 Session 返回。
var ErrNilSession = errors.New("mcp adapter: session 不能为空")

// SessionCaller 是 adapter 依赖的 Session 子集接口，仅暴露 CallTool。
//
// 抽取为接口的好处：
//   - 测试侧可用 mock 直接构造 adapterTool，无需启动真实 transport / RPC
//   - 与 Session 解耦：未来 Session 重构（如换实现）不影响 adapter
type SessionCaller interface {
	// Name 返回 server 名，仅用于错误信息与日志，无业务依赖。
	Name() string
	// CallTool 透传到底层 Session 的 tools/call。
	CallTool(ctx context.Context, name string, arguments json.RawMessage) (*session.MCPCallResult, error)
}

// 编译期保证 *session.Session 实现 SessionCaller，避免接口漂移。
var _ SessionCaller = (*session.Session)(nil)

// BuildToolName 按 `mcp__<server>__<tool>` 格式拼装 CodePilot 侧的工具名。
//
// 即使 server 或 tool 名包含下划线也不会引入歧义：分隔符是连续双下划线，
// LLM / Registry 按完整字符串作为唯一 key 即可。
func BuildToolName(server, remoteTool string) string {
	return ToolNamePrefix + server + nameSeparator + remoteTool
}

// adapterTool 把一个远端 MCP 工具包装为 CodePilot tool.Tool。
//
// 字段说明：
//   - serverName    server 名（用于拼装 Name 与日志）
//   - remoteName    远端 server 内部的工具名（传给 tools/call）
//   - description   远端 server 给 LLM 看的描述（透传）
//   - inputSchema   远端 JSON Schema 原始字节（透传给 LLM 与 Provider）
//   - sessionCaller 实际执行 tools/call 的会话引用（一般为 *session.Session）
type adapterTool struct {
	serverName    string
	remoteName    string
	description   string
	inputSchema   json.RawMessage
	sessionCaller SessionCaller
}

// 编译期保证 adapterTool 实现 tool.Tool 接口，避免接口漂移。
var _ tool.Tool = (*adapterTool)(nil)

// NewAdapterTool 构造一个把远端 MCP 工具包装为 CodePilot tool.Tool 的实例。
//
// 入参：
//   - serverName  CodePilot 侧的 server 标识（如 "github" / "mock"）
//   - mcpTool     远端 server 通过 tools/list 返回的工具描述
//   - caller      实际执行 tools/call 的会话引用
//
// 返回：
//   - 成功时返回实现 tool.Tool 接口的实例
//   - server 名 / tool 名 / session 缺失时返回上面定义的 sentinel error
func NewAdapterTool(serverName string, mcpTool session.MCPTool, caller SessionCaller) (tool.Tool, error) {
	if strings.TrimSpace(serverName) == "" {
		return nil, ErrEmptyServerName
	}
	if strings.TrimSpace(mcpTool.Name) == "" {
		return nil, ErrEmptyToolName
	}
	if caller == nil {
		return nil, ErrNilSession
	}
	return &adapterTool{
		serverName:    serverName,
		remoteName:    mcpTool.Name,
		description:   mcpTool.Description,
		inputSchema:   normalizeSchema(mcpTool.InputSchema),
		sessionCaller: caller,
	}, nil
}

// Name 实现 tool.Tool；返回 `mcp__<server>__<tool>` 形式的 CodePilot 工具名。
func (t *adapterTool) Name() string {
	return BuildToolName(t.serverName, t.remoteName)
}

// Description 实现 tool.Tool；远端 description 直接透传，缺失时回退到固定提示。
//
// 之所以加默认提示而不是返回空串：LLM 在 tool 列表中看不到 description 时
// 倾向于不调用，影响远端能力被发现。
func (t *adapterTool) Description() string {
	if t.description == "" {
		return fmt.Sprintf("MCP 远端工具：%s/%s（远端未提供描述）", t.serverName, t.remoteName)
	}
	return t.description
}

// InputSchema 实现 tool.Tool；远端 JSON Schema 原样返回。
//
// 若远端缺失 schema（极少见），返回标准空对象 schema 避免 Provider 校验失败。
func (t *adapterTool) InputSchema() json.RawMessage {
	if len(t.inputSchema) == 0 {
		return defaultEmptySchema
	}
	return t.inputSchema
}

// Permission 实现 tool.Tool；MCP 工具能力黑盒一律 PermExec。
//
// 设计取舍：远端工具的真实副作用对 client 不可见（可能是读、写、Shell、网络），
// 用最严格档位归类，配合 Step 5 权限系统按 `mcp__<server>__<tool>` 规则
// allow / deny / ask，由用户在配置或 HITL 决策时显式放行。
func (t *adapterTool) Permission() tool.ToolPermission {
	return tool.PermExec
}

// Execute 实现 tool.Tool；把入参透传给远端 tools/call，结果折叠为单条文本。
//
// 行为约定：
//   - input 为 nil/空 → 仍发起调用，但 arguments 字段省略（兼容无参工具）
//   - MCPCallResult.IsError=true → 返回 error，conversation manager 会封装为
//     ToolResultBlock{is_error=true} 反馈给 LLM，保持与内置工具失败一致
//   - Content 数组为空 → 返回空串（属正常情况，部分工具仅副作用无回显）
//   - text 类型直接拼接；image/resource 类型以可读占位代替（避免 base64 污染 LLM 上下文）
func (t *adapterTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	// 透传入参：input 已是 LLM 给的 raw json，无需二次序列化
	args := normalizeArguments(input)

	result, err := t.sessionCaller.CallTool(ctx, t.remoteName, args)
	if err != nil {
		return "", fmt.Errorf("mcp[%s/%s] 调用失败: %w", t.serverName, t.remoteName, err)
	}
	if result == nil {
		// 兜底：理论上 Session.CallTool 不会返回 (nil, nil)，防御性处理
		return "", fmt.Errorf("mcp[%s/%s]: 远端返回空 result", t.serverName, t.remoteName)
	}

	text := flattenContent(result.Content)

	// 业务执行失败：MCP 规范允许 result.isError=true 表达工具内部错误，
	// 此时 text 通常已包含具体错误描述（如"参数校验失败"），整体作为 error.Message
	if result.IsError {
		if text == "" {
			text = "（远端未提供错误描述）"
		}
		return "", fmt.Errorf("mcp[%s/%s] 工具执行失败: %s", t.serverName, t.remoteName, text)
	}
	return text, nil
}

// flattenContent 把 MCP content 数组折叠为单条可读文本。
//
// 折叠规则：
//   - text → 直接附加内容
//   - image → 输出 `[image mime=<mt> bytes=<n>]` 占位（避免 base64 污染 LLM 上下文）
//   - resource → 输出 `[resource mime=<mt> bytes=<n>]` 占位
//   - 其他未知类型 → 输出 `[unknown content type=<t>]`
//
// 多段之间以单个 `\n` 分隔；最终结果末尾不强制换行。
func flattenContent(items []session.MCPContent) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for i, c := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch c.Type {
		case session.MCPContentText:
			b.WriteString(c.Text)
		case session.MCPContentImage:
			fmt.Fprintf(&b, "[image mime=%s bytes=%d]", c.MimeType, len(c.Data))
		case session.MCPContentResource:
			fmt.Fprintf(&b, "[resource mime=%s bytes=%d]", c.MimeType, len(c.Data))
		default:
			fmt.Fprintf(&b, "[unknown content type=%s]", c.Type)
		}
	}
	return b.String()
}

// normalizeArguments 对外部传入的 raw args 做最小化清理：
//   - 空字节 / 空白串 → 返回 nil（CallTool 会进一步 omitempty）
//   - 否则原样透传
//
// 不做语义校验：LLM 可能传任意 JSON 结构，server 端按 inputSchema 拒绝即可。
func normalizeArguments(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	return raw
}

// defaultEmptySchema 是远端未提供 inputSchema 时的兜底空对象 schema。
//
// 用 `additionalProperties: true` 而非 false：避免 Provider（Anthropic/OpenAI）
// 校验时把任意入参当成"未声明字段"拒绝，保证 LLM 仍能尝试调用。
var defaultEmptySchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":true}`)

// normalizeSchema 对远端 schema 做最低限度的规范化：
//   - 空字节直接返回 nil（让 InputSchema 走 defaultEmptySchema 兜底）
//   - 否则透传：内部结构是否合法由 LLM/Provider 自行处理
//
// 不强行 unmarshal：保留 server 端原始字节，避免双向转换丢精度。
func normalizeSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	return raw
}
