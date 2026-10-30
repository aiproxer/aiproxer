package authoritycoord_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

type fakeAttemptProvider struct {
	id                string
	admit             func(context.Context, authority.AttemptAdmission) (authority.Decision, error)
	settle            func(context.Context, authority.AttemptSettlement) (authority.Settlement, error)
	released          atomic.Int32
	settled           atomic.Int32
	lastSettleHandles atomic.Value // []string
}

func (f *fakeAttemptProvider) AdmitAttempt(ctx context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	if f.admit != nil {
		return f.admit(ctx, in)
	}
	return authority.Decision{
		Kind:         authority.DecisionAllow,
		Reservations: []authority.Reservation{spendReservation(f.id + "-h")},
	}, nil
}

func (f *fakeAttemptProvider) SettleAttempt(ctx context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	f.settled.Add(1)
	handles := append([]string(nil), in.Handles...)
	f.lastSettleHandles.Store(handles)
	if f.settle != nil {
		return f.settle(ctx, in)
	}
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (f *fakeAttemptProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	f.released.Add(1)
	return nil
}

func TestAttemptCoordinator_IndependentBLegsAndCompensate(t *testing.T) {
	t.Parallel()
	spend := &fakeAttemptProvider{id: "spend"}
	deny := &fakeAttemptProvider{id: "quota"}
	deny.admit = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{Kind: authority.DecisionDeny}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "spend", Class: authoritycoord.AttemptPriorityHardSpend, Provider: spend},
			{ID: "quota", Class: authoritycoord.AttemptPriorityQuotaRate, Provider: deny},
		},
		CleanupTimeout: time.Second,
	}
	_, err := coord.Admit(context.Background(), validAttemptAdmission("b1"))
	if !authoritycoord.IsDenied(err) {
		t.Fatalf("got %v", err)
	}
	if spend.released.Load() != 1 {
		t.Fatalf("spend released=%d", spend.released.Load())
	}

	// Second B-leg with allow-only still independent.
	okCoord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "spend2", Class: authoritycoord.AttemptPriorityHardSpend, Provider: &fakeAttemptProvider{id: "spend2"}},
		},
	}
	d, err := okCoord.Admit(context.Background(), validAttemptAdmission("b2"))
	if err != nil || d.Kind != authority.DecisionAllow {
		t.Fatalf("b2 err=%v d=%+v", err, d)
	}
}

func TestAttemptCoordinator_AggregatesBoundVersions(t *testing.T) {
	t.Parallel()
	prov := &fakeAttemptProvider{id: "spend"}
	prov.admit = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind: authority.DecisionAllow,
			BoundVersions: []economics.PolicySnapshotRef{{
				VersionRef: economics.VersionRef{ID: "usage_authority", Version: "v1"},
				PolicyID:   "usage_authority",
			}},
			Reservations: []authority.Reservation{spendReservation("h1")},
		}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "spend", Class: authoritycoord.AttemptPriorityHardSpend, Provider: prov},
		},
	}
	d, err := coord.Admit(context.Background(), validAttemptAdmission("b-bound"))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if len(d.BoundVersions) != 1 || d.BoundVersions[0].Version != "v1" {
		t.Fatalf("bound versions = %+v", d.BoundVersions)
	}
}

func TestAttemptCoordinator_MixedCurrencyClampReleasesCurrentHolds(t *testing.T) {
	t.Parallel()
	usd := &fakeAttemptProvider{id: "usd"}
	usd.admit = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:         authority.DecisionAllow,
			Reservations: []authority.Reservation{spendReservation("usd-h")},
			Clamps: []authority.Clamp{{
				Kind:  authority.ClampMaxSpend,
				Money: economics.Money{NanoUnits: 100, Currency: "USD", Present: true},
			}},
		}, nil
	}
	eur := &fakeAttemptProvider{id: "eur"}
	eur.admit = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:         authority.DecisionAllow,
			Reservations: []authority.Reservation{spendReservation("eur-h")},
			Clamps: []authority.Clamp{{
				Kind:  authority.ClampMaxSpend,
				Money: economics.Money{NanoUnits: 50, Currency: "EUR", Present: true},
			}},
		}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "usd", Class: authoritycoord.AttemptPriorityHardSpend, Provider: usd, Strength: authority.StrengthRequired},
			{ID: "eur", Class: authoritycoord.AttemptPriorityHardSpend, Provider: eur, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	_, err := coord.Admit(context.Background(), validAttemptAdmission("b-mixed"))
	if err == nil {
		t.Fatal("expected mixed-currency clamp merge failure")
	}
	if usd.released.Load() != 1 {
		t.Fatalf("prior usd hold released=%d want 1", usd.released.Load())
	}
	if eur.released.Load() != 1 {
		t.Fatalf("current eur hold released=%d want 1 (must not leak on clamp merge failure)", eur.released.Load())
	}
}

func validAttemptAdmission(bleg string) authority.AttemptAdmission {
	return authority.AttemptAdmission{
		RequestID:   "req-1",
		AttemptID:   bleg,
		BLegID:      bleg,
		ALegID:      "a-1",
		Perspective: metering.PerspectiveOperator,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
		},
	}
}

func TestAttemptCoordinator_SettleRoutesHandlesToOwningProvider(t *testing.T) {
	t.Parallel()
	builtin := &fakeAttemptProvider{id: "usage-authority-attempt"}
	external := &fakeAttemptProvider{id: "enterprise-attempt"}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "usage-authority-attempt", Class: authoritycoord.AttemptPriorityHardSpend, Provider: builtin, Strength: authority.StrengthRequired},
			{ID: "enterprise-attempt", Class: authoritycoord.AttemptPriorityQuotaRate, Provider: external, Strength: authority.StrengthRequired},
		},
	}
	d, err := coord.Admit(context.Background(), validAttemptAdmission("b-mixed"))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if err := coord.Settle(context.Background(), d.Stack, authority.AttemptSettlement{
		RequestID: "req-1",
		AttemptID: "b-mixed",
		BLegID:    "b-mixed",
		Handles:   d.Stack.Handles(),
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if builtin.settled.Load() != 1 || external.settled.Load() != 1 {
		t.Fatalf("settled builtin=%d external=%d want 1 each", builtin.settled.Load(), external.settled.Load())
	}
	builtinHandles, _ := builtin.lastSettleHandles.Load().([]string)
	externalHandles, _ := external.lastSettleHandles.Load().([]string)
	if len(builtinHandles) != 1 || builtinHandles[0] != "usage-authority-attempt-h" {
		t.Fatalf("builtin handles=%v want [usage-authority-attempt-h]", builtinHandles)
	}
	if len(externalHandles) != 1 || externalHandles[0] != "enterprise-attempt-h" {
		t.Fatalf("external handles=%v want [enterprise-attempt-h]", externalHandles)
	}
}

func TestAttemptCoordinator_RejectsUnknownPriorityClass(t *testing.T) {
	t.Parallel()
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "spend", Class: authoritycoord.AttemptPriorityUnknown, Provider: &fakeAttemptProvider{id: "spend"}},
		},
	}
	_, err := coord.Admit(context.Background(), validAttemptAdmission("b-unknown"))
	if err == nil {
		t.Fatal("unknown attempt priority class must be rejected")
	}
	if !strings.Contains(err.Error(), "unknown attempt priority class") {
		t.Fatalf("got %v", err)
	}
}
