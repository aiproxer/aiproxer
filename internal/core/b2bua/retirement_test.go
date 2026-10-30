package b2bua

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStoreLoadAttemptsNotifiesStaleRetirement(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0).UTC()
	store, err := NewMemoryStore(MemoryStoreOptions{TTL: time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateALeg(context.Background(), "stale")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	retired := make(chan string, 1)
	store.SetALegRetirementObserver(func(aLegID string) { retired <- aLegID })
	if _, err := store.LoadAttempts(context.Background(), first.ALegID); err != ErrALegNotFound {
		t.Fatalf("LoadAttempts error = %v, want %v", err, ErrALegNotFound)
	}
	select {
	case got := <-retired:
		if got != first.ALegID {
			t.Fatalf("retired A-leg = %q, want %q", got, first.ALegID)
		}
	case <-time.After(time.Second):
		t.Fatal("stale LoadAttempts did not notify retirement")
	}
}

func TestMemoryStoreRetirementObserverRunsAfterEvictionUnlock(t *testing.T) {
	t.Parallel()
	tick := time.Unix(1_700_000_000, 0).UTC()
	store, err := NewMemoryStore(MemoryStoreOptions{MaxLegs: 1, Now: func() time.Time { return tick }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := store.CreateALeg(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	tick = tick.Add(time.Second)
	called := make(chan struct{})
	store.SetALegRetirementObserver(func(aLegID string) {
		if aLegID != first.ALegID {
			t.Errorf("retired A-leg = %q, want %q", aLegID, first.ALegID)
		}
		// Re-entering the store would deadlock if the callback still ran under
		// the mutation lock.
		store.SetALegRetirementObserver(nil)
		close(called)
	})
	if _, err := store.CreateALeg(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("retirement callback did not complete outside the store lock")
	}
}
