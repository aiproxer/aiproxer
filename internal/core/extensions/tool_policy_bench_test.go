package extensions_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

type benchPol struct{}

func (benchPol) ID() string                        { return "bench-pol" }
func (benchPol) Order() int                        { return 0 }
func (benchPol) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (benchPol) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

// BenchmarkRunToolPolicyStage_preSortedPolicies measures the steady-state tool policy stage when
// policies are already in execution order (runtime path).
func BenchmarkRunToolPolicyStage_preSortedPolicies(b *testing.B) {
	ctx := b.Context()
	ev := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolCallID: "tc1", ToolName: "fn"}
	policies := toolpolicy.MaterializeSorted([]toolpolicy.Policy{benchPol{}})
	b.ReportAllocs()

	for b.Loop() {
		if err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
			Ctx:      ctx,
			Policies: policies,
			Event:    ev,
		}); err != nil {
			b.Fatal(err)
		}
	}
}
