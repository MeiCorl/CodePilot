package integration

import "github.com/MeiCorl/CodePilot/src/internal/hook"

// SessionTarget 是 Handler 暴露给 Hook 集成层的最小接口。
type SessionTarget interface {
	SetHookEngine(*hook.Engine)
}

// WireSession 把 HookEngine 注入会话生命周期入口。
func WireSession(engine *hook.Engine, target SessionTarget) {
	if target == nil {
		return
	}
	target.SetHookEngine(engine)
}
