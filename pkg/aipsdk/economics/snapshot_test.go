package economics_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

func TestSnapshotStateKnown(t *testing.T) {
	t.Parallel()
	for _, s := range []economics.SnapshotState{
		economics.SnapshotReady,
		economics.SnapshotStale,
		economics.SnapshotDegraded,
		economics.SnapshotUnavailable,
		economics.SnapshotDisabled,
	} {
		if !s.IsKnown() {
			t.Fatalf("%q must be known", s)
		}
	}
	if economics.SnapshotState("bogus").IsKnown() {
		t.Fatal("bogus must be unknown")
	}
}

func TestSnapshotPolicyAndRatingRefs(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0).UTC()
	snap := economics.Snapshot[economics.PolicyRulesView]{
		ID:          "usage_authority",
		Version:     "v3",
		EffectiveAt: now,
		FetchedAt:   now,
		State:       economics.SnapshotReady,
		Value:       economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
	}
	pref := snap.PolicyRef("usage")
	if pref.ID != "usage_authority" || pref.Version != "v3" || pref.PolicyID != "usage" {
		t.Fatalf("policy ref=%+v", pref)
	}
	rsnap := economics.Snapshot[economics.RatingCatalogView]{
		ID:      "rating",
		Version: "cat-1",
		State:   economics.SnapshotReady,
		Value:   economics.RatingCatalogView{Currency: "USD", CatalogVersion: "cat-1"},
	}
	rref := rsnap.RatingRef("reference")
	if rref.Version != "cat-1" || rref.RaterID != "reference" {
		t.Fatalf("rating ref=%+v", rref)
	}
}
