package leasestore_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
)

func acquireSetCmd(requestID string, rules []string, now time.Time, ttl time.Duration) app.AcquireSetCommand {
	dims := principalDims("alice")
	members := make([]app.AcquireSetMember, 0, len(rules))
	for _, ruleID := range rules {
		leaseID := domain.StableLeaseID("default", ruleID, "v1", requestID, dims)
		members = append(members, app.AcquireSetMember{
			Lease: domain.NewLease(domain.NewLeaseParams{
				LeaseID: leaseID, RuleID: ruleID, RuleVersion: "v1", LogicalID: requestID,
				Namespace: "default", Dimensions: dims, Now: now, TTL: ttl,
			}),
			RuleID: ruleID, Dimensions: dims, Limit: testLimit, Mode: domain.RuleModeStrict,
		})
	}
	return app.AcquireSetCommand{
		SetID:     domain.StableSetID("default", requestID, rules),
		RequestID: requestID, Members: members, TTL: ttl, RenewBefore: 15 * time.Second, Now: now,
	}
}

// runFiveSlotAcquireSetContract proves ≤5 multi-rule sets across two instances.
func runFiveSlotAcquireSetContract(t *testing.T, a, b app.LeaseStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 19, 0, 0, 0, time.UTC)
	ttl := time.Minute
	rules := []string{"max-active", "max-active-b"}
	wantOrder := append([]string(nil), rules...)
	sort.Strings(wantOrder)

	var allowed atomic.Int32
	var firstSetID, firstReqID atomic.Value
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := a
			if i%2 == 1 {
				store = b
			}
			reqID := fmt.Sprintf("req-%02d", i)
			res, err := store.AcquireSet(ctx, acquireSetCmd(reqID, rules, now, ttl))
			if err != nil {
				t.Errorf("acquire set: %v", err)
				return
			}
			if res.CapacityExceeded {
				return
			}
			allowed.Add(1)
			if len(res.LockOrder) != 2 || res.LockOrder[0] != wantOrder[0] || res.LockOrder[1] != wantOrder[1] {
				t.Errorf("lock order=%v want %v", res.LockOrder, wantOrder)
			}
			firstSetID.CompareAndSwap(nil, res.Set.SetID)
			firstReqID.CompareAndSwap(nil, res.Set.RequestID)
		}(i)
	}
	wg.Wait()
	if got := allowed.Load(); got != 5 {
		t.Fatalf("allowed sets=%d want 5", got)
	}
	setID, _ := firstSetID.Load().(string)
	reqID, _ := firstReqID.Load().(string)
	if setID == "" || reqID == "" {
		t.Fatal("missing first set identity")
	}

	replay, err := a.AcquireSet(ctx, acquireSetCmd(reqID, rules, now, ttl))
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Set.SetID != setID {
		t.Fatalf("expected set replay for %s, got %+v", setID, replay)
	}

	sixth, err := b.AcquireSet(ctx, acquireSetCmd("req-sixth", rules, now, ttl))
	if err != nil {
		t.Fatal(err)
	}
	if !sixth.CapacityExceeded {
		t.Fatalf("sixth must deny: %+v", sixth)
	}

	if err := a.MarkSetUncertain(ctx, setID, now); err != nil {
		t.Fatal(err)
	}
	stillDenied, err := b.AcquireSet(ctx, acquireSetCmd("req-after-uncertain", rules, now, ttl))
	if err != nil {
		t.Fatal(err)
	}
	if !stillDenied.CapacityExceeded {
		t.Fatalf("uncertain set must keep capacity occupied: %+v", stillDenied)
	}

	if _, err := a.ReleaseSet(ctx, app.ReleaseSetCommand{SetID: setID, RequestID: reqID, Now: now}); err != nil {
		t.Fatal(err)
	}
	recovered, err := b.AcquireSet(ctx, acquireSetCmd("req-recover", rules, now, ttl))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.CapacityExceeded || recovered.Replayed {
		t.Fatalf("capacity must recover after set release: %+v", recovered)
	}
}
