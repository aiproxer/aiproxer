package routing

import (
	"errors"
	"testing"
)

// Task 2.2: narrow thinker + parallel executor hybrid selector form.
// One thinker weighted branch plus one non-thinker weighted branch whose
// target is an embedded parallel executor group. General weighted/parallel
// mixing stays rejected.

func TestParseThinkerParallelHybridAccepted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		in              string
		thinkerAt       int
		executorAt      int
		thinkerModel    string
		parallelLegs    int
		firstLegBackend string
		firstLegModel   string
	}{
		{
			name:            "thinker first then parallel executor",
			in:              "[thinker]a:m^b:m!c:m",
			thinkerAt:       0,
			executorAt:      1,
			thinkerModel:    "m",
			parallelLegs:    2,
			firstLegBackend: "b",
			firstLegModel:   "m",
		},
		{
			name:            "parallel executor first then thinker",
			in:              "b:m!c:m^[thinker]a:m",
			thinkerAt:       1,
			executorAt:      0,
			thinkerModel:    "m",
			parallelLegs:    2,
			firstLegBackend: "b",
			firstLegModel:   "m",
		},
		{
			name:            "executor leg handicap preserved",
			in:              "[thinker]a:m^[handicap=10]b:m!c:m",
			thinkerAt:       0,
			executorAt:      1,
			parallelLegs:    2,
			firstLegBackend: "b",
			firstLegModel:   "m",
		},
		{
			name:            "executor with three parallel legs",
			in:              "[thinker]a:m^b:m!c:m!d:m",
			thinkerAt:       0,
			executorAt:      1,
			parallelLegs:    3,
			firstLegBackend: "b",
			firstLegModel:   "m",
		},
		{
			name:            "thinker weight preserved, executor weight fixed",
			in:              "[thinker][weight=2]a:m^b:m!c:m",
			thinkerAt:       0,
			executorAt:      1,
			parallelLegs:    2,
			firstLegBackend: "b",
			firstLegModel:   "m",
		},
		{
			name:            "failover around hybrid",
			in:              "[thinker]a:m^b:m!c:m|d:e",
			thinkerAt:       0,
			executorAt:      1,
			parallelLegs:    2,
			firstLegBackend: "b",
			firstLegModel:   "m",
		},
		{
			name:            "executor leg query params preserved",
			in:              "[thinker]a:m^b:m?temp=0.3!c:m",
			thinkerAt:       0,
			executorAt:      1,
			parallelLegs:    2,
			firstLegBackend: "b",
			firstLegModel:   "m",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("expected accept, got error: %v", err)
			}
			// For failover-around-hybrid, the hybrid is arm 0; otherwise arm 0 is the only arm.
			alt := sel.Alternatives[0]
			w := alt.Weighted
			if w == nil {
				t.Fatalf("expected weighted selector, got %#v", alt)
			}
			if len(w.Branches) != 2 {
				t.Fatalf("expected 2 weighted branches, got %d: %#v", len(w.Branches), w.Branches)
			}
			tb := w.Branches[tc.thinkerAt]
			eb := w.Branches[tc.executorAt]
			if !tb.IsThinker {
				t.Fatalf("thinker branch %d not marked thinker: %#v", tc.thinkerAt, tb)
			}
			if eb.IsThinker {
				t.Fatalf("executor branch %d unexpectedly marked thinker: %#v", tc.executorAt, eb)
			}
			if tb.Parallel != nil {
				t.Fatalf("thinker branch must not carry an embedded parallel group: %#v", tb)
			}
			if eb.Parallel == nil {
				t.Fatalf("executor branch %d missing embedded parallel group: %#v", tc.executorAt, eb)
			}
			if len(eb.Parallel.Branches) != tc.parallelLegs {
				t.Fatalf("parallel legs: got %d want %d", len(eb.Parallel.Branches), tc.parallelLegs)
			}
			if tc.thinkerModel != "" && tb.Target.Model != tc.thinkerModel {
				t.Fatalf("thinker model: got %q want %q", tb.Target.Model, tc.thinkerModel)
			}
			leg0 := eb.Parallel.Branches[0]
			if leg0.Target.Backend != tc.firstLegBackend || leg0.Target.Model != tc.firstLegModel {
				t.Fatalf("executor leg0: got %q:%q want %q:%q", leg0.Target.Backend, leg0.Target.Model, tc.firstLegBackend, tc.firstLegModel)
			}
		})
	}
}

func TestParseThinkerParallelHybridExecutorLegHandicapAndQuery(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^[handicap=10]b:m?temp=0.3!c:m")
	if err != nil {
		t.Fatal(err)
	}
	eb := sel.Alternatives[0].Weighted.Branches[1]
	if eb.Parallel == nil || len(eb.Parallel.Branches) != 2 {
		t.Fatalf("executor parallel: %#v", eb)
	}
	if eb.Parallel.Branches[0].Handicap.String() != "10s" {
		t.Fatalf("leg0 handicap: %v", eb.Parallel.Branches[0].Handicap)
	}
	if eb.Parallel.Branches[0].Target.Params.Get("temp") != "0.3" {
		t.Fatalf("leg0 params: %v", eb.Parallel.Branches[0].Target.Params)
	}
}

func TestParseThinkerParallelHybridWeightsRecorded(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker][weight=2]a:m^b:m!c:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	if w.Branches[0].Weight != 2 {
		t.Fatalf("thinker weight: %d", w.Branches[0].Weight)
	}
	if w.Branches[1].Weight != 1 {
		t.Fatalf("executor weight: got %d want 1", w.Branches[1].Weight)
	}
}

func TestParseThinkerParallelHybridRejectsInvalidShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{name: "no thinker general mixing", in: "a:m^b:m!c:m"},
		{name: "two thinker branches", in: "[thinker]a:m^[thinker]b:m!c:m"},
		{name: "thinker branch carries embedded parallel", in: "[thinker]a:m!x:m^b:m!c:m"},
		{name: "both branches carry embedded parallel", in: "a:m!x:m^[thinker]b:m!c:m"},
		{name: "thinker inside embedded parallel leg", in: "[thinker]a:m^b:m![thinker]c:m"},
		{name: "first on executor branch", in: "[thinker]a:m^[first]b:m!c:m"},
		{name: "thinker on executor branch prefix", in: "[thinker]a:m^[thinker]b:m!c:m"},
		{name: "weight on executor branch", in: "[thinker]a:m^[weight=3]b:m!c:m"},
		{name: "malformed parallel empty leg", in: "[thinker]a:m^b:m!!c:m"},
		{name: "three weighted branches with mix", in: "[thinker]a:m^b:m^c:m!d:m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(tc.in)
			if err == nil {
				t.Fatal("expected rejection, got nil error")
			}
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("expected ErrInvalidSelector, got %v", err)
			}
		})
	}
}

func TestParseThinkerParallelHybridPreservesGeneralMixingRejection(t *testing.T) {
	t.Parallel()
	cases := []string{
		"a:m!b:m^c:m",
		"[weight=2]a:m!b:m",
		"[first]a:m!b:m",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(in)
			if err == nil {
				t.Fatal("expected rejection for general weighted/parallel mixing")
			}
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("expected ErrInvalidSelector, got %v", err)
			}
		})
	}
}
