package modelcatalog_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
)

type fakeSnapshotSource struct {
	snap modelcatalog.Snapshot
	err  error
}

func (f *fakeSnapshotSource) Fetch(ctx context.Context) (modelcatalog.Snapshot, error) {
	_ = ctx
	return f.snap, f.err
}

type fakeSnapshotCache struct {
	snap modelcatalog.Snapshot
	errL error
	errS error
}

func (f *fakeSnapshotCache) Load(ctx context.Context) (modelcatalog.Snapshot, error) {
	_ = ctx
	return f.snap, f.errL
}

func (f *fakeSnapshotCache) Save(ctx context.Context, snapshot modelcatalog.Snapshot) error {
	_ = ctx
	_ = snapshot
	return f.errS
}

func TestSnapshotPorts_compileTimeChecks(t *testing.T) {
	t.Parallel()
	var _ modelcatalog.SnapshotSource = (*fakeSnapshotSource)(nil)
	var _ modelcatalog.SnapshotCache = (*fakeSnapshotCache)(nil)

	src := &fakeSnapshotSource{snap: modelcatalog.Snapshot{
		Generation:  "g1",
		FetchedAt:   time.Unix(1700000000, 0).UTC(),
		ContentHash: "sha256:abc",
		Index:       modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{"openai/gpt-4o": {}}),
	}}
	cache := &fakeSnapshotCache{snap: src.snap}

	ctx := context.Background()
	got, err := src.Fetch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Generation != "g1" {
		t.Fatal(got.Generation)
	}
	if _, err := cache.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cache.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotIndex_lookup(t *testing.T) {
	t.Parallel()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"anthropic/claude-3-5-sonnet": {Source: modelcatalog.FactSourceCatalog},
	})
	f, ok := idx.FactsByCatalogModelID("anthropic/claude-3-5-sonnet")
	if !ok || f.Source != modelcatalog.FactSourceCatalog {
		t.Fatalf("lookup: ok=%v facts=%+v", ok, f)
	}
	_, ok = idx.FactsByCatalogModelID("missing")
	if ok {
		t.Fatal("expected miss")
	}
}
