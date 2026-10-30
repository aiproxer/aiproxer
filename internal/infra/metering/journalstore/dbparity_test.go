package journalstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/stretchr/testify/require"
)

// TestDBParity_SQLite is the canonical parity entry point for metering-journal on SQLite.
func TestDBParity_SQLite(t *testing.T) {
	t.Parallel()
	t.Run("CreateAndClose", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteJournal(t)
		_ = store
	})
	t.Run("AppendIdempotent", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteJournal(t)
		ctx := context.Background()
		f := validFact("parity-fact", "parity-stream", 1)
		if err := store.Append(ctx, f); err != nil {
			t.Fatalf("Append: %v", err)
		}
		if err := store.Append(ctx, f); err != nil {
			t.Fatalf("Append idempotent: %v", err)
		}
	})
	t.Run("MigrationAndSchemaParity", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		sqlDB, err := sql.Open("sqlite", memorySQLiteDSN())
		require.NoError(t, err)
		defer func() { _ = sqlDB.Close() }()
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		require.NoError(t, err)
		require.NoError(t, journalstore.Migrate(ctx, bunDB))
		require.NoError(t, dbparity.VerifySchema(ctx, bunDB, journalstore.MeteringJournalLogicalSchemaSpec()))

		_, thisFile, _, ok := runtime.Caller(0)
		require.True(t, ok)
		discovered, err := dbparity.DiscoverMigrations(filepath.Dir(thisFile))
		require.NoError(t, err)
		require.NotEmpty(t, discovered)

		var names []string
		rows, err := bunDB.QueryContext(ctx, "SELECT name FROM bun_metering_journal_migrations")
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
		require.NoError(t, journalstore.Migrate(ctx, bunDB))
		var countAfter int
		require.NoError(t, bunDB.NewRaw("SELECT count(*) FROM bun_metering_journal_migrations").Scan(ctx, &countAfter))
		require.Equal(t, len(names), countAfter)
	})
}
