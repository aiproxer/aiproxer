package hooks

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func validTestCall() *aipapi.Call {
	return &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
}

// TestBus_nil_receiver_matches_empty_bus verifies a nil *Bus behaves like hooks.New(Config{}):
// no panic and identical hook-chain semantics (see HookChainLengths).
func TestBus_nil_receiver_matches_empty_bus(t *testing.T) {
	t.Parallel()
	var nilBus *Bus
	empty := New(Config{})

	ns, nr, nresp, nt := nilBus.HookChainLengths()
	es, er, eresp, et := empty.HookChainLengths()
	if ns != es || nr != er || nresp != eresp || nt != et {
		t.Fatalf("HookChainLengths: nil bus got (%d,%d,%d,%d) want (%d,%d,%d,%d)", ns, nr, nresp, nt, es, er, eresp, et)
	}

	ctx := context.Background()
	callNil := validTestCall()
	callEmpty := validTestCall()

	if err := nilBus.RunSubmit(ctx, callNil, nil); err != nil {
		t.Fatalf("nil RunSubmit: %v", err)
	}
	if err := empty.RunSubmit(ctx, callEmpty, nil); err != nil {
		t.Fatalf("empty RunSubmit: %v", err)
	}

	reqNil := validTestCall()
	reqEmpty := validTestCall()
	if err := nilBus.RunRequestPartHooks(ctx, reqNil, sdk.PartMeta{}); err != nil {
		t.Fatalf("nil RunRequestPartHooks: %v", err)
	}
	if err := empty.RunRequestPartHooks(ctx, reqEmpty, sdk.PartMeta{}); err != nil {
		t.Fatalf("empty RunRequestPartHooks: %v", err)
	}

	ev := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hi"}
	if err := nilBus.RunResponsePartHooks(ctx, &ev, sdk.PartMeta{}); err != nil {
		t.Fatalf("nil RunResponsePartHooks: %v", err)
	}
	ev2 := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hi"}
	if err := empty.RunResponsePartHooks(ctx, &ev2, sdk.PartMeta{}); err != nil {
		t.Fatalf("empty RunResponsePartHooks: %v", err)
	}

	te := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "fn"}
	gotNil := nilBus.ApplyToolReactors(ctx, te, sdk.ToolMeta{})
	gotEmpty := empty.ApplyToolReactors(ctx, te, sdk.ToolMeta{})
	if gotNil.Emit != gotEmpty.Emit || gotNil.Event != gotEmpty.Event || (gotNil.Err != nil) != (gotEmpty.Err != nil) {
		t.Fatalf("ApplyToolReactors: nil %+v empty %+v", gotNil, gotEmpty)
	}
}
