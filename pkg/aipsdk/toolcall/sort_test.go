package toolcall_test

import (
	"context"
	"math"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

type sortFin struct {
	id  string
	ord int
}

func (f sortFin) ID() string { return f.id }
func (f sortFin) Order() int { return f.ord }
func (f sortFin) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

func TestMaterializeSorted_orderIDRegistration(t *testing.T) {
	t.Parallel()
	in := []toolcall.Finalizer{
		sortFin{id: "b", ord: 1},
		nil,
		sortFin{id: "a", ord: 1},
		sortFin{id: "z", ord: 0},
	}
	got := toolcall.MaterializeSorted(in)
	if len(got) != 3 {
		t.Fatalf("len=%d want 3 (nil skipped)", len(got))
	}
	want := []string{"z", "a", "b"}
	for i, id := range want {
		if got[i].ID() != id {
			t.Fatalf("got[%d]=%q want %q", i, got[i].ID(), id)
		}
	}
}

func TestMaterializeSorted_emptyNil(t *testing.T) {
	t.Parallel()
	if toolcall.MaterializeSorted(nil) != nil {
		t.Fatal("nil in")
	}
	if toolcall.MaterializeSorted([]toolcall.Finalizer{}) != nil {
		t.Fatal("empty in")
	}
}

func TestMaterializeSorted_extremeOrderNoWrap(t *testing.T) {
	t.Parallel()
	in := []toolcall.Finalizer{
		sortFin{id: "hi", ord: math.MaxInt},
		sortFin{id: "lo", ord: math.MinInt},
		sortFin{id: "mid", ord: 0},
	}
	got := toolcall.MaterializeSorted(in)
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	want := []string{"lo", "mid", "hi"}
	for i, id := range want {
		if got[i].ID() != id {
			t.Fatalf("got[%d]=%q want %q (Order subtraction must not wrap)", i, got[i].ID(), id)
		}
	}
}
