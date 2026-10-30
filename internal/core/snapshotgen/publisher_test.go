package snapshotgen_test

import (
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

func TestPublisher_AtomicPublishAndPreserveOnDegrade(t *testing.T) {
	t.Parallel()
	p := snapshotgen.NewPublisher()
	if p.Current() != nil {
		t.Fatal("expected nil before publish")
	}
	g1 := p.Publish(snapshotgen.RuntimeGeneration{
		State: economics.SnapshotReady,
		Usage: economics.Snapshot[economics.PolicyRulesView]{
			ID: "usage_authority", Version: "v1", State: economics.SnapshotReady,
			Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
		},
		Concurrency: economics.Snapshot[economics.PolicyRulesView]{
			ID: "concurrency", Version: "c1", State: economics.SnapshotReady,
			Value: economics.PolicyRulesView{Kind: economics.PolicyKindConcurrency},
		},
		Rating: economics.Snapshot[economics.RatingCatalogView]{
			ID: "rating", Version: "r1", State: economics.SnapshotReady,
			Value: economics.RatingCatalogView{CatalogVersion: "r1", Currency: "USD"},
		},
	})
	if g1 == nil || g1.ID != 1 || g1.Usage.Version != "v1" {
		t.Fatalf("g1=%+v", g1)
	}
	held := p.Current()
	g2 := p.Publish(snapshotgen.RuntimeGeneration{
		State: economics.SnapshotReady,
		Usage: economics.Snapshot[economics.PolicyRulesView]{
			ID: "usage_authority", Version: "v2", State: economics.SnapshotReady,
			Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
		},
	})
	if held.Usage.Version != "v1" {
		t.Fatalf("in-flight generation mutated: %+v", held)
	}
	if g2.Usage.Version != "v2" || g2.ID <= g1.ID {
		t.Fatalf("g2=%+v", g2)
	}
	degraded := p.MarkUnusable(economics.SnapshotDegraded, "refresh_failed")
	if degraded.State != economics.SnapshotDegraded || degraded.Usage.Version != "v2" {
		t.Fatalf("degrade must keep values: %+v", degraded)
	}
	if p.MarkUnusable(economics.SnapshotState("bogus"), "x") != p.Current() {
		t.Fatal("unknown state must not replace generation")
	}
}

func TestPublisher_ConcurrentReadersStable(t *testing.T) {
	t.Parallel()
	p := snapshotgen.NewPublisher()
	p.Publish(snapshotgen.RuntimeGeneration{
		Usage: economics.Snapshot[economics.PolicyRulesView]{Version: "v1", State: economics.SnapshotReady},
	})
	var wg sync.WaitGroup
	errCh := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			for range 100 {
				cur := p.Current()
				if cur == nil || cur.Usage.Version == "" {
					errCh <- "nil/empty"
					return
				}
				_ = cur.PublishedAt
			}
		})
	}
	for range 20 {
		p.Publish(snapshotgen.RuntimeGeneration{
			PublishedAt: time.Now().UTC(),
			Usage: economics.Snapshot[economics.PolicyRulesView]{
				Version: "v-new", State: economics.SnapshotReady,
			},
		})
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Fatal(msg)
	}
}
