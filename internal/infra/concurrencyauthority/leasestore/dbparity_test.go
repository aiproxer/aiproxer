package leasestore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// TestDBParity_SQLite is the canonical parity entry point for concurrency-authority on SQLite.
func TestDBParity_SQLite(t *testing.T) {
	t.Parallel()
	runLeaseParitySuite(t, func(t *testing.T) *leaseStoreFactory {
		t.Helper()
		path := filepath.Join(t.TempDir(), "parity.db")
		store, bunDB := newSQLiteParityStore(t, path, "parity-sqlite")
		return &leaseStoreFactory{
			store: store,
			db:    bunDB,
		}
	})
}

func newSQLiteParityStore(t *testing.T, path, storeID string) (*leasestore.DurableStore, *bun.DB) {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := leasestore.NewDurable(ctx, bunDB, leasestore.DurableConfig{StoreID: storeID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, bunDB
}

type leaseStoreFactory struct {
	store interface {
		CheckReadiness(ctx context.Context) (domain.Readiness, error)
	}
	db *bun.DB
}

func runLeaseParitySuite(t *testing.T, newStore func(t *testing.T) *leaseStoreFactory) {
	t.Helper()
	t.Run("CheckReadiness", func(t *testing.T) {
		t.Parallel()
		f := newStore(t)
		ready, err := f.store.CheckReadiness(context.Background())
		if err != nil {
			t.Fatalf("CheckReadiness: %v", err)
		}
		if ready.State != domain.ReadinessStateReady && ready.State != domain.ReadinessStateDegraded {
			t.Fatalf("unexpected readiness state %v", ready.State)
		}
	})

	t.Run("MigrationAndSchemaParity", func(t *testing.T) {
		t.Parallel()
		f := newStore(t)
		ctx := context.Background()
		require.NoError(t, dbparity.VerifySchema(ctx, f.db, leasestore.ConcurrencyLeaseLogicalSchemaSpec()))

		_, thisFile, _, ok := runtime.Caller(0)
		require.True(t, ok)
		discovered, err := dbparity.DiscoverMigrations(filepath.Dir(thisFile))
		require.NoError(t, err)
		require.NotEmpty(t, discovered)

		var names []string
		rows, err := f.db.QueryContext(ctx, "SELECT name FROM bun_concurrency_lease_migrations")
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		recorded := make(map[string]bool)
		for rows.Next() {
			var name string
			require.NoError(t, rows.Scan(&name))
			names = append(names, name)
			id := name
			if len(name) >= 14 {
				id = name[:14]
			}
			recorded[id] = true
		}
		require.NoError(t, dbparity.AssertMigrationHistoryIDs(dbparity.MigrationIDs(discovered), recorded))

		// Verify migration rerun idempotency
		require.NoError(t, leasestore.Migrate(ctx, f.db))
		var countAfter int
		require.NoError(t, f.db.NewRaw("SELECT count(*) FROM bun_concurrency_lease_migrations").Scan(ctx, &countAfter))
		require.Equal(t, len(names), countAfter)
	})
}
