package b2bua

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
)

func TestMemoryStore_TTL_evictsRouteOverrideWithALeg(t *testing.T) {
	t.Parallel()
	t0 := time.Unix(1_710_000_000, 0).UTC()
	tick := t0
	s, err := NewMemoryStore(MemoryStoreOptions{
		TTL: time.Hour,
		Now: func() time.Time { return tick },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	leg, err := s.CreateALeg(ctx, "ttl-ov")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(ctx, leg.ALegID, "openai:gpt-4", t0); err != nil {
		t.Fatal(err)
	}
	tick = t0.Add(2 * time.Hour)
	if _, err := s.Snapshot(ctx, leg.ALegID); !errors.Is(err, routeoverride.ErrNotFound) {
		t.Fatalf("expired A-leg override: got %v want %v", err, routeoverride.ErrNotFound)
	}
}

func TestMemoryStore_maxLegs_evictsOverrideWithOldestALeg(t *testing.T) {
	t.Parallel()
	t0 := time.Unix(1_710_000_100, 0).UTC()
	tick := t0
	s, err := NewMemoryStore(MemoryStoreOptions{
		MaxLegs: 2,
		Now:     func() time.Time { return tick },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	oldest, err := s.CreateALeg(ctx, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(ctx, oldest.ALegID, "openai:gpt-4", t0); err != nil {
		t.Fatal(err)
	}
	tick = t0.Add(time.Second)
	if _, err := s.CreateALeg(ctx, "k2"); err != nil {
		t.Fatal(err)
	}
	tick = t0.Add(2 * time.Second)
	if _, err := s.CreateALeg(ctx, "k3"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(ctx, oldest.ALegID); !errors.Is(err, routeoverride.ErrNotFound) {
		t.Fatalf("evicted A-leg override: got %v want %v", err, routeoverride.ErrNotFound)
	}
}

func TestMemoryStore_newInstanceDoesNotRetainOverride(t *testing.T) {
	t.Parallel()
	s1, err := NewMemoryStore(MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	leg, err := s1.CreateALeg(ctx, "ov-mem-restart")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Replace(ctx, leg.ALegID, "openai:gpt-4", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	s2, err := NewMemoryStore(MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Snapshot(ctx, leg.ALegID); !errors.Is(err, routeoverride.ErrNotFound) {
		t.Fatalf("memory continuity makes no restart durability claim: got %v", err)
	}
}

func TestMemoryStore_recreateContinuityKeyDoesNotInheritOverride(t *testing.T) {
	t.Parallel()
	s, err := NewMemoryStore(MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	leg, err := s.CreateALeg(ctx, "ov-mem-recreate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(ctx, leg.ALegID, "openai:gpt-4", time.Unix(1, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.CreateALeg(ctx, "ov-mem-recreate")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ALegID == leg.ALegID {
		t.Fatal("recreate must allocate a new A-leg")
	}
	if _, err := s.Snapshot(ctx, leg.ALegID); !errors.Is(err, routeoverride.ErrNotFound) {
		t.Fatalf("old A-leg Snapshot: %v", err)
	}
	got, err := s.Snapshot(ctx, fresh.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Active || got.Revision != 0 {
		t.Fatalf("new A-leg must not inherit override: %+v", got)
	}
}
