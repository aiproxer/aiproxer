package extensions_test

import (
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// wantLegalPipelineOrder is the canonical extension pipeline. Keep aligned with ADR 0006 plus
// pre-request admission before route planning.
var wantLegalPipelineOrder = []string{
	feature.StageIDTransportAuth,
	feature.StageIDSessionOpen,
	feature.StageIDSecretGuard,
	feature.StageIDSessionClassification,
	feature.StageIDSubmit,
	feature.StageIDToolCatalog,
	feature.StageIDRequestWide,
	feature.StageIDPreRequest,
	feature.StageIDRouteHinting,
	feature.StageIDCandidateAttemptTransform,
	feature.StageIDAttemptLifecycle,
	feature.StageIDStreamEventMutation,
	feature.StageIDToolEventReaction,
	feature.StageIDCompletionGating,
	feature.StageIDFinalStreamObservation,
	feature.StageIDTrafficObservation,
	feature.StageIDEgressEncoding,
}

func TestLegalPipelineStageNames_matchesR2CanonicalOrder_RED(t *testing.T) {
	t.Parallel()
	got := extensions.LegalPipelineStageNames()
	if len(got) != len(wantLegalPipelineOrder) {
		t.Fatalf("RED stage four: LegalPipelineStageNames must return %d R2 stages (got %d)",
			len(wantLegalPipelineOrder), len(got))
	}
	if !slices.Equal(got, wantLegalPipelineOrder) {
		t.Fatalf("RED stage four: stage order mismatch\ngot  %#v\nwant %#v", got, wantLegalPipelineOrder)
	}
}
