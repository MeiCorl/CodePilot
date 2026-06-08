package security

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/tool"

	"go.uber.org/zap"
)

// InterceptorResult 是拦截器 Check 方法的返回结果。
// 非 nil 表示工具调用被拦截（Deny 或需要 HITL 确认后用户拒绝）；
// nil 表示放行，调用方应继续执行工具。
type InterceptorResult struct {
	// Decision 为最终决策。
	Decision Decision
	// PermanentRule 为需要持久化到配置文件的规则（仅当 HITL 返回 ScopePermanent 时非 nil）。
	// 调用方（如 WebUI Handler）负责将其写入 setting.json。
	PermanentRule *Rule
}

// Interceptor 在工具执行前进行权限拦截。
//
// 职责：
//   - 调用 Checker.Decide() 获取权限决策
//   - ActionAllow → 放行
//   - ActionDeny → 拦截，返回错误 ToolResultBlock
//   - ActionAsk → 通过 HITLCallback 请求用户确认
//
// 拦截器位于 ToolHandler.doExecute() 的工具查找之后、执行之前，
// 所有工具调用必经此拦截点。
type Interceptor struct {
	checker      *Checker
	hitlCallback HITLCallback
	mu           sync.RWMutex
}

// NewInterceptor 创建拦截器。
// hitlCallback 可为 nil，nil 时 ActionAsk 等同于 ActionDeny。
func NewInterceptor(checker *Checker, callback HITLCallback) *Interceptor {
	return &Interceptor{
		checker:      checker,
		hitlCallback: callback,
	}
}

// SetHITLCallback 设置或更新 HITL 回调。
// 传入 nil 表示清除回调（ActionAsk 将等同于 ActionDeny）。
func (i *Interceptor) SetHITLCallback(callback HITLCallback) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.hitlCallback = callback
}

// getHITLCallback 安全获取当前 HITL 回调。
func (i *Interceptor) getHITLCallback() HITLCallback {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.hitlCallback
}

// Check 执行权限检查，决定工具调用是否放行。
//
// 返回值：
//   - nil: 放行，调用方继续执行工具
//   - *InterceptorResult: 拦截，调用方应构造 ToolResultBlock{IsError: true} 返回
//   - error: 检查过程本身的系统错误（如 JSON 解析失败）
//
// HITL 确认流程：
//   - 用户选择 ScopeOneTime → 仅本次放行
//   - 用户选择 ScopeSession → 追加会话级临时规则 + 本次放行
//   - 用户选择 ScopePermanent → 在返回结果中携带 PermanentRule，由调用方写配置文件 + 本次放行
//   - 用户拒绝 / 超时 / 无回调 → 视为 Deny
func (i *Interceptor) Check(ctx context.Context, toolName string, input json.RawMessage, perm tool.ToolPermission) (*InterceptorResult, error) {
	// 解析工具输入参数为 map
	params, err := parseInputParams(input)
	if err != nil {
		// JSON 解析失败不影响安全性，按空参数处理
		logger.Warn("权限拦截器解析工具输入失败，按空参数处理",
			zap.String("tool", toolName),
			zap.Error(err),
		)
		params = nil
	}

	// 调用 Checker 获取决策
	decision := i.checker.Decide(ctx, toolName, params, perm)

	switch decision.Action {
	case ActionAllow:
		logger.Debug("权限检查放行",
			zap.String("tool", toolName),
			zap.String("reason", decision.Reason),
		)
		return nil, nil // 放行

	case ActionDeny:
		logger.Info("权限检查拒绝",
			zap.String("tool", toolName),
			zap.String("reason", decision.Reason),
		)
		return &InterceptorResult{Decision: decision}, nil

	case ActionAsk:
		return i.handleAsk(ctx, toolName, params, decision)

	default:
		// 未知动作，保守处理为 Deny
		logger.Warn("权限检查返回未知动作，保守拒绝",
			zap.String("tool", toolName),
			zap.String("action", string(decision.Action)),
		)
		decision.Action = ActionDeny
		decision.Reason = "权限系统内部错误：未知的动作类型"
		return &InterceptorResult{Decision: decision}, nil
	}
}

// handleAsk 处理需要用户确认的场景。
func (i *Interceptor) handleAsk(ctx context.Context, toolName string, params map[string]interface{}, decision Decision) (*InterceptorResult, error) {
	callback := i.getHITLCallback()
	if callback == nil {
		// 无 HITL 回调，视为 Deny
		logger.Info("权限检查需要确认但无 HITL 通道，拒绝",
			zap.String("tool", toolName),
		)
		return &InterceptorResult{
			Decision: Decision{
				Action: ActionDeny,
				Reason: "权限系统要求确认但无可用的确认通道",
			},
		}, nil
	}

	// 构造确认请求（带 TargetPath/Workdir 供前端展示 + 路径级永久规则生成）
	req := PermissionRequest{
		ToolName:      toolName,
		ParamsSummary: buildParamsSummary(toolName, params),
		Reason:        decision.Reason,
		MatchedRule:   decision.MatchedRule,
		TargetPath:    decision.TargetPath,
		Workdir:       decision.Workdir,
	}

	// 调用 HITL 回调
	resp, err := callback(ctx, req)
	if err != nil {
		// 回调失败（超时、连接断开等），视为 Deny
		logger.Warn("HITL 回调失败，拒绝",
			zap.String("tool", toolName),
			zap.Error(err),
		)
		return &InterceptorResult{
			Decision: Decision{
				Action: ActionDeny,
				Reason: fmt.Sprintf("权限确认失败：%s", err.Error()),
			},
		}, nil
	}

	if !resp.Allowed {
		// 用户拒绝
		logger.Info("用户拒绝权限请求",
			zap.String("tool", toolName),
		)
		return &InterceptorResult{
			Decision: Decision{
				Action:      ActionDeny,
				Reason:      "用户拒绝本次操作",
				MatchedRule: decision.MatchedRule,
			},
		}, nil
	}

	// 用户允许，根据 Scope 处理
	// 路径类工具 + 有 TargetPath → 构造目录级 glob Pattern（"父目录 + /*"）；
	// 非路径类工具或无 TargetPath → 退化为工具级豁免 Pattern="*"
	pattern := buildPathAwarePattern(toolName, req.TargetPath, req.Workdir)

	switch resp.Scope {
	case ScopeSession:
		// 追加会话级临时规则（路径类工具用目录级 pattern，工具级用 "*"）
		sessionRule := Rule{
			Tool:    toolName,
			Pattern: pattern,
			Action:  ActionAllow,
			Reason:  "用户本会话授权",
		}
		i.checker.AddSessionRule(sessionRule)
		logger.Info("权限确认：本会话允许",
			zap.String("tool", toolName),
			zap.String("pattern", pattern),
		)

	case ScopePermanent:
		// 标记需要持久化的规则，由调用方写入配置文件
		permanentRule := &Rule{
			Tool:    toolName,
			Pattern: pattern,
			Action:  ActionAllow,
			Reason:  fmt.Sprintf("用户永久授权：放行 %s", pattern),
		}
		logger.Info("权限确认：永久允许",
			zap.String("tool", toolName),
			zap.String("pattern", pattern),
		)
		// 放行，同时携带 PermanentRule
		return &InterceptorResult{
			Decision:      Decision{Action: ActionAllow, Reason: "用户永久授权"},
			PermanentRule: permanentRule,
		}, nil

	case ScopeOneTime:
		logger.Info("权限确认：本次允许",
			zap.String("tool", toolName),
			zap.String("pattern", pattern),
		)
	}

	// 放行
	return nil, nil
}

// buildPathAwarePattern 根据工具是否为路径类工具 + 是否有 TargetPath，
// 决定本次授权写入规则的 Pattern：
//   - 路径类工具 + 有 TargetPath → 目录级 glob（父目录 + /*）
//   - 其他 → 工具级豁免 "*"
//
// 这是拦截器在用户授权"永久/本会话"时构造路径级规则的关键。
// 之所以放在这里而不是 buildParamsSummary，是因为只有 Interceptor 知道
// 用户已授权后的真实意图。
func buildPathAwarePattern(toolName, targetPath, workdir string) string {
	if _, isPathTool := IsPathTool(toolName); !isPathTool {
		return "*"
	}
	if targetPath == "" {
		return "*"
	}
	return BuildPathPattern(targetPath, workdir)
}

// parseInputParams 将工具输入的 JSON 解析为 map[string]interface{}。
func parseInputParams(input json.RawMessage) (map[string]interface{}, error) {
	if len(input) == 0 {
		return nil, nil
	}
	var params map[string]interface{}
	if err := json.Unmarshal(input, &params); err != nil {
		return nil, err
	}
	return params, nil
}

// buildParamsSummary 为 HITL 对话框构建参数的可读摘要。
func buildParamsSummary(toolName string, params map[string]interface{}) string {
	if params == nil {
		return ""
	}

	// 根据工具类型提取关键参数
	switch toolName {
	case "Bash":
		if cmd, ok := params["command"].(string); ok {
			if len(cmd) > 100 {
				return "command: " + cmd[:100] + "..."
			}
			return "command: " + cmd
		}
	case "WriteFile", "EditFile":
		if fp, ok := params["file_path"].(string); ok {
			return "file_path: " + fp
		}
	case "ReadFile":
		if fp, ok := params["file_path"].(string); ok {
			return "file_path: " + fp
		}
	case "Glob":
		if p, ok := params["path"].(string); ok {
			return "path: " + p
		}
	case "Grep":
		if p, ok := params["path"].(string); ok {
			return "path: " + p
		}
	}

	// 回退：拼接所有参数的键值
	var parts []string
	for k, v := range params {
		parts = append(parts, fmt.Sprintf("%s: %v", k, v))
	}
	summary := ""
	for i, p := range parts {
		if i > 0 {
			summary += ", "
		}
		summary += p
	}
	if len(summary) > 200 {
		summary = summary[:200] + "..."
	}
	return summary
}
