package extensions_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

func TestLegalPipelineStageNames_interleavedShapingOutsideExtensionStages(t *testing.T) {
	t.Parallel()
	stages := extensions.LegalPipelineStageNames()
	if !slices.Contains(stages, feature.StageIDPreRequest) {
		t.Fatal("pre-request must remain in legal pipeline")
	}
	if idx := slices.Index(stages, feature.StageIDAttemptLifecycle); idx < 0 {
		t.Fatal("attempt lifecycle must remain in legal pipeline")
	}
	preIdx := slices.Index(stages, feature.StageIDPreRequest)
	attemptIdx := slices.Index(stages, feature.StageIDAttemptLifecycle)
	if preIdx >= attemptIdx {
		t.Fatalf("pre-request (%d) must precede attempt lifecycle (%d)", preIdx, attemptIdx)
	}
	for _, s := range stages {
		if strings.Contains(strings.ToLower(s), "interleaved") || strings.Contains(strings.ToLower(s), "thinker") {
			t.Fatalf("interleaved shaping must not be an extension stage, found %q", s)
		}
	}
}
