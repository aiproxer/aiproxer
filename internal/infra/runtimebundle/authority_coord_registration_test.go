package runtimebundle_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestBuild_RequestRegistration_NoIndexGeneratedIDs(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Production = runtimebundle.ProductionOptions{
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "prod-quota",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestAdmit,
					Strength:        authority.StrengthAdvisory,
					FailureBehavior: authority.FailureFailOpen,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: prodAllowRequest{},
		}},
	}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	slots := built.Executor().RequestCoordinator.Slots
	if len(slots) == 0 {
		t.Fatal("expected request slots from registration")
	}
	for _, s := range slots {
		if strings.HasPrefix(s.ID, "production-request-") {
			t.Fatalf("index-generated id %q forbidden", s.ID)
		}
		if s.ID == "prod-quota" {
			if s.Strength != authority.StrengthAdvisory {
				t.Fatalf("strength=%q want advisory", s.Strength)
			}
			if s.FailureBehavior != authority.FailureFailOpen {
				t.Fatalf("failure=%q want fail_open", s.FailureBehavior)
			}
			return
		}
	}
	t.Fatalf("slot prod-quota not found in %+v", slots)
}

func TestBuild_RejectsDuplicateRegistrationIDs(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	desc := authority.ProviderDescriptor{
		ID:   "dup",
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageRequestAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
	opts.Production = runtimebundle.ProductionOptions{
		RequestRegistrations: []authority.RequestRegistration{
			{Descriptor: desc, Priority: authority.RequestPriorityCreditWallet, Provider: prodAllowRequest{}},
			{Descriptor: desc, Priority: authority.RequestPriorityQuotaBudgetRate, Provider: prodAllowRequest{}},
		},
	}
	_, _, err := processAndCandidateErr(t, cfg, opts)
	if err == nil {
		t.Fatal("duplicate registration IDs must fail Build")
	}
}

func TestBuild_RegistrationReadinessProviderIDs(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Production = runtimebundle.ProductionOptions{
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "ready-quota",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: prodAllowRequest{},
		}},
	}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	if runtimebundle.CandidateReadinessReport(built) == nil {
		t.Fatal("expected readiness report")
	}
	report, err := runtimebundle.CandidateReadinessReport(built).Report(context.Background())
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	found := false
	for _, c := range report.Components {
		if c.Component != controlplane.ReadinessComponentRequestCoordinator {
			continue
		}
		found = true
		if len(c.ProviderIDs) != 1 || c.ProviderIDs[0] != "ready-quota" {
			t.Fatalf("ProviderIDs=%v want [ready-quota]", c.ProviderIDs)
		}
	}
	if !found {
		t.Fatal("request coordinator component missing")
	}
}

func TestBuild_AttemptAndConcurrencyRegistrations(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Production = runtimebundle.ProductionOptions{
		AttemptRegistrations: []authority.AttemptRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "prod-hard",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageAttemptAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.AttemptPriorityHardSpend,
			Provider: prodAllowAttempt{},
		}},
		ConcurrencyRegistration: &authority.ConcurrencyRegistration{
			Descriptor: authority.ProviderDescriptor{
				ID:   "prod-lease",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageLeaseAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Provider: prodAllowConcurrency{},
		},
	}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	if built.Executor().ConcurrencyProvider == nil {
		t.Fatal("concurrency registration must wire ConcurrencyProvider")
	}
	if built.Executor().AttemptCoordinator == nil || len(built.Executor().AttemptCoordinator.Slots) == 0 {
		t.Fatal("attempt registration must create attempt slots")
	}
	found := false
	for _, s := range built.Executor().AttemptCoordinator.Slots {
		if s.ID == "prod-hard" {
			found = true
			if s.Strength != authority.StrengthRequired || s.FailureBehavior != authority.FailureFailClosed {
				t.Fatalf("attempt slot posture strength=%q failure=%q", s.Strength, s.FailureBehavior)
			}
		}
		if strings.HasPrefix(s.ID, "production-attempt-") {
			t.Fatalf("index-generated attempt id %q forbidden", s.ID)
		}
	}
	if !found {
		t.Fatal("prod-hard attempt slot missing")
	}
}

func TestBuild_CanonicalRegistrationBoundary_NoLegacyFields(t *testing.T) {
	t.Parallel()
	// ProductionOptions is canonical-only after Task 8.2; legacy provider/rater
	// fields are not constructible here. Prove registration-only wiring still works.
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Production = runtimebundle.ProductionOptions{
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "boundary-quota",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: prodAllowRequest{},
		}},
	}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	if built.Executor().RequestCoordinator == nil || len(built.Executor().RequestCoordinator.Slots) == 0 {
		t.Fatal("canonical request registration must wire coordinator slots")
	}
}

type prodAllowAttempt struct{}

func (prodAllowAttempt) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (prodAllowAttempt) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (prodAllowAttempt) ReleaseAttempt(context.Context, authority.AttemptRelease) error { return nil }

type prodAllowConcurrency struct{}

func (prodAllowConcurrency) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "l1"}, nil
}

func (prodAllowConcurrency) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "l1"}, nil
}

func (prodAllowConcurrency) ReleaseLease(context.Context, authority.LeaseRelease) error { return nil }

func (prodAllowConcurrency) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}
