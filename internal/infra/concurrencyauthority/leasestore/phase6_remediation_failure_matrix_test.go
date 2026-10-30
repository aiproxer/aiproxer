package leasestore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
)

// runAcquireSetFailureMatrixContract covers outage, renewal-loss, and release-failure
// semantics for multi-rule sets across two store handles.
func runAcquireSetFailureMatrixContract(t *testing.T, a, b app.LeaseStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 21, 0, 0, 0, time.UTC)
	ttl := time.Minute
	rules := []string{"max-active", "max-active-b"}

	res, err := a.AcquireSet(ctx, acquireSetCmd("req-matrix", rules, now, ttl))
	if err != nil || res.CapacityExceeded {
		t.Fatalf("seed acquire: %+v err=%v", res, err)
	}
	setID := res.Set.SetID
	reqID := res.Set.RequestID
	gen := res.Set.Generation

	// Renewal loss: generation mismatch must fail deterministically; capacity stays.
	_, err = a.RenewSet(ctx, app.RenewSetCommand{
		SetID: setID, RequestID: reqID, ExpectedGeneration: gen + 99,
		TTL: ttl, RenewBefore: 15 * time.Second, Now: now,
	})
	if !errors.Is(err, domain.ErrGenerationMismatch) {
		t.Fatalf("renew mismatch: %v", err)
	}

	// Fill remaining slots (limit 5) then prove no over-admit.
	for i := range 10 {
		_, _ = b.AcquireSet(ctx, acquireSetCmd(fmt.Sprintf("req-fill-%02d", i), rules, now, ttl))
	}
	over, err := a.AcquireSet(ctx, acquireSetCmd("req-over", rules, now, ttl))
	if err != nil {
		t.Fatal(err)
	}
	if !over.CapacityExceeded {
		t.Fatalf("must not over-admit after renew mismatch: %+v", over)
	}

	// Release failure: canceled context must error and keep capacity occupied.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = a.ReleaseSet(cctx, app.ReleaseSetCommand{SetID: setID, RequestID: reqID, Now: now})
	if err == nil {
		t.Fatal("canceled release must error")
	}
	denied, err := b.AcquireSet(ctx, acquireSetCmd("req-after-bad-release", rules, now, ttl))
	if err != nil {
		t.Fatal(err)
	}
	if !denied.CapacityExceeded {
		t.Fatalf("failed release must keep capacity: %+v", denied)
	}

	// Outage renew: canceled context fails renew; occupancy remains.
	rctx, rcancel := context.WithCancel(ctx)
	rcancel()
	_, err = a.RenewSet(rctx, app.RenewSetCommand{
		SetID: setID, RequestID: reqID, ExpectedGeneration: gen,
		TTL: ttl, RenewBefore: 15 * time.Second, Now: now,
	})
	if err == nil {
		t.Fatal("canceled renew must error")
	}
	afterOutage, err := b.AcquireSet(ctx, acquireSetCmd("req-after-outage", rules, now, ttl))
	if err != nil {
		t.Fatal(err)
	}
	if !afterOutage.CapacityExceeded {
		t.Fatalf("outage renew must not free capacity: %+v", afterOutage)
	}

	if _, err := a.ReleaseSet(ctx, app.ReleaseSetCommand{SetID: setID, RequestID: reqID, Now: now}); err != nil {
		t.Fatal(err)
	}
}
