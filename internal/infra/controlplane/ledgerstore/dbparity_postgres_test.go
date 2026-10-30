//go:build integration

package ledgerstore

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/controlplane/ledgerstore/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/stretchr/testify/require"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for the
// control-plane event ledger persistence on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()

	t.Run("Contract", func(t *testing.T) {
		contract.RunSuite(t, pgFactory{dsn: dsn})
	})

	t.Run("MigrationAndSchemaParity", func(t *testing.T) {
		st := newPostgresStoreForTest(t, dsn)
		require.NoError(t, dbparity.VerifySchema(ctx, st.db, ControlPlaneLedgerLogicalSchemaSpec()))

		_, thisFile, _, ok := runtime.Caller(0)
		require.True(t, ok)
		discovered, err := dbparity.DiscoverMigrations(filepath.Dir(thisFile))
		require.NoError(t, err)
		require.NotEmpty(t, discovered)

		var names []string
		rows, err := st.db.QueryContext(ctx, "SELECT name FROM bun_controlplane_migrations")
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
		require.NoError(t, runControlPlaneSchemaMigrate(ctx, st.db))
		var countAfter int
		require.NoError(t, st.db.NewRaw("SELECT count(*) FROM bun_controlplane_migrations").Scan(ctx, &countAfter))
		require.Equal(t, len(names), countAfter)
	})
}
