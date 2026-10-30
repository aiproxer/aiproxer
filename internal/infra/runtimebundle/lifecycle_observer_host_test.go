package runtimebundle_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type syncLogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncLogBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncLogBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestAutomaticRetirement_ObserverLifecycleMetricsTracingLogging proves Host
// binding wires Manager.SetLifecycleObserver so automatic post-publish
// retirement emits quiesce+cleanup observations exactly once (Manager-owned).
func TestAutomaticRetirement_ObserverLifecycleMetricsTracingLogging(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      testConfigPath(t),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	hostServeCleanup(t, host)
	if !runtimebundle.HostManager(host).HasLifecycleObserver() {
		t.Fatal("bindHost must wire Manager.SetLifecycleObserver")
	}

	var logBuf syncLogBuffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	reg := prometheus.NewRegistry()
	reloadProm := metrics.RegisterReloadProm(reg)

	// Keep Manager as sole retirement scheduler; swap sinks for exact assertions.
	observer := runtimehost.NewReloadObserver(runtimehost.ReloadObserverDeps{
		Logger:  logger,
		Tracer:  tp.Tracer("test.lifecycle"),
		Metrics: reloadProm,
	})
	runtimebundle.HostManager(host).SetLifecycleObserver(observer)
	runtimebundle.HostManager(host).SetCleanupPolicy(runtimehost.CleanupPolicy{MaxAttempts: 3})

	g1 := runtimebundle.HostManager(host).Active()
	if g1 == nil {
		t.Fatal("expected generation 1")
	}
	g2 := runtimebundle.HostManager(host).Prepare("g2-lifecycle-obs")
	if err := runtimebundle.HostManager(host).Publish(g2); err != nil {
		t.Fatalf("publish g2: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if g1.Lifecycle() == runtimehost.GenClosed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("g1 lifecycle=%v want GenClosed after automatic retirement", g1.Lifecycle())
	}

	var logs string
	for time.Now().Before(deadline) {
		logs = logBuf.String()
		if strings.Count(logs, `stage=quiesce`) >= 1 && strings.Count(logs, `stage=cleanup`) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	quiesceLogs := strings.Count(logs, `stage=quiesce`)
	cleanupLogs := strings.Count(logs, `stage=cleanup`)
	if quiesceLogs != 1 {
		t.Fatalf("quiesce lifecycle logs=%d want 1; log=%s", quiesceLogs, logs)
	}
	if cleanupLogs != 1 {
		t.Fatalf("cleanup lifecycle logs=%d want 1; log=%s", cleanupLogs, logs)
	}
	if testutil.CollectAndCount(reg, "aip_reload_stage_duration_seconds") < 2 {
		t.Fatal("expected quiesce+cleanup stage metrics")
	}

	var quiesceSpans, cleanupSpans int
	for _, sp := range exporter.GetSpans() {
		switch sp.Name {
		case "quiesce":
			quiesceSpans++
		case "cleanup":
			cleanupSpans++
		}
	}
	if quiesceSpans != 1 || cleanupSpans != 1 {
		t.Fatalf("spans quiesce=%d cleanup=%d want 1/1 (no Coordinator duplicate)", quiesceSpans, cleanupSpans)
	}
}

func TestLifecycle_CleanupFailureRetryBoundedOutcome(t *testing.T) {
	t.Parallel()
	var logBuf syncLogBuffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	observer := runtimehost.NewReloadObserver(runtimehost.ReloadObserverDeps{Logger: logger})

	var closes atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		n := closes.Add(1)
		if n < 3 {
			return errors.New("temp-close")
		}
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(2, nil)
	m.SetLifecycleObserver(observer)
	m.SetCleanupPolicy(runtimehost.CleanupPolicy{MaxAttempts: 3})
	g1 := m.PrepareRequestPlane("g1-cleanup-retry", bundle)
	mustPublishBundle(t, m, g1)
	mustPublishBundle(t, m, m.Prepare("g2"))

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if g1.Lifecycle() == runtimehost.GenClosed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v", g1.Lifecycle())
	}
	if closes.Load() != 3 {
		t.Fatalf("closes=%d want 3 bounded retries", closes.Load())
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "stage=cleanup") {
		t.Fatalf("expected cleanup lifecycle observation; log=%s", logs)
	}
}
