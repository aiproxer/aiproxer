package aipapi_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestToolEventFromEvent_started(t *testing.T) {
	t.Parallel()
	ev := aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "fn"}
	te, ok := aipapi.ToolEventFromEvent(ev)
	if !ok {
		t.Fatal("expected ok")
	}
	if te.Kind != aipapi.ToolEventStarted || te.ToolCallID != "c1" || te.ToolName != "fn" {
		t.Fatalf("unexpected tool event: %#v", te)
	}
}

func TestToolEventFromEvent_argsDelta(t *testing.T) {
	t.Parallel()
	ev := aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"x":1}`}
	te, ok := aipapi.ToolEventFromEvent(ev)
	if !ok {
		t.Fatal("expected ok")
	}
	if te.Kind != aipapi.ToolEventArgsDelta || te.ArgsDelta != `{"x":1}` {
		t.Fatalf("unexpected tool event: %#v", te)
	}
}

func TestToolEventFromEvent_finished(t *testing.T) {
	t.Parallel()
	ev := aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1"}
	te, ok := aipapi.ToolEventFromEvent(ev)
	if !ok {
		t.Fatal("expected ok")
	}
	if te.Kind != aipapi.ToolEventFinished {
		t.Fatalf("unexpected tool event: %#v", te)
	}
}

func TestToolEventFromEvent_rejectsMissingToolCallID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind aipapi.EventKind
	}{
		{"started", aipapi.EventToolCallStarted},
		{"args_delta", aipapi.EventToolCallArgsDelta},
		{"finished", aipapi.EventToolCallFinished},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, ok := aipapi.ToolEventFromEvent(aipapi.Event{Kind: tc.kind})
			if ok {
				t.Fatalf("expected false for kind %q without tool call id", tc.kind)
			}
		})
	}
}

func TestToolEventFromEvent_nonToolKinds(t *testing.T) {
	t.Parallel()
	_, ok := aipapi.ToolEventFromEvent(aipapi.Event{Kind: aipapi.EventTextDelta})
	if ok {
		t.Fatal("expected false")
	}
}
