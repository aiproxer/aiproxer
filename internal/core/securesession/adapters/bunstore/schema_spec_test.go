package bunstore_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/bunstore"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// SecureSessionLogicalSchemaSpec returns the declared logical schema invariant specification
// for the secure-session persistence component across SQLite and PostgreSQL.
func SecureSessionLogicalSchemaSpec() dbparity.LogicalSchemaSpec {
	return dbparity.LogicalSchemaSpec{
		ComponentID: "secure-sessions",
		Tables: []dbparity.TableSpec{
			{
				Name: "aip_secure_sessions",
				Columns: []dbparity.ColumnSpec{
					{Name: "session_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "resume_fingerprint", Type: dbparity.TypeBlob, Nullable: dbparity.PtrBool(false)},
					{Name: "owner_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "owner_issuer", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "owner_tenant", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "workspace_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "client_session_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "agent_digest", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "policy_version", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "transcript_enabled", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "effective_treatment", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "stricter_policy_resolution", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "route_hint", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "redaction_profile", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "audit_mode", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "resume_eligible", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "status", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "quarantined_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "quarantine_reason_code", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "quarantine_event_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "last_activity_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "last_activity_source", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "created_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "usage_in", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "usage_out", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "attempt_count", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "latest_attempt_trace_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
					{Name: "latest_attempt_outcome_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
					{Name: "latest_attempt_accounting_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"session_id"},
			},
			{
				Name: "aip_secure_turns",
				Columns: []dbparity.ColumnSpec{
					{Name: "session_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "turn_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
				},
				PrimaryKey: []string{"session_id", "turn_id"},
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"session_id"}, RefTable: "aip_secure_sessions"},
				},
			},
			{
				Name: "aip_secure_attempt_traces",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "session_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "turn_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "b_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "attempt_seq", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "requested_model", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "requested_alias", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "resolved_backend", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "resolved_model", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "route_source", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "route_reason", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "settings_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
					{Name: "started_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "ended_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "success", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "surface_state", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "http_status", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "provider_status", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "error_code", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "timeout_class", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "debug_reason", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "outcome_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
					{Name: "open_failed", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"id"},
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"session_id"}, RefTable: "aip_secure_sessions"},
				},
			},
			{
				Name: "aip_secure_transcript",
				Columns: []dbparity.ColumnSpec{
					{Name: "session_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "seq", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "turn_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "event_kind", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "payload_ref", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "created_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"session_id", "seq"},
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"session_id"}, RefTable: "aip_secure_sessions"},
				},
			},
			{
				Name: "aip_secure_usage",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "session_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "turn_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "b_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "input_tokens", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "output_tokens", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "cache_read_tokens", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "cache_write_tokens", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "non_cached_input_tokens", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "reasoning_tokens", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "non_reasoning_output_tokens", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "total_tokens", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "cost_nano_units", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "cost_minor_units", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "currency", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "cost_source", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "raw_usage_json", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "billing_unavailable", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "request_started_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "first_remote_event_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "first_meaningful_token_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "remote_completed_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "proxy_completed_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "ttft_millis", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "remote_duration_millis", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "completion_duration_millis", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "completion_tps_milli", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "created_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"id"},
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"session_id"}, RefTable: "aip_secure_sessions"},
				},
			},
			{
				Name: "aip_secure_audit",
				Columns: []dbparity.ColumnSpec{
					{Name: "session_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "seq", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "turn_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "action", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "result", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "created_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"session_id", "seq"},
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"session_id"}, RefTable: "aip_secure_sessions"},
				},
			},
		},
		Indexes: []dbparity.IndexSpec{
			{Name: "idx_aip_secure_sessions_resume_fp", Table: "aip_secure_sessions", Columns: []string{"resume_fingerprint"}, Unique: true},
			{Name: "idx_aip_secure_sessions_a_leg_unique", Table: "aip_secure_sessions", Columns: []string{"a_leg_id"}, Unique: true, Predicate: "a_leg_id != ''"},
			{Name: "idx_aip_secure_sessions_owner", Table: "aip_secure_sessions", Columns: []string{"owner_id"}},
			{Name: "idx_aip_secure_sessions_workspace", Table: "aip_secure_sessions", Columns: []string{"workspace_id"}},
			{Name: "idx_aip_secure_sessions_owner_workspace", Table: "aip_secure_sessions", Columns: []string{"owner_id", "workspace_id"}},
			{Name: "idx_aip_secure_sessions_last_activity", Table: "aip_secure_sessions", Columns: []string{"last_activity_unix"}},
			{Name: "idx_aip_secure_turns_session", Table: "aip_secure_turns", Columns: []string{"session_id"}},
			{Name: "idx_aip_secure_attempt_traces_session", Table: "aip_secure_attempt_traces", Columns: []string{"session_id"}},
			{Name: "idx_aip_secure_attempt_traces_b_leg", Table: "aip_secure_attempt_traces", Columns: []string{"b_leg_id"}},
			{Name: "idx_aip_secure_attempt_traces_resolved", Table: "aip_secure_attempt_traces", Columns: []string{"resolved_backend", "resolved_model"}},
			{Name: "idx_aip_secure_transcript_session_seq", Table: "aip_secure_transcript", Columns: []string{"session_id", "seq"}},
			{Name: "idx_aip_secure_usage_session", Table: "aip_secure_usage", Columns: []string{"session_id"}},
			{Name: "idx_aip_secure_usage_b_leg", Table: "aip_secure_usage", Columns: []string{"b_leg_id"}},
			{Name: "idx_aip_secure_audit_session_seq", Table: "aip_secure_audit", Columns: []string{"session_id", "seq"}},
		},
	}
}

func runSecureSessionMigrationAndSchemaParity(t *testing.T, bunDB *bun.DB, migrationDir string) {
	t.Helper()
	ctx := context.Background()

	// 1. Verify schema invariants
	require.NoError(t, dbparity.VerifySchema(ctx, bunDB, SecureSessionLogicalSchemaSpec()))

	// 2. Discover migrations and verify applied history
	discovered, err := dbparity.DiscoverMigrations(migrationDir)
	require.NoError(t, err)
	require.NotEmpty(t, discovered)

	var names []string
	rows, err := bunDB.QueryContext(ctx, "SELECT name FROM bun_securesession_migrations")
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

	// 3. Verify migration rerun idempotency
	require.NoError(t, bunstore.RunSchemaMigrate(ctx, bunDB))
	var countAfter int
	require.NoError(t, bunDB.NewRaw("SELECT count(*) FROM bun_securesession_migrations").Scan(ctx, &countAfter))
	require.Equal(t, len(names), countAfter)
}

func TestSecureSessionSchemaSpec_NegativeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	newTestDB := func(t *testing.T) *bun.DB {
		t.Helper()
		id := bunstoreParityMemSeq.Add(1)
		dsn := fmt.Sprintf("file:bunstoreschemaneg%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", id)
		sqlDB, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		require.NoError(t, err)
		t.Cleanup(func() { _ = bunDB.Close() })
		require.NoError(t, bunstore.RunSchemaMigrate(ctx, bunDB))
		return bunDB
	}

	t.Run("predicate removal fails", func(t *testing.T) {
		t.Parallel()
		bunDB := newTestDB(t)

		_, err := bunDB.ExecContext(ctx, "DROP INDEX idx_aip_secure_sessions_a_leg_unique")
		require.NoError(t, err)
		_, err = bunDB.ExecContext(ctx, "CREATE UNIQUE INDEX idx_aip_secure_sessions_a_leg_unique ON aip_secure_sessions(a_leg_id)")
		require.NoError(t, err)

		err = dbparity.VerifySQLiteSchema(ctx, bunDB, SecureSessionLogicalSchemaSpec())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing predicate")
	})

	t.Run("wrong column fails", func(t *testing.T) {
		t.Parallel()
		bunDB := newTestDB(t)

		_, err := bunDB.ExecContext(ctx, "DROP INDEX idx_aip_secure_sessions_a_leg_unique")
		require.NoError(t, err)
		_, err = bunDB.ExecContext(ctx, "CREATE UNIQUE INDEX idx_aip_secure_sessions_a_leg_unique ON aip_secure_sessions(owner_id) WHERE a_leg_id != ''")
		require.NoError(t, err)

		err = dbparity.VerifySQLiteSchema(ctx, bunDB, SecureSessionLogicalSchemaSpec())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "columns mismatch")
	})

	t.Run("non-unique index fails", func(t *testing.T) {
		t.Parallel()
		bunDB := newTestDB(t)

		_, err := bunDB.ExecContext(ctx, "DROP INDEX idx_aip_secure_sessions_a_leg_unique")
		require.NoError(t, err)
		_, err = bunDB.ExecContext(ctx, "CREATE INDEX idx_aip_secure_sessions_a_leg_unique ON aip_secure_sessions(a_leg_id) WHERE a_leg_id != ''")
		require.NoError(t, err)

		err = dbparity.VerifySQLiteSchema(ctx, bunDB, SecureSessionLogicalSchemaSpec())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "uniqueness mismatch")
	})
}
