package modelcatalog_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

func snapshotJSON(t *testing.T, raw string, ts time.Time) modelcatalog.Snapshot {
	t.Helper()
	return testkit.ModelsDevCatalogSnapshot(t, raw, ts)
}

type memSource struct {
	mu    sync.Mutex
	snaps []modelcatalog.Snapshot
	errs  []error
	call  int
}

func (m *memSource) Fetch(context.Context) (modelcatalog.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.call
	m.call++
	if i < len(m.errs) && m.errs[i] != nil {
		return modelcatalog.Snapshot{}, m.errs[i]
	}
	if i < len(m.snaps) {
		return m.snaps[i], nil
	}
	return modelcatalog.Snapshot{}, errors.New("memSource: exhausted")
}

type memCache struct {
	mu      sync.Mutex
	onSave  []modelcatalog.Snapshot
	loadErr error
	loadRet modelcatalog.Snapshot
}

func (m *memCache) Load(context.Context) (modelcatalog.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return modelcatalog.Snapshot{}, m.loadErr
	}
	return m.loadRet, nil
}

func (m *memCache) Save(_ context.Context, s modelcatalog.Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onSave = append(m.onSave, s)
	return nil
}

func TestCatalogRuntime_refreshSuccessUpdatesActive(t *testing.T) {
	t.Parallel()
	s1 := snapshotJSON(t, `{"a":{"id":"a","models":[{"id":"m1"}]}}`, time.Unix(1, 0))
	s2 := snapshotJSON(t, `{"a":{"id":"a","models":[{"id":"m2","tool_call":true}]}}`, time.Unix(2, 0))
	src := &memSource{snaps: []modelcatalog.Snapshot{s1, s2}}
	cache := &memCache{loadErr: errors.New("no cache")}

	rt := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{
		Source: src,
		Cache:  cache,
	})
	ctx := context.Background()
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	rt.RunRefresh(ctx)
	rt.RunRefresh(ctx)
	active, ok := rt.Active()
	if !ok {
		t.Fatal("expected active snapshot")
	}
	if _, ok := active.Index.FactsByCatalogModelID("a/m2"); !ok {
		t.Fatalf("expected refreshed model a/m2, have keys from %+v", active)
	}
	idxAI, refAI := rt.ActiveIndex()
	if idxAI != active.Index || refAI.Generation != active.Generation {
		t.Fatalf("ActiveIndex: ptr match=%v ref=%q active gen=%q", idxAI == active.Index, refAI.Generation, active.Generation)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if len(cache.onSave) < 1 {
		t.Fatalf("expected at least one Save, got %d", len(cache.onSave))
	}
}

func TestCatalogRuntime_fetchFailureKeepsPrior(t *testing.T) {
	t.Parallel()
	s1 := snapshotJSON(t, `{"b":{"id":"b","models":[{"id":"x"}]}}`, time.Unix(1, 0))
	src := &memSource{
		snaps: []modelcatalog.Snapshot{s1},
		errs:  []error{nil, errors.New("boom")},
	}
	cache := &memCache{loadErr: errors.New("no cache")}

	rt := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{
		Source: src,
		Cache:  cache,
	})
	ctx := context.Background()
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	rt.RunRefresh(ctx)
	rt.RunRefresh(ctx)
	active, ok := rt.Active()
	if !ok {
		t.Fatal("expected active snapshot")
	}
	if _, ok := active.Index.FactsByCatalogModelID("b/x"); !ok {
		t.Fatal("expected first snapshot to remain")
	}
	_ = rt.Close()
}

func TestCatalogRuntime_activeNonBlocking(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	src := &blockingSource{release: block}
	cache := &memCache{loadErr: errors.New("no cache")}
	rt := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{
		Source: src,
		Cache:  cache,
	})
	ctx := context.Background()
	// Start must return without waiting on Source.Fetch; refresh is explicit RunRefresh only.
	// If Start ever regresses into a blocking refresh, fail fast instead of hanging until -timeout.
	startErr := make(chan error, 1)
	go func() { startErr <- rt.Start(ctx) }()
	select {
	case err := <-startErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start blocked (must not await RunRefresh/network fetch)")
	}
	doneFetch := make(chan struct{})
	go func() {
		rt.RunRefresh(ctx)
		close(doneFetch)
	}()
	done := make(chan struct{})
	go func() {
		for range 50 {
			_, _ = rt.Active()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Active should not block while fetch waits")
	}
	close(block)
	<-doneFetch
	_ = rt.Close()
}

type blockingSource struct {
	release <-chan struct{}
}

func (b *blockingSource) Fetch(ctx context.Context) (modelcatalog.Snapshot, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
		return modelcatalog.Snapshot{}, ctx.Err()
	}
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"c/z": {},
	})
	return modelcatalog.Snapshot{
		Generation: "g",
		FetchedAt:  time.Unix(0, 0).UTC(),
		Index:      idx,
	}, nil
}
