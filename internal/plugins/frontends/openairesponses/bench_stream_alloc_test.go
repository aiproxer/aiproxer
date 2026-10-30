package openairesponses

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// BenchmarkWriteStreamSSE_textDeltas measures allocations on the streaming encode
// hot path (many text deltas + terminal events). Use -benchmem to inspect allocs/op
// when tuning [TestWriteStreamSSE_AllocBudget_textOnly].
func BenchmarkWriteStreamSSE_textDeltas(b *testing.B) {
	const nDelta = 200
	events := make([]aipapi.Event, 0, nDelta+3)
	events = append(
		events,
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
	)
	for range nDelta {
		events = append(events, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "xy"})
	}
	events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished})

	call := &aipapi.Call{ID: "bench-call"}

	b.ReportAllocs()

	for b.Loop() {
		rec := httptest.NewRecorder()
		es := aipapi.NewFixedEventStream(events)
		if err := WriteStreamSSE(context.Background(), rec, call, es, EncodeOptions{
			ResponseID: "resp_bench",
			MessageID:  "msg_bench",
			CreatedAt:  1,
		}); err != nil {
			b.Fatal(err)
		}
	}
}
