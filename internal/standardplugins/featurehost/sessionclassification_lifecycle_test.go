package featurehost

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	featureclassification "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	hostclassification "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

func TestCompileGeneration_EnabledSessionClassificationAddsLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runtime, err := NewProcess(ctx, ProcessInput{Logger: slog.Default()})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("Runtime.Close: %v", err)
		}
	})

	compile := func(registrations []aipsdk.Registration) GenerationOutput {
		t.Helper()
		out, err := runtime.CompileGeneration(ctx, GenerationInput{Registrations: registrations})
		if err != nil {
			t.Fatalf("CompileGeneration: %v", err)
		}
		return out
	}

	withoutFeature := compile(nil)
	disabledFeature := compile([]aipsdk.Registration{{
		ID:      featureclassification.ID,
		Kind:    aipsdk.PluginKindFeature,
		Enabled: false,
	}})
	withFeature := compile([]aipsdk.Registration{{
		ID:      featureclassification.ID,
		Kind:    aipsdk.PluginKindFeature,
		Enabled: true,
	}})
	if got := len(disabledFeature.Lifecycles); got != len(withoutFeature.Lifecycles) {
		t.Fatalf("disabled session-classification lifecycles = %d, want absent baseline %d", got, len(withoutFeature.Lifecycles))
	}
	want := len(withoutFeature.Lifecycles) + 1
	if got := len(withFeature.Lifecycles); got != want {
		t.Fatalf("enabled session-classification lifecycles = %d, want %d (one lifecycle beyond ordinary features)", got, want)
	}
	lifecycle, ok := withFeature.Lifecycles[len(withFeature.Lifecycles)-1].(*sessionClassificationGenerationLifecycle)
	if !ok || lifecycle == nil || !lifecycle.SafeUnderCandidateOverlap() {
		t.Fatalf("enabled session-classification lifecycle = %T, want overlap-safe generation lifecycle", withFeature.Lifecycles[len(withFeature.Lifecycles)-1])
	}
	if runtime.sessionClassification.Coordinator() != nil {
		t.Fatal("CompileGeneration initialized classification state before candidate preparation")
	}
}

func TestNewProcess_NeverEnabledLeavesClassificationStoreSchemaAndNetworkUnused(t *testing.T) {
	// Keep serial because this test temporarily observes the default HTTP transport.
	originalTransport := http.DefaultTransport
	transport := &countingRoundTripper{}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	sqlDB, bunDB := openFeatureHostClassificationDB(t)
	registry := prometheus.NewRegistry()
	runtime, err := NewProcess(context.Background(), ProcessInput{
		Logger:          slog.Default(),
		BunDB:           bunDB,
		MetricsRegistry: registry,
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("Runtime.Close: %v", err)
		}
	})

	if runtime.sessionClassification.Coordinator() != nil {
		t.Fatal("never-enabled process initialized classification state")
	}
	if _, err := runtime.CompileGeneration(context.Background(), GenerationInput{}); err != nil {
		t.Fatalf("CompileGeneration without classification: %v", err)
	}
	if got := featureHostClassificationTableCount(t, bunDB); got != 0 {
		t.Fatalf("classification table count after process startup = %d, want 0", got)
	}
	if got := featureHostStoreReadyMetric(t, registry); got != 0 {
		t.Fatalf("classification store readiness after process startup = %v, want 0", got)
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("default HTTP requests while classification was never enabled = %d, want 0", got)
	}
	if err := sqlDB.PingContext(context.Background()); err != nil {
		t.Fatalf("process construction or Close took ownership of borrowed Bun DB: %v", err)
	}
}

type countingRoundTripper struct{ calls atomic.Int32 }

func (r *countingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls.Add(1)
	return nil, errors.New("unexpected HTTP request")
}

func TestSessionClassificationLifecycle_OverlappingGenerationsShareStateUntilProcessClose(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runtime, err := NewProcess(ctx, ProcessInput{Logger: slog.Default()})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("Runtime.Close: %v", err)
		}
	})
	registration := aipsdk.Registration{ID: featureclassification.ID, Kind: aipsdk.PluginKindFeature, Enabled: true}
	compile := func() *sessionClassificationGenerationLifecycle {
		t.Helper()
		out, err := runtime.CompileGeneration(ctx, GenerationInput{Registrations: []aipsdk.Registration{registration}})
		if err != nil {
			t.Fatalf("CompileGeneration: %v", err)
		}
		for _, candidate := range out.Lifecycles {
			if lifecycle, ok := candidate.(*sessionClassificationGenerationLifecycle); ok {
				return lifecycle
			}
		}
		t.Fatal("enabled generation did not contain session-classification lifecycle")
		return nil
	}

	first := compile()
	second := compile()
	if err := first.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	coordinator := runtime.sessionClassification.Coordinator()
	if coordinator == nil {
		t.Fatal("first Start did not initialize process classification state")
	}
	if err := second.Start(ctx); err != nil {
		t.Fatalf("overlapping Start: %v", err)
	}
	if runtime.sessionClassification.Coordinator() != coordinator {
		t.Fatal("overlapping generations did not share one initialized coordinator")
	}
	if err := first.Stop(ctx); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if runtime.sessionClassification.Coordinator() != coordinator {
		t.Fatal("first Stop destroyed shared state still owned by the second generation")
	}
	if err := second.Stop(ctx); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if runtime.sessionClassification.Coordinator() != coordinator {
		t.Fatal("generation Stop destroyed process state before Runtime.Close")
	}

	if err := runtime.Close(); err != nil {
		t.Fatalf("Runtime.Close: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("repeated Runtime.Close: %v", err)
	}
	if runtime.sessionClassification.Coordinator() != nil {
		t.Fatal("final process Close did not dispose shared classification state exactly once")
	}
}

func TestCompileGeneration_FailureDoesNotInitializeOrPublishClassificationLifecycle(t *testing.T) {
	t.Parallel()

	runtime, err := NewProcess(context.Background(), ProcessInput{Logger: slog.Default()})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	failure := errors.New("candidate rejected")
	out, err := runtime.CompileGeneration(context.Background(), GenerationInput{
		Registrations: []aipsdk.Registration{{ID: featureclassification.ID, Kind: aipsdk.PluginKindFeature, Enabled: true}},
		FaultInject:   failure,
	})
	if !errors.Is(err, failure) {
		t.Fatalf("CompileGeneration error = %v, want injected candidate failure", err)
	}
	if len(out.Lifecycles) != 0 || !out.Planes.IsZero() {
		t.Fatalf("failed candidate published feature output: lifecycles=%d planes-zero=%t", len(out.Lifecycles), out.Planes.IsZero())
	}
	if runtime.sessionClassification.Coordinator() != nil {
		t.Fatal("failed candidate initialized process classification state")
	}
}

func openFeatureHostClassificationDB(t *testing.T) (*sql.DB, *bun.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "classification-lifecycle.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bunDB.Close() })
	return sqlDB, bunDB
}

func featureHostClassificationTableCount(t *testing.T, bunDB *bun.DB) int {
	t.Helper()
	var count int
	if err := bunDB.NewRaw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'session_classification'").Scan(context.Background(), &count); err != nil {
		t.Fatalf("count classification table: %v", err)
	}
	return count
}

func featureHostStoreReadyMetric(t *testing.T, registry *prometheus.Registry) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "aip_session_classification_store_ready" {
			continue
		}
		if len(family.GetMetric()) != 1 || family.GetMetric()[0].GetGauge() == nil {
			t.Fatalf("store readiness metric family = %+v, want one gauge", family)
		}
		return family.GetMetric()[0].GetGauge().GetValue()
	}
	t.Fatal("store readiness metric was not gathered")
	return 0
}

// TestSessionClassificationLifecycle_CancelledStartDoesNotConsumeTheLifecycle
// pins the failure half of Start: an Acquire rejected by an already-cancelled
// context surfaces the cancellation without consuming the lifecycle, so a later
// Start with a live context still initializes the shared process state.
func TestSessionClassificationLifecycle_CancelledStartDoesNotConsumeTheLifecycle(t *testing.T) {
	t.Parallel()

	holder := hostclassification.NewStateHolder(nil, nil)
	t.Cleanup(func() { _ = holder.Close() })
	lifecycle := &sessionClassificationGenerationLifecycle{holder: holder}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lifecycle.Start(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start with a cancelled context = %v, want context.Canceled", err)
	}
	if holder.Coordinator() != nil {
		t.Fatal("a cancelled Start initialized process classification state")
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatalf("Start after a cancelled Start = %v, want a live-context initialization", err)
	}
	if holder.Coordinator() == nil {
		t.Fatal("Start after a cancelled Start did not initialize shared process state")
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestSessionClassificationLifecycle_StoppedLifecycleRejectsRestart pins the
// terminal half of the lifecycle contract: Stop is final for one generation
// lifecycle, so Start afterwards returns ErrStateHolderClosed instead of
// reacquiring process state the retired generation no longer owns, and a
// repeated Stop stays a no-op.
func TestSessionClassificationLifecycle_StoppedLifecycleRejectsRestart(t *testing.T) {
	t.Parallel()

	holder := hostclassification.NewStateHolder(nil, nil)
	t.Cleanup(func() { _ = holder.Close() })
	lifecycle := &sessionClassificationGenerationLifecycle{holder: holder}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := lifecycle.Start(context.Background()); !errors.Is(err, hostclassification.ErrStateHolderClosed) {
		t.Fatalf("Start after Stop = %v, want ErrStateHolderClosed", err)
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("repeated Stop = %v, want nil", err)
	}
}

// TestCompileGeneration_ReKeyedSessionClassificationRegistrationPublishesLifecycle
// pins the documented registry-factory-key resolution: a registration whose id is
// re-keyed to the canonical classification factory still publishes the bounded
// classifier plane and its generation lifecycle.
func TestCompileGeneration_ReKeyedSessionClassificationRegistrationPublishesLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runtime, err := NewProcess(ctx, ProcessInput{Logger: slog.Default()})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("Runtime.Close: %v", err)
		}
	})
	out, err := runtime.CompileGeneration(ctx, GenerationInput{Registrations: []aipsdk.Registration{{
		ID:          "rekeyed-session-classification",
		FactoryKind: featureclassification.ID,
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     true,
	}}})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	if classifier := aipfeature.Get(out.Planes, aipfeature.PlaneSessionClassifier); classifier == nil {
		t.Fatal("a re-keyed enabled registration published no classifier plane")
	}
	published := false
	for _, candidate := range out.Lifecycles {
		if _, ok := candidate.(*sessionClassificationGenerationLifecycle); ok {
			published = true
		}
	}
	if !published {
		t.Fatalf("a re-keyed enabled registration published no session-classification lifecycle (lifecycles = %d)", len(out.Lifecycles))
	}
}
