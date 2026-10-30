package openaicompat_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/compatibleparity"
)

// TestInstanceIsolation_openaiPairsPresent locks OpenAI-family isolation fixtures.
func TestInstanceIsolation_openaiPairsPresent(t *testing.T) {
	t.Parallel()
	var legacy, responses bool
	for _, pair := range compatibleparity.IsolationPairs() {
		switch pair.Family {
		case compatibleparity.FamilyOpenAILegacy:
			legacy = true
		case compatibleparity.FamilyOpenAIResponses:
			responses = true
		}
	}
	if !legacy || !responses {
		t.Fatalf("openai isolation pairs missing legacy=%v responses=%v", legacy, responses)
	}
}
