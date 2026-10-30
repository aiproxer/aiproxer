package leasestore_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
)

func TestPhase61_AcquireSetAtomicMultiRuleAndLockOrder(t *testing.T) {
	t.Parallel()
	store := leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "phase61"})
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	dims := principalDims("alice")
	members := []app.AcquireSetMember{
		{
			Lease: domain.NewLease(domain.NewLeaseParams{
				LeaseID: "L-b", RuleID: "rule-b", RuleVersion: "v1", LogicalID: "req-1",
				Namespace: "default", Dimensions: dims, Now: now, TTL: time.Minute,
			}),
			RuleID: "rule-b", Dimensions: dims, Limit: 5, Mode: domain.RuleModeStrict,
		},
		{
			Lease: domain.NewLease(domain.NewLeaseParams{
				LeaseID: "L-a", RuleID: "rule-a", RuleVersion: "v1", LogicalID: "req-1",
				Namespace: "default", Dimensions: dims, Now: now, TTL: time.Minute,
			}),
			RuleID: "rule-a", Dimensions: dims, Limit: 5, Mode: domain.RuleModeStrict,
		},
	}
	setID := domain.StableSetID("default", "req-1", []string{"rule-b", "rule-a"})
	res, err := store.AcquireSet(ctx, app.AcquireSetCommand{
		SetID: setID, RequestID: "req-1", Members: members,
		TTL: time.Minute, RenewBefore: 15 * time.Second, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CapacityExceeded || len(res.Set.Members) != 2 {
		t.Fatalf("set=%+v", res)
	}
	if len(res.LockOrder) != 2 || res.LockOrder[0] != "rule-a" || res.LockOrder[1] != "rule-b" {
		t.Fatalf("lock order=%v want [rule-a rule-b]", res.LockOrder)
	}
	replay, err := store.AcquireSet(ctx, app.AcquireSetCommand{
		SetID: setID, RequestID: "req-1", Members: members,
		TTL: time.Minute, RenewBefore: 15 * time.Second, Now: now,
	})
	if err != nil || !replay.Replayed {
		t.Fatalf("replay: %+v err=%v", replay, err)
	}
}

func TestPhase61_UncertainNotReclaimedAsFreeCapacity(t *testing.T) {
	t.Parallel()
	store := leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "phase61-unc"})
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	dims := principalDims("alice")
	setID := domain.StableSetID("default", "req-u", []string{"max-active"})
	_, err := store.AcquireSet(ctx, app.AcquireSetCommand{
		SetID: setID, RequestID: "req-u",
		Members: []app.AcquireSetMember{{
			Lease: domain.NewLease(domain.NewLeaseParams{
				LeaseID: "L-u", RuleID: testRuleID, RuleVersion: "v1", LogicalID: "req-u",
				Namespace: "default", Dimensions: dims, Now: now, TTL: time.Second,
			}),
			RuleID: testRuleID, Dimensions: dims, Limit: 1, Mode: domain.RuleModeStrict,
		}},
		TTL: time.Second, RenewBefore: 200 * time.Millisecond, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSetUncertain(ctx, setID, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * time.Second)
	denied, err := store.AcquireSet(ctx, app.AcquireSetCommand{
		SetID:     domain.StableSetID("default", "req-other", []string{"max-active"}),
		RequestID: "req-other",
		Members: []app.AcquireSetMember{{
			Lease: domain.NewLease(domain.NewLeaseParams{
				LeaseID: "L-other", RuleID: testRuleID, RuleVersion: "v1", LogicalID: "req-other",
				Namespace: "default", Dimensions: dims, Now: later, TTL: time.Minute,
			}),
			RuleID: testRuleID, Dimensions: dims, Limit: 1, Mode: domain.RuleModeStrict,
		}},
		TTL: time.Minute, RenewBefore: 15 * time.Second, Now: later,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !denied.CapacityExceeded {
		t.Fatalf("uncertain occupancy must block reclaim: %+v", denied)
	}
}
