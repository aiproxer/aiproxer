package bunmigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"
)

// uniqueMemoryDSN returns an in-memory SQLite DSN private to one test.
// openStubSQLite shares one "file::memory:?cache=shared" database across the
// process, which would make parallel migration tests observe each other's
// migration history tables.
func uniqueMemoryDSN(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
}

func openMigrationTestDB(t *testing.T) *bun.DB {
	t.Helper()
	sqldb, err := sql.Open("sqlite", uniqueMemoryDSN(t))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	bunDB, err := db.NewBunDB(sqldb, db.DialectSQLite)
	if err != nil {
		t.Fatalf("new bun db: %v", err)
	}
	return bunDB
}

func migrationHistoryCount(t *testing.T, bunDB *bun.DB, table string) int {
	t.Helper()
	var count int
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
	if err := bunDB.NewRaw(query).Scan(context.Background(), &count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

// TestMigrate_RetriesFailedMigration proves a failed up function is not
// recorded as applied, so the next Migrate run executes it again instead of
// silently skipping a half-built schema.
func TestMigrate_RetriesFailedMigration(t *testing.T) {
	t.Parallel()
	bunDB := openMigrationTestDB(t)
	ctx := context.Background()
	const table = "bun_runner_retry_migrations"

	var succeed atomic.Bool
	firstMigrations := migrate.NewMigrations()
	registerFailingMigration(firstMigrations, succeed.Load)

	err := Migrate(ctx, bunDB, table, firstMigrations)
	if err == nil {
		t.Fatal("first Migrate must return the up failure")
	}
	if !errors.Is(err, errFailingMigrationUp) {
		t.Fatalf("error must wrap the up failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), table) {
		t.Fatalf("error must name the migration table %q, got: %v", table, err)
	}
	if got := migrationHistoryCount(t, bunDB, table); got != 0 {
		t.Fatalf("failed migration must not be recorded as applied, history rows=%d", got)
	}

	// Retry with the same migration now succeeding: it must run and only then
	// be recorded, proving the earlier failure was not persisted.
	succeed.Store(true)
	retryMigrations := migrate.NewMigrations()
	registerFailingMigration(retryMigrations, succeed.Load)
	if err := Migrate(ctx, bunDB, table, retryMigrations); err != nil {
		t.Fatalf("retry must succeed, got %v", err)
	}
	if got := migrationHistoryCount(t, bunDB, table); got != 1 {
		t.Fatalf("retried migration must be recorded once, history rows=%d", got)
	}
}

// TestMigrate_SecondRunIsNoOp proves an applied migration is recorded and not
// executed again on a later Migrate run.
func TestMigrate_SecondRunIsNoOp(t *testing.T) {
	t.Parallel()
	bunDB := openMigrationTestDB(t)
	ctx := context.Background()
	const table = "bun_runner_noop_migrations"

	var executions atomic.Int32
	migrations := migrate.NewMigrations()
	registerCountingMigration(migrations, func() { executions.Add(1) })

	if err := Migrate(ctx, bunDB, table, migrations); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("first run must execute the up function once, got %d", got)
	}
	if err := Migrate(ctx, bunDB, table, migrations); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("second run must not re-execute an applied migration, got %d executions", got)
	}
	if got := migrationHistoryCount(t, bunDB, table); got != 1 {
		t.Fatalf("history rows=%d want 1", got)
	}
}

// TestMigrate_UsesRequestedTableName proves the runner honors its table
// argument instead of Bun's default migration table.
func TestMigrate_UsesRequestedTableName(t *testing.T) {
	t.Parallel()
	bunDB := openMigrationTestDB(t)
	ctx := context.Background()
	const table = "bun_runner_custom_table_migrations"

	migrations := migrate.NewMigrations()
	registerCountingMigration(migrations, func() {})
	if err := Migrate(ctx, bunDB, table, migrations); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var count int
	if err := bunDB.NewRaw("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(ctx, &count); err != nil {
		t.Fatalf("lookup table: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration history table %q not created", table)
	}
	var defaultCount int
	if err := bunDB.NewRaw("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='bun_migrations'").Scan(ctx, &defaultCount); err != nil {
		t.Fatalf("lookup default table: %v", err)
	}
	if defaultCount != 0 {
		t.Fatal("runner must not use Bun's default migration table")
	}
}

// TestMigrate_InvalidArguments proves the runner fails closed on nil inputs.
func TestMigrate_InvalidArguments(t *testing.T) {
	t.Parallel()
	bunDB := openMigrationTestDB(t)
	migrations := migrate.NewMigrations()
	registerCountingMigration(migrations, func() {})

	cases := []struct {
		name       string
		ctx        context.Context //nolint:containedctx // explicit nil-context contract check
		db         *bun.DB
		table      string
		migrations *migrate.Migrations
	}{
		{name: "nil context", ctx: nil, db: bunDB, table: "t", migrations: migrations},
		{name: "nil db", ctx: context.Background(), db: nil, table: "t", migrations: migrations},
		{name: "empty table", ctx: context.Background(), db: bunDB, table: "  ", migrations: migrations},
		{name: "nil migrations", ctx: context.Background(), db: bunDB, table: "t", migrations: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := Migrate(tc.ctx, tc.db, tc.table, tc.migrations); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
