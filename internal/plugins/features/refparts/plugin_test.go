package refparts

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func TestRequestHook_appendsSuffixToFirstUserText(t *testing.T) {
	t.Parallel()
	h := NewRequestPartHook(Config{Suffix: " [x]"})
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}}}
	if err := h.HandleRequestParts(context.Background(), call, sdk.PartMeta{}); err != nil {
		t.Fatal(err)
	}
	if got := call.Messages[0].Parts[0].Text; got != "hi [x]" {
		t.Fatalf("text %q", got)
	}
}

func TestResponseHook_prefixesTextDelta(t *testing.T) {
	t.Parallel()
	h := NewResponsePartHook(Config{ResponsePrefix: "P:"})
	ev := &aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "out"}
	if err := h.HandleEvent(context.Background(), ev, sdk.PartMeta{}); err != nil {
		t.Fatal(err)
	}
	if ev.Delta != "P:out" {
		t.Fatalf("delta %q", ev.Delta)
	}
}
