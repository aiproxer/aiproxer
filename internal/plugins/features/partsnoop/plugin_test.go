package partsnoop

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func testCall() *aipapi.Call {
	return &aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("x")},
		}},
	}
}

func testEvent() *aipapi.Event {
	return &aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "y"}
}

func TestNewRequestPartHook_contract(t *testing.T) {
	t.Parallel()
	h := NewRequestPartHook()
	if h.ID() != ID {
		t.Fatalf("ID: got %q want %q", h.ID(), ID)
	}
	if h.Order() != hookOrder {
		t.Fatalf("Order: got %d want %d", h.Order(), hookOrder)
	}
	if h.FailureMode() != sdk.FailOpen {
		t.Fatalf("FailureMode: got %v want FailOpen", h.FailureMode())
	}
}

func TestNewRequestPartHook_noMutation(t *testing.T) {
	t.Parallel()
	h := NewRequestPartHook()
	call := testCall()
	before, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	meta := sdk.PartMeta{TraceID: "t", ALegID: "a", BLegID: "b", AttemptSeq: 1}
	if err := h.HandleRequestParts(context.Background(), call, meta); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("call mutated:\nbefore %s\nafter %s", before, after)
	}
}

func TestNewResponsePartHook_contract(t *testing.T) {
	t.Parallel()
	h := NewResponsePartHook()
	if h.ID() != ID {
		t.Fatalf("ID: got %q want %q", h.ID(), ID)
	}
	if h.Order() != hookOrder {
		t.Fatalf("Order: got %d want %d", h.Order(), hookOrder)
	}
	if h.FailureMode() != sdk.FailOpen {
		t.Fatalf("FailureMode: got %v want FailOpen", h.FailureMode())
	}
}

func TestNewResponsePartHook_noMutation(t *testing.T) {
	t.Parallel()
	h := NewResponsePartHook()
	ev := testEvent()
	before, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	meta := sdk.PartMeta{TraceID: "t", ALegID: "a", BLegID: "b", AttemptSeq: 0}
	if err := h.HandleEvent(context.Background(), ev, meta); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("event mutated:\nbefore %s\nafter %s", before, after)
	}
}
