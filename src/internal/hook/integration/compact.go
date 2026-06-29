package integration

import "github.com/MeiCorl/CodePilot/src/internal/hook"

// CompactTarget 与 SessionTarget 共用 SetHookEngine,Handler 内部负责 compact 事件派发。
type CompactTarget interface {
	SetHookEngine(*hook.Engine)
}

// WireCompact 把 HookEngine 注入上下文压缩回调入口。
func WireCompact(engine *hook.Engine, target CompactTarget) {
	if target == nil {
		return
	}
	target.SetHookEngine(engine)
}
