package routing

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
)

// Task 2.3: thinker-aware weighted cycle selection.

func TestBuildThinkerCycle_RepeatsNonThinkerByWeightAppendsThinkerOnce(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		sel         string
		wantEntries []interleavedstate.CycleEntry
		wantKey     string
	}{
		{
			name: "weight one each, thinker last",
			sel:  "[thinker]a:m^b:m",
			wantEntries: []interleavedstate.CycleEntry{
				{Key: "b:m", Role: interleavedstate.RoleExecutor},
				{Key: "a:m", Role: interleavedstate.RoleThinker},
			},
			wantKey: "a:m^b:m",
		},
		{
			name: "executor weight two repeats",
			sel:  "[thinker]a:m^[weight=2]b:m",
			wantEntries: []interleavedstate.CycleEntry{
				{Key: "b:m", Role: interleavedstate.RoleExecutor},
				{Key: "b:m", Role: interleavedstate.RoleExecutor},
				{Key: "a:m", Role: interleavedstate.RoleThinker},
			},
			wantKey: "a:m^b:m",
		},
		{
			name: "two non-thinker branches each repeated by weight",
			sel:  "[weight=2]a:m^[thinker]b:m^c:m",
			wantEntries: []interleavedstate.CycleEntry{
				{Key: "a:m", Role: interleavedstate.RoleExecutor},
				{Key: "a:m", Role: interleavedstate.RoleExecutor},
				{Key: "c:m", Role: interleavedstate.RoleExecutor},
				{Key: "b:m", Role: interleavedstate.RoleThinker},
			},
			wantKey: "a:m^b:m^c:m",
		},
		{
			name: "hybrid parallel executor branch appears once",
			sel:  "[thinker]a:m^b:m!c:m",
			wantEntries: []interleavedstate.CycleEntry{
				{Key: "parallel:b:m!c:m", Role: interleavedstate.RoleExecutor},
				{Key: "a:m", Role: interleavedstate.RoleThinker},
			},
			wantKey: "a:m^parallel:b:m!c:m",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := Parse(tc.sel)
			if err != nil {
				t.Fatal(err)
			}
			w := sel.Alternatives[0].Weighted
			entries, key := buildThinkerCycle(w)
			if key != tc.wantKey {
				t.Fatalf("selector key: got %q want %q", key, tc.wantKey)
			}
			if len(entries) != len(tc.wantEntries) {
				t.Fatalf("entries len: got %d want %d (%+v)", len(entries), len(tc.wantEntries), entries)
			}
			for i, e := range entries {
				if e.Key != tc.wantEntries[i].Key || e.Role != tc.wantEntries[i].Role {
					t.Fatalf("entry %d: got %+v want %+v", i, e, tc.wantEntries[i])
				}
			}
		})
	}
}

func TestBuildThinkerCycle_ClampsHugeWeights(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^[weight=100000000]b:m")
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := buildThinkerCycle(sel.Alternatives[0].Weighted)
	if len(entries) != int(maxThinkerCycleWeightRepeats)+1 {
		t.Fatalf("entries len: got %d want %d", len(entries), int(maxThinkerCycleWeightRepeats)+1)
	}
	if entries[len(entries)-1].Role != interleavedstate.RoleThinker {
		t.Fatalf("last entry must remain thinker, got %+v", entries[len(entries)-1])
	}
}

func TestPickThinkerCycle_InvalidNextIndexNormalized(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	sess := &SessionRoutingState{FirstRequestConsumed: true}
	seq := []interleavedstate.CycleEntry{
		{Key: "b:m", Role: interleavedstate.RoleExecutor},
		{Key: "a:m", Role: interleavedstate.RoleThinker},
	}
	for _, badIdx := range []int{-1, 99} {
		t.Run(fmt.Sprintf("NextIndex=%d", badIdx), func(t *testing.T) {
			t.Parallel()
			stale := interleavedstate.CycleState{
				SelectorKey: "a:m^b:m",
				Sequence:    seq,
				NextIndex:   badIdx,
			}
			cands, _, next, err := pickWeighted(w, PlanOptions{Session: sess, ThinkerCycle: stale})
			if err != nil {
				t.Fatal(err)
			}
			if len(cands) != 1 || cands[0].Key != "b:m" {
				t.Fatalf("normalized pick: got %+v want b:m", cands)
			}
			if next == nil || next.NextIndex != 1 {
				t.Fatalf("next cursor: got %+v want NextIndex 1", next)
			}
		})
	}
}

func TestPickThinkerCycle_AdvancesCursorAndWraps(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^[weight=2]b:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	sess := &SessionRoutingState{FirstRequestConsumed: true}
	want := []string{"b:m", "b:m", "a:m", "b:m", "b:m", "a:m"}
	var state *interleavedstate.CycleState
	for i, wantKey := range want {
		opt := PlanOptions{Session: sess}
		if state != nil {
			opt.ThinkerCycle = *state
		}
		cands, consumeFirst, next, err := pickWeighted(w, opt)
		if err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
		if consumeFirst {
			t.Fatalf("turn %d: unexpected [first] consumption", i)
		}
		if len(cands) != 1 || cands[0].Key != wantKey {
			t.Fatalf("turn %d: got %+v want %q", i, cands, wantKey)
		}
		if next == nil {
			t.Fatalf("turn %d: nil next cycle state", i)
		}
		state = next
	}
	// Cursor wraps to 0 after the thinker; final state should point at position 0.
	if state.NextIndex != 0 {
		t.Fatalf("final NextIndex: got %d want 0", state.NextIndex)
	}
}

func TestPickThinkerCycle_ThinkerCandidateCarriesRoleAndSelectorKey(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	sess := &SessionRoutingState{FirstRequestConsumed: true}
	// First cycle position is the executor (b:m); advance once, then thinker.
	first, _, next, err := pickWeighted(w, PlanOptions{Session: sess})
	if err != nil {
		t.Fatal(err)
	}
	if first[0].InterleavedRole != interleavedstate.RoleExecutor {
		t.Fatalf("executor role: got %q want %q", first[0].InterleavedRole, interleavedstate.RoleExecutor)
	}
	if first[0].SelectorKey != "a:m^b:m" {
		t.Fatalf("executor selector key: got %q", first[0].SelectorKey)
	}
	second, _, _, err := pickWeighted(w, PlanOptions{Session: sess, ThinkerCycle: *next})
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Key != "a:m" || second[0].InterleavedRole != interleavedstate.RoleThinker {
		t.Fatalf("thinker candidate: got %+v", second[0])
	}
	if second[0].SelectorKey != "a:m^b:m" {
		t.Fatalf("thinker selector key: got %q", second[0].SelectorKey)
	}
}

func TestPickThinkerCycle_StaleSelectorResetsCursor(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	sess := &SessionRoutingState{FirstRequestConsumed: true}
	// Feed a stale state with a different selector key and a cursor pointing past the end.
	stale := interleavedstate.CycleState{
		SelectorKey: "other:m^selector:m",
		Sequence:    []interleavedstate.CycleEntry{{Key: "other:m", Role: interleavedstate.RoleExecutor}},
		NextIndex:   0,
	}
	cands, _, next, err := pickWeighted(w, PlanOptions{Session: sess, ThinkerCycle: stale})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Key != "b:m" {
		t.Fatalf("stale reset should pick cycle position 0 (b:m), got %+v", cands)
	}
	if next == nil || next.SelectorKey != "a:m^b:m" {
		t.Fatalf("next state selector key: %+v", next)
	}
	if next.NextIndex != 1 {
		t.Fatalf("next cursor after pick: got %d want 1", next.NextIndex)
	}
}

func TestPickThinkerCycle_StaleSequenceResetsCursor(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	sess := &SessionRoutingState{FirstRequestConsumed: true}
	// Selector key matches the fresh build, but the stored sequence is reversed and
	// the cursor points at a position whose stored meaning differs from the fresh
	// build. Without a sequence-equality check the planner would honor the stale
	// cursor and land on the thinker (a:m) instead of resetting to position 0.
	stale := interleavedstate.CycleState{
		SelectorKey: "a:m^b:m",
		Sequence: []interleavedstate.CycleEntry{
			{Key: "a:m", Role: interleavedstate.RoleThinker},
			{Key: "b:m", Role: interleavedstate.RoleExecutor},
		},
		NextIndex: 1,
	}
	cands, _, next, err := pickWeighted(w, PlanOptions{Session: sess, ThinkerCycle: stale})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Key != "b:m" {
		t.Fatalf("stale sequence reset should pick fresh position 0 (b:m), got %+v", cands)
	}
	if cands[0].InterleavedRole != interleavedstate.RoleExecutor {
		t.Fatalf("stale sequence reset role: got %q want executor", cands[0].InterleavedRole)
	}
	if next == nil || next.SelectorKey != "a:m^b:m" {
		t.Fatalf("next state selector key: %+v", next)
	}
	wantSeq := []interleavedstate.CycleEntry{
		{Key: "b:m", Role: interleavedstate.RoleExecutor},
		{Key: "a:m", Role: interleavedstate.RoleThinker},
	}
	if !next.Equal(interleavedstate.CycleState{SelectorKey: "a:m^b:m", Sequence: wantSeq, NextIndex: 1}) {
		t.Fatalf("next state must carry fresh sequence and cursor 1: %+v", next)
	}
}

func TestPickThinkerCycle_FirstRequestCandidateCarriesRoleAndSelectorKey(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first]b:m^[thinker]a:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	sess := &SessionRoutingState{}
	cands, _, _, err := pickWeighted(w, PlanOptions{Session: sess})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Key != "b:m" || !cands[0].MarkedFirst {
		t.Fatalf("first pick: got %+v want b:m marked first", cands)
	}
	if cands[0].InterleavedRole != interleavedstate.RoleExecutor {
		t.Fatalf("first candidate role: got %q want %q", cands[0].InterleavedRole, interleavedstate.RoleExecutor)
	}
	if cands[0].SelectorKey != "b:m^a:m" {
		t.Fatalf("first candidate selector key: got %q want %q", cands[0].SelectorKey, "b:m^a:m")
	}
}

func TestPickThinkerCycle_FirstRequestHonoredBeforeCycle(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first]b:m^[thinker]a:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	sess := &SessionRoutingState{}
	cands, consumeFirst, next, err := pickWeighted(w, PlanOptions{Session: sess})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Key != "b:m" {
		t.Fatalf("first pick: got %+v want b:m", cands)
	}
	if !cands[0].MarkedFirst {
		t.Fatal("first branch must be marked first")
	}
	if !consumeFirst {
		t.Fatal("consumeFirst must be true when [first] is honored")
	}
	if sess.FirstRequestConsumed {
		t.Fatal("pickWeighted must not mutate session; caller consumes [first]")
	}
	if next == nil || next.NextIndex != 0 {
		t.Fatalf("next cursor after [first]: %+v want NextIndex 0", next)
	}
	sess.FirstRequestConsumed = true
	cands2, consumeFirst2, _, err := pickWeighted(w, PlanOptions{Session: sess, ThinkerCycle: *next})
	if err != nil {
		t.Fatal(err)
	}
	if consumeFirst2 {
		t.Fatal("[first] must not be honored when valid cycle state exists")
	}
	if len(cands2) != 1 || cands2[0].Key != "b:m" {
		t.Fatalf("second pick: got %+v want b:m", cands2)
	}
	if cands2[0].MarkedFirst {
		t.Fatal("cycle pick must not be marked first")
	}
}

func TestPickThinkerCycle_SuppressThinkerPicksExecutor(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	sess := &SessionRoutingState{FirstRequestConsumed: true}
	// Cursor pointing at the thinker position; suppression must skip it.
	state := interleavedstate.CycleState{
		SelectorKey: "a:m^b:m",
		Sequence: []interleavedstate.CycleEntry{
			{Key: "b:m", Role: interleavedstate.RoleExecutor},
			{Key: "a:m", Role: interleavedstate.RoleThinker},
		},
		NextIndex: 1,
	}
	cands, _, _, err := pickWeighted(w, PlanOptions{Session: sess, ThinkerCycle: state, SuppressThinker: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Key != "b:m" {
		t.Fatalf("suppressed pick: got %+v want b:m", cands)
	}
	if cands[0].InterleavedRole != interleavedstate.RoleExecutor {
		t.Fatalf("suppressed pick role: got %q", cands[0].InterleavedRole)
	}
}

func TestPickThinkerCycle_SuppressThinkerNoExecutorReturnsNoEligible(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	sess := &SessionRoutingState{FirstRequestConsumed: true}
	ex := map[string]struct{}{"b:m": {}}
	_, _, _, err = pickWeighted(w, PlanOptions{
		Session:         sess,
		Excluded:        ex,
		SuppressThinker: true,
	})
	if !errors.Is(err, ErrNoEligibleCandidate) {
		t.Fatalf("got %v want ErrNoEligibleCandidate", err)
	}
}

func TestPickThinkerCycle_HybridParallelExecutorReturnsLegs(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m!c:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	sess := &SessionRoutingState{FirstRequestConsumed: true}
	// Cycle position 0 is the parallel executor branch.
	cands, _, _, err := pickWeighted(w, PlanOptions{Session: sess})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("parallel executor legs: got %d want 2", len(cands))
	}
	for _, c := range cands {
		if !c.IsParallel {
			t.Fatalf("candidate %q must be parallel", c.Key)
		}
		if c.InterleavedRole != interleavedstate.RoleExecutor {
			t.Fatalf("candidate %q role: got %q want executor", c.Key, c.InterleavedRole)
		}
		if c.SelectorKey != "a:m^parallel:b:m!c:m" {
			t.Fatalf("candidate %q selector key: got %q", c.Key, c.SelectorKey)
		}
	}
	if cands[0].Key != "b:m" || cands[1].Key != "c:m" {
		t.Fatalf("leg order: %v %v", cands[0].Key, cands[1].Key)
	}
}

func TestPickThinkerCycle_HybridThinkerPickedThenSuppressedExecutor(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m!c:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	sess := &SessionRoutingState{FirstRequestConsumed: true}
	// Advance to the thinker (position 1).
	exec, _, next, err := pickWeighted(w, PlanOptions{Session: sess})
	if err != nil {
		t.Fatal(err)
	}
	if exec[0].InterleavedRole != interleavedstate.RoleExecutor {
		t.Fatalf("first hybrid pick must be executor, got %+v", exec[0])
	}
	thinker, _, next2, err := pickWeighted(w, PlanOptions{Session: sess, ThinkerCycle: *next})
	if err != nil {
		t.Fatal(err)
	}
	if len(thinker) != 1 || thinker[0].Key != "a:m" || thinker[0].InterleavedRole != interleavedstate.RoleThinker {
		t.Fatalf("thinker pick: got %+v", thinker)
	}
	// Continuation suppresses thinker; cursor wraps to 0 (parallel executor).
	cont, _, _, err := pickWeighted(w, PlanOptions{
		Session:         sess,
		ThinkerCycle:    *next2,
		SuppressThinker: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cont) != 2 {
		t.Fatalf("continuation executor legs: got %d want 2", len(cont))
	}
	for _, c := range cont {
		if c.InterleavedRole != interleavedstate.RoleExecutor {
			t.Fatalf("continuation leg %q role: got %q", c.Key, c.InterleavedRole)
		}
	}
}

func TestPickWeighted_NonThinkerWeightedPreservesRNGRoll(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[weight=1]a:x^[weight=1]b:y")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	out, _, _, err := pickWeighted(w, PlanOptions{Rand: rng(0), Session: &SessionRoutingState{FirstRequestConsumed: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "a:x" {
		t.Fatalf("non-thinker RNG roll: got %+v want a:x", out)
	}
	if out[0].InterleavedRole != interleavedstate.RoleNone {
		t.Fatalf("non-thinker candidate role must be none, got %q", out[0].InterleavedRole)
	}
	if out[0].SelectorKey != "" {
		t.Fatalf("non-thinker candidate selector key must be empty, got %q", out[0].SelectorKey)
	}
}

func TestPickThinkerCycle_FirstEligibleAfterExcludedCycleTurn(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first]cheap:m^[thinker]a:m^expensive:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	sess := &SessionRoutingState{FirstRequestConsumed: false}
	_, _, next, err := pickWeighted(w, PlanOptions{
		Session:  sess,
		Excluded: map[string]struct{}{"cheap:m": {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if next == nil {
		t.Fatal("turn 1 must persist valid thinker cycle")
	}
	if sess.FirstRequestConsumed {
		t.Fatal("[first] must not be consumed when cheap was excluded on turn 1")
	}
	cands, consumeFirst, _, err := pickWeighted(w, PlanOptions{
		Session:      sess,
		ThinkerCycle: *next,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Key != "cheap:m" || !cands[0].MarkedFirst {
		t.Fatalf("turn 2: got %+v want cheap:m marked first", cands)
	}
	if !consumeFirst {
		t.Fatal("turn 2 must consume [first]")
	}
	if sess.FirstRequestConsumed {
		t.Fatal("pickWeighted must not mutate session on turn 2")
	}
}

func TestExpandFailover_ThinkerFirstRequestCallerMutatesSession(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first]b:m^[thinker]a:m")
	if err != nil {
		t.Fatal(err)
	}
	sess := &SessionRoutingState{}
	out, err := ExpandFailover(sel, PlanOptions{Session: sess})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "b:m" || !out[0].MarkedFirst {
		t.Fatalf("first pick: got %+v want b:m marked first", out)
	}
	if !sess.FirstRequestConsumed {
		t.Fatal("ExpandFailover must consume [first] for caller")
	}
}

func TestPickThinkerCycle_RetryPathStillIgnoresFirstWithValidCycle(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first]cheap:m^[thinker]a:m^expensive:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	sess := &SessionRoutingState{FirstRequestConsumed: false}
	state := interleavedstate.CycleState{
		SelectorKey: "cheap:m^a:m^expensive:m",
		Sequence: []interleavedstate.CycleEntry{
			{Key: "cheap:m", Role: interleavedstate.RoleExecutor},
			{Key: "expensive:m", Role: interleavedstate.RoleExecutor},
			{Key: "a:m", Role: interleavedstate.RoleThinker},
		},
		NextIndex: 0,
	}
	cands, _, _, err := pickWeighted(w, PlanOptions{
		Session:      sess,
		ThinkerCycle: state,
		IsRetryPath:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Key != "cheap:m" || cands[0].MarkedFirst {
		t.Fatalf("retry path must not honor [first]: got %+v", cands[0])
	}
}

func TestExpandFailover_ThinkerWeightedProducesCycleCandidate(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{Session: &SessionRoutingState{FirstRequestConsumed: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "b:m" {
		t.Fatalf("expand failover thinker cycle position 0: got %+v want b:m", out)
	}
	if out[0].InterleavedRole != interleavedstate.RoleExecutor {
		t.Fatalf("expand failover role: got %q", out[0].InterleavedRole)
	}
}
