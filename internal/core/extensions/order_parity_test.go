package extensions_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/hooks"
)

func TestStableParticipantLess_documentedTriple(t *testing.T) {
	t.Parallel()
	// order -> id -> registration index
	if g := hooks.StableParticipantLess(1, 2, "a", "a", 0, 0); g >= 0 {
		t.Fatalf("lower order should sort first, got %d", g)
	}
	if g := hooks.StableParticipantLess(1, 1, "b", "a", 0, 0); g <= 0 {
		t.Fatalf("equal order, ascending id: want > 0, got %d", g)
	}
	if g := hooks.StableParticipantLess(1, 1, "x", "x", 3, 7); g >= 0 {
		t.Fatalf("equal order+id, ascending reg index: want < 0, got %d", g)
	}
}
