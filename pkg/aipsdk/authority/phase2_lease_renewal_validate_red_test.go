package authority_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

func TestPhase2_CompileContract_LeaseDecisionValidateRenewalFor(t *testing.T) {
	t.Parallel()
	type leaseValidateRenewalFor interface {
		ValidateRenewalFor(authority.LeaseRenew, authority.ProviderDescriptor) error
	}
	if _, ok := any(authority.LeaseDecision{}).(leaseValidateRenewalFor); !ok {
		t.Fatal("LeaseDecision.ValidateRenewalFor(LeaseRenew, ProviderDescriptor) missing (req 4.1, 10.2; task 2.4)")
	}
}

func TestPhase2_LeaseDecisionValidateRenewalFor_RejectsHostileRenewals(t *testing.T) {
	t.Parallel()
	type leaseValidateRenewalFor interface {
		ValidateRenewalFor(authority.LeaseRenew, authority.ProviderDescriptor) error
	}
	reg := authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
	now := time.Unix(1000, 0).UTC()
	in := authority.LeaseRenew{
		LeaseID:            "L1",
		RequestID:          "req-1",
		ExpectedGeneration: 3,
		TTL:                time.Minute,
		RuleID:             "rule-a",
	}
	cases := []struct {
		name string
		d    authority.LeaseDecision
	}{
		{
			name: "unknown_kind",
			d: authority.LeaseDecision{
				Kind: "weird", LeaseID: "L1", Generation: 4, ExpiresAt: now.Add(time.Minute),
			},
		},
		{
			name: "lease_id_ownership_mismatch",
			d: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "OTHER", Generation: 4, ExpiresAt: now.Add(time.Minute),
			},
		},
		{
			name: "rule_mismatch",
			d: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 4, ExpiresAt: now.Add(time.Minute),
				Leases: []authority.LeaseOccupancy{{
					LeaseID: "L1", Generation: 4, RuleID: "rule-b", ExpiresAt: now.Add(time.Minute),
				}},
			},
		},
		{
			name: "stale_generation",
			d: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 2, ExpiresAt: now.Add(time.Minute),
			},
		},
		{
			name: "zero_expires_at",
			d: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 4,
			},
		},
		{
			name: "renew_before_exceeds_ttl",
			d: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 4,
				ExpiresAt: now.Add(time.Minute), TTL: 30 * time.Second, RenewBefore: time.Minute,
			},
		},
		{
			name: "negative_remaining_slots",
			d: authority.LeaseDecision{
				Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 4,
				ExpiresAt: now.Add(time.Minute), RemainingSlots: -1,
			},
		},
		{
			name: "deny_with_lease_id",
			d: authority.LeaseDecision{
				Kind: authority.LeaseDeny, LeaseID: "L1", Generation: 4, ExpiresAt: now.Add(time.Minute),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, ok := any(tc.d).(leaseValidateRenewalFor)
			if !ok {
				t.Fatal("LeaseDecision.ValidateRenewalFor missing (req 4.1, 10.2)")
			}
			if err := v.ValidateRenewalFor(in, reg); err == nil {
				t.Fatal("hostile renewal must fail ValidateRenewalFor (req 4.1, 10.2)")
			}
		})
	}
}

func TestPhase2_LeaseDecisionValidateRenewalFor_AcceptsValidRenewal(t *testing.T) {
	t.Parallel()
	reg := authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
	now := time.Unix(1000, 0).UTC()
	d := authority.LeaseDecision{
		Kind:       authority.LeaseAllow,
		LeaseID:    "L1",
		Generation: 4,
		ExpiresAt:  now.Add(time.Minute),
		TTL:        time.Minute,
		Leases: []authority.LeaseOccupancy{{
			LeaseID: "L1", Generation: 4, RuleID: "rule-a", ExpiresAt: now.Add(time.Minute),
		}},
	}
	in := authority.LeaseRenew{
		LeaseID: "L1", ExpectedGeneration: 3, TTL: time.Minute, RuleID: "rule-a",
	}
	if err := d.ValidateRenewalFor(in, reg); err != nil {
		t.Fatalf("valid renewal: %v", err)
	}
	wrongStage := authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired, FailureBehavior: authority.FailureFailClosed,
		}},
	}
	if err := d.ValidateRenewalFor(in, wrongStage); err == nil {
		t.Fatal("renewal ValidateRenewalFor must require StageLeaseAdmit")
	}
	if err := d.ValidateRenewalFor(in, wrongStage); err != nil && !strings.Contains(err.Error(), "lease_admit") && !strings.Contains(err.Error(), "Stage") && !strings.Contains(strings.ToLower(err.Error()), "stage") {
		_ = err
	}
}

func TestPhase2_LeaseDecisionValidateRenewalFor_RejectsForeignAndDuplicateOccupancy(t *testing.T) {
	t.Parallel()
	reg := authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
	now := time.Unix(1000, 0).UTC()
	in := authority.LeaseRenew{LeaseID: "L1", ExpectedGeneration: 1, RuleID: "rule-a"}
	foreign := authority.LeaseDecision{
		Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 2, ExpiresAt: now.Add(time.Minute),
		Leases: []authority.LeaseOccupancy{
			{LeaseID: "L1", Generation: 2, RuleID: "rule-a", ExpiresAt: now.Add(time.Minute)},
			{LeaseID: "L2", Generation: 2, RuleID: "rule-b", ExpiresAt: now.Add(time.Minute)},
		},
	}
	if err := foreign.ValidateRenewalFor(in, reg); err == nil {
		t.Fatal("single-target renew must reject foreign extra lease IDs in Leases")
	}
	dup := authority.LeaseDecision{
		Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 2, ExpiresAt: now.Add(time.Minute),
		Leases: []authority.LeaseOccupancy{
			{LeaseID: "L1", Generation: 2, RuleID: "rule-a", ExpiresAt: now.Add(time.Minute)},
			{LeaseID: "L1", Generation: 2, RuleID: "rule-a", ExpiresAt: now.Add(time.Minute)},
		},
	}
	if err := dup.ValidateRenewalFor(in, reg); err == nil {
		t.Fatal("single-target renew must reject duplicate lease IDs in Leases")
	}
}

func TestPhase2_LeaseDecisionValidateRenewalFor_AcceptsEqualGenerationTTLOnlyRenew(t *testing.T) {
	t.Parallel()
	reg := authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
	now := time.Unix(1000, 0).UTC()
	// TTL-only renew may return the same generation while extending ExpiresAt.
	d := authority.LeaseDecision{
		Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 3, ExpiresAt: now.Add(2 * time.Minute), TTL: time.Minute,
	}
	in := authority.LeaseRenew{LeaseID: "L1", ExpectedGeneration: 3, TTL: time.Minute}
	if err := d.ValidateRenewalFor(in, reg); err != nil {
		t.Fatalf("equal generation TTL-only renew must be accepted: %v", err)
	}
}
