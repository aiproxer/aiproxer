package modelsdev_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/infra/modelcatalog/modelsdev"
)

func TestFileSnapshotStore_roundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	raw := []byte(`{"p":{"id":"p","models":[{"id":"m","tool_call":true}]}}`)
	s0, err := modelsdev.ParseSnapshot(raw, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	store := modelsdev.NewFileSnapshotStore(path)
	if err := store.Save(context.Background(), s0); err != nil {
		t.Fatal(err)
	}
	store2 := modelsdev.NewFileSnapshotStore(path)
	s1, err := store2.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s1.ContentHash != s0.ContentHash || s1.Generation != s0.Generation {
		t.Fatalf(
			"metadata mismatch: got hash=%q gen=%q want hash=%q gen=%q",
			s1.ContentHash, s1.Generation, s0.ContentHash, s0.Generation,
		)
	}
	if !s1.FetchedAt.Equal(s0.FetchedAt) {
		t.Fatalf("fetched_at: got %v want %v", s1.FetchedAt, s0.FetchedAt)
	}
	f, ok := s1.Index.FactsByCatalogModelID("p/m")
	if !ok || f.Tools != modelcatalog.CapabilitySupported {
		t.Fatalf("index round-trip: ok=%v facts=%+v", ok, f)
	}
}

func TestFileSnapshotStore_rejectCorrupt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := modelsdev.NewFileSnapshotStore(path)
	_, err := store.Load(context.Background())
	if err == nil {
		t.Fatal("expected error for corrupt file")
	}
}

func TestFileSnapshotStore_missingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nope.json")
	store := modelsdev.NewFileSnapshotStore(path)
	_, err := store.Load(context.Background())
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFileSnapshotStore_rejectHashMismatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "tamper.json")
	raw := []byte(`{"p":{"id":"p","models":[{"id":"m"}]}}`)
	snap, err := modelsdev.ParseSnapshot(raw, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := modelsdev.NewFileSnapshotStore(path).Save(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt stored file bytes without valid envelope structure — easiest: truncate
	if err := os.WriteFile(path, b[:len(b)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = modelsdev.NewFileSnapshotStore(path).Load(context.Background())
	if err == nil {
		t.Fatal("expected error for truncated cache")
	}
}
