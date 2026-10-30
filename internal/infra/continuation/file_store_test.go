package continuation_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	infra "github.com/aiproxer/aiproxer/internal/infra/continuation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

func TestFileStoreSurvivesRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "continuation.json")
	scope := aipcont.Scope{TenantID: "tenant", PrincipalID: "principal", SessionID: "session"}
	ctx := context.Background()
	first, err := infra.NewFileStore(path, aipcont.DefaultStorageLimits())
	if err != nil {
		t.Fatal(err)
	}
	id, err := first.Reserve(ctx, scope, aipcont.StoragePolicy{Mode: aipcont.PersistencePersistent, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	record := aipcont.ContinuationRecord{ID: id, Scope: scope, Terminal: true, Policy: aipcont.StoragePolicy{Mode: aipcont.PersistencePersistent, TTL: time.Hour}}
	if err := first.PutTerminal(ctx, record); err != nil {
		t.Fatal(err)
	}
	second, err := infra.NewFileStore(path, aipcont.DefaultStorageLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := aipcont.Lookup(ctx, second, scope, id)
	if err != nil || got.ID != id {
		t.Fatalf("restart lookup=%v err=%v", got.ID, err)
	}
}

func TestFileStoreFailureIsNotReportedAsNotFound(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing", "continuation.json")
	_, err := infra.NewFileStore(path, aipcont.DefaultStorageLimits())
	if !errors.Is(err, aipcont.ErrStorageFailure) {
		t.Fatalf("constructor error=%v", err)
	}
}

func TestFileStoreHonorsContextAtIOBoundary(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "continuation.json")
	store, err := infra.NewFileStore(path, aipcont.DefaultStorageLimits())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.Reserve(ctx, aipcont.Scope{PrincipalID: "p"}, aipcont.StoragePolicy{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("reserve error=%v", err)
	}
}

func TestFileStoreRejectsSymlinkAndTraversalPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := infra.NewFileStore(link, aipcont.DefaultStorageLimits()); !errors.Is(err, aipcont.ErrStorageFailure) {
		t.Fatalf("symlink constructor error=%v", err)
	}
	traversal := dir + string(os.PathSeparator) + ".." + string(os.PathSeparator) + filepath.Base(dir) + string(os.PathSeparator) + "traversed.json"
	if _, err := infra.NewFileStore(traversal, aipcont.DefaultStorageLimits()); !errors.Is(err, aipcont.ErrStorageFailure) {
		t.Fatalf("traversal constructor error=%v", err)
	}
}

func TestFileStoreRejectsUnprotectedNativeReferences(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "continuation.json")
	store, err := infra.NewFileStore(path, aipcont.DefaultStorageLimits())
	if err != nil {
		t.Fatal(err)
	}
	scope := aipcont.Scope{PrincipalID: "p"}
	id, err := store.Reserve(context.Background(), scope, aipcont.StoragePolicy{Mode: aipcont.PersistencePersistent})
	if err != nil {
		t.Fatal(err)
	}
	err = store.PutTerminal(context.Background(), aipcont.ContinuationRecord{
		ID: id, Scope: scope, Terminal: true,
		NativeRefs: []aipcont.NativeReference{{Provider: "p", ID: "native"}},
	})
	if !errors.Is(err, aipcont.ErrNativeReferencesUnprotected) {
		t.Fatalf("native refs error=%v", err)
	}
}

func TestFileStoreRejectsCorruptJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "continuation.json")
	if err := os.WriteFile(path, []byte(`{"records":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := infra.NewFileStore(path, aipcont.DefaultStorageLimits()); !errors.Is(err, aipcont.ErrStorageFailure) {
		t.Fatalf("corrupt constructor error=%v", err)
	}
}

func TestFileStoreSerializesIndependentInstances(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "continuation.json")
	first, err := infra.NewFileStore(path, aipcont.DefaultStorageLimits())
	if err != nil {
		t.Fatal(err)
	}
	second, err := infra.NewFileStore(path, aipcont.DefaultStorageLimits())
	if err != nil {
		t.Fatal(err)
	}
	scope := aipcont.Scope{PrincipalID: "p"}
	ctx := context.Background()
	var wg sync.WaitGroup
	ids := make(chan aipcont.ResponseID, 20)
	for i := range 20 {
		store := first
		if i%2 == 1 {
			store = second
		}
		wg.Go(func() {
			id, reserveErr := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
			if reserveErr != nil {
				t.Errorf("reserve: %v", reserveErr)
				return
			}
			ids <- id
		})
	}
	wg.Wait()
	close(ids)
	seen := make(map[aipcont.ResponseID]struct{})
	for id := range ids {
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != 20 {
		t.Fatalf("reserved %d IDs", len(seen))
	}
}

func TestFileStoreKeepsScopesAndReturnedRecordsIndependent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "continuation.json")
	store, err := infra.NewFileStore(path, aipcont.DefaultStorageLimits())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scope := aipcont.Scope{TenantID: "tenant", PrincipalID: "p", SessionID: "s"}
	id, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	record := aipcont.ContinuationRecord{
		ID: id, Scope: scope, Terminal: true,
		InputItems: []aipapi.Item{{ID: "input"}},
	}
	if err := store.PutTerminal(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.InputItems[0].ID = "changed-after-put"
	got, err := store.Get(ctx, scope, id)
	if err != nil {
		t.Fatal(err)
	}
	got.InputItems[0].ID = "changed-after-get"
	gotAgain, err := store.Get(ctx, scope, id)
	if err != nil {
		t.Fatal(err)
	}
	if gotAgain.InputItems[0].ID != "input" {
		t.Fatalf("durable record was aliased: %q", gotAgain.InputItems[0].ID)
	}
	if _, err := aipcont.Lookup(ctx, store, aipcont.Scope{TenantID: "other", PrincipalID: "p", SessionID: "s"}, id); !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("cross-scope lookup=%v", err)
	}
}
