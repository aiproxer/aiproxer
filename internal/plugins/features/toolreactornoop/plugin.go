package toolreactornoop

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

const hookOrder = 100

type reactor struct{}

var _ sdk.ToolReactor = reactor{}

// NewToolReactor returns the registered tool reactor entrypoint for tool-reactor-noop.
func NewToolReactor() sdk.ToolReactor { return reactor{} }

func (reactor) ID() string { return ID }
func (reactor) Order() int { return hookOrder }
func (reactor) HandleToolEvent(_ context.Context, te aipapi.ToolEvent, _ sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
	return sdk.ToolPass, te, nil
}
