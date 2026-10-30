package authoritycoord_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Phase 2.1 RED hostile-provider / coordinator contracts
// (requirements 3.1–3.9, 4.1–4.10, 13.1, 13.3; design D4, D5, D14, D17).
//
// Unpreviewed exposure-increasing admit clamps are rejected on the runtime
// product path by Executor.enforcePostAdmitClamps (see final_backend_exposure_test).
// Coordinator PreviewClamps remains side-effect-free clamp merge only.

func TestPhase2_RejectsEmptyProviderIDInventedFromClassIndex(t *testing.T) {
	t.Parallel()
	prov := &fakeRequestProvider{id: "anon"}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired},
		},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	for _, e := range d.Stack.Entries() {
		if strings.HasPrefix(e.ProviderID, "class-") {
			t.Fatalf("invented index provider id %q forbidden (req 3.9)", e.ProviderID)
		}
	}
	if err == nil {
		t.Fatal("empty provider ID must be rejected at admit/composition (req 3.2, 3.9)")
	}
}

func TestPhase2_RejectsDuplicateProviderIDs(t *testing.T) {
	t.Parallel()
	a := &fakeRequestProvider{id: "dup"}
	b := &fakeRequestProvider{id: "dup"}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "dup", Class: authoritycoord.PriorityCreditWallet, Provider: a, Strength: authority.StrengthRequired},
			{ID: "dup", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: b, Strength: authority.StrengthRequired},
		},
	}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("duplicate provider IDs must be rejected (req 3.2)")
	}
}

func TestPhase2_RequestForeignProviderID_UnavailableAndCompensates(t *testing.T) {
	t.Parallel()
	prior := &fakeRequestProvider{id: "prior"}
	hostile := &fakeRequestProvider{id: "slot-ok"}
	hostile.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:       authority.DecisionAllow,
			ProviderID: "foreign-not-slot",
			Reservations: []authority.Reservation{{
				Handle:   "hostile-h",
				Kind:     authority.ReservationQuota,
				Quantity: &metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
			}},
		}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "prior", Class: authoritycoord.PriorityCreditWallet, Provider: prior, Strength: authority.StrengthRequired},
			{ID: "slot-ok", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: hostile, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	var unavail *authoritycoord.UnavailableError
	if !errors.As(err, &unavail) {
		t.Fatalf("want UnavailableError for foreign ProviderID, got %T %v", err, err)
	}
	if unavail.ProviderID != "slot-ok" {
		t.Fatalf("unavailable ProviderID=%q want slot-ok", unavail.ProviderID)
	}
	if prior.released.Load() != 1 {
		t.Fatalf("prior must compensate; released=%d", prior.released.Load())
	}
	if hostile.released.Load() != 1 {
		t.Fatalf("hostile own hold must compensate; released=%d", hostile.released.Load())
	}
}

func TestPhase2_AttemptForeignProviderID_UnavailableAndCompensates(t *testing.T) {
	t.Parallel()
	prior := &fakeAttemptProvider{id: "prior"}
	hostile := &fakeAttemptProvider{id: "slot-ok"}
	hostile.admit = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:       authority.DecisionAllow,
			ProviderID: "foreign-not-slot",
			Reservations: []authority.Reservation{{
				Handle: "hostile-h",
				Kind:   authority.ReservationSpend,
				Money:  &economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
			}},
		}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "prior", Class: authoritycoord.AttemptPriorityHardSpend, Provider: prior, Strength: authority.StrengthRequired},
			{ID: "slot-ok", Class: authoritycoord.AttemptPriorityQuotaRate, Provider: hostile, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	_, err := coord.Admit(context.Background(), validAttemptAdmission("b-foreign-id"))
	var unavail *authoritycoord.UnavailableError
	if !errors.As(err, &unavail) {
		t.Fatalf("want UnavailableError for foreign ProviderID, got %T %v", err, err)
	}
	if unavail.ProviderID != "slot-ok" {
		t.Fatalf("unavailable ProviderID=%q want slot-ok", unavail.ProviderID)
	}
	if prior.released.Load() != 1 {
		t.Fatalf("prior must compensate; released=%d", prior.released.Load())
	}
	if hostile.released.Load() != 1 {
		t.Fatalf("hostile own hold must compensate; released=%d", hostile.released.Load())
	}
}

func TestPhase2_DeterministicRequestPriorityOrderAndStableTies(t *testing.T) {
	t.Parallel()
	var mu atomic.Value
	mu.Store([]string(nil))
	mk := func(id string) *fakeRequestProvider {
		p := &fakeRequestProvider{id: id}
		captured := id
		p.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
			prev, _ := mu.Load().([]string)
			mu.Store(append(append([]string(nil), prev...), captured))
			return authority.Decision{
				Kind: authority.DecisionAllow,
				Reservations: []authority.Reservation{{
					Handle:   captured + "-h",
					Kind:     authority.ReservationQuota,
					Quantity: &metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
				}},
			}, nil
		}
		return p
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "adv-b", Class: authoritycoord.PriorityAdvisory, Provider: mk("adv-b"), Strength: authority.StrengthAdvisory},
			{ID: "quota-b", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: mk("quota-b"), Strength: authority.StrengthRequired},
			{ID: "adv-a", Class: authoritycoord.PriorityAdvisory, Provider: mk("adv-a"), Strength: authority.StrengthAdvisory},
			{ID: "wallet", Class: authoritycoord.PriorityCreditWallet, Provider: mk("wallet"), Strength: authority.StrengthRequired},
			{ID: "quota-a", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: mk("quota-a"), Strength: authority.StrengthRequired},
		},
	}
	if _, err := coord.Admit(context.Background(), validRequestAdmission()); err != nil {
		t.Fatal(err)
	}
	order, _ := mu.Load().([]string)
	want := []string{"wallet", "quota-a", "quota-b", "adv-a", "adv-b"}
	if len(order) != len(want) {
		t.Fatalf("order=%v want %v (req 3.3)", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order=%v want %v (req 3.3)", order, want)
		}
	}
}

func TestPhase2_DeterministicAttemptPriorityOrderAndStableTies(t *testing.T) {
	t.Parallel()
	var mu atomic.Value
	mu.Store([]string(nil))
	mk := func(id string) *fakeAttemptProvider {
		p := &fakeAttemptProvider{id: id}
		captured := id
		p.admit = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
			prev, _ := mu.Load().([]string)
			mu.Store(append(append([]string(nil), prev...), captured))
			return authority.Decision{
				Kind: authority.DecisionAllow,
				Reservations: []authority.Reservation{{
					Handle: captured + "-h",
					Kind:   authority.ReservationSpend,
					Money:  &economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
				}},
			}, nil
		}
		return p
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "adv-b", Class: authoritycoord.AttemptPriorityAdvisory, Provider: mk("adv-b"), Strength: authority.StrengthAdvisory},
			{ID: "quota-b", Class: authoritycoord.AttemptPriorityQuotaRate, Provider: mk("quota-b"), Strength: authority.StrengthRequired},
			{ID: "hard", Class: authoritycoord.AttemptPriorityHardSpend, Provider: mk("hard"), Strength: authority.StrengthRequired},
			{ID: "adv-a", Class: authoritycoord.AttemptPriorityAdvisory, Provider: mk("adv-a"), Strength: authority.StrengthAdvisory},
			{ID: "quota-a", Class: authoritycoord.AttemptPriorityQuotaRate, Provider: mk("quota-a"), Strength: authority.StrengthRequired},
		},
	}
	if _, err := coord.Admit(context.Background(), validAttemptAdmission("b-order")); err != nil {
		t.Fatal(err)
	}
	order, _ := mu.Load().([]string)
	want := []string{"hard", "quota-a", "quota-b", "adv-a", "adv-b"}
	if len(order) != len(want) {
		t.Fatalf("order=%v want %v (req 3.4)", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order=%v want %v (req 3.4)", order, want)
		}
	}
}

func TestPhase2_AdvisoryDeny_NormalizesToEvidenceNotTrafficDeny(t *testing.T) {
	t.Parallel()
	hard := &fakeRequestProvider{id: "hard"}
	adv := &fakeRequestProvider{id: "adv"}
	adv.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:       authority.DecisionDeny,
			ProviderID: "adv",
			Evidence:   authority.SafeEvidence{Category: "advisory_signal", Code: "soft_quota", Message: "advisory deny"},
		}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "hard", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: hard, Strength: authority.StrengthRequired},
			{ID: "adv", Class: authoritycoord.PriorityAdvisory, Provider: adv, Strength: authority.StrengthAdvisory, FailureBehavior: authority.FailureFailOpen},
		},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatalf("advisory deny must not deny traffic: %v (req 3.5)", err)
	}
	if d.Kind != authority.DecisionAllow {
		t.Fatalf("kind=%s want allow after advisory deny normalize (req 3.5)", d.Kind)
	}
	found := false
	for _, pd := range d.ProviderDecisions {
		if pd.ProviderID == "adv" || pd.Evidence.Category == "advisory_signal" || pd.Evidence.Category == "authority_advisory" {
			found = true
		}
	}
	if !found && d.Evidence.Category != "authority_advisory" && d.Evidence.Category != "advisory_signal" {
		t.Fatal("advisory deny must become advisory evidence on composite decision (req 3.5)")
	}
}

func TestPhase2_RequiredFailOpen_DeterministicDenyStillFailsClosed(t *testing.T) {
	t.Parallel()
	deny := &fakeRequestProvider{id: "quota"}
	deny.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{Kind: authority.DecisionDeny, ProviderID: "quota"}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: deny, Strength: authority.StrengthRequired, FailureBehavior: authority.FailureFailOpen},
		},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if !authoritycoord.IsDenied(err) {
		t.Fatalf("required deterministic deny must fail closed even when fail-open: err=%v (req 3.6)", err)
	}
	if d.Kind != authority.DecisionDeny {
		t.Fatalf("kind=%s want deny", d.Kind)
	}
}

func TestPhase2_UnavailableTruthTable_RequiredFailOpenInfraDegrades(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		strength        authority.Strength
		failBeh         authority.FailureBehavior
		admitErr        error
		wantErr         bool
		wantUnavailable bool
		wantAllow       bool
	}{
		{
			name:     "required_fail_closed_infra_unavailable",
			strength: authority.StrengthRequired, failBeh: authority.FailureFailClosed,
			admitErr: errors.New("store down"), wantErr: true, wantUnavailable: true,
		},
		{
			name:     "required_fail_open_infra_degrades",
			strength: authority.StrengthRequired, failBeh: authority.FailureFailOpen,
			admitErr: errors.New("store down"), wantAllow: true,
		},
		{
			name:     "advisory_infra_degrades",
			strength: authority.StrengthAdvisory, failBeh: authority.FailureFailOpen,
			admitErr: errors.New("observer down"), wantAllow: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hard := &fakeRequestProvider{id: "hard"}
			target := &fakeRequestProvider{id: "target"}
			target.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
				return authority.Decision{}, tc.admitErr
			}
			class := authoritycoord.PriorityQuotaBudgetRate
			if tc.strength == authority.StrengthAdvisory {
				class = authoritycoord.PriorityAdvisory
			}
			coord := &authoritycoord.RequestCoordinator{
				Slots: []authoritycoord.RequestSlot{
					{ID: "hard", Class: authoritycoord.PriorityCreditWallet, Provider: hard, Strength: authority.StrengthRequired},
					{ID: "target", Class: class, Provider: target, Strength: tc.strength, FailureBehavior: tc.failBeh},
				},
			}
			d, err := coord.Admit(context.Background(), validRequestAdmission())
			if tc.wantAllow {
				if err != nil {
					t.Fatalf("want allow/degrade, got err=%v", err)
				}
				if d.Kind != authority.DecisionAllow {
					t.Fatalf("kind=%s", d.Kind)
				}
				return
			}
			if err == nil {
				t.Fatal("want error")
			}
			if tc.wantUnavailable {
				if _, ok := errors.AsType[*authoritycoord.UnavailableError](err); !ok {
					t.Fatalf("want UnavailableError, got %T %v", err, err)
				}
			}
		})
	}
}

func TestPhase2_DenyWithHolds_CompensatesOwnHoldsBeforeContinue(t *testing.T) {
	t.Parallel()
	prior := &fakeRequestProvider{id: "prior"}
	deny := &fakeRequestProvider{id: "deny"}
	deny.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:       authority.DecisionDeny,
			ProviderID: "deny",
			Reservations: []authority.Reservation{{
				Handle:   "deny-hold",
				Kind:     authority.ReservationQuota,
				Quantity: &metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
			}},
		}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "prior", Class: authoritycoord.PriorityCreditWallet, Provider: prior, Strength: authority.StrengthRequired},
			{ID: "deny", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: deny, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if !authoritycoord.IsDenied(err) {
		t.Fatalf("want deny, got %v", err)
	}
	if prior.released.Load() != 1 {
		t.Fatalf("prior hold must reverse-compensate; released=%d", prior.released.Load())
	}
	if deny.released.Load() != 1 {
		t.Fatalf("deny-with-holds must compensate own hold before continue (req 4.4); released=%d", deny.released.Load())
	}
}

func TestPhase2_MalformedHolds_RejectedAndCompensated(t *testing.T) {
	t.Parallel()
	prior := &fakeRequestProvider{id: "prior"}
	bad := &fakeRequestProvider{id: "bad"}
	bad.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:       authority.DecisionAllow,
			ProviderID: "bad",
			Reservations: []authority.Reservation{
				{Handle: "", Kind: authority.ReservationQuota},
				{Handle: "dup", Kind: authority.ReservationQuota, Quantity: &metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true}},
				{Handle: "dup", Kind: authority.ReservationBudget, Money: &economics.Money{NanoUnits: 1, Currency: "USD", Present: true}},
			},
		}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "prior", Class: authoritycoord.PriorityCreditWallet, Provider: prior, Strength: authority.StrengthRequired},
			{ID: "bad", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: bad, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("malformed holds must fail closed (req 4.2–4.3)")
	}
	if _, ok := errors.AsType[*authoritycoord.UnavailableError](err); !ok {
		t.Fatalf("want UnavailableError for malformed holds, got %T %v", err, err)
	}
	if prior.released.Load() != 1 {
		t.Fatalf("prior hold must compensate; released=%d", prior.released.Load())
	}
	if d.Kind != authority.DecisionDeny {
		t.Fatalf("kind=%s want deny", d.Kind)
	}
}

func TestPhase2_StandaloneCompensationHandle_Rejected(t *testing.T) {
	t.Parallel()
	bad := &fakeRequestProvider{id: "bad"}
	bad.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:               authority.DecisionAllow,
			ProviderID:         "bad",
			CompensationHandle: "orphan-only",
		}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "bad", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: bad, Strength: authority.StrengthRequired},
		},
	}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("standalone compensation handle without reservation must be rejected (req 4.4)")
	}
}

func TestPhase2_Settle_EmptyAndForeignHandlesUnavailableNotSettled(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		out  authority.Settlement
	}{
		{name: "empty_final", out: authority.Settlement{Kind: authority.SettlementFinal}},
		{name: "empty_partial", out: authority.Settlement{Kind: authority.SettlementPartial}},
		{name: "empty_estimated", out: authority.Settlement{Kind: authority.SettlementEstimated}},
		{name: "foreign", out: authority.Settlement{Kind: authority.SettlementFinal, Handle: "foreign"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &fakeRequestProvider{id: "quota"}
			p.settle = func(_ context.Context, _ authority.RequestSettlement) (authority.Settlement, error) {
				return tc.out, nil
			}
			coord := &authoritycoord.RequestCoordinator{
				Slots: []authoritycoord.RequestSlot{{
					ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: p, Strength: authority.StrengthRequired,
				}},
				CleanupTimeout: time.Second,
			}
			d, err := coord.Admit(context.Background(), validRequestAdmission())
			if err != nil {
				t.Fatal(err)
			}
			settleIn := authority.RequestSettlement{RequestID: "req-1", Handles: d.Stack.Handles()}
			err = coord.Settle(context.Background(), d.Stack, settleIn)
			if err == nil {
				t.Fatal("malformed settlement must be unavailable (req 4.5; D5)")
			}
			if _, ok := errors.AsType[*authoritycoord.UnavailableError](err); !ok {
				t.Fatalf("want UnavailableError, got %T %v", err, err)
			}
			first := p.settled.Load()
			if first != 1 {
				t.Fatalf("settled=%d want 1", first)
			}
			if err := coord.Settle(context.Background(), d.Stack, settleIn); err == nil {
				t.Fatal("failed settlement must remain retryable (req 8.6)")
			}
			if p.settled.Load() != 2 {
				t.Fatalf("retry settled=%d want 2 (not marked done)", p.settled.Load())
			}
		})
	}
}

func TestPhase2_CompatibilityHolds_CompensatedBeforePriorStack(t *testing.T) {
	t.Parallel()
	var mu atomic.Value
	mu.Store([]string(nil))
	prior := &fakeRequestProvider{id: "prior"}
	compat := &fakeRequestProvider{id: "compat"}
	compat.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:       authority.DecisionKind("not-a-kind"),
			ProviderID: "compat",
			Reservations: []authority.Reservation{{
				Handle: "compat-hold", Kind: authority.ReservationQuota,
				Quantity: &metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
			}},
		}, nil
	}
	priorWrapped := &releaseOrderProvider{inner: prior, name: "prior", order: &mu}
	compatWrapped := &releaseOrderProvider{inner: compat, name: "compat", order: &mu}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "prior", Class: authoritycoord.PriorityCreditWallet, Provider: priorWrapped, Strength: authority.StrengthRequired},
			{ID: "compat", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: compatWrapped, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expected malformed decision failure")
	}
	releaseOrder, _ := mu.Load().([]string)
	if len(releaseOrder) < 2 {
		t.Fatalf("release order=%v want compat then prior", releaseOrder)
	}
	if releaseOrder[0] != "compat" {
		t.Fatalf("compatibility hold must compensate before prior stack; order=%v", releaseOrder)
	}
}

type releaseOrderProvider struct {
	inner *fakeRequestProvider
	name  string
	order *atomic.Value
}

func (p *releaseOrderProvider) AdmitRequest(ctx context.Context, in authority.RequestAdmission) (authority.Decision, error) {
	return p.inner.AdmitRequest(ctx, in)
}

func (p *releaseOrderProvider) SettleRequest(ctx context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	return p.inner.SettleRequest(ctx, in)
}

func (p *releaseOrderProvider) ReleaseRequest(ctx context.Context, in authority.RequestRelease) error {
	prev, _ := p.order.Load().([]string)
	p.order.Store(append(append([]string(nil), prev...), p.name))
	return p.inner.ReleaseRequest(ctx, in)
}

func TestPhase2_ForeignSettlementHandles_Rejected(t *testing.T) {
	t.Parallel()
	a := &fakeRequestProvider{id: "a"}
	b := &fakeRequestProvider{id: "b"}
	b.settle = func(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
		return authority.Settlement{Kind: authority.SettlementFinal, Handle: "foreign-not-submitted"}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "a", Class: authoritycoord.PriorityCreditWallet, Provider: a, Strength: authority.StrengthRequired},
			{ID: "b", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: b, Strength: authority.StrengthRequired},
		},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatal(err)
	}
	err = coord.Settle(context.Background(), d.Stack, authority.RequestSettlement{
		RequestID: "req-1",
		Handles:   d.Stack.Handles(),
	})
	if err == nil {
		t.Fatal("foreign settlement handle must fail closed (req 4.5)")
	}
}

func TestPhase2_PartialSettlement_RetriesOnlyUnfinishedProviders(t *testing.T) {
	t.Parallel()
	okProv := &fakeRequestProvider{id: "ok"}
	failProv := &fakeRequestProvider{id: "fail"}
	failProv.settle = func(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
		return authority.Settlement{}, errors.New("transient settle failure")
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "ok", Class: authoritycoord.PriorityCreditWallet, Provider: okProv, Strength: authority.StrengthRequired},
			{ID: "fail", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: failProv, Strength: authority.StrengthRequired},
		},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatal(err)
	}
	settleIn := authority.RequestSettlement{RequestID: "req-1", Handles: d.Stack.Handles()}
	if err := coord.Settle(context.Background(), d.Stack, settleIn); err == nil {
		t.Fatal("want settle error from fail provider")
	}
	if okProv.settled.Load() != 1 || failProv.settled.Load() != 1 {
		t.Fatalf("first settle: ok=%d fail=%d", okProv.settled.Load(), failProv.settled.Load())
	}
	_ = coord.Settle(context.Background(), d.Stack, settleIn)
	if okProv.settled.Load() != 1 {
		t.Fatalf("successful provider must not be re-settled; settled=%d (req 8.6 / task 2.3)", okProv.settled.Load())
	}
	if failProv.settled.Load() < 2 {
		t.Fatalf("unfinished provider must be retried; settled=%d", failProv.settled.Load())
	}
}

func TestPhase2_MalformedSettlementOutput_FailClosed(t *testing.T) {
	t.Parallel()
	p := &fakeRequestProvider{id: "p"}
	p.settle = func(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
		return authority.Settlement{
			Kind:     authority.SettlementKind("nope"),
			Handle:   "p-h",
			Consumed: economics.Money{NanoUnits: -1, Currency: "USD", Present: true},
		}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "p", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: p, Strength: authority.StrengthRequired},
		},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatal(err)
	}
	if err := coord.Settle(context.Background(), d.Stack, authority.RequestSettlement{
		RequestID: "req-1", Handles: d.Stack.Handles(),
	}); err == nil {
		t.Fatal("malformed settlement output must fail closed (req 4.5)")
	}
}

func TestPhase2_InvalidClamps_RejectedAtAdmit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		clamps []authority.Clamp
	}{
		{
			name:   "unknown_kind",
			clamps: []authority.Clamp{{Kind: authority.ClampKind("widen_everything"), Value: 1}},
		},
		{
			name:   "negative_tokens",
			clamps: []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: -1}},
		},
		{
			name: "empty_currency_spend",
			clamps: []authority.Clamp{{
				Kind:  authority.ClampMaxSpend,
				Money: economics.Money{NanoUnits: 1, Currency: "", Present: true},
			}},
		},
		{
			name: "negative_spend",
			clamps: []authority.Clamp{{
				Kind:  authority.ClampMaxSpend,
				Money: economics.Money{NanoUnits: -9, Currency: "USD", Present: true},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &fakeRequestProvider{id: "p"}
			clamps := tc.clamps
			p.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
				return authority.Decision{Kind: authority.DecisionAllow, ProviderID: "p", Clamps: clamps}, nil
			}
			coord := &authoritycoord.RequestCoordinator{
				Slots: []authoritycoord.RequestSlot{
					{ID: "p", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: p, Strength: authority.StrengthRequired},
				},
			}
			_, err := coord.Admit(context.Background(), validRequestAdmission())
			if err == nil {
				t.Fatal("invalid clamp must fail closed (req 4.1)")
			}
		})
	}
}

func TestPhase2_NonWideningClampMerge_IgnoresWiderProposal(t *testing.T) {
	t.Parallel()
	// Living green invariant: merge keeps the tighter limit (design non-widening).
	first := &fakeRequestProvider{id: "first"}
	first.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:   authority.DecisionAllow,
			Clamps: []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: 50}},
			Reservations: []authority.Reservation{{
				Handle: "first-h", Kind: authority.ReservationQuota,
				Quantity: &metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
			}},
		}, nil
	}
	widen := &fakeRequestProvider{id: "widen"}
	widen.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:   authority.DecisionAllow,
			Clamps: []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: 200}},
			Reservations: []authority.Reservation{{
				Handle: "widen-h", Kind: authority.ReservationQuota,
				Quantity: &metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
			}},
		}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "first", Class: authoritycoord.PriorityCreditWallet, Provider: first, Strength: authority.StrengthRequired},
			{ID: "widen", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: widen, Strength: authority.StrengthRequired},
		},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Clamps) != 1 || d.Clamps[0].Value != 50 {
		t.Fatalf("non-widening merge must keep 50, got %+v", d.Clamps)
	}
}

func TestPhase2_PreviewDecision_RejectsReservationsAndHolds(t *testing.T) {
	t.Parallel()
	p := &previewAttemptProvider{}
	p.preview = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind: authority.DecisionAllow,
			Reservations: []authority.Reservation{{
				Handle: "preview-hold", Kind: authority.ReservationSpend,
				Money: &economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
			}},
		}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "preview", Class: authoritycoord.AttemptPriorityHardSpend, Provider: p, Strength: authority.StrengthRequired},
		},
	}
	_, err := coord.PreviewClamps(context.Background(), validAttemptAdmission("b-preview"))
	if err == nil {
		t.Fatal("preview with reservations must fail (design Clamp Preview)")
	}
}

func TestPhase2_PreviewDecision_RejectsUnknownClamp(t *testing.T) {
	t.Parallel()
	p := &previewAttemptProvider{}
	p.preview = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:   authority.DecisionAllow,
			Clamps: []authority.Clamp{{Kind: authority.ClampKind("not-a-clamp"), Value: 1}},
		}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "preview", Class: authoritycoord.AttemptPriorityHardSpend, Provider: p, Strength: authority.StrengthRequired},
		},
	}
	_, err := coord.PreviewClamps(context.Background(), validAttemptAdmission("b-preview-bad"))
	if err == nil {
		t.Fatal("unknown preview clamp must fail (req 4.1; design Clamp Preview)")
	}
}

func TestPhase2_MalformedLease_OwnershipGenerationTiming(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0).UTC()
	cases := []struct {
		name string
		ld   authority.LeaseDecision
	}{
		{
			name: "allow_empty_lease_id",
			ld:   authority.LeaseDecision{Kind: authority.LeaseAllow, Generation: 1, ExpiresAt: now.Add(time.Minute)},
		},
		{
			name: "negative_generation",
			ld: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "L1", Generation: -1, ExpiresAt: now.Add(time.Minute),
			},
		},
		{
			name: "expired_allow",
			ld: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 1, ExpiresAt: now.Add(-time.Second),
			},
		},
		{
			name: "deny_with_occupancy",
			ld: authority.LeaseDecision{
				Kind: authority.LeaseDeny, LeaseID: "L1", Generation: 1, ExpiresAt: now.Add(time.Minute),
			},
		},
		{
			name: "rule_amount_contradiction",
			ld: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 1, ExpiresAt: now.Add(time.Minute),
				RemainingSlots: -3,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ld := tc.ld
			conc := &fakeConcurrencyProvider{
				admit: func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
					return ld, nil
				},
			}
			coord := &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
				Now:            func() time.Time { return now },
			}
			_, err := coord.Admit(context.Background(), validRequestAdmission())
			if _, ok := errors.AsType[*authoritycoord.UnavailableError](err); !ok {
				t.Fatalf("malformed lease must be UnavailableError, got %T %v (req 4.1)", err, err)
			}
		})
	}
}

func TestPhase2_ProviderPanic_OperatorSafeNoRawPayloadLeak(t *testing.T) {
	t.Parallel()
	secret := "sk-live-PANIC-SECRET"
	panicProv := &fakeRequestProvider{id: "enterprise"}
	panicProv.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		panic(fmt.Sprintf("enterprise boom credential=%s body={\"balance\":0}", secret))
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "enterprise", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: panicProv, Strength: authority.StrengthRequired},
		},
	}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expected unavailable")
	}
	if _, ok := errors.AsType[*authoritycoord.UnavailableError](err); !ok {
		t.Fatalf("want UnavailableError, got %T %v", err, err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error must not leak raw panic credential (D14 / req 13.3): %v", err)
	}
	if strings.Contains(err.Error(), `"balance"`) {
		t.Fatalf("error must not leak provider payload (D14): %v", err)
	}
}

type previewAttemptProvider struct {
	preview  func(context.Context, authority.AttemptAdmission) (authority.Decision, error)
	admit    func(context.Context, authority.AttemptAdmission) (authority.Decision, error)
	released atomic.Int32
}

func (p *previewAttemptProvider) AdmitAttempt(ctx context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	if p.admit != nil {
		return p.admit(ctx, in)
	}
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (p *previewAttemptProvider) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (p *previewAttemptProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	p.released.Add(1)
	return nil
}

func (p *previewAttemptProvider) PreviewAttempt(ctx context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	if p.preview != nil {
		return p.preview(ctx, in)
	}
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}
