package runtimegen_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/runtimegen"
)

type stubReq struct{ id string }

func (s stubReq) Describe() authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID: s.id, Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func (s stubReq) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (s stubReq) SettleRequest(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
	return authority.Settlement{}, nil
}
func (s stubReq) ReleaseRequest(context.Context, authority.RequestRelease) error { return nil }

func TestGenerationContribution_RejectsEmptyAndMetadataOnly(t *testing.T) {
	t.Parallel()
	if err := (runtimegen.GenerationContribution{SourceID: "s", Version: "1"}).Validate(); err == nil {
		t.Fatal("empty contribution must fail")
	}
}

func TestGenerationContribution_AcceptsDescriptorBoundRegistrations(t *testing.T) {
	t.Parallel()
	c := runtimegen.GenerationContribution{
		SourceID: "static",
		Version:  "v1",
		State:    economics.SnapshotReady,
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: stubReq{id: "quota"}.Describe(),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   stubReq{id: "quota"},
		}},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	src := runtimegen.StaticSource{Value: c}
	got, err := src.Contribution(context.Background())
	if err != nil || got.Version != "v1" {
		t.Fatalf("source: %+v err=%v", got, err)
	}
}
