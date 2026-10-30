package aipruntime_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

type facadeQuerier struct{}

func (facadeQuerier) List(context.Context, metering.Query) (metering.Page, error) {
	return metering.Page{}, nil
}

func TestExternalFacade_ExecutorViewAndReadyBeforeAfterClose(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := aipruntime.Build(ctx, aipruntime.Options{ConfigPath: repoConfigPath(t), LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	view := rt.ExecutorView()
	if view == nil {
		t.Fatal("ExecutorView required before Close")
	}
	if !rt.Ready() {
		t.Fatal("Ready required before Close")
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if rt.ExecutorView() == nil {
		t.Fatal("ExecutorView must remain non-nil after Close")
	}
	if rt.ExecutorView() != view {
		t.Fatal("ExecutorView identity must survive Close")
	}
	if rt.Ready() {
		t.Fatal("Ready must be false after Close")
	}
}

func TestExternalFacade_CapabilityAccessorsAndClosedVocabulary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath:       repoConfigPath(t),
		LogWriter:        io.Discard,
		TrafficObservers: []traffic.Observer{noopTraffic{}},
		UsageObservers:   []usage.Observer{noopUsage{}},
		MeteringQuerier:  facadeQuerier{},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })

	if !rt.HasTrafficObservers() || !rt.HasUsageObservers() {
		t.Fatal("production observer capabilities must derive from host attachment")
	}
	if !rt.HasProductionMeteringQuerier() || rt.MeteringQuerier() == nil {
		t.Fatal("metering querier capability must derive from host")
	}
	state := rt.ExecutableGenerationState()
	switch state {
	case controlplane.CapabilityReady, controlplane.CapabilityDisabled,
		controlplane.CapabilityUnavailable, controlplane.CapabilityDegraded:
	default:
		t.Fatalf("ExecutableGenerationState=%q outside closed vocabulary", state)
	}
	if rt.SnapshotGenerationID() == 0 {
		t.Fatal("SnapshotGenerationID must be non-zero on ready facade")
	}
	caps := rt.Capabilities()
	if caps.TrafficObservers != rt.HasTrafficObservers() || caps.SnapshotGenerationID != rt.SnapshotGenerationID() {
		t.Fatal("named accessors must match Capabilities() snapshot")
	}
	report := rt.ReadinessReport()
	if report == nil {
		t.Fatal("ReadinessReport must be exposed on facade")
	}
	if _, err := report.Report(ctx); err != nil {
		t.Fatalf("ReadinessReport.Report: %v", err)
	}
}

func TestExternalFacade_ReloadStatusImportable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := aipruntime.Build(ctx, aipruntime.Options{ConfigPath: repoConfigPath(t), LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if rt.ReloadControl() == nil {
		t.Fatal("ReloadControl must be bound")
	}
	res := rt.Reload(ctx, aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI, SafeActor: "external"})
	if res.Category != aipruntime.ResultNoop && res.Category != aipruntime.ResultPublished {
		t.Fatalf("reload category=%q", res.Category)
	}
	st := rt.ReloadStatus()
	if st.ActiveGeneration < 1 {
		t.Fatalf("active generation=%d", st.ActiveGeneration)
	}
}

func TestExternalFacade_SnapshotRefreshSemantics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := aipruntime.Build(ctx, aipruntime.Options{ConfigPath: repoConfigPath(t), LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	before := rt.ReloadStatus().ActiveGeneration
	if err := rt.RefreshSnapshots(ctx); err != nil {
		t.Fatalf("RefreshSnapshots success path: %v", err)
	}
	if rt.ReloadStatus().ActiveGeneration != before {
		t.Fatal("RefreshSnapshots must not publish config generations")
	}
	empty := &aipruntime.Runtime{}
	if err := empty.RefreshSnapshots(ctx); err == nil {
		t.Fatal("RefreshSnapshots on empty Runtime must error")
	}
}

func TestExternalClose_RetryIdempotencyAtPublicBoundary(t *testing.T) {
	t.Parallel()
	host := &facadeFakeHost{
		ready:     true,
		closeErrs: []error{errors.New("tracing shutdown boom")},
	}
	rt := aipruntime.NewRuntimeWithHostForTest(host)
	err := rt.Close(context.Background())
	if err == nil || err.Error() != "tracing shutdown boom" {
		t.Fatalf("first Close err=%v", err)
	}
	if host.closeCalls.Load() != 1 {
		t.Fatalf("close calls=%d", host.closeCalls.Load())
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if host.closeCalls.Load() != 2 {
		t.Fatalf("retry must invoke host Close again; calls=%d", host.closeCalls.Load())
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
	if host.closeCalls.Load() != 2 {
		t.Fatalf("successful Close must not re-enter host; calls=%d", host.closeCalls.Load())
	}
}
