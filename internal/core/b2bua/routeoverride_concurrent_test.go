package b2bua_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
)

func TestMemoryStore_concurrentSnapshotGetReplaceClear(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	leg, err := store.CreateALeg(ctx, "ov-conc-mix")
	if err != nil {
		t.Fatal(err)
	}

	const (
		readers = 8
		writers = 24
		rounds  = 40
	)
	selectors := []string{"openai:gpt-4", "anthropic:claude"}
	allowed := map[string]struct{}{
		"":           {},
		selectors[0]: {},
		selectors[1]: {},
	}

	var committedMu sync.Mutex
	committed := make([]routeoverride.State, 0, writers*rounds)

	recordCommitted := func(st routeoverride.State) {
		committedMu.Lock()
		committed = append(committed, st)
		committedMu.Unlock()
	}

	assertComplete := func(st routeoverride.State, op string) {
		t.Helper()
		if err := st.Validate(); err != nil {
			t.Errorf("%s: incomplete state %+v: %v", op, st, err)
			return
		}
		if st.ALegID != leg.ALegID {
			t.Errorf("%s: ALegID=%q want %q", op, st.ALegID, leg.ALegID)
		}
		if _, ok := allowed[st.Selector]; !ok {
			t.Errorf("%s: torn selector %q in %+v", op, st.Selector, st)
		}
		if st.Active && st.Selector == "" {
			t.Errorf("%s: active with empty selector: %+v", op, st)
		}
		if !st.Active && st.Selector != "" {
			t.Errorf("%s: inactive with selector: %+v", op, st)
		}
	}

	var wg sync.WaitGroup
	for range readers {
		wg.Go(func() {
			var lastRev int64
			for range rounds {
				snap, err := store.Snapshot(ctx, leg.ALegID)
				if err != nil {
					t.Errorf("Snapshot: %v", err)
					return
				}
				assertComplete(snap, "Snapshot")
				if snap.Revision < lastRev {
					t.Errorf("Snapshot revision went backwards: %d -> %d", lastRev, snap.Revision)
				}
				lastRev = snap.Revision

				got, err := store.Get(ctx, leg.ALegID)
				if err != nil {
					t.Errorf("Get: %v", err)
					return
				}
				assertComplete(got, "Get")
				if got.Revision < lastRev {
					t.Errorf("Get revision went backwards vs prior Snapshot: %d -> %d", lastRev, got.Revision)
				}
				lastRev = got.Revision
			}
		})
	}
	for w := range writers {
		wg.Go(func() {
			for r := range rounds {
				now := time.Unix(2_100_000_000, int64(w*rounds+r)).UTC()
				switch (w + r) % 5 {
				case 0, 1:
					st, err := store.Replace(ctx, leg.ALegID, selectors[(w+r)%2], now)
					if err != nil {
						t.Errorf("Replace: %v", err)
						return
					}
					assertComplete(st, "Replace")
					recordCommitted(st)
				case 2:
					st, err := store.Clear(ctx, leg.ALegID, now)
					if err != nil {
						t.Errorf("Clear: %v", err)
						return
					}
					assertComplete(st, "Clear")
					recordCommitted(st)
				default:
					st, err := store.Get(ctx, leg.ALegID)
					if err != nil {
						t.Errorf("writer Get: %v", err)
						return
					}
					assertComplete(st, "writer Get")
				}
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}

	final, err := store.Snapshot(ctx, leg.ALegID)
	if err != nil {
		t.Fatalf("final Snapshot: %v", err)
	}
	assertComplete(final, "final Snapshot")

	committedMu.Lock()
	defer committedMu.Unlock()
	if len(committed) == 0 {
		t.Fatal("expected committed Replace/Clear results")
	}
	var maxRev int64
	for _, st := range committed {
		if st.Revision > maxRev {
			maxRev = st.Revision
		}
	}
	if final.Revision != maxRev {
		t.Fatalf("final revision %d must equal highest committed revision %d (final=%+v)", final.Revision, maxRev, final)
	}
	matched := false
	for _, st := range committed {
		if st.Revision != maxRev {
			continue
		}
		if st != final {
			t.Fatalf("highest committed revision is torn vs final: committed=%+v final=%+v", st, final)
		}
		matched = true
	}
	if !matched {
		t.Fatalf("no committed state at revision %d; final=%+v", maxRev, final)
	}
}
