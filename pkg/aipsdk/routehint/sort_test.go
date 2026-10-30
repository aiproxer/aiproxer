package routehint_test

import (
	"context"
	"testing"

	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
)

type stubProv struct {
	id    string
	order int
}

func (s stubProv) ID() string                        { return s.id }
func (s stubProv) Order() int                        { return s.order }
func (s stubProv) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubProv) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

func TestMaterializeSorted_orderThenID(t *testing.T) {
	t.Parallel()
	a := stubProv{id: "b", order: 1}
	b := stubProv{id: "a", order: 1}
	c := stubProv{id: "z", order: 0}
	got := routehint.MaterializeSorted([]routehint.Provider{c, b, a})
	if len(got) != 3 {
		t.Fatalf("len %d", len(got))
	}
	first, ok := got[0].(stubProv)
	if !ok {
		t.Fatalf("want stubProv at [0], got %T", got[0])
	}
	if first.id != "z" {
		t.Fatalf("want z first got %v", first.id)
	}
	second, ok2 := got[1].(stubProv)
	third, ok3 := got[2].(stubProv)
	if !ok2 || !ok3 {
		t.Fatalf("want stubProv at [1] and [2], got %T %T", got[1], got[2])
	}
	if second.id != "a" || third.id != "b" {
		t.Fatalf("order %s %s", second.id, third.id)
	}
}
