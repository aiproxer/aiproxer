package routing

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
)

func TestStickyCandidate_ThinkerBranchSetsThinkerRole(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{
		StickyBackendID: "a",
		Session:         &SessionRoutingState{FirstRequestConsumed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "a:m" {
		t.Fatalf("got %#v want sticky a:m", out)
	}
	if out[0].InterleavedRole != interleavedstate.RoleThinker {
		t.Fatalf("role: got %q want thinker", out[0].InterleavedRole)
	}
	if out[0].SelectorKey != "a:m^b:m" {
		t.Fatalf("selector key: got %q", out[0].SelectorKey)
	}
}

func TestStickyCandidate_SuppressedThinkerFallsThroughToExecutor(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{
		StickyBackendID: "a",
		SuppressThinker: true,
		Session:         &SessionRoutingState{FirstRequestConsumed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "b:m" {
		t.Fatalf("got %#v want executor b:m", out)
	}
	if out[0].InterleavedRole != interleavedstate.RoleExecutor {
		t.Fatalf("role: got %q want executor", out[0].InterleavedRole)
	}
}

func TestStickyCandidate_SuppressedThinkerNoExecutorReturnsNoEligible(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ExpandFailover(sel, PlanOptions{
		StickyBackendID: "a",
		SuppressThinker: true,
		Excluded:        map[string]struct{}{"b:m": {}},
		Session:         &SessionRoutingState{FirstRequestConsumed: true},
	})
	if !errors.Is(err, ErrNoEligibleCandidate) {
		t.Fatalf("got %v want ErrNoEligibleCandidate", err)
	}
}

func TestStickyCandidate_ExecutorBranchSetsExecutorRole(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{
		StickyBackendID: "b",
		Session:         &SessionRoutingState{FirstRequestConsumed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "b:m" {
		t.Fatalf("got %#v want sticky b:m", out)
	}
	if out[0].InterleavedRole != interleavedstate.RoleExecutor {
		t.Fatalf("role: got %q want executor", out[0].InterleavedRole)
	}
	if out[0].SelectorKey != "a:m^b:m" {
		t.Fatalf("selector key: got %q", out[0].SelectorKey)
	}
}

func TestStickyCandidate_DuplicateWeightedEntryAdvancesFromCurrentCursor(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^[weight=2]b:m")
	if err != nil {
		t.Fatal(err)
	}
	entries, selKey := buildThinkerCycle(sel.Alternatives[0].Weighted)
	cycle := interleavedstate.CycleState{
		SelectorKey: selKey,
		Sequence:    entries,
		NextIndex:   1,
	}
	groups, err := ExpandFailoverGroups(sel, PlanOptions{
		StickyBackendID: "b",
		ThinkerCycle:    cycle,
		Session:         &SessionRoutingState{FirstRequestConsumed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Candidates) != 1 || groups[0].Candidates[0].Key != "b:m" {
		t.Fatalf("sticky executor: got %#v", groups)
	}
	next := groups[0].NextThinkerCycle
	if next == nil || next.NextIndex != 2 {
		t.Fatalf("next cycle from second b:m: got %+v want NextIndex 2", next)
	}
}

func TestExpandFailoverGroups_StickyThinkerWeightedSetsNextThinkerCycle(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m")
	if err != nil {
		t.Fatal(err)
	}
	groups, err := ExpandFailoverGroups(sel, PlanOptions{
		StickyBackendID: "b",
		Session:         &SessionRoutingState{FirstRequestConsumed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Candidates) != 1 || groups[0].Candidates[0].Key != "b:m" {
		t.Fatalf("sticky group: got %#v", groups)
	}
	next := groups[0].NextThinkerCycle
	if next == nil || next.SelectorKey != "a:m^b:m" || next.NextIndex != 1 {
		t.Fatalf("next cycle: got %+v want selector a:m^b:m index 1", next)
	}
}

func TestExpandFailoverGroups_StickyThinkerCycleAdvancesOnNextTurn(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m")
	if err != nil {
		t.Fatal(err)
	}
	groups, err := ExpandFailoverGroups(sel, PlanOptions{
		StickyBackendID: "b",
		Session:         &SessionRoutingState{FirstRequestConsumed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	next := groups[0].NextThinkerCycle
	if next == nil {
		t.Fatal("sticky executor must persist next thinker cycle")
	}
	groups2, err := ExpandFailoverGroups(sel, PlanOptions{
		Session:      &SessionRoutingState{FirstRequestConsumed: true},
		ThinkerCycle: *next,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups2) != 1 || groups2[0].Candidates[0].Key != "a:m" {
		t.Fatalf("cycle after sticky executor: got %#v want thinker a:m", groups2[0].Candidates)
	}
	if groups2[0].Candidates[0].InterleavedRole != interleavedstate.RoleThinker {
		t.Fatalf("role: got %q want thinker", groups2[0].Candidates[0].InterleavedRole)
	}
}

func TestStickyCandidate_HybridParallelLegMatchesBackend(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m!c:m")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{
		StickyBackendID: "b",
		Session:         &SessionRoutingState{FirstRequestConsumed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "b:m" {
		t.Fatalf("got %#v want sticky parallel leg b:m", out)
	}
	if !out[0].IsParallel {
		t.Fatal("hybrid sticky leg must be parallel")
	}
	if out[0].InterleavedRole != interleavedstate.RoleExecutor {
		t.Fatalf("role: got %q want executor", out[0].InterleavedRole)
	}
	if out[0].SelectorKey != "a:m^parallel:b:m!c:m" {
		t.Fatalf("selector key: got %q", out[0].SelectorKey)
	}
}

func TestExpandFailoverGroups_StickyHybridParallelSetsNextThinkerCycle(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m!c:m")
	if err != nil {
		t.Fatal(err)
	}
	groups, err := ExpandFailoverGroups(sel, PlanOptions{
		StickyBackendID: "b",
		Session:         &SessionRoutingState{FirstRequestConsumed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups[0].Candidates) != 1 || groups[0].Candidates[0].Key != "b:m" {
		t.Fatalf("hybrid sticky group: got %#v", groups[0].Candidates)
	}
	if groups[0].NextThinkerCycle == nil || groups[0].NextThinkerCycle.NextIndex != 1 {
		t.Fatalf("next cycle after hybrid sticky: %+v", groups[0].NextThinkerCycle)
	}
}

func TestStickyCandidate_LegacyWeightedNoInterleavedRole(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[weight=100]a:m^[weight=1]b:m")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{
		StickyBackendID: "b",
		Rand:            rng(0),
		Session:         &SessionRoutingState{FirstRequestConsumed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "b:m" {
		t.Fatalf("got %#v want sticky b:m", out)
	}
	if out[0].InterleavedRole != interleavedstate.RoleNone {
		t.Fatalf("legacy sticky role: got %q want none", out[0].InterleavedRole)
	}
	if out[0].SelectorKey != "" {
		t.Fatalf("legacy sticky selector key: got %q want empty", out[0].SelectorKey)
	}
}
