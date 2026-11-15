package sqlitefixture

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestFileDSNConfiguresSQLite(t *testing.T) {
	t.Parallel()

	dsn, err := FileDSN(filepath.Join(t.TempDir(), "fixture with spaces.db"))
	if err != nil {
		t.Fatal(err)
	}
	// Callers that require a transaction mode can add it without losing the
	// helper's pragmas.
	db, err := sql.Open("sqlite", dsn+"&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var journal string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal_mode = %q, err = %v; want wal", journal, err)
	}
	for pragma, want := range map[string]int{"synchronous": 1, "foreign_keys": 1, "busy_timeout": 5000} {
		var got int
		if err := db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != want {
			t.Errorf("%s = %d, err = %v; want %d", pragma, got, err, want)
		}
	}
}

func TestFileDSNRejectsUnsafePath(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"", "  ", ":memory:", "fixture.db?mode=memory", "a#b", "a&b", "a\x00b"} {
		if _, err := FileDSN(path); err == nil {
			t.Errorf("FileDSN(%q) accepted a non-file or unsafe path", path)
		}
	}
}
