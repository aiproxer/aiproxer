package metrics

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestConversationViewProm_BoundedLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	cv := RegisterConversationViewProm(reg)
	sink := NewConversationViewSink(cv)
	if sink == nil {
		t.Fatal("sink nil")
	}
	// Exercise all bounded label combinations.
	sink.OnProjection(conversationprojection.StageEarly, conversationprojection.ProjectionSummary{FilteredCount: 2, StablePrefixCount: 1, AfterMessageCount: 1})
	sink.OnProjection(conversationprojection.StageFinal, conversationprojection.ProjectionSummary{FilteredCount: 1})
	sink.OnSteeringMutation(CacheDiscontinuityCreate, conversationprojection.PlacementStablePrefix)
	sink.OnSteeringMutation(CacheDiscontinuityReplace, conversationprojection.PlacementAfterMessage)
	sink.OnSteeringMutation(CacheDiscontinuityMove, conversationprojection.PlacementStablePrefix)
	sink.OnSteeringMutation(CacheDiscontinuityDeactivate, conversationprojection.PlacementAfterMessage)
	sink.OnAnchorFallback(conversationprojection.StageEarly, conversationprojection.AnchorStablePrefixFallback)
	sink.OnAnchorFallback(conversationprojection.StageFinal, conversationprojection.AnchorStablePrefixFallback)
	sink.OnAnchorFailure(conversationprojection.AnchorFailClosed)
	sink.OnProjectionFailure(conversationprojection.StageEarly)
	sink.OnProjectionFailure(conversationprojection.StageFinal)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		for _, m := range mf.Metric {
			for _, lp := range m.Label {
				val := lp.GetValue()
				// Ensure no high-cardinality values leak: no overlay IDs, digests, or plaintext.
				if len(val) > 64 {
					t.Fatalf("label value too long (possible plaintext): %q", val)
				}
				// Check that label values are bounded enums only.
				allowed := map[string]bool{
					"early": true, "final": true, "sdk_resolve": true, "unknown": true,
					"stable_prefix": true, "after_message": true,
					"create": true, "replace": true, "move": true, "deactivate": true,
					"stable_prefix_fallback": true, "fail_closed": true,
				}
				if !allowed[val] {
					t.Fatalf("unexpected label value %q for label %q metric %q: not bounded enum", val, lp.GetName(), mf.GetName())
				}
				// Ensure label name is bounded set.
				allowedNames := map[string]bool{"stage": true, "placement": true, "operation": true, "policy": true}
				if !allowedNames[lp.GetName()] {
					t.Fatalf("unexpected label name %q", lp.GetName())
				}
				_ = dto.Metric{}
			}
		}
	}
}
