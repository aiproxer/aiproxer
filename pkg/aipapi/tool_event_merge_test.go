package aipapi_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestMergeToolEventInto_rewriteArgsDelta(t *testing.T) {
	t.Parallel()
	orig := aipapi.Event{
		Kind:       aipapi.EventToolCallArgsDelta,
		ToolCallID: "call_1",
		ToolName:   "weather",
		Delta:      `{"a":1}`,
	}
	te := aipapi.ToolEvent{
		Kind:       aipapi.ToolEventArgsDelta,
		ToolCallID: "call_1",
		ToolName:   "weather_v2",
		ArgsDelta:  `{"b":2}`,
	}
	got := aipapi.MergeToolEventInto(orig, te)
	if got.Delta != `{"b":2}` || got.ToolName != "weather_v2" {
		t.Fatalf("got %+v", got)
	}
}

func TestMergeToolEventInto_startedRewritesID(t *testing.T) {
	t.Parallel()
	orig := aipapi.Event{
		Kind:       aipapi.EventToolCallStarted,
		ToolCallID: "old",
		ToolName:   "fn",
	}
	te := aipapi.ToolEvent{
		Kind:       aipapi.ToolEventStarted,
		ToolCallID: "new",
		ToolName:   "fn2",
	}
	got := aipapi.MergeToolEventInto(orig, te)
	if got.ToolCallID != "new" || got.ToolName != "fn2" {
		t.Fatalf("got %+v", got)
	}
}
