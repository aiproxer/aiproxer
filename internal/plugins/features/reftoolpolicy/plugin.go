package reftoolpolicy

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
)

const defaultOrder = 45

type toolFilter struct {
	policy
	order int
	id    string
}

var _ toolcatalog.Filter = toolFilter{}

// NewToolCatalogFilter removes blocked tools from the call before the backend.
func NewToolCatalogFilter(cfg Config) toolcatalog.Filter {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	return toolFilter{policy: newPolicy(cfg), order: o, id: ID + "-filter"}
}

func (f toolFilter) ID() string                   { return f.id }
func (f toolFilter) Order() int                   { return f.order }
func (f toolFilter) FailureMode() sdk.FailureMode { return sdk.FailOpen }

func (f toolFilter) Handle(_ context.Context, call *aipapi.Call, _ toolcatalog.CatalogMeta, _ toolcatalog.Services) error {
	if call == nil {
		return nil
	}
	out := call.Tools[:0]
	for _, t := range call.Tools {
		if f.blocked(t.Name) {
			continue
		}
		out = append(out, t)
	}
	call.Tools = out
	return nil
}

type toolReactor struct {
	policy
	order int
}

var _ sdk.ToolReactor = toolReactor{}

// NewToolReactor swallows tool stream events for blocked tool names.
func NewToolReactor(cfg Config) sdk.ToolReactor {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	return toolReactor{policy: newPolicy(cfg), order: o}
}

func (r toolReactor) ID() string { return ID + "-reactor" }
func (r toolReactor) Order() int { return r.order }

func (r toolReactor) HandleToolEvent(_ context.Context, te aipapi.ToolEvent, _ sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
	if r.blocked(te.ToolName) {
		return sdk.ToolSwallow, aipapi.ToolEvent{}, nil
	}
	return sdk.ToolPass, aipapi.ToolEvent{}, nil
}
