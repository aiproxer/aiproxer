package authoritycoord_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

type fakeRequestProvider struct {
	id                  string
	admit               func(context.Context, authority.RequestAdmission) (authority.Decision, error)
	settle              func(context.Context, authority.RequestSettlement) (authority.Settlement, error)
	released            atomic.Int32
	settled             atomic.Int32
	lastSettleHandles   atomic.Value // []string
	lastSettleCtxActive atomic.Bool  // true when SettleRequest saw a non-canceled ctx
}

func (f *fakeRequestProvider) AdmitRequest(ctx context.Context, in authority.RequestAdmission) (authority.Decision, error) {
	if f.admit != nil {
		return f.admit(ctx, in)
	}
	return authority.Decision{
		Kind:         authority.DecisionAllow,
		Reservations: []authority.Reservation{quotaReservation(f.id + "-h")},
	}, nil
}

func (f *fakeRequestProvider) SettleRequest(ctx context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	f.settled.Add(1)
	f.lastSettleHandles.Store(append([]string(nil), in.Handles...))
	f.lastSettleCtxActive.Store(ctx != nil && ctx.Err() == nil)
	if f.settle != nil {
		return f.settle(ctx, in)
	}
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (f *fakeRequestProvider) ReleaseRequest(ctx context.Context, in authority.RequestRelease) error {
	_ = ctx
	_ = in
	f.released.Add(1)
	return nil
}

func TestRequestCoordinator_PriorityAndReverseCompensate(t *testing.T) {
	t.Parallel()
	wallet := &fakeRequestProvider{id: "wallet"}
	quota := &fakeRequestProvider{id: "quota"}
	quota.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{Kind: authority.DecisionDeny, ProviderID: "quota"}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: quota, Strength: authority.StrengthRequired},
			{ID: "wallet", Class: authoritycoord.PriorityCreditWallet, Provider: wallet, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if !authoritycoord.IsDenied(err) {
		t.Fatalf("got %v", err)
	}
	if wallet.released.Load() != 1 {
		t.Fatalf("wallet released=%d want 1 (reverse compensate)", wallet.released.Load())
	}
}

func TestRequestCoordinator_NilConcurrencySkipped(t *testing.T) {
	t.Parallel()
	quota := &fakeRequestProvider{id: "quota"}
	coord := &authoritycoord.RequestCoordinator{
		Concurrency: nil,
		Slots: []authoritycoord.RequestSlot{
			{ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: quota},
		},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != authority.DecisionAllow || len(d.Stack.Handles()) != 1 {
		t.Fatalf("decision=%+v", d)
	}
}

func TestRequestCoordinator_AdvisoryFailOpen(t *testing.T) {
	t.Parallel()
	adv := &fakeRequestProvider{id: "adv"}
	adv.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{}, errors.New("observer down")
	}
	hard := &fakeRequestProvider{id: "hard"}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "hard", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: hard, Strength: authority.StrengthRequired},
			{ID: "adv", Class: authoritycoord.PriorityAdvisory, Provider: adv, Strength: authority.StrengthAdvisory, FailureBehavior: authority.FailureFailOpen},
		},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != authority.DecisionAllow {
		t.Fatalf("kind=%s", d.Kind)
	}
}

func TestMergeClampsNonWidening(t *testing.T) {
	t.Parallel()
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "a", Class: authoritycoord.PriorityQuotaBudgetRate,
			Provider: &fakeRequestProvider{id: "a", admit: func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
				return authority.Decision{
					Kind:   authority.DecisionAllow,
					Clamps: []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: 100}},
				}, nil
			}},
		}, {
			ID: "b", Class: authoritycoord.PriorityAdvisory,
			Provider: &fakeRequestProvider{id: "b", admit: func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
				return authority.Decision{
					Kind:   authority.DecisionAllow,
					Clamps: []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: 50}},
				}, nil
			}},
		}},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Clamps) != 1 || d.Clamps[0].Value != 50 {
		t.Fatalf("clamps=%+v", d.Clamps)
	}
}

func validRequestAdmission() authority.RequestAdmission {
	return authority.RequestAdmission{
		RequestID:   "req-1",
		ALegID:      "a-1",
		Perspective: metering.PerspectiveCustomer,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveCustomer,
			Boundary:    metering.BoundaryFrontendIngress,
			Lifecycle:   metering.LifecycleLogicalRequest,
		},
	}
}

func TestRequestCoordinator_SettleRoutesHandlesToOwningProvider(t *testing.T) {
	t.Parallel()
	conc := &fakeConcurrencyProvider{}
	quota := &fakeRequestProvider{id: "usage-authority-request"}
	wallet := &fakeRequestProvider{id: "wallet"}
	coord := &authoritycoord.RequestCoordinator{
		Concurrency: conc,
		Slots: []authoritycoord.RequestSlot{
			{ID: "wallet", Class: authoritycoord.PriorityCreditWallet, Provider: wallet, Strength: authority.StrengthRequired},
			{ID: "usage-authority-request", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: quota, Strength: authority.StrengthRequired},
		},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if err := coord.Settle(context.Background(), d.Stack, authority.RequestSettlement{
		RequestID: "req-1",
		Handles:   d.Stack.Handles(),
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if quota.settled.Load() != 1 || wallet.settled.Load() != 1 {
		t.Fatalf("settled quota=%d wallet=%d want 1 each", quota.settled.Load(), wallet.settled.Load())
	}
	quotaHandles, _ := quota.lastSettleHandles.Load().([]string)
	walletHandles, _ := wallet.lastSettleHandles.Load().([]string)
	if len(quotaHandles) != 1 || quotaHandles[0] != "usage-authority-request-h" {
		t.Fatalf("quota handles=%v want [usage-authority-request-h] (concurrency lease must not be included)", quotaHandles)
	}
	if len(walletHandles) != 1 || walletHandles[0] != "wallet-h" {
		t.Fatalf("wallet handles=%v want [wallet-h]", walletHandles)
	}
	for _, h := range append(quotaHandles, walletHandles...) {
		if h == d.Lease.LeaseID {
			t.Fatalf("concurrency lease ID %q must not be sent to request-provider settlement", h)
		}
	}
}

func TestRequestCoordinator_SettleUsesFreshCleanupContext(t *testing.T) {
	t.Parallel()
	quota := &fakeRequestProvider{id: "quota"}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: quota, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := coord.Settle(canceled, d.Stack, authority.RequestSettlement{
		RequestID: "req-1",
		Handles:   d.Stack.Handles(),
	}); err != nil {
		t.Fatalf("settle on canceled parent: %v", err)
	}
	if quota.settled.Load() != 1 {
		t.Fatalf("settled=%d want 1", quota.settled.Load())
	}
	if !quota.lastSettleCtxActive.Load() {
		t.Fatal("SettleRequest must receive fresh non-canceled cleanup context")
	}
}
