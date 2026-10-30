package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
)

// The receive path derives the frozen-facts projection of the caller context once
// per DISTINCT parent and reuses it for every further delivered event, instead of
// re-cloning the frozen view maps and rebuilding the value chain per event. These
// tests pin the reuse and the semantics that make the reuse safe: frozen facts
// still win, non-fact parent values and cancellation still resolve through the
// parent chain, and a parent whose type cannot be compared is projected again
// rather than compared.

func TestRecvProjection_SameParentReusesOneProjection(t *testing.T) {
	t.Parallel()
	p := newResponsePipeline()
	facts := driftFacts()
	parent := context.Background()

	first := p.projectedRecvContext(parent, facts)
	second := p.projectedRecvContext(parent, facts)

	if first != second {
		t.Fatal("the same parent must reuse the projection built for the previous receive")
	}
	if got := execctx.RouteCandidatePreferences(second); len(got) != 2 || got[0] != "cand-a" || got[1] != "cand-b" {
		t.Fatalf("reused projection must still carry the frozen route preferences, got %v", got)
	}
	views, ok := execctx.FromContext(second)
	if !ok || views.Session.AuthoritativeSessionID != "sess-1" {
		t.Fatalf("reused projection must still carry the frozen views, got ok=%v viewSessionSet=%v", ok, ok && views.Session.AuthoritativeSessionID != "")
	}
}

func TestRecvProjection_DistinctParentProjectsAgain(t *testing.T) {
	t.Parallel()
	p := newResponsePipeline()
	facts := driftFacts()
	type projectionParentKey struct{}

	first := p.projectedRecvContext(context.WithValue(context.Background(), projectionParentKey{}, nil), facts)
	second := p.projectedRecvContext(context.WithValue(context.Background(), projectionParentKey{}, nil), facts)

	if first == second {
		t.Fatal("a distinct parent chain must be projected again, not reused")
	}
	if got := execctx.RouteCandidatePreferences(second); len(got) != 2 {
		t.Fatalf("re-projection must carry the frozen route preferences, got %d entries", len(got))
	}
}

func TestRecvProjection_ReusedProjectionObservesParentCancellation(t *testing.T) {
	t.Parallel()
	p := newResponsePipeline()
	facts := driftFacts()
	parent, cancel := context.WithCancel(context.Background())
	first := p.projectedRecvContext(parent, facts)

	cancel()
	second := p.projectedRecvContext(parent, facts)

	if second != first {
		t.Fatal("cancellation must not invalidate the reuse of the same parent")
	}
	if !errors.Is(second.Err(), context.Canceled) {
		t.Fatalf("reused projection must observe the parent cancellation, got %v", second.Err())
	}
}

func TestRecvProjection_NonFactParentValuesStillResolveThroughParent(t *testing.T) {
	t.Parallel()
	p := newResponsePipeline()
	facts := driftFacts()
	type parentCarriedKey struct{}
	parent := context.WithValue(context.Background(), parentCarriedKey{}, "parent-carried-value")

	first := p.projectedRecvContext(parent, facts)
	second := p.projectedRecvContext(parent, facts)

	if second != first {
		t.Fatal("the same parent must reuse the projection")
	}
	if got, _ := second.Value(parentCarriedKey{}).(string); got != "parent-carried-value" {
		t.Fatalf("reused projection must still resolve parent-carried values, got %q", got)
	}
}

// TestRecvProjection_NonComparableParentIsProjectedNotCompared drives a parent
// whose dynamic type holds a slice. Comparing such an interface value panics, so
// the memo must miss it instead of comparing it.
func TestRecvProjection_NonComparableParentIsProjectedNotCompared(t *testing.T) {
	t.Parallel()
	p := newResponsePipeline()
	facts := driftFacts()

	first := p.projectedRecvContext(nonComparableParent{}, facts)
	second := p.projectedRecvContext(nonComparableParent{}, facts)

	if first == second {
		t.Fatal("a non-comparable parent type can never be a memo hit")
	}
	if got := execctx.RouteCandidatePreferences(second); len(got) != 2 {
		t.Fatalf("a non-comparable parent must still project the frozen facts, got %d entries", len(got))
	}
}

func TestSameContextValue_RequiresTheSameComparableValue(t *testing.T) {
	t.Parallel()

	// An absent parent is a supported receive input, so the nil cases are pinned
	// here on purpose rather than avoided.
	if sameContextValue(nil, nil) { //nolint:staticcheck // nil parent is the case under test
		t.Fatal("a nil parent is never a memo hit")
	}
	parent := context.Background()
	if !sameContextValue(parent, parent) {
		t.Fatal("the same comparable context must compare equal")
	}
	if sameContextValue(parent, nil) || sameContextValue(nil, parent) { //nolint:staticcheck // nil parent is the case under test
		t.Fatal("a nil side can never compare equal")
	}
	if sameContextValue(context.Background(), context.TODO()) {
		t.Fatal("distinct background values must not compare equal")
	}
	if sameContextValue(nonComparableParent{}, nonComparableParent{}) {
		t.Fatal("a non-comparable dynamic type must never compare equal")
	}
}

// nonComparableParent is a context whose dynamic type contains a slice, so an
// interface comparison against it panics.
type nonComparableParent struct{ carried []byte }

func (nonComparableParent) Deadline() (time.Time, bool) { return time.Time{}, false }

func (nonComparableParent) Done() <-chan struct{} { return nil }

func (nonComparableParent) Err() error { return nil }

func (nonComparableParent) Value(any) any { return nil }
