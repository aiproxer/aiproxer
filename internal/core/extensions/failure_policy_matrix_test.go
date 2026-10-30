package extensions_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

func TestDefaultFailurePolicyForStage_matchesDesignMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		stage string
		want  extensions.FailurePolicy
	}{
		{feature.StageIDTransportAuth, extensions.FailurePolicyFailClosed},
		{feature.StageIDSessionOpen, extensions.FailurePolicyFailOpen},
		{feature.StageIDSessionClassification, extensions.FailurePolicyFailOpen},
		{feature.StageIDSubmit, extensions.FailurePolicyFailOpen},
		{feature.StageIDToolCatalog, extensions.FailurePolicyFailOpen},
		{feature.StageIDRequestWide, extensions.FailurePolicyFailOpen},
		{feature.StageIDRouteHinting, extensions.FailurePolicyFailOpen},
		{feature.StageIDCandidateAttemptTransform, extensions.FailurePolicyFailOpen},
		{feature.StageIDAttemptLifecycle, extensions.FailurePolicyFailOpen},
		{feature.StageIDStreamEventMutation, extensions.FailurePolicyFailOpen},
		{feature.StageIDToolEventReaction, extensions.FailurePolicyFailOpen},
		{feature.StageIDCompletionGating, extensions.FailurePolicyFailOpen},
		{feature.StageIDFinalStreamObservation, extensions.FailurePolicyFailOpen},
		{feature.StageIDTrafficObservation, extensions.FailurePolicyFailOpen},
		{feature.StageIDEgressEncoding, extensions.FailurePolicyFailClosed},
	}
	for _, tc := range cases {
		t.Run(tc.stage, func(t *testing.T) {
			t.Parallel()
			if got := extensions.DefaultFailurePolicyForStage(tc.stage); got != tc.want {
				t.Fatalf("DefaultFailurePolicyForStage(%q) = %v want %v", tc.stage, got, tc.want)
			}
		})
	}
	t.Run("unknown_stage_returns_unset", func(t *testing.T) {
		t.Parallel()
		if got := extensions.DefaultFailurePolicyForStage("unknown_stage_xyz"); got != extensions.FailurePolicyUnset {
			t.Fatalf("unknown stage: got %v want Unset", got)
		}
	})
}
