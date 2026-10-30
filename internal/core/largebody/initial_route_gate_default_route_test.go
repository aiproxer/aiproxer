package largebody_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// TestInitialRouteAssessmentGate_defaultRouteKeepsItsFrozenModelBinding proves
// the wire assessment binds the model of a derived default route with the
// generation's frozen binding.
//
// The wire lane resolves a route without the request-bound registry, so the
// only model identity it may add is the one the generation's own default route
// needs. A request whose route origin is that default route keeps it; every
// other route keeps the lane's own resolution.
func TestInitialRouteAssessmentGate_defaultRouteKeepsItsFrozenModelBinding(t *testing.T) {
	t.Parallel()
	newGate := func(t *testing.T) (*largebody.InitialRouteAssessmentGate, *testStubBackend) {
		t.Helper()
		be := &testStubBackend{compatible: true, needsModelRewrite: true}
		return &largebody.InitialRouteAssessmentGate{
			BackendExecutionResolver:   inferenceResolver(),
			ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
			BackendResolver:            largebody.WireBackendMap{"be": be},
			DefaultRouteBindings: routing.DefaultRouteBindings{{
				Backend: "be",
				Model:   "canonical-m",
				Native:  "native-m-frozen",
			}},
		}, be
	}

	t.Run("default route", func(t *testing.T) {
		t.Parallel()
		gate, be := newGate(t)
		proof := validProof()
		proof.RouteSelector = "be:canonical-m"
		proof.RouteOrigin = routing.RouteOriginDefault
		proof.ClientModel = "client-model"

		decision, reason, cands := gate.Evaluate(context.Background(), proof)
		if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
			t.Fatalf("decision = %v / %v want accept", decision, reason)
		}
		if len(cands) != 1 {
			t.Fatalf("candidates = %d want 1", len(cands))
		}
		if cands[0].Primary.NativeModel != "native-m-frozen" {
			t.Fatalf("native model = %q want the frozen binding", cands[0].Primary.NativeModel)
		}
		if be.lastFacts.CandidateModel != "native-m-frozen" {
			t.Fatalf("candidate model = %q want the frozen binding", be.lastFacts.CandidateModel)
		}
	})

	t.Run("explicit route", func(t *testing.T) {
		t.Parallel()
		gate, _ := newGate(t)
		proof := validProof()
		proof.RouteSelector = "be:canonical-m"
		proof.RouteOrigin = routing.RouteOriginExplicit
		proof.ClientModel = "client-model"

		decision, reason, cands := gate.Evaluate(context.Background(), proof)
		if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
			t.Fatalf("decision = %v / %v want accept", decision, reason)
		}
		if len(cands) != 1 {
			t.Fatalf("candidates = %d want 1", len(cands))
		}
		if cands[0].Primary.NativeModel != "" {
			t.Fatalf("native model = %q want none for an explicit route", cands[0].Primary.NativeModel)
		}
	})
}
