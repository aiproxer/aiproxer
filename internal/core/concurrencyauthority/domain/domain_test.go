package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestStableLeaseID_DeterministicAcrossRetries(t *testing.T) {
	t.Parallel()

	dims := domain.Dimensions{Principal: scope.Known("user-1"), Tenant: scope.Known("t1")}
	a := domain.StableLeaseID("ns", "rule-a", "v1", "req-42", dims)
	b := domain.StableLeaseID("ns", "rule-a", "v1", "req-42", dims)
	if a == "" || a != b {
		t.Fatalf("lease id unstable: %q vs %q", a, b)
	}
	other := domain.StableLeaseID("ns", "rule-a", "v1", "req-43", dims)
	if a == other {
		t.Fatal("different logical requests must not share lease id")
	}
}

func TestStableLeaseID_IncludesRuleIdentity(t *testing.T) {
	t.Parallel()

	dims := domain.Dimensions{Principal: scope.Known("user-1"), Tenant: scope.Known("t1")}
	a := domain.StableLeaseID("ns", "rule-a", "v1", "req-42", dims)
	b := domain.StableLeaseID("ns", "rule-b", "v1", "req-42", dims)
	if a == "" || b == "" {
		t.Fatalf("empty lease id: a=%q b=%q", a, b)
	}
	if a == b {
		t.Fatal("different RuleIDs must produce different lease ids")
	}
	same := domain.StableLeaseID("ns", "rule-a", "v1", "req-42", dims)
	if a != same {
		t.Fatalf("same RuleID must be idempotent: %q vs %q", a, same)
	}
}

func TestRule_SafeScopeMatching(t *testing.T) {
	t.Parallel()

	rule := domain.Rule{
		ID:    "max5",
		Mode:  domain.RuleModeStrict,
		Limit: 5,
		Match: domain.DimensionsMatcher{
			Principal: domain.DimensionMatcher{Value: scope.Known("alice")},
		},
	}
	if !rule.Matches(domain.Dimensions{Principal: scope.Known("alice")}) {
		t.Fatal("expected principal match")
	}
	if rule.Matches(domain.Dimensions{Principal: scope.Known("bob")}) {
		t.Fatal("expected principal mismatch")
	}
}

func TestLease_RenewGenerationCAS(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	lease := domain.NewLease(domain.NewLeaseParams{
		LeaseID:     "L1",
		RuleID:      "max5",
		RuleVersion: "v1",
		LogicalID:   "req-1",
		Namespace:   "ns",
		Dimensions:  domain.Dimensions{Principal: scope.Known("alice")},
		Now:         now,
		TTL:         time.Minute,
	})
	if lease.Generation != 1 || lease.State != domain.LeaseStateActive {
		t.Fatalf("new lease: gen=%d state=%s", lease.Generation, lease.State)
	}

	if err := lease.Renew(now.Add(30*time.Second), 1, time.Minute); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if lease.Generation != 2 {
		t.Fatalf("generation after renew = %d, want 2", lease.Generation)
	}

	if err := lease.Renew(now.Add(40*time.Second), 1, time.Minute); err == nil {
		t.Fatal("stale generation must fail CAS")
	}
}

func TestLease_CannotResurrectReleased(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	lease := domain.NewLease(domain.NewLeaseParams{
		LeaseID: "L1", RuleID: "max5", RuleVersion: "v1", LogicalID: "req-1",
		Namespace: "ns", Dimensions: domain.Dimensions{Principal: scope.Known("alice")},
		Now: now, TTL: time.Minute,
	})
	lease.Release(now.Add(time.Second))
	if err := lease.Renew(now.Add(2*time.Second), lease.Generation, time.Minute); err == nil {
		t.Fatal("renew after release must fail")
	}
	if lease.State != domain.LeaseStateReleased {
		t.Fatalf("state=%s", lease.State)
	}
}

func TestLease_Expiry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	lease := domain.NewLease(domain.NewLeaseParams{
		LeaseID: "L1", RuleID: "max5", RuleVersion: "v1", LogicalID: "req-1",
		Namespace: "ns", Dimensions: domain.Dimensions{Principal: scope.Known("alice")},
		Now: now, TTL: time.Minute,
	})
	if !lease.IsLive(now.Add(30 * time.Second)) {
		t.Fatal("should be live before expiry")
	}
	if lease.IsLive(now.Add(2 * time.Minute)) {
		t.Fatal("should not be live after wall-clock expiry")
	}
	lease.Expire(now.Add(2 * time.Minute))
	if lease.State != domain.LeaseStateExpired {
		t.Fatalf("state=%s", lease.State)
	}
	if err := lease.Renew(now.Add(3*time.Minute), lease.Generation, time.Minute); err == nil {
		t.Fatal("renew after expire must fail")
	}
}

func TestLease_ReleaseIdempotent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	lease := domain.NewLease(domain.NewLeaseParams{
		LeaseID: "L1", RuleID: "max5", RuleVersion: "v1", LogicalID: "req-1",
		Namespace: "ns", Dimensions: domain.Dimensions{Principal: scope.Known("alice")},
		Now: now, TTL: time.Minute,
	})
	lease.Release(now.Add(time.Second))
	lease.Release(now.Add(2 * time.Second))
	if lease.State != domain.LeaseStateReleased {
		t.Fatalf("state=%s", lease.State)
	}
}

func TestDenialEvidence_ConcurrencyLimitCategory(t *testing.T) {
	t.Parallel()

	ev := domain.DenialEvidence("max5", 0)
	if ev.Category != domain.EvidenceCategoryConcurrencyLimit {
		t.Fatalf("category=%q", ev.Category)
	}
	if strings.Contains(strings.ToLower(ev.Message), "lease-") {
		t.Fatalf("message must not reveal lease ids: %q", ev.Message)
	}
	if strings.Contains(ev.Message, "alice") || strings.Contains(ev.Message, "bob") {
		t.Fatalf("message must not reveal other principals: %q", ev.Message)
	}
	if ev.RuleID != "max5" {
		t.Fatalf("rule_id=%q", ev.RuleID)
	}
}
