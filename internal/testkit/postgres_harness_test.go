package testkit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uptrace/bun"
)

func TestUniquePostgresStoreID_isGloballyDistinct(t *testing.T) {
	a := UniquePostgresStoreID("auth")
	b := UniquePostgresStoreID("auth")
	if a == "" || b == "" || a == b {
		t.Fatalf("want distinct non-empty ids; a=%q b=%q", a, b)
	}
	if !strings.HasPrefix(a, "auth-") || !strings.HasPrefix(b, "auth-") {
		t.Fatalf("prefix missing: a=%q b=%q", a, b)
	}
}

func TestClassifyForbiddenRuntimeSQL(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		want    string
		wantAny bool // non-empty reason required; exact string optional
	}{
		{name: "select_ok", query: `SELECT row_json FROM usage_authority_limit_rows WHERE store_id=?`, want: ""},
		{name: "insert_ok", query: `INSERT INTO concurrency_leases(store_id, lease_id) VALUES (?,?)`, want: ""},
		{name: "update_ok", query: `UPDATE metering_facts SET store_id=? WHERE fact_id=?`, want: ""},
		{name: "delete_ok", query: `DELETE FROM concurrency_leases WHERE store_id=?`, want: ""},
		{name: "set_transaction_ok", query: `SET TRANSACTION ISOLATION LEVEL READ COMMITTED`, want: ""},
		{name: "set_transaction_lower", query: `set transaction read only`, want: ""},
		{name: "advisory_xact_ok", query: `SELECT pg_advisory_xact_lock(42)`, want: ""},
		{name: "try_advisory_xact_ok", query: `SELECT pg_try_advisory_xact_lock(1, 2)`, want: ""},
		{name: "leading_whitespace_create", query: "  \n\tCREATE TABLE IF NOT EXISTS concurrency_leases (store_id TEXT)", wantAny: true},
		{name: "line_comment_then_create", query: "-- bootstrap\nCREATE TABLE t(id INT)", wantAny: true},
		{name: "block_comment_then_alter", query: "/* schema */ ALTER TABLE metering_facts ADD COLUMN x TEXT", wantAny: true},
		{name: "create_table", query: `CREATE TABLE IF NOT EXISTS concurrency_leases (store_id TEXT)`, wantAny: true},
		{name: "create_index", query: `CREATE INDEX IF NOT EXISTS idx_x ON concurrency_leases(store_id)`, wantAny: true},
		{name: "alter", query: `ALTER TABLE metering_facts ADD COLUMN x TEXT`, wantAny: true},
		{name: "drop", query: `DROP TABLE metering_facts`, wantAny: true},
		{name: "search_path", query: `SET search_path TO lease_test_1`, wantAny: true},
		{name: "set_local", query: `SET LOCAL work_mem = '64MB'`, wantAny: true},
		{name: "set_guc", query: `SET work_mem = '64MB'`, wantAny: true},
		{name: "reset", query: `RESET search_path`, wantAny: true},
		{name: "reset_all", query: `RESET ALL`, wantAny: true},
		{name: "prepare", query: `PREPARE foo AS SELECT 1`, wantAny: true},
		{name: "deallocate", query: `DEALLOCATE foo`, wantAny: true},
		{name: "temp_table", query: `CREATE TEMP TABLE tmp (id INT)`, wantAny: true},
		{name: "temporary_table", query: `CREATE TEMPORARY TABLE tmp (id INT)`, wantAny: true},
		{name: "session_advisory_lock", query: `SELECT pg_advisory_lock(99)`, wantAny: true},
		{name: "session_try_advisory_lock", query: `SELECT pg_try_advisory_lock(1, 2)`, wantAny: true},
		{name: "session_advisory_unlock", query: `SELECT pg_advisory_unlock(99)`, wantAny: true},
		{name: "literal_contains_create_word", query: `SELECT ' create table evil' AS note`, want: ""},
		{name: "comment_contains_drop_word", query: `SELECT 1 /* drop table evil */`, want: ""},
		{name: "line_comment_contains_alter", query: "SELECT 1 -- alter table evil", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyForbiddenRuntimeSQL(tc.query)
			if tc.wantAny {
				if got == "" {
					t.Fatalf("query %q: want forbidden reason, got empty", tc.query)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("query %q: got %q want %q", tc.query, got, tc.want)
			}
		})
	}
}

func TestRuntimeSQLGuard_recordsViolations(t *testing.T) {
	guard := NewRuntimeSQLGuard()
	_ = guard.BeforeQuery(t.Context(), &bun.QueryEvent{Query: `SET search_path TO public`})
	_ = guard.BeforeQuery(t.Context(), &bun.QueryEvent{Query: `SELECT 1`})
	violations := guard.Violations()
	if len(violations) != 1 {
		t.Fatalf("violations=%v", violations)
	}
}

func TestCleanupStatementsJournalUsesStoreScopedFilters(t *testing.T) {
	const storeID = "store-under-test"

	statements := cleanupStatements(PostgresComponentJournal, storeID)
	// Dependency-safe order: child rows before metering_facts. components
	// carries FK (store_id, observation_row_id) -> facts(store_id, id);
	// outbox, supersessions and filters are fact-scoped children.
	wantTables := []string{
		"metering_observation_economic_outbox",
		"metering_components",
		"metering_fact_supersessions",
		"metering_fact_filters",
		"metering_facts",
	}
	if len(statements) != len(wantTables) {
		t.Fatalf("journal cleanup statements=%d want %d (%v)", len(statements), len(wantTables), wantTables)
	}
	for i, table := range wantTables {
		normalized := strings.Join(strings.Fields(statements[i].sql), " ")
		want := "DELETE FROM " + table + " WHERE store_id = ?"
		if normalized != want {
			t.Fatalf("journal cleanup[%d] %q want %q (ordered, store-scoped)", i, normalized, want)
		}
		if len(statements[i].args) != 1 || statements[i].args[0] != storeID {
			t.Fatalf("journal cleanup[%d] args=%v want [%q]", i, statements[i].args, storeID)
		}
	}
}

func TestAssertNoSearchPathInDualPlanePostgresSources(t *testing.T) {
	root := moduleRoot(t)
	for _, relDir := range DualPlanePostgresSearchPathGuardDirs() {
		dir := filepath.Join(root, filepath.FromSlash(relDir))
		AssertNoSearchPathInSources(t, dir)
	}
}

// TestAssertNoSearchPathInSources pins the shared guard seam that the
// open-source and Enterprise store guards both call: a directory without any
// search_path reference passes, and a directory that references search_path
// fails closed.
func TestAssertNoSearchPathInSources(t *testing.T) {
	t.Run("clean directory passes", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "store.go"), []byte("package store\n\nconst query = \"SELECT 1\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "nested.go"), []byte("package store\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		AssertNoSearchPathInSources(t, dir)
	})

	t.Run("search_path reference fails closed", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "store.go"), []byte("package store\n\nconst query = \"SET search_path TO public\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := noSearchPathInSources(dir)
		if err == nil {
			t.Fatal("expected the search_path guard to fail closed for a directory that references search_path")
		}
		if !strings.Contains(err.Error(), "search_path") {
			t.Fatalf("guard error %q must name the search_path violation", err)
		}
	})

	t.Run("missing directory fails closed", func(t *testing.T) {
		err := noSearchPathInSources(filepath.Join(t.TempDir(), "absent"))
		if err == nil {
			t.Fatal("expected the search_path guard to fail closed for a missing directory")
		}
	})
}
