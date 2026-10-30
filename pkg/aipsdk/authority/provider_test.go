package authority_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

type fakeRequestProvider struct{}

func (fakeRequestProvider) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (fakeRequestProvider) SettleRequest(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (fakeRequestProvider) ReleaseRequest(context.Context, authority.RequestRelease) error {
	return nil
}

type fakeAttemptProvider struct{}

func (fakeAttemptProvider) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (fakeAttemptProvider) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (fakeAttemptProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	return nil
}

type fakeConcurrencyProvider struct{}

func (fakeConcurrencyProvider) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "lease-1"}, nil
}

func (fakeConcurrencyProvider) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "lease-1", Generation: 2}, nil
}

func (fakeConcurrencyProvider) ReleaseLease(context.Context, authority.LeaseRelease) error {
	return nil
}

func (fakeConcurrencyProvider) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func TestProvidersCompileWithFakes(t *testing.T) {
	t.Parallel()
	var req authority.RequestProvider = fakeRequestProvider{}
	var att authority.AttemptProvider = fakeAttemptProvider{}
	var conc authority.ConcurrencyProvider = fakeConcurrencyProvider{}
	ctx := context.Background()
	if _, err := req.AdmitRequest(ctx, authority.RequestAdmission{RequestID: "r1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := att.AdmitAttempt(ctx, authority.AttemptAdmission{
		RequestID: "r1", AttemptID: "a1", BLegID: "b1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := conc.AdmitLease(ctx, authority.LeaseAdmission{RequestID: "r1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := conc.RenewLease(ctx, authority.LeaseRenew{LeaseID: "lease-1", ExpectedGeneration: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestRequestAdmissionLacksAttemptIdentity(t *testing.T) {
	t.Parallel()
	in := authority.RequestAdmission{
		RequestID:   "req-1",
		ALegID:      "a-1",
		Perspective: metering.PerspectiveCustomer,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveCustomer,
			Boundary:    metering.BoundaryFrontendIngress,
			Lifecycle:   metering.LifecycleLogicalRequest,
		},
	}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	// RequestAdmission has no AttemptID/BLegID fields — compile-time separation.
	_ = struct {
		RequestID string
		ALegID    string
	}{in.RequestID, in.ALegID}
}

func TestAttemptAdmissionRequiresAttemptIdentity(t *testing.T) {
	t.Parallel()
	missing := authority.AttemptAdmission{RequestID: "r1"}
	if err := missing.Validate(); err == nil {
		t.Fatal("attempt/B-leg identity required")
	}
	ok := authority.AttemptAdmission{
		RequestID:   "r1",
		AttemptID:   "att-1",
		BLegID:      "b-1",
		Perspective: metering.PerspectiveOperator,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
		},
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionDTOFields(t *testing.T) {
	t.Parallel()
	d := authority.Decision{
		Kind:               authority.DecisionAllow,
		Reservations:       []authority.Reservation{{Handle: "h1", Kind: authority.ReservationQuota}},
		Clamps:             []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: 100}},
		CompensationHandle: "comp-1",
		Readiness:          authority.ReadinessReady,
		BoundVersions:      []economics.PolicySnapshotRef{{VersionRef: economics.VersionRef{ID: "p", Version: "1"}}},
		Exposure:           economics.ExposureBasis{Perspective: metering.PerspectiveCustomer},
		Evidence:           authority.SafeEvidence{Category: "ok", Message: "allowed"},
	}
	if !d.Kind.IsKnown() || d.Evidence.Message == "" {
		t.Fatal("decision fields")
	}
	deny := authority.Decision{Kind: authority.DecisionDeny, Evidence: authority.SafeEvidence{Category: "quota_exceeded"}}
	if !deny.Kind.IsKnown() {
		t.Fatal("deny")
	}
	if !authority.ReservationQuota.IsKnown() || !authority.ClampMaxOutputTokens.IsKnown() || !authority.SettlementFinal.IsKnown() {
		t.Fatal("enum IsKnown helpers")
	}
}

func TestProviderDescriptorPosture(t *testing.T) {
	t.Parallel()
	desc := authority.ProviderDescriptor{
		ID:   "customer-quota",
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageRequestAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
	if err := desc.Validate(); err != nil {
		t.Fatal(err)
	}
	if desc.EffectiveKind() != authority.ProviderKindAuthority {
		t.Fatalf("EffectiveKind=%q", desc.EffectiveKind())
	}
	var d authority.Describer = staticDescriber{desc}
	if got := d.Describe(); got.ID != "customer-quota" {
		t.Fatalf("%#v", got)
	}
	bad := authority.ProviderDescriptor{ID: "x", Postures: []authority.StagePosture{{
		Stage: authority.StageRequestAdmit, Strength: "nope", FailureBehavior: authority.FailureFailOpen,
	}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown strength must fail")
	}
}

func TestProviderDescriptor_omittedKindDefaultsToAuthority(t *testing.T) {
	t.Parallel()
	desc := authority.ProviderDescriptor{
		ID: "legacy-quota",
		Postures: []authority.StagePosture{{
			Stage:           authority.StageRequestAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
	if err := desc.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := desc.EffectiveKind(); got != authority.ProviderKindAuthority {
		t.Fatalf("EffectiveKind=%q want authority (additive optional Kind)", got)
	}
}

func TestProviderDescriptor_observerCannotBeRequired(t *testing.T) {
	t.Parallel()
	observer := authority.ProviderDescriptor{
		ID:   "traffic-observer",
		Kind: authority.ProviderKindObserver,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageRequestAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailOpen,
		}},
	}
	if err := observer.Validate(); err == nil {
		t.Fatal("observer + required strength must fail (requirement 12.7)")
	}
	ok := authority.ProviderDescriptor{
		ID:   "traffic-observer",
		Kind: authority.ProviderKindObserver,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageRequestAdmit,
			Strength:        authority.StrengthAdvisory,
			FailureBehavior: authority.FailureFailOpen,
		}},
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

type staticDescriber struct{ d authority.ProviderDescriptor }

func (s staticDescriber) Describe() authority.ProviderDescriptor { return s.d }
