package authority_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

type stubRequestProvider struct{ id string }

func (s stubRequestProvider) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow, ProviderID: s.id}, nil
}

func (s stubRequestProvider) SettleRequest(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (s stubRequestProvider) ReleaseRequest(context.Context, authority.RequestRelease) error {
	return nil
}

type describingRequestProvider struct {
	stubRequestProvider
	desc authority.ProviderDescriptor
}

func (d describingRequestProvider) Describe() authority.ProviderDescriptor { return d.desc }

type stubAttemptProvider struct{ id string }

func (s stubAttemptProvider) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow, ProviderID: s.id}, nil
}

func (s stubAttemptProvider) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (s stubAttemptProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	return nil
}

type stubConcurrencyProvider struct{}

func (stubConcurrencyProvider) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow}, nil
}

func (stubConcurrencyProvider) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow}, nil
}

func (stubConcurrencyProvider) ReleaseLease(context.Context, authority.LeaseRelease) error {
	return nil
}

func (stubConcurrencyProvider) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func requestDesc(id string) authority.ProviderDescriptor {
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

func attemptDesc(id string) authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID:   id,
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageAttemptAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func leaseDesc(id string) authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID:   id,
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func TestCompileContract_RequestRegistration(t *testing.T) {
	t.Parallel()
	reg := authority.RequestRegistration{
		Descriptor: requestDesc("quota"),
		Priority:   authority.RequestPriorityQuotaBudgetRate,
		Provider:   stubRequestProvider{id: "quota"},
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("valid registration: %v", err)
	}
}

func TestCompileContract_AttemptRegistration(t *testing.T) {
	t.Parallel()
	reg := authority.AttemptRegistration{
		Descriptor: attemptDesc("hard"),
		Priority:   authority.AttemptPriorityHardSpend,
		Provider:   stubAttemptProvider{id: "hard"},
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("valid registration: %v", err)
	}
}

func TestCompileContract_ConcurrencyRegistration(t *testing.T) {
	t.Parallel()
	reg := authority.ConcurrencyRegistration{
		Descriptor: leaseDesc("conc"),
		Provider:   stubConcurrencyProvider{},
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("valid registration: %v", err)
	}
}

func TestRequestRegistration_RejectsNilProvider(t *testing.T) {
	t.Parallel()
	reg := authority.RequestRegistration{
		Descriptor: requestDesc("quota"),
		Priority:   authority.RequestPriorityQuotaBudgetRate,
	}
	if err := reg.Validate(); err == nil {
		t.Fatal("nil provider must be rejected")
	}
}

func TestRequestRegistration_RejectsAttemptStage(t *testing.T) {
	t.Parallel()
	reg := authority.RequestRegistration{
		Descriptor: attemptDesc("wrong-stage"),
		Priority:   authority.RequestPriorityQuotaBudgetRate,
		Provider:   stubRequestProvider{id: "wrong-stage"},
	}
	if err := reg.Validate(); err == nil {
		t.Fatal("attempt-stage descriptor on request registration must be rejected")
	}
}

func TestRequestRegistration_RejectsObserverKind(t *testing.T) {
	t.Parallel()
	desc := requestDesc("obs")
	desc.Kind = authority.ProviderKindObserver
	desc.Postures[0].Strength = authority.StrengthAdvisory
	desc.Postures[0].FailureBehavior = authority.FailureFailOpen
	reg := authority.RequestRegistration{
		Descriptor: desc,
		Priority:   authority.RequestPriorityAdvisory,
		Provider:   stubRequestProvider{id: "obs"},
	}
	if err := reg.Validate(); err == nil {
		t.Fatal("observer kind must not register as request authority")
	}
}

func TestRequestRegistration_RejectsUnknownPriority(t *testing.T) {
	t.Parallel()
	reg := authority.RequestRegistration{
		Descriptor: requestDesc("quota"),
		Priority:   authority.RequestPriority("nope"),
		Provider:   stubRequestProvider{id: "quota"},
	}
	if err := reg.Validate(); err == nil {
		t.Fatal("unknown priority must be rejected")
	}
}

func TestRequestRegistration_RejectsDescriberMismatch(t *testing.T) {
	t.Parallel()
	reg := authority.RequestRegistration{
		Descriptor: requestDesc("quota-a"),
		Priority:   authority.RequestPriorityQuotaBudgetRate,
		Provider: describingRequestProvider{
			stubRequestProvider: stubRequestProvider{id: "quota-b"},
			desc:                requestDesc("quota-b"),
		},
	}
	if err := reg.Validate(); err == nil {
		t.Fatal("Describer ID mismatch must be rejected")
	}
}

func TestRequestPriority_KnownValues(t *testing.T) {
	t.Parallel()
	for _, p := range []authority.RequestPriority{
		authority.RequestPriorityConcurrency,
		authority.RequestPriorityCreditWallet,
		authority.RequestPriorityQuotaBudgetRate,
		authority.RequestPriorityAdvisory,
	} {
		if err := p.Validate(); err != nil {
			t.Fatalf("priority %q: %v", p, err)
		}
	}
}

func TestAttemptPriority_KnownValues(t *testing.T) {
	t.Parallel()
	for _, p := range []authority.AttemptPriority{
		authority.AttemptPriorityHardSpend,
		authority.AttemptPriorityQuotaRate,
		authority.AttemptPriorityAdvisory,
	} {
		if err := p.Validate(); err != nil {
			t.Fatalf("priority %q: %v", p, err)
		}
	}
}

func TestConcurrencyRegistration_RejectsRequestStage(t *testing.T) {
	t.Parallel()
	reg := authority.ConcurrencyRegistration{
		Descriptor: requestDesc("not-lease"),
		Provider:   stubConcurrencyProvider{},
	}
	if err := reg.Validate(); err == nil {
		t.Fatal("request-stage descriptor on concurrency registration must be rejected")
	}
}

func TestRequestRegistration_RejectsEmptyID(t *testing.T) {
	t.Parallel()
	desc := requestDesc("x")
	desc.ID = "  "
	reg := authority.RequestRegistration{
		Descriptor: desc,
		Priority:   authority.RequestPriorityQuotaBudgetRate,
		Provider:   stubRequestProvider{id: "x"},
	}
	err := reg.Validate()
	if err == nil {
		t.Fatal("whitespace provider id must be rejected")
	}
	if !strings.Contains(err.Error(), "provider id") {
		t.Fatalf("err=%v", err)
	}
}

func TestRequestRegistration_RejectsSettleOnlyDescriptor(t *testing.T) {
	t.Parallel()
	reg := authority.RequestRegistration{
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
		Provider: stubRequestProvider{id: "settle-only"},
	}
	if err := reg.Validate(); err == nil {
		t.Fatal("settle-only descriptor must fail without request admit posture")
	}
}

func TestAttemptRegistration_RejectsSettleOnlyDescriptor(t *testing.T) {
	t.Parallel()
	reg := authority.AttemptRegistration{
		Descriptor: authority.ProviderDescriptor{
			ID:   "settle-only",
			Kind: authority.ProviderKindAuthority,
			Postures: []authority.StagePosture{{
				Stage:           authority.StageAttemptSettle,
				Strength:        authority.StrengthRequired,
				FailureBehavior: authority.FailureFailClosed,
			}},
		},
		Priority: authority.AttemptPriorityHardSpend,
		Provider: stubAttemptProvider{id: "settle-only"},
	}
	if err := reg.Validate(); err == nil {
		t.Fatal("settle-only attempt descriptor must fail without attempt admit posture")
	}
}

func TestConcurrencyRegistration_RejectsReleaseOnlyDescriptor(t *testing.T) {
	t.Parallel()
	reg := authority.ConcurrencyRegistration{
		Descriptor: authority.ProviderDescriptor{
			ID:   "release-only",
			Kind: authority.ProviderKindAuthority,
			Postures: []authority.StagePosture{{
				Stage:           authority.StageLeaseRelease,
				Strength:        authority.StrengthRequired,
				FailureBehavior: authority.FailureFailClosed,
			}},
		},
		Provider: stubConcurrencyProvider{},
	}
	if err := reg.Validate(); err == nil {
		t.Fatal("release-only lease descriptor must fail without lease admit posture")
	}
}

func TestRequestRegistration_RejectsTypedNilProvider(t *testing.T) {
	t.Parallel()
	var typedNil *ptrRequestProvider
	reg := authority.RequestRegistration{
		Descriptor: requestDesc("quota"),
		Priority:   authority.RequestPriorityQuotaBudgetRate,
		Provider:   typedNil,
	}
	if err := reg.Validate(); err == nil {
		t.Fatal("typed-nil provider must be rejected")
	}
}

type ptrRequestProvider struct{ stubRequestProvider }

func (p *ptrRequestProvider) AdmitRequest(ctx context.Context, in authority.RequestAdmission) (authority.Decision, error) {
	return p.stubRequestProvider.AdmitRequest(ctx, in)
}

func (p *ptrRequestProvider) SettleRequest(ctx context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	return p.stubRequestProvider.SettleRequest(ctx, in)
}

func (p *ptrRequestProvider) ReleaseRequest(ctx context.Context, in authority.RequestRelease) error {
	return p.stubRequestProvider.ReleaseRequest(ctx, in)
}

func TestRequireAdmitPosture_ErrorsWhenMissing(t *testing.T) {
	t.Parallel()
	_, err := authority.RequireAdmitPosture(authority.ProviderDescriptor{
		ID:   "x",
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageRequestSettle,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}, authority.StageRequestAdmit)
	if err == nil {
		t.Fatal("RequireAdmitPosture must error when admit stage absent")
	}
}
