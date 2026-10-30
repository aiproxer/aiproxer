package authoritycoord_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

type fakeConcurrencyProvider struct {
	admit    func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error)
	released atomic.Int32
	renewed  atomic.Int32
}

func (f *fakeConcurrencyProvider) AdmitLease(ctx context.Context, in authority.LeaseAdmission) (authority.LeaseDecision, error) {
	if f.admit != nil {
		return f.admit(ctx, in)
	}
	return authority.LeaseDecision{
		Kind:       authority.LeaseAllow,
		LeaseID:    "lease-1",
		Generation: 1,
		ExpiresAt:  time.Now().Add(time.Minute),
	}, nil
}

func (f *fakeConcurrencyProvider) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	f.renewed.Add(1)
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "lease-1", Generation: 2, ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (f *fakeConcurrencyProvider) ReleaseLease(context.Context, authority.LeaseRelease) error {
	f.released.Add(1)
	return nil
}

func (f *fakeConcurrencyProvider) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func TestRequestCoordinator_AdmitLease_RejectsExpiredRelativeToInjectedNow(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0).UTC()
	conc := &fakeConcurrencyProvider{
		admit: func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
			return authority.LeaseDecision{
				Kind:       authority.LeaseAllow,
				LeaseID:    "lease-1",
				Generation: 1,
				ExpiresAt:  now.Add(-time.Second),
			}, nil
		},
	}
	desc := authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage: authority.StageLeaseAdmit, Strength: authority.StrengthRequired, FailureBehavior: authority.FailureFailClosed,
		}},
	}
	coord := &authoritycoord.RequestCoordinator{
		Concurrency:           conc,
		ConcurrencyDescriptor: &desc,
		Now:                   func() time.Time { return now },
	}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expired lease relative to injected Now must fail (req 10.2)")
	}
	if _, ok := errors.AsType[*authoritycoord.UnavailableError](err); !ok {
		t.Fatalf("want UnavailableError, got %T %v", err, err)
	}
}

func TestRequestCoordinator_AdmitCapturesLeaseAndPassesLifecycle(t *testing.T) {
	t.Parallel()
	var gotLifecycle authority.LifecycleScope
	var gotParent string
	conc := &fakeConcurrencyProvider{
		admit: func(_ context.Context, in authority.LeaseAdmission) (authority.LeaseDecision, error) {
			gotLifecycle = in.Lifecycle
			gotParent = in.ParentLeaseID
			return authority.LeaseDecision{
				Kind:        authority.LeaseAllow,
				LeaseID:     "lease-aux",
				Generation:  3,
				ExpiresAt:   time.Now().Add(time.Minute),
				RenewBefore: 10 * time.Second,
				TTL:         time.Minute,
			}, nil
		},
	}
	coord := &authoritycoord.RequestCoordinator{Concurrency: conc}
	in := validRequestAdmission()
	in.Lifecycle = authority.LifecycleAuxiliaryRequest
	in.ParentLeaseID = "parent-lease"
	d, err := coord.Admit(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if gotLifecycle != authority.LifecycleAuxiliaryRequest || gotParent != "parent-lease" {
		t.Fatalf("lifecycle=%q parent=%q", gotLifecycle, gotParent)
	}
	if d.Lease.LeaseID != "lease-aux" || d.Lease.Generation != 3 {
		t.Fatalf("lease=%+v", d.Lease)
	}
	if len(d.Stack.Handles()) != 1 || d.Stack.Handles()[0] != "lease-aux" {
		t.Fatalf("handles=%v", d.Stack.Handles())
	}
}

func TestRequestCoordinator_SettleDoesNotReleaseLease(t *testing.T) {
	t.Parallel()
	conc := &fakeConcurrencyProvider{}
	quota := &fakeRequestProvider{id: "quota"}
	coord := &authoritycoord.RequestCoordinator{
		Concurrency: conc,
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: quota,
		}},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatal(err)
	}
	if err := coord.Settle(context.Background(), d.Stack, authority.RequestSettlement{RequestID: "req-1", Handles: d.Stack.Handles()}); err != nil {
		t.Fatal(err)
	}
	if conc.released.Load() != 0 {
		t.Fatalf("Settle must not release concurrency lease; released=%d", conc.released.Load())
	}
	if err := coord.ReleaseLease(context.Background(), d.Lease.LeaseID, "req-1", "settled"); err != nil {
		t.Fatal(err)
	}
	if conc.released.Load() != 1 {
		t.Fatalf("ReleaseLease released=%d want 1", conc.released.Load())
	}
}

func TestRequestCoordinator_MultiLeaseCompensateAndReleaseAll(t *testing.T) {
	t.Parallel()
	var released []string
	conc := &fakeConcurrencyProvider{
		admit: func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
			return authority.LeaseDecision{
				Kind:       authority.LeaseAllow,
				LeaseID:    "lease-b", // primary = lastAllow
				Generation: 2,
				ExpiresAt:  time.Now().Add(time.Minute),
				Leases: []authority.LeaseOccupancy{
					{LeaseID: "lease-a", Generation: 1, RuleID: "rule-a"},
					{LeaseID: "lease-b", Generation: 2, RuleID: "rule-b"},
				},
			}, nil
		},
	}
	tracking := &trackingConcurrency{fake: conc, onRelease: func(id string) {
		released = append(released, id)
	}}

	denyQuota := &fakeRequestProvider{
		id: "quota",
		admit: func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
			return authority.Decision{Kind: authority.DecisionDeny}, nil
		},
	}
	coord := &authoritycoord.RequestCoordinator{
		Concurrency: tracking,
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: denyQuota,
		}},
		CleanupTimeout: time.Second,
	}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expected deny from quota")
	}
	if len(released) != 2 {
		t.Fatalf("compensate released=%v want both lease-a and lease-b", released)
	}
	byID := map[string]int{}
	for _, id := range released {
		byID[id]++
	}
	if byID["lease-a"] != 1 || byID["lease-b"] != 1 {
		t.Fatalf("released counts=%v", byID)
	}
}

func TestRequestCoordinator_MultiLeaseStackHandlesAll(t *testing.T) {
	t.Parallel()
	conc := &fakeConcurrencyProvider{
		admit: func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
			return authority.LeaseDecision{
				Kind:       authority.LeaseAllow,
				LeaseID:    "lease-b",
				Generation: 1,
				ExpiresAt:  time.Now().UTC().Add(time.Minute),
				Leases: []authority.LeaseOccupancy{
					{LeaseID: "lease-a", Generation: 1, RuleID: "rule-a", ExpiresAt: time.Now().UTC().Add(time.Minute)},
					{LeaseID: "lease-b", Generation: 1, RuleID: "rule-b", ExpiresAt: time.Now().UTC().Add(time.Minute)},
				},
			}, nil
		},
	}
	coord := &authoritycoord.RequestCoordinator{Concurrency: conc}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatal(err)
	}
	handles := d.Stack.Handles()
	if len(handles) != 2 {
		t.Fatalf("handles=%v want 2", handles)
	}
	if err := coord.ReleaseLeases(context.Background(), []string{"lease-a", "lease-b"}, "req-1", "settled"); err != nil {
		t.Fatal(err)
	}
	if conc.released.Load() != 2 {
		t.Fatalf("ReleaseLeases released=%d want 2", conc.released.Load())
	}
}

type trackingConcurrency struct {
	fake      *fakeConcurrencyProvider
	onRelease func(leaseID string)
}

func (t *trackingConcurrency) AdmitLease(ctx context.Context, in authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return t.fake.AdmitLease(ctx, in)
}

func (t *trackingConcurrency) RenewLease(ctx context.Context, in authority.LeaseRenew) (authority.LeaseDecision, error) {
	return t.fake.RenewLease(ctx, in)
}

func (t *trackingConcurrency) ReleaseLease(ctx context.Context, in authority.LeaseRelease) error {
	if t.onRelease != nil {
		t.onRelease(in.LeaseID)
	}
	return t.fake.ReleaseLease(ctx, in)
}

func (t *trackingConcurrency) QueryLeases(ctx context.Context, q authority.LeaseQuery) (authority.LeasePage, error) {
	return t.fake.QueryLeases(ctx, q)
}
