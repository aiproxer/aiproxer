package runtimebundle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

type phase55FailingUsageSource struct{}

func (phase55FailingUsageSource) Snapshot(context.Context) (economics.Snapshot[economics.PolicyRulesView], error) {
	return economics.Snapshot[economics.PolicyRulesView]{}, errors.New("source fetch failed")
}

func TestPhase55_ReadinessSeparatesExecutableSourceAndTerminal(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Production.RequestRegistrations = []authority.RequestRegistration{{
		Descriptor: phase53Req{id: "quota"}.Describe(),
		Priority:   authority.RequestPriorityQuotaBudgetRate,
		Provider:   phase53Req{id: "quota"},
	}}
	opts.Production.UsageSnapshotSource = phase55FailingUsageSource{}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	if runtimebundle.CandidateReadinessReport(built) == nil {
		t.Fatal("expected readiness report")
	}
	report, err := runtimebundle.CandidateReadinessReport(built).Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.ExecutableGeneration.EvidenceObjectID != "quota" {
		t.Fatalf("executable evidence=%q", report.ExecutableGeneration.EvidenceObjectID)
	}
	if report.ExecutableGeneration.State != cp.CapabilityReady {
		t.Fatalf("executable state=%q want ready after source fetch failure", report.ExecutableGeneration.State)
	}
	byID := map[cp.ReadinessComponentID]cp.ReadinessComponentStatus{}
	for _, c := range report.Components {
		byID[c.Component] = c
	}
	if _, ok := byID[cp.ReadinessComponentExecutableGeneration]; !ok {
		t.Fatal("missing executable_generation component")
	}
	if byID[cp.ReadinessComponentUsageSnapshot].State != cp.CapabilityUnavailable &&
		byID[cp.ReadinessComponentUsageSnapshot].State != cp.CapabilityDegraded {
		t.Fatalf("usage source fetch state=%q", byID[cp.ReadinessComponentUsageSnapshot].State)
	}
	if byID[cp.ReadinessComponentExecutableGeneration].EvidenceObjectID == "quota" &&
		byID[cp.ReadinessComponentUsageSnapshot].State == cp.CapabilityReady {
		t.Fatal("source fetch must not report ready when injectable usage source failed")
	}
}

func TestPhase55_RefreshKeepsPriorExecutableOnSourceFailure(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Production.RequestRegistrations = []authority.RequestRegistration{{
		Descriptor: phase53Req{id: "quota"}.Describe(),
		Priority:   authority.RequestPriorityQuotaBudgetRate,
		Provider:   phase53Req{id: "quota"},
	}}
	src := &mutablePhase55UsageSource{ver: "u1"}
	opts.Production.UsageSnapshotSource = src
	_, built := mustProcessAndCandidate(t, cfg, opts)
	before := runtimebundle.CandidateSnapshotGeneration(built).CurrentExecutable()
	if before == nil || before.EvidenceObjectID() != "quota" {
		t.Fatalf("before=%v", before)
	}
	src.fail = true
	if runtimebundle.CandidateSnapshotController(built) == nil {
		t.Fatal("expected snapshot controller")
	}
	_ = runtimebundle.CandidateSnapshotController(built).Refresh(context.Background())
	after := runtimebundle.CandidateSnapshotGeneration(built).CurrentExecutable()
	if after == nil || after.ID != before.ID || after.EvidenceObjectID() != "quota" {
		t.Fatalf("prior executable must remain; before=%v after=%v", before, after)
	}
	cur := runtimebundle.CandidateSnapshotGeneration(built).Current()
	if cur == nil || (cur.Usage.State != economics.SnapshotUnavailable && cur.Usage.State != economics.SnapshotDegraded) {
		t.Fatalf("metadata source-fetch posture=%#v", cur)
	}
}

type mutablePhase55UsageSource struct {
	ver  string
	fail bool
}

func (m *mutablePhase55UsageSource) Snapshot(context.Context) (economics.Snapshot[economics.PolicyRulesView], error) {
	if m.fail {
		return economics.Snapshot[economics.PolicyRulesView]{}, errors.New("refresh failed")
	}
	now := time.Unix(400, 0).UTC()
	return economics.Snapshot[economics.PolicyRulesView]{
		ID: "usage_authority", Version: m.ver, EffectiveAt: now, FetchedAt: now,
		State: economics.SnapshotReady,
		Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
	}, nil
}
