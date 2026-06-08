package security

import (
	"fmt"

	"github.com/MeiCorl/CodePilot/src/internal/config"
)

// ---------------------------------------------------------------------------
// 合并后的策略（Policy）
// ---------------------------------------------------------------------------

// PermissionPolicy 是多层配置合并后的最终策略，供 Checker 使用。
type PermissionPolicy struct {
	// Mode 为最终生效的权限模式。
	Mode Mode
	// Rules 为最终生效的规则列表（项目级在前，全局在后，按顺序匹配）。
	Rules []Rule
	// HasProjectConfig 标记是否存在项目级配置。
	HasProjectConfig bool
}

// ---------------------------------------------------------------------------
// 配置加载与合并
// ---------------------------------------------------------------------------

// LoadPermissions 合并全局配置和项目级配置，生成最终生效的权限策略。
//
// 合并规则：
//   - 项目级 mode 覆盖全局 mode（项目级非空时取项目级，否则取全局）
//   - 项目级 rules 排在全局 rules 前面（优先匹配）
//   - 两层均为空时返回 ModeDefault + 空规则列表
//
// 参数 globalConf 为全局配置，projectConf 为项目级配置（可为 nil）。
func LoadPermissions(globalConf *config.Config, projectConf *config.Config) *PermissionPolicy {
	policy := &PermissionPolicy{
		Mode: ModeDefault,
	}

	// 防护：globalConf 为 nil 时使用零值配置
	if globalConf == nil {
		globalConf = &config.Config{}
	}

	// 收集全局 rules
	globalRules := parseRules(globalConf)

	// 处理项目级配置
	if projectConf != nil {
		policy.HasProjectConfig = true
		projectRules := parseRules(projectConf)

		// 项目级 mode 覆盖全局 mode
		if projectConf.Permissions.Mode != "" {
			policy.Mode = Mode(projectConf.Permissions.Mode)
		} else if globalConf.Permissions.Mode != "" {
			policy.Mode = Mode(globalConf.Permissions.Mode)
		}

		// 项目级 rules 在前，全局 rules 在后
		policy.Rules = append(projectRules, globalRules...)
	} else {
		// 仅全局配置
		if globalConf.Permissions.Mode != "" {
			policy.Mode = Mode(globalConf.Permissions.Mode)
		}
		policy.Rules = globalRules
	}

	// 校验 mode 合法性
	switch policy.Mode {
	case ModeStrict, ModeDefault, ModePermissive:
		// 合法
	default:
		// 不合法时降级为 default
		policy.Mode = ModeDefault
	}

	return policy
}

// parseRules 从 Config 中解析 Permissions.Rules 为内部 Rule 列表。
// 校验每条规则的 Action 合法性，不合法的规则跳过并记录。
func parseRules(cfg *config.Config) []Rule {
	if cfg == nil {
		return nil
	}

	var rules []Rule
	for i, rc := range cfg.Permissions.Rules {
		action := Action(rc.Action)
		switch action {
		case ActionAllow, ActionDeny, ActionAsk:
			rules = append(rules, Rule{
				Tool:    rc.Tool,
				Pattern: rc.Pattern,
				Action:  action,
				Reason:  rc.Reason,
			})
		default:
			// 跳过 action 不合法的规则
			fmt.Printf("[security] 跳过第 %d 条规则：不合法的 action %q\n", i+1, rc.Action)
		}
	}
	return rules
}
