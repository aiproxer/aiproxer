package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/runtimegen"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type phase5RemedReq struct{ id string }

func (s phase5RemedReq) Describe() authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID: s.id, Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func (s phase5RemedReq) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (s phase5RemedReq) SettleRequest(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
	return authority.Settlement{}, nil
}

func (s phase5RemedReq) ReleaseRequest(context.Context, authority.RequestRelease) error { return nil }

type phase5RemedConc struct{ id string }

func (s phase5RemedConc) Describe() authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID: s.id, Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage: authority.StageLeaseAdmit, Strength: authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func (s phase5RemedConc) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "L-" + s.id, Generation: 1}, nil
}

func (s phase5RemedConc) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "L-" + s.id, Generation: 2}, nil
}
func (s phase5RemedConc) ReleaseLease(context.Context, authority.LeaseRelease) error { return nil }
func (s phase5RemedConc) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func TestPhase5Remediation_BindBeforeAdmitUsesBoundGeneration(t *testing.T) {
	t.Parallel()
	pub := snapshotgen.NewPublisher()
	g1, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID: "static", Version: "bind-g1", State: economics.SnapshotReady,
		ConcurrencyRegistration: &authority.ConcurrencyRegistration{
			Descriptor: phase5RemedConc{id: "conc"}.Describe(),
			Provider:   phase5RemedConc{id: "conc"},
		},
		MaxActiveRequests: 5,
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: phase5RemedReq{id: "quota-v1"}.Describe(),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   phase5RemedReq{id: "quota-v1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pub.PublishExecutable(g1); err != nil {
		t.Fatal(err)
	}
	// Build-time coordinator is intentionally nil so admit must use bound generation.
	ex := &Executor{AccountingRuntime: AccountingRuntime{SnapshotGeneration: pub}}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-bind", "a-1", "tr-bind", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	st := requestAuthorityFrom(ctx)
	if st == nil || st.ExecutableGen == nil {
		t.Fatal("request must bind ExecutableGen before/during admit")
	}
	if st.ExecutableGen.Version != "bind-g1" {
		t.Fatalf("bound version=%q", st.ExecutableGen.Version)
	}
	if st.ExecutableGen.LiveRefs() < 1 {
		t.Fatal("bound generation must be retained for request lifetime")
	}
	boundEvidence := st.ExecutableGen.EvidenceObjectID()

	g2, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID: "static", Version: "bind-g2", State: economics.SnapshotReady,
		ConcurrencyRegistration: &authority.ConcurrencyRegistration{
			Descriptor: phase5RemedConc{id: "conc"}.Describe(),
			Provider:   phase5RemedConc{id: "conc"},
		},
		MaxActiveRequests: 2,
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: phase5RemedReq{id: "quota-v2"}.Describe(),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   phase5RemedReq{id: "quota-v2"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pub.PublishExecutable(g2); err != nil {
		t.Fatal(err)
	}
	if pub.CurrentExecutable().Version != "bind-g2" {
		t.Fatal("refresh must publish new current executable")
	}
	if st.ExecutableGen.Version != "bind-g1" {
		t.Fatal("in-flight binding must remain on old generation")
	}
	res := usagelimit.AdmissionResult{}
	ex.applyGenerationBoundVersionFrom(st.ExecutableGen, &res)
	if res.BoundVersion.ID != boundEvidence || res.BoundVersion.Version != "bind-g1" {
		t.Fatalf("settlement evidence must name bound generation objects: %+v", res.BoundVersion)
	}
	if res.BoundVersion.Version == pub.CurrentExecutable().Version {
		t.Fatal("mid-flight refresh must not rewrite bound settlement evidence")
	}
	coord := ex.requestCoordinatorFor(st)
	if coord == nil || coord != st.ExecutableGen.RequestCoord {
		t.Fatal("settle/release path must use bound generation RequestCoord")
	}
	if err := ex.releaseRequestAuthority(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}
}
