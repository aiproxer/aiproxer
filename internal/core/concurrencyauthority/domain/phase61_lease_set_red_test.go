package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
)

func TestPhase61_RuleValidateTimingRequiresRenewBeforeLessThanTTL(t *testing.T) {
	t.Parallel()
	ok := domain.Rule{LeaseTTL: time.Minute, RenewBefore: 15 * time.Second}
	if err := ok.ValidateTiming(); err != nil {
		t.Fatalf("valid timing: %v", err)
	}
	badEqual := domain.Rule{LeaseTTL: time.Minute, RenewBefore: time.Minute}
	if err := badEqual.ValidateTiming(); !errors.Is(err, domain.ErrInvalidTiming) {
		t.Fatalf("equal renew_before must fail: %v", err)
	}
	badZero := domain.Rule{LeaseTTL: time.Minute, RenewBefore: 0}
	// EffectiveRenewBefore defaults to 15s which is valid — explicit zero after Effective is ok.
	// Require explicit invalid when RenewBefore >= TTL via ValidateTiming on raw values:
	if err := domain.ValidateTiming(time.Second, 0); !errors.Is(err, domain.ErrInvalidTiming) {
		t.Fatalf("zero renew_before must fail: %v", err)
	}
	if err := domain.ValidateTiming(0, time.Second); !errors.Is(err, domain.ErrInvalidTiming) {
		t.Fatalf("zero ttl must fail: %v", err)
	}
	_ = badZero
}

func TestPhase61_StableSetIDDeterministicAndOrderIndependent(t *testing.T) {
	t.Parallel()
	a := domain.StableSetID("ns", "req-1", []string{"rule-b", "rule-a"})
	b := domain.StableSetID("ns", "req-1", []string{"rule-a", "rule-b"})
	if a == "" || a != b {
		t.Fatalf("set id unstable: %q vs %q", a, b)
	}
	other := domain.StableSetID("ns", "req-2", []string{"rule-a", "rule-b"})
	if a == other {
		t.Fatal("different requests must not share set id")
	}
}

func TestPhase61_UncertainSetStillOccupiesCapacity(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	set := domain.LeaseSet{
		SetID: "cset_1", RequestID: "req-1", Generation: 1,
		State: domain.LeaseSetStateActive, ExpiresAt: now.Add(time.Minute),
		Members: []domain.Lease{{LeaseID: "L1", RuleID: "r1", State: domain.LeaseStateActive}},
	}
	if err := set.MarkUncertain(now); err != nil {
		t.Fatal(err)
	}
	if set.State != domain.LeaseSetStateUncertain {
		t.Fatalf("state=%s", set.State)
	}
	if !set.OccupiesCapacity(now.Add(2 * time.Minute)) {
		t.Fatal("uncertain occupancy must remain counted after wall expiry")
	}
}

func TestPhase61_ValidateSetDecisionShape(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	err := domain.ValidateSetDecisionShape("cset_1", 1, now.Add(time.Minute), time.Minute, 15*time.Second, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := domain.ValidateSetDecisionShape("", 1, now.Add(time.Minute), time.Minute, 15*time.Second, 1); err == nil {
		t.Fatal("empty set id must fail")
	}
	if err := domain.ValidateSetDecisionShape("cset_1", 1, now.Add(time.Minute), time.Minute, 15*time.Second, 0); !errors.Is(err, domain.ErrIncompleteSet) {
		t.Fatalf("empty members: %v", err)
	}
}
