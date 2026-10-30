package proxymemorystore_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// TestDBParity_SQLite is the canonical parity entry point for proxy memory
// persistence on SQLite.
func TestDBParity_SQLite(t *testing.T) {
	t.Run("store", TestStoreContract_SQLite)
	t.Run("schema", func(t *testing.T) {
		bunDB := openSQLite(t)
		assertProxyMemorySchemaAndMigrationHistory(t, bunDB)
	})
}

// assertProxyMemorySchemaAndMigrationHistory verifies the migrated schema
// against the component's logical spec and that every discovered migration
// file is recorded in the applied history — the component-level dbparity
// contract pattern.
func assertProxyMemorySchemaAndMigrationHistory(t *testing.T, db *bun.DB) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, dbparity.VerifySchema(ctx, db, proxyMemoryLogicalSchemaSpec()))

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	discovered, err := dbparity.DiscoverMigrations(filepath.Dir(thisFile))
	require.NoError(t, err)
	require.NotEmpty(t, discovered)

	var names []string
	require.NoError(t, db.NewRaw(`SELECT name FROM bun_proxy_memory_migrations`).Scan(ctx, &names))
	recorded := make(map[string]bool, len(names))
	for _, name := range names {
		id := name
		if len(id) >= 14 {
			id = id[:14]
		}
		recorded[id] = true
	}
	require.NoError(t, dbparity.AssertMigrationHistoryIDs(dbparity.MigrationIDs(discovered), recorded))
}
