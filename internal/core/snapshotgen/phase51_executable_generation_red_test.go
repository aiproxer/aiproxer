package snapshotgen_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/runtimegen"
)

type stubRequestProvider struct {
	id string
}

func (s stubRequestProvider) Describe() authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID:   s.id,
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageRequestAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func (s stubRequestProvider) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (s stubRequestProvider) SettleRequest(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
	return authority.Settlement{}, nil
}

func (s stubRequestProvider) ReleaseRequest(context.Context, authority.RequestRelease) error {
	return nil
}

type stubConcurrencyProvider struct{ id string }

func (s stubConcurrencyProvider) Describe() authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID:   s.id,
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func (s stubConcurrencyProvider) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "L1"}, nil
}

func (s stubConcurrencyProvider) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "L1"}, nil
}

func (s stubConcurrencyProvider) ReleaseLease(context.Context, authority.LeaseRelease) error {
	return nil
}

func (s stubConcurrencyProvider) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func concReg(id string, prov authority.ConcurrencyProvider) *authority.ConcurrencyRegistration {
	return &authority.ConcurrencyRegistration{
		Descriptor: authority.ProviderDescriptor{
			ID:   id,
			Kind: authority.ProviderKindAuthority,
			Postures: []authority.StagePosture{{
				Stage:           authority.StageLeaseAdmit,
				Strength:        authority.StrengthRequired,
				FailureBehavior: authority.FailureFailClosed,
			}},
		},
		Provider: prov,
	}
}

func TestPhase51_MetadataOnlyGenerationRejected(t *testing.T) {
	t.Parallel()
	meta := &snapshotgen.ExecutableGeneration{
		Version: "meta-v1",
		State:   economics.SnapshotReady,
		Concurrency: economics.Snapshot[economics.PolicyRulesView]{
			ID: "concurrency", Version: "c-five", State: economics.SnapshotReady,
			Value: economics.PolicyRulesView{Kind: economics.PolicyKindConcurrency},
		},
	}
	if err := meta.ValidateComplete(); err == nil {
		t.Fatal("metadata-only ready generation must be rejected (D10)")
	}
	p := snapshotgen.NewPublisher()
	if _, err := p.PublishExecutable(meta); err == nil {
		t.Fatal("PublishExecutable must reject metadata-only generation")
	}
	if p.CurrentExecutable() != nil {
		t.Fatal("failed publish must leave prior executable nil")
	}
}

func TestPhase51_FiveToTwoRefreshPreservesInFlight(t *testing.T) {
	t.Parallel()
	p := snapshotgen.NewPublisher()
	g1, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID:                "static",
		Version:                 "g1",
		State:                   economics.SnapshotReady,
		ConcurrencyRegistration: concReg("conc", stubConcurrencyProvider{id: "conc"}),
		MaxActiveRequests:       5,
	})
	if err != nil {
		t.Fatal(err)
	}
	pub1, err := p.PublishExecutable(g1)
	if err != nil {
		t.Fatal(err)
	}
	pub1.Retain()
	held := p.LookupExecutable(pub1.ID)
	if held == nil || held.EnforcementMaxActive() != 5 {
		t.Fatalf("held g1 max=%d", held.EnforcementMaxActive())
	}

	g2, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID:                "static",
		Version:                 "g2",
		State:                   economics.SnapshotReady,
		ConcurrencyRegistration: concReg("conc", stubConcurrencyProvider{id: "conc"}),
		MaxActiveRequests:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	pub2, err := p.PublishExecutable(g2)
	if err != nil {
		t.Fatal(err)
	}
	if p.CurrentExecutable().EnforcementMaxActive() != 2 {
		t.Fatalf("new admissions must see max=2, got %d", p.CurrentExecutable().EnforcementMaxActive())
	}
	if held.EnforcementMaxActive() != 5 {
		t.Fatalf("in-flight generation mutated: max=%d", held.EnforcementMaxActive())
	}
	if pub2.Version == pub1.Version {
		t.Fatal("generation evidence must change with generation")
	}
}

func TestPhase51_FailedRefreshPreservesPriorExecutable(t *testing.T) {
	t.Parallel()
	p := snapshotgen.NewPublisher()
	g1, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID: "static", Version: "g1", State: economics.SnapshotReady,
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: stubRequestProvider{id: "quota"}.Describe(),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   stubRequestProvider{id: "quota"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.PublishExecutable(g1); err != nil {
		t.Fatal(err)
	}
	prior := p.CurrentExecutable()
	bad := &snapshotgen.ExecutableGeneration{Version: "bad", State: economics.SnapshotReady}
	got, err := p.PublishExecutable(bad)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if got != prior || p.CurrentExecutable().Version != "g1" {
		t.Fatalf("prior executable must remain active: got=%+v", p.CurrentExecutable())
	}
}

func TestPhase51_SettlementEvidenceNamesEvaluatorObject(t *testing.T) {
	t.Parallel()
	g, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID: "static", Version: "g1", EffectiveAt: time.Unix(1, 0).UTC(),
		State: economics.SnapshotReady,
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: stubRequestProvider{id: "quota"}.Describe(),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   stubRequestProvider{id: "quota"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.EvidenceObjectID() != "quota" {
		t.Fatalf("evidence=%q want request authority object id", g.EvidenceObjectID())
	}
	if g.Rating.Version != "" && g.EvidenceObjectID() == g.Rating.Version {
		t.Fatal("evidence must not be metadata-only rating catalog version")
	}
}

func TestPhase51_PendingProviderBlocksRemoval(t *testing.T) {
	t.Parallel()
	g, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID: "static", Version: "g1", State: economics.SnapshotReady,
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: stubRequestProvider{id: "quota-old"}.Describe(),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   stubRequestProvider{id: "quota-old"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !g.CanRemoveProvider("quota-old") {
		t.Fatal("removable before pending/live refs")
	}
	g.AddPendingProvider("quota-old")
	if g.CanRemoveProvider("quota-old") {
		t.Fatal("pending work must block provider removal")
	}
	g.ClearPendingProvider("quota-old")
	if !g.CanRemoveProvider("quota-old") {
		t.Fatal("expected removable after drain")
	}
}

func TestPhase51_IncompatibleReplacementUsesNewProviderID(t *testing.T) {
	t.Parallel()
	old, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID: "static", Version: "g1", State: economics.SnapshotReady,
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: stubRequestProvider{id: "quota-v1"}.Describe(),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   stubRequestProvider{id: "quota-v1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	old.AddPendingProvider("quota-v1")
	next, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID: "static", Version: "g2", State: economics.SnapshotReady,
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: stubRequestProvider{id: "quota-v2"}.Describe(),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   stubRequestProvider{id: "quota-v2"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.RequestRegistrations[0].Descriptor.ID == "quota-v1" {
		t.Fatal("incompatible replacement must use a new provider id")
	}
	if old.CanRemoveProvider("quota-v1") {
		t.Fatal("old id must remain while pending work references it")
	}
}
