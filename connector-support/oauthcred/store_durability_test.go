package oauthcred

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withSyncSeams records durability primitive calls and restores the production
// primitives when the test ends. Tests using it must not call t.Parallel:
// parallel siblings could observe the swapped variables.
func withSyncSeams(t *testing.T) *[]string {
	t.Helper()
	origFile, origDir := fileSync, dirSync
	t.Cleanup(func() { fileSync, dirSync = origFile, origDir })
	var recorded []string
	fileSync = func(f *os.File) error { recorded = append(recorded, "file"); return origFile(f) }
	dirSync = func(dir string) error { recorded = append(recorded, "dir"); return origDir(dir) }
	return &recorded
}

func seedStore(t *testing.T, token string) *FileStore {
	t.Helper()
	store := NewFileStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err := store.Save(TokenRecord{AccessToken: token}); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	return store
}

// TestFileStore_SaveSyncsBeforePublishThenDirectory pins the crash-safe write
// order: token bytes hit stable storage before the rename publishes them, and
// the directory entry is flushed after the rename.
//
//nolint:paralleltest // swaps the package-level fileSync/dirSync durability seams; parallel siblings would race.
func TestFileStore_SaveSyncsBeforePublishThenDirectory(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "tokens.json"))
	calls := withSyncSeams(t)
	if err := store.Save(TokenRecord{AccessToken: "tok"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := strings.Join(*calls, ","); got != "file,dir" {
		t.Fatalf("durability calls %q, want %q", got, "file,dir")
	}
}

// TestFileStore_SaveFailsClosedWhenFileSyncFails proves a failed data flush is
// reported and nothing is published, so a caller never treats a torn write as
// a successful refresh.
//
//nolint:paralleltest // swaps the package-level fileSync/dirSync durability seams; parallel siblings would race.
func TestFileStore_SaveFailsClosedWhenFileSyncFails(t *testing.T) {
	store := seedStore(t, "durable")
	withSyncSeams(t)
	fileSync = func(*os.File) error { return errors.New("simulated data flush failure") }

	err := store.Save(TokenRecord{AccessToken: "torn"})
	if err == nil || !strings.Contains(err.Error(), "sync temp token file") {
		t.Fatalf("save with failing file sync = %v, want sync temp token file error", err)
	}
	loaded, loadErr := store.Load()
	if loadErr != nil {
		t.Fatalf("previous record unreadable after failed save: %v", loadErr)
	}
	if loaded.AccessToken != "durable" {
		t.Fatalf("failed save published credentials: %q", loaded.AccessToken)
	}
}

// TestFileStore_SaveReportsDirectorySyncFailureAfterPublish proves the
// directory flush runs after the rename and its failure reaches the caller
// instead of being swallowed.
//
//nolint:paralleltest // swaps the package-level fileSync/dirSync durability seams; parallel siblings would race.
func TestFileStore_SaveReportsDirectorySyncFailureAfterPublish(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "tokens.json"))
	withSyncSeams(t)
	dirSync = func(string) error { return errors.New("simulated directory flush failure") }

	err := store.Save(TokenRecord{AccessToken: "published"})
	if err == nil || !strings.Contains(err.Error(), "sync token directory") {
		t.Fatalf("save with failing directory sync = %v, want sync token directory error", err)
	}
	loaded, loadErr := store.Load()
	if loadErr != nil || loaded.AccessToken != "published" {
		t.Fatalf("record not published before directory flush: %+v, %v", loaded, loadErr)
	}
}

// TestFileStore_DeleteFlushesDirectoryOnce proves logout makes the removal
// durable and stays idempotent without claiming a change when nothing existed.
//
//nolint:paralleltest // swaps the package-level fileSync/dirSync durability seams; parallel siblings would race.
func TestFileStore_DeleteFlushesDirectoryOnce(t *testing.T) {
	store := seedStore(t, "tok")
	calls := withSyncSeams(t)
	if err := store.Delete(); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := strings.Join(*calls, ","); got != "dir" {
		t.Fatalf("delete durability calls %q, want %q", got, "dir")
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("load after delete returned nil error, want missing record")
	}
	*calls = nil
	if err := store.Delete(); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("idempotent delete performed durability calls %v, want none", *calls)
	}
}

// TestFileStore_SavePublishesByRename proves Save installs a newly written
// file instead of truncating the live credential file in place: in-place
// writes can leave partial credentials visible after a crash.
func TestFileStore_SavePublishesByRename(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("file identity comparison is not meaningful on Windows")
	}
	store := seedStore(t, "first")
	before, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	if err := store.Save(TokenRecord{AccessToken: "second"}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	after, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if os.SameFile(before, after) {
		t.Fatal("Save rewrote the credential file in place; a crash mid-write could publish partial credentials")
	}
}
