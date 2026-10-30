package completion_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

type gateStub struct {
	id  string
	ord int
}

func (g gateStub) ID() string                        { return g.id }
func (g gateStub) Order() int                        { return g.ord }
func (g gateStub) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gateStub) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

func TestMaterializeSorted_sameIDUsesRegistrationIndex(t *testing.T) {
	t.Parallel()
	second := gateStub{id: "same", ord: 0}
	first := gateStub{id: "same", ord: 0}
	got := completion.MaterializeSorted([]completion.Gate{second, first})
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	g0, ok := got[0].(gateStub)
	if !ok {
		t.Fatalf("unexpected type %T", got[0])
	}
	if g0 != second {
		t.Fatalf("expected first registered gate first when Order+ID match")
	}
}
