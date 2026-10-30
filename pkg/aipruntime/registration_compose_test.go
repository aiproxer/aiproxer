package aipruntime_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

func assertBuiltRuntimeReady(t *testing.T, rt *aipruntime.Runtime) {
	t.Helper()
	if !rt.Ready() {
		t.Fatal("expected ready runtime")
	}
	if rt.ExecutorView() == nil {
		t.Fatal("expected ExecutorView")
	}
	if st := rt.ReloadStatus(); st.ActiveGeneration < 1 {
		t.Fatalf("active generation=%d want >= 1", st.ActiveGeneration)
	}
}

func TestBuild_DescriptorBoundRequestRegistration_PreservesID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath: repoConfigPath(t),
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "enterprise-quota",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: allowRequestProvider{},
		}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	assertBuiltRuntimeReady(t, rt)
}

func TestBuild_RejectsDuplicateRequestRegistrationIDs(t *testing.T) {
	t.Parallel()
	desc := func(id string) authority.ProviderDescriptor {
		return authority.ProviderDescriptor{
			ID:   id,
			Kind: authority.ProviderKindAuthority,
			Postures: []authority.StagePosture{{
				Stage:           authority.StageRequestAdmit,
				Strength:        authority.StrengthRequired,
				FailureBehavior: authority.FailureFailClosed,
			}},
		}
	}
	_, err := aipruntime.Build(context.Background(), aipruntime.Options{
		ConfigPath: repoConfigPath(t),
		RequestRegistrations: []authority.RequestRegistration{
			{Descriptor: desc("dup"), Priority: authority.RequestPriorityCreditWallet, Provider: allowRequestProvider{}},
			{Descriptor: desc("dup"), Priority: authority.RequestPriorityQuotaBudgetRate, Provider: allowRequestProvider{}},
		},
	})
	if err == nil {
		t.Fatal("duplicate request registration IDs must fail startup")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuild_AttemptRegistration_PreservesID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath: repoConfigPath(t),
		AttemptRegistrations: []authority.AttemptRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "enterprise-hard-spend",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageAttemptAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.AttemptPriorityHardSpend,
			Provider: allowAttemptProvider{},
		}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	assertBuiltRuntimeReady(t, rt)
}

func TestBuild_ConcurrencyRegistration_WiresProvider(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath: repoConfigPath(t),
		ConcurrencyRegistration: &authority.ConcurrencyRegistration{
			Descriptor: authority.ProviderDescriptor{
				ID:   "enterprise-concurrency",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageLeaseAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Provider: allowConcurrencyProvider{},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if !rt.Ready() {
		t.Fatal("expected ready runtime with concurrency registration")
	}
}

func TestBuild_RejectsSettleOnlyRequestRegistration(t *testing.T) {
	t.Parallel()
	_, err := aipruntime.Build(context.Background(), aipruntime.Options{
		ConfigPath: repoConfigPath(t),
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "settle-only",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestSettle,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: allowRequestProvider{},
		}},
	})
	if err == nil {
		t.Fatal("settle-only request registration must fail Build")
	}
	if !strings.Contains(err.Error(), "admit") {
		t.Fatalf("err=%v want admit posture error", err)
	}
}

type allowAttemptProvider struct{}

func (allowAttemptProvider) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow, ProviderID: "enterprise-hard-spend"}, nil
}

func (allowAttemptProvider) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (allowAttemptProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	return nil
}

type allowConcurrencyProvider struct{}

func (allowConcurrencyProvider) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "lease-1"}, nil
}

func (allowConcurrencyProvider) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "lease-1"}, nil
}

func (allowConcurrencyProvider) ReleaseLease(context.Context, authority.LeaseRelease) error {
	return nil
}

func (allowConcurrencyProvider) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}
