package reftoolpolicy

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

type ToolCallPolicy struct {
	policy
	order int
	id    string
}

var _ toolpolicy.Policy = ToolCallPolicy{}

// NewToolCallPolicy denies canonical tool-call stream events that match the same block rules
// as [NewToolCatalogFilter] (deterministic fail-closed denial before tool reactors run).
func NewToolCallPolicy(cfg Config) ToolCallPolicy {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	return ToolCallPolicy{policy: newPolicy(cfg), order: o, id: ID + "-tool-policy"}
}

func (p ToolCallPolicy) ID() string { return p.id }

func (p ToolCallPolicy) Order() int { return p.order }

func (p ToolCallPolicy) FailureMode() sdk.FailureMode { return sdk.FailClosed }

func (p ToolCallPolicy) Handle(
	_ context.Context,
	event aipapi.ToolEvent,
	_ toolpolicy.Meta,
	_ toolpolicy.Services,
) (toolpolicy.Decision, error) {
	if event.ToolName != "" && p.blocked(event.ToolName) {
		return toolpolicy.DecisionDeny, nil
	}
	return toolpolicy.DecisionAllow, nil
}
