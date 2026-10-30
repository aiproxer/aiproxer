package authority_test

import (
	"math"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Phase 2.1 RED public contracts (requirements 3.1–3.9, 4.1–4.10, 13.1, 13.3;
// design D4, D5, D14, D17).
//
// Deferred to task 2.2 (no fake compile sentinels): RequestRegistration,
// AttemptRegistration, ConcurrencyRegistration, descriptor-bound composition.
// Deferred to task 2.4: SafeEvidence.Validate / public unavailable mapper (not
// in design ValidateFor set); LeaseRenew.Validate (not designed).

func TestPhase2_CompileContract_DecisionValidateFor(t *testing.T) {
	t.Parallel()
	type decisionValidateFor interface {
		ValidateFor(authority.ProviderDescriptor, authority.Stage) error
	}
	if _, ok := any(authority.Decision{}).(decisionValidateFor); !ok {
		t.Fatal("Decision.ValidateFor(ProviderDescriptor, Stage) missing (design External Result Validation; task 2.4)")
	}
}

func TestPhase2_CompileContract_SettlementValidateFor(t *testing.T) {
	t.Parallel()
	type settlementValidateFor interface {
		ValidateFor([]string, metering.EconomicPerspective) error
	}
	if _, ok := any(authority.Settlement{}).(settlementValidateFor); !ok {
		t.Fatal("Settlement.ValidateFor([]string, EconomicPerspective) missing (design External Result Validation; task 2.4)")
	}
}

func TestPhase2_CompileContract_LeaseDecisionValidateFor(t *testing.T) {
	t.Parallel()
	type leaseValidateFor interface {
		ValidateFor(authority.LeaseAdmission, authority.ProviderDescriptor) error
	}
	if _, ok := any(authority.LeaseDecision{}).(leaseValidateFor); !ok {
		t.Fatal("LeaseDecision.ValidateFor(LeaseAdmission, ProviderDescriptor) missing (design External Result Validation; task 2.4)")
	}
}

func TestPhase2_ProviderDescriptor_RejectsDuplicateStagePostures(t *testing.T) {
	t.Parallel()
	dup := authority.ProviderDescriptor{
		ID:   "quota",
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{
			{Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired, FailureBehavior: authority.FailureFailClosed},
			{Stage: authority.StageRequestAdmit, Strength: authority.StrengthAdvisory, FailureBehavior: authority.FailureFailOpen},
		},
	}
	if err := dup.Validate(); err == nil {
		t.Fatal("duplicate stage postures must be rejected (req 3.2)")
	}
}

func TestPhase2_ProviderDescriptor_RejectsEmptyIDWhitespace(t *testing.T) {
	t.Parallel()
	ws := authority.ProviderDescriptor{
		ID: "  ",
		Postures: []authority.StagePosture{{
			Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired, FailureBehavior: authority.FailureFailClosed,
		}},
	}
	if err := ws.Validate(); err == nil {
		t.Fatal("whitespace-only provider id must be rejected (req 3.2)")
	}
}

func TestPhase2_DecisionValidate_RejectsEmptyReservationHandle(t *testing.T) {
	t.Parallel()
	d := authority.Decision{
		Kind:         authority.DecisionAllow,
		ProviderID:   "quota",
		Reservations: []authority.Reservation{{Handle: "", Kind: authority.ReservationQuota}},
	}
	if err := d.Validate(); err == nil {
		t.Fatal("empty reservation handle must fail validation (req 4.2)")
	}
}

func TestPhase2_DecisionValidate_RejectsDuplicateReservationHandles(t *testing.T) {
	t.Parallel()
	d := authority.Decision{
		Kind:       authority.DecisionAllow,
		ProviderID: "quota",
		Reservations: []authority.Reservation{
			{Handle: "h1", Kind: authority.ReservationQuota, Quantity: &metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true}},
			{Handle: "h1", Kind: authority.ReservationBudget, Money: &economics.Money{NanoUnits: 1, Currency: "USD", Present: true}},
		},
	}
	if err := d.Validate(); err == nil {
		t.Fatal("duplicate reservation handles within one decision must fail (req 4.2)")
	}
}

func TestPhase2_DecisionValidate_RejectsBothQuantityAndMoney(t *testing.T) {
	t.Parallel()
	qty := metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 10, Present: true}
	money := economics.Money{NanoUnits: 5, Currency: "USD", Present: true}
	d := authority.Decision{
		Kind:       authority.DecisionAllow,
		ProviderID: "quota",
		Reservations: []authority.Reservation{{
			Handle: "h1", Kind: authority.ReservationQuota, Quantity: &qty, Money: &money,
		}},
	}
	if err := d.Validate(); err == nil {
		t.Fatal("reservation must carry exactly one of quantity or money (req 4.3)")
	}
}

func TestPhase2_DecisionValidate_RejectsNeitherQuantityNorMoney(t *testing.T) {
	t.Parallel()
	d := authority.Decision{
		Kind:       authority.DecisionAllow,
		ProviderID: "quota",
		Reservations: []authority.Reservation{{
			Handle: "h1", Kind: authority.ReservationQuota,
		}},
	}
	if err := d.Validate(); err == nil {
		t.Fatal("reservation without quantity or money must fail (req 4.3)")
	}
}

func TestPhase2_DecisionValidate_RejectsNegativeOrdinaryAmounts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		res  authority.Reservation
	}{
		{
			name: "negative_quantity",
			res: authority.Reservation{
				Handle: "h1", Kind: authority.ReservationQuota,
				Quantity: &metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: -1, Present: true},
			},
		},
		{
			name: "negative_money",
			res: authority.Reservation{
				Handle: "h1", Kind: authority.ReservationSpend,
				Money: &economics.Money{NanoUnits: -1, Currency: "USD", Present: true},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := authority.Decision{Kind: authority.DecisionAllow, ProviderID: "p", Reservations: []authority.Reservation{tc.res}}
			if err := d.Validate(); err == nil {
				t.Fatal("negative ordinary reservation amount must fail (req 4.3)")
			}
		})
	}
}

func TestPhase2_DecisionValidate_RejectsNonAllowWithHolds(t *testing.T) {
	t.Parallel()
	qty := metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true}
	for _, kind := range []authority.DecisionKind{authority.DecisionDeny, authority.DecisionAdvisory} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			d := authority.Decision{
				Kind:       kind,
				ProviderID: "p",
				Reservations: []authority.Reservation{{
					Handle: "h1", Kind: authority.ReservationQuota, Quantity: &qty,
				}},
			}
			if err := d.Validate(); err == nil {
				t.Fatalf("%s with holds must fail validation (req 4.4)", kind)
			}
		})
	}
}

func TestPhase2_DecisionValidate_RejectsStandaloneCompensationHandleWithoutAllow(t *testing.T) {
	t.Parallel()
	d := authority.Decision{
		Kind:               authority.DecisionDeny,
		ProviderID:         "p",
		CompensationHandle: "orphan-comp",
	}
	if err := d.Validate(); err == nil {
		t.Fatal("deny with standalone compensation handle must fail (req 4.4)")
	}
}

func TestPhase2_DecisionValidateFor_RejectsProviderIDMismatch(t *testing.T) {
	t.Parallel()
	type decisionValidateFor interface {
		ValidateFor(authority.ProviderDescriptor, authority.Stage) error
	}
	reg := authority.ProviderDescriptor{
		ID: "quota-a",
		Postures: []authority.StagePosture{{
			Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired, FailureBehavior: authority.FailureFailClosed,
		}},
	}
	d := authority.Decision{Kind: authority.DecisionAllow, ProviderID: "quota-b"}
	v, ok := any(d).(decisionValidateFor)
	if !ok {
		t.Fatal("Decision.ValidateFor missing (req 3.2, 4.1; task 2.4)")
	}
	if err := v.ValidateFor(reg, authority.StageRequestAdmit); err == nil {
		t.Fatal("provider_id mismatch vs registration descriptor must fail (req 3.2)")
	}
}

func TestPhase2_DecisionValidate_RejectsUnknownClampAndNegativeSpend(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		d    authority.Decision
	}{
		{
			name: "unknown_clamp",
			d: authority.Decision{
				Kind: authority.DecisionAllow, ProviderID: "p",
				Clamps: []authority.Clamp{{Kind: authority.ClampKind("not-a-clamp"), Value: 1}},
			},
		},
		{
			name: "negative_output_tokens",
			d: authority.Decision{
				Kind: authority.DecisionAllow, ProviderID: "p",
				Clamps: []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: -1}},
			},
		},
		{
			name: "spend_empty_currency",
			d: authority.Decision{
				Kind: authority.DecisionAllow, ProviderID: "p",
				Clamps: []authority.Clamp{{
					Kind:  authority.ClampMaxSpend,
					Money: economics.Money{NanoUnits: 1, Currency: "", Present: true},
				}},
			},
		},
		{
			name: "spend_negative",
			d: authority.Decision{
				Kind: authority.DecisionAllow, ProviderID: "p",
				Clamps: []authority.Clamp{{
					Kind:  authority.ClampMaxSpend,
					Money: economics.Money{NanoUnits: -5, Currency: "USD", Present: true},
				}},
			},
		},
		{
			name: "spend_absent_money",
			d: authority.Decision{
				Kind: authority.DecisionAllow, ProviderID: "p",
				Clamps: []authority.Clamp{{Kind: authority.ClampMaxSpend}},
			},
		},
		{
			name: "overflow_output_tokens",
			d: authority.Decision{
				Kind: authority.DecisionAllow, ProviderID: "p",
				Clamps: []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: math.MinInt64}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.d.Validate(); err == nil {
				t.Fatal("invalid clamp must fail Decision.Validate (req 4.1)")
			}
		})
	}
}

func TestPhase2_SettlementValidateFor_RejectsForeignAndMalformedHandles(t *testing.T) {
	t.Parallel()
	type settlementValidateFor interface {
		ValidateFor([]string, metering.EconomicPerspective) error
	}
	submitted := []string{"owned-1", "owned-2"}
	cases := []struct {
		name string
		s    authority.Settlement
	}{
		{name: "foreign_handle", s: authority.Settlement{Kind: authority.SettlementFinal, Handle: "foreign"}},
		{name: "empty_handle_with_final", s: authority.Settlement{Kind: authority.SettlementFinal, Handle: ""}},
		{name: "unknown_kind", s: authority.Settlement{Kind: authority.SettlementKind("nope"), Handle: "owned-1"}},
		{
			name: "negative_consumed",
			s: authority.Settlement{
				Kind: authority.SettlementFinal, Handle: "owned-1",
				Consumed: economics.Money{NanoUnits: -1, Currency: "USD", Present: true},
			},
		},
		{
			name: "mixed_currency_consumed_released",
			s: authority.Settlement{
				Kind: authority.SettlementPartial, Handle: "owned-1",
				Consumed: economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
				Released: economics.Money{NanoUnits: 1, Currency: "EUR", Present: true},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, ok := any(tc.s).(settlementValidateFor)
			if !ok {
				t.Fatal("Settlement.ValidateFor missing (req 4.5; task 2.4)")
			}
			if err := v.ValidateFor(submitted, metering.PerspectiveCustomer); err == nil {
				t.Fatal("malformed/foreign settlement must fail (req 4.5, 4.6, 4.9)")
			}
		})
	}
}

func TestPhase2_LeaseDecisionValidate_RejectsOwnershipTimingContradictions(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0).UTC()
	cases := []struct {
		name string
		d    authority.LeaseDecision
	}{
		{
			name: "allow_without_lease_id",
			d:    authority.LeaseDecision{Kind: authority.LeaseAllow, Generation: 1, ExpiresAt: now.Add(time.Minute)},
		},
		{
			name: "negative_generation",
			d: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "L1", Generation: -1, ExpiresAt: now.Add(time.Minute),
			},
		},
		{
			name: "zero_generation_on_allow",
			d: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 0, ExpiresAt: now.Add(time.Minute),
			},
		},
		{
			name: "renew_before_exceeds_ttl",
			d: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 1,
				ExpiresAt: now.Add(time.Minute), TTL: 30 * time.Second, RenewBefore: time.Minute,
			},
		},
		{
			name: "negative_remaining_slots",
			d: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 1,
				ExpiresAt: now.Add(time.Minute), RemainingSlots: -1,
			},
		},
		{
			name: "deny_with_lease_id",
			d: authority.LeaseDecision{
				Kind: authority.LeaseDeny, LeaseID: "L1", Generation: 1, ExpiresAt: now.Add(time.Minute),
			},
		},
		{
			name: "occupancy_rule_mismatch",
			d: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 1, ExpiresAt: now.Add(time.Minute),
				Leases: []authority.LeaseOccupancy{{
					LeaseID: "L2", Generation: 1, RuleID: "rule-a", ExpiresAt: now.Add(time.Minute),
				}},
			},
		},
		{
			name: "allow_zero_expires_at",
			d: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 1,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.d.Validate(); err == nil {
				t.Fatal("contradictory lease decision must fail validation (req 4.1)")
			}
		})
	}
}

func TestPhase2_LeaseDecisionValidateFor_RejectsStructuralContradictions(t *testing.T) {
	t.Parallel()
	type leaseValidateFor interface {
		ValidateFor(authority.LeaseAdmission, authority.ProviderDescriptor) error
	}
	reg := authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage: authority.StageLeaseAdmit, Strength: authority.StrengthRequired, FailureBehavior: authority.FailureFailClosed,
		}},
	}
	in := authority.LeaseAdmission{RequestID: "req-1", RuleID: "rule-a", TTL: time.Minute}
	d := authority.LeaseDecision{
		Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 1,
		// zero ExpiresAt is structural; wall-clock expiry is coordinator-owned
	}
	v, ok := any(d).(leaseValidateFor)
	if !ok {
		t.Fatal("LeaseDecision.ValidateFor missing (req 4.1; task 2.4)")
	}
	if err := v.ValidateFor(in, reg); err == nil {
		t.Fatal("zero expires_at must fail public ValidateFor (req 4.1, 10.2)")
	}
	wrongStage := authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired, FailureBehavior: authority.FailureFailClosed,
		}},
	}
	okLease := authority.LeaseDecision{
		Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 1, ExpiresAt: time.Unix(1000, 0).UTC().Add(time.Minute),
	}
	if err := okLease.ValidateFor(in, wrongStage); err == nil {
		t.Fatal("lease ValidateFor must require StageLeaseAdmit on registration")
	}
}

func TestPhase2_ValidateFor_AcceptsValidExternalResults(t *testing.T) {
	t.Parallel()
	qty := metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true}
	reg := authority.ProviderDescriptor{
		ID: "quota",
		Postures: []authority.StagePosture{{
			Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired, FailureBehavior: authority.FailureFailClosed,
		}},
	}
	d := authority.Decision{
		Kind:       authority.DecisionAllow,
		ProviderID: "quota",
		Reservations: []authority.Reservation{{
			Handle: "h1", Kind: authority.ReservationQuota, Quantity: &qty,
		}},
	}
	if err := d.ValidateFor(reg, authority.StageRequestAdmit); err != nil {
		t.Fatalf("valid decision: %v", err)
	}
	s := authority.Settlement{
		Kind:     authority.SettlementFinal,
		Handle:   "h1",
		Consumed: economics.Money{NanoUnits: 0, Currency: "USD", Present: true},
	}
	if err := s.ValidateFor([]string{"h1"}, metering.PerspectiveCustomer); err != nil {
		t.Fatalf("valid settlement: %v", err)
	}
	leaseReg := authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage: authority.StageLeaseAdmit, Strength: authority.StrengthRequired, FailureBehavior: authority.FailureFailClosed,
		}},
	}
	ld := authority.LeaseDecision{
		Kind:        authority.LeaseAllow,
		LeaseID:     "L1",
		Generation:  1,
		ExpiresAt:   time.Now().UTC().Add(time.Minute),
		TTL:         time.Minute,
		RenewBefore: 10 * time.Second,
	}
	if err := ld.ValidateFor(authority.LeaseAdmission{RequestID: "r1", RuleID: "rule-a", TTL: time.Minute}, leaseReg); err != nil {
		t.Fatalf("valid lease: %v", err)
	}
}
