package aipruntime_test

import (
	"context"
	"io"
	"testing"

	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// TestCapabilityReporting_DogfoodFacadeHostSnapshot freezes externally observable
// runtime capability reporting on the public facade path (derived host snapshot)
// for the standard dogfood composition.
func TestCapabilityReporting_DogfoodFacadeHostSnapshot(t *testing.T) {
	t.Parallel()
	path := bpkit.WriteDogfoodLocalStubConfig(t)
	ctx := context.Background()
	rt, err := aipruntime.Build(ctx, aipruntime.Options{ConfigPath: path, LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })

	if !rt.Ready() {
		t.Fatal("Ready must be true with active generation")
	}
	if rt.ExecutorView() == nil {
		t.Fatal("ExecutorView required for capability-bearing runtime")
	}
	if id := rt.SnapshotGenerationID(); id == 0 {
		t.Fatal("SnapshotGenerationID must be non-zero on dogfood facade")
	}
	state := rt.ExecutableGenerationState()
	switch state {
	case cp.CapabilityReady, cp.CapabilityDisabled, cp.CapabilityUnavailable, cp.CapabilityDegraded:
	default:
		t.Fatalf("ExecutableGenerationState=%q outside closed capability vocabulary", state)
	}
	report := rt.ReadinessReport()
	if report == nil {
		t.Fatal("ReadinessReport must be exposed on facade")
	}
	if _, err := report.Report(ctx); err != nil {
		t.Fatalf("ReadinessReport.Report: %v", err)
	}
	if rt.HasProductionMetering() {
		t.Fatal("HasProductionMetering must be false without MeteringRecorder")
	}
	if rt.HasTrafficObservers() || rt.HasUsageObservers() || rt.HasProductionEvidenceSink() {
		t.Fatal("observer attachment flags must be false without production injections")
	}
	if rt.HasProductionMeteringQuerier() {
		t.Fatal("HasProductionMeteringQuerier must be false without MeteringQuerier")
	}
}
