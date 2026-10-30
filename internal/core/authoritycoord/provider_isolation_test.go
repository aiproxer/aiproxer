package authoritycoord_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

// Requirement 15.9: panics / malformed enterprise provider responses must be
// isolated and mapped through stable failure posture (fail-closed by default).

func TestRequestCoordinator_IsolatesProviderPanicFailClosed(t *testing.T) {
	t.Parallel()
	panicProv := &fakeRequestProvider{id: "enterprise"}
	panicProv.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		panic("enterprise provider boom")
	}
	prior := &fakeRequestProvider{id: "prior"}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "prior", Class: authoritycoord.PriorityCreditWallet, Provider: prior, Strength: authority.StrengthRequired},
			{ID: "enterprise", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: panicProv, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expected unavailable error from provider panic")
	}
	var unavail *authoritycoord.UnavailableError
	if !errors.As(err, &unavail) {
		t.Fatalf("want UnavailableError, got %T %v", err, err)
	}
	if unavail.ProviderID != "enterprise" {
		t.Fatalf("provider id=%q", unavail.ProviderID)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "panic") {
		t.Fatalf("error should mention panic: %v", err)
	}
	if d.Kind != authority.DecisionDeny {
		t.Fatalf("kind=%s want deny", d.Kind)
	}
	if prior.released.Load() != 1 {
		t.Fatalf("prior hold must reverse-compensate on panic; released=%d", prior.released.Load())
	}
}

func TestRequestCoordinator_IsolatesMalformedDecisionKind(t *testing.T) {
	t.Parallel()
	bad := &fakeRequestProvider{id: "malformed"}
	bad.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{Kind: authority.DecisionKind("not-a-kind"), ProviderID: "malformed"}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "malformed", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: bad, Strength: authority.StrengthRequired},
		},
	}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expected unavailable error from malformed decision")
	}
	var unavail *authoritycoord.UnavailableError
	if !errors.As(err, &unavail) {
		t.Fatalf("want UnavailableError, got %T %v", err, err)
	}
	if unavail.ProviderID != "malformed" {
		t.Fatalf("provider id=%q", unavail.ProviderID)
	}
}

func TestRequestCoordinator_MalformedDecisionReleasesOwnHold(t *testing.T) {
	t.Parallel()
	prior := &fakeRequestProvider{id: "prior"}
	bad := &fakeRequestProvider{id: "malformed"}
	bad.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:         authority.DecisionKind("not-a-kind"),
			ProviderID:   "malformed",
			Reservations: []authority.Reservation{quotaReservation("malformed-hold")},
		}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "prior", Class: authoritycoord.PriorityCreditWallet, Provider: prior, Strength: authority.StrengthRequired},
			{ID: "malformed", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: bad, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expected unavailable error from malformed decision")
	}
	if _, ok := errors.AsType[*authoritycoord.UnavailableError](err); !ok {
		t.Fatalf("want UnavailableError, got %T %v", err, err)
	}
	if d.Kind != authority.DecisionDeny {
		t.Fatalf("kind=%s want deny", d.Kind)
	}
	if prior.released.Load() != 1 {
		t.Fatalf("prior hold must reverse-compensate; released=%d", prior.released.Load())
	}
	if bad.released.Load() != 1 {
		t.Fatalf("malformed provider hold must reverse-compensate; released=%d", bad.released.Load())
	}
}

func TestRequestCoordinator_AdvisoryMalformedDecisionReleasesOwnHold(t *testing.T) {
	t.Parallel()
	hard := &fakeRequestProvider{id: "hard"}
	adv := &fakeRequestProvider{id: "adv"}
	adv.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:         authority.DecisionKind("not-a-kind"),
			ProviderID:   "adv",
			Reservations: []authority.Reservation{quotaReservation("adv-hold")},
		}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "hard", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: hard, Strength: authority.StrengthRequired},
			{ID: "adv", Class: authoritycoord.PriorityAdvisory, Provider: adv, Strength: authority.StrengthAdvisory, FailureBehavior: authority.FailureFailOpen},
		},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatalf("advisory malformed must fail-open: %v", err)
	}
	if d.Kind != authority.DecisionAllow {
		t.Fatalf("kind=%s", d.Kind)
	}
	if hard.released.Load() != 0 {
		t.Fatalf("hard hold must remain; released=%d", hard.released.Load())
	}
	if adv.released.Load() != 1 {
		t.Fatalf("advisory malformed hold must reverse-compensate; released=%d", adv.released.Load())
	}
}

func TestRequestCoordinator_AdvisoryPanicDegradesNotDeny(t *testing.T) {
	t.Parallel()
	adv := &fakeRequestProvider{id: "adv"}
	adv.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		panic("advisory observer panic")
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
		t.Fatalf("advisory panic must fail-open: %v", err)
	}
	if d.Kind != authority.DecisionAllow {
		t.Fatalf("kind=%s", d.Kind)
	}
	if d.Readiness != authority.ReadinessDegraded {
		t.Fatalf("readiness=%s want degraded", d.Readiness)
	}
}

func TestRequestCoordinator_IsolatesConcurrencyPanic(t *testing.T) {
	t.Parallel()
	conc := &fakeConcurrencyProvider{
		admit: func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
			panic("concurrency provider boom")
		},
	}
	coord := &authoritycoord.RequestCoordinator{Concurrency: conc}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expected unavailable error from concurrency panic")
	}
	var unavail *authoritycoord.UnavailableError
	if !errors.As(err, &unavail) {
		t.Fatalf("want UnavailableError, got %T %v", err, err)
	}
	if unavail.ProviderID != "concurrency" {
		t.Fatalf("provider id=%q", unavail.ProviderID)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "panic") {
		t.Fatalf("error should mention panic: %v", err)
	}
}

func TestRequestCoordinator_IsolatesMalformedLeaseDecisionKind(t *testing.T) {
	t.Parallel()
	conc := &fakeConcurrencyProvider{
		admit: func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
			return authority.LeaseDecision{Kind: authority.LeaseDecisionKind("not-a-lease-kind")}, nil
		},
	}
	coord := &authoritycoord.RequestCoordinator{Concurrency: conc}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expected unavailable error from malformed lease decision")
	}
	var unavail *authoritycoord.UnavailableError
	if !errors.As(err, &unavail) {
		t.Fatalf("want UnavailableError, got %T %v", err, err)
	}
	if unavail.ProviderID != "concurrency" {
		t.Fatalf("provider id=%q", unavail.ProviderID)
	}
}

func TestRequestCoordinator_MalformedLeaseDecisionReleasesOwnHold(t *testing.T) {
	t.Parallel()
	conc := &fakeConcurrencyProvider{
		admit: func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
			return authority.LeaseDecision{
				Kind:    authority.LeaseDecisionKind("not-a-lease-kind"),
				LeaseID: "leaked-lease",
			}, nil
		},
	}
	coord := &authoritycoord.RequestCoordinator{
		Concurrency:    conc,
		CleanupTimeout: time.Second,
	}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expected unavailable error from malformed lease decision")
	}
	if conc.released.Load() != 1 {
		t.Fatalf("malformed lease hold must reverse-compensate; released=%d", conc.released.Load())
	}
}

func TestAttemptCoordinator_IsolatesProviderPanic(t *testing.T) {
	t.Parallel()
	panicProv := &fakeAttemptProvider{id: "enterprise-attempt"}
	panicProv.admit = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		panic("attempt provider boom")
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "enterprise-attempt", Class: authoritycoord.AttemptPriorityHardSpend, Provider: panicProv, Strength: authority.StrengthRequired},
		},
	}
	_, err := coord.Admit(context.Background(), validAttemptAdmission("b-panic"))
	if err == nil {
		t.Fatal("expected unavailable error from attempt provider panic")
	}
	if _, ok := errors.AsType[*authoritycoord.UnavailableError](err); !ok {
		t.Fatalf("want UnavailableError, got %T %v", err, err)
	}
}

func TestAttemptCoordinator_MalformedDecisionReleasesOwnHold(t *testing.T) {
	t.Parallel()
	prior := &fakeAttemptProvider{id: "prior"}
	bad := &fakeAttemptProvider{id: "malformed"}
	bad.admit = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:         authority.DecisionKind("not-a-kind"),
			ProviderID:   "malformed",
			Reservations: []authority.Reservation{spendReservation("malformed-attempt-hold")},
		}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "prior", Class: authoritycoord.AttemptPriorityHardSpend, Provider: prior, Strength: authority.StrengthRequired},
			{ID: "malformed", Class: authoritycoord.AttemptPriorityQuotaRate, Provider: bad, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validAttemptAdmission("b-malformed"))
	if err == nil {
		t.Fatal("expected unavailable error from malformed attempt decision")
	}
	if _, ok := errors.AsType[*authoritycoord.UnavailableError](err); !ok {
		t.Fatalf("want UnavailableError, got %T %v", err, err)
	}
	if d.Kind != authority.DecisionDeny {
		t.Fatalf("kind=%s want deny", d.Kind)
	}
	if prior.released.Load() != 1 {
		t.Fatalf("prior hold must reverse-compensate; released=%d", prior.released.Load())
	}
	if bad.released.Load() != 1 {
		t.Fatalf("malformed attempt hold must reverse-compensate; released=%d", bad.released.Load())
	}
}

func TestRequestCoordinator_IsolatesSettleRequestPanicFailClosed(t *testing.T) {
	t.Parallel()
	panicProv := &fakeRequestProvider{id: "enterprise"}
	panicProv.settle = func(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
		panic("settle request boom")
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "enterprise", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: panicProv, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	err = coord.Settle(context.Background(), d.Stack, authority.RequestSettlement{
		RequestID: "req-1",
		Handles:   d.Stack.Handles(),
	})
	if err == nil {
		t.Fatal("expected unavailable error from SettleRequest panic")
	}
	var unavail *authoritycoord.UnavailableError
	if !errors.As(err, &unavail) {
		t.Fatalf("want UnavailableError, got %T %v", err, err)
	}
	if unavail.ProviderID != "enterprise" {
		t.Fatalf("provider id=%q", unavail.ProviderID)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "panic") {
		t.Fatalf("error should mention panic: %v", err)
	}
}

func TestRequestCoordinator_AdvisorySettlePanicRemainsObservable(t *testing.T) {
	t.Parallel()
	hard := &fakeRequestProvider{id: "hard"}
	adv := &fakeRequestProvider{id: "adv"}
	adv.settle = func(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
		panic("advisory settle boom")
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "hard", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: hard, Strength: authority.StrengthRequired},
			{ID: "adv", Class: authoritycoord.PriorityAdvisory, Provider: adv, Strength: authority.StrengthAdvisory, FailureBehavior: authority.FailureFailOpen},
		},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if err := coord.Settle(context.Background(), d.Stack, authority.RequestSettlement{
		RequestID: "req-1",
		Handles:   d.Stack.Handles(),
	}); err == nil {
		t.Fatal("advisory settle panic must remain observable for reconciliation retry")
	}
	if hard.settled.Load() != 1 {
		t.Fatalf("hard settled=%d want 1", hard.settled.Load())
	}
}

func TestRequestCoordinator_SettleEmptyStackDoesNotBroadcastHandles(t *testing.T) {
	t.Parallel()
	quota := &fakeRequestProvider{id: "quota"}
	wallet := &fakeRequestProvider{id: "wallet"}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "wallet", Class: authoritycoord.PriorityCreditWallet, Provider: wallet, Strength: authority.StrengthRequired},
			{ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: quota, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	var empty authoritycoord.CompensationStack
	if err := coord.Settle(context.Background(), empty, authority.RequestSettlement{
		RequestID: "req-1",
		Handles:   []string{"orphan-h1", "orphan-h2"},
	}); err != nil {
		t.Fatalf("empty stack settle must be no-op: %v", err)
	}
	if quota.settled.Load() != 0 || wallet.settled.Load() != 0 {
		t.Fatalf("empty stack must not broadcast Handles; settled quota=%d wallet=%d",
			quota.settled.Load(), wallet.settled.Load())
	}
}

func TestAttemptCoordinator_SettleEmptyStackDoesNotBroadcastHandles(t *testing.T) {
	t.Parallel()
	spend := &fakeAttemptProvider{id: "spend"}
	quota := &fakeAttemptProvider{id: "quota"}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "spend", Class: authoritycoord.AttemptPriorityHardSpend, Provider: spend, Strength: authority.StrengthRequired},
			{ID: "quota", Class: authoritycoord.AttemptPriorityQuotaRate, Provider: quota, Strength: authority.StrengthRequired},
		},
	}
	var empty authoritycoord.CompensationStack
	if err := coord.Settle(context.Background(), empty, authority.AttemptSettlement{
		RequestID: "req-1",
		AttemptID: "b-empty",
		BLegID:    "b-empty",
		Handles:   []string{"orphan-h1", "orphan-h2"},
	}); err != nil {
		t.Fatalf("empty stack settle must be no-op: %v", err)
	}
	if spend.settled.Load() != 0 || quota.settled.Load() != 0 {
		t.Fatalf("empty stack must not broadcast Handles; settled spend=%d quota=%d",
			spend.settled.Load(), quota.settled.Load())
	}
}

func TestAttemptCoordinator_IsolatesSettleAttemptPanicFailClosed(t *testing.T) {
	t.Parallel()
	panicProv := &fakeAttemptProvider{id: "enterprise-attempt"}
	panicProv.settle = func(context.Context, authority.AttemptSettlement) (authority.Settlement, error) {
		panic("settle attempt boom")
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "enterprise-attempt", Class: authoritycoord.AttemptPriorityHardSpend, Provider: panicProv, Strength: authority.StrengthRequired},
		},
	}
	d, err := coord.Admit(context.Background(), validAttemptAdmission("b-settle-panic"))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	err = coord.Settle(context.Background(), d.Stack, authority.AttemptSettlement{
		RequestID: "req-1",
		AttemptID: "b-settle-panic",
		BLegID:    "b-settle-panic",
		Handles:   d.Stack.Handles(),
	})
	if err == nil {
		t.Fatal("expected unavailable error from SettleAttempt panic")
	}
	var unavail *authoritycoord.UnavailableError
	if !errors.As(err, &unavail) {
		t.Fatalf("want UnavailableError, got %T %v", err, err)
	}
	if unavail.ProviderID != "enterprise-attempt" {
		t.Fatalf("provider id=%q", unavail.ProviderID)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "panic") {
		t.Fatalf("error should mention panic: %v", err)
	}
}

func TestAttemptCoordinator_AdvisorySettleFailureRemainsObservable(t *testing.T) {
	t.Parallel()
	hard := &fakeAttemptProvider{id: "hard"}
	adv := &fakeAttemptProvider{id: "adv"}
	adv.settle = func(context.Context, authority.AttemptSettlement) (authority.Settlement, error) {
		panic("advisory settle boom")
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "hard", Class: authoritycoord.AttemptPriorityHardSpend, Provider: hard, Strength: authority.StrengthRequired},
			{ID: "adv", Class: authoritycoord.AttemptPriorityAdvisory, Provider: adv, Strength: authority.StrengthAdvisory, FailureBehavior: authority.FailureFailOpen},
		},
	}
	d, err := coord.Admit(context.Background(), validAttemptAdmission("b-adv-settle"))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if err := coord.Settle(context.Background(), d.Stack, authority.AttemptSettlement{
		RequestID: "req-1",
		AttemptID: "b-adv-settle",
		BLegID:    "b-adv-settle",
		Handles:   d.Stack.Handles(),
	}); err == nil {
		t.Fatal("advisory settle failure must remain observable for reconciliation retry (req 15.5)")
	}
	if hard.settled.Load() != 1 {
		t.Fatalf("hard settled=%d want 1", hard.settled.Load())
	}
}

func TestCompensationStack_IsolatesReleasePanic(t *testing.T) {
	t.Parallel()
	var stack authoritycoord.CompensationStack
	stack.Push(authoritycoord.StackEntry{
		ProviderID: "ok",
		Handle:     "h-ok",
		Compensate: func(context.Context) error { return nil },
	})
	stack.Push(authoritycoord.StackEntry{
		ProviderID: "boom",
		Handle:     "h-boom",
		Compensate: func(context.Context) error {
			panic("release panic")
		},
	})
	failed := stack.ReverseCompensate(context.Background(), time.Second)
	if len(failed) != 1 {
		t.Fatalf("failed=%+v want one isolated panic failure", failed)
	}
	if failed[0].ProviderID != "boom" {
		t.Fatalf("provider=%q", failed[0].ProviderID)
	}
	if !strings.Contains(strings.ToLower(failed[0].Err.Error()), "panic") {
		t.Fatalf("err=%v", failed[0].Err)
	}
}
