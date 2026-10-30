//go:build integration

package workstore_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/stretchr/testify/require"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for terminal-work on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()

	t.Run("CreateAndClose", func(t *testing.T) {
		t.Parallel()
		store := newPostgresWorkStore(t, dsn, "parity-pg-"+testkit.UniquePostgresStoreID("work"))
		_ = store
	})

	t.Run("MigrationAndSchemaParity", func(t *testing.T) {
		bunDB := testkit.OpenPostgresBunForTest(t, dsn, 4)
		defer func() { _ = bunDB.Close() }()
		require.NoError(t, workstore.Migrate(ctx, bunDB))
		require.NoError(t, dbparity.VerifySchema(ctx, bunDB, workstore.TerminalWorkLogicalSchemaSpec()))

		_, thisFile, _, ok := runtime.Caller(0)
		require.True(t, ok)
		discovered, err := dbparity.DiscoverMigrations(filepath.Dir(thisFile))
		require.NoError(t, err)
		require.NotEmpty(t, discovered)

		var names []string
		rows, err := bunDB.QueryContext(ctx, "SELECT name FROM bun_terminal_work_migrations")
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
		require.NoError(t, workstore.Migrate(ctx, bunDB))
		var countAfter int
		require.NoError(t, bunDB.NewRaw("SELECT count(*) FROM bun_terminal_work_migrations").Scan(ctx, &countAfter))
		require.Equal(t, len(names), countAfter)
	})
}
