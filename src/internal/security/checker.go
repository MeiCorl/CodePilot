package security

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// ---------------------------------------------------------------------------
// Checker
// ---------------------------------------------------------------------------
//
// 路径类工具的元数据（toolName → paramKey）统一维护在 sandbox_middleware.go
// 的 PathTools / IsPathTool 中，Checker 内部不再持有副本，避免双份定义。

// Checker 是权限检查器的核心实现，负责：
//  1. Bash 黑名单硬安全预检
//  2. 会话级 / 配置级显式规则匹配
//  3. 路径类工具的沙箱内外策略判断
//  4. 非路径工具按权限模式兜底
//
// Checker 是并发安全的：
//   - mode、sessionRules 与 oneTimePathRules 通过 RWMutex 保护，
//   - Decide() 可被多个 goroutine 并发调用，
//   - SetMode() 用于 WebUI 状态栏「权限模式」下拉切换，运行时生效。
type Checker struct {
	// mode 为当前生效的权限模式（由 LoadPermissions 合并后的最终值，
	// 也可通过 SetMode() 在运行时切换）。
	mode Mode
	// rules 为配置级规则列表（项目级在前，全局在后，按顺序匹配）
	rules []Rule
	// workdir 为当前工作目录（用于路径越界检查）
	workdir string
	// mu 保护 mode、sessionRules 与 oneTimePathRules 的并发读写。
	mu sync.RWMutex
	// sessionRules 为会话级临时规则（内存，用户选择"本会话允许"时追加）
	sessionRules []Rule
	// oneTimePathRules 为一次性路径放行规则（内存，用户选择"本次允许"时追加）。
	// 它只供 SandboxMiddleware 消费一次，用来让已通过 HITL 的越界路径穿过
	// 沙箱硬兜底；命中后立即删除，避免"本次允许"退化为会话级授权。
	oneTimePathRules []Rule
}

// NewChecker 根据合并后的策略创建权限检查器。
// workdir 为当前工作目录，用于路径越界检查。
func NewChecker(policy *PermissionPolicy, workdir string) *Checker {
	if policy == nil {
		policy = &PermissionPolicy{Mode: ModeDefault}
	}
	return &Checker{
		mode:    policy.Mode,
		rules:   policy.Rules,
		workdir: workdir,
	}
}

// Decide 执行一次完整的权限检查，返回最终决策。
//
// 检查流程（按优先级从高到低）：
//  1. Bash 黑名单：命中直接 Deny，不受任何配置绕过。
//  2. 显式权限规则：会话级规则优先，其次配置级规则；命中 allow/deny/ask
//     即按规则返回，allow 不再进入后续沙箱策略判断。
//  3. 路径沙箱策略：路径类工具按"是否在工作目录内 + 读写权限 + 当前模式"
//     决定 Allow / Ask / Deny。
//  4. 非路径工具兜底：按 mode + permission 使用通用默认策略。
func (c *Checker) Decide(_ context.Context, toolName string, params map[string]interface{}, perm tool.ToolPermission) Decision {
	// Step 1 — 硬安全预检（Bash 黑名单，不可被配置绕过）。
	if toolName == "Bash" {
		cmd := extractStringParam(params, "command")
		if cmd != "" {
			if err := CheckBashCommand(cmd); err != nil {
				return Decision{
					Action: ActionDeny,
					Reason: "安全策略拦截：" + err.Error(),
				}
			}
		}
	}

	var (
		pathValue string
		isPath    bool
		outside   bool
	)
	if paramKey, ok := IsPathTool(toolName); ok && c.workdir != "" {
		isPath = true
		pathValue = extractStringParam(params, paramKey)
		if pathValue != "" {
			outside, _ = IsPathOutsideSandbox(pathValue, c.workdir)
		}
	}

	// Step 2 — 显式规则匹配（会话级优先，配置级其次）。
	c.mu.RLock()
	for i := range c.sessionRules {
		if c.matchRule(c.sessionRules[i], toolName, params) {
			rule := c.sessionRules[i]
			c.mu.RUnlock()
			return Decision{
				Action:      rule.Action,
				Reason:      ruleReason(&rule),
				MatchedRule: &rule,
				TargetPath:  pathValue,
				Workdir:     c.workdir,
			}
		}
	}
	c.mu.RUnlock()

	for i := range c.rules {
		if c.matchRule(c.rules[i], toolName, params) {
			rule := c.rules[i]
			return Decision{
				Action:      rule.Action,
				Reason:      ruleReason(&rule),
				MatchedRule: &rule,
				TargetPath:  pathValue,
				Workdir:     c.workdir,
			}
		}
	}

	c.mu.RLock()
	mode := c.mode
	c.mu.RUnlock()

	// Step 3 — 路径沙箱策略。
	if isPath && pathValue != "" {
		action := PathSandboxAction(mode, perm, outside)
		return Decision{
			Action:     action,
			Reason:     pathSandboxReason(mode, perm, outside, action),
			TargetPath: pathValue,
			Workdir:    c.workdir,
		}
	}

	// Step 4 — 非路径工具兜底。
	action := ModeDefaultAction(mode, perm)
	return Decision{
		Action: action,
		Reason: modeDefaultReason(mode, action),
	}
}

// MatchPathRule 实现 PathRuleProvider 接口，查询沙箱外路径是否可被放行。
//
// 匹配顺序：
//  1. 一次性路径授权（命中后消费）
//  2. 会话级 / 配置级显式 allow 规则
//  3. 当前权限模式对沙箱外路径的默认放行策略
//
// 本方法是 SandboxMiddleware 越界查询的入口，并发安全。
func (c *Checker) MatchPathRule(toolName, absPath string) (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.oneTimePathRules {
		if c.ruleAllowsPath(c.oneTimePathRules[i], toolName, absPath) {
			rule := c.oneTimePathRules[i]
			c.oneTimePathRules = append(c.oneTimePathRules[:i], c.oneTimePathRules[i+1:]...)
			return true, reasonOrDefault(rule.Reason, "一次性路径授权")
		}
	}
	for i := range c.sessionRules {
		if c.ruleAllowsPath(c.sessionRules[i], toolName, absPath) {
			return true, c.sessionRules[i].Reason
		}
	}
	for i := range c.rules {
		if c.ruleAllowsPath(c.rules[i], toolName, absPath) {
			return true, c.rules[i].Reason
		}
	}
	if perm, ok := PathToolPermission(toolName); ok {
		action := PathSandboxAction(c.mode, perm, true)
		if action == ActionAllow {
			return true, pathSandboxReason(c.mode, perm, true, action)
		}
	}
	return false, ""
}

// ruleAllowsPath 判断单条 allow 规则是否对该 (toolName, absPath) 放行。
func (c *Checker) ruleAllowsPath(rule Rule, toolName, absPath string) bool {
	if rule.Action != ActionAllow {
		return false
	}
	if !matchToolName(rule.Tool, toolName) {
		return false
	}
	pattern := rule.Pattern
	if pattern == "" || pattern == "*" {
		return true
	}
	matched, err := filepath.Match(pattern, absPath)
	return err == nil && matched
}

// reasonOrDefault 在 reason 为空时回退到 defaultReason。
func reasonOrDefault(reason, defaultReason string) string {
	if reason != "" {
		return "路径级规则命中：" + reason
	}
	return defaultReason
}

// Mode 返回当前生效的权限模式。
func (c *Checker) Mode() Mode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mode
}

// SetMode 运行时切换权限模式。WebUI 状态栏下拉切换、自动化脚本
// 临时调整档位等场景使用，调用后立即对后续 Decide() 生效。
func (c *Checker) SetMode(mode Mode) {
	switch mode {
	case ModeStrict, ModeDefault, ModePermissive:
		// 合法档位，继续
	default:
		return
	}
	c.mu.Lock()
	c.mode = mode
	c.mu.Unlock()
}

// SessionRuleCount 返回当前会话级临时规则的数量（用于 UI 展示）。
func (c *Checker) SessionRuleCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.sessionRules)
}

// RuleCount 返回配置级规则的数量（用于 UI 展示）。
func (c *Checker) RuleCount() int {
	return len(c.rules)
}

// AddSessionRule 追加一条会话级临时规则（写锁保护）。
// 用户选择"本会话允许"时调用。
func (c *Checker) AddSessionRule(rule Rule) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionRules = append(c.sessionRules, rule)
}

// AddOneTimePathRule 追加一条一次性路径放行规则。
// 用户选择"本次允许"时由 Interceptor 调用，随后 SandboxMiddleware 命中即消费。
func (c *Checker) AddOneTimePathRule(rule Rule) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.oneTimePathRules = append(c.oneTimePathRules, rule)
}

// ClearSessionRules 清空所有会话级临时规则。
func (c *Checker) ClearSessionRules() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionRules = nil
}

// ---------------------------------------------------------------------------
// 规则匹配
// ---------------------------------------------------------------------------

// matchRule 判断一条规则是否匹配当前工具调用。
//
// 匹配逻辑：
//   - rule.Tool: 支持精确工具名、"*"、以及 filepath.Match 风格 glob
//   - rule.Pattern:
//     路径类工具 → 对 file_path/path 参数做 glob 匹配（同时尝试原始路径与规范化绝对路径）
//     Bash 工具 → 对 command 参数做命令前缀匹配
//     "*" 或空 → 匹配所有参数
func matchRule(rule Rule, toolName string, params map[string]interface{}) bool {
	return matchRuleWithWorkdir(rule, toolName, params, "")
}

func (c *Checker) matchRule(rule Rule, toolName string, params map[string]interface{}) bool {
	return matchRuleWithWorkdir(rule, toolName, params, c.workdir)
}

func matchRuleWithWorkdir(rule Rule, toolName string, params map[string]interface{}, workdir string) bool {
	if !matchToolName(rule.Tool, toolName) {
		return false
	}

	pattern := rule.Pattern
	if pattern == "" || pattern == "*" {
		return true
	}

	if paramKey, isPathTool := IsPathTool(toolName); isPathTool {
		pathValue := extractStringParam(params, paramKey)
		if pathValue == "" {
			return false
		}
		if matched, _ := filepath.Match(pattern, pathValue); matched {
			return true
		}
		if workdir != "" {
			absValue := normalizeForRule(pathValue, workdir)
			matched, _ := filepath.Match(pattern, absValue)
			return matched
		}
		return false
	}

	if toolName == "Bash" {
		command := extractStringParam(params, "command")
		if command == "" {
			return false
		}
		return matchBashPrefix(pattern, command)
	}

	// 其他未知工具：工具名已匹配，pattern 忽略。
	return true
}

func matchToolName(pattern, toolName string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	if pattern == toolName {
		return true
	}
	matched, err := filepath.Match(pattern, toolName)
	return err == nil && matched
}

// matchBashPrefix 对 Bash 命令做前缀匹配。
//
// pattern "git *" 匹配 "git status" 和 "git push origin main"；
// 匹配逻辑为 strings.HasPrefix(command, pattern 去掉尾部的 " *" 后 + " ")，
// 或者 command 整体等于 pattern 去掉 " *" 后的命令名。
func matchBashPrefix(pattern, command string) bool {
	prefix := strings.TrimSuffix(pattern, " *")
	prefix = strings.TrimSpace(prefix)

	if prefix == "" {
		return true
	}

	return strings.HasPrefix(command, prefix+" ") || command == prefix
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// extractStringParam 从 params map 中安全地提取字符串参数。
func extractStringParam(params map[string]interface{}, key string) string {
	if params == nil {
		return ""
	}
	v, ok := params[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// ruleReason 为规则命中时生成可读的原因文本。
func ruleReason(r *Rule) string {
	if r.Reason != "" {
		return r.Reason
	}
	return "命中规则：工具=" + r.Tool + " 模式=" + string(r.Pattern) + " 动作=" + string(r.Action)
}

// modeDefaultReason 为档位默认策略生成可读的原因文本。
func modeDefaultReason(mode Mode, action Action) string {
	switch action {
	case ActionAllow:
		return string(mode) + "模式：自动放行"
	case ActionAsk:
		return string(mode) + "模式：需要用户确认"
	case ActionDeny:
		return string(mode) + "模式：操作被拒绝"
	default:
		return string(mode) + "模式"
	}
}

// normalizeForRule 把 LLM 传入的 path 规范化为"用于规则匹配"的字符串。
//
// 行为：
//  1. 绝对路径 → filepath.Clean(path)
//  2. 相对路径 → filepath.Join(workdir, path) → Clean
//  3. workdir 为空 → filepath.Clean(path)
//
// **不**做 symlink 解析——用户写 /tmp/* 期望包含 symlink。
// 与 ResolveInSandbox 不同：本函数不强制路径必须在 workdir 内，
// 仅做"绝对化+规范化"，用于规则匹配。
func normalizeForRule(path, workdir string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if workdir == "" {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(workdir, path))
}
