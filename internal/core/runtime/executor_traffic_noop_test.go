package runtime_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// recordingTrafficObserver captures emitted observations grouped by traffic leg for functional tests.
type recordingTrafficObserver struct {
	mu   sync.Mutex
	legs map[sdktraffic.Leg][]sdktraffic.Observation
}

// newRecordingTrafficObserver constructs an initialized recording traffic observer.
func newRecordingTrafficObserver() *recordingTrafficObserver {
	return &recordingTrafficObserver{
		legs: make(map[sdktraffic.Leg][]sdktraffic.Observation),
	}
}

// OnObservation records the observation under its traffic leg.
func (r *recordingTrafficObserver) OnObservation(_ context.Context, obs sdktraffic.Observation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.legs[obs.Leg] = append(r.legs[obs.Leg], obs)
	return nil
}

// observationsFor returns a shallow copy of the recorded observations for the given leg.
func (r *recordingTrafficObserver) observationsFor(leg sdktraffic.Leg) []sdktraffic.Observation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sdktraffic.Observation(nil), r.legs[leg]...)
}

// benchDiscardTrafficObserver is a non-retaining observer that exercises the active emission path
// during benchmarks without accumulating payloads across iterations.
type benchDiscardTrafficObserver struct{}

// OnObservation discards the observation without performing allocations.
func (benchDiscardTrafficObserver) OnObservation(_ context.Context, _ sdktraffic.Observation) error {
	return nil
}

// trafficTestExecutor builds a minimal test executor configured with a fixed mock backend and
// a snapshot bound to the provided traffic observer.
func trafficTestExecutor(tb testing.TB, tobs sdktraffic.Observer) *runtime.Executor {
	tb.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		tb.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		TrafficObserver: tobs,
	})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"mock": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "hello"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	return ex
}

// TestExecutor_Traffic_DisabledSkipsPayloadMarshal verifies that when traffic observation is disabled,
// execution completes successfully and EmitIsNoop short-circuits before JSON serialization.
func TestExecutor_Traffic_DisabledSkipsPayloadMarshal(t *testing.T) {
	t.Parallel()
	ex := trafficTestExecutor(t, nil)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "mock:model-1"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello world")}},
		},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("unexpected collect error: %v", err)
	}
	if collected.Text.String() != "hello" {
		t.Fatalf("expected text %q, got %q", "hello", collected.Text.String())
	}
}

// TestExecutor_Traffic_EnabledEmitsCTPAndPTB verifies that when traffic observation is active,
// both CTP and PTB payloads are marshaled and delivered to the observer.
func TestExecutor_Traffic_EnabledEmitsCTPAndPTB(t *testing.T) {
	t.Parallel()
	recorder := newRecordingTrafficObserver()
	ex := trafficTestExecutor(t, recorder)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "mock:model-1"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello world")}},
		},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}
	_, err = aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("unexpected collect error: %v", err)
	}

	ctpObs := recorder.observationsFor(sdktraffic.LegCTP)
	if len(ctpObs) != 1 {
		t.Fatalf("expected 1 CTP observation, got %d", len(ctpObs))
	}
	var ctpCall aipapi.Call
	if err := json.Unmarshal(ctpObs[0].Body, &ctpCall); err != nil {
		t.Fatalf("failed to unmarshal CTP body: %v", err)
	}
	if len(ctpCall.Messages) != 1 {
		t.Fatalf("expected 1 message in CTP body, got %d", len(ctpCall.Messages))
	}

	ptbObs := recorder.observationsFor(sdktraffic.LegPTB)
	if len(ptbObs) != 1 {
		t.Fatalf("expected 1 PTB observation, got %d", len(ptbObs))
	}
	var ptbCall aipapi.Call
	if err := json.Unmarshal(ptbObs[0].Body, &ptbCall); err != nil {
		t.Fatalf("failed to unmarshal PTB body: %v", err)
	}
	if len(ptbCall.Messages) != 1 {
		t.Fatalf("expected 1 message in PTB body, got %d", len(ptbCall.Messages))
	}
}

// BenchmarkExecutor_TrafficDisabled benchmarks request execution when traffic observation is disabled.
func BenchmarkExecutor_TrafficDisabled(b *testing.B) {
	ex := trafficTestExecutor(b, nil)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "mock:model-1"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello world from benchmark prompt with realistic payload size")}},
		},
	}
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		stream, err := ex.Execute(ctx, call)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = aipapi.Collect(ctx, stream)
	}
}

// BenchmarkExecutor_TrafficEnabled benchmarks request execution when traffic observation is enabled,
// using a non-retaining observer to isolate emission overhead from retention memory.
func BenchmarkExecutor_TrafficEnabled(b *testing.B) {
	ex := trafficTestExecutor(b, benchDiscardTrafficObserver{})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "mock:model-1"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello world from benchmark prompt with realistic payload size")}},
		},
	}
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		stream, err := ex.Execute(ctx, call)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = aipapi.Collect(ctx, stream)
	}
}
