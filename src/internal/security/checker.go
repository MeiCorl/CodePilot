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
//  1. 硬安全预检（Bash 黑名单）
//  2. 路径越界策略检查（根据档位决定 Ask/Deny/Allow）
//  3. 会话级规则匹配（内存，优先级最高）
//  4. 配置级规则匹配（项目级在前，全局在后）
//  5. 档位默认策略兜底
//
// Checker 是并发安全的：
//   - mode 与 sessionRules 通过 RWMutex 保护，
//   - Decide() 可被多个 goroutine 并发调用。
//   - SetMode() 用于 WebUI 状态栏「权限模式」下拉切换，运行时生效。
type Checker struct {
	// mode 为当前生效的权限模式（由 LoadPermissions 合并后的最终值，
	// 也可通过 SetMode() 在运行时切换）。
	mode Mode
	// rules 为配置级规则列表（项目级在前，全局在后，按顺序匹配）
	rules []Rule
	// workdir 为当前工作目录（用于路径越界检查）
	workdir string
	// mu 保护 mode 与 sessionRules 的并发读写。
	// 注意：mode 之所以也纳入 mu 保护，是因为 WebUI 切换模式时
	// Decide() 可能在多个 goroutine 并发读取 mode，不加锁会产生数据竞争。
	mu sync.RWMutex
	// sessionRules 为会话级临时规则（内存，用户选择"本会话允许"时追加）
	sessionRules []Rule
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
//  1. 路径级规则预检：路径类工具 + 路径级 allow 规则命中时直接 Allow（Step 0）
//  2. 硬安全预检：Bash 工具命中黑名单直接 Deny，不受档位和规则影响（Step 1）
//  3. 路径越界检查（Step 1.5）——**仅做"标记"，不提前 return**：
//     - 不论 read 还是 write/exec 越界，都仅记录 outsidePath/outsideWorkdir，
//       让流程 fall-through 到 Step 2/3/4，由「setting.json 规则 + mode 兜底」
//       共同决定最终动作
//  4. 会话级规则匹配：遍历 sessionRules，命中第一条即返回（Step 2）
//  5. 配置级规则匹配：遍历 rules（项目级在前、全局在后），命中第一条即返回（Step 3）
//  6. 档位默认策略：无规则命中时根据 mode + perm 确定默认动作（Step 4）
//
// 越界路径由「setting.json 规则 + mode 兜底」共同决策的好处：
//   - 用户显式配置 deny 规则 → Step 3 命中 Deny（与 mode 无关，最严格）
//   - 用户显式配置 ask 规则 → Step 3 命中 Ask（弹 HITL 窗）
//   - 用户显式配置 allow 规则（路径 glob 或工具级）→ Step 3 命中 Allow
//   - 无规则 → 走 mode 兜底：Strict/Ask, Default/Ask(for exec) / Allow(for read|write),
//     Permissive/Allow
//
// 配合 ToolHandler 的执行链：Checker 放行后 SandboxMiddleware 仍做硬兜底
// 校验（路径越界时直接返回 ErrPathOutsideSandbox，工具 Execute 不会被调用），
// 防止「Permissive + 无规则 越界路径」被静默放行。
func (c *Checker) Decide(_ context.Context, toolName string, params map[string]interface{}, perm tool.ToolPermission) Decision {
	// Step 0 — 路径级规则预检（NEW）。
	// 对路径类工具，先把所有路径绝对化（相对路径基于 workdir 拼接），
	// 走 MatchPathRule 查 session 优先 / config 次之的 allow 规则。
	// 命中即直接 Allow 短路（不再走 Step 1.5 越界检查）。
	// 注意：放在 Step 1 黑名单之前是**安全的**——黑名单仅作用于 Bash，
	// 而路径规则仅对路径类工具生效，作用域无重叠。
	if paramKey, isPathTool := IsPathTool(toolName); isPathTool && c.workdir != "" {
		pathValue := extractStringParam(params, paramKey)
		if pathValue != "" {
			absForRule := normalizeForRule(pathValue, c.workdir)
			if matched, reason := c.MatchPathRule(toolName, absForRule); matched {
				return Decision{
					Action: ActionAllow,
					Reason: reasonOrDefault(reason, "路径级规则命中"),
				}
			}
		}
	}

	// Step 1 — 硬安全预检（Bash 黑名单，不可被配置绕过）
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

	// Step 1.5 — 路径越界检查（标记而非硬拦截）
	//
	// 此处不再做任何"提前 return 硬决策"（不论 read 还是 write/exec），
	// 仅把越界状态降级为流程级标记：outsidePath/outsideWorkdir。
	// 让 Step 2（sessionRules）→ Step 3（configRules）→ Step 4（mode 兜底）
	// 有机会介入决策。这样所有越界路径都按"1→2→3"流程走：
	//   黑名单 → 用户规则（setting.json）→ mode 兜底
	//
	// ToolHandler 后端的 SandboxMiddleware 仍负责硬兜底校验（即便 Checker
	// 放行，越界路径在 Middleware 也会被 ErrPathOutsideSandbox 拦住），
	// 形成纵深防御——这是 read 越界"无规则被 mode 兜底 Allow"也不
	// 致数据泄露的安全保证。
	var (
		outsidePath    string
		outsideWorkdir string
	)
	if paramKey, isPathTool := IsPathTool(toolName); isPathTool && c.workdir != "" {
		pathValue := extractStringParam(params, paramKey)
		if pathValue != "" {
			outside, _ := IsPathOutsideSandbox(pathValue, c.workdir)
			if outside {
				// 越界：仅记录，让流程 fall-through
				outsidePath = pathValue
				outsideWorkdir = c.workdir
			}
		}
	}

	// Step 2 — 会话级规则匹配（优先级最高）
	c.mu.RLock()
	for i := range c.sessionRules {
		if matchRule(c.sessionRules[i], toolName, params) {
			rule := c.sessionRules[i] // 值拷贝
			c.mu.RUnlock()
			return Decision{
				Action:      rule.Action,
				Reason:      ruleReason(&rule),
				MatchedRule: &rule,
				TargetPath:  outsidePath,
				Workdir:     outsideWorkdir,
			}
		}
	}
	c.mu.RUnlock()

	// Step 3 — 配置级规则匹配（项目级在前、全局在后）
	for i := range c.rules {
		if matchRule(c.rules[i], toolName, params) {
			rule := c.rules[i] // 值拷贝
			return Decision{
				Action:      rule.Action,
				Reason:      ruleReason(&rule),
				MatchedRule: &rule,
				TargetPath:  outsidePath,
				Workdir:     outsideWorkdir,
			}
		}
	}

	// Step 4 — 档位默认策略兜底
	// 同样在函数顶部快照 mode，避免被并发 SetMode 撕裂。
	c.mu.RLock()
	mode := c.mode
	c.mu.RUnlock()
	action := ModeDefaultAction(mode, perm)
	reason := modeDefaultReason(mode, action)
	if outsidePath != "" {
		// 越界场景下追加"目标路径在工作目录外"提示，让日志/UI 能区分
		// 「in-sandbox 因 mode 兜底放行/询问/拒绝」与「越界后被 mode 兜底」。
		reason = reason + "（目标路径在工作目录外）"
	}
	return Decision{
		Action:     action,
		Reason:     reason,
		TargetPath: outsidePath,
		Workdir:    outsideWorkdir,
	}
}

// MatchPathRule 实现 PathRuleProvider 接口，查询路径类工具的 allow 规则。
//
// 匹配顺序：会话级（内存）→ 配置级（项目级在前、全局在后），命中第一条即返回。
// 匹配条件（同时满足）：
//  1. rule.Action == ActionAllow（deny/ask 规则不参与放行查询）
//  2. rule.Tool == toolName（路径规则严格匹配工具名；Tool="*" 不参与
//     防止「用 * 工具匹配所有 ReadFile 越界」的越权风险）
//  3. rule.Pattern == "" / "*" → 视为工具级豁免，命中
//  4. 其他 → filepath.Match(pattern, absPath) 命中
//
// 本方法是 SandboxMiddleware 越界查询的入口，并发安全（持 RLock）。
func (c *Checker) MatchPathRule(toolName, absPath string) (bool, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()

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
	return false, ""
}

// ruleAllowsPath 判断单条规则是否对该 (toolName, absPath) 放行。
//
// 注意：仅精确 toolName 匹配，不接受 Tool="*"——这是有意为之的安全策略，
// 避免「用 * 工具匹配所有 ReadFile 越界」的潜在越权。
func (c *Checker) ruleAllowsPath(rule Rule, toolName, absPath string) bool {
	if rule.Action != ActionAllow {
		return false
	}
	if rule.Tool != toolName {
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
//
// 入参 mode 必须是合法的档位（ModeStrict / ModeDefault / ModePermissive），
// 非法值会被忽略（保护机制，避免前端误传字符串导致 Checker 进入无效状态）。
// rules、sessionRules、workdir 等其他字段保持不变。
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
//   - rule.Tool: "*" 匹配所有工具，否则精确匹配大驼峰工具名
//   - rule.Pattern:
//   路径类工具 → 对 file_path/path 参数做 glob 匹配
//   Bash 工具 → 对 command 参数做命令前缀匹配
//   "*" 或空 → 匹配所有参数
func matchRule(rule Rule, toolName string, params map[string]interface{}) bool {
	// 工具名匹配
	if rule.Tool != "*" && rule.Tool != toolName {
		return false
	}

	// Pattern 为 "*" 或空 → 匹配所有参数
	pattern := rule.Pattern
	if pattern == "" || pattern == "*" {
		return true
	}

	// 根据工具类型选择匹配方式
	if paramKey, isPathTool := IsPathTool(toolName); isPathTool {
		// 路径类工具：glob 匹配
		pathValue := extractStringParam(params, paramKey)
		if pathValue == "" {
			return false
		}
		matched, _ := filepath.Match(pattern, pathValue)
		return matched
	}

	if toolName == "Bash" {
		// Bash 工具：命令前缀匹配
		command := extractStringParam(params, "command")
		if command == "" {
			return false
		}
		return matchBashPrefix(pattern, command)
	}

	// 其他未知工具：仅匹配工具名，pattern 忽略
	return true
}

// matchBashPrefix 对 Bash 命令做前缀匹配。
//
// pattern "git *" 匹配 "git status" 和 "git push origin main"；
// 匹配逻辑为 strings.HasPrefix(command, pattern 去掉尾部的 " *" 后 + " ")，
// 或者 command 整体等于 pattern 去掉 " *" 后的命令名。
func matchBashPrefix(pattern, command string) bool {
	// 去掉 pattern 尾部的通配符前缀，提取命令名
	prefix := strings.TrimSuffix(pattern, " *")
	prefix = strings.TrimSpace(prefix)

	if prefix == "" {
		return true
	}

	// 精确前缀匹配：command 以 "prefix " 开头，或 command 就是 prefix 本身
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
