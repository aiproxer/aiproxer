//go:build integration

package runtimebundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// pooledPostgresRuntimeFixture retains both process-owned and generation-owned
// owners so tests can close them in canonical order before asserting the
// process-owned postgres pool gauge reaches zero.
type pooledPostgresRuntimeFixture struct {
	process   *runtimebundle.ProcessServices
	candidate *runtimebundle.CandidateHTTPCompile
	closed    bool
}

func TestPostgresPooled_BuildRuntimeSharesRegistryAndClosesCleanly(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	fx := buildPooledPostgresRuntime(t, adminDSN, runtimeDSN)
	t.Cleanup(func() {
		closePooledPostgresRuntime(t, fx)
	})
	b := fx.candidate

	if got := gatheredGauge(t, b.Metrics().Registry, "aip_postgres_pool_open_connections"); got != 1 {
		t.Fatalf("open postgres runtime connections = %v want 1", got)
	}

	closePooledPostgresRuntime(t, fx)

	if got := gatheredGauge(t, b.Metrics().Registry, "aip_postgres_pool_open_connections"); got != 0 {
		t.Fatalf("open postgres runtime connections after close = %v want 0", got)
	}
}

func TestPostgresPooled_BuildRuntimeLeaseAndJournalSmoke(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	fx := buildPooledPostgresRuntime(t, adminDSN, runtimeDSN)
	b := fx.candidate

	t.Cleanup(func() {
		closePooledPostgresRuntime(t, fx)
	})

	if b.Executor() == nil || b.Executor().ConcurrencyProvider == nil {
		t.Fatal("expected concurrency provider on built executor")
	}
	if b.Executor().MeteringRecorder == nil {
		t.Fatal("expected metering recorder on built executor")
	}
	if b.ReadinessReport() == nil {
		t.Fatal("expected readiness report on CandidateRuntime")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	dec, err := b.Executor().ConcurrencyProvider.AdmitLease(ctx, authority.LeaseAdmission{
		RequestID: "rtbundle-pooled-req",
		Scope:     scope.PrincipalScopeView{PrincipalID: scope.Known("alice")},
	})
	if err != nil {
		t.Fatalf("AdmitLease: %v", err)
	}
	if dec.Kind != authority.LeaseAllow || dec.LeaseID == "" {
		t.Fatalf("AdmitLease dec=%+v want allow with lease id", dec)
	}
	if err := b.Executor().ConcurrencyProvider.ReleaseLease(ctx, authority.LeaseRelease{
		LeaseID:   dec.LeaseID,
		RequestID: "rtbundle-pooled-req",
	}); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}

	fact := metering.Fact{
		FactID:      "rtbundle-pooled-fact",
		StreamID:    "rtbundle-pooled-stream",
		Sequence:    1,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveCustomer,
		Boundary:    metering.BoundaryFrontendEgress,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Correlation: metering.Correlation{RequestID: "req-1", ALegID: "a-1"},
		Scope: scope.PrincipalScopeView{
			PrincipalID: scope.Known("prin-1"),
			TenantID:    scope.Known("ten-1"),
		},
		Source:     metering.SourceObserved,
		Authority:  metering.AuthorityAuthoritative,
		Presence:   metering.PresencePresent,
		RecordedAt: time.Unix(10, 0).UTC(),
		Quantities: []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     1,
			Present:   true,
		}},
	}
	if err := b.Executor().MeteringRecorder.Append(ctx, fact); err != nil {
		t.Fatalf("MeteringRecorder.Append: %v", err)
	}

	report, err := b.ReadinessReport().Report(ctx)
	if err != nil {
		t.Fatalf("ReadinessReport: %v", err)
	}
	if len(report.Components) == 0 {
		t.Fatal("expected readiness components")
	}
	var sawConcurrency, sawJournal bool
	for _, c := range report.Components {
		if c.Component == controlplane.ReadinessComponentConcurrencyAuthority {
			sawConcurrency = true
			if c.State == "" {
				t.Fatal("concurrency readiness state empty")
			}
		}
		if c.Component == controlplane.ReadinessComponentMeteringJournal {
			sawJournal = true
			if c.State == "" {
				t.Fatal("metering journal readiness state empty")
			}
		}
	}
	if !sawConcurrency || !sawJournal {
		t.Fatalf("readiness missing components concurrency=%v journal=%v", sawConcurrency, sawJournal)
	}

	if got := gatheredGauge(t, b.Metrics().Registry, "aip_postgres_pool_open_connections"); got != 1 {
		t.Fatalf("open postgres runtime connections = %v want 1", got)
	}
	closePooledPostgresRuntime(t, fx)
	if got := gatheredGauge(t, b.Metrics().Registry, "aip_postgres_pool_open_connections"); got != 0 {
		t.Fatalf("open postgres runtime connections after close = %v want 0", got)
	}
}

func buildPooledPostgresRuntime(t *testing.T, adminDSN, runtimeDSN string) *pooledPostgresRuntimeFixture {
	t.Helper()
	concurrencyStoreID := testkit.UniquePostgresStoreID("rtbundle-concurrency")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSN, concurrencyStoreID, testkit.PostgresComponentLease)
		testkit.CleanupPostgresStoreByID(t, adminDSN, "metering-postgres", testkit.PostgresComponentJournal)
	})

	admin := testkit.OpenPostgresBunForTest(t, adminDSN, 2)
	t.Cleanup(func() { _ = admin.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if err := leasestore.Migrate(ctx, admin); err != nil {
		t.Fatalf("migrate concurrency schema: %v", err)
	}
	if err := journalstore.Migrate(ctx, admin); err != nil {
		t.Fatalf("migrate metering schema: %v", err)
	}

	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{InMemory: true},
		Database: config.DatabaseConfig{
			ConnectionMode: config.DatabaseConnectionModeTransactionPool,
			SchemaMode:     config.DatabaseSchemaModeVerifyOnly,
			MaxOpenConns:   1,
			MaxIdleConns:   1,
		},
		Observability: config.ObservabilityConfig{
			Metrics: config.MetricsConfig{Enabled: true},
		},
		Accounting: config.AccountingConfig{
			Concurrency: config.ConcurrencyAuthorityConfig{
				Enabled:     true,
				Store:       "postgres",
				StoreID:     concurrencyStoreID,
				PostgresDSN: runtimeDSN,
				Rules: []config.ConcurrencyAuthorityRuleConfig{{
					ID:                "rtbundle-concurrency",
					MaxActiveRequests: 1,
				}},
			},
		},
		Metering: config.MeteringConfig{
			Enabled: true,
			Journal: config.MeteringJournalConfig{
				Store:       "postgres",
				PostgresDSN: runtimeDSN,
			},
		},
	}

	ps, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Startup:        runtimebundle.StartupOptions{StartupContext: t.Context()},
	})
	if b.Metrics() == nil || b.Metrics().Registry == nil || b.Metrics().PostgresPool == nil {
		t.Fatal("expected postgres pool metrics bundle")
	}
	return &pooledPostgresRuntimeFixture{process: ps, candidate: b}
}

// closePooledPostgresRuntime closes the unpublished candidate first, then
// ProcessServices exactly once. Both closes are idempotently guarded so test
// Cleanup and in-body close share the same transaction.
func closePooledPostgresRuntime(t *testing.T, fx *pooledPostgresRuntimeFixture) {
	t.Helper()
	if fx == nil || fx.closed {
		return
	}
	if fx.candidate != nil {
		if err := fx.candidate.Close(); err != nil {
			t.Fatalf("close candidate: %v", err)
		}
	}
	if fx.process != nil {
		if err := fx.process.Close(); err != nil {
			t.Fatalf("close process: %v", err)
		}
	}
	fx.closed = true
}

func gatheredGauge(t *testing.T, reg *prometheus.Registry, metricName string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather %s: %v", metricName, err)
	}
	for _, family := range families {
		if family.GetName() != metricName {
			continue
		}
		metrics := family.GetMetric()
		if len(metrics) != 1 {
			t.Fatalf("%s metric count = %d want 1", metricName, len(metrics))
		}
		return gaugeValue(metrics[0])
	}
	t.Fatalf("metric %s not found", metricName)
	return 0
}

func gaugeValue(metric *dto.Metric) float64 {
	if metric == nil || metric.GetGauge() == nil {
		return 0
	}
	return metric.GetGauge().GetValue()
}
