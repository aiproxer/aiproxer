package dbparity_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

func newSQLiteTestDB(t *testing.T) *bun.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "schema_test.db")
	dsn, err := db.SQLiteFileDSN(path)
	if err != nil {
		t.Fatalf("SQLiteFileDSN: %v", err)
	}
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	bunDB, err := db.NewBunDB(sqldb, db.DialectSQLite)
	if err != nil {
		t.Fatalf("NewBunDB: %v", err)
	}
	return bunDB
}

func TestVerifySchema_SQLite_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	stmts := []string{
		`CREATE TABLE users (
	id TEXT PRIMARY KEY,
	username TEXT NOT NULL,
	email TEXT,
	is_active BOOLEAN NOT NULL DEFAULT 1,
	age INTEGER,
	metadata JSON,
	avatar BLOB,
	created_at TIMESTAMP NOT NULL
)`,
		`CREATE TABLE orders (
	order_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id),
	amount_nano INTEGER NOT NULL CHECK(amount_nano > 0),
	status TEXT NOT NULL
)`,
		`CREATE UNIQUE INDEX idx_users_username ON users(username)`,
		`CREATE INDEX idx_orders_pending ON orders(status, amount_nano) WHERE status = 'pending'`,
		`CREATE TRIGGER orders_immutable_delete BEFORE DELETE ON orders
BEGIN
	SELECT RAISE(ABORT, 'orders are immutable');
END`,
	}
	for _, stmt := range stmts {
		if _, err := bunDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec DDL failed: %v\nSQL:\n%s", err, stmt)
		}
	}

	spec := dbparity.LogicalSchemaSpec{
		ComponentID: "test-commerce",
		Tables: []dbparity.TableSpec{
			{
				Name: "users",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "username", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "email", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(true)},
					{Name: "is_active", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), DefaultValue: "1"},
					{Name: "age", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(true)},
					{Name: "metadata", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(true)},
					{Name: "avatar", Type: dbparity.TypeBlob, Nullable: dbparity.PtrBool(true)},
					{Name: "created_at", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"id"},
				UniqueConstraints: []dbparity.UniqueConstraintSpec{
					{Columns: []string{"username"}},
				},
			},
			{
				Name: "orders",
				Columns: []dbparity.ColumnSpec{
					{Name: "order_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "user_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "amount_nano", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "status", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"order_id"},
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"user_id"}, RefTable: "users"},
				},
				CheckConstraints: []dbparity.CheckConstraintSpec{
					{Expression: "amount_nano > 0"},
				},
			},
		},
		Indexes: []dbparity.IndexSpec{
			{
				Name:    "idx_users_username",
				Table:   "users",
				Columns: []string{"username"},
				Unique:  true,
			},
			{
				Name:      "idx_orders_pending",
				Table:     "orders",
				Columns:   []string{"status", "amount_nano"},
				Unique:    false,
				Predicate: "status = 'pending'",
			},
		},
		Protections: []dbparity.ImmutabilityProtection{
			{
				Name:        "orders-immutability",
				Table:       "orders",
				TriggerName: "orders_immutable_delete",
			},
			{
				Name:         "users-app-level-immutability",
				Table:        "users",
				AppLevelOnly: true,
				Description:  "Protected by application layer assertions",
			},
		},
		Retired: dbparity.RetiredArtifacts{
			Tables:   []string{"legacy_orders", "temp_users"},
			Columns:  []dbparity.RetiredColumn{{Table: "users", Column: "legacy_phone"}, {Table: "users", Column: "old_%_col"}},
			Indexes:  []string{"idx_users_legacy_id"},
			Triggers: []string{"trg_old_update"},
		},
	}

	if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
		t.Fatalf("VerifySQLiteSchema unexpected error: %v", err)
	}

	if err := dbparity.VerifySchema(ctx, bunDB, spec); err != nil {
		t.Fatalf("VerifySchema unexpected error: %v", err)
	}
}

func TestVerifySchema_SQLite_MissingTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	spec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{Name: "missing_table"},
		},
	}

	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for missing table")
	}
	if !strings.Contains(err.Error(), "table \"missing_table\" not found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestVerifySchema_SQLite_MissingColumn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE accounts (id TEXT PRIMARY KEY);"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	spec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{
				Name: "accounts",
				Columns: []dbparity.ColumnSpec{
					{Name: "id"},
					{Name: "balance_nano"},
				},
			},
		},
	}

	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for missing column")
	}
	if !strings.Contains(err.Error(), "missing column \"balance_nano\"") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestVerifySchema_SQLite_ColumnTypeMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE accounts (id TEXT PRIMARY KEY, count TEXT NOT NULL);"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	spec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{
				Name: "accounts",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeText},
					{Name: "count", Type: dbparity.TypeInteger},
				},
			},
		},
	}

	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for column type mismatch")
	}
	if !strings.Contains(err.Error(), "type mismatch") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestVerifySchema_SQLite_NullabilityMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE accounts (id TEXT PRIMARY KEY, optional_field TEXT, required_field TEXT NOT NULL);"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	t.Run("expected not null but got nullable", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "accounts",
					Columns: []dbparity.ColumnSpec{
						{Name: "optional_field", Nullable: dbparity.PtrBool(false)},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "must be NOT NULL") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("expected nullable but got not null", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "accounts",
					Columns: []dbparity.ColumnSpec{
						{Name: "required_field", Nullable: dbparity.PtrBool(true)},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "must be nullable") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestVerifySchema_SQLite_PrimaryKeyMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE compound_pk (store_id TEXT, row_key TEXT, PRIMARY KEY (store_id, row_key));"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	t.Run("matching compound PK", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name:       "compound_pk",
					PrimaryKey: []string{"store_id", "row_key"},
				},
			},
		}
		if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("mismatched PK columns", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name:       "compound_pk",
					PrimaryKey: []string{"store_id"},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error for PK mismatch")
		}
		if !strings.Contains(err.Error(), "primary key mismatch") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestVerifySchema_SQLite_MissingIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE logs (id TEXT PRIMARY KEY, level TEXT);"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	spec := dbparity.LogicalSchemaSpec{
		Indexes: []dbparity.IndexSpec{
			{
				Name:    "idx_logs_level",
				Table:   "logs",
				Columns: []string{"level"},
			},
		},
	}

	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for missing index")
	}
	if !strings.Contains(err.Error(), "missing SQLite index \"idx_logs_level\"") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerifySchema_SQLite_MissingTrigger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE ledger (id TEXT PRIMARY KEY);"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	spec := dbparity.LogicalSchemaSpec{
		Protections: []dbparity.ImmutabilityProtection{
			{
				Name:        "ledger_immutable",
				Table:       "ledger",
				TriggerName: "ledger_no_delete",
			},
		},
	}

	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for missing trigger")
	}
	if !strings.Contains(err.Error(), "missing SQLite immutability trigger \"ledger_no_delete\"") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerifySchema_SQLite_MissingCheckConstraint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE items (id TEXT PRIMARY KEY, count INTEGER CHECK(count > 0));"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	spec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{
				Name: "items",
				CheckConstraints: []dbparity.CheckConstraintSpec{
					{Expression: "count > 100"},
				},
			},
		},
	}

	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for missing check constraint")
	}
	if !strings.Contains(err.Error(), "missing check constraint") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerifySchema_SQLite_MissingForeignKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE parent (id TEXT PRIMARY KEY); CREATE TABLE child (id TEXT PRIMARY KEY, parent_id TEXT);"); err != nil {
		t.Fatalf("create tables: %v", err)
	}

	spec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{
				Name: "child",
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"parent_id"}, RefTable: "parent"},
				},
			},
		},
	}

	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for missing foreign key")
	}
	if !strings.Contains(err.Error(), "missing foreign key") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerifySchema_SQLite_RetiredArtifactPresent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("retired table present", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE retired_table (id TEXT PRIMARY KEY);"); err != nil {
			t.Fatalf("create table: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Retired: dbparity.RetiredArtifacts{
				Tables: []string{"retired_table"},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when retired table is present")
		}
		if !strings.Contains(err.Error(), "retired table \"retired_table\" is still present") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("retired column present exact", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE accounts (id TEXT PRIMARY KEY, old_balance INTEGER);"); err != nil {
			t.Fatalf("create table: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Retired: dbparity.RetiredArtifacts{
				Columns: []dbparity.RetiredColumn{
					{Table: "accounts", Column: "old_balance"},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when retired column is present")
		}
		if !strings.Contains(err.Error(), "table \"accounts\" contains retired column \"old_balance\"") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("retired column present pattern wildcard", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE accounts (id TEXT PRIMARY KEY, reserved_nano INTEGER);"); err != nil {
			t.Fatalf("create table: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Retired: dbparity.RetiredArtifacts{
				Columns: []dbparity.RetiredColumn{
					{Table: "accounts", Column: "reserved%nano"},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when retired column pattern matches")
		}
		if !strings.Contains(err.Error(), "table \"accounts\" contains retired column \"reserved_nano\"") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("retired index present", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE accounts (id TEXT PRIMARY KEY, name TEXT); CREATE INDEX idx_retired ON accounts(name);"); err != nil {
			t.Fatalf("create table and index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Retired: dbparity.RetiredArtifacts{
				Indexes: []string{"idx_retired"},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when retired index is present")
		}
		if !strings.Contains(err.Error(), "retired index \"idx_retired\" is still present") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("retired trigger present", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE accounts (id TEXT PRIMARY KEY); CREATE TRIGGER trg_retired BEFORE DELETE ON accounts BEGIN SELECT 1; END;"); err != nil {
			t.Fatalf("create table and trigger: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Retired: dbparity.RetiredArtifacts{
				Triggers: []string{"trg_retired"},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when retired trigger is present")
		}
		if !strings.Contains(err.Error(), "retired trigger \"trg_retired\" is still present") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestVerifySchema_NilValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var nilCtx context.Context
	bunDB := newSQLiteTestDB(t)
	spec := dbparity.LogicalSchemaSpec{}

	if err := dbparity.VerifySchema(nilCtx, bunDB, spec); err == nil {
		t.Error("VerifySchema(nil ctx) should error")
	}
	if err := dbparity.VerifySchema(ctx, nil, spec); err == nil {
		t.Error("VerifySchema(nil db) should error")
	}
	if err := dbparity.VerifySQLiteSchema(nilCtx, bunDB, spec); err == nil {
		t.Error("VerifySQLiteSchema(nil ctx) should error")
	}
	if err := dbparity.VerifySQLiteSchema(ctx, nil, spec); err == nil {
		t.Error("VerifySQLiteSchema(nil db) should error")
	}
	if err := dbparity.VerifyPostgresSchema(nilCtx, bunDB, spec); err == nil {
		t.Error("VerifyPostgresSchema(nil ctx) should error")
	}
	if err := dbparity.VerifyPostgresSchema(ctx, nil, spec); err == nil {
		t.Error("VerifyPostgresSchema(nil db) should error")
	}
}

func TestVerifySchema_SQLite_NamedIndex_Validation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("uniqueness mismatch: expected unique got non-unique", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT); CREATE INDEX idx_users_email ON users(email);"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_email", Table: "users", Columns: []string{"email"}, Unique: true},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "uniqueness mismatch") {
			t.Fatalf("expected uniqueness mismatch error, got: %v", err)
		}
	})

	t.Run("uniqueness mismatch: expected non-unique got unique", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT); CREATE UNIQUE INDEX idx_users_email ON users(email);"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_email", Table: "users", Columns: []string{"email"}, Unique: false},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "uniqueness mismatch") {
			t.Fatalf("expected uniqueness mismatch error, got: %v", err)
		}
	})

	t.Run("columns mismatch: wrong columns", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT, name TEXT); CREATE INDEX idx_users_email ON users(email);"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_email", Table: "users", Columns: []string{"name"}},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "columns mismatch") {
			t.Fatalf("expected columns mismatch error, got: %v", err)
		}
	})

	t.Run("columns mismatch: wrong order", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, a TEXT, b TEXT); CREATE INDEX idx_users_ab ON users(a, b);"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_ab", Table: "users", Columns: []string{"b", "a"}},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "columns mismatch") {
			t.Fatalf("expected columns mismatch error, got: %v", err)
		}
	})

	t.Run("owning table mismatch", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT); CREATE TABLE accounts (id TEXT PRIMARY KEY); CREATE INDEX idx_users_email ON users(email);"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_email", Table: "accounts", Columns: []string{"email"}},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "owning table mismatch") {
			t.Fatalf("expected owning table mismatch error, got: %v", err)
		}
	})

	t.Run("missing predicate on partial spec", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT); CREATE INDEX idx_users_email ON users(email);"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_email", Table: "users", Columns: []string{"email"}, Predicate: "email != ''"},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "missing predicate") {
			t.Fatalf("expected missing predicate error, got: %v", err)
		}
	})

	t.Run("unexpected predicate on non-partial spec", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT); CREATE INDEX idx_users_email ON users(email) WHERE email != '';"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_email", Table: "users", Columns: []string{"email"}},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "unexpected partial predicate") {
			t.Fatalf("expected unexpected partial predicate error, got: %v", err)
		}
	})

	t.Run("predicate expression mismatch", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT, status TEXT); CREATE INDEX idx_users_email ON users(email) WHERE status = 'active';"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_email", Table: "users", Columns: []string{"email"}, Predicate: "status = 'pending'"},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "predicate mismatch") {
			t.Fatalf("expected predicate mismatch error, got: %v", err)
		}
	})

	t.Run("predicate containment rejected: stronger actual predicate fails", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE sessions (id TEXT PRIMARY KEY, a_leg_id TEXT, extra_flag INTEGER); CREATE INDEX idx_sessions_a_leg ON sessions(a_leg_id) WHERE a_leg_id != '' AND extra_flag = 1;"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_sessions_a_leg", Table: "sessions", Columns: []string{"a_leg_id"}, Predicate: "a_leg_id != ''"},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected predicate mismatch error for stronger actual predicate, got nil")
		}
		if !strings.Contains(err.Error(), "predicate mismatch") {
			t.Fatalf("expected error containing 'predicate mismatch', got: %v", err)
		}
		if !strings.Contains(err.Error(), "a_leg_id != '' AND extra_flag = 1") || !strings.Contains(err.Error(), "a_leg_id != ''") {
			t.Fatalf("expected error naming both actual and expected fragments, got: %v", err)
		}
	})
}

func TestVerifySchema_SQLite_DefaultValue_Validation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("exact default passes", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, `
CREATE TABLE items (
	id TEXT PRIMARY KEY,
	status TEXT NOT NULL DEFAULT 'active',
	retry_count INTEGER NOT NULL DEFAULT 0,
	flag INTEGER NOT NULL DEFAULT 1,
	empty_str TEXT NOT NULL DEFAULT '',
	json_meta TEXT NOT NULL DEFAULT '{}'
);`); err != nil {
			t.Fatalf("create table: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "items",
					Columns: []dbparity.ColumnSpec{
						{Name: "id", Type: dbparity.TypeText, PrimaryKey: true},
						{Name: "status", Type: dbparity.TypeText, Default: "'active'"},
						{Name: "retry_count", Type: dbparity.TypeInteger, Default: "0"},
						{Name: "flag", Type: dbparity.TypeInteger, Default: "1"},
						{Name: "empty_str", Type: dbparity.TypeText, Default: "''"},
						{Name: "json_meta", Type: dbparity.TypeText, Default: "'{}'"},
					},
				},
			},
		}
		if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected exact defaults to pass, got: %v", err)
		}
	})

	t.Run("default present but different fails naming expected and got", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, `
CREATE TABLE items (
	id TEXT PRIMARY KEY,
	retry_count INTEGER NOT NULL DEFAULT 0
);`); err != nil {
			t.Fatalf("create table: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "items",
					Columns: []dbparity.ColumnSpec{
						{Name: "id", Type: dbparity.TypeText, PrimaryKey: true},
						{Name: "retry_count", Type: dbparity.TypeInteger, Default: "5"},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected default mismatch error, got nil")
		}
		if !strings.Contains(err.Error(), "default mismatch") {
			t.Fatalf("expected error containing 'default mismatch', got: %v", err)
		}
		if !strings.Contains(err.Error(), "got \"0\"") || !strings.Contains(err.Error(), "want \"5\"") {
			t.Fatalf("expected error naming got '0' and want '5', got: %v", err)
		}
	})

	t.Run("default removed fails", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, `
CREATE TABLE items (
	id TEXT PRIMARY KEY,
	retry_count INTEGER NOT NULL
);`); err != nil {
			t.Fatalf("create table: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "items",
					Columns: []dbparity.ColumnSpec{
						{Name: "id", Type: dbparity.TypeText, PrimaryKey: true},
						{Name: "retry_count", Type: dbparity.TypeInteger, Default: "0"},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected default mismatch error when default is removed, got nil")
		}
		if !strings.Contains(err.Error(), "default mismatch") || !strings.Contains(err.Error(), "got no default") {
			t.Fatalf("expected error naming 'got no default', got: %v", err)
		}
	})
}

func TestVerifySchema_SpecValidation_EmptyColumns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	t.Run("empty foreign key columns rejected", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "orders",
					ForeignKeys: []dbparity.ForeignKeySpec{
						{Columns: []string{}, RefTable: "users"},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "empty columns") {
			t.Fatalf("expected empty columns error for FK, got: %v", err)
		}
	})

	t.Run("empty unique constraint columns rejected", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "users",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{}},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "empty columns") {
			t.Fatalf("expected empty columns error for unique constraint, got: %v", err)
		}
	})

	t.Run("empty index columns rejected", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_empty", Table: "users", Columns: []string{}},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "empty columns") {
			t.Fatalf("expected empty columns error for index, got: %v", err)
		}
	})

	t.Run("mismatched FK ref_columns count rejected", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "orders",
					ForeignKeys: []dbparity.ForeignKeySpec{
						{Columns: []string{"a", "b"}, RefTable: "parent", RefColumns: []string{"pa"}},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "mismatched column counts") {
			t.Fatalf("expected mismatched column counts error, got: %v", err)
		}
	})
}

func TestVerifySchema_SQLite_SafeQuotedIdentifiers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	// Create table and index with quoted identifiers containing quotes and special characters
	stmts := []string{
		`CREATE TABLE "user""table" (
			"id""col" TEXT PRIMARY KEY,
			"val""col" TEXT NOT NULL
		);`,
		`CREATE INDEX "idx""user""val" ON "user""table" ("val""col");`,
	}
	for _, stmt := range stmts {
		if _, err := bunDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec DDL failed: %v", err)
		}
	}

	spec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{
				Name: `user"table`,
				Columns: []dbparity.ColumnSpec{
					{Name: `id"col`, Type: dbparity.TypeText, PrimaryKey: true},
					{Name: `val"col`, Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{`id"col`},
			},
		},
		Indexes: []dbparity.IndexSpec{
			{
				Name:    `idx"user"val`,
				Table:   `user"table`,
				Columns: []string{`val"col`},
				Unique:  false,
			},
		},
	}

	if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
		t.Fatalf("expected quoted table and index lookup to succeed, got: %v", err)
	}
}

func TestVerifySchema_SQLite_ExactUniqueConstraints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	stmts := []string{
		`CREATE TABLE compound_unique (
			id TEXT PRIMARY KEY,
			col_a TEXT NOT NULL,
			col_b TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT 'this is unique text',
			CONSTRAINT uq_compound UNIQUE (col_a, col_b)
		);`,
	}
	for _, stmt := range stmts {
		if _, err := bunDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec DDL failed: %v", err)
		}
	}

	t.Run("exact composite unique constraint passes", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "compound_unique",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{"col_a", "col_b"}},
					},
				},
			},
		}
		if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected exact composite unique to pass, got: %v", err)
		}
	})

	t.Run("subset of composite unique rejected", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "compound_unique",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{"col_a"}},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when verifying subset [col_a] against UNIQUE(col_a, col_b)")
		}
		if !strings.Contains(err.Error(), "missing unique constraint on [col_a]") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("reversed column order in composite unique rejected", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "compound_unique",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{"col_b", "col_a"}},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error for reversed column order in unique constraint")
		}
		if !strings.Contains(err.Error(), "missing unique constraint on [col_b col_a]") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("unrelated unique substring in DDL rejected", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "compound_unique",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{"description"}},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error for non-unique column even if DDL contains word 'unique'")
		}
		if !strings.Contains(err.Error(), "missing unique constraint on [description]") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

func TestVerifySchema_SQLite_ExactForeignKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	stmts := []string{
		`CREATE TABLE parents (
			p_id1 TEXT NOT NULL,
			p_id2 TEXT NOT NULL,
			extra TEXT,
			PRIMARY KEY (p_id1, p_id2)
		);`,
		`CREATE TABLE children (
			c_id TEXT PRIMARY KEY,
			f_id1 TEXT NOT NULL,
			f_id2 TEXT NOT NULL,
			note TEXT DEFAULT 'references parents',
			FOREIGN KEY (f_id1, f_id2) REFERENCES parents(p_id1, p_id2)
		);`,
	}
	for _, stmt := range stmts {
		if _, err := bunDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec DDL failed: %v", err)
		}
	}

	t.Run("exact composite FK passes without explicit ref columns", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "children",
					ForeignKeys: []dbparity.ForeignKeySpec{
						{Columns: []string{"f_id1", "f_id2"}, RefTable: "parents"},
					},
				},
			},
		}
		if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected exact composite FK to pass, got: %v", err)
		}
	})

	t.Run("exact composite FK passes with matching ref columns", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "children",
					ForeignKeys: []dbparity.ForeignKeySpec{
						{Columns: []string{"f_id1", "f_id2"}, RefTable: "parents", RefColumns: []string{"p_id1", "p_id2"}},
					},
				},
			},
		}
		if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected composite FK with ref columns to pass, got: %v", err)
		}
	})

	t.Run("composite FK fails when local column subset requested", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "children",
					ForeignKeys: []dbparity.ForeignKeySpec{
						{Columns: []string{"f_id1"}, RefTable: "parents"},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when requesting subset [f_id1] for composite FK")
		}
		if !strings.Contains(err.Error(), "missing foreign key referencing \"parents\"") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("composite FK fails when ref columns reversed", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "children",
					ForeignKeys: []dbparity.ForeignKeySpec{
						{Columns: []string{"f_id1", "f_id2"}, RefTable: "parents", RefColumns: []string{"p_id2", "p_id1"}},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when ref columns order does not match")
		}
		if !strings.Contains(err.Error(), "missing foreign key referencing \"parents\"") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("foreign key referencing wrong table rejected even if DDL contains word 'references'", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "children",
					ForeignKeys: []dbparity.ForeignKeySpec{
						{Columns: []string{"note"}, RefTable: "parents"},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error for fake foreign key on column 'note'")
		}
		if !strings.Contains(err.Error(), "missing foreign key referencing \"parents\"") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

func TestVerifySchema_SentinelErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE sentinel_test (id TEXT PRIMARY KEY);"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// When index doesn't exist, sqlite_master query returns sql.ErrNoRows or 0 rows
	spec := dbparity.LogicalSchemaSpec{
		Indexes: []dbparity.IndexSpec{
			{Name: "non_existent_idx", Table: "sentinel_test", Columns: []string{"id"}},
		},
	}
	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for non-existent index")
	}
	if !strings.Contains(err.Error(), "missing SQLite index \"non_existent_idx\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestVerifySchema_SQLite_ExpressionKeyIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	stmts := []string{
		`CREATE TABLE expr_test (
			id TEXT PRIMARY KEY,
			col_a TEXT NOT NULL,
			col_b TEXT NOT NULL,
			col_c TEXT NOT NULL
		);`,
		`CREATE UNIQUE INDEX idx_expr_unique ON expr_test (col_a, lower(col_b));`,
		`CREATE INDEX idx_expr_middle ON expr_test (col_a, lower(col_b), col_c);`,
	}
	for _, stmt := range stmts {
		if _, err := bunDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec DDL: %v", err)
		}
	}

	t.Run("expression key in unique index cannot satisfy column-only unique constraint [col_a]", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "expr_test",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{"col_a"}},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error: index on (col_a, lower(col_b)) must NOT satisfy UNIQUE(col_a)")
		}
		if !strings.Contains(err.Error(), "missing unique constraint on [col_a]") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("expression key in unique index cannot satisfy column-only unique constraint [col_a, col_b]", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "expr_test",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{"col_a", "col_b"}},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error: index on (col_a, lower(col_b)) must NOT satisfy UNIQUE(col_a, col_b)")
		}
		if !strings.Contains(err.Error(), "missing unique constraint on [col_a col_b]") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("expression key in named index preserves position and rejects column list [col_a, col_c]", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{
					Name:    "idx_expr_middle",
					Table:   "expr_test",
					Columns: []string{"col_a", "col_c"},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error: index on (col_a, lower(col_b), col_c) must NOT match columns [col_a, col_c]")
		}
		if !strings.Contains(err.Error(), "columns mismatch") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

func TestVerifySchema_SQLite_ImplicitForeignKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	stmts := []string{
		`CREATE TABLE parent_users (
			user_id TEXT PRIMARY KEY,
			name TEXT NOT NULL
		);`,
		`CREATE TABLE child_orders (
			order_id TEXT PRIMARY KEY,
			buyer_id TEXT NOT NULL REFERENCES parent_users,
			description TEXT
		);`,
	}
	for _, stmt := range stmts {
		if _, err := bunDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec DDL: %v", err)
		}
	}

	t.Run("implicit target FK passes when RefColumns omitted", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "child_orders",
					ForeignKeys: []dbparity.ForeignKeySpec{
						{Columns: []string{"buyer_id"}, RefTable: "parent_users"},
					},
				},
			},
		}
		if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected implicit target FK with omitted RefColumns to pass, got: %v", err)
		}
	})

	t.Run("implicit target FK passes when RefColumns matches parent primary key", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "child_orders",
					ForeignKeys: []dbparity.ForeignKeySpec{
						{Columns: []string{"buyer_id"}, RefTable: "parent_users", RefColumns: []string{"user_id"}},
					},
				},
			},
		}
		if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected implicit target FK with matching parent PK to pass, got: %v", err)
		}
	})

	t.Run("implicit target FK fails when RefColumns does not match parent primary key", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "child_orders",
					ForeignKeys: []dbparity.ForeignKeySpec{
						{Columns: []string{"buyer_id"}, RefTable: "parent_users", RefColumns: []string{"name"}},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when RefColumns does not match parent PK")
		}
		if !strings.Contains(err.Error(), "missing foreign key referencing \"parent_users\"") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

func TestVerifySchema_SQLite_UnnamedIndex_Validation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	stmts := []string{
		`CREATE TABLE products (
			id TEXT PRIMARY KEY,
			sku TEXT NOT NULL,
			category TEXT NOT NULL,
			price_nano INTEGER NOT NULL,
			is_active BOOLEAN NOT NULL
		);`,
		`CREATE UNIQUE INDEX idx_products_sku_uq ON products(sku);`,
		`CREATE INDEX idx_products_cat_price ON products(category, price_nano);`,
		`CREATE INDEX idx_products_active_cat ON products(category) WHERE is_active = 1;`,
	}
	for _, stmt := range stmts {
		if _, err := bunDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec DDL: %v", err)
		}
	}

	t.Run("unnamed unique index passes", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{
					Table:   "products",
					Columns: []string{"sku"},
					Unique:  true,
				},
			},
		}
		if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected unnamed unique index matching to pass, got: %v", err)
		}
	})

	t.Run("unnamed composite non-unique index passes", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{
					Table:   "products",
					Columns: []string{"category", "price_nano"},
					Unique:  false,
				},
			},
		}
		if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected unnamed composite index matching to pass, got: %v", err)
		}
	})

	t.Run("unnamed partial index passes with matching predicate", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{
					Table:     "products",
					Columns:   []string{"category"},
					Unique:    false,
					Predicate: "is_active = 1",
				},
			},
		}
		if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected unnamed partial index matching to pass, got: %v", err)
		}
	})

	t.Run("unnamed index missing columns fails", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{
					Table:   "products",
					Columns: []string{"price_nano", "category"},
					Unique:  false,
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error for reversed column order on unnamed index")
		}
		if !strings.Contains(err.Error(), "missing index on [price_nano category]") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("unnamed index uniqueness mismatch fails", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{
					Table:   "products",
					Columns: []string{"sku"},
					Unique:  false,
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when uniqueness does not match on unnamed index")
		}
		if !strings.Contains(err.Error(), "missing index on [sku]") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("cancelled context propagates introspection error", func(t *testing.T) {
		cancCtx, cancel := context.WithCancel(context.Background())
		cancel()
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{
					Table:   "products",
					Columns: []string{"sku"},
					Unique:  true,
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(cancCtx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error on cancelled context")
		}
		if !strings.Contains(err.Error(), "sqlite check unnamed index") && !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

func TestIsMissingRow(t *testing.T) {
	t.Parallel()

	if dbparity.IsMissingRow(nil) {
		t.Error("IsMissingRow(nil) should be false")
	}
	if !dbparity.IsMissingRow(sql.ErrNoRows) {
		t.Error("IsMissingRow(sql.ErrNoRows) should be true")
	}
	if !dbparity.IsMissingRow(fmt.Errorf("wrapped: %w", sql.ErrNoRows)) {
		t.Error("IsMissingRow(wrapped sql.ErrNoRows) should be true")
	}
	if !dbparity.IsMissingRow(fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", sql.ErrNoRows))) {
		t.Error("IsMissingRow(nested wrapped sql.ErrNoRows) should be true")
	}
	if dbparity.IsMissingRow(errors.New("something else")) {
		t.Error("IsMissingRow(other error) should be false")
	}
}
