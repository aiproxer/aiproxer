package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type setRenewConcurrency struct {
	renewSetCalls atomic.Int32
	renewCalls    atomic.Int32
	failRenew     atomic.Bool
	canceled      atomic.Bool
}

func (s *setRenewConcurrency) AdmitLease(_ context.Context, _ authority.LeaseAdmission) (authority.LeaseDecision, error) {
	ttl := 120 * time.Millisecond
	return authority.LeaseDecision{
		Kind: authority.LeaseAllow, LeaseID: "L-a", Generation: 1,
		ExpiresAt: time.Now().UTC().Add(ttl), RenewBefore: 80 * time.Millisecond, TTL: ttl,
		FailureBehavior: authority.FailureFailClosed, SetID: "set-1",
		Leases: []authority.LeaseOccupancy{
			{LeaseID: "L-a", Generation: 1, RuleID: "rule-a", ExpiresAt: time.Now().UTC().Add(ttl), TTL: ttl},
			{LeaseID: "L-b", Generation: 1, RuleID: "rule-b", ExpiresAt: time.Now().UTC().Add(ttl), TTL: ttl},
		},
	}, nil
}

func (s *setRenewConcurrency) RenewLease(_ context.Context, in authority.LeaseRenew) (authority.LeaseDecision, error) {
	if in.SetID != "" {
		s.renewSetCalls.Add(1)
		if s.failRenew.Load() {
			return authority.LeaseDecision{}, context.DeadlineExceeded
		}
		ttl := 200 * time.Millisecond
		return authority.LeaseDecision{
			Kind: authority.LeaseAllow, LeaseID: in.LeaseID, Generation: in.ExpectedGeneration + 1,
			ExpiresAt: time.Now().UTC().Add(ttl), TTL: ttl, SetID: in.SetID,
			Leases: []authority.LeaseOccupancy{
				{LeaseID: "L-a", Generation: in.ExpectedGeneration + 1},
				{LeaseID: "L-b", Generation: in.ExpectedGeneration + 1},
			},
		}, nil
	}
	s.renewCalls.Add(1)
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: in.LeaseID, Generation: in.ExpectedGeneration + 1, ExpiresAt: time.Now().UTC().Add(time.Minute)}, nil
}

func (s *setRenewConcurrency) ReleaseLease(context.Context, authority.LeaseRelease) error { return nil }

func (s *setRenewConcurrency) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func TestPhase63_HeartbeatRenewsCompleteSetAtomically(t *testing.T) {
	t.Parallel()
	conc := &setRenewConcurrency{}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency: conc, CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-set", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	if st == nil || st.LeaseSetID != "set-1" {
		t.Fatalf("state=%+v", st)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conc.renewSetCalls.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conc.renewSetCalls.Load() < 1 {
		t.Fatal("expected RenewLease with SetID")
	}
	if conc.renewCalls.Load() != 0 {
		t.Fatalf("per-lease renews=%d want 0 for set heartbeat", conc.renewCalls.Load())
	}
	_ = ex.releaseRequestAuthority(ctx)
}

func TestPhase63_FailClosedCancelsBeforeUnprovenExpiry(t *testing.T) {
	t.Parallel()
	conc := &setRenewConcurrency{}
	conc.failRenew.Store(true)
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency: conc, CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-fc-set", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ctx.Err() == nil {
		t.Fatal("fail-closed must cancel request context before unproven expiry")
	}
	if st.heartbeat != nil && !st.heartbeat.Degraded() {
		t.Fatal("expected degraded heartbeat")
	}
}
