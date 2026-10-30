package runtimebundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/runtimegen"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type phase53Req struct{ id string }

func (s phase53Req) Describe() authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID: s.id, Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func (s phase53Req) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (s phase53Req) SettleRequest(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
	return authority.Settlement{}, nil
}
func (s phase53Req) ReleaseRequest(context.Context, authority.RequestRelease) error { return nil }

func TestPhase53_CompileStaticContributionAndPublishExecutable(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Accounting.Concurrency.Enabled = true
	cfg.Accounting.Concurrency.SnapshotVersion = "conc-v2"
	cfg.Accounting.Concurrency.Rules = []config.ConcurrencyAuthorityRuleConfig{{
		ID: "max-active", MaxActiveRequests: 2,
		Match: config.AccountingAuthorityDimensionsConfig{
			Principal: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("alice")},
		},
	}}
	prod := runtimebundle.ProductionOptions{
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: phase53Req{id: "quota"}.Describe(),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   phase53Req{id: "quota"},
		}},
	}
	contrib := runtimebundle.CompileGenerationContribution(cfg, prod, time.Unix(10, 0).UTC())
	if err := contrib.Validate(); err != nil {
		t.Fatal(err)
	}
	if contrib.MaxActiveRequests != 2 || contrib.SourceID != "static-config" {
		t.Fatalf("contrib=%+v", contrib)
	}
	pub := snapshotgen.NewPublisher()
	gen, err := runtimebundle.PublishExecutableFromProduction(pub, cfg, prod, time.Unix(10, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if gen.EnforcementMaxActive() != 2 || gen.EvidenceObjectID() != "quota" {
		t.Fatalf("gen=%+v", gen)
	}
	_ = runtimegen.StaticSource{Value: contrib}
}

func TestPhase53_BuildPublishesExecutableWhenRegistrationsPresent(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Production.RequestRegistrations = []authority.RequestRegistration{{
		Descriptor: phase53Req{id: "quota"}.Describe(),
		Priority:   authority.RequestPriorityQuotaBudgetRate,
		Provider:   phase53Req{id: "quota"},
	}}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	exec := runtimebundle.CandidateSnapshotGeneration(built).CurrentExecutable()
	if exec == nil {
		t.Fatal("expected executable generation after CompileCandidate")
	}
	if exec.EvidenceObjectID() != "quota" {
		t.Fatalf("evidence=%q", exec.EvidenceObjectID())
	}
}
