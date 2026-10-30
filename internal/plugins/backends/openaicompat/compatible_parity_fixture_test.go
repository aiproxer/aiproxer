package openaicompat_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/compatibleparity"
)

// TestCompatibleParity_fixturesAvailable locks that OpenAI-family parity fixtures
// remain importable from the backends tree (Task 1.4 validation path).
func TestCompatibleParity_fixturesAvailable(t *testing.T) {
	t.Parallel()
	fxs := compatibleparity.ParityFixtures()
	var legacy, responses int
	for _, fx := range fxs {
		switch fx.Family {
		case compatibleparity.FamilyOpenAILegacy:
			legacy++
		case compatibleparity.FamilyOpenAIResponses:
			responses++
		}
	}
	if legacy == 0 || responses == 0 {
		t.Fatalf("expected OpenAI legacy/responses fixtures, got legacy=%d responses=%d", legacy, responses)
	}
}
