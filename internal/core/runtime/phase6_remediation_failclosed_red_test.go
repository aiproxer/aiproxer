package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type remediatingSetConc struct {
	failRenew   atomic.Bool
	failRelease atomic.Bool
	uncertain   atomic.Int32
	renewCalls  atomic.Int32
	lastSetID   atomic.Value
	markErr     error
	renewErr    error
	releaseErr  error
}

func (s *remediatingSetConc) AdmitLease(_ context.Context, _ authority.LeaseAdmission) (authority.LeaseDecision, error) {
	ttl := 150 * time.Millisecond
	return authority.LeaseDecision{
		Kind: authority.LeaseAllow, LeaseID: "L-a", Generation: 1, SetID: "set-remed",
		ExpiresAt: time.Now().UTC().Add(ttl), RenewBefore: 100 * time.Millisecond, TTL: ttl,
		FailureBehavior: authority.FailureFailClosed,
		Leases: []authority.LeaseOccupancy{
			{LeaseID: "L-a", Generation: 1, RuleID: "rule-a", ExpiresAt: time.Now().UTC().Add(ttl), TTL: ttl},
			{LeaseID: "L-b", Generation: 1, RuleID: "rule-b", ExpiresAt: time.Now().UTC().Add(ttl), TTL: ttl},
		},
	}, nil
}

func (s *remediatingSetConc) RenewLease(_ context.Context, in authority.LeaseRenew) (authority.LeaseDecision, error) {
	s.renewCalls.Add(1)
	s.lastSetID.Store(in.SetID)
	if s.failRenew.Load() {
		if s.renewErr != nil {
			return authority.LeaseDecision{}, s.renewErr
		}
		return authority.LeaseDecision{}, context.DeadlineExceeded
	}
	return authority.LeaseDecision{
		Kind: authority.LeaseAllow, LeaseID: in.LeaseID, SetID: in.SetID,
		Generation: in.ExpectedGeneration + 1, ExpiresAt: time.Now().UTC().Add(time.Minute), TTL: time.Minute,
		Leases: []authority.LeaseOccupancy{{LeaseID: "L-a"}, {LeaseID: "L-b"}},
	}, nil
}

func (s *remediatingSetConc) ReleaseLease(_ context.Context, in authority.LeaseRelease) error {
	if s.failRelease.Load() {
		if s.releaseErr != nil {
			return s.releaseErr
		}
		return errors.New("release lease set failed")
	}
	_ = in
	return nil
}

func (s *remediatingSetConc) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func (s *remediatingSetConc) MarkLeaseSetUncertain(_ context.Context, setID string) error {
	s.uncertain.Add(1)
	s.lastSetID.Store(setID)
	return s.markErr
}

type capturingIntentStore struct {
	appended []terminalwork.WorkRecord
	fail     error
}

func (s *capturingIntentStore) AppendIntent(_ context.Context, rec terminalwork.WorkRecord) error {
	if s.fail != nil {
		return s.fail
	}
	s.appended = append(s.appended, rec)
	return nil
}

func (s *capturingIntentStore) PromotePending(context.Context, terminalwork.PromotePendingCommand) error {
	return nil
}

// awaitFailClosedCancel waits until the lease heartbeat cancels the request
// context. Heartbeat writes LeaseSetReleaseAcceptErr / intent / uncertain mark
// before cancelRequest, so receiving on ctx.Done establishes happens-before for
// those fields without sleeping on unsynchronized spies.
func awaitFailClosedCancel(ctx context.Context, t *testing.T) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for fail-closed cancel")
	}
}

func TestPhase6Remediation_FailClosedMarksUncertainAcceptsIntentThenCancels(t *testing.T) {
	t.Parallel()
	conc := &remediatingSetConc{}
	conc.failRenew.Store(true)
	conc.renewErr = context.DeadlineExceeded
	store := &capturingIntentStore{}
	intents := terminalworkapp.NewIntentService(store, terminalworkapp.IntentServiceConfig{})
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency: conc, CleanupTimeout: time.Second,
			},
			TerminalWork: intents,
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-remed", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	awaitFailClosedCancel(ctx, t)
	if conc.uncertain.Load() < 1 {
		t.Fatal("must MarkLeaseSetUncertain before cancel")
	}
	if ctx.Err() == nil {
		t.Fatal("must cancel request context")
	}
	if len(store.appended) != 1 || store.appended[0].Kind != sdk.WorkKindReleaseLeaseSet {
		t.Fatalf("want persisted release_lease_set intent, got %+v", store.appended)
	}
	if store.appended[0].LeaseSetID != "set-remed" {
		t.Fatalf("lease_set_id=%q", store.appended[0].LeaseSetID)
	}
	if st.LeaseSetReleaseAcceptErr != nil {
		t.Fatalf("accept should succeed: %v", st.LeaseSetReleaseAcceptErr)
	}
}

func TestPhase6Remediation_FailClosedRecordsAcceptFailure(t *testing.T) {
	t.Parallel()
	conc := &remediatingSetConc{}
	conc.failRenew.Store(true)
	store := &capturingIntentStore{fail: errors.New("intent store down")}
	intents := terminalworkapp.NewIntentService(store, terminalworkapp.IntentServiceConfig{})
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency: conc, CleanupTimeout: time.Second,
			},
			TerminalWork: intents,
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-accept-fail", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	awaitFailClosedCancel(ctx, t)
	if st.LeaseSetReleaseAcceptErr == nil {
		t.Fatal("accept failure must be recorded, never ignored")
	}
	if ctx.Err() == nil {
		t.Fatal("must still cancel on fail-closed")
	}
}

func TestPhase6Remediation_FailClosedAcceptErrObservableOnlyAfterCancel(t *testing.T) {
	t.Parallel()
	conc := &remediatingSetConc{}
	conc.failRenew.Store(true)
	store := &capturingIntentStore{fail: errors.New("intent store down")}
	intents := terminalworkapp.NewIntentService(store, terminalworkapp.IntentServiceConfig{})
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency: conc, CleanupTimeout: time.Second,
			},
			TerminalWork: intents,
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-accept-hb", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	awaitFailClosedCancel(ctx, t)
	if st.LeaseSetReleaseAcceptErr == nil {
		t.Fatal("accept failure must be recorded before cancelRequest")
	}
	if conc.uncertain.Load() < 1 {
		t.Fatal("uncertain mark must precede cancel")
	}
}

func TestPhase6Remediation_SettleReleaseFailureAcceptsDurablePending(t *testing.T) {
	t.Parallel()
	conc := &remediatingSetConc{}
	conc.failRelease.Store(true)
	store := &capturingIntentStore{}
	intents := terminalworkapp.NewIntentService(store, terminalworkapp.IntentServiceConfig{})
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency: conc, CleanupTimeout: time.Second,
			},
			TerminalWork: intents,
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-settle-rel", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	err = ex.settleRequestAuthority(ctx, nil)
	if !errors.Is(err, terminalworkapp.ErrDurablePending) {
		t.Fatalf("got %v want ErrDurablePending", err)
	}
	st := requestAuthorityFrom(ctx)
	if st.Settled || st.Released {
		t.Fatalf("must not mark settled/released; settled=%v released=%v", st.Settled, st.Released)
	}
	if len(store.appended) != 1 || store.appended[0].Kind != sdk.WorkKindReleaseLeaseSet {
		t.Fatalf("want release_lease_set intent, got %+v", store.appended)
	}
	if store.appended[0].LeaseSetID != "set-remed" {
		t.Fatalf("lease_set_id=%q", store.appended[0].LeaseSetID)
	}
}

func TestPhase6Remediation_SettleReleaseAcceptFailureRecorded(t *testing.T) {
	t.Parallel()
	conc := &remediatingSetConc{}
	conc.failRelease.Store(true)
	store := &capturingIntentStore{fail: errors.New("intent store down")}
	intents := terminalworkapp.NewIntentService(store, terminalworkapp.IntentServiceConfig{})
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency: conc, CleanupTimeout: time.Second,
			},
			TerminalWork: intents,
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-settle-accept-fail", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	err = ex.settleRequestAuthority(ctx, nil)
	if err == nil {
		t.Fatal("want settle error when release+accept fail")
	}
	st := requestAuthorityFrom(ctx)
	if st.Settled || st.Released {
		t.Fatalf("must not mark settled/released; settled=%v released=%v", st.Settled, st.Released)
	}
	if st.LeaseSetReleaseAcceptErr == nil {
		t.Fatal("accept failure must be recorded")
	}
	if errors.Is(err, terminalworkapp.ErrDurablePending) {
		t.Fatal("accept failure must not report durable pending")
	}
}
