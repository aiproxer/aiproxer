package b2bua

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Orphan A-legs (e.g. anonymous CreateALeg) must be reclaimable via TTL without a
// later FetchALeg/Resolve touch; sweep on CreateALeg provides that path.
func TestMemoryStore_TTL_sweepsStaleAnonymousLegsOnCreate(t *testing.T) {
	t.Parallel()
	t0 := time.Unix(1700000000, 0).UTC()
	tick := t0
	s, err := NewMemoryStore(MemoryStoreOptions{
		TTL: time.Hour,
		Now: func() time.Time { return tick },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const n = 10
	for range n {
		if _, err := s.CreateALeg(ctx, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(s.legs); got != n {
		t.Fatalf("before idle: want %d legs, got %d", n, got)
	}
	tick = t0.Add(2 * time.Hour)
	if _, err := s.CreateALeg(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got := len(s.legs); got != 1 {
		t.Fatalf("after sweep+create: want 1 leg, got %d", got)
	}
}

func TestMemoryStore_TTL_sweepsStaleUniqueContinuityKeysOnCreate(t *testing.T) {
	t.Parallel()
	t0 := time.Unix(1700000100, 0).UTC()
	tick := t0
	s, err := NewMemoryStore(MemoryStoreOptions{
		TTL: time.Hour,
		Now: func() time.Time { return tick },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const n = 8
	for i := range n {
		key := fmt.Sprintf("once-%d", i)
		if _, err := s.CreateALeg(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(s.legs); got != n {
		t.Fatalf("before idle: want %d legs, got %d", n, got)
	}
	tick = t0.Add(2 * time.Hour)
	if _, err := s.CreateALeg(ctx, "fresh-after-idle"); err != nil {
		t.Fatal(err)
	}
	if got := len(s.legs); got != 1 {
		t.Fatalf("after sweep+create: want 1 leg, got %d", got)
	}
}

func TestMemoryStore_defaultMaxLegs_evictsOldestWhenTTLDisabled(t *testing.T) {
	t.Parallel()
	t0 := time.Unix(1711000000, 0).UTC()
	tick := t0
	s, err := NewMemoryStore(MemoryStoreOptions{
		TTL: 0,
		Now: func() time.Time { return tick },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	const capacity = 5
	s.maxLegs = capacity

	for i := range capacity + 2 {
		tick = t0.Add(time.Duration(i) * time.Millisecond)
		if _, err := s.CreateALeg(ctx, fmt.Sprintf("u-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(s.legs); got != capacity {
		t.Fatalf("want %d legs after overflow create, got %d", capacity, got)
	}
}

func TestMemoryStore_zeroTTL_doesNotSweepOnCreate(t *testing.T) {
	t.Parallel()
	t0 := time.Unix(1700000200, 0).UTC()
	tick := t0
	s, err := NewMemoryStore(MemoryStoreOptions{
		TTL: 0,
		Now: func() time.Time { return tick },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.CreateALeg(ctx, ""); err != nil {
		t.Fatal(err)
	}
	tick = t0.Add(48 * time.Hour)
	if _, err := s.CreateALeg(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got := len(s.legs); got != 2 {
		t.Fatalf("TTL disabled: want 2 legs retained, got %d", got)
	}
}
