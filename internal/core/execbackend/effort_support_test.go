package execbackend

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The internal effort-support seam of design §2: the provider states what it
// carries, and a provider that publishes no fact answers unknown — never a
// refusal.

// TestEffectiveEffortSupportAnswersUnknownWithoutAProviderFact pins the
// "nil callback means unknown" shape, the same shape EffectiveCaps has.
func TestEffectiveEffortSupportAnswersUnknownWithoutAProviderFact(t *testing.T) {
	t.Parallel()

	got := EffectiveEffortSupport(context.Background(), Backend{}, aipapi.Call{}, routing.AttemptCandidate{}, "high")
	if got != EffortSupportUnknown {
		t.Fatalf("effort support = %v, want unknown", got)
	}
}

// TestEffectiveEffortSupportCarriesTheProviderAnswer pins the query shape: the
// provider's own answer reaches the root, and the three states stay apart.
func TestEffectiveEffortSupportCarriesTheProviderAnswer(t *testing.T) {
	t.Parallel()

	for _, want := range []EffortSupportState{
		EffortSupportUnknown,
		EffortSupportSupported,
		EffortSupportUnsupported,
	} {
		be := Backend{
			ResolveEffortSupport: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate, _ string) EffortSupportState {
				return want
			},
		}
		got := EffectiveEffortSupport(context.Background(), be, aipapi.Call{}, routing.AttemptCandidate{}, "high")
		if got != want {
			t.Fatalf("effort support = %v, want %v", got, want)
		}
	}
}
