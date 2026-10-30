package proxymemorystore_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/proxymemorystore"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

var sqliteStoreSeq atomic.Int64

// sqliteDSNs records the DSN of every in-memory database this package opened,
// so the settings contract can open a second handle to the same database.
var sqliteDSNs sync.Map

// openSQLite mirrors the SQLite opener of
// internal/infra/conversationview/bun_store_test.go (same pragmas and
// db.NewBunDB construction), backed by a per-test in-memory database.
func openSQLite(t *testing.T) *bun.DB {
	t.Helper()
	dsn := fmt.Sprintf(
		"file:proxymemory-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)",
		sqliteStoreSeq.Add(1),
	)
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bunDB.Close() })
	sqliteDSNs.Store(bunDB, dsn)

	require.NoError(t, proxymemorystore.EnsureSchema(context.Background(), bunDB))
	return bunDB
}

// openSecondSQLite returns a second handle to the same in-memory database as
// first. The shared cache makes both handles read the same data, so the second
// handle stands for a second instance of the same store.
func openSecondSQLite(t *testing.T, first *bun.DB) *bun.DB {
	t.Helper()
	value, ok := sqliteDSNs.Load(first)
	if !ok {
		t.Fatal("the database was not opened by openSQLite")
	}
	dsn, ok := value.(string)
	if !ok {
		t.Fatal("the recorded DSN is not a string")
	}
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bunDB.Close() })
	return bunDB
}

// TestStoreContract_SQLite runs the behavioral §12.2/§12.6 contract of the
// proxy memory store against an in-memory SQLite database.
func TestStoreContract_SQLite(t *testing.T) {
	runContract(t, openSQLite, openSecondSQLite, nil)
}

// TestEnsureSchema_Idempotent proves EnsureSchema can run twice (and, through
// openSQLite, a third time) on the same database without error, and that each
// migration is recorded exactly once under its version prefix.
func TestEnsureSchema_Idempotent(t *testing.T) {
	bunDB := openSQLite(t)
	ctx := context.Background()
	require.NoError(t, proxymemorystore.EnsureSchema(ctx, bunDB))
	require.NoError(t, proxymemorystore.EnsureSchema(ctx, bunDB))

	for _, migrationName := range []string{
		proxymemorystore.BaselineMigrationName,
		proxymemorystore.SettingsMigrationName,
		proxymemorystore.SettingsContextMigrationName,
		proxymemorystore.PhaseFailuresMigrationName,
	} {
		var applied int
		require.NoError(t, bunDB.NewRaw(
			`SELECT count(*) FROM bun_proxy_memory_migrations WHERE name = ?`,
			migrationName,
		).Scan(ctx, &applied))
		assert.Equal(t, 1, applied, "migration %s recorded exactly once", migrationName)
	}
}

// TestEnsureSchema_CreatesDesignTables pins the schema of design §12.1
// column by column.
func TestEnsureSchema_CreatesDesignTables(t *testing.T) {
	bunDB := openSQLite(t)

	var n int
	require.NoError(t, bunDB.NewRaw(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		"bun_proxy_memory_migrations",
	).Scan(context.Background(), &n))
	assert.Equal(t, 1, n, "migration history table missing")

	want := map[string][]string{
		"proxy_memory_session_bindings": {
			"a_leg_id", "tenant_id", "principal_id", "project_key", "project_label",
			"first_turn_outcome", "first_turn_injection", "first_turn_summary_version", "first_turn_epoch",
		},
		"proxy_memory_sessions": {
			"a_leg_id", "tenant_id", "principal_id", "project_key", "project_label", "transcript",
			"revision", "last_activity_ms", "source_seq", "lease_owner", "lease_until_ms", "attempts", "next_attempt_ms",
		},
		"proxy_memory_extractions": {
			"a_leg_id", "tenant_id", "principal_id", "project_key", "session_ref", "raw_memory",
			"rollout_summary", "rollout_slug", "source_updated_ms", "source_seq", "generated_at_ms",
			"consolidated_at_ms",
			"extraction_revision",
		},
		"proxy_memory_notes": {
			"note_id", "tenant_id", "principal_id", "project_key", "note", "created_at_ms",
		},
		"proxy_memory_banks": {
			"tenant_id", "principal_id", "project_key", "project_label", "version", "memory_md",
			"memory_summary", "published_at_ms", "consolidated_names", "dirty_seq", "consolidated_seq",
			"lease_owner", "lease_until_ms", "attempts", "next_attempt_ms",
		},
		"proxy_memory_fence": {
			"a_leg_id", "tenant_id", "principal_id", "forgotten_at_ms",
		},
		"proxy_memory_extraction_ledger": {
			"a_leg_id", "tenant_id", "principal_id", "project_key", "recorded_at_ms", "source_activity_ms", "last_activity_ms", "source_seq",
		},
		"proxy_memory_erasure_epochs": {
			"tenant_id", "principal_id", "project_key", "epoch",
		},
		"proxy_memory_settings": {
			"tenant_id", "principal_id", "phase", "backend", "model", "reasoning_effort",
			"dependency_present", "dependency_backend", "dependency_model", "inherited_backend", "inherited_model",
		},
	}
	for table, wantColumns := range want {
		assert.Equal(t, wantColumns, tableColumns(t, bunDB, table), "table %s columns", table)
	}
}

func tableColumns(t *testing.T, bunDB *bun.DB, table string) []string {
	t.Helper()
	var names []string
	err := bunDB.NewRaw(
		`SELECT name FROM pragma_table_info(?) ORDER BY cid`,
		table,
	).Scan(context.Background(), &names)
	require.NoError(t, err)
	return names
}

// openRawSQLite opens an empty in-memory database without applying the proxy
// memory schema, so a test can reproduce the pre-context table and prove the
// additive upgrade.
func openRawSQLite(t *testing.T) *bun.DB {
	t.Helper()
	dsn := fmt.Sprintf(
		"file:proxymemory-raw-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)",
		sqliteStoreSeq.Add(1),
	)
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bunDB.Close() })
	return bunDB
}

// TestSettingsMigration_AdditiveUpgradePreservesTheLegacyRow proves the
// context columns are added to an existing settings table without rewriting
// the owner's raw overrides: the upgraded row carries the raw values and no
// trusted context, so it keeps the legacy compatibility behavior.
func TestSettingsMigration_AdditiveUpgradePreservesTheLegacyRow(t *testing.T) {
	bunDB := openRawSQLite(t)
	ctx := context.Background()
	_, err := bunDB.NewRaw(`CREATE TABLE proxy_memory_settings (
		tenant_id TEXT NOT NULL,
		principal_id TEXT NOT NULL,
		phase TEXT NOT NULL,
		backend TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		reasoning_effort TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (tenant_id, principal_id, phase)
	)`).Exec(ctx)
	require.NoError(t, err)
	_, err = bunDB.NewRaw(`INSERT INTO proxy_memory_settings
		(tenant_id, principal_id, phase, backend, model, reasoning_effort)
		VALUES ('tenant-legacy', 'alice', 'extraction', 'openai', 'base', 'high')`).Exec(ctx)
	require.NoError(t, err)

	require.NoError(t, proxymemorystore.EnsureSchema(ctx, bunDB))

	store := proxymemorystore.New(bunDB, proxymemorystore.NewALegReader(bunDB))
	got, err := store.ReadSettings(ctx, proxymemory.Principal{TenantID: "tenant-legacy", PrincipalID: "alice"})
	require.NoError(t, err)
	assert.Equal(t, proxymemory.OwnerOverrides{
		Extraction: proxymemory.TargetOverrides{Backend: "openai", Model: "base", ReasoningEffort: "high"},
	}, got.Overrides())
	assert.False(t, got.Extraction.Dependency.Present)

	assert.Equal(t, []string{
		"tenant_id", "principal_id", "phase", "backend", "model", "reasoning_effort",
		"dependency_present", "dependency_backend", "dependency_model", "inherited_backend", "inherited_model",
	}, tableColumns(t, bunDB, "proxy_memory_settings"))

	var present, inheritedBackend, inheritedModel int
	var contextBackend, contextModel string
	require.NoError(t, bunDB.NewRaw(`SELECT dependency_present, dependency_backend, dependency_model, inherited_backend, inherited_model
		FROM proxy_memory_settings WHERE tenant_id = 'tenant-legacy' AND principal_id = 'alice' AND phase = 'extraction'`).Scan(ctx,
		&present, &contextBackend, &contextModel, &inheritedBackend, &inheritedModel))
	assert.Equal(t, 0, present)
	assert.Empty(t, contextBackend)
	assert.Empty(t, contextModel)
	assert.Equal(t, 0, inheritedBackend)
	assert.Equal(t, 0, inheritedModel)
}
