package routing

import (
	"testing"
	"time"
)

func TestExpandFailoverParallelArmReturnsAllLegs(t *testing.T) {
	t.Parallel()
	sel, err := Parse("nvidia:m1!nvidia:m2!nvidia:m3")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// A parallel arm should return all three legs as candidates in the same failover step.
	if len(out) != 3 {
		t.Fatalf("expected 3 parallel legs, got %d", len(out))
	}
	if out[0].Key != "nvidia:m1" || out[1].Key != "nvidia:m2" || out[2].Key != "nvidia:m3" {
		t.Fatalf("keys: %v %v %v", out[0].Key, out[1].Key, out[2].Key)
	}
}

func TestExpandFailoverParallelExcludesIneligibleLegs(t *testing.T) {
	t.Parallel()
	sel, err := Parse("a:m1!b:m2!c:m3")
	if err != nil {
		t.Fatal(err)
	}
	ex := map[string]struct{}{"b:m2": {}}
	out, err := ExpandFailover(sel, PlanOptions{Excluded: ex})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 eligible legs, got %d", len(out))
	}
	if out[0].Key != "a:m1" || out[1].Key != "c:m3" {
		t.Fatalf("keys: %v %v", out[0].Key, out[1].Key)
	}
}

func TestExpandFailoverParallelPreservesHandicapMetadata(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[handicap=10]a:m1![handicap=5]b:m2!c:m3")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 legs, got %d", len(out))
	}
	if out[0].Handicap != 10*time.Second {
		t.Fatalf("leg0 handicap: %v", out[0].Handicap)
	}
	if out[1].Handicap != 5*time.Second {
		t.Fatalf("leg1 handicap: %v", out[1].Handicap)
	}
	if out[2].Handicap != 0 {
		t.Fatalf("leg2 handicap: %v want 0", out[2].Handicap)
	}
}

func TestExpandFailoverParallelPreservesTTFTTimeout(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[ttft_timeout=10]a:m1![ttft_timeout=5]b:m2!c:m3")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 legs, got %d", len(out))
	}
	if out[0].Primary.TTFTTimeout == nil || *out[0].Primary.TTFTTimeout != 10*time.Second {
		t.Fatalf("leg0 ttft: %v", out[0].Primary.TTFTTimeout)
	}
	if out[1].Primary.TTFTTimeout == nil || *out[1].Primary.TTFTTimeout != 5*time.Second {
		t.Fatalf("leg1 ttft: %v", out[1].Primary.TTFTTimeout)
	}
	if out[2].Primary.TTFTTimeout != nil {
		t.Fatalf("leg2 ttft: want nil, got %v", out[2].Primary.TTFTTimeout)
	}
}

func TestExpandFailoverOfParallelGroups(t *testing.T) {
	t.Parallel()
	sel, err := Parse("a:m1!b:m2|c:m3!d:m4")
	if err != nil {
		t.Fatal(err)
	}
	// First parallel group: 2 legs; second parallel group: 2 legs.
	// ExpandFailover should return the union of the first eligible arm.
	out, err := ExpandFailover(sel, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The first arm's parallel group is a:m1, b:m2.
	// They should all be returned (they are a parallel group, not sequential failover).
	if len(out) < 2 {
		t.Fatalf("expected at least 2 legs from first parallel arm, got %d", len(out))
	}
}

func TestExpandFailoverAllParallelExcludedFallsToNextArm(t *testing.T) {
	t.Parallel()
	sel, err := Parse("a:m1!b:m2|c:m3")
	if err != nil {
		t.Fatal(err)
	}
	ex := map[string]struct{}{"a:m1": {}, "b:m2": {}}
	out, err := ExpandFailover(sel, PlanOptions{Excluded: ex})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "c:m3" {
		t.Fatalf("expected fallback to c:m3, got %#v", out)
	}
}

func TestExpandFailoverParallelIsParallelFlag(t *testing.T) {
	t.Parallel()
	sel, err := Parse("a:m1!b:m2")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 2 {
		t.Fatalf("expected at least 2 parallel legs, got %d", len(out))
	}
	for _, c := range out {
		if !c.IsParallel {
			t.Fatalf("candidate %q should have IsParallel=true", c.Key)
		}
	}
}
