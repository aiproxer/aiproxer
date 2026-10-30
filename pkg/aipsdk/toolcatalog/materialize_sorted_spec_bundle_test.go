package toolcatalog_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
)

type stubFilter struct {
	id    string
	order int
}

func (s stubFilter) ID() string                        { return s.id }
func (s stubFilter) Order() int                        { return s.order }
func (s stubFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s stubFilter) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

func TestMaterializeSorted_ordersByOrderThenIDThenRegistrationIndex(t *testing.T) {
	t.Parallel()
	filters := []toolcatalog.Filter{
		stubFilter{id: "zeta", order: 2},
		stubFilter{id: "alpha", order: 2},
		stubFilter{id: "m", order: 1},
	}
	got := toolcatalog.MaterializeSorted(filters)
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	g0, ok0 := got[0].(stubFilter)
	if !ok0 {
		t.Fatalf("unexpected type %T", got[0])
	}
	if g0.id != "m" {
		t.Fatalf("first want order=1, got %v", g0)
	}
	g1, ok1 := got[1].(stubFilter)
	g2, ok2 := got[2].(stubFilter)
	if !ok1 || !ok2 {
		t.Fatalf("unexpected types %T %T", got[1], got[2])
	}
	if g1.id != "alpha" || g2.id != "zeta" {
		t.Fatalf("stable ID order wrong: %#v %#v", g1.id, g2.id)
	}
}

func TestMaterializeSorted_sameIDUsesRegistrationIndex(t *testing.T) {
	t.Parallel()
	second := stubFilter{id: "same", order: 0}
	first := stubFilter{id: "same", order: 0}
	got := toolcatalog.MaterializeSorted([]toolcatalog.Filter{second, first})
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	g0, ok := got[0].(stubFilter)
	if !ok {
		t.Fatalf("unexpected type %T", got[0])
	}
	if g0 != second {
		t.Fatalf("expected first registered filter first when Order+ID match")
	}
}

func TestMaterializeSorted_emptyReturnsNil(t *testing.T) {
	t.Parallel()
	if toolcatalog.MaterializeSorted(nil) != nil {
		t.Fatal("expected nil")
	}
	if toolcatalog.MaterializeSorted([]toolcatalog.Filter{}) != nil {
		t.Fatal("expected nil")
	}
}
