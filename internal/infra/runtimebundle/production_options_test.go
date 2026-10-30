package runtimebundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

type prodMeter struct{}

func (prodMeter) Append(context.Context, metering.Fact) error { return nil }

type prodRuleSource struct {
	ver string
}

func (p prodRuleSource) Snapshot(context.Context) (economics.Snapshot[economics.PolicyRulesView], error) {
	return economics.Snapshot[economics.PolicyRulesView]{
		ID: "usage_authority", Version: p.ver, State: economics.SnapshotReady,
		Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
	}, nil
}

type prodAllowRequest struct{}

func (prodAllowRequest) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (prodAllowRequest) SettleRequest(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (prodAllowRequest) ReleaseRequest(context.Context, authority.RequestRelease) error { return nil }

func TestBuild_ProductionOptionsOutsideTesting(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Production = runtimebundle.ProductionOptions{
		MeteringRecorder: prodMeter{},
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "prod-request",
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
		UsageSnapshotSource: prodRuleSource{ver: "prod-snap-v1"},
		EvidenceSink:        prodEvidence{},
		MeteringQuerier:     prodQuerier{},
	}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	if built.Executor() == nil || built.Executor().MeteringRecorder == nil {
		t.Fatal("production metering must attach on executor")
	}
	if runtimebundle.CandidateMeteringQuerier(built) == nil {
		t.Fatal("production metering querier must mount on Built")
	}
	if built.Executor().RequestCoordinator == nil || len(built.Executor().RequestCoordinator.Slots) == 0 {
		t.Fatal("production request provider must create coordinator slots")
	}
	cur := runtimebundle.CandidateSnapshotGeneration(built).Current()
	if cur == nil || cur.Usage.Version != "prod-snap-v1" {
		t.Fatalf("generation usage=%+v", cur)
	}
	_ = time.Now()
}

type prodEvidence struct{}

func (prodEvidence) RecordPolicyDecision(context.Context, policydecision.Record) error { return nil }

func (prodEvidence) RecordAccountingAuthority(context.Context, controlplane.Event) error {
	return nil
}

type prodQuerier struct{}

func (prodQuerier) List(context.Context, metering.Query) (metering.Page, error) {
	return metering.Page{}, nil
}
