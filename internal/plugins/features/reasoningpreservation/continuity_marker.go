package reasoningpreservation

import (
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func containsDialect(dialects []aipapi.ReasoningDialect, want aipapi.ReasoningDialect) bool {
	want = aipapi.NormalizeReasoningDialect(want)
	return slices.Contains(aipapi.NormalizeReasoningDialects(dialects), want)
}
