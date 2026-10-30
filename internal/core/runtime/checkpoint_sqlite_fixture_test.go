package runtime

import (
	"path/filepath"
	"testing"
)

func durableCheckpointSQLiteDSN(path string) string {
	// WAL avoids repeated rollback-journal creation while FULL still syncs every
	// commit. These fixtures retain real files and close/reopen durability proofs.
	return "file:" + filepath.ToSlash(path) + "?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"
}

func TestCheckpointFixtureUsesDurableWAL(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "checkpoint.sqlite")
	for _, phase := range []string{"initial", "reopened"} {
		store := r5c2bOpenFileStore(t, path, "fixture-durability")
		t.Cleanup(func() {
			_ = store.store.Close()
			_ = store.sqlDB.Close()
		})
		var mode string
		if err := store.sqlDB.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if mode != "wal" {
			t.Fatalf("%s journal mode=%q, want wal", phase, mode)
		}
		var synchronous int
		if err := store.sqlDB.QueryRowContext(t.Context(), "PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatal(err)
		}
		if synchronous != 2 {
			t.Fatalf("%s synchronous=%d, want FULL (2)", phase, synchronous)
		}
		store.close(t)
	}
}
