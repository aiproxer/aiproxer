package runtimehost_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"github.com/prometheus/client_golang/prometheus"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type panicLogHandler struct{}

func (panicLogHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (panicLogHandler) Handle(context.Context, slog.Record) error { panic("telemetry sink failed") }
func (panicLogHandler) WithAttrs([]slog.Attr) slog.Handler        { return panicLogHandler{} }
func (panicLogHandler) WithGroup(string) slog.Handler             { return panicLogHandler{} }

type cleanupLifecycleHandler struct {
	mu                 sync.Mutex
	cleanupFailedCount int
	text               strings.Builder
}

func (*cleanupLifecycleHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *cleanupLifecycleHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.text.WriteString(record.Message)
	record.Attrs(func(attr slog.Attr) bool {
		h.text.WriteString(attr.Key)
		h.text.WriteByte('=')
		h.text.WriteString(attr.Value.String())
		return true
	})
	if record.Message == "reload lifecycle stage" {
		var stage, result string
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "stage" {
				stage = attr.Value.String()
			}
			if attr.Key == "result" {
				result = attr.Value.String()
			}
			return true
		})
		if stage == "cleanup" && result == "cleanup_failed" {
			h.cleanupFailedCount++
		}
	}
	return nil
}
func (h *cleanupLifecycleHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *cleanupLifecycleHandler) WithGroup(string) slog.Handler      { return h }

func (h *cleanupLifecycleHandler) snapshot() (int, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cleanupFailedCount, h.text.String()
}

func TestReloadObservability_LogsSpansHistoryAndMetrics(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	reg := prometheus.NewRegistry()
	prom := metrics.RegisterReloadProm(reg)

	mgr := runtimehost.NewManager(4, nil)
	plane := newFakePlane(nil)
	initial := mgr.PrepareRequestPlane("boot", plane)
	initial.SetMetaHints(runtimehost.MetaHints{PublicFingerprint: "fp-boot"})
	if err := mgr.Publish(initial); err != nil {
		t.Fatal(err)
	}
	// Pin the boot generation so Manager's automatic post-publish retirement
	// (task 7.3) cannot quiesce/close/sweep it before the RetainedGenerations
	// assertion below observes it.
	bootLease, ok := mgr.Acquire()
	if !ok {
		t.Fatal("acquire boot generation")
	}
	bootPin, ok := bootLease.TransferPin(runtimehost.PinProvider)
	if !ok {
		t.Fatal("pin boot generation")
	}
	t.Cleanup(bootPin.Release)

	digest := [32]byte{9, 9, 9}
	src := &fakeSource{
		path: "/fixed/config.yaml",
		snap: configsource.SourceSnapshot{
			Bytes:          []byte("x: 1"),
			HandleIdentity: configsource.FileIdentity{Platform: "windows", Scheme: "win-fileid", Opaque: [32]byte{1}},
			Size:           4,
			ModTime:        time.Unix(1, 0).UTC(),
			PrivateDigest:  digest,
		},
		atomic: configsource.AtomicEligible,
	}
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		return &config.EffectiveConfig{
			Config:   &config.Config{Routing: config.RoutingConfig{MaxAttempts: 3}},
			Identity: config.EffectiveIdentity{PrivateDigest: digest, PublicFingerprint: "fp-cand"},
			LoadedAt: time.Unix(2, 0).UTC(),
		}, nil
	})
	compile := &controllableCompiler{kinds: map[string]int{"local-stub": 1}}

	obs := runtimehost.NewReloadObserver(runtimehost.ReloadObserverDeps{
		Logger:  logger,
		Tracer:  tp.Tracer("aip.reload"),
		Metrics: prom,
	})

	coord, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source:  src,
		Loader:  loader,
		Compile: compile,
		Manager: mgr,
		// This test exercises observability, not timeout classification. Leave
		// enough headroom for a repository-wide Windows run under CPU pressure.
		Timeout:  30 * time.Second,
		Observer: obs,
		ActiveEffective: &config.EffectiveConfig{
			Config:   &config.Config{Routing: config.RoutingConfig{MaxAttempts: 1}},
			Identity: config.EffectiveIdentity{PrivateDigest: [32]byte{1}, PublicFingerprint: "fp-old"},
		},
		ActiveSource: &configsource.ActiveSourceVersion{
			HandleIdentity: configsource.FileIdentity{Platform: "windows", Scheme: "win-fileid", Opaque: [32]byte{0}},
			PrivateDigest:  [32]byte{1},
		},
		Classify: func(_, _ *config.EffectiveConfig) ([]configreload.SafeChange, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	res := coord.Reload(context.Background(), sdkreload.Trigger{
		Kind:       sdkreload.TriggerAPI,
		AcceptedAt: time.Now().UTC(),
		SafeActor:  "test-actor",
	})
	if res.Category != sdkreload.ResultPublished {
		t.Fatalf("category=%q want published", res.Category)
	}

	st := coord.Status()
	if st.LastSuccess.Category != sdkreload.ResultPublished {
		t.Fatalf("LastSuccess=%q", st.LastSuccess.Category)
	}
	if st.RetainedGenerations < 1 {
		t.Fatalf("RetainedGenerations=%d want >=1", st.RetainedGenerations)
	}
	if len(st.History) == 0 {
		t.Fatal("expected bounded status history")
	}
	if got := st.History[len(st.History)-1].CandidateGeneration; got != res.ActiveGeneration {
		t.Fatalf("candidate generation=%d want published generation %d", got, res.ActiveGeneration)
	}

	logOut := logBuf.String()
	for _, need := range []string{`"attempt_id"`, `"trigger"`, `"stage"`, `"result"`, `"active_generation"`, `"duration_ms"`} {
		if !strings.Contains(logOut, need) {
			t.Fatalf("structured reload log missing %s; got %s", need, logOut)
		}
	}
	if strings.Contains(logOut, "sk-") || strings.Contains(logOut, "password=") {
		t.Fatalf("log leaked secret material: %s", logOut)
	}

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected process-owned reload spans")
	}
	found := map[string]bool{}
	for _, sp := range spans {
		found[sp.Name] = true
	}
	stageHits := 0
	for _, name := range []string{"read", "load", "classify", "compile", "prepare", "publish"} {
		if found[name] {
			stageHits++
		}
	}
	if stageHits < 4 {
		t.Fatalf("expected most stage spans, found=%v", found)
	}

	obs.RefreshGauges(mgr)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var sawAttempts bool
	for _, f := range families {
		if f.GetName() == "aip_reload_attempts_total" && len(f.GetMetric()) > 0 {
			sawAttempts = true
		}
	}
	if !sawAttempts {
		t.Fatal("expected reload attempt counter observations")
	}
}

func TestReloadObservability_PanickingSinkCannotEscape(t *testing.T) {
	t.Parallel()
	obs := runtimehost.NewReloadObserver(runtimehost.ReloadObserverDeps{
		Logger: slog.New(panicLogHandler{}),
	})
	ctx, endAttempt := obs.BeginAttempt(context.Background(), sdkreload.Trigger{
		Kind: sdkreload.TriggerAPI,
	}, 1, 1)
	_, endStage := obs.BeginStage(ctx, configreload.StagePublish)
	endStage(string(sdkreload.ResultPublished))
	endAttempt(sdkreload.Result{
		Category:         sdkreload.ResultPublished,
		AttemptID:        1,
		ActiveGeneration: 2,
	})
	obs.ObserveLifecycle(ctx, "cleanup", "ok", time.Millisecond)
}

func TestReloadObservability_FailedReloadDoesNotChangeActiveReadiness(t *testing.T) {
	t.Parallel()
	mgr := runtimehost.NewManager(2, nil)
	plane := newFakePlane(nil)
	initial := mgr.PrepareRequestPlane("boot", plane)
	if err := mgr.Publish(initial); err != nil {
		t.Fatal(err)
	}
	activeBefore := mgr.Active().ID()

	src := &fakeSource{
		path: "/fixed/config.yaml",
		err:  &configsource.IntegrityError{Category: configsource.CategoryNonAtomicUpdate},
	}
	obs := runtimehost.NewReloadObserver(runtimehost.ReloadObserverDeps{})
	coord, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source: src,
		Loader: runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
			return &config.EffectiveConfig{Config: &config.Config{}}, nil
		}),
		Compile:  &controllableCompiler{kinds: map[string]int{"local-stub": 1}},
		Manager:  mgr,
		Observer: obs,
		ActiveEffective: &config.EffectiveConfig{
			Config:   &config.Config{},
			Identity: config.EffectiveIdentity{PrivateDigest: [32]byte{1}, PublicFingerprint: "fp"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	readyBefore := runtimehost.DataPlaneReady(mgr)
	res := coord.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if res.Category != sdkreload.ResultSourceIntegrity && res.Category != sdkreload.ResultInvalid {
		t.Fatalf("category=%q want source-integrity/invalid", res.Category)
	}
	if mgr.Active() == nil || mgr.Active().ID() != activeBefore {
		t.Fatal("active generation must remain last-good after failed reload")
	}
	readyAfter := runtimehost.DataPlaneReady(mgr)
	if !readyBefore || !readyAfter {
		t.Fatalf("data-plane readiness before=%v after=%v; reload-control failure must not fail healthy active", readyBefore, readyAfter)
	}
	st := coord.Status()
	if st.LastFailure.Category == "" {
		t.Fatal("expected LastFailure recorded separately from readiness")
	}
	if !st.ControlDegraded {
		t.Fatal("reload-control posture should be visible without flipping data-plane ready")
	}
}

func TestAttemptRunner_FailedReadCleanupErrorEmitsOneBoundedLifecycleEvent(t *testing.T) {
	t.Parallel()
	markerCause := errors.New("raw private close detail")
	handler := &cleanupLifecycleHandler{}
	obs := runtimehost.NewReloadObserver(runtimehost.ReloadObserverDeps{Logger: slog.New(handler)})
	mgr := runtimehost.NewManager(2, nil)
	initial := mgr.PrepareRequestPlane("boot", newFakePlane(nil))
	if err := mgr.Publish(initial); err != nil {
		t.Fatal(err)
	}
	sourceErr := errors.Join(
		&configsource.IntegrityError{Category: configsource.CategoryNonAtomicUpdate},
		configsource.MarkSourceCleanupFailure(markerCause),
	)
	coord, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source: &fakeSource{path: "/fixed/config.yaml", err: sourceErr},
		Loader: runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
			return &config.EffectiveConfig{Config: &config.Config{}}, nil
		}),
		Compile: &controllableCompiler{}, Manager: mgr, Observer: obs,
		ActiveEffective: &config.EffectiveConfig{Config: &config.Config{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := coord.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if res.Category != sdkreload.ResultSourceIntegrity {
		t.Fatalf("failed read category=%q want source-integrity", res.Category)
	}
	count, logged := handler.snapshot()
	if count != 1 {
		t.Fatalf("cleanup_failed lifecycle observations=%d want exactly once", count)
	}
	if strings.Contains(logged, markerCause.Error()) {
		t.Fatalf("cleanup error text leaked into telemetry: %q", logged)
	}
}
