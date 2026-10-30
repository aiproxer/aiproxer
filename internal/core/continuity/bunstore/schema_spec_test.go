package bunstore

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ContinuityLogicalSchemaSpec returns the declared logical schema invariant specification
// for the continuity persistence component across SQLite and PostgreSQL.
func ContinuityLogicalSchemaSpec() dbparity.LogicalSchemaSpec {
	return dbparity.LogicalSchemaSpec{
		ComponentID: "continuity",
		Tables: []dbparity.TableSpec{
			{
				Name: "a_leg_steering_bootstrap",
				Columns: []dbparity.ColumnSpec{
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "producer_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "outcome", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "matched_count", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "model_evidence", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey:  []string{"a_leg_id", "producer_id"},
				ForeignKeys: []dbparity.ForeignKeySpec{{Columns: []string{"a_leg_id"}, RefTable: "a_legs"}},
			},
			{
				Name: "a_legs",
				Columns: []dbparity.ColumnSpec{
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: new(false), PrimaryKey: true},
					{Name: "continuity_key", Type: dbparity.TypeText, Nullable: new(false)},
					{Name: "created_at_unix", Type: dbparity.TypeInteger, Nullable: new(false)},
					{Name: "last_seen_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "weighted_first_consumed", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "next_b_seq", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "interleaved_state_json", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"a_leg_id"},
			},
			{
				Name: "b_legs",
				Columns: []dbparity.ColumnSpec{
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "seq", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "b_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"a_leg_id", "seq"},
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"a_leg_id"}, RefTable: "a_legs"},
				},
			},
			{
				Name: "attempts",
				Columns: []dbparity.ColumnSpec{
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "seq", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "b_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "backend_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "effective_model", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "started_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "finished_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "outcome", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "reason", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"a_leg_id", "seq"},
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"a_leg_id"}, RefTable: "a_legs"},
				},
			},
			{
				Name: "a_leg_route_overrides",
				Columns: []dbparity.ColumnSpec{
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "active", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "selector", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "revision", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "updated_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"a_leg_id"},
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"a_leg_id"}, RefTable: "a_legs"},
				},
			},
			{
				Name: "a_leg_conversation_view_state",
				Columns: []dbparity.ColumnSpec{
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "state_revision", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "next_slot_ordinal", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"a_leg_id"},
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"a_leg_id"}, RefTable: "a_legs"},
				},
			},
			{
				Name: "a_leg_never_backend_messages",
				Columns: []dbparity.ColumnSpec{
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "identity_version", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "identity_digest", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "reason", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "created_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"a_leg_id", "identity_version", "identity_digest"},
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"a_leg_id"}, RefTable: "a_legs"},
				},
			},
			{
				Name: "a_leg_steering_overlays",
				Columns: []dbparity.ColumnSpec{
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "overlay_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "overlay_revision", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "slot_ordinal", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "active", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "message_version", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "message_role", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "message_text", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "placement_kind", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "anchor_identity_version", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "anchor_identity_digest", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "anchor_occurrence", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "anchor_missing_policy", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "reason", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "created_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "updated_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"a_leg_id", "overlay_id"},
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"a_leg_id"}, RefTable: "a_legs"},
				},
				UniqueConstraints: []dbparity.UniqueConstraintSpec{
					{Columns: []string{"a_leg_id", "slot_ordinal"}},
				},
			},
		},
		Indexes: []dbparity.IndexSpec{
			{
				Name:      "idx_a_legs_continuity",
				Table:     "a_legs",
				Columns:   []string{"continuity_key"},
				Unique:    true,
				Predicate: "continuity_key != ''",
			},
		},
	}
}

func TestContinuitySchemaSpec_NegativeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("predicate removal fails", func(t *testing.T) {
		t.Parallel()
		st, cleanup := newTestStore(t)
		defer cleanup()

		_, err := st.db.ExecContext(ctx, "DROP INDEX idx_a_legs_continuity")
		require.NoError(t, err)
		_, err = st.db.ExecContext(ctx, "CREATE UNIQUE INDEX idx_a_legs_continuity ON a_legs(continuity_key)")
		require.NoError(t, err)

		err = dbparity.VerifySQLiteSchema(ctx, st.db, ContinuityLogicalSchemaSpec())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing predicate")
	})

	t.Run("wrong column fails", func(t *testing.T) {
		t.Parallel()
		st, cleanup := newTestStore(t)
		defer cleanup()

		_, err := st.db.ExecContext(ctx, "DROP INDEX idx_a_legs_continuity")
		require.NoError(t, err)
		_, err = st.db.ExecContext(ctx, "CREATE UNIQUE INDEX idx_a_legs_continuity ON a_legs(last_seen_at_unix) WHERE continuity_key != ''")
		require.NoError(t, err)

		err = dbparity.VerifySQLiteSchema(ctx, st.db, ContinuityLogicalSchemaSpec())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "columns mismatch")
	})

	t.Run("non-unique index fails", func(t *testing.T) {
		t.Parallel()
		st, cleanup := newTestStore(t)
		defer cleanup()

		_, err := st.db.ExecContext(ctx, "DROP INDEX idx_a_legs_continuity")
		require.NoError(t, err)
		_, err = st.db.ExecContext(ctx, "CREATE INDEX idx_a_legs_continuity ON a_legs(continuity_key) WHERE continuity_key != ''")
		require.NoError(t, err)

		err = dbparity.VerifySQLiteSchema(ctx, st.db, ContinuityLogicalSchemaSpec())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "uniqueness mismatch")
	})
}
