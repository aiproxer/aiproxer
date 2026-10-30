package toolpolicy_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

type stubPolicy struct {
	id    string
	order int
}

func (s stubPolicy) ID() string                        { return s.id }
func (s stubPolicy) Order() int                        { return s.order }
func (s stubPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s stubPolicy) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

func TestMaterializeSorted_ordersByOrderThenIDThenRegistrationIndex(t *testing.T) {
	t.Parallel()
	policies := []toolpolicy.Policy{
		stubPolicy{id: "zeta", order: 2},
		stubPolicy{id: "alpha", order: 2},
		stubPolicy{id: "m", order: 1},
	}
	got := toolpolicy.MaterializeSorted(policies)
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	g0, ok0 := got[0].(stubPolicy)
	if !ok0 {
		t.Fatalf("unexpected type %T", got[0])
	}
	if g0.id != "m" {
		t.Fatalf("first want order=1, got %v", g0)
	}
	g1, ok1 := got[1].(stubPolicy)
	g2, ok2 := got[2].(stubPolicy)
	if !ok1 || !ok2 {
		t.Fatalf("unexpected types %T %T", got[1], got[2])
	}
	if g1.id != "alpha" || g2.id != "zeta" {
		t.Fatalf("stable ID order wrong: %#v %#v", g1.id, g2.id)
	}
}

func TestMaterializeSorted_sameIDUsesRegistrationIndex(t *testing.T) {
	t.Parallel()
	second := stubPolicy{id: "same", order: 0}
	first := stubPolicy{id: "same", order: 0}
	got := toolpolicy.MaterializeSorted([]toolpolicy.Policy{second, first})
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	s0, ok0 := got[0].(stubPolicy)
	s1, ok1 := got[1].(stubPolicy)
	if !ok0 || !ok1 {
		t.Fatalf("unexpected types %T %T", got[0], got[1])
	}
	if s0.id != "same" || s1.id != "same" {
		t.Fatal()
	}
	if s0 != second {
		t.Fatalf("expected first registered policy first when Order+ID match: got %#v want %#v", s0, second)
	}
}

func TestMaterializeSorted_emptyReturnsNil(t *testing.T) {
	t.Parallel()
	if toolpolicy.MaterializeSorted(nil) != nil {
		t.Fatal("expected nil")
	}
	if toolpolicy.MaterializeSorted([]toolpolicy.Policy{}) != nil {
		t.Fatal("expected nil")
	}
}

func TestMaterializeSorted_singleReturnsSingleton(t *testing.T) {
	t.Parallel()
	p := stubPolicy{id: "only", order: 3}
	got := toolpolicy.MaterializeSorted([]toolpolicy.Policy{p})
	if len(got) != 1 {
		t.Fatalf("got %#v", got)
	}
	only, ok := got[0].(stubPolicy)
	if !ok {
		t.Fatalf("unexpected type %T", got[0])
	}
	if only.id != "only" {
		t.Fatalf("got %#v", got)
	}
}
