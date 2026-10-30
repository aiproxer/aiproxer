//go:build integration

package proxymemorystore_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/proxymemorystore"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for proxy
// memory persistence on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)

	t.Run("schema", func(t *testing.T) {
		bunDB := testkit.OpenPostgresBunForTest(t, dsn, 2)
		t.Cleanup(func() { _ = bunDB.Close() })
		if err := proxymemorystore.EnsureSchema(context.Background(), bunDB); err != nil {
			t.Fatal(err)
		}
		assertProxyMemorySchemaAndMigrationHistory(t, bunDB)
	})

	t.Run("additive settings context upgrade", func(t *testing.T) {
		assertPostgresSettingsContextUpgrade(t, dsn)
	})

	runContract(t, func(t *testing.T) *bun.DB {
		t.Helper()
		bunDB := testkit.OpenPostgresBunForTest(t, dsn, 2)
		t.Cleanup(func() { _ = bunDB.Close() })
		if err := proxymemorystore.EnsureSchema(context.Background(), bunDB); err != nil {
			t.Fatal(err)
		}
		return bunDB
	}, func(t *testing.T, _ *bun.DB) *bun.DB {
		t.Helper()
		second := testkit.OpenPostgresBunForTest(t, dsn, 2)
		t.Cleanup(func() { _ = second.Close() })
		return second
	}, func(t *testing.T) *bun.DB {
		t.Helper()
		observer := testkit.OpenPostgresBunForTest(t, dsn, 1)
		t.Cleanup(func() { _ = observer.Close() })
		return observer
	})
}

// assertPostgresSettingsContextUpgrade reproduces a database that already
// applied the settings migration and carries a legacy row, then runs the
// migration set again in an isolated schema on the same server. The legacy
// table must gain the context columns without rewriting the raw overrides, and
// the legacy row must read back as a row without trusted context.
//
// The schema is created and dropped by this test; no database is created or
// dropped, and the shared public schema is never touched.
func assertPostgresSettingsContextUpgrade(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	schema := "proxy_memory_upgrade_" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	bunDB := testkit.OpenPostgresBunForTest(t, dsn, 1)
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = bunDB.ExecContext(cleanupCtx, "SET search_path TO public")
		_, _ = bunDB.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = bunDB.Close()
	})
	_, err := bunDB.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	// The single-connection pool keeps this session setting for every
	// following statement of the test, including the migration runner.
	_, err = bunDB.ExecContext(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)

	_, err = bunDB.ExecContext(ctx, `CREATE TABLE proxy_memory_settings (
		tenant_id TEXT NOT NULL,
		principal_id TEXT NOT NULL,
		phase TEXT NOT NULL,
		backend TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		reasoning_effort TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (tenant_id, principal_id, phase)
	)`)
	require.NoError(t, err)
	_, err = bunDB.ExecContext(ctx, `INSERT INTO proxy_memory_settings
		(tenant_id, principal_id, phase, backend, model, reasoning_effort)
		VALUES ('tenant-legacy', 'alice', 'extraction', 'openai', 'base', 'high')`)
	require.NoError(t, err)

	require.NoError(t, proxymemorystore.EnsureSchema(ctx, bunDB))

	store := proxymemorystore.New(bunDB, proxymemorystore.NewALegReader(bunDB))
	got, err := store.ReadSettings(ctx, proxymemory.Principal{TenantID: "tenant-legacy", PrincipalID: "alice"})
	require.NoError(t, err)
	assert.Equal(t, proxymemory.OwnerOverrides{
		Extraction: proxymemory.TargetOverrides{Backend: "openai", Model: "base", ReasoningEffort: "high"},
	}, got.Overrides())
	assert.False(t, got.Extraction.Dependency.Present,
		"an upgraded legacy row must not gain a fabricated dependency history")

	var columns int
	require.NoError(t, bunDB.NewRaw(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = ? AND table_name = 'proxy_memory_settings'`, schema).Scan(ctx, &columns))
	assert.Equal(t, 11, columns, "the additive migration must add exactly the five context columns")
}
