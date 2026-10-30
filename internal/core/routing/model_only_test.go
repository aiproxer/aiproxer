package routing_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

func TestApplyModelOnlyBackends(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("gpt-4")
	if err != nil {
		t.Fatal(err)
	}
	routing.ApplyModelOnlyBackends(sel, "openai")
	if routing.SelectorHasEmptyBackend(sel) {
		t.Fatal("expected backend filled")
	}
	list, err := routing.ExpandFailover(sel, routing.PlanOptions{Rand: fixedRng{}})
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Primary.Backend != "openai" || list[0].Primary.Model != "gpt-4" {
		t.Fatalf("got %#v", list[0].Primary)
	}
}

func TestDefaultBackendFromRouteSelector(t *testing.T) {
	t.Parallel()
	b, err := routing.DefaultBackendFromRouteSelector("openai:gpt-4")
	if err != nil || b != "openai" {
		t.Fatalf("got %q %v", b, err)
	}
	if _, err := routing.DefaultBackendFromRouteSelector(""); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultBackendFromRouteSelector_HybridParallelFirstBranch(t *testing.T) {
	t.Parallel()
	b, err := routing.DefaultBackendFromRouteSelector("b:m!c:m^[thinker]a:m")
	if err != nil || b != "b" {
		t.Fatalf("got %q %v", b, err)
	}
}

func TestSelectorHasRequestSizeConstraints_HybridParallelLeg(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("[thinker]a:m^[max_context=10]b:m!c:m")
	if err != nil {
		t.Fatal(err)
	}
	if !routing.SelectorHasRequestSizeConstraints(sel) {
		t.Fatal("embedded parallel leg max_context must be detected")
	}
}

func TestApplyModelOnlyBackendsParallelBranches(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("gpt-4!claude")
	if err != nil {
		t.Fatal(err)
	}
	routing.ApplyModelOnlyBackends(sel, "openai")
	if routing.SelectorHasEmptyBackend(sel) {
		t.Fatal("expected all backends filled in parallel branches")
	}
}

func TestSelectorHasEmptyBackendParallel(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("openai:gpt-4!claude")
	if err != nil {
		t.Fatal(err)
	}
	if !routing.SelectorHasEmptyBackend(sel) {
		t.Fatal("model-only parallel branch should report empty backend")
	}
}

func TestSelectorHasEmptyBackendThinkerHybridParallel(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("[thinker]a:m^b:m!c:m")
	if err != nil {
		t.Fatal(err)
	}
	if routing.SelectorHasEmptyBackend(sel) {
		t.Fatal("embedded parallel legs with backends must not report empty backend")
	}
}

func TestApplyModelOnlyBackendsThinkerHybridParallel(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("[thinker]:m^:m!:m")
	if err != nil {
		t.Fatal(err)
	}
	routing.ApplyModelOnlyBackends(sel, "openai")
	if routing.SelectorHasEmptyBackend(sel) {
		t.Fatal("expected embedded parallel legs filled")
	}
	w := sel.Alternatives[0].Weighted
	if w == nil || len(w.Branches) != 2 || w.Branches[1].Parallel == nil {
		t.Fatalf("unexpected hybrid shape: %#v", sel)
	}
	for _, leg := range w.Branches[1].Parallel.Branches {
		if leg.Target.Backend != "openai" {
			t.Fatalf("parallel leg backend: got %q want openai", leg.Target.Backend)
		}
	}
}

type fixedRng struct{}

func (fixedRng) Intn(n int) int { return 0 }
