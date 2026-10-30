package b2bua_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/testkit/b2buatest"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func assertOpaqueALegID(t *testing.T, id string) {
	t.Helper()
	if len(id) != 2+32 || !strings.HasPrefix(id, "a_") {
		t.Fatalf("unexpected A-leg id shape: %q", id)
	}
}

func assertOpaqueBLegID(t *testing.T, id string) {
	t.Helper()
	if len(id) != 2+32 || !strings.HasPrefix(id, "b_") {
		t.Fatalf("unexpected B-leg id shape: %q", id)
	}
}

func TestMemoryStore_Resolve_requiresNonEmptyKey(t *testing.T) {
	t.Parallel()
	s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = s.ResolveALeg(ctx, "")
	if !errors.Is(err, b2bua.ErrInvalidContinuityKey) {
		t.Fatalf("Resolve empty key: got %v want %v", err, b2bua.ErrInvalidContinuityKey)
	}
}

func TestMemoryStore_Resolve_notFound(t *testing.T) {
	t.Parallel()
	s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = s.ResolveALeg(ctx, "unknown-session")
	if !errors.Is(err, b2bua.ErrALegNotFound) {
		t.Fatalf("got %v want %v", err, b2bua.ErrALegNotFound)
	}
}

func TestMemoryStore_Create_emptyContinuityKey_alwaysNewSession(t *testing.T) {
	t.Parallel()
	s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a1, err := s.CreateALeg(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	a2, err := s.CreateALeg(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if a1.ALegID == a2.ALegID {
		t.Fatalf("expected distinct A-leg ids, got %q", a1.ALegID)
	}
	assertOpaqueALegID(t, a1.ALegID)
	assertOpaqueALegID(t, a2.ALegID)
	_, err = s.ResolveALeg(ctx, "")
	if !errors.Is(err, b2bua.ErrInvalidContinuityKey) {
		t.Fatalf("Resolve: %v", err)
	}
}

func TestMemoryStore_Create_sameContinuityKeyReplacesOldALeg(t *testing.T) {
	t.Parallel()
	s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := "reuse-key"
	a1, err := s.CreateALeg(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := s.CreateALeg(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := s.ResolveALeg(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ALegID != a2.ALegID {
		t.Fatalf("Resolve: want %q got %q", a2.ALegID, resolved.ALegID)
	}
	if a1.ALegID == a2.ALegID {
		t.Fatalf("expected distinct A-leg ids, got %q", a1.ALegID)
	}
	assertOpaqueALegID(t, a1.ALegID)
	assertOpaqueALegID(t, a2.ALegID)
	_, err = s.FetchALeg(ctx, a1.ALegID)
	if !errors.Is(err, b2bua.ErrALegNotFound) {
		t.Fatalf("old A-leg should be removed, got %v", err)
	}
}

func TestMemoryStore_ResolveCreate_roundTripContinuity(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := "client-sess-xyz"
	created, err := s.CreateALeg(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if created.ContinuityKey != key {
		t.Fatalf("ContinuityKey: got %q", created.ContinuityKey)
	}
	assertOpaqueALegID(t, created.ALegID)
	resolved, err := s.ResolveALeg(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ALegID != created.ALegID {
		t.Fatalf("ALegID mismatch: %q vs %q", resolved.ALegID, created.ALegID)
	}
}

func TestMemoryStore_WeightedFirstConsumed_persists(t *testing.T) {
	t.Parallel()
	clock := time.Unix(1700000000, 0).UTC()
	tick := clock
	s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{
		Now: func() time.Time { return tick },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, err := s.CreateALeg(ctx, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if a.WeightedFirstConsumed {
		t.Fatal("expected false initially")
	}
	if err := s.SetWeightedFirstConsumed(ctx, a.ALegID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWeightedFirstConsumed(ctx, a.ALegID, true); err != nil {
		t.Fatal(err)
	}
	tick = tick.Add(time.Minute)
	got, err := s.ResolveALeg(ctx, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.WeightedFirstConsumed {
		t.Fatal("expected WeightedFirstConsumed after resolve")
	}
}

func TestMemoryStore_NextBLeg_monotonicSeq(t *testing.T) {
	t.Parallel()
	s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, _ := s.CreateALeg(ctx, "k")
	b1, err := s.NextBLeg(ctx, a.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := s.NextBLeg(ctx, a.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if b1.Seq != 1 || b2.Seq != 2 {
		t.Fatalf("seq want 1,2 got %d,%d", b1.Seq, b2.Seq)
	}
	if b1.BLegID == b2.BLegID {
		t.Fatalf("expected distinct B-leg ids, got %q", b1.BLegID)
	}
	assertOpaqueBLegID(t, b1.BLegID)
	assertOpaqueBLegID(t, b2.BLegID)
	if b1.ALegID != a.ALegID || b2.ALegID != a.ALegID {
		t.Fatal("ALegID mismatch on B-leg")
	}
}

func TestMemoryStore_RecordAttempt_and_LoadAttempts_order(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 4, 20, 11, 0, 0, 0, time.UTC)
	s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, _ := s.CreateALeg(ctx, "k")
	b1, _ := s.NextBLeg(ctx, a.ALegID)
	b2, _ := s.NextBLeg(ctx, a.ALegID)
	r2 := aipapi.AttemptRecord{
		BLegID: b2.BLegID, ALegID: a.ALegID, Seq: 2,
		BackendID: "b", EffectiveModel: "m2",
		StartedAt: start, FinishedAt: start.Add(time.Second),
		Outcome: aipapi.AttemptSurfacedFailure, Reason: "quota",
	}
	r1 := aipapi.AttemptRecord{
		BLegID: b1.BLegID, ALegID: a.ALegID, Seq: 1,
		BackendID: "a", EffectiveModel: "m1",
		StartedAt: start, FinishedAt: start.Add(time.Second),
		Outcome: aipapi.AttemptSwallowedFailure, Reason: "timeout_pre_output",
	}
	if err := s.RecordAttempt(ctx, r2); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAttempt(ctx, r1); err != nil {
		t.Fatal(err)
	}
	rows, err := s.LoadAttempts(ctx, a.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("len %d", len(rows))
	}
	if rows[0].Seq != 1 || rows[1].Seq != 2 {
		t.Fatalf("order: %+v", rows)
	}
	if rows[0].Outcome != aipapi.AttemptSwallowedFailure || rows[1].Outcome != aipapi.AttemptSurfacedFailure {
		t.Fatalf("outcomes: %+v", rows)
	}
}

func TestMemoryStore_RecordAttempt_rejectsWrongBLegID(t *testing.T) {
	t.Parallel()
	s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, _ := s.CreateALeg(ctx, "k")
	b1, _ := s.NextBLeg(ctx, a.ALegID)
	err = s.RecordAttempt(ctx, aipapi.AttemptRecord{
		BLegID: "wrong", ALegID: a.ALegID, Seq: b1.Seq,
		Outcome: aipapi.AttemptSuccess,
	})
	if err == nil || !errors.Is(err, b2bua.ErrInvalidAttempt) {
		t.Fatalf("want ErrInvalidAttempt, got %v", err)
	}
	_ = b1
}

func TestMemoryStore_TTL_lazyEvictOnResolve(t *testing.T) {
	t.Parallel()
	t0 := time.Unix(1800, 0).UTC()
	tick := t0
	s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{
		TTL: time.Hour,
		Now: func() time.Time { return tick },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := "sess-ttl"
	if _, err := s.CreateALeg(ctx, key); err != nil {
		t.Fatal(err)
	}
	tick = t0.Add(2 * time.Hour)
	_, err = s.ResolveALeg(ctx, key)
	if !errors.Is(err, b2bua.ErrALegNotFound) {
		t.Fatalf("Resolve after TTL: got %v", err)
	}
	a2, err := s.CreateALeg(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveALeg(ctx, key); err != nil {
		t.Fatal(err)
	}
	if a2.ALegID == "" {
		t.Fatal("expected new A-leg")
	}
}

func TestMemoryStore_NextBLeg_concurrentUniqueSeq(t *testing.T) {
	t.Parallel()
	s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, _ := s.CreateALeg(ctx, "race-key")
	const n = 64
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			_, _ = s.NextBLeg(ctx, a.ALegID)
		})
	}
	wg.Wait()
	b, err := s.NextBLeg(ctx, a.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Seq != n+1 {
		t.Fatalf("expected next seq %d after %d concurrent allocations, got %d", n+1, n, b.Seq)
	}
}

func TestMemoryStore_InterleavedState(t *testing.T) {
	t.Parallel()
	b2buatest.TestInterleavedStateStore(t, func(t *testing.T) b2buatest.Store {
		t.Helper()
		s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}
