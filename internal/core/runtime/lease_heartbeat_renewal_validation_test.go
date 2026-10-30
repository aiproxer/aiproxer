package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type hostileRenewConcurrency struct {
	released        atomic.Int32
	renewed         atomic.Int32
	renewCh         chan struct{}
	mode            atomic.Value // string
	secret          string
	failureBehavior authority.FailureBehavior
}

func (h *hostileRenewConcurrency) AdmitLease(_ context.Context, _ authority.LeaseAdmission) (authority.LeaseDecision, error) {
	ttl := 150 * time.Millisecond
	fb := h.failureBehavior
	if fb == "" {
		fb = authority.FailureFailClosed
	}
	return authority.LeaseDecision{
		Kind:            authority.LeaseAllow,
		LeaseID:         "lease-hostile",
		Generation:      1,
		ExpiresAt:       time.Now().UTC().Add(ttl),
		RenewBefore:     100 * time.Millisecond,
		TTL:             ttl,
		FailureBehavior: fb,
	}, nil
}

func (h *hostileRenewConcurrency) RenewLease(_ context.Context, in authority.LeaseRenew) (authority.LeaseDecision, error) {
	h.renewed.Add(1)
	select {
	case h.renewCh <- struct{}{}:
	default:
	}
	mode, _ := h.mode.Load().(string)
	now := time.Now().UTC()
	switch mode {
	case "unknown_kind":
		return authority.LeaseDecision{Kind: "weird", LeaseID: in.LeaseID, Generation: in.ExpectedGeneration + 1, ExpiresAt: now.Add(time.Minute)}, nil
	case "ownership_mismatch":
		return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "stolen", Generation: in.ExpectedGeneration + 1, ExpiresAt: now.Add(time.Minute)}, nil
	case "stale_generation":
		return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: in.LeaseID, Generation: in.ExpectedGeneration - 1, ExpiresAt: now.Add(time.Minute)}, nil
	case "zero_expires":
		return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: in.LeaseID, Generation: in.ExpectedGeneration + 1}, nil
	case "expired":
		return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: in.LeaseID, Generation: in.ExpectedGeneration + 1, ExpiresAt: now.Add(-time.Second)}, nil
	case "negative_slots":
		return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: in.LeaseID, Generation: in.ExpectedGeneration + 1, ExpiresAt: now.Add(time.Minute), RemainingSlots: -1}, nil
	case "deny_occupancy":
		return authority.LeaseDecision{Kind: authority.LeaseDeny, LeaseID: in.LeaseID, Generation: in.ExpectedGeneration + 1, ExpiresAt: now.Add(time.Minute)}, nil
	case "advisory":
		return authority.LeaseDecision{Kind: authority.LeaseAdvisory, LeaseID: in.LeaseID, Generation: in.ExpectedGeneration + 1, ExpiresAt: now.Add(time.Minute)}, nil
	case "panic":
		panic(fmt.Sprintf("renew boom credential=%s", h.secret))
	default:
		return authority.LeaseDecision{
			Kind:       authority.LeaseAllow,
			LeaseID:    in.LeaseID,
			Generation: in.ExpectedGeneration + 1,
			ExpiresAt:  now.Add(200 * time.Millisecond),
			TTL:        200 * time.Millisecond,
		}, nil
	}
}

func (h *hostileRenewConcurrency) ReleaseLease(context.Context, authority.LeaseRelease) error {
	h.released.Add(1)
	return nil
}

func (h *hostileRenewConcurrency) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func TestPhase2_Heartbeat_RejectsHostileRenewalBeforeMutatingState(t *testing.T) {
	t.Parallel()
	modes := []string{
		"unknown_kind",
		"ownership_mismatch",
		"stale_generation",
		"zero_expires",
		"expired",
		"negative_slots",
		"deny_occupancy",
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			conc := &hostileRenewConcurrency{renewCh: make(chan struct{}, 4)}
			conc.mode.Store(mode)
			desc := authority.ProviderDescriptor{
				ID: "concurrency",
				Postures: []authority.StagePosture{{
					Stage:           authority.StageLeaseAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			}
			ex := &Executor{
				AccountingRuntime: AccountingRuntime{
					RequestCoordinator: &authoritycoord.RequestCoordinator{
						Concurrency:           conc,
						ConcurrencyDescriptor: &desc,
						CleanupTimeout:        time.Second,
					},
				},
			}
			ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-hostile-"+mode, "a1", "t1", scope.PrincipalScopeView{})
			if err != nil {
				t.Fatal(err)
			}
			st := requestAuthorityFrom(ctx)
			if st == nil {
				t.Fatal("missing authority state")
			}
			genBefore := st.LeaseGeneration
			expBefore := st.LeaseExpiresAt
			select {
			case <-conc.renewCh:
			case <-time.After(2 * time.Second):
				t.Fatal("expected renew attempt")
			}
			deadline := time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) {
				if st.LeaseGeneration != genBefore || !st.LeaseExpiresAt.Equal(expBefore) {
					t.Fatalf("hostile renew mutated state: gen %d->%d exp %v->%v",
						genBefore, st.LeaseGeneration, expBefore, st.LeaseExpiresAt)
				}
				if st.heartbeat != nil && st.heartbeat.Degraded() {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if st.LeaseGeneration != genBefore || !st.LeaseExpiresAt.Equal(expBefore) {
				t.Fatalf("hostile renew must not mutate lease state (req 4.1, 10.2)")
			}
			if st.heartbeat == nil || !st.heartbeat.Degraded() {
				t.Fatal("required malformed renew must fail closed / degrade (D5)")
			}
			if conc.released.Load() != 0 {
				t.Fatalf("hostile renew must not release; released=%d", conc.released.Load())
			}
			_ = ex.releaseRequestAuthority(ctx)
		})
	}
}

func TestPhase2_Heartbeat_AdvisoryRenewalDoesNotMutateState(t *testing.T) {
	t.Parallel()
	conc := &hostileRenewConcurrency{
		renewCh:         make(chan struct{}, 4),
		failureBehavior: authority.FailureFailOpen,
	}
	conc.mode.Store("advisory")
	desc := authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailOpen,
		}},
	}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:           conc,
				ConcurrencyDescriptor: &desc,
				CleanupTimeout:        time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-adv-renew", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	genBefore := st.LeaseGeneration
	expBefore := st.LeaseExpiresAt
	select {
	case <-conc.renewCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected renew attempt")
	}
	time.Sleep(100 * time.Millisecond)
	if st.LeaseGeneration != genBefore || !st.LeaseExpiresAt.Equal(expBefore) {
		t.Fatal("advisory renew must not silently mutate lease state (D5)")
	}
	_ = ex.releaseRequestAuthority(ctx)
}

func TestPhase2_Heartbeat_RenewPanicIsSafeWithoutPayload(t *testing.T) {
	t.Parallel()
	conc := &hostileRenewConcurrency{
		renewCh: make(chan struct{}, 4),
		secret:  "super-secret-token-xyz",
	}
	conc.mode.Store("panic")
	desc := authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:           conc,
				ConcurrencyDescriptor: &desc,
				CleanupTimeout:        time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-panic-renew", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	genBefore := st.LeaseGeneration
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conc.renewed.Load() > 0 && st.heartbeat != nil && st.heartbeat.Degraded() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conc.renewed.Load() == 0 {
		t.Fatal("expected renew panic attempt")
	}
	if st.heartbeat == nil || !st.heartbeat.Degraded() {
		t.Fatal("renew panic must degrade fail-closed heartbeat")
	}
	if st.LeaseGeneration != genBefore {
		t.Fatal("panic renew must not mutate generation")
	}
	_ = ex.releaseRequestAuthority(ctx)
}

func TestPhase2_Heartbeat_MultiLeaseTickIsAtomicOnSecondFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"second_malformed", "second_panic"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			conc := &partialMultiRenewConcurrency{
				renewCh: make(chan struct{}, 8),
				mode:    mode,
				secret:  "partial-renew-secret",
			}
			desc := authority.ProviderDescriptor{
				ID: "concurrency",
				Postures: []authority.StagePosture{{
					Stage:           authority.StageLeaseAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			}
			ex := &Executor{
				AccountingRuntime: AccountingRuntime{
					RequestCoordinator: &authoritycoord.RequestCoordinator{
						Concurrency:           conc,
						ConcurrencyDescriptor: &desc,
						CleanupTimeout:        time.Second,
					},
				},
			}
			ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-atomic-"+mode, "a1", "t1", scope.PrincipalScopeView{})
			if err != nil {
				t.Fatal(err)
			}
			st := requestAuthorityFrom(ctx)
			if st == nil || len(st.LeaseTargets) != 2 {
				t.Fatalf("want 2 lease targets, got %+v", st)
			}
			before := append([]leaseRenewTarget(nil), st.LeaseTargets...)
			genBefore := st.LeaseGeneration
			expBefore := st.LeaseExpiresAt
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if conc.renewed.Load() >= 2 && st.heartbeat != nil && st.heartbeat.Degraded() {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if conc.renewed.Load() < 2 {
				t.Fatalf("expected both renew attempts in tick; renewed=%d", conc.renewed.Load())
			}
			if st.LeaseGeneration != genBefore || !st.LeaseExpiresAt.Equal(expBefore) {
				t.Fatalf("partial tick must not mutate primary gen/expiry")
			}
			if len(st.LeaseTargets) != len(before) {
				t.Fatalf("target count changed")
			}
			for i := range before {
				if st.LeaseTargets[i].Generation != before[i].Generation || !st.LeaseTargets[i].ExpiresAt.Equal(before[i].ExpiresAt) {
					t.Fatalf("partial tick mutated target[%d]: before=%+v after=%+v", i, before[i], st.LeaseTargets[i])
				}
			}
			_ = ex.releaseRequestAuthority(ctx)
		})
	}
}

func TestPhase2_RequestCoordinator_RenewLeaseUsesValidateRenewalForWithoutDescriptor(t *testing.T) {
	t.Parallel()
	stub := &fixedExpiryConcurrency{expiresAt: time.Now().UTC().Add(time.Minute)}
	stub.foreignExtra = true
	c := &authoritycoord.RequestCoordinator{Concurrency: stub}
	_, err := c.RenewLease(context.Background(), authority.LeaseRenew{
		LeaseID: "L1", ExpectedGeneration: 1, TTL: time.Minute,
	})
	if err == nil {
		t.Fatal("nil ConcurrencyDescriptor must still ValidateRenewalFor (no bypass)")
	}
}

func TestPhase2_RequestCoordinator_RenewPanicStripsPayload(t *testing.T) {
	t.Parallel()
	secret := "renew-panic-credential-xyz"
	conc := &hostileRenewConcurrency{secret: secret}
	conc.mode.Store("panic")
	desc := authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
	c := &authoritycoord.RequestCoordinator{Concurrency: conc, ConcurrencyDescriptor: &desc}
	_, err := c.RenewLease(context.Background(), authority.LeaseRenew{LeaseID: "L1", ExpectedGeneration: 1})
	if err == nil {
		t.Fatal("expected panic isolation error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("panic error leaked payload: %v", err)
	}
}

func TestPhase2_RequestCoordinator_RenewLeaseUsesDescriptorAndNow(t *testing.T) {
	t.Parallel()
	fixed := time.Unix(1_700_000_100, 0).UTC()
	conc := &hostileRenewConcurrency{renewCh: make(chan struct{}, 1)}
	conc.mode.Store("expired")
	// Override RenewLease wall clock: expired relative to fixed Now requires ExpiresAt <= fixed.
	desc := authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
	c := &authoritycoord.RequestCoordinator{
		Concurrency:           conc,
		ConcurrencyDescriptor: &desc,
		Now:                   func() time.Time { return fixed },
	}
	// Provider returns ExpiresAt = wall now - 1s which may still be after fixed if clocks differ.
	// Call RenewLease through coordinator with a decision that ValidateRenewalFor accepts structurally
	// but Now rejects: use a stub that returns expires = fixed.
	stub := &fixedExpiryConcurrency{expiresAt: fixed}
	c.Concurrency = stub
	_, err := c.RenewLease(context.Background(), authority.LeaseRenew{
		LeaseID: "L1", ExpectedGeneration: 1, TTL: time.Minute,
	})
	if err == nil {
		t.Fatal("RenewLease must reject already-expired renewal against injected Now (req 4.1, 10.2)")
	}
}

type fixedExpiryConcurrency struct {
	expiresAt    time.Time
	foreignExtra bool
}

func (f *fixedExpiryConcurrency) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{}, nil
}

func (f *fixedExpiryConcurrency) RenewLease(_ context.Context, in authority.LeaseRenew) (authority.LeaseDecision, error) {
	now := f.expiresAt
	if now.IsZero() {
		now = time.Now().UTC().Add(time.Minute)
	}
	d := authority.LeaseDecision{
		Kind: authority.LeaseAllow, LeaseID: in.LeaseID, Generation: in.ExpectedGeneration + 1, ExpiresAt: now, TTL: time.Minute,
	}
	if f.foreignExtra {
		d.Leases = []authority.LeaseOccupancy{
			{LeaseID: in.LeaseID, Generation: d.Generation, ExpiresAt: now},
			{LeaseID: "foreign-extra", Generation: d.Generation, ExpiresAt: now},
		}
	}
	return d, nil
}

func (f *fixedExpiryConcurrency) ReleaseLease(context.Context, authority.LeaseRelease) error {
	return nil
}

func (f *fixedExpiryConcurrency) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

type partialMultiRenewConcurrency struct {
	released atomic.Int32
	renewed  atomic.Int32
	renewCh  chan struct{}
	mode     string
	secret   string
}

func (p *partialMultiRenewConcurrency) AdmitLease(_ context.Context, _ authority.LeaseAdmission) (authority.LeaseDecision, error) {
	ttl := 150 * time.Millisecond
	exp := time.Now().UTC().Add(ttl)
	leases := []authority.LeaseOccupancy{
		{LeaseID: "lease-a", Generation: 1, RuleID: "rule-a", ExpiresAt: exp, RenewBefore: 100 * time.Millisecond, TTL: ttl, FailureBehavior: authority.FailureFailClosed},
		{LeaseID: "lease-b", Generation: 1, RuleID: "rule-b", ExpiresAt: exp, RenewBefore: 100 * time.Millisecond, TTL: ttl, FailureBehavior: authority.FailureFailClosed},
	}
	return authority.LeaseDecision{
		Kind:            authority.LeaseAllow,
		LeaseID:         "lease-b",
		Generation:      1,
		ExpiresAt:       exp,
		RenewBefore:     100 * time.Millisecond,
		TTL:             ttl,
		FailureBehavior: authority.FailureFailClosed,
		Leases:          leases,
	}, nil
}

func (p *partialMultiRenewConcurrency) RenewLease(_ context.Context, in authority.LeaseRenew) (authority.LeaseDecision, error) {
	n := p.renewed.Add(1)
	select {
	case p.renewCh <- struct{}{}:
	default:
	}
	now := time.Now().UTC()
	if n == 1 {
		return authority.LeaseDecision{
			Kind: authority.LeaseAllow, LeaseID: in.LeaseID, Generation: in.ExpectedGeneration + 1,
			ExpiresAt: now.Add(time.Minute), TTL: time.Minute,
			Leases: []authority.LeaseOccupancy{{LeaseID: in.LeaseID, Generation: in.ExpectedGeneration + 1, RuleID: in.RuleID, ExpiresAt: now.Add(time.Minute)}},
		}, nil
	}
	switch p.mode {
	case "second_panic":
		panic(fmt.Sprintf("multi renew boom credential=%s", p.secret))
	default:
		return authority.LeaseDecision{Kind: "weird", LeaseID: in.LeaseID, Generation: in.ExpectedGeneration + 1, ExpiresAt: now.Add(time.Minute)}, nil
	}
}

func (p *partialMultiRenewConcurrency) ReleaseLease(context.Context, authority.LeaseRelease) error {
	p.released.Add(1)
	return nil
}

func (p *partialMultiRenewConcurrency) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}
