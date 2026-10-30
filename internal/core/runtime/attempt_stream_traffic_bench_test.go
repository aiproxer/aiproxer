package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// Profiling traffic JSON (manual):
//
//	go test -bench=BenchmarkEmitTrafficBTP_jsonMarshal -benchmem -count=8 -cpuprofile=traffic.cpu ./internal/core/runtime/
//	go tool pprof -top traffic.cpu
//
// Baseline isolate for json.Marshal alone:
//
//	go test -bench=BenchmarkMarshalTrafficEvent_jsonOnly -benchmem -memprofile=traffic.mem ./internal/core/runtime/
//	go tool pprof -top traffic.mem
//
// Today json.Marshal(ev) dominates emitTrafficBTP/PTC CPU/allocs vs the rest of the emit path.
// Further optimization (subset payloads, codegen, or pooling) needs a product decision on
// capture fidelity and must prove copy semantics for [sdktraffic.PortBundle.Emit] consumers.

type nopBenchTrafficObserver struct{}

func (nopBenchTrafficObserver) OnObservation(context.Context, sdktraffic.Observation) error {
	return nil
}

func benchRetryRecvForTrafficEmit() (*retryRecvStream, aipapi.Event, sdk.PartMeta) {
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		TrafficObserver: nopBenchTrafficObserver{},
	})
	ex := TestExecutor()
	ex.RuntimeSnapshot = snap
	s := &retryRecvStream{
		attempt: testAttemptSlot(b2bua.BLegRecord{}, routing.AttemptCandidate{
			Primary: routing.Primary{Backend: "bench-backend", Model: "m"},
			Key:     "bench-backend:m",
		}, authorityLifecycle{}),
	}
	ev := aipapi.Event{
		Kind:  aipapi.EventTextDelta,
		Delta: strings.Repeat("x", 512),
	}
	pm := sdk.PartMeta{
		TraceID: "trace", ALegID: "a", BLegID: "b", AttemptSeq: 1,
	}
	return s, ev, pm
}

func BenchmarkEmitTrafficBTP_jsonMarshal(b *testing.B) {
	s, ev, pm := benchRetryRecvForTrafficEmit()
	ctx := b.Context()
	b.ReportAllocs()

	for b.Loop() {
		s.responsePipeline.emitTraffic(ctx, s.attempt.snapshot(), sdktraffic.LegBTP, ev, pm)
	}
}

func BenchmarkEmitTrafficPTC_jsonMarshal(b *testing.B) {
	s, ev, pm := benchRetryRecvForTrafficEmit()
	ctx := b.Context()
	b.ReportAllocs()

	for b.Loop() {
		_, _ = s.responsePipeline.emitTrafficPTCFinal(ctx, s.facts, s.attempt.snapshot(), ev, pm)
	}
}

func BenchmarkMarshalTrafficEvent_jsonOnly(b *testing.B) {
	_, ev, _ := benchRetryRecvForTrafficEmit()
	b.ReportAllocs()

	for b.Loop() {
		_, _ = json.Marshal(ev)
	}
}
