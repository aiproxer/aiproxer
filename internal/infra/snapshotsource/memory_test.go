package snapshotsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/snapshotsource"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

func TestMemoryRuleSource_PublishDoesNotMutatePriorSnapshot(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	src := snapshotsource.NewMemoryRuleSource(economics.Snapshot[economics.PolicyRulesView]{
		ID: "concurrency", Version: "v1", FetchedAt: now, State: economics.SnapshotReady,
		Value: economics.PolicyRulesView{Kind: economics.PolicyKindConcurrency, Payload: []byte(`{"v":1}`)},
	})
	first, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	src.Publish(economics.Snapshot[economics.PolicyRulesView]{
		ID: "concurrency", Version: "v2", FetchedAt: now, State: economics.SnapshotReady,
		Value: economics.PolicyRulesView{Kind: economics.PolicyKindConcurrency, Payload: []byte(`{"v":2}`)},
	})
	if first.Version != "v1" || string(first.Value.Payload) != `{"v":1}` {
		t.Fatalf("prior snapshot mutated: %+v", first)
	}
	second, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != "v2" {
		t.Fatalf("second=%+v", second)
	}
}

func TestStaticRatingFromCatalog(t *testing.T) {
	t.Parallel()
	snap := snapshotsource.StaticRatingFromCatalog("rating", "cat-9", "USD", time.Unix(100, 0).UTC())
	if snap.State != economics.SnapshotReady || snap.Version != "cat-9" || snap.Value.Currency != "USD" {
		t.Fatalf("snap=%+v", snap)
	}
	src := snapshotsource.NewMemoryRatingSource(snap)
	got, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "cat-9" {
		t.Fatalf("got=%+v", got)
	}
}
