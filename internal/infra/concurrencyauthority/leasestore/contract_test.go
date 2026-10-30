package leasestore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

const (
	testRuleID = "max-active"
	testLimit  = 5
)

func principalDims(id string) domain.Dimensions {
	return domain.Dimensions{Principal: scope.Known(id)}
}

func acquireCmd(leaseID, logicalID string, now time.Time, ttl time.Duration) app.AcquireCommand {
	dims := principalDims("alice")
	lease := domain.NewLease(domain.NewLeaseParams{
		LeaseID:     leaseID,
		RuleID:      testRuleID,
		RuleVersion: "v1",
		LogicalID:   logicalID,
		Namespace:   "default",
		Dimensions:  dims,
		Now:         now,
		TTL:         ttl,
	})
	return app.AcquireCommand{
		Lease:      lease,
		RuleID:     testRuleID,
		Dimensions: dims,
		Limit:      testLimit,
		Mode:       domain.RuleModeStrict,
		Now:        now,
	}
}

// runFiveSlotContract proves ≤5 live leases across two store handles, plus
// replay, renew CAS, release, and expired reclaim on next acquire.
func runFiveSlotContract(t *testing.T, a, b app.LeaseStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	ttl := time.Minute

	for i := 1; i <= 5; i++ {
		store := a
		if i%2 == 0 {
			store = b
		}
		id := fmt.Sprintf("lease-%d", i)
		res, err := store.Acquire(ctx, acquireCmd(id, fmt.Sprintf("req-%d", i), now, ttl))
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		if res.CapacityExceeded || res.Rejected || res.Lease.LeaseID != id {
			t.Fatalf("acquire %d: %+v", i, res)
		}
		if res.RemainingSlots != testLimit-i {
			t.Fatalf("acquire %d remaining=%d want %d", i, res.RemainingSlots, testLimit-i)
		}
	}

	sixth, err := b.Acquire(ctx, acquireCmd("lease-6", "req-6", now, ttl))
	if err != nil {
		t.Fatalf("sixth: %v", err)
	}
	if !sixth.CapacityExceeded || sixth.RemainingSlots != 0 {
		t.Fatalf("sixth want capacity exceeded, got %+v", sixth)
	}

	replay, err := b.Acquire(ctx, acquireCmd("lease-1", "req-1", now, ttl))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed || replay.Lease.LeaseID != "lease-1" || replay.CapacityExceeded {
		t.Fatalf("replay=%+v", replay)
	}

	renewed, err := a.Renew(ctx, app.RenewCommand{
		LeaseID:            "lease-1",
		RequestID:          "req-1",
		ExpectedGeneration: 1,
		TTL:                ttl,
		Now:                now.Add(10 * time.Second),
	})
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if renewed.Lease.Generation != 2 {
		t.Fatalf("generation=%d", renewed.Lease.Generation)
	}
	_, err = a.Renew(ctx, app.RenewCommand{
		LeaseID:            "lease-1",
		ExpectedGeneration: 1,
		TTL:                ttl,
		Now:                now.Add(11 * time.Second),
	})
	if !errors.Is(err, domain.ErrGenerationMismatch) {
		t.Fatalf("stale renew err=%v", err)
	}

	rel, err := a.Release(ctx, app.ReleaseCommand{LeaseID: "lease-2", RequestID: "req-2", Now: now})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !rel.Applied || rel.Lease.State != domain.LeaseStateReleased {
		t.Fatalf("release=%+v", rel)
	}
	afterRelease, err := b.Acquire(ctx, acquireCmd("lease-6", "req-6", now, ttl))
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	if afterRelease.CapacityExceeded || afterRelease.Lease.LeaseID != "lease-6" {
		t.Fatalf("after release=%+v", afterRelease)
	}

	// lease-3 expires at now+1m; at now+2m reclaim must free capacity for lease-7.
	expireAt := now.Add(2 * time.Minute)
	reclaimed, err := a.Acquire(ctx, acquireCmd("lease-7", "req-7", expireAt, ttl))
	if err != nil {
		t.Fatalf("reclaim acquire: %v", err)
	}
	if reclaimed.CapacityExceeded {
		t.Fatalf("expected reclaim of expired leases to free a slot, got %+v", reclaimed)
	}

	q, err := a.Query(ctx, app.QueryCommand{State: domain.LeaseStateActive, Now: expireAt, Limit: 20})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	live := 0
	for _, l := range q.Leases {
		if l.IsLive(expireAt) && l.RuleID == testRuleID {
			live++
		}
	}
	if live > testLimit {
		t.Fatalf("live=%d exceeds limit %d: %+v", live, testLimit, q.Leases)
	}
}

func TestMemoryStore_FiveSlotAcrossHandles(t *testing.T) {
	t.Parallel()
	shared := leasestore.NewMemoryState()
	a := leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "mem-test", State: shared})
	b := leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "mem-test", State: shared})
	runFiveSlotContract(t, a, b)
}

func TestMemoryStore_ReadinessIsSingleProcess(t *testing.T) {
	t.Parallel()
	store := leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "mem-ready"})
	ready, err := store.CheckReadiness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != domain.ReadinessStateDegraded {
		t.Fatalf("memory readiness state=%s want degraded", ready.State)
	}
	if ready.Reason == "" {
		t.Fatal("expected single-process reason")
	}
}

func TestMemoryStore_BoundedReclaimSkipsOtherDimensions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "mem-reclaim"})

	// Fill alice to capacity with short TTL.
	for i := 1; i <= 5; i++ {
		_, err := store.Acquire(ctx, acquireCmd(fmt.Sprintf("a-%d", i), fmt.Sprintf("ar-%d", i), now, time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	// Bob has many expired leases that must not be required for alice reclaim.
	bobDims := principalDims("bob")
	for i := 1; i <= 100; i++ {
		lease := domain.NewLease(domain.NewLeaseParams{
			LeaseID:     fmt.Sprintf("b-%d", i),
			RuleID:      testRuleID,
			RuleVersion: "v1",
			LogicalID:   fmt.Sprintf("br-%d", i),
			Namespace:   "default",
			Dimensions:  bobDims,
			Now:         now.Add(-time.Hour),
			TTL:         time.Minute,
		})
		_, err := store.Acquire(ctx, app.AcquireCommand{
			Lease: lease, RuleID: testRuleID, Dimensions: bobDims, Limit: 1000, Mode: domain.RuleModeStrict, Now: now.Add(-time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	later := now.Add(2 * time.Second)
	res, err := store.Acquire(ctx, acquireCmd("a-new", "ar-new", later, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res.CapacityExceeded {
		t.Fatal("alice reclaim should free slots without scanning bob history as capacity")
	}
}
