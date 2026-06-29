// Package sources implements the Hook self-awareness prompt source.
package sources

import (
	"context"

	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/tokens"
)

const hooksAwarenessContent = `<hooks>setting.json hooks; command/http/prompt/agent. Config skill config-management; code skill codebase-overview. Edit ReadFile+EditFile/WriteFile.</hooks>`

// HooksAwarenessSource tells the model that CodePilot has a hook system and where
// to load the detailed schema. It deliberately stays in the prompt engine layer
// and does not import the skill package.
type HooksAwarenessSource struct{}

func NewHooksAwarenessSource() *HooksAwarenessSource { return &HooksAwarenessSource{} }

func (s *HooksAwarenessSource) Name() string { return "hooks_awareness" }

func (s *HooksAwarenessSource) Assemble(_ context.Context, _ Env) (Section, error) {
	return Section{
		Name:      "hooks_awareness",
		Content:   hooksAwarenessContent,
		Placement: PlacementSystem,
		Tokens:    tokens.Estimate(hooksAwarenessContent),
	}, nil
}
