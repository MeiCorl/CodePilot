package integration

import (
	"github.com/MeiCorl/CodePilot/src/internal/engine/conversation"
	"github.com/MeiCorl/CodePilot/src/internal/hook"
)

// WireAgentLoop 把 HookEngine 注入 ConversationManager,由 AgentLoop 内部触发轮次、消息与错误事件。
func WireAgentLoop(engine *hook.Engine, mgr *conversation.ConversationManager, workdir string) {
	if mgr == nil {
		return
	}
	mgr.SetHookEngine(engine, workdir)
}
