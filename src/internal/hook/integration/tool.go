package integration

import (
	"github.com/MeiCorl/CodePilot/src/internal/engine/conversation"
	"github.com/MeiCorl/CodePilot/src/internal/hook"
)

// WireToolHandler 把 HookEngine 注入 ToolHandler,由 ToolHandler 内部触发工具前后事件。
func WireToolHandler(engine *hook.Engine, h *conversation.ToolHandler) {
	if h == nil {
		return
	}
	h.SetHookEngine(engine)
}
