package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/runtimegen"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type generationQuotaBindingProvider struct {
	id       string
	allow    bool
	admitted atomic.Int64
}

func (p *generationQuotaBindingProvider) Describe() authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID: p.id, Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func (*generationQuotaBindingProvider) GenerationScopedRequestProvider() {}

func (p *generationQuotaBindingProvider) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	p.admitted.Add(1)
	if !p.allow {
		return authority.Decision{Kind: authority.DecisionDeny}, nil
	}
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (*generationQuotaBindingProvider) SettleRequest(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
	return authority.Settlement{}, nil
}

func (*generationQuotaBindingProvider) ReleaseRequest(context.Context, authority.RequestRelease) error {
	return nil
}

func TestAdmitRequestAuthority_UsesCandidateGenerationScopedQuotaWithBoundSnapshot(t *testing.T) {
	old := &generationQuotaBindingProvider{id: "old", allow: false}
	newQuota := &generationQuotaBindingProvider{id: "new", allow: true}
	descriptor := old.Describe()
	pub := snapshotgen.NewPublisher()
	gen, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID: "static", Version: "bound-v1", State: economics.SnapshotReady,
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: descriptor, Priority: authority.RequestPriorityQuotaBudgetRate, Provider: old,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pub.PublishExecutable(gen); err != nil {
		t.Fatal(err)
	}

	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	ex.SnapshotGeneration = pub
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{Slots: []authoritycoord.RequestSlot{{
		ID: newQuota.id, Class: authoritycoord.PriorityQuotaBudgetRate, Provider: newQuota,
		Strength: authority.StrengthRequired, FailureBehavior: authority.FailureFailClosed,
		Descriptor: func() *authority.ProviderDescriptor { d := newQuota.Describe(); return &d }(),
		Stage:      authority.StageRequestAdmit,
	}}}

	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "request-1", aLegID, "trace-1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admitRequestAuthorityOnce: %v", err)
	}
	defer func() { _ = ex.releaseRequestAuthority(ctx) }()
	if old.admitted.Load() != 0 {
		t.Fatalf("bound stale quota provider admitted %d requests", old.admitted.Load())
	}
	if newQuota.admitted.Load() != 1 {
		t.Fatalf("candidate quota provider admitted %d requests, want 1", newQuota.admitted.Load())
	}
	st := requestAuthorityFrom(ctx)
	if st == nil || st.Coordinator != ex.RequestCoordinator {
		t.Fatal("request settlement must retain the candidate coordinator used for admission")
	}
}
