package traffic_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	coretraffic "github.com/aiproxer/aiproxer/internal/core/traffic"
	"github.com/aiproxer/aiproxer/internal/testkit"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

type testRedactor struct{}

func (testRedactor) ID() string { return "r" }

func (testRedactor) Redact(_ context.Context, _ sdktraffic.Leg, _ sdktraffic.CaptureMeta, _ []byte) ([]byte, error) {
	return []byte("Y"), nil
}

func TestPortBundleFromSnapshot_nilSnapshot_noPanic(t *testing.T) {
	t.Parallel()
	b := coretraffic.PortBundleFromSnapshot(nil)
	if !b.EmitIsNoop() {
		t.Fatal("expected empty port bundle from nil snapshot to be a no-op emit")
	}
}

func TestPortBundleFromSnapshot_eachLegMeta(t *testing.T) {
	t.Parallel()
	raw := &testkit.RecordingRawCaptureSink{}
	obs := &testkit.RecordingTrafficObserver{}
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		TrafficObserver: obs,
		RawCapture:      raw,
	})
	b := coretraffic.PortBundleFromSnapshot(snap)
	meta := sdktraffic.CaptureMeta{
		TraceID: "tr", ALegID: "a1", BLegID: "b2", AttemptSeq: 3,
		PrincipalID: "p1", SessionID: "s9", BackendID: "be",
	}
	b.Emit(context.Background(), sdktraffic.LegCTP, meta, "aip/canonical+json", "application/json", []byte("x"))
	if len(raw.Seen) != 1 || raw.Seen[0].TraceID != "tr" || raw.Seen[0].BLegID != "b2" {
		t.Fatalf("raw meta=%+v", raw.Seen)
	}
	if len(obs.Seen) != 1 || obs.Seen[0].Leg != sdktraffic.LegCTP || string(obs.Seen[0].Body) != "x" {
		t.Fatalf("obs=%+v", obs.Seen)
	}
}

func TestPortBundle_privilegedRawSeesPreRedaction(t *testing.T) {
	t.Parallel()
	raw := &testkit.RecordingRawCaptureSink{}
	obs := &testkit.RecordingTrafficObserver{}
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		TrafficObserver: obs,
		RawCapture:      raw,
		FeaturePlanes: testkit.FreezeBundle(testkit.FeatureBundle(t, "feat", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "feat", []sdktraffic.Redactor{testRedactor{}})
		}, nil)),
	})
	coretraffic.PortBundleFromSnapshot(snap).Emit(context.Background(), sdktraffic.LegBTP, sdktraffic.CaptureMeta{TraceID: "t"}, "p", "application/json", []byte("X"))
	if len(raw.Data) != 1 || string(raw.Data[0]) != "X" {
		t.Fatalf("raw data=%q", raw.Data)
	}
	if len(obs.Seen) != 1 || string(obs.Seen[0].Body) != "Y" {
		t.Fatalf("obs body=%q", obs.Seen[0].Body)
	}
}
