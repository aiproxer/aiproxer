package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/uptrace/bun/driver/pgdriver"
)

// classifyProbeDB opens a private file-backed SQLite database for classifier
// probes. File-backed (not ":memory:") because SQLITE_BUSY needs two real
// connections contending on one lock file.
func classifyProbeDB(t *testing.T) *sql.DB {
	t.Helper()
	return openClassifyProbe(t, filepath.Join(t.TempDir(), "classify.db"))
}

func openClassifyProbe(t *testing.T, path string) *sql.DB {
	t.Helper()
	sqldb, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqldb.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqldb.Close() })
	return sqldb
}

func TestIsUniqueViolation_RealSQLiteCodes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first := classifyProbeDB(t)
	if _, err := first.ExecContext(ctx, `CREATE TABLE classify_unique_probe(id INTEGER PRIMARY KEY, code TEXT NOT NULL UNIQUE)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := first.ExecContext(ctx, `INSERT INTO classify_unique_probe(id, code) VALUES (1, 'a')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, uniqueErr := first.ExecContext(ctx, `INSERT INTO classify_unique_probe(id, code) VALUES (2, 'a')`)
	if uniqueErr == nil {
		t.Fatal("expected UNIQUE constraint violation")
	}
	if !IsUniqueViolation(uniqueErr) {
		t.Fatalf("UNIQUE violation not classified: %v", uniqueErr)
	}
	if !IsUniqueViolation(fmt.Errorf("insert: %w", uniqueErr)) {
		t.Fatalf("wrapped UNIQUE violation not classified: %v", uniqueErr)
	}

	_, pkErr := first.ExecContext(ctx, `INSERT INTO classify_unique_probe(id, code) VALUES (1, 'b')`)
	if pkErr == nil {
		t.Fatal("expected PRIMARY KEY constraint violation")
	}
	if !IsUniqueViolation(pkErr) {
		t.Fatalf("PRIMARY KEY violation not classified: %v", pkErr)
	}

	_, notNullErr := first.ExecContext(ctx, `INSERT INTO classify_unique_probe(id, code) VALUES (3, NULL)`)
	if notNullErr == nil {
		t.Fatal("expected NOT NULL constraint violation")
	}
	if IsUniqueViolation(notNullErr) {
		t.Fatalf("NOT NULL violation misclassified as unique: %v", notNullErr)
	}
}

func TestIsUniqueViolation_PostgresSQLState(t *testing.T) {
	t.Parallel()
	unique := pgdriver.NewError(map[byte]string{'C': "23505"})
	if !IsUniqueViolation(unique) {
		t.Fatal("SQLSTATE 23505 not classified")
	}
	if !IsUniqueViolation(fmt.Errorf("insert: %w", unique)) {
		t.Fatal("wrapped SQLSTATE 23505 not classified")
	}
	if IsUniqueViolation(pgdriver.NewError(map[byte]string{'C': "23503"})) {
		t.Fatal("foreign-key violation misclassified as unique")
	}
}

func TestIsUniqueViolation_NonMatches(t *testing.T) {
	t.Parallel()
	if IsUniqueViolation(nil) {
		t.Fatal("nil must not match")
	}
	if IsUniqueViolation(errors.New("constraint failed: generic")) {
		t.Fatal("plain constraint text must not match")
	}
	if IsUniqueViolation(errors.New("unique")) {
		t.Fatal("bare 'unique' text must not match")
	}
}

func TestIsSQLiteBusy_RealSQLiteBusy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "classify-busy.db")
	writer := openClassifyProbe(t, path)
	if _, err := writer.ExecContext(ctx, `CREATE TABLE classify_busy_probe(id INTEGER PRIMARY KEY, x TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := writer.ExecContext(ctx, `INSERT INTO classify_busy_probe(id, x) VALUES (1, 'seed')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	contender := openClassifyProbe(t, path)
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin writer tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO classify_busy_probe(id, x) VALUES (2, 'held')`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("writer insert: %v", err)
	}
	_, busyErr := contender.ExecContext(ctx, `INSERT INTO classify_busy_probe(id, x) VALUES (3, 'blocked')`)
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatalf("rollback writer tx: %v", rollbackErr)
	}
	if busyErr == nil {
		t.Fatal("expected SQLITE_BUSY from the second writer")
	}
	if !IsSQLiteBusy(busyErr) {
		t.Fatalf("real lock contention not classified: %v", busyErr)
	}
	if !IsSQLiteBusy(fmt.Errorf("append: %w", busyErr)) {
		t.Fatalf("wrapped lock contention not classified: %v", busyErr)
	}
}

func TestIsSQLiteBusy_NarrowPhraseFallback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"database locked", errors.New("database is locked (5) (SQLITE_BUSY)"), true},
		{"table locked", errors.New("database table is locked (6)"), true},
		{"wrapped locked", fmt.Errorf("append: %w", errors.New("database is locked")), true},
		{"sqlite_busy token only", errors.New("sqlite_busy"), false},
		{"pgx conn busy", errors.New("conn busy"), false},
		{"device busy", errors.New("open /data/store.db: device or resource busy"), false},
		{"constraint", errors.New("constraint failed"), false},
	}
	for _, tc := range cases {
		if got := IsSQLiteBusy(tc.err); got != tc.want {
			t.Errorf("%s: IsSQLiteBusy(%v)=%v want %v", tc.name, tc.err, got, tc.want)
		}
	}
}
