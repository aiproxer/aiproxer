package openairesponses

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

//nolint:paralleltest // allocation budget is sensitive to cross-test scheduling vs httptest/pool state
func TestWriteStreamSSE_AllocBudget_textOnly(t *testing.T) {
	const n = 200
	events := make([]aipapi.Event, 0, n+3)
	events = append(
		events,
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
	)
	for range n {
		events = append(events, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
	}
	events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished})

	ctx := context.Background()
	call := &aipapi.Call{ID: "alloc-call"}
	opts := EncodeOptions{ResponseID: "resp_alloc", MessageID: "msg_alloc", CreatedAt: 1}
	allocs := testing.AllocsPerRun(5, func() {
		rec := httptest.NewRecorder()
		es := aipapi.NewFixedEventStream(events)
		if err := WriteStreamSSE(ctx, rec, call, es, opts); err != nil {
			t.Fatal(err)
		}
	})
	// Baseline ~2k allocs/run for n=200 text deltas (see BenchmarkWriteStreamSSE_textDeltas -benchmem).
	// Cap leaves headroom for CI / OS variance vs httptest and json pooling.
	const maxAllocs = 25_000
	if int(allocs) > maxAllocs {
		t.Fatalf("allocs per run=%g (n=%d deltas), want <= %d", allocs, n, maxAllocs)
	}
}
