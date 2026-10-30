package filestore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/infra/modelregistry/filestore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestStoreRoundTripSnapshot(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "models.json")
	store := filestore.New(path)
	want := modelregistry.Snapshot{
		Generation:  "gen-1",
		RefreshedAt: time.Unix(1000, 0).UTC(),
		Models: []modelregistry.BackendModel{{
			CanonicalID: "openai/gpt-4o-mini",
			NativeID:    "gpt-4o-mini",
			DisplayName: "GPT-4o mini",
			BackendID:   "openai",
			Kind:        "openai-responses",
			Source:      modelinventory.SourceRemote,
			LoadedAt:    time.Unix(900, 0).UTC(),
		}},
	}

	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Generation != want.Generation || !got.RefreshedAt.Equal(want.RefreshedAt) {
		t.Fatalf("snapshot metadata = %+v, want %+v", got, want)
	}
	if len(got.Models) != 1 || got.Models[0].CanonicalID != "openai/gpt-4o-mini" {
		t.Fatalf("models = %+v", got.Models)
	}
}

func TestStoreLoadMissingReturnsSnapshotUnavailable(t *testing.T) {
	t.Parallel()

	_, err := filestore.New(filepath.Join(t.TempDir(), "missing.json")).Load(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if err != modelregistry.ErrSnapshotUnavailable {
		t.Fatalf("error = %v, want ErrSnapshotUnavailable", err)
	}
}

func TestStoreLoadRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := filestore.New(path).Load(context.Background()); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestStoreSaveCreatesParentDirectory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "models.json")
	store := filestore.New(path)
	if err := store.Save(context.Background(), modelregistry.Snapshot{
		Generation:  "gen",
		RefreshedAt: time.Unix(1, 0).UTC(),
		Models: []modelregistry.BackendModel{{
			CanonicalID: "openai/gpt-4o",
			NativeID:    "gpt-4o",
			BackendID:   "openai",
			Kind:        "openai-responses",
			Source:      modelinventory.SourceRemote,
			LoadedAt:    time.Unix(1, 0).UTC(),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat saved file: %v", err)
	}
}
