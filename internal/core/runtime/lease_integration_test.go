package runtime

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type recordingConcurrency struct {
	admitGen        int64
	released        atomic.Int32
	renewed         atomic.Int32
	renewFail       atomic.Bool
	renewCh         chan struct{}
	releaseCh       chan struct{}
	expiresIn       time.Duration
	renewBefore     time.Duration
	failureBehavior authority.FailureBehavior
	lastAdmit       atomic.Pointer[authority.LeaseAdmission]
	admitCount      atomic.Int32
	multiLeases     []authority.LeaseOccupancy
	primaryLeaseID  string
}

func (r *recordingConcurrency) AdmitLease(_ context.Context, in authority.LeaseAdmission) (authority.LeaseDecision, error) {
	cp := in
	r.lastAdmit.Store(&cp)
	r.admitCount.Add(1)
	ttl := r.expiresIn
	if ttl <= 0 {
		ttl = 200 * time.Millisecond
	}
	renewBefore := r.renewBefore
	if renewBefore <= 0 {
		renewBefore = 50 * time.Millisecond
	}
	n := r.admitCount.Load()
	if len(r.multiLeases) > 0 {
		leases := make([]authority.LeaseOccupancy, len(r.multiLeases))
		copy(leases, r.multiLeases)
		for i := range leases {
			if leases[i].ExpiresAt.IsZero() {
				leases[i].ExpiresAt = time.Now().Add(ttl)
			}
			if leases[i].RenewBefore <= 0 {
				leases[i].RenewBefore = renewBefore
			}
			if leases[i].TTL <= 0 {
				leases[i].TTL = ttl
			}
			if leases[i].FailureBehavior == "" {
				leases[i].FailureBehavior = r.failureBehavior
			}
			if leases[i].Generation == 0 {
				leases[i].Generation = 1
			}
		}
		primary := r.primaryLeaseID
		if primary == "" {
			primary = leases[len(leases)-1].LeaseID
		}
		var primaryOcc authority.LeaseOccupancy
		for _, occ := range leases {
			if occ.LeaseID == primary {
				primaryOcc = occ
				break
			}
		}
		if primaryOcc.LeaseID == "" {
			primaryOcc = leases[len(leases)-1]
		}
		return authority.LeaseDecision{
			Kind:            authority.LeaseAllow,
			LeaseID:         primaryOcc.LeaseID,
			Generation:      primaryOcc.Generation,
			ExpiresAt:       primaryOcc.ExpiresAt,
			RenewBefore:     renewBefore,
			TTL:             ttl,
			FailureBehavior: r.failureBehavior,
			Leases:          leases,
		}, nil
	}
	return authority.LeaseDecision{
		Kind:            authority.LeaseAllow,
		LeaseID:         fmt.Sprintf("lease-hb-%d", n),
		Generation:      1,
		ExpiresAt:       time.Now().Add(ttl),
		RenewBefore:     renewBefore,
		TTL:             ttl,
		FailureBehavior: r.failureBehavior,
	}, nil
}

func (r *recordingConcurrency) RenewLease(ctx context.Context, in authority.LeaseRenew) (authority.LeaseDecision, error) {
	if r.renewFail.Load() {
		return authority.LeaseDecision{}, context.DeadlineExceeded
	}
	r.renewed.Add(1)
	select {
	case r.renewCh <- struct{}{}:
	default:
	}
	ttl := r.expiresIn
	if ttl <= 0 {
		ttl = 200 * time.Millisecond
	}
	return authority.LeaseDecision{
		Kind:       authority.LeaseAllow,
		LeaseID:    in.LeaseID,
		Generation: in.ExpectedGeneration + 1,
		ExpiresAt:  time.Now().Add(ttl),
	}, nil
}

func (r *recordingConcurrency) ReleaseLease(context.Context, authority.LeaseRelease) error {
	r.released.Add(1)
	select {
	case r.releaseCh <- struct{}{}:
	default:
	}
	return nil
}

func (r *recordingConcurrency) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func TestPhase83_settleReleasesConcurrencyLease(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{releaseCh: make(chan struct{}, 1)}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-settle", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	if st == nil || st.LeaseID == "" {
		t.Fatalf("state=%+v", st)
	}
	_ = ex.settleRequestAuthority(ctx, nil)
	if conc.released.Load() != 1 {
		t.Fatalf("released=%d want 1 (settle must ReleaseLease; Settle alone does not)", conc.released.Load())
	}
	_ = ex.settleRequestAuthority(ctx, nil) // idempotent
	if conc.released.Load() != 1 {
		t.Fatalf("idempotent settle released=%d", conc.released.Load())
	}
}

func TestPhase83_settleReleasesAllMultiLeases(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{
		releaseCh: make(chan struct{}, 4),
		multiLeases: []authority.LeaseOccupancy{
			{LeaseID: "lease-a", Generation: 1, RuleID: "rule-a", ExpiresAt: time.Now().Add(time.Minute)},
			{LeaseID: "lease-b", Generation: 1, RuleID: "rule-b", ExpiresAt: time.Now().Add(time.Minute)},
		},
		primaryLeaseID: "lease-b",
	}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-multi-settle", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	if st == nil {
		t.Fatal("missing authority state")
	}
	if st.LeaseID != "lease-b" {
		t.Fatalf("primary LeaseID=%q want lease-b", st.LeaseID)
	}
	if len(st.LeaseIDs) != 2 {
		t.Fatalf("LeaseIDs=%v want 2", st.LeaseIDs)
	}
	_ = ex.settleRequestAuthority(ctx, nil)
	if conc.released.Load() != 2 {
		t.Fatalf("released=%d want 2", conc.released.Load())
	}
	_ = ex.settleRequestAuthority(ctx, nil)
	if conc.released.Load() != 2 {
		t.Fatalf("idempotent settle released=%d", conc.released.Load())
	}
}

func TestPhase83_heartbeatRenewsAllMultiLeases(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{
		expiresIn:   150 * time.Millisecond,
		renewBefore: 100 * time.Millisecond,
		renewCh:     make(chan struct{}, 8),
		releaseCh:   make(chan struct{}, 4),
		multiLeases: []authority.LeaseOccupancy{
			{LeaseID: "lease-a", Generation: 1, RuleID: "rule-a"},
			{LeaseID: "lease-b", Generation: 1, RuleID: "rule-b"},
		},
		primaryLeaseID: "lease-b",
	}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-multi-hb", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conc.renewed.Load() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conc.renewed.Load() < 2 {
		t.Fatalf("renewed=%d want >=2 (both leases)", conc.renewed.Load())
	}
	_ = ex.settleRequestAuthority(ctx, nil)
	if conc.released.Load() != 2 {
		t.Fatalf("released=%d want 2", conc.released.Load())
	}
}

func TestPhase83_releaseStopsHeartbeatAndReleasesLease(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{
		expiresIn:   5 * time.Second,
		renewBefore: 4 * time.Second,
		renewCh:     make(chan struct{}, 4),
		releaseCh:   make(chan struct{}, 1),
	}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-rel", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	_ = ex.releaseRequestAuthority(ctx)
	if conc.released.Load() != 1 {
		t.Fatalf("released=%d want 1", conc.released.Load())
	}
	time.Sleep(200 * time.Millisecond)
	if conc.renewed.Load() != 0 {
		t.Fatalf("heartbeat must stop on release; renewed=%d", conc.renewed.Load())
	}
}

func TestPhase83_heartbeatRenewsBeforeExpiry(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{
		expiresIn:   150 * time.Millisecond,
		renewBefore: 100 * time.Millisecond,
		renewCh:     make(chan struct{}, 2),
		releaseCh:   make(chan struct{}, 1),
	}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-hb", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-conc.renewCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected lease renew before expiry")
	}
	_ = ex.settleRequestAuthority(ctx, nil)
	if conc.released.Load() != 1 {
		t.Fatalf("released=%d", conc.released.Load())
	}
}

func TestPhase83_renewFailureMarksDegradedWithoutRelease(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{
		expiresIn:       120 * time.Millisecond,
		renewBefore:     90 * time.Millisecond,
		renewCh:         make(chan struct{}, 1),
		releaseCh:       make(chan struct{}, 1),
		failureBehavior: authority.FailureFailClosed,
	}
	conc.renewFail.Store(true)
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-deg", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st.heartbeat != nil && st.heartbeat.Degraded() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.heartbeat == nil || !st.heartbeat.Degraded() {
		t.Fatal("expected degraded heartbeat after renew failure")
	}
	if conc.released.Load() != 0 {
		t.Fatalf("renew failure must not release lease; released=%d", conc.released.Load())
	}
	_ = ex.releaseRequestAuthority(ctx)
}

func TestPhase83_renewFailClosedStopsRetrying(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{
		expiresIn:       80 * time.Millisecond,
		renewBefore:     60 * time.Millisecond,
		failureBehavior: authority.FailureFailClosed,
	}
	conc.renewFail.Store(true)
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-fc", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st.heartbeat != nil && st.heartbeat.Degraded() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !st.heartbeat.Degraded() {
		t.Fatal("expected degraded")
	}
	// Fail-closed stops the renew loop; RenewLease should have been attempted once.
	time.Sleep(200 * time.Millisecond)
	if n := conc.renewed.Load(); n != 0 {
		t.Fatalf("fail_closed must not succeed renews; renewed=%d", n)
	}
	_ = ex.releaseRequestAuthority(ctx)
}

func TestPhase83_renewFailOpenRetries(t *testing.T) {
	t.Parallel()
	var renewAttempts atomic.Int32
	conc := &recordingConcurrency{
		expiresIn:       80 * time.Millisecond,
		renewBefore:     60 * time.Millisecond,
		failureBehavior: authority.FailureFailOpen,
	}
	conc.renewFail.Store(true)
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency: &countingRenewConcurrency{
					recordingConcurrency: conc,
					attempts:             &renewAttempts,
				},
				CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-fo", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if renewAttempts.Load() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if renewAttempts.Load() < 2 {
		t.Fatalf("fail_open must retry renew; attempts=%d", renewAttempts.Load())
	}
	_ = ex.releaseRequestAuthority(ctx)
}

type countingRenewConcurrency struct {
	*recordingConcurrency
	attempts *atomic.Int32
}

func (c *countingRenewConcurrency) RenewLease(ctx context.Context, in authority.LeaseRenew) (authority.LeaseDecision, error) {
	c.attempts.Add(1)
	return c.recordingConcurrency.RenewLease(ctx, in)
}

func TestPhase83_auxiliaryAcquireOwnAdmitsSecondLease(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{expiresIn: time.Second, renewBefore: 500 * time.Millisecond}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			ConcurrencyAuxiliaryLeasePolicy: "acquire_own",
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
			},
		},
	}
	parentCtx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-aux", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	parent := requestAuthorityFrom(parentCtx)
	if parent == nil || parent.LeaseID == "" {
		t.Fatal("missing parent lease")
	}
	auxCtx := execctx.WithAuxiliaryDepth(parentCtx, 1)
	childCtx, err := ex.admitRequestAuthorityOnce(auxCtx, "req-aux", "a2", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	child := requestAuthorityFrom(childCtx)
	if child == nil || child.LeaseID == "" {
		t.Fatal("missing child lease")
	}
	if child.LeaseID == parent.LeaseID {
		t.Fatalf("acquire_own must take a distinct lease; parent=%s child=%s", parent.LeaseID, child.LeaseID)
	}
	if conc.admitCount.Load() != 2 {
		t.Fatalf("admitCount=%d want 2", conc.admitCount.Load())
	}
	last := conc.lastAdmit.Load()
	if last == nil || last.AuxPolicy != "acquire_own" {
		t.Fatalf("last admit=%+v", last)
	}
	_ = ex.releaseRequestAuthority(childCtx)
	_ = ex.releaseRequestAuthority(parentCtx)
}

func TestPhase83_auxiliaryInheritSkipsSecondAdmit(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{expiresIn: time.Second, renewBefore: 500 * time.Millisecond}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			ConcurrencyAuxiliaryLeasePolicy: "inherit",
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
			},
		},
	}
	parentCtx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-inh", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	auxCtx := execctx.WithAuxiliaryDepth(parentCtx, 1)
	childCtx, err := ex.admitRequestAuthorityOnce(auxCtx, "req-inh", "a2", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	if conc.admitCount.Load() != 1 {
		t.Fatalf("inherit must not re-admit; count=%d", conc.admitCount.Load())
	}
	if requestAuthorityFrom(childCtx) != requestAuthorityFrom(parentCtx) {
		t.Fatal("inherit must reuse parent authority state")
	}
	_ = ex.releaseRequestAuthority(parentCtx)
}

func TestPhase83_uncommittedReleaseFreesLease(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{releaseCh: make(chan struct{}, 1)}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-uncommit", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	// Mirrors prepare cleanup / uncommitted cancel: release without settle.
	_ = ex.releaseRequestAuthority(ctx)
	if conc.released.Load() != 1 {
		t.Fatalf("released=%d want 1", conc.released.Load())
	}
}

func TestBuildAuthorityCoordinators_WiresConcurrency(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{}
	req, att := BuildAuthorityCoordinators(nil, conc)
	if req == nil || req.Concurrency != conc {
		t.Fatalf("req=%v", req)
	}
	if att != nil {
		t.Fatal("attempt coordinator should be nil without usage authority")
	}
}
