package service

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The ACP usage object reports inputTokens outside the cached and the written
// prompt subsets. Both exclusions must travel with the counters so a frontend
// counts the prompt in its own semantics instead of reporting an input smaller
// than the cache reads and writes it also reports.
func TestClassifiedStream_declaresExclusiveInput(t *testing.T) {
	t.Parallel()
	inner := aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
		{
			Kind:             aipapi.EventUsageDelta,
			InputTokens:      2,
			OutputTokens:     4,
			CacheReadTokens:  10145,
			CacheWriteTokens: 2594,
			TotalTokens:      12745,
			UsagePresence: aipapi.UsagePresence{
				InputTokens: true, OutputTokens: true,
				CacheReadTokens: true, CacheWriteTokens: true, TotalTokens: true,
			},
			// The adapter annotates provider-reported usage before the
			// connector sees it: the declaration must be added next to it.
			Accounting: aipapi.UsageAccountingMetadata{
				Plane:     aipapi.UsagePlaneProviderBillable,
				Source:    aipapi.UsageSourceProviderReported,
				Authority: aipapi.UsageAuthorityAuthoritative,
			},
		},
	})}
	stream := &classifiedStream{ManagedEventStream: inner, spec: mustTestSpec(t), phase: aipapi.PhasePreOutput}
	event, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !event.Accounting.InputTokensExcludeCacheRead || !event.Accounting.InputTokensExcludeCacheWrite {
		t.Fatalf("accounting = %+v, want both cache exclusions", event.Accounting)
	}
	if event.InputTokens != 2 || event.OutputTokens != 4 || event.CacheReadTokens != 10145 || event.CacheWriteTokens != 2594 || event.TotalTokens != 12745 {
		t.Fatalf("counters changed: %+v", event)
	}
	if !event.UsagePresence.InputTokens || !event.UsagePresence.OutputTokens || !event.UsagePresence.CacheReadTokens || !event.UsagePresence.CacheWriteTokens || !event.UsagePresence.TotalTokens {
		t.Fatalf("presence changed: %+v", event.UsagePresence)
	}
	if event.Accounting.Plane != aipapi.UsagePlaneProviderBillable || event.Accounting.Source != aipapi.UsageSourceProviderReported || event.Accounting.Authority != aipapi.UsageAuthorityAuthoritative {
		t.Fatalf("provider accounting changed: %+v", event.Accounting)
	}
}

// A turn without usage keeps its events untouched: no cache relation is
// declared, and accounting metadata unrelated to it survives unchanged.
func TestClassifiedStream_leavesOtherEventsUnchanged(t *testing.T) {
	t.Parallel()
	inner := aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
		{
			Kind:  aipapi.EventTextDelta,
			Delta: "hi",
			Accounting: aipapi.UsageAccountingMetadata{
				Plane: aipapi.UsagePlaneClientVisible, DedupeKey: "acp.text",
			},
		},
	})}
	stream := &classifiedStream{ManagedEventStream: inner, spec: mustTestSpec(t), phase: aipapi.PhasePreOutput}
	event, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != aipapi.EventTextDelta || event.Delta != "hi" {
		t.Fatalf("event = %+v", event)
	}
	if event.Accounting.InputTokensExcludeCacheRead || event.Accounting.InputTokensExcludeCacheWrite {
		t.Fatalf("text event declared a cache relation: %+v", event.Accounting)
	}
	if event.Accounting.Plane != aipapi.UsagePlaneClientVisible || event.Accounting.DedupeKey != "acp.text" {
		t.Fatalf("unrelated accounting metadata changed: %+v", event.Accounting)
	}
}
