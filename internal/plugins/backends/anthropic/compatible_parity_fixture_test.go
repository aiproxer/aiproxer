package anthropic_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/compatibleparity"
)

// TestCompatibleParity_anthropicFixturesAvailable locks Anthropic-family parity
// fixtures for the backends validation path (Task 1.4).
func TestCompatibleParity_anthropicFixturesAvailable(t *testing.T) {
	t.Parallel()
	n := 0
	for _, fx := range compatibleparity.ParityFixtures() {
		if fx.Family == compatibleparity.FamilyAnthropic {
			n++
		}
	}
	if n == 0 {
		t.Fatal("expected anthropic parity fixtures")
	}
}

// TestInstanceIsolation_anthropicPairPresent locks the Anthropic isolation pair
// fixture used by Task 1.4 RED policy tests.
func TestInstanceIsolation_anthropicPairPresent(t *testing.T) {
	t.Parallel()
	for _, pair := range compatibleparity.IsolationPairs() {
		if pair.Family == compatibleparity.FamilyAnthropic {
			if pair.InstanceA.TokenizerID == pair.InstanceB.TokenizerID {
				t.Fatal("anthropic isolation pair must differ in tokenizer")
			}
			return
		}
	}
	t.Fatal("missing anthropic isolation pair")
}
