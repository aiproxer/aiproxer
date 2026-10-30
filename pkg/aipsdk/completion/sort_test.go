package completion_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

type sortGate struct {
	id  string
	ord int
	idx int
}

func (g sortGate) ID() string                        { return g.id }
func (g sortGate) Order() int                        { return g.ord }
func (g sortGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (g sortGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

func TestMaterializeSorted_orderPriorityIDRegistration(t *testing.T) {
	t.Parallel()
	gates := []completion.Gate{
		sortGate{id: "b", ord: 1, idx: 0},
		sortGate{id: "a", ord: 2, idx: 1},
		sortGate{id: "a", ord: 1, idx: 2},
	}
	sorted := completion.MaterializeSorted(gates)
	if len(sorted) != 3 {
		t.Fatalf("len=%d", len(sorted))
	}
	first, ok := sorted[0].(sortGate)
	if !ok {
		t.Fatalf("want sortGate at [0], got %T", sorted[0])
	}
	if first.id != "a" || first.idx != 2 {
		t.Fatalf("want first ord=1 id=a reg=2 got %+v", first)
	}
	second, ok2 := sorted[1].(sortGate)
	if !ok2 {
		t.Fatalf("want sortGate at [1], got %T", sorted[1])
	}
	if second.id != "b" {
		t.Fatalf("want second b got %+v", second)
	}
	third, ok3 := sorted[2].(sortGate)
	if !ok3 {
		t.Fatalf("want sortGate at [2], got %T", sorted[2])
	}
	if third.id != "a" || third.ord != 2 {
		t.Fatalf("want third ord=2 id=a got %+v", third)
	}
}

func TestMaterializeSorted_emptyNil(t *testing.T) {
	t.Parallel()
	if completion.MaterializeSorted(nil) != nil {
		t.Fatal("expected nil")
	}
	if completion.MaterializeSorted([]completion.Gate{}) != nil {
		t.Fatal("expected nil for empty")
	}
}

func TestBuffered_defensiveCopy(t *testing.T) {
	t.Parallel()
	orig := []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "x"}}
	buf := completion.NewBuffered(orig)
	orig[0].Delta = "y"
	evs := buf.Events()
	if len(evs) != 1 || evs[0].Delta != "x" {
		t.Fatalf("got %#v", evs)
	}
}

func TestBufferLimitsOverCapacity(t *testing.T) {
	t.Parallel()
	lim := completion.BufferLimits{MaxEvents: 2}
	if !lim.OverCapacity(3) {
		t.Fatal("expected over capacity")
	}
	if lim.OverCapacity(2) {
		t.Fatal("boundary")
	}
	d := completion.DefaultBufferLimits()
	if d.MaxEvents <= 0 {
		t.Fatal("default max events")
	}
}
