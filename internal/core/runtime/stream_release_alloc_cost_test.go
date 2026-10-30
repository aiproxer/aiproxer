package runtime_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Per-event allocation debt on the streaming release path: the frozen-facts
// receive projection used to be re-derived (and every request view map re-cloned)
// for each delivered event, and the released-content accumulator retained a full
// canonical event per delta. Both are response-lifetime costs, so this fixture
// measures them as one incremental per-event number that is independent of the
// fixed cost of starting and settling a logical response.

// streamReleaseExecutor builds the streaming fixture: a stub backend emitting
// nDeltas text deltas between the response and message lifecycle markers, with no
// hooks, features, or observers registered.
func streamReleaseExecutor(tb testing.TB, nDeltas int) *runtime.Executor {
	tb.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		tb.Fatal(err)
	}
	events := benchEvents(nDeltas)
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
	return ex
}

func streamReleaseCall() *aipapi.Call {
	return &aipapi.Call{
		ID:    "stream-release-cost-call",
		Route: aipapi.RouteIntent{Selector: "stub:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("bench")},
		}},
	}
}

// streamReleaseDrainOne executes one logical response and reads it to
// completion, returning the number of text deltas it delivered. The count keeps
// the ratchet honest: a release path that stopped delivering events early would
// otherwise lower the measured per-event cost instead of failing the test.
func streamReleaseDrainOne(tb testing.TB, ex *runtime.Executor, call *aipapi.Call) int {
	tb.Helper()
	ctx := context.Background()
	s, err := ex.Execute(ctx, call)
	if err != nil {
		tb.Fatal(err)
	}
	recvCtx := diag.EnsureCallDiag(ctx, call.ID, call.Session.ALegID)
	deltas := 0
	for {
		ev, recvErr := s.Recv(recvCtx)
		if recvErr != nil {
			if !errors.Is(recvErr, io.EOF) {
				tb.Fatalf("Recv: %v", recvErr)
			}
			break
		}
		if ev.Kind == aipapi.EventTextDelta {
			deltas++
		}
	}
	_ = s.Close()
	return deltas
}

func streamReleaseAllocs(tb testing.TB, nDeltas int) float64 {
	tb.Helper()
	ex := streamReleaseExecutor(tb, nDeltas)
	call := streamReleaseCall()
	return testing.AllocsPerRun(3, func() {
		if got := streamReleaseDrainOne(tb, ex, call); got != nDeltas {
			tb.Fatalf("delivered %d text deltas, want %d", got, nDeltas)
		}
	})
}

// TestStreamReleasePerEventAllocationsStayBounded is a shared-hot-path allocation
// ratchet. It compares two stream lengths so the reported cost is the INCREMENTAL
// allocation of one delivered event, which is what regressed: the receive
// projection re-cloned the frozen request view maps per event (~11 allocations)
// and the content accumulator retained a 632-byte canonical event per delta.
//
// Measured on the fixture below: 47.0 allocations/event before the frozen-state
// fix, 5.0 after it, and 2.0 after the observation seams stopped moving the
// 640-byte canonical event to the heap per delivered event (the always-created
// final-stream session with no observers, the response-part hook copy, and the
// compaction-preserver address). The cap keeps one allocation of toolchain
// drift while still failing if per-event context projection, view cloning,
// event copying, or full-event retention returns.
func TestStreamReleasePerEventAllocationsStayBounded(t *testing.T) {
	const (
		smallDeltas = 32
		largeDeltas = 256
		maxPerEvent = 3
	)
	small := streamReleaseAllocs(t, smallDeltas)
	large := streamReleaseAllocs(t, largeDeltas)
	perEvent := (large - small) / float64(largeDeltas-smallDeltas)
	if perEvent > maxPerEvent {
		t.Fatalf("streaming release path allocates %.2f allocations per delivered event (%d->%d deltas), want <= %d",
			perEvent, smallDeltas, largeDeltas, maxPerEvent)
	}
}

// BenchmarkExecutorExecuteAndDrain512Deltas is the long-stream view of the same
// release path. With -benchmem its total must stay close to linear in the number
// of deltas: a per-event context projection or view clone shows up as a growing
// B/op and allocs/op slope rather than a constant.
func BenchmarkExecutorExecuteAndDrain512Deltas(b *testing.B) {
	ex := streamReleaseExecutor(b, 512)
	call := streamReleaseCall()
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
			if _, recvErr := s.Recv(recvCtx); recvErr != nil {
				break
			}
		}
		_ = s.Close()
	}
}
