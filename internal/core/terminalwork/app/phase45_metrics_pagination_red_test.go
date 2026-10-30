package app_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestPhase45_MetricsSnapshotPaginatesBeyondOnePage(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 7, 0, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID:         "m-page-300",
		DefaultPageSize: 256,
		MaxPageSize:     256,
		Now:             func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	const n = 300
	for i := range n {
		rec := terminalwork.WorkRecord{
			WorkID:         fmt.Sprintf("tw_page_%04d", i),
			SourceKey:      terminalwork.SourceKey{IdentityVersion: 1, Key: fmt.Sprintf("sk_page_%04d", i)},
			PayloadVersion: 1,
			Kind:           sdk.WorkKindSettleRequestProvider,
			State:          sdk.WorkStateIntent,
			ProviderID:     "quota",
			Lifecycle:      terminalwork.LifecycleCorrelation{RequestID: fmt.Sprintf("req-p-%d", i)},
			CreatedAt:      clock,
			UpdatedAt:      clock,
		}
		if err := store.AppendIntent(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
		if err := store.PromotePending(context.Background(), terminalwork.PromotePendingCommand{
			WorkID: rec.WorkID,
			Now:    clock,
		}); err != nil {
			t.Fatal(err)
		}
	}
	obs := app.NewMetricsObserver(store, app.MetricsConfig{Clock: func() time.Time { return clock.Add(time.Hour) }})
	snap, err := obs.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Pending != n || snap.Backlog != n {
		t.Fatalf("pending=%d backlog=%d want %d (must paginate past MaxPageSize=256)", snap.Pending, snap.Backlog, n)
	}
}

func TestPhase45_MetricsSnapshotDetectsCursorCycle(t *testing.T) {
	t.Parallel()
	obs := app.NewMetricsObserver(cyclingQueryStore{}, app.MetricsConfig{})
	_, err := obs.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected cursor-cycle/fault error")
	}
	if !errors.Is(err, app.ErrMetricsCursorFault) {
		t.Fatalf("err=%v want ErrMetricsCursorFault", err)
	}
}

type cyclingQueryStore struct{}

func (cyclingQueryStore) List(_ context.Context, q terminalwork.ListQuery) (terminalwork.ListPage, error) {
	// Always returns a non-empty page and the same next cursor → cycle.
	rec := terminalwork.WorkRecord{
		WorkID:     "tw_cycle",
		SourceKey:  terminalwork.SourceKey{IdentityVersion: 1, Key: "sk_cycle"},
		Kind:       sdk.WorkKindSettleRequestProvider,
		State:      sdk.WorkStatePending,
		ProviderID: "quota",
		Lifecycle:  terminalwork.LifecycleCorrelation{RequestID: "req-cycle"},
	}
	return terminalwork.ListPage{Records: []terminalwork.WorkRecord{rec}, Cursor: "stuck"}, nil
}
