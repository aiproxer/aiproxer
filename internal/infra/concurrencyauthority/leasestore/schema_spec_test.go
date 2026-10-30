package leasestore

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

// ConcurrencyLeaseLogicalSchemaSpec returns the declared logical schema invariant specification
// for the concurrency authority persistence component across SQLite and PostgreSQL.
func ConcurrencyLeaseLogicalSchemaSpec() dbparity.LogicalSchemaSpec {
	return dbparity.LogicalSchemaSpec{
		ComponentID: "concurrency-authority",
		Tables: []dbparity.TableSpec{
			{
				Name: "concurrency_leases",
				Columns: []dbparity.ColumnSpec{
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "lease_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "rule_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "rule_version", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "namespace", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "dimension_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "logical_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "holder_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "acquired_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "renewed_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "expires_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "released_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "generation", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "state", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "dimensions_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
					{Name: "identity_version", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "1"},
					{Name: "set_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "set_generation", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "set_state", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
				},
				PrimaryKey: []string{"store_id", "lease_id"},
			},
			{
				Name: "concurrency_lease_capacity",
				Columns: []dbparity.ColumnSpec{
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "rule_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "dimension_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
				},
				PrimaryKey: []string{"store_id", "rule_id", "dimension_key"},
			},
		},
		Indexes: []dbparity.IndexSpec{
			{
				Name:    "idx_concurrency_leases_capacity",
				Table:   "concurrency_leases",
				Columns: []string{"store_id", "rule_id", "dimension_key", "state", "expires_at_unix"},
				Unique:  false,
			},
			{
				Name:    "idx_concurrency_leases_set",
				Table:   "concurrency_leases",
				Columns: []string{"store_id", "set_id"},
				Unique:  false,
			},
		},
	}
}

func TestConcurrencyLeaseSchemaSpec_NegativeCases(t *testing.T) {
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
		spec := ConcurrencyLeaseLogicalSchemaSpec()
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
		spec := ConcurrencyLeaseLogicalSchemaSpec()
		spec.Indexes[0].Columns = []string{"store_id", "rule_id"}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "columns mismatch")
	})
}
