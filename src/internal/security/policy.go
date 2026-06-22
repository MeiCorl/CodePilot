// Package security 提供 CodePilot 的安全层——权限策略模型、检查器、
// 工具执行拦截器以及 HITL（人在回路）回调机制。
//
// 安全层作为架构第 5 层（横切关注点），为所有工具调用提供可配置的
// 纵深防御：危险命令黑名单（硬拦截）→ 可配置规则匹配 → 多档权限模式 →
// 人在回路确认。本包整合了原 tool/safety 包的路径沙箱和 Bash 黑名单，
// 统一归口到 internal/security 管理。
package security

import "github.com/MeiCorl/CodePilot/src/internal/tool"

// ---------------------------------------------------------------------------
// 权限模式（Mode）
// ---------------------------------------------------------------------------

// Mode 定义权限系统的整体严格程度档位。
// 三档覆盖从"全部自动放行"到"所有敏感操作需确认"的光谱。
type Mode string

const (
	// ModeStrict 严格模式：所有写操作（Write/Exec）均需用户确认，路径越界直接拒绝。
	ModeStrict Mode = "strict"
	// ModeDefault 默认模式：读写操作自动放行，Bash 执行和越界路径需用户确认。
	// 无 permissions 配置时的等效行为。
	ModeDefault Mode = "default"
	// ModePermissive 放行模式：仅黑名单拦截，其余全部自动放行（含越界路径）。
	ModePermissive Mode = "permissive"
)

// String 返回模式的可读标识，用于日志、UI 展示和配置序列化。
func (m Mode) String() string { return string(m) }

// ModeDefaultAction 根据当前档位和工具权限级别，返回默认的权限动作。
// 当无自定义规则命中时，由档位默认策略兜底。
//
// 映射规则：
//   - Strict:     Read → Allow, Write → Ask, Exec → Ask
//   - Default:    Read → Allow, Write → Allow, Exec → Ask
//   - Permissive: Read → Allow, Write → Allow, Exec → Allow
func ModeDefaultAction(mode Mode, perm tool.ToolPermission) Action {
	switch mode {
	case ModeStrict:
		switch perm {
		case tool.PermRead:
			return ActionAllow
		case tool.PermWrite, tool.PermExec:
			return ActionAsk
		}
	case ModeDefault:
		switch perm {
		case tool.PermRead, tool.PermWrite:
			return ActionAllow
		case tool.PermExec:
			return ActionAsk
		}
	case ModePermissive:
		return ActionAllow
	}
	// 未知模式或权限级别，保守处理为 Ask
	return ActionAsk
}

// ---------------------------------------------------------------------------
// 权限动作（Action）
// ---------------------------------------------------------------------------

// Action 定义单次权限检查的结果动作。
type Action string

const (
	// ActionAllow 允许执行，无需用户确认。
	ActionAllow Action = "allow"
	// ActionDeny 拒绝执行，将作为错误反馈给 LLM。
	ActionDeny Action = "deny"
	// ActionAsk 需要用户确认（HITL），暂停 Agent Loop 等待用户决策。
	ActionAsk Action = "ask"
)

// String 返回动作的可读标识。
func (a Action) String() string { return string(a) }

// ---------------------------------------------------------------------------
// 授权范围（Scope）
// ---------------------------------------------------------------------------

// Scope 定义用户授权的持续范围，用于 HITL 确认后的后续处理。
type Scope string

const (
	// ScopeOneTime 仅本次允许，下次相同操作仍需重新确认。
	ScopeOneTime Scope = "once"
	// ScopeSession 本会话允许，在当前会话生命周期内相同操作自动放行。
	ScopeSession Scope = "session"
	// ScopePermanent 永久允许，将规则写入配置文件，重启后仍生效。
	ScopePermanent Scope = "permanent"
)

// String 返回范围的可读标识。
func (s Scope) String() string { return string(s) }

// ---------------------------------------------------------------------------
// 规则（Rule）
// ---------------------------------------------------------------------------

// Rule 定义一条权限规则，按「工具名 + 参数模式」声明放行、拒绝或询问。
//
// 匹配逻辑：
//   - Tool: "*" 匹配所有工具，否则精确匹配大驼峰工具名（如 "Bash"、"WriteFile"）
//   - Pattern:
//     路径类工具：对 file_path/path/base_dir 参数做 glob 匹配
//     Bash 工具：对 command 参数做命令前缀匹配
//     "*" 或空字符串：匹配所有参数
type Rule struct {
	// Tool 为目标工具名，大驼峰格式（如 "Bash"、"WriteFile"）；"*" 匹配所有工具。
	Tool string `json:"tool"`
	// Pattern 为参数匹配模式，路径 glob 或 Bash 命令前缀；"*" 或空匹配所有参数。
	Pattern string `json:"pattern"`
	// Action 为命中后的动作：allow / deny / ask。
	Action Action `json:"action"`
	// Reason 为可选的可读说明，用于日志和 HITL 对话框展示。
	Reason string `json:"reason,omitempty"`
}

// ---------------------------------------------------------------------------
// 决策结果（Decision）
// ---------------------------------------------------------------------------

// Decision 是单次权限检查的完整结果，携带动作、原因和匹配到的规则。
type Decision struct {
	// Action 为本次检查的最终动作。
	Action Action
	// Reason 为可读原因说明，将作为 ToolResultBlock 的错误内容反馈给 LLM。
	// 区分"安全策略拦截"和"用户拒绝"两类。
	Reason string
	// MatchedRule 为命中的规则引用，可能为 nil（档位默认策略兜底时）。
	MatchedRule *Rule
	// TargetPath 为路径类工具触发决策时的目标路径（原始输入），仅在
	// Step 1.5 路径越界分支填入。Interceptor 据此构造 PermissionRequest
	// 的 TargetPath 字段，供前端展示和"永久允许"时生成路径级规则。
	// 不参与 JSON 序列化（仅进程内传递）。
	TargetPath string `json:"-"`
	// Workdir 为当前工作目录绝对路径，与 TargetPath 配合使用。
	// 不参与 JSON 序列化。
	Workdir string `json:"-"`
}

// ---------------------------------------------------------------------------
// HITL 交互结构
// ---------------------------------------------------------------------------

// PermissionRequest 是权限确认请求，由后端发送给前端（通过 WebSocket）。
type PermissionRequest struct {
	// ToolName 为待确认的工具名（大驼峰）。
	ToolName string `json:"tool_name"`
	// ParamsSummary 为参数的可读摘要，如 "command: git push origin main"。
	ParamsSummary string `json:"params_summary"`
	// Reason 为触发确认的原因说明。
	Reason string `json:"reason"`
	// MatchedRule 为命中的规则（可能为 nil，表示档位默认策略触发）。
	MatchedRule *Rule `json:"matched_rule,omitempty"`
	// TargetPath 为路径类工具触发确认时的目标路径（原始输入，未规范化）。
	// 当前端展示"目标路径"一栏、用户点击"永久允许"时，服务端基于该路径
	// 构造目录级 glob Pattern（如 /tmp/foo → /tmp/*），避免工具级豁免的
	// 安全风险。
	TargetPath string `json:"target_path,omitempty"`
	// Workdir 为当前工作目录的绝对路径，弹窗展示 + 后端规范化 target_path 用。
	Workdir string `json:"workdir,omitempty"`
}

// PermissionResponse 是用户权限确认的响应，由前端发送回后端。
type PermissionResponse struct {
	// Allowed 表示用户是否允许本次操作。
	Allowed bool `json:"allowed"`
	// Scope 表示用户的授权范围：once / session / permanent。
	Scope Scope `json:"scope"`
}

// PathSandboxAction 根据路径是否越过工作目录、工具读写权限与当前权限模式，
// 返回路径类工具在沙箱策略阶段的默认动作。
//
// 规则：
//   - 沙箱内：读操作允许；写操作 strict 询问，default/permissive 允许。
//   - 沙箱外：读操作 strict 拒绝，default/permissive 允许。
//   - 沙箱外：写操作 strict 拒绝，default 询问，permissive 允许。
func PathSandboxAction(mode Mode, perm tool.ToolPermission, outside bool) Action {
	if !outside {
		switch perm {
		case tool.PermRead:
			return ActionAllow
		case tool.PermWrite:
			if mode == ModeStrict {
				return ActionAsk
			}
			return ActionAllow
		case tool.PermExec:
			return ModeDefaultAction(mode, perm)
		default:
			return ActionAsk
		}
	}

	switch perm {
	case tool.PermRead:
		if mode == ModeStrict {
			return ActionDeny
		}
		return ActionAllow
	case tool.PermWrite:
		switch mode {
		case ModeStrict:
			return ActionDeny
		case ModeDefault:
			return ActionAsk
		case ModePermissive:
			return ActionAllow
		}
	case tool.PermExec:
		return ModeDefaultAction(mode, perm)
	}
	return ActionAsk
}

func pathSandboxReason(mode Mode, perm tool.ToolPermission, outside bool, action Action) string {
	location := "沙箱路径内"
	if outside {
		location = "沙箱路径外"
	}
	return location + "：" + perm.String() + " 操作在 " + string(mode) + " 模式下" + actionText(action)
}

func actionText(action Action) string {
	switch action {
	case ActionAllow:
		return "自动放行"
	case ActionAsk:
		return "需要用户确认"
	case ActionDeny:
		return "被拒绝"
	default:
		return string(action)
	}
}
