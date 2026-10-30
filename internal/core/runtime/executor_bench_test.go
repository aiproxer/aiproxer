package runtime_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// benchEvents returns a realistic-sized stream for hot-path Recv benchmarking.
func benchEvents(nDeltas int) []aipapi.Event {
	ev := make([]aipapi.Event, 0, 3+nDeltas+1)
	ev = append(
		ev,
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
	)
	for range nDeltas {
		ev = append(ev, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
	}
	ev = append(ev, aipapi.Event{Kind: aipapi.EventResponseFinished})
	return ev
}

// benchmarkHistoryCall builds a call with a large, content-realistic history so
// history-proportional per-delta work is visible in -benchmem.
func benchmarkHistoryCall(messages, partsPerMessage int) *aipapi.Call {
	msgs := make([]aipapi.Message, 0, messages)
	for range messages {
		parts := make([]aipapi.Part, 0, partsPerMessage)
		for range partsPerMessage {
			parts = append(parts, aipapi.TextPart("history part with representative payload text"))
		}
		msgs = append(msgs, aipapi.Message{Role: aipapi.RoleUser, Parts: parts})
	}
	return &aipapi.Call{Route: aipapi.RouteIntent{Selector: "stub:m"}, Messages: msgs}
}

// BenchmarkExecutorExecuteAndDrain32DeltasLargeHistory guards the streaming hot path
// against history-proportional work per delivered event. The release tail runs once per
// client event, so it must read request identity from the frozen facts rather than build
// a terminal snapshot: each terminalFacts() call deep-clones the baseline,
// conversation-filtered and ingress calls. On this fixture (100 messages x 20 parts,
// 32 deltas) the benchmark measured 41.3 MB / 19,464 allocations per stream while the
// release tail built a per-event terminal snapshot, and 12.3 MB / 12,152 after.
func BenchmarkExecutorExecuteAndDrain32DeltasLargeHistory(b *testing.B) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		b.Fatal(err)
	}
	events := benchEvents(32)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(42)
	ex.Backends = map[string]execbackend.Backend{
		"stub": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream(events), nil
			},
		},
	}
	call := benchmarkHistoryCall(100, 20)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s, err := ex.Execute(ctx, call)
		if err != nil {
			b.Fatal(err)
		}
		recvCtx := diag.EnsureCallDiag(ctx, call.ID, call.Session.ALegID)
		for {
			_, err := s.Recv(recvCtx)
			if err != nil {
				break
			}
		}
		_ = s.Close()
	}
}

// BenchmarkExecutorExecuteAndDrain32Deltas is the no-authority/no-metering baseline (16.8).
func BenchmarkExecutorExecuteAndDrain32Deltas(b *testing.B) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		b.Fatal(err)
	}
	events := benchEvents(32)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(42)
	ex.Backends = map[string]execbackend.Backend{
		"stub": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream(events), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("bench")},
		}},
	}
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s, err := ex.Execute(ctx, call)
		if err != nil {
			b.Fatal(err)
		}
		// Match production frontends: attach call diag once so retryRecvStream.Recv can use EnsureCallDiag fast path.
		recvCtx := diag.EnsureCallDiag(ctx, call.ID, call.Session.ALegID)
		for {
			_, err := s.Recv(recvCtx)
			if err != nil {
				break
			}
		}
		_ = s.Close()
	}
}
