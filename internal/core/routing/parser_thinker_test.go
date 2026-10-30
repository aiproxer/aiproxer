package routing

import (
	"errors"
	"testing"
)

// Task 2.1: thinker annotation parsing and validation on weighted branches.

func TestParseThinkerAcceptedForms(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		in        string
		thinkerAt int
	}{
		{name: "bare in two-branch weighted", in: "[thinker]a:m^b:m", thinkerAt: 0},
		{name: "bare on second branch", in: "a:m^[thinker]b:m", thinkerAt: 1},
		{name: "true value", in: "[thinker=true]a:m^b:m", thinkerAt: 0},
		{name: "yes value", in: "[thinker=yes]a:m^b:m", thinkerAt: 0},
		{name: "one value", in: "[thinker=1]a:m^b:m", thinkerAt: 0},
		{name: "true case-insensitive", in: "[thinker=TRUE]a:m^b:m", thinkerAt: 0},
		{name: "with weight after thinker", in: "[thinker][weight=2]a:m^b:m", thinkerAt: 0},
		{name: "with weight before thinker", in: "[weight=2][thinker]a:m^b:m", thinkerAt: 0},
		{name: "with max_context", in: "[thinker][max_context=4096]a:m^b:m", thinkerAt: 0},
		{name: "with ttft_timeout", in: "[thinker][ttft_timeout=10]a:m^b:m", thinkerAt: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("expected accept, got error: %v", err)
			}
			w := sel.Alternatives[0].Weighted
			if w == nil {
				t.Fatalf("expected weighted selector, got %#v", sel.Alternatives[0])
			}
			if !w.Branches[tc.thinkerAt].IsThinker {
				t.Fatalf("branch %d not marked thinker: %#v", tc.thinkerAt, w.Branches[tc.thinkerAt])
			}
			for i, b := range w.Branches {
				if i == tc.thinkerAt {
					continue
				}
				if b.IsThinker {
					t.Fatalf("branch %d unexpectedly marked thinker", i)
				}
			}
		})
	}
}

func TestParseThinkerRejectsThinkerOnlyWeightedSelector(t *testing.T) {
	t.Parallel()
	cases := []string{
		"[thinker]a:m",
		"[thinker=true]a:m",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(in)
			if err == nil {
				t.Fatal("expected rejection for thinker-only weighted selector")
			}
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("expected ErrInvalidSelector, got %v", err)
			}
		})
	}
}

func TestParseThinkerRejectedValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{name: "false", in: "[thinker=false]a:m^b:m"},
		{name: "no", in: "[thinker=no]a:m^b:m"},
		{name: "zero", in: "[thinker=0]a:m^b:m"},
		{name: "empty value", in: "[thinker=]a:m^b:m"},
		{name: "unrecognized value", in: "[thinker=foo]a:m^b:m"},
		{name: "two is not boolean", in: "[thinker=2]a:m^b:m"},
		{name: "y is not recognized", in: "[thinker=y]a:m^b:m"},
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

func TestParseThinkerRejectsDuplicateInWeightedGroup(t *testing.T) {
	t.Parallel()
	cases := []string{
		"[thinker]a:m^[thinker]b:m",
		"[thinker]a:m^[thinker=true]b:m",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(in)
			if err == nil {
				t.Fatal("expected rejection for duplicate thinker branches")
			}
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("expected ErrInvalidSelector, got %v", err)
			}
		})
	}
}

func TestParseThinkerRejectsFirstPlusThinkerOnSameBranch(t *testing.T) {
	t.Parallel()
	cases := []string{
		"[first][thinker]a:m^b:m",
		"[thinker][first]a:m^b:m",
		"[first,thinker]a:m^b:m",
		"[thinker,first]a:m^b:m",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(in)
			if err == nil {
				t.Fatal("expected rejection for [first] plus [thinker] on same branch")
			}
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("expected ErrInvalidSelector, got %v", err)
			}
		})
	}
}

func TestParseThinkerRejectsDuplicateOnSameBranch(t *testing.T) {
	t.Parallel()
	_, err := Parse("[thinker][thinker]a:m^b:m")
	if err == nil {
		t.Fatal("expected rejection for duplicate thinker annotation on same branch")
	}
	if !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("expected ErrInvalidSelector, got %v", err)
	}
}

func TestParseThinkerRejectsOutsideWeightedContext(t *testing.T) {
	t.Parallel()
	cases := []string{
		// Parallel arm with thinker prefix: mixes weighted annotation with parallel.
		"[thinker]a:m!b:m",
		// Thinker annotation on a parallel leg.
		"a:m![thinker]b:m",
		// Thinker annotation on a parallel leg with handicap sibling.
		"[handicap=1]a:m![thinker]b:m",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(in)
			if err == nil {
				t.Fatal("expected rejection for thinker outside weighted context")
			}
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("expected ErrInvalidSelector, got %v", err)
			}
		})
	}
}

func TestParseThinkerPreservesOtherAnnotations(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker][weight=3][max_context=4096]a:m^[weight=1]b:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	if w == nil || len(w.Branches) != 2 {
		t.Fatalf("weighted branches: %#v", sel.Alternatives[0])
	}
	b0 := w.Branches[0]
	if !b0.IsThinker {
		t.Fatalf("branch0 not thinker: %#v", b0)
	}
	if b0.Weight != 3 {
		t.Fatalf("branch0 weight: got %d want 3", b0.Weight)
	}
	if b0.Target.Size.MaxContextTokens == nil || *b0.Target.Size.MaxContextTokens != 4096 {
		t.Fatalf("branch0 max_context: %#v", b0.Target.Size)
	}
	if b0.IsFirst {
		t.Fatalf("branch0 must not be first")
	}
	if w.Branches[1].Weight != 1 || w.Branches[1].IsThinker {
		t.Fatalf("branch1: %#v", w.Branches[1])
	}
}

func TestParseThinkerRejectsHybridExecutorSideAnnotations(t *testing.T) {
	t.Parallel()
	cases := []string{
		"[thinker]a:m^[first]b:m!c:m",
		"[thinker]a:m^[weight=2]b:m!c:m",
		"[thinker]a:m^[thinker]b:m!c:m",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(in)
			if err == nil {
				t.Fatal("expected rejection for executor-side hybrid annotation")
			}
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("expected ErrInvalidSelector, got %v", err)
			}
		})
	}
}

func TestParseThinkerFailoverAroundWeightedStillAccepted(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m|c:d")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Alternatives) != 2 {
		t.Fatalf("want 2 failover arms, got %d", len(sel.Alternatives))
	}
	w := sel.Alternatives[0].Weighted
	if w == nil || !w.Branches[0].IsThinker {
		t.Fatalf("arm0 weighted thinker: %#v", sel.Alternatives[0])
	}
	if sel.Alternatives[1].Primary == nil || sel.Alternatives[1].Primary.Model != "d" {
		t.Fatalf("arm1 primary: %#v", sel.Alternatives[1])
	}
}
