package runtimebundle_test

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite" // register sqlite driver for fake pool handles
)

// TestBuild_sharedPostgresPoolAcrossStores proves continuity, secure-session,
// and control-plane postgres stores sharing one DSN open a single registry pool
// (one opener call), keep it claimed past PruneUnclaimed, and close it exactly
// once via ProcessServices.Close.
func TestBuild_sharedPostgresPoolAcrossStores(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	const sharedDSN = "postgres://runtime/shared"
	var openCalls atomic.Int32
	var opened *bun.DB
	opener := func(ctx context.Context, _ string, _ db.PoolSettings) (*bun.DB, error) {
		openCalls.Add(1)
		sqlDB, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			return nil, err
		}
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			return nil, err
		}
		opened = bunDB
		return bunDB, nil
	}

	cfg := &config.Config{
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{
			Store:       "postgres",
			PostgresDSN: sharedDSN,
		},
		SecureSession: config.SecureSessionConfig{
			Store:               "postgres",
			PostgresDSN:         sharedDSN,
			TokenFingerprintKey: testSecureKey32,
		},
		ControlPlane: config.ControlPlaneConfig{
			Enabled:     true,
			Store:       "postgres",
			PostgresDSN: sharedDSN,
		},
	}

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{
			PluginRegistry: reg,
			Testing:        runtimebundle.TestingOptions{PostgresPoolOpener: opener},
		},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	if got := openCalls.Load(); got != 1 {
		t.Fatalf("opener called %d times, want 1 shared pool for identical DSN", got)
	}
	if opened == nil {
		t.Fatal("registry opener was not called")
	}
	// Pool survived PruneUnclaimed: it must still serve while the process lives.
	if err := opened.PingContext(context.Background()); err != nil {
		t.Fatalf("shared pool pruned or closed during build: %v", err)
	}

	if err := ps.Close(); err != nil {
		t.Fatalf("ProcessServices.Close: %v", err)
	}
	if err := opened.PingContext(context.Background()); err == nil {
		t.Fatal("shared pool must be closed once by ProcessServices.Close")
	}
}
