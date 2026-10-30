package runtimebundle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	ssmemory "github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/observers"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestBuildControlPlaneRuntime_Disabled_NoWrapping(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	runtime, err := buildControlPlaneRuntime(controlPlaneBuildInput{
		StartupContext: context.Background(),
		Cfg:            cfg,
	})
	if err != nil {
		t.Fatalf("disabled: unexpected error: %v", err)
	}
	if runtime != nil {
		t.Fatalf("disabled: expected nil runtime, got %+v", runtime)
	}
}

func TestBuildControlPlaneRuntime_Memory_Ready(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "memory"
	cfg.ControlPlane.RecordingPolicy = "best_effort"
	runtime, err := buildControlPlaneRuntime(controlPlaneBuildInput{
		StartupContext: context.Background(),
		Cfg:            cfg,
	})
	if err != nil {
		t.Fatalf("memory: unexpected error: %v", err)
	}
	if runtime == nil || !runtime.enabled {
		t.Fatalf("memory: expected enabled runtime")
	}
	if runtime.store == nil || runtime.recorder == nil || runtime.queries == nil || runtime.status == nil {
		t.Fatalf("memory: missing wired handles")
	}
	status, err := runtime.recorder.Status(context.Background())
	if err != nil {
		t.Fatalf("memory: status error: %v", err)
	}
	if status.State != cp.CapabilityReady {
		t.Fatalf("memory: expected ready status, got %q", status.State)
	}
}

func TestBuildControlPlaneRuntime_Sqlite_DurableReady(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "sqlite"
	cfg.ControlPlane.SQLitePath = t.TempDir() + "/cp.sqlite"
	cfg.ControlPlane.RecordingPolicy = "best_effort"
	runtime, err := buildControlPlaneRuntime(controlPlaneBuildInput{
		StartupContext: context.Background(),
		Cfg:            cfg,
	})
	if err != nil {
		t.Fatalf("sqlite: unexpected error: %v", err)
	}
	if runtime == nil || runtime.store == nil {
		t.Fatalf("sqlite: expected durable store")
	}
	if err := runtime.store.CheckReadiness(context.Background()); err != nil {
		t.Fatalf("sqlite: readiness: %v", err)
	}
	if runtime.closer == nil {
		t.Fatalf("sqlite: expected closer")
	}
	if err := runtime.closer(); err != nil {
		t.Fatalf("sqlite: closer: %v", err)
	}
}

func TestBuildControlPlaneRuntime_RequiredPreWork_FailsOnMemory(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "memory"
	cfg.ControlPlane.RecordingPolicy = "required_pre_work"
	_, err := buildControlPlaneRuntime(controlPlaneBuildInput{
		StartupContext: context.Background(),
		Cfg:            cfg,
	})
	if err == nil {
		t.Fatalf("expected validation error for required_pre_work on memory")
	}
}

// TestBuildControlPlaneRuntime_InvalidMaxTimeWindow_FailsAndClosesStore proves
// that an unvalidated config with query enabled and an invalid max_time_window
// fails startup at buildControlPlaneRuntime instead of silently building an
// unbounded query service. The store has already been opened when the parse
// error is detected, so the store closer must be disposed to avoid leaking the
// durable handle (consistent with the retention-window parse error path).
func TestBuildControlPlaneRuntime_InvalidMaxTimeWindow_FailsAndClosesStore(t *testing.T) {
	t.Parallel()
	store := &closeObservableStore{fakeRetentionStore: &fakeRetentionStore{}}
	cfg := &config.Config{}
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "memory"
	cfg.ControlPlane.RecordingPolicy = "best_effort"
	cfg.ControlPlane.Query.Enabled = true
	cfg.ControlPlane.Query.PathPrefix = "/cp"
	cfg.ControlPlane.Query.MaxTimeWindow = "not-a-duration"
	_, err := buildControlPlaneRuntime(controlPlaneBuildInput{
		StartupContext: context.Background(),
		Cfg:            cfg,
		StoreOverride:  store,
	})
	if err == nil {
		t.Fatal("expected buildControlPlaneRuntime to fail on invalid max_time_window")
	}
	if !strings.Contains(err.Error(), "max_time_window") {
		t.Fatalf("expected error to mention max_time_window, got %v", err)
	}
	if !store.closed.Load() {
		t.Fatal("control-plane store closer must be disposed when max_time_window parse fails after store open")
	}
}

func TestBuildControlPlaneRuntime_RetentionEnabled_WiresController(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "memory"
	cfg.ControlPlane.Retention.Enabled = true
	cfg.ControlPlane.Retention.Window = "24h"
	runtime, err := buildControlPlaneRuntime(controlPlaneBuildInput{
		StartupContext: context.Background(),
		Cfg:            cfg,
	})
	if err != nil {
		t.Fatalf("retention: %v", err)
	}
	if runtime.retention == nil {
		t.Fatalf("retention: expected controller wired")
	}
	res, err := runtime.retention.Apply(context.Background(), time.Now().Add(-time.Hour), cp.VisibilityDefault)
	if err != nil {
		t.Fatalf("retention apply: %v", err)
	}
	if res.Status.State != cp.CapabilityReady {
		t.Fatalf("retention: expected ready status, got %q", res.Status.State)
	}
}

func TestControlPlaneRuntime_Wrappers_DisabledArePassThrough(t *testing.T) {
	t.Parallel()
	var runtime *controlPlaneRuntime // nil runtime == disabled
	if got := runtime.wrapAuthSink(nil); got != nil {
		t.Fatalf("disabled wrapAuthSink: expected nil, got %T", got)
	}
	delegate, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := runtime.wrapB2BUA(delegate); got != delegate {
		t.Fatalf("disabled wrapB2BUA: expected pass-through")
	}
	ss := ssmemory.New(ssmemory.Options{})
	if got := runtime.wrapSecureSession(ss); got != ss {
		t.Fatalf("disabled wrapSecureSession: expected pass-through")
	}
	if got := runtime.policyObserver(); got != nil {
		t.Fatalf("disabled policyObserver: expected nil")
	}
	if got := runtime.usageObserver(); got != nil {
		t.Fatalf("disabled usageObserver: expected nil")
	}
}

func TestControlPlaneRuntime_Wrappers_EnabledWrap(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "memory"
	runtime, err := buildControlPlaneRuntime(controlPlaneBuildInput{
		StartupContext: context.Background(),
		Cfg:            cfg,
	})
	if err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if got := runtime.wrapAuthSink(nil); got == nil {
		t.Fatalf("enabled wrapAuthSink(nil): expected record-only adapter, got nil")
	}
	delegate, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := runtime.wrapB2BUA(delegate)
	if wrapped == delegate {
		t.Fatalf("enabled wrapB2BUA: expected decorator")
	}
	if _, ok := wrapped.(*observers.B2BUAStoreDecorator); !ok {
		t.Fatalf("enabled wrapB2BUA: expected *B2BUAStoreDecorator, got %T", wrapped)
	}
	ss := ssmemory.New(ssmemory.Options{})
	wrappedSS := runtime.wrapSecureSession(ss)
	if wrappedSS == ss {
		t.Fatalf("enabled wrapSecureSession: expected decorator")
	}
	if _, ok := wrappedSS.(*observers.SecureSessionStoreDecorator); !ok {
		t.Fatalf("enabled wrapSecureSession: expected *SecureSessionStoreDecorator, got %T", wrappedSS)
	}
	if _, ok := runtime.policyObserver().(*observers.PolicyObserverAdapter); !ok {
		t.Fatalf("enabled policyObserver: expected *PolicyObserverAdapter, got %T", runtime.policyObserver())
	}
	if _, ok := runtime.usageObserver().(*observers.UsageObserverAdapter); !ok {
		t.Fatalf("enabled usageObserver: expected *UsageObserverAdapter, got %T", runtime.usageObserver())
	}
}

func TestRedactedStoreUnavailableError_NoInfraLeak_ClassifiesUnavailable(t *testing.T) {
	t.Parallel()
	sensitive := []string{
		"postgres://operator:hunter2@db.internal:5432/cp?sslmode=disable",
		"CREATE TABLE cp_events (id TEXT PRIMARY KEY)",
		"modernc.org/sqlite",
		"pq: password authentication failed for user \"operator\"",
		"hunter2",
		"db.internal",
		"cp_events",
		"operator",
		"dsn=",
		"sqlite_path",
	}

	got := redactedStoreUnavailableError()
	if got == nil {
		t.Fatalf("expected non-nil redacted error")
	}
	msg := got.Error()
	for _, s := range sensitive {
		if strings.Contains(strings.ToLower(msg), strings.ToLower(s)) {
			t.Fatalf("redacted error leaks sensitive substring %q in: %q", s, msg)
		}
	}
	// The returned error must classify as the stable unavailable error and must
	// wrap only controlplane.ErrUnavailable (no raw infra error in the chain).
	if !errors.Is(got, controlplane.ErrUnavailable) {
		t.Fatalf("redacted error must classify as controlplane.ErrUnavailable, got: %v", got)
	}
	if code := controlplane.Classify(got); code != cp.ErrCodeUnavailable {
		t.Fatalf("expected classified code %q, got %q", cp.ErrCodeUnavailable, code)
	}
	if unwrapped := errors.Unwrap(got); unwrapped != nil && unwrapped != controlplane.ErrUnavailable {
		t.Fatalf("redacted error must wrap only controlplane.ErrUnavailable, got: %v", unwrapped)
	}
}
