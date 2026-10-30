package ledgerstore

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ControlPlaneLedgerLogicalSchemaSpec returns the declared logical schema invariant specification
// for the control-plane event ledger persistence component across SQLite and PostgreSQL.
func ControlPlaneLedgerLogicalSchemaSpec() dbparity.LogicalSchemaSpec {
	return dbparity.LogicalSchemaSpec{
		ComponentID: "control-plane-ledger",
		Tables: []dbparity.TableSpec{
			{
				Name: "control_plane_events",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "source_event_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "category", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "occurred_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "recorded_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "trace_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "request_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "session_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "b_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "attempt_seq", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "frontend_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "backend_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "model", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "parent_trace_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "outcome", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "effect", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "reason_code", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "visibility", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "surfaced", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "usage_plane", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "usage_availability", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "evidence_state", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "redaction_state", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "source_name", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "source_version", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "summary", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "summary_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false), Default: "'{}'"},
					{Name: "scope_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false), Default: "'{}'"},
					{Name: "detail_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false), Default: "'{}'"},
					{Name: "principal_known", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), Default: "0", DefaultPostgres: "false"},
					{Name: "principal_value", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "credential_known", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), Default: "0", DefaultPostgres: "false"},
					{Name: "credential_value", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "tenant_known", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), Default: "0", DefaultPostgres: "false"},
					{Name: "tenant_value", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "organization_known", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), Default: "0", DefaultPostgres: "false"},
					{Name: "organization_value", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "workspace_known", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), Default: "0", DefaultPostgres: "false"},
					{Name: "workspace_value", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "project_known", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), Default: "0", DefaultPostgres: "false"},
					{Name: "project_value", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "department_known", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), Default: "0", DefaultPostgres: "false"},
					{Name: "department_value", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "cost_center_known", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), Default: "0", DefaultPostgres: "false"},
					{Name: "cost_center_value", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
				},
				PrimaryKey: []string{"id"},
			},
		},
		Indexes: []dbparity.IndexSpec{
			{
				Name:      "idx_control_plane_events_source_key",
				Table:     "control_plane_events",
				Columns:   []string{"source_event_key"},
				Unique:    true,
				Predicate: "source_event_key != ''",
			},
			{
				Name:    "idx_control_plane_events_order",
				Table:   "control_plane_events",
				Columns: []string{"id"},
				Unique:  false,
			},
			{
				Name:    "idx_control_plane_events_category_time",
				Table:   "control_plane_events",
				Columns: []string{"category", "occurred_at_unix"},
				Unique:  false,
			},
			{
				Name:      "idx_control_plane_events_trace",
				Table:     "control_plane_events",
				Columns:   []string{"trace_id"},
				Unique:    false,
				Predicate: "trace_id != ''",
			},
			{
				Name:      "idx_control_plane_events_session",
				Table:     "control_plane_events",
				Columns:   []string{"session_id"},
				Unique:    false,
				Predicate: "session_id != ''",
			},
			{
				Name:      "idx_control_plane_events_a_leg",
				Table:     "control_plane_events",
				Columns:   []string{"a_leg_id"},
				Unique:    false,
				Predicate: "a_leg_id != ''",
			},
			{
				Name:      "idx_control_plane_events_b_leg",
				Table:     "control_plane_events",
				Columns:   []string{"b_leg_id"},
				Unique:    false,
				Predicate: "b_leg_id != ''",
			},
			{
				Name:    "idx_control_plane_events_backend_model",
				Table:   "control_plane_events",
				Columns: []string{"backend_id", "model"},
				Unique:  false,
			},
			{
				Name:      "idx_control_plane_events_outcome",
				Table:     "control_plane_events",
				Columns:   []string{"outcome"},
				Unique:    false,
				Predicate: "outcome != ''",
			},
			{
				Name:      "idx_control_plane_events_reason",
				Table:     "control_plane_events",
				Columns:   []string{"reason_code"},
				Unique:    false,
				Predicate: "reason_code != ''",
			},
			{
				Name:      "idx_control_plane_events_surfaced",
				Table:     "control_plane_events",
				Columns:   []string{"surfaced"},
				Unique:    false,
				Predicate: "surfaced != ''",
			},
			{
				Name:      "idx_control_plane_events_usage_plane",
				Table:     "control_plane_events",
				Columns:   []string{"usage_plane"},
				Unique:    false,
				Predicate: "usage_plane != ''",
			},
			{
				Name:    "idx_control_plane_events_principal",
				Table:   "control_plane_events",
				Columns: []string{"principal_known", "principal_value"},
				Unique:  false,
			},
			{
				Name:    "idx_control_plane_events_tenant",
				Table:   "control_plane_events",
				Columns: []string{"tenant_known", "tenant_value"},
				Unique:  false,
			},
			{
				Name:    "idx_control_plane_events_workspace",
				Table:   "control_plane_events",
				Columns: []string{"workspace_known", "workspace_value"},
				Unique:  false,
			},
			{
				Name:    "idx_control_plane_events_project",
				Table:   "control_plane_events",
				Columns: []string{"project_known", "project_value"},
				Unique:  false,
			},
		},
	}
}

func TestControlPlaneLedgerSchemaSpec_NegativeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("predicate removal fails", func(t *testing.T) {
		t.Parallel()
		st := newSQLiteStoreForTest(t, nil)

		_, err := st.db.ExecContext(ctx, "DROP INDEX idx_control_plane_events_source_key")
		require.NoError(t, err)
		_, err = st.db.ExecContext(ctx, "CREATE UNIQUE INDEX idx_control_plane_events_source_key ON control_plane_events(source_event_key)")
		require.NoError(t, err)

		err = dbparity.VerifySQLiteSchema(ctx, st.db, ControlPlaneLedgerLogicalSchemaSpec())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing predicate")
	})

	t.Run("wrong column fails", func(t *testing.T) {
		t.Parallel()
		st := newSQLiteStoreForTest(t, nil)

		_, err := st.db.ExecContext(ctx, "DROP INDEX idx_control_plane_events_source_key")
		require.NoError(t, err)
		_, err = st.db.ExecContext(ctx, "CREATE UNIQUE INDEX idx_control_plane_events_source_key ON control_plane_events(occurred_at_unix) WHERE source_event_key != ''")
		require.NoError(t, err)

		err = dbparity.VerifySQLiteSchema(ctx, st.db, ControlPlaneLedgerLogicalSchemaSpec())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "columns mismatch")
	})

	t.Run("non-unique index fails", func(t *testing.T) {
		t.Parallel()
		st := newSQLiteStoreForTest(t, nil)

		_, err := st.db.ExecContext(ctx, "DROP INDEX idx_control_plane_events_source_key")
		require.NoError(t, err)
		_, err = st.db.ExecContext(ctx, "CREATE INDEX idx_control_plane_events_source_key ON control_plane_events(source_event_key) WHERE source_event_key != ''")
		require.NoError(t, err)

		err = dbparity.VerifySQLiteSchema(ctx, st.db, ControlPlaneLedgerLogicalSchemaSpec())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "uniqueness mismatch")
	})
}
