package authorityattribution_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/authorityattribution"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

func TestReadinessRank_canonicalOrder(t *testing.T) {
	t.Parallel()
	order := []authority.Readiness{
		authority.ReadinessReady,
		authority.ReadinessDegraded,
		authority.ReadinessDisabled,
		authority.ReadinessUnavailable,
	}
	for i := 0; i < len(order)-1; i++ {
		if authorityattribution.ReadinessRank(order[i]) >= authorityattribution.ReadinessRank(order[i+1]) {
			t.Fatalf("rank(%q)=%d must be < rank(%q)=%d",
				order[i], authorityattribution.ReadinessRank(order[i]),
				order[i+1], authorityattribution.ReadinessRank(order[i+1]))
		}
	}
}

func TestAggregateReadiness_picksWorst(t *testing.T) {
	t.Parallel()
	got := authorityattribution.AggregateReadiness(
		authority.ReadinessReady,
		authority.ReadinessDegraded,
		authority.ReadinessDisabled,
		authority.ReadinessUnavailable,
	)
	if got != authority.ReadinessUnavailable {
		t.Fatalf("got %q want unavailable", got)
	}
}
