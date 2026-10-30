package filestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
)

// seamedStore replaces the durability primitives so tests prove publish
// ordering and failure handling without mutating package state.
func seamedStore(path string, fileSync func(*os.File) error, dirSync func(string) error) *Store {
	return &Store{path: path, fileSync: fileSync, dirSync: dirSync}
}

func snapshot(generation string) modelregistry.Snapshot {
	return modelregistry.Snapshot{Generation: generation}
}

func TestStoreSaveFlushesFileBeforePublishAndDirectoryAfter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	var order []string
	fileSync := func(f *os.File) error {
		order = append(order, "sync-file")
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("target must not exist while the temp file is flushed; stat err = %v", err)
		}
		return f.Sync()
	}
	dirSync := func(got string) error {
		order = append(order, "sync-dir")
		if got != dir {
			t.Errorf("directory sync path = %q, want %q", got, dir)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("target must be published before the directory flush: %v", err)
		}
		return nil
	}

	if err := seamedStore(path, fileSync, dirSync).Save(context.Background(), snapshot("gen-1")); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "sync-file" || order[1] != "sync-dir" {
		t.Fatalf("durability order = %v, want [sync-file sync-dir]", order)
	}
	loaded, err := New(path).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Generation != "gen-1" {
		t.Fatalf("published generation = %q, want gen-1", loaded.Generation)
	}
}

func TestStoreSaveFailsClosedWhenFileFlushFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if err := New(path).Save(context.Background(), snapshot("gen-1")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	syncErr := errors.New("file sync failed")
	store := seamedStore(path, func(*os.File) error { return syncErr }, func(string) error {
		t.Error("directory flush must not run after a failed file flush")
		return nil
	})
	if err := store.Save(context.Background(), snapshot("gen-2")); !errors.Is(err, syncErr) {
		t.Fatalf("Save error = %v, want %v", err, syncErr)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("a failed file flush must leave the previous snapshot untouched")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("a failed save must remove its temp file; entries = %v", entries)
	}
}

func TestStoreSaveSurfacesDirectoryFlushFailure(t *testing.T) {
	t.Parallel()

	syncErr := errors.New("directory sync failed")
	path := filepath.Join(t.TempDir(), "models.json")
	store := seamedStore(path, func(f *os.File) error { return f.Sync() }, func(string) error { return syncErr })
	if err := store.Save(context.Background(), snapshot("gen-1")); !errors.Is(err, syncErr) {
		t.Fatalf("Save error = %v, want %v", err, syncErr)
	}
	if _, err := New(path).Load(context.Background()); err != nil {
		t.Fatalf("published snapshot must stay loadable: %v", err)
	}
}
