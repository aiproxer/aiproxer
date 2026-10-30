package hooks

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// markSubmit records invocation order for hooks that intentionally share Order and ID.
type markSubmit struct {
	id    string
	order int
	slot  int
	out   *[]int
}

func (h markSubmit) ID() string                   { return h.id }
func (h markSubmit) Order() int                   { return h.order }
func (h markSubmit) FailureMode() sdk.FailureMode { return sdk.FailOpen }
func (h markSubmit) Handle(context.Context, *aipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
	*h.out = append(*h.out, h.slot)
	return sdk.SubmitDecision{}, nil
}

func TestBus_submitOrderStableWhenOrderAndIDCollide(t *testing.T) {
	t.Parallel()
	var got []int
	first := markSubmit{id: "dup", order: 1, slot: 1, out: &got}
	second := markSubmit{id: "dup", order: 1, slot: 2, out: &got}
	b := New(Config{SubmitHooks: []sdk.SubmitHook{first, second}})
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	if err := b.RunSubmit(context.Background(), call, nil); err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2}
	if len(got) != len(want) {
		t.Fatalf("invocations: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("invocations: got %v want %v (stable registration order when Order+ID collide)", got, want)
		}
	}
}
