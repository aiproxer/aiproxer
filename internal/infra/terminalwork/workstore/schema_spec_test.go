package workstore

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
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

// TerminalWorkLogicalSchemaSpec returns the declared logical schema invariant specification
// for the terminal work persistence component across SQLite and PostgreSQL.
func TerminalWorkLogicalSchemaSpec() dbparity.LogicalSchemaSpec {
	return dbparity.LogicalSchemaSpec{
		ComponentID: "terminal-work",
		Tables: []dbparity.TableSpec{
			{
				Name: "economic_terminal_work",
				Columns: []dbparity.ColumnSpec{
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "work_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "source_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "identity_version", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "payload_version", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "kind", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "state", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "provider_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "request_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "attempt_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "trace_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "generation_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "runtime_instance_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "runtime_generation_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "bound_provider_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "rating_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "fact_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "lease_set_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "payload_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
					{Name: "attempts", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "next_retry_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "claim_owner_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "claim_expires_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "error_code", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "error_permanent", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), Default: "0", DefaultPostgres: "false"},
					{Name: "error_message", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "created_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "updated_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"store_id", "work_id"},
				UniqueConstraints: []dbparity.UniqueConstraintSpec{
					{Columns: []string{"store_id", "source_key"}},
				},
			},
		},
		Indexes: []dbparity.IndexSpec{
			{
				Name:    "idx_terminal_work_due",
				Table:   "economic_terminal_work",
				Columns: []string{"store_id", "state", "next_retry_at_unix", "claim_expires_at_unix"},
				Unique:  false,
			},
			{
				Name:    "idx_terminal_work_provider",
				Table:   "economic_terminal_work",
				Columns: []string{"store_id", "provider_id", "state"},
				Unique:  false,
			},
			{
				Name:      "idx_terminal_work_request",
				Table:     "economic_terminal_work",
				Columns:   []string{"store_id", "request_id"},
				Unique:    false,
				Predicate: "request_id != ''",
			},
		},
	}
}

func TestTerminalWorkSchemaSpec_NegativeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	newTestDB := func(t *testing.T) *bun.DB {
		t.Helper()
		path := filepath.Join(t.TempDir(), "negative.db")
		dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", filepath.ToSlash(path))
		sqlDB, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		require.NoError(t, err)
		require.NoError(t, Migrate(ctx, bunDB))
		return bunDB
	}

	t.Run("missing column fails", func(t *testing.T) {
		t.Parallel()
		bunDB := newTestDB(t)
		spec := TerminalWorkLogicalSchemaSpec()
		spec.Tables[0].Columns = append(spec.Tables[0].Columns, dbparity.ColumnSpec{
			Name: "missing_field",
			Type: dbparity.TypeText,
		})
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing column")
	})

	t.Run("wrong index column fails", func(t *testing.T) {
		t.Parallel()
		bunDB := newTestDB(t)
		spec := TerminalWorkLogicalSchemaSpec()
		spec.Indexes[0].Columns = []string{"store_id", "state"}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "columns mismatch")
	})
}
