package journalstore

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// MeteringJournalLogicalSchemaSpec returns the declared logical schema invariant specification
// for the metering journal persistence component across SQLite and PostgreSQL.
func MeteringJournalLogicalSchemaSpec() dbparity.LogicalSchemaSpec {
	return dbparity.LogicalSchemaSpec{
		ComponentID: "metering-journal",
		Tables: []dbparity.TableSpec{
			{
				Name: "metering_facts",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "fact_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "stream_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "sequence", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "source_event_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "fact_kind", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "perspective", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "boundary", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "lifecycle_scope", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "request_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "b_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "attempt_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "frontend_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "backend_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "model", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "presence", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "source", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "authority", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "recorded_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "payload_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
					{Name: "identity_version", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "source_revision", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "source_event_kind", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "source_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "payload_kind", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "'fact'"},
					{Name: "observation_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "observation_revision", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "observation_fingerprint", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "observation_subject_kind", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "observation_subject_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "observation_tenant_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "observation_origin", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "observation_acquisition", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "observation_provider_account_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "observation_pool_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "observation_window_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "observation_reset_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "observation_observed_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "observation_received_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
				},
				PrimaryKey: []string{"id"},
				UniqueConstraints: []dbparity.UniqueConstraintSpec{
					{Columns: []string{"store_id", "source_event_key"}},
				},
			},
			{
				Name: "metering_fact_filters",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "fact_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "stream_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "field_name", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "field_value", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"id"},
			},
			{
				Name: "metering_fact_supersessions",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "stream_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "from_fact_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "to_fact_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"id"},
				UniqueConstraints: []dbparity.UniqueConstraintSpec{
					{Columns: []string{"store_id", "stream_id", "from_fact_id", "to_fact_id"}},
				},
			},
			{
				Name: "metering_components",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "observation_row_id", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "observation_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "observation_revision", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "observation_fingerprint", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "item_kind", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "item_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "component_key", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "component_key_hash", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "coefficient", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "scale", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "value_present", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), Default: "0", DefaultPostgres: "false"},
					{Name: "money_present", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), Default: "0", DefaultPostgres: "false"},
					{Name: "currency", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "charge_coverage_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false), Default: "'[]'"},
					{Name: "subject_kind", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "subject_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "tenant_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "provider_account_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "stream_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "sequence", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "origin", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "acquisition", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "authority", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "projection_version", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "1"},
				},
				PrimaryKey:        []string{"id"},
				ForeignKeys:       []dbparity.ForeignKeySpec{{Columns: []string{"store_id", "observation_row_id"}, RefTable: "metering_facts", RefColumns: []string{"store_id", "id"}}},
				UniqueConstraints: []dbparity.UniqueConstraintSpec{{Columns: []string{"observation_row_id", "item_kind", "item_id"}}},
				CheckConstraints:  []dbparity.CheckConstraintSpec{{Expression: "measure"}},
			},
			{
				Name: "metering_observation_economic_outbox",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "observation_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "observation_revision", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "observation_fingerprint", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "payload_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
					{Name: "status", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "'pending'"},
					{Name: "attempt_count", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "next_attempt_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "lease_owner", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "lease_until_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "last_error", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "created_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "updated_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey:        []string{"id"},
				UniqueConstraints: []dbparity.UniqueConstraintSpec{{Columns: []string{"store_id", "observation_id", "observation_revision"}}},
				CheckConstraints:  []dbparity.CheckConstraintSpec{{Expression: "pending"}},
			},
		},
		Indexes: []dbparity.IndexSpec{
			{
				Name:    "idx_metering_facts_stream_seq",
				Table:   "metering_facts",
				Columns: []string{"stream_id", "sequence"},
				Unique:  false,
			},
			{
				Name:      "idx_metering_facts_request",
				Table:     "metering_facts",
				Columns:   []string{"request_id"},
				Unique:    false,
				Predicate: "request_id != ''",
			},
			{
				Name:    "metering_facts_store_stream_fact_id_key",
				Table:   "metering_facts",
				Columns: []string{"store_id", "stream_id", "fact_id"},
				Unique:  true,
			},
			{
				Name:    "idx_metering_facts_store_stream_seq",
				Table:   "metering_facts",
				Columns: []string{"store_id", "stream_id", "sequence"},
				Unique:  false,
			},
			{
				Name:      "idx_metering_facts_store_attempt",
				Table:     "metering_facts",
				Columns:   []string{"store_id", "attempt_id"},
				Unique:    false,
				Predicate: "attempt_id != ''",
			},
			{
				Name:    "idx_metering_facts_store_recorded",
				Table:   "metering_facts",
				Columns: []string{"store_id", "recorded_at_unix"},
				Unique:  false,
			},
			{
				Name:    "idx_metering_facts_store_plane",
				Table:   "metering_facts",
				Columns: []string{"store_id", "perspective", "boundary", "lifecycle_scope"},
				Unique:  false,
			},
			{
				Name:      "idx_metering_facts_store_account_window",
				Table:     "metering_facts",
				Columns:   []string{"store_id", "observation_provider_account_key", "observation_pool_id", "observation_window_id", "observation_reset_at_unix", "observation_observed_at_unix", "observation_received_at_unix", "stream_id", "sequence", "observation_id", "observation_revision", "id"},
				Predicate: "payload_kind = 'observation' AND observation_subject_kind = 'account_window'",
			},
			{
				Name:    "idx_metering_fact_filters_field",
				Table:   "metering_fact_filters",
				Columns: []string{"store_id", "field_name", "field_value", "stream_id"},
				Unique:  false,
			},
			{
				Name:    "idx_metering_fact_supersessions_to",
				Table:   "metering_fact_supersessions",
				Columns: []string{"store_id", "stream_id", "to_fact_id"},
				Unique:  false,
			},
			{
				Name:      "metering_facts_store_observation_revision_key",
				Table:     "metering_facts",
				Columns:   []string{"store_id", "observation_id", "observation_revision"},
				Unique:    true,
				Predicate: "payload_kind = 'observation' AND observation_id != ''",
			},
			{
				Name:    "metering_facts_store_id_key",
				Table:   "metering_facts",
				Columns: []string{"store_id", "id"},
				Unique:  true,
			},
			{
				Name:      "idx_metering_facts_store_bleg",
				Table:     "metering_facts",
				Columns:   []string{"store_id", "b_leg_id", "stream_id", "sequence", "observation_id", "observation_revision", "id"},
				Unique:    false,
				Predicate: "payload_kind = 'observation'",
			},
			{
				Name:    meteringFactsStoreBLegStatementIndex,
				Table:   "metering_facts",
				Columns: []string{"store_id", "b_leg_id", "stream_id", "sequence", "observation_id", "observation_revision", "id"},
				Unique:  false,
				Predicate: "payload_kind = 'observation' AND observation_subject_kind = 'statement_line'" +
					" AND observation_origin = 'statement' AND observation_acquisition = 'statement_importer'" +
					" AND authority = 'verified_statement'",
			},
			{
				Name:    "idx_metering_components_store_subject",
				Table:   "metering_components",
				Columns: []string{"store_id", "subject_kind", "subject_id", "stream_id", "sequence", "observation_id", "observation_revision", "item_kind", "item_id"},
			},
			{
				Name:    "idx_metering_components_store_component",
				Table:   "metering_components",
				Columns: []string{"store_id", "component_key_hash", "component_key", "subject_kind", "subject_id", "observation_id", "observation_revision", "item_id"},
			},
			{
				Name:    "idx_metering_components_store_provider_account",
				Table:   "metering_components",
				Columns: []string{"store_id", "provider_account_key", "stream_id", "sequence", "observation_id", "observation_revision", "item_kind", "item_id"},
			},
			{
				Name:    "idx_metering_components_observation",
				Table:   "metering_components",
				Columns: []string{"observation_row_id", "item_kind", "item_id"},
			},
			{
				Name:    "idx_metering_observation_outbox_pending",
				Table:   "metering_observation_economic_outbox",
				Columns: []string{"store_id", "status", "next_attempt_at_unix", "lease_until_unix", "created_at_unix", "id"},
			},
		},
	}
}

func TestMeteringJournalSchemaSpec_NegativeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "negative.db")
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", filepath.ToSlash(path))
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	require.NoError(t, Migrate(ctx, bunDB))

	t.Run("missing column fails", func(t *testing.T) {
		spec := MeteringJournalLogicalSchemaSpec()
		spec.Tables[0].Columns = append(spec.Tables[0].Columns, dbparity.ColumnSpec{
			Name: "missing_field",
			Type: dbparity.TypeText,
		})
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing column")
	})

	t.Run("wrong index column fails", func(t *testing.T) {
		spec := MeteringJournalLogicalSchemaSpec()
		spec.Indexes[0].Columns = []string{"stream_id"}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "columns mismatch")
	})
}
