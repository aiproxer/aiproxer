package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestPhase6Remediation_ExternalSetShapeAndTimingValidation(t *testing.T) {
	t.Parallel()
	err := domain.ValidateSetDecisionShape("", 1, time.Now(), time.Minute, 15*time.Second, 2)
	if err == nil {
		t.Fatal("empty set_id must fail")
	}
	err = domain.ValidateSetDecisionShape("set-1", 0, time.Now(), time.Minute, 15*time.Second, 2)
	if err == nil {
		t.Fatal("generation 0 must fail")
	}
	err = domain.ValidateSetDecisionShape("set-1", 1, time.Time{}, time.Minute, 15*time.Second, 2)
	if err == nil {
		t.Fatal("zero expires_at must fail")
	}
	err = domain.ValidateSetDecisionShape("set-1", 1, time.Now(), time.Second, time.Second, 2)
	if err == nil {
		t.Fatal("renew_before == ttl must fail")
	}
	err = domain.ValidateSetDecisionShape("set-1", 1, time.Now(), time.Minute, 15*time.Second, 0)
	if !errors.Is(err, domain.ErrIncompleteSet) {
		t.Fatalf("empty members: %v", err)
	}
	reg := authority.ProviderDescriptor{
		ID: "conc",
		Postures: []authority.StagePosture{{
			Stage: authority.StageLeaseAdmit, Strength: authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
	exp := time.Now().Add(time.Minute)
	dec := authority.LeaseDecision{
		Kind: authority.LeaseAllow, SetID: "set-1", LeaseID: "L-a", Generation: 2,
		ExpiresAt: exp, TTL: time.Minute, RenewBefore: 15 * time.Second,
		Leases: []authority.LeaseOccupancy{
			{LeaseID: "L-a", Generation: 2, ExpiresAt: exp, TTL: time.Minute},
			{LeaseID: "L-b", Generation: 2, ExpiresAt: exp, TTL: time.Minute},
		},
	}
	if err := dec.ValidateRenewalFor(authority.LeaseRenew{
		SetID: "set-1", ExpectedGeneration: 1, TTL: time.Minute, RenewBefore: 15 * time.Second,
	}, reg); err != nil {
		t.Fatalf("valid set renew shape: %v", err)
	}
}

func TestPhase6Remediation_RollbackFailureDoesNotReleasePriorSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 20, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	ruleA := strictRule(5)
	ruleA.ID = "rule-a"
	ruleA.RenewBefore = 15 * time.Second
	svc := newService(t, []domain.Rule{ruleA}, store, now)
	first, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-keep", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil || first.Kind != domain.DecisionAllow {
		t.Fatalf("seed=%+v err=%v", first, err)
	}
	failing := &acquireErrorStore{memoryStore: store, ruleID: "rule-b", err: errors.New("store outage")}
	ruleB := strictRule(5)
	ruleB.ID = "rule-b"
	ruleB.RenewBefore = 15 * time.Second
	both := app.NewService(staticRules{snap: app.RuleSnapshot{
		Readiness: domain.Readiness{State: domain.ReadinessStateReady},
		Rules:     []domain.Rule{ruleA, ruleB},
	}}, failing, fixedClock{t: now})
	_, err = both.Admit(ctx, app.AdmitInput{
		RequestID: "req-keep", Scope: principalScope("alice"), Namespace: "default",
	})
	if err == nil {
		t.Fatal("want acquire set error")
	}
	if countLiveLeases(t, svc, "req-keep", now) != 1 {
		t.Fatal("rollback/error must not release pre-existing occupancy")
	}
}

func TestPhase6Remediation_AuxRetryParallelDoNotAddLogicalOccupancy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 20, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	rule := strictRule(1)
	rule.RenewBefore = 15 * time.Second
	svc := newService(t, []domain.Rule{rule}, store, now)
	parent, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-top", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil || parent.Kind != domain.DecisionAllow {
		t.Fatalf("parent=%+v err=%v", parent, err)
	}
	aux, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-aux", Scope: principalScope("alice"), Namespace: "default",
		Lifecycle: metering.LifecycleAuxiliaryRequest, ParentLeaseID: parent.LeaseID,
		AuxPolicy: domain.AuxPolicyInherit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if aux.LeaseID != parent.LeaseID || aux.Acquired {
		t.Fatalf("aux must inherit parent: %+v", aux)
	}
	retry, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-top", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Replayed {
		t.Fatalf("retry must replay set/lease: %+v", retry)
	}
	parallel, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-other", Scope: scope.PrincipalScopeView{PrincipalID: scope.Known("alice"), Origin: scope.OriginClient},
		Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if parallel.Kind != domain.DecisionDeny {
		t.Fatalf("parallel second logical request must deny at limit 1: %+v", parallel)
	}
}

func TestPhase6Remediation_ReadinessDegradesOnUncertain(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 18, 20, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	rule := strictRule(2)
	rule.RenewBefore = 15 * time.Second
	svc := newService(t, []domain.Rule{rule}, store, now)
	res, err := svc.Admit(context.Background(), app.AdmitInput{
		RequestID: "req-u", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil || res.SetID == "" {
		t.Fatalf("admit=%+v err=%v", res, err)
	}
	if err := store.MarkSetUncertain(context.Background(), res.SetID, now); err != nil {
		t.Fatal(err)
	}
	ready, err := svc.ReadinessDomain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != domain.ReadinessStateDegraded {
		t.Fatalf("state=%s want degraded", ready.State)
	}
	counts, err := svc.LeaseSetOccupancyCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts.Uncertain != 1 {
		t.Fatalf("counts=%+v", counts)
	}
}

func TestPhase6Remediation_ReadinessDegradesOnExpiring(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 18, 20, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	rule := strictRule(2)
	rule.RenewBefore = 15 * time.Second
	svc := newService(t, []domain.Rule{rule}, store, now)
	res, err := svc.Admit(context.Background(), app.AdmitInput{
		RequestID: "req-exp", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil || res.SetID == "" {
		t.Fatalf("admit=%+v err=%v", res, err)
	}
	store.mu.Lock()
	for id, l := range store.leases {
		if l.SetID == res.SetID {
			l.SetState = domain.LeaseSetStateExpiring
			l.State = domain.LeaseStateExpiring
			store.leases[id] = l
		}
	}
	store.mu.Unlock()
	ready, err := svc.ReadinessDomain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != domain.ReadinessStateDegraded {
		t.Fatalf("state=%s want degraded on expiring", ready.State)
	}
	counts, err := svc.LeaseSetOccupancyCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts.Expiring != 1 {
		t.Fatalf("counts=%+v", counts)
	}
}
