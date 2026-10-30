package runtimebundle_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

// TestBuildFailedBuildClosesPostgresPoolRegistry proves Build's deferred
// postgresPools.Close runs when a later store fails after a registry-owned pool
// was opened and claimed.
func TestBuildFailedBuildClosesPostgresPoolRegistry(t *testing.T) {
	t.Parallel()

	var opened *bun.DB
	cfg := baseAuthorityConfig(false, "fail_closed")
	cfg.Database = config.DatabaseConfig{
		ConnectionMode: config.DatabaseConnectionModeTransactionPool,
		SchemaMode:     config.DatabaseSchemaModeVerifyOnly,
	}
	cfg.Accounting.Concurrency = config.ConcurrencyAuthorityConfig{
		Enabled:     true,
		Store:       "postgres",
		StoreID:     "build-abort-lease",
		PostgresDSN: "postgres://runtime/db",
		Rules: []config.ConcurrencyAuthorityRuleConfig{{
			ID:                "max-active",
			MaxActiveRequests: 1,
			Match: config.AccountingAuthorityDimensionsConfig{
				Principal: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("alice")},
			},
		}},
	}
	cfg.Metering = config.MeteringConfig{
		Enabled: true,
		Journal: config.MeteringJournalConfig{
			Store:       "postgres",
			PostgresDSN: "postgres://runtime/db",
		},
	}

	opts := baseAuthorityOptions(t)
	opts.Testing.PostgresPoolOpener = func(ctx context.Context, _ string, _ db.PoolSettings) (*bun.DB, error) {
		sqlDB, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			return nil, err
		}
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			return nil, err
		}
		// Lease schema only: concurrency OpenStore succeeds; metering VerifySchema fails.
		if err := leasestore.Migrate(ctx, bunDB); err != nil {
			_ = bunDB.Close()
			return nil, err
		}
		opened = bunDB
		return bunDB, nil
	}

	_, _, err := processAndCandidateErr(t, cfg, opts)
	if err == nil {
		t.Fatal("expected Build to fail when metering schema is missing")
	}
	if !strings.Contains(err.Error(), "schema verification") && !strings.Contains(err.Error(), "metering") {
		t.Fatalf("error=%v want metering schema verification failure", err)
	}
	if opened == nil {
		t.Fatal("registry opener was not called")
	}
	if pingErr := opened.PingContext(t.Context()); pingErr == nil {
		t.Fatal("failed Build must close registry-owned postgres pools")
	}
}
