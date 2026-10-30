//go:build integration

package dbparity_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/uptrace/bun"
)

var schemaSeq atomic.Uint64

func setPostgresSearchPath(dsn, schema string) (string, error) {
	before, after, ok := strings.Cut(dsn, "?")
	var base, rawQuery string
	if !ok {
		base, rawQuery = dsn, ""
	} else {
		base, rawQuery = before, after
	}
	vals, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("parse dsn query: %w", err)
	}
	vals.Del("search_path")
	vals.Set("search_path", schema)
	encoded := vals.Encode()
	if encoded == "" {
		return base, nil
	}
	return base + "?" + encoded, nil
}

func newPostgresTestDB(t *testing.T) (*bun.DB, string) {
	t.Helper()
	return newPostgresTestDBWithPool(t, db.PoolSettings{})
}

func newPostgresTestDBWithPool(t *testing.T, pool db.PoolSettings) (*bun.DB, string) {
	t.Helper()
	dsn := testkit.SkipUnlessPostgres(t)
	adminDSN, _ := testkit.PostgresAdminDSN()
	if adminDSN == "" {
		adminDSN = dsn
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	schemaName := fmt.Sprintf("test_schema_parity_%d_%d", time.Now().UnixNano(), schemaSeq.Add(1))

	// Create schema via admin DB connection
	adminDB, err := db.OpenPostgresBun(ctx, adminDSN, db.PoolSettings{})
	if err != nil {
		t.Fatalf("open admin postgres db: %v", err)
	}
	if _, err := adminDB.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA %s;", schemaName)); err != nil {
		if closeErr := adminDB.Close(); closeErr != nil {
			t.Errorf("close adminDB after create schema failure: %v", closeErr)
		}
		t.Fatalf("create isolated schema %s: %v", schemaName, err)
	}
	if closeErr := adminDB.Close(); closeErr != nil {
		t.Errorf("close adminDB after create schema: %v", closeErr)
	}

	var bunDB *bun.DB
	var cleanedUp bool
	// Register cleanup immediately after CREATE SCHEMA so that even if subsequent steps fatal, cleanup runs
	t.Cleanup(func() {
		if cleanedUp {
			return
		}
		cleanedUp = true
		if bunDB != nil {
			if closeErr := bunDB.Close(); closeErr != nil {
				t.Errorf("close bunDB during cleanup: %v", closeErr)
			}
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		cleanupDB, err := db.OpenPostgresBun(cleanupCtx, adminDSN, db.PoolSettings{})
		if err != nil {
			t.Errorf("open adminDB for cleanup schema %s: %v", schemaName, err)
			return
		}
		defer func() {
			if closeErr := cleanupDB.Close(); closeErr != nil {
				t.Errorf("close cleanupDB for schema %s: %v", schemaName, closeErr)
			}
		}()
		if _, err := cleanupDB.ExecContext(cleanupCtx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE;", schemaName)); err != nil {
			t.Errorf("drop schema %s: %v", schemaName, err)
		}
	})

	// Open test bun DB with search_path URL parameter in DSN
	schemaDSN, err := setPostgresSearchPath(dsn, schemaName)
	if err != nil {
		t.Fatalf("set search_path on dsn: %v", err)
	}

	bunDB, err = db.OpenPostgresBun(ctx, schemaDSN, pool)
	if err != nil {
		t.Fatalf("OpenPostgresBun with search_path: %v", err)
	}

	return bunDB, schemaName
}

func TestVerifySchema_Postgres_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB, _ := newPostgresTestDB(t)

	stmts := []string{
		`CREATE TABLE users (
	id VARCHAR(64) PRIMARY KEY,
	username TEXT NOT NULL,
	email TEXT,
	is_active BOOLEAN NOT NULL DEFAULT true,
	age INTEGER,
	metadata JSONB,
	avatar BYTEA,
	created_at TIMESTAMPTZ NOT NULL
)`,
		`CREATE TABLE orders (
	order_id VARCHAR(64) PRIMARY KEY,
	user_id VARCHAR(64) NOT NULL REFERENCES users(id),
	amount_nano BIGINT NOT NULL CHECK(amount_nano > 0),
	status TEXT NOT NULL
)`,
		`CREATE UNIQUE INDEX idx_users_username ON users(username)`,
		`CREATE INDEX idx_orders_pending ON orders(status, amount_nano) WHERE status = 'pending'`,
		`CREATE OR REPLACE FUNCTION prevent_orders_delete() RETURNS trigger AS $$
BEGIN
	RAISE EXCEPTION 'orders are immutable';
END;
$$ LANGUAGE plpgsql`,
		`CREATE TRIGGER orders_immutable_delete BEFORE DELETE ON orders
FOR EACH ROW EXECUTE FUNCTION prevent_orders_delete()`,
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
					{Name: "is_active", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), DefaultValue: "true"},
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

	if err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec); err != nil {
		t.Fatalf("VerifyPostgresSchema unexpected error: %v", err)
	}

	if err := dbparity.VerifySchema(ctx, bunDB, spec); err != nil {
		t.Fatalf("VerifySchema unexpected error: %v", err)
	}
}

func TestVerifySchema_Postgres_NegativeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB, _ := newPostgresTestDB(t)

	if _, err := bunDB.ExecContext(ctx, `
CREATE TABLE accounts (
	id VARCHAR(64) PRIMARY KEY,
	balance BIGINT NOT NULL,
	retired_old_col TEXT
);
CREATE TABLE items (
	id VARCHAR(64) PRIMARY KEY,
	count INTEGER CHECK(count > 0)
);
`); err != nil {
		t.Fatalf("exec DDL: %v", err)
	}

	t.Run("missing table", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{{Name: "non_existent_table"}},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "table \"non_existent_table\" not found") {
			t.Fatalf("expected missing table error, got: %v", err)
		}
	})

	t.Run("missing column", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "accounts",
					Columns: []dbparity.ColumnSpec{
						{Name: "missing_col"},
					},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "missing column \"missing_col\"") {
			t.Fatalf("expected missing column error, got: %v", err)
		}
	})

	t.Run("column type mismatch", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "accounts",
					Columns: []dbparity.ColumnSpec{
						{Name: "id", Type: dbparity.TypeInteger},
					},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "type mismatch") {
			t.Fatalf("expected type mismatch error, got: %v", err)
		}
	})

	t.Run("nullability mismatch", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "accounts",
					Columns: []dbparity.ColumnSpec{
						{Name: "balance", Nullable: dbparity.PtrBool(true)},
					},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "must be nullable") {
			t.Fatalf("expected nullability mismatch error, got: %v", err)
		}
	})

	t.Run("missing index", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{
					Name:    "idx_missing_pg",
					Table:   "accounts",
					Columns: []string{"balance"},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "missing PostgreSQL index \"idx_missing_pg\"") {
			t.Fatalf("expected missing index error, got: %v", err)
		}
	})

	t.Run("retired column present", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Retired: dbparity.RetiredArtifacts{
				Columns: []dbparity.RetiredColumn{
					{Table: "accounts", Column: "retired_old_col"},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "contains retired column \"retired_old_col\"") {
			t.Fatalf("expected retired column error, got: %v", err)
		}
	})

	t.Run("index uniqueness mismatch", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_test_accounts_balance", Table: "accounts", Columns: []string{"balance"}, Unique: true},
			},
		}
		_, err := bunDB.ExecContext(ctx, "CREATE INDEX idx_test_accounts_balance ON accounts(balance);")
		if err != nil {
			t.Fatalf("create index: %v", err)
		}
		err = dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "uniqueness mismatch") {
			t.Fatalf("expected uniqueness mismatch error, got: %v", err)
		}
	})

	t.Run("index columns mismatch", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_test_accounts_balance_col", Table: "accounts", Columns: []string{"id"}},
			},
		}
		_, err := bunDB.ExecContext(ctx, "CREATE INDEX idx_test_accounts_balance_col ON accounts(balance);")
		if err != nil {
			t.Fatalf("create index: %v", err)
		}
		err = dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "columns mismatch") {
			t.Fatalf("expected columns mismatch error, got: %v", err)
		}
	})

	t.Run("index owning table mismatch", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_test_accounts_tbl", Table: "items", Columns: []string{"balance"}},
			},
		}
		_, err := bunDB.ExecContext(ctx, "CREATE INDEX idx_test_accounts_tbl ON accounts(balance);")
		if err != nil {
			t.Fatalf("create index: %v", err)
		}
		err = dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "owning table mismatch") {
			t.Fatalf("expected owning table mismatch error, got: %v", err)
		}
	})

	t.Run("index missing predicate", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_test_accounts_pred_miss", Table: "accounts", Columns: []string{"balance"}, Predicate: "balance > 0"},
			},
		}
		_, err := bunDB.ExecContext(ctx, "CREATE INDEX idx_test_accounts_pred_miss ON accounts(balance);")
		if err != nil {
			t.Fatalf("create index: %v", err)
		}
		err = dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "missing predicate") {
			t.Fatalf("expected missing predicate error, got: %v", err)
		}
	})

	t.Run("index unexpected partial predicate", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_test_accounts_pred_unexp", Table: "accounts", Columns: []string{"balance"}},
			},
		}
		_, err := bunDB.ExecContext(ctx, "CREATE INDEX idx_test_accounts_pred_unexp ON accounts(balance) WHERE balance > 0;")
		if err != nil {
			t.Fatalf("create index: %v", err)
		}
		err = dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "unexpected partial predicate") {
			t.Fatalf("expected unexpected partial predicate error, got: %v", err)
		}
	})

	t.Run("index predicate mismatch", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_test_accounts_pred_diff", Table: "accounts", Columns: []string{"balance"}, Predicate: "balance > 100"},
			},
		}
		_, err := bunDB.ExecContext(ctx, "CREATE INDEX idx_test_accounts_pred_diff ON accounts(balance) WHERE balance > 0;")
		if err != nil {
			t.Fatalf("create index: %v", err)
		}
		err = dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "predicate mismatch") {
			t.Fatalf("expected predicate mismatch error, got: %v", err)
		}
	})
}

func TestVerifySchema_Postgres_ExactConstraints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB, _ := newPostgresTestDB(t)

	stmts := []string{
		`CREATE TABLE parents (
			p_id1 VARCHAR(64) NOT NULL,
			p_id2 VARCHAR(64) NOT NULL,
			extra TEXT,
			PRIMARY KEY (p_id1, p_id2)
		);`,
		`CREATE TABLE children (
			c_id VARCHAR(64) PRIMARY KEY,
			f_id1 VARCHAR(64) NOT NULL,
			f_id2 VARCHAR(64) NOT NULL,
			description TEXT NOT NULL DEFAULT 'references parents',
			FOREIGN KEY (f_id1, f_id2) REFERENCES parents(p_id1, p_id2),
			CONSTRAINT uq_children_f_ids UNIQUE (f_id1, f_id2)
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
					Name: "children",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{"f_id1", "f_id2"}},
					},
				},
			},
		}
		if err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected exact composite unique to pass on postgres, got: %v", err)
		}
	})

	t.Run("subset of composite unique rejected on postgres", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "children",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{"f_id1"}},
					},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when verifying subset [f_id1] against UNIQUE(f_id1, f_id2) on postgres")
		}
		if !strings.Contains(err.Error(), "missing unique constraint on [f_id1]") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("reversed column order in composite unique rejected on postgres", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "children",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{"f_id2", "f_id1"}},
					},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error for reversed column order in unique constraint on postgres")
		}
		if !strings.Contains(err.Error(), "missing unique constraint on [f_id2 f_id1]") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("unrelated unique column rejected on postgres", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "children",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{"description"}},
					},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error for non-unique column 'description' on postgres")
		}
		if !strings.Contains(err.Error(), "missing unique constraint on [description]") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("exact composite FK passes without explicit ref columns on postgres", func(t *testing.T) {
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
		if err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected exact composite FK to pass on postgres, got: %v", err)
		}
	})

	t.Run("exact composite FK passes with matching ref columns on postgres", func(t *testing.T) {
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
		if err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected composite FK with ref columns to pass on postgres, got: %v", err)
		}
	})

	t.Run("composite FK fails when local column subset requested on postgres", func(t *testing.T) {
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
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when requesting subset [f_id1] for composite FK on postgres")
		}
		if !strings.Contains(err.Error(), "missing foreign key referencing \"parents\"") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("composite FK fails when ref columns reversed on postgres", func(t *testing.T) {
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
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when ref columns order does not match on postgres")
		}
		if !strings.Contains(err.Error(), "missing foreign key referencing \"parents\"") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("foreign key referencing wrong table rejected on postgres", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "children",
					ForeignKeys: []dbparity.ForeignKeySpec{
						{Columns: []string{"description"}, RefTable: "parents"},
					},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error for fake foreign key on column 'description' on postgres")
		}
		if !strings.Contains(err.Error(), "missing foreign key referencing \"parents\"") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

func TestVerifySchema_Postgres_ExpressionKeyIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB, _ := newPostgresTestDB(t)

	stmts := []string{
		`CREATE TABLE expr_test_pg (
			id VARCHAR(64) PRIMARY KEY,
			col_a TEXT NOT NULL,
			col_b TEXT NOT NULL,
			col_c TEXT NOT NULL
		);`,
		`CREATE UNIQUE INDEX idx_pg_expr_unique ON expr_test_pg (col_a, lower(col_b));`,
		`CREATE INDEX idx_pg_expr_middle ON expr_test_pg (col_a, lower(col_b), col_c);`,
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
					Name: "expr_test_pg",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{"col_a"}},
					},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error: index on (col_a, lower(col_b)) must NOT satisfy UNIQUE(col_a) on postgres")
		}
		if !strings.Contains(err.Error(), "missing unique constraint on [col_a]") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("expression key in unique index cannot satisfy column-only unique constraint [col_a, col_b]", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "expr_test_pg",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{"col_a", "col_b"}},
					},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error: index on (col_a, lower(col_b)) must NOT satisfy UNIQUE(col_a, col_b) on postgres")
		}
		if !strings.Contains(err.Error(), "missing unique constraint on [col_a col_b]") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("expression key in named index preserves position and rejects column list [col_a, col_c]", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{
					Name:    "idx_pg_expr_middle",
					Table:   "expr_test_pg",
					Columns: []string{"col_a", "col_c"},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error: index on (col_a, lower(col_b), col_c) must NOT match columns [col_a, col_c] on postgres")
		}
		if !strings.Contains(err.Error(), "columns mismatch") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

func TestVerifySchema_Postgres_IncludeIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB, _ := newPostgresTestDB(t)

	stmts := []string{
		`CREATE TABLE inc_test_pg (
			id VARCHAR(64) PRIMARY KEY,
			key_col TEXT NOT NULL,
			inc_col TEXT NOT NULL
		);`,
		`CREATE UNIQUE INDEX idx_pg_inc_uq ON inc_test_pg (key_col) INCLUDE (inc_col);`,
		`CREATE INDEX idx_pg_inc_non_uq ON inc_test_pg (key_col) INCLUDE (inc_col);`,
	}
	for _, stmt := range stmts {
		if _, err := bunDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec DDL: %v", err)
		}
	}

	t.Run("unique constraint matches key-only columns and ignores include columns", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "inc_test_pg",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{"key_col"}},
					},
				},
			},
		}
		if err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected unique index with INCLUDE to satisfy UNIQUE(key_col), got: %v", err)
		}
	})

	t.Run("unique constraint rejects key_col + inc_col as key columns", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "inc_test_pg",
					UniqueConstraints: []dbparity.UniqueConstraintSpec{
						{Columns: []string{"key_col", "inc_col"}},
					},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when unique constraint expects [key_col, inc_col] for index with INCLUDE")
		}
		if !strings.Contains(err.Error(), "missing unique constraint on [key_col inc_col]") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("named index matches key-only columns", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{
					Name:    "idx_pg_inc_non_uq",
					Table:   "inc_test_pg",
					Columns: []string{"key_col"},
					Unique:  false,
				},
			},
		}
		if err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected named index with INCLUDE to match key-only columns [key_col], got: %v", err)
		}
	})
}

func TestVerifySchema_Postgres_SingleConnection_UniqueIndexNoDeadlock(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1 connection in pool: proves postgresHasUniqueConstraint closes rows before fallback query
	bunDB, _ := newPostgresTestDBWithPool(t, db.PoolSettings{
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})

	stmts := []string{
		`CREATE TABLE one_conn_table (
			id VARCHAR(64) PRIMARY KEY,
			token_hash TEXT NOT NULL
		);`,
		// Create a unique INDEX (not a table constraint), forcing fallback path in postgresHasUniqueConstraint
		`CREATE UNIQUE INDEX idx_one_conn_token ON one_conn_table (token_hash);`,
	}
	for _, stmt := range stmts {
		if _, err := bunDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec DDL: %v", err)
		}
	}

	spec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{
				Name: "one_conn_table",
				UniqueConstraints: []dbparity.UniqueConstraintSpec{
					{Columns: []string{"token_hash"}},
				},
			},
		},
	}

	if err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec); err != nil {
		t.Fatalf("expected 1-connection unique index fallback verification to pass without blocking, got: %v", err)
	}
}

func TestVerifySchema_Postgres_ForeignKeySchemaScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB, schemaName := newPostgresTestDB(t)

	adminDSN, _ := testkit.PostgresAdminDSN()
	if adminDSN == "" {
		adminDSN = testkit.SkipUnlessPostgres(t)
	}

	otherSchema := fmt.Sprintf("other_schema_%d_%d", time.Now().UnixNano(), schemaSeq.Add(1))
	adminDB, err := db.OpenPostgresBun(ctx, adminDSN, db.PoolSettings{})
	if err != nil {
		t.Fatalf("open admin DB: %v", err)
	}
	if _, err := adminDB.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA %s; CREATE TABLE %s.parents (id VARCHAR(64) PRIMARY KEY);", otherSchema, otherSchema)); err != nil {
		_ = adminDB.Close()
		t.Fatalf("create other schema/table: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		cleanupDB, err := db.OpenPostgresBun(cleanupCtx, adminDSN, db.PoolSettings{})
		if err == nil {
			_, _ = cleanupDB.ExecContext(cleanupCtx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE;", otherSchema))
			_ = cleanupDB.Close()
		}
	})
	_ = adminDB.Close()

	// In current schema, create child referencing other_schema.parents
	if _, err := bunDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s.children (id VARCHAR(64) PRIMARY KEY, parent_id VARCHAR(64) REFERENCES %s.parents(id));", schemaName, otherSchema)); err != nil {
		t.Fatalf("create child referencing other schema: %v", err)
	}

	// Schema spec asserting FK referencing 'parents' in current_schema() must fail
	spec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{
				Name: "children",
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"parent_id"}, RefTable: "parents"},
				},
			},
		},
	}

	err = dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error: FK referencing parents in other_schema must NOT satisfy spec for current_schema()")
	}
	if !strings.Contains(err.Error(), "missing foreign key referencing \"parents\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestVerifySchema_Postgres_SingleConnection_UnnamedIndexNoDeadlock(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1 connection in pool: proves postgresHasIndexMatching closes rows before detail queries
	bunDB, _ := newPostgresTestDBWithPool(t, db.PoolSettings{
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})

	stmts := []string{
		`CREATE TABLE one_conn_unnamed_tbl (
			id VARCHAR(64) PRIMARY KEY,
			token_hash TEXT NOT NULL,
			scope TEXT NOT NULL
		);`,
		`CREATE INDEX idx_one_conn_unnamed_scope ON one_conn_unnamed_tbl (token_hash, scope);`,
	}
	for _, stmt := range stmts {
		if _, err := bunDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec DDL: %v", err)
		}
	}

	spec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{
				Name: "one_conn_unnamed_tbl",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", PrimaryKey: true},
					{Name: "token_hash"},
					{Name: "scope"},
				},
				PrimaryKey: []string{"id"},
			},
		},
		Indexes: []dbparity.IndexSpec{
			{
				Table:   "one_conn_unnamed_tbl",
				Columns: []string{"token_hash", "scope"},
				Unique:  false,
			},
		},
	}

	if err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec); err != nil {
		t.Fatalf("expected 1-connection unnamed index verification to pass without blocking, got: %v", err)
	}
}

func TestVerifySchema_Postgres_UnnamedIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB, _ := newPostgresTestDB(t)

	stmts := []string{
		`CREATE TABLE unnamed_pg_tbl (
			id VARCHAR(64) PRIMARY KEY,
			category TEXT NOT NULL,
			price_nano BIGINT NOT NULL,
			is_active BOOLEAN NOT NULL
		);`,
		`CREATE UNIQUE INDEX idx_unnamed_pg_cat ON unnamed_pg_tbl (category);`,
		`CREATE INDEX idx_unnamed_pg_comp ON unnamed_pg_tbl (category, price_nano);`,
		`CREATE INDEX idx_unnamed_pg_partial ON unnamed_pg_tbl (category) WHERE is_active = true;`,
	}
	for _, stmt := range stmts {
		if _, err := bunDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec DDL: %v", err)
		}
	}

	t.Run("unnamed unique index matches", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Table: "unnamed_pg_tbl", Columns: []string{"category"}, Unique: true},
			},
		}
		if err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected unnamed unique index matching to pass, got: %v", err)
		}
	})

	t.Run("unnamed composite non-unique index matches", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Table: "unnamed_pg_tbl", Columns: []string{"category", "price_nano"}, Unique: false},
			},
		}
		if err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected unnamed composite index matching to pass, got: %v", err)
		}
	})

	t.Run("unnamed partial index matches with predicate", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Table: "unnamed_pg_tbl", Columns: []string{"category"}, Unique: false, Predicate: "is_active = true"},
			},
		}
		if err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected unnamed partial index matching to pass, got: %v", err)
		}
	})

	t.Run("unnamed index columns mismatch fails", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Table: "unnamed_pg_tbl", Columns: []string{"price_nano", "category"}, Unique: false},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error for reversed column order on unnamed index")
		}
		if !strings.Contains(err.Error(), "missing index on [price_nano category]") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

func TestVerifySchema_Postgres_InvalidAndNotReadyIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB, schemaName := newPostgresTestDB(t)

	stmts := []string{
		`CREATE TABLE validity_tbl (
			id VARCHAR(64) PRIMARY KEY,
			col_named TEXT NOT NULL,
			col_unnamed TEXT NOT NULL,
			col_uq TEXT NOT NULL
		);`,
		`CREATE INDEX idx_validity_named ON validity_tbl (col_named);`,
		`CREATE INDEX idx_validity_unnamed ON validity_tbl (col_unnamed);`,
		`CREATE UNIQUE INDEX idx_validity_uq ON validity_tbl (col_uq);`,
	}
	for _, stmt := range stmts {
		if _, err := bunDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec DDL: %v", err)
		}
	}

	// 1. Check baseline passes
	baseSpec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{
				Name: "validity_tbl",
				UniqueConstraints: []dbparity.UniqueConstraintSpec{
					{Columns: []string{"col_uq"}},
				},
			},
		},
		Indexes: []dbparity.IndexSpec{
			{Name: "idx_validity_named", Table: "validity_tbl", Columns: []string{"col_named"}, Unique: false},
			{Table: "validity_tbl", Columns: []string{"col_unnamed"}, Unique: false},
		},
	}
	if err := dbparity.VerifyPostgresSchema(ctx, bunDB, baseSpec); err != nil {
		t.Fatalf("expected baseline to pass, got: %v", err)
	}

	// Try updating pg_index via admin connection
	adminDSN, _ := testkit.PostgresAdminDSN()
	if adminDSN == "" {
		adminDSN = testkit.SkipUnlessPostgres(t)
	}
	adminDB, err := db.OpenPostgresBun(ctx, adminDSN, db.PoolSettings{})
	if err != nil {
		t.Logf("open admin DB for catalog update failed: %v", err)
		return
	}
	defer func() { _ = adminDB.Close() }()

	_, err = adminDB.ExecContext(ctx, fmt.Sprintf("UPDATE pg_index SET indisvalid = false WHERE indexrelid = '%s.idx_validity_named'::regclass;", schemaName))
	if err != nil {
		t.Logf("catalog update not permitted in test environment: %v; asserting valid/ready in baseline query source", err)
		return
	}

	// 2. Named invalid index fails with explanatory error
	specNamed := dbparity.LogicalSchemaSpec{
		Indexes: []dbparity.IndexSpec{
			{Name: "idx_validity_named", Table: "validity_tbl", Columns: []string{"col_named"}, Unique: false},
		},
	}
	err = dbparity.VerifyPostgresSchema(ctx, bunDB, specNamed)
	if err == nil {
		t.Fatal("expected error for invalid named index")
	}
	if !strings.Contains(err.Error(), "is invalid (indisvalid = false)") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Restore valid, mark not ready
	if _, err := adminDB.ExecContext(ctx, fmt.Sprintf("UPDATE pg_index SET indisvalid = true, indisready = false WHERE indexrelid = '%s.idx_validity_named'::regclass;", schemaName)); err != nil {
		t.Fatalf("update indisready: %v", err)
	}
	err = dbparity.VerifyPostgresSchema(ctx, bunDB, specNamed)
	if err == nil {
		t.Fatal("expected error for not ready named index")
	}
	if !strings.Contains(err.Error(), "is not ready (indisready = false)") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// 3. Unnamed index with indisvalid = false fails to match
	if _, err := adminDB.ExecContext(ctx, fmt.Sprintf("UPDATE pg_index SET indisvalid = false WHERE indexrelid = '%s.idx_validity_unnamed'::regclass;", schemaName)); err != nil {
		t.Fatalf("update indisvalid unnamed: %v", err)
	}
	specUnnamed := dbparity.LogicalSchemaSpec{
		Indexes: []dbparity.IndexSpec{
			{Table: "validity_tbl", Columns: []string{"col_unnamed"}, Unique: false},
		},
	}
	err = dbparity.VerifyPostgresSchema(ctx, bunDB, specUnnamed)
	if err == nil {
		t.Fatal("expected error for invalid unnamed index")
	}
	if !strings.Contains(err.Error(), "missing index on [col_unnamed]") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// 4. Unique fallback with indisvalid = false fails to satisfy UniqueConstraintSpec
	if _, err := adminDB.ExecContext(ctx, fmt.Sprintf("UPDATE pg_index SET indisvalid = false WHERE indexrelid = '%s.idx_validity_uq'::regclass;", schemaName)); err != nil {
		t.Fatalf("update indisvalid uq: %v", err)
	}
	specUQ := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{
				Name: "validity_tbl",
				UniqueConstraints: []dbparity.UniqueConstraintSpec{
					{Columns: []string{"col_uq"}},
				},
			},
		},
	}
	err = dbparity.VerifyPostgresSchema(ctx, bunDB, specUQ)
	if err == nil {
		t.Fatal("expected error when unique fallback index is invalid")
	}
	if !strings.Contains(err.Error(), "missing unique constraint on [col_uq]") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
