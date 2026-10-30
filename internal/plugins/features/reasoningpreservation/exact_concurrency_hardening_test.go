package reasoningpreservation_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

func TestTurnStore_exactObserver_sessionIsolationConcurrent(t *testing.T) {
	t.Parallel()
	cfg := observeExactConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	const n = 32
	parts := make([]*aipapi.ReasoningPart, n)
	obs := make([]response.StreamObserver, n)
	for i := range n {
		parts[i] = exactResponsesPart(t, fmt.Sprintf("rs_%d", i))
		obs[i] = openExactObserver(t, cfg, store, fmt.Sprintf("sess-conc-%d", i), nil)
	}
	errCh := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_ = obs[i].Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: parts[i]})
			_ = obs[i].Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
			if err := obs[i].Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
				errCh <- fmt.Errorf("Finish sess-conc-%d: %w", i, err)
			}
		})
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	for i := range n {
		sess := fmt.Sprintf("sess-conc-%d", i)
		snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
		if err != nil {
			t.Fatalf("Snapshot %s: %v", sess, err)
		}
		if len(snap) != 1 {
			t.Fatalf("session %s artifact_count=%d want 1", sess, len(snap))
		}
	}
}
