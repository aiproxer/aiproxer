package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

func BenchmarkPhase5_disabledRuntimeNoFeatureParticipants(b *testing.B) {
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{})
	if len(snap.AttemptTransforms()) != 0 || len(snap.StreamObserverFactories()) != 0 {
		b.Fatal("disabled snapshot must have empty reasoning stages")
	}
	emptyGen, err := featurebundle.MergeBundlesGenerated()
	if err != nil {
		b.Fatal(err)
	}
	if len(aipfeature.Get(emptyGen.Frozen, aipfeature.PlaneAttemptTransforms)) != 0 {
		b.Fatal("absent FeatureBundle merge must stay empty")
	}
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		b.Fatal(err)
	}
	var opened atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot, ex.Rand = st, bus, snap, routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"be": p5ReplayBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opened.Add(1)
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		}),
	}
	b.ReportAllocs()
	for b.Loop() {
		call := p5ObserveCall()
		stream, err := ex.Execute(b.Context(), call)
		if err != nil {
			b.Fatalf("execute: %v", err)
		}
		if _, err := aipapi.Collect(b.Context(), stream); err != nil {
			b.Fatalf("collect: %v", err)
		}
		p5AssertNoReasoningParticipants(b, ex.RuntimeSnapshot)
		inv := reasoningpreservation.BuildSafeInventory(reasoningpreservation.Config{}, nil)
		if inv.Enabled || len(inv.AggregateCounters) != 0 {
			b.Fatal("disabled inventory must stay empty during runtime execution")
		}
	}
	if opened.Load() == 0 {
		b.Fatal("disabled runtime must still open backend")
	}
}
