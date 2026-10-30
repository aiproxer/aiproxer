package service

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// A stream that was never opened stays nil, so the caller's own nil-stream
// guard still sees it instead of a wrapper around a missing stream.
func TestDeclareExclusiveInputUsage_nilStreamStaysNil(t *testing.T) {
	t.Parallel()
	if got := declareExclusiveInputUsage(nil); got != nil {
		t.Fatalf("nil stream wrapped: %+v", got)
	}
}

// The declared relation travels with the counters, and the counters themselves
// are untouched.
func TestDeclareExclusiveInputUsage_declaresRelation(t *testing.T) {
	t.Parallel()
	inner := aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventUsageDelta, InputTokens: 2, OutputTokens: 4, CacheReadTokens: 10145, CacheWriteTokens: 2594, TotalTokens: 12745},
	})}
	stream := declareExclusiveInputUsage(inner)
	event, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !event.Accounting.InputTokensExcludeCacheRead || !event.Accounting.InputTokensExcludeCacheWrite {
		t.Fatalf("accounting = %+v", event.Accounting)
	}
	if event.InputTokens != 2 || event.OutputTokens != 4 || event.CacheReadTokens != 10145 || event.CacheWriteTokens != 2594 || event.TotalTokens != 12745 {
		t.Fatalf("counters changed: %+v", event)
	}
}

// A turn without usage keeps its events untouched.
func TestDeclareExclusiveInputUsage_leavesOtherEventsUnchanged(t *testing.T) {
	t.Parallel()
	inner := aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "hi"},
	})}
	stream := declareExclusiveInputUsage(inner)
	event, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != aipapi.EventTextDelta || event.Delta != "hi" || event.Accounting.InputTokensExcludeCacheRead || event.Accounting.InputTokensExcludeCacheWrite {
		t.Fatalf("event = %+v", event)
	}
}
