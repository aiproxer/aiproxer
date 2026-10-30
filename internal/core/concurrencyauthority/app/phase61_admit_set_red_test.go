package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestPhase61_AdmitUsesAtomicSetForMultiRule(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	rules := []domain.Rule{
		{
			ID: "rule-a", Namespace: "default", Version: "v1", Mode: domain.RuleModeStrict, Limit: 2,
			LeaseTTL: time.Minute, RenewBefore: 15 * time.Second,
			Match: domain.DimensionsMatcher{Principal: domain.DimensionMatcher{Value: scope.Known("alice")}},
		},
		{
			ID: "rule-b", Namespace: "default", Version: "v1", Mode: domain.RuleModeStrict, Limit: 2,
			LeaseTTL: time.Minute, RenewBefore: 15 * time.Second,
			Match: domain.DimensionsMatcher{Principal: domain.DimensionMatcher{Value: scope.Known("alice")}},
		},
	}
	svc := newService(t, rules, store, now)
	res, err := svc.Admit(context.Background(), app.AdmitInput{
		RequestID: "req-set-1", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != domain.DecisionAllow || len(res.Leases) != 2 {
		t.Fatalf("want 2-member set allow, got %+v", res)
	}
	var setIDs int
	for _, l := range store.leases {
		if l.SetID != "" {
			setIDs++
		}
	}
	if setIDs != 2 {
		t.Fatalf("members with set id=%d", setIDs)
	}
}

func TestPhase61_AdmitRejectsInvalidTiming(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	rules := []domain.Rule{{
		ID: "bad-timing", Namespace: "default", Version: "v1", Mode: domain.RuleModeStrict, Limit: 1,
		LeaseTTL: time.Second, RenewBefore: time.Second,
		Match: domain.DimensionsMatcher{Principal: domain.DimensionMatcher{Value: scope.Known("alice")}},
	}}
	svc := newService(t, rules, store, now)
	_, err := svc.Admit(context.Background(), app.AdmitInput{
		RequestID: "req-bad", Scope: principalScope("alice"), Namespace: "default",
	})
	if err == nil {
		t.Fatal("invalid renew_before == ttl must fail admit")
	}
}
