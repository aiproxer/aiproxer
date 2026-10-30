package runtimebundle_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func phase5ConcCfg(maxConcurrent int, version string) *config.Config {
	cfg := baseAuthorityConfig(false, "fail_closed")
	cfg.Accounting.Concurrency.Enabled = true
	cfg.Accounting.Concurrency.SnapshotVersion = version
	cfg.Accounting.Concurrency.Rules = []config.ConcurrencyAuthorityRuleConfig{{
		ID: "max-active", MaxActiveRequests: maxConcurrent,
		Match: config.AccountingAuthorityDimensionsConfig{
			Principal: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("alice")},
		},
	}}
	return cfg
}

func TestPhase5Remediation_BuildFiveToTwoAndBoundEvidence(t *testing.T) {
	t.Parallel()
	cfg := phase5ConcCfg(5, "conc-five")
	opts := baseAuthorityOptions(t)
	opts.Production.RequestRegistrations = []authority.RequestRegistration{{
		Descriptor: phase53Req{id: "quota-v1"}.Describe(),
		Priority:   authority.RequestPriorityQuotaBudgetRate,
		Provider:   phase53Req{id: "quota-v1"},
	}}
	opts.Production.ConcurrencyRegistration = &authority.ConcurrencyRegistration{
		Descriptor: authority.ProviderDescriptor{
			ID: "conc", Kind: authority.ProviderKindAuthority,
			Postures: []authority.StagePosture{{
				Stage: authority.StageLeaseAdmit, Strength: authority.StrengthRequired,
				FailureBehavior: authority.FailureFailClosed,
			}},
		},
		Provider: phase53Conc{id: "conc"},
	}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	g1 := runtimebundle.CandidateSnapshotGeneration(built).CurrentExecutable()
	if g1 == nil || g1.RequestCoord == nil || g1.EnforcementMaxActive() != 5 {
		t.Fatalf("build executable must enforce max=5 via live coordinator: %+v", g1)
	}
	for i := range 5 {
		if _, err := g1.RequestCoord.Admit(context.Background(), phase5E2EAdmit(fmt.Sprintf("e2e-old-%d", i))); err != nil {
			t.Fatalf("admit %d under five: %v", i, err)
		}
	}
	if _, err := g1.RequestCoord.Admit(context.Background(), phase5E2EAdmit("e2e-old-overflow")); err == nil {
		t.Fatal("five-slot generation must deny sixth admit")
	}

	cfgTwo := phase5ConcCfg(2, "conc-two")
	prodTwo := opts.Production
	g1.Retain()
	g2, err := runtimebundle.PublishExecutableFromProduction(runtimebundle.CandidateSnapshotGeneration(built), cfgTwo, prodTwo, time.Unix(20, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if g2.EnforcementMaxActive() != 2 {
		t.Fatalf("refresh must change admit limit: %+v", g2)
	}
	cur := runtimebundle.CandidateSnapshotGeneration(built).CurrentExecutable()
	for i := range 2 {
		if _, err := cur.RequestCoord.Admit(context.Background(), phase5E2EAdmit(fmt.Sprintf("e2e-new-%d", i))); err != nil {
			t.Fatalf("admit %d under two: %v", i, err)
		}
	}
	if _, err := cur.RequestCoord.Admit(context.Background(), phase5E2EAdmit("e2e-new-overflow")); err == nil {
		t.Fatal("two-slot generation must deny third new admit")
	}
	if g1.EnforcementMaxActive() != 5 {
		t.Fatalf("in-flight generation mutated: max=%d", g1.EnforcementMaxActive())
	}

	bad := phase5ConcCfg(2, "conc-bad")
	badProd := prodTwo
	badProd.RequestRegistrations = nil
	badProd.ConcurrencyRegistration = nil
	prior := runtimebundle.CandidateSnapshotGeneration(built).CurrentExecutable()
	got, pubErr := runtimebundle.PublishExecutableFromProduction(runtimebundle.CandidateSnapshotGeneration(built), bad, badProd, time.Unix(30, 0).UTC())
	if pubErr == nil {
		t.Fatal("empty contribution refresh must fail")
	}
	if got != prior || runtimebundle.CandidateSnapshotGeneration(built).CurrentExecutable().Version != prior.Version {
		t.Fatal("failed refresh must preserve prior executable")
	}
}

func TestPhase5Remediation_PendingClearAndCanRemoveProvider(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "phase5-pending-drain",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := phase5ConcCfg(2, "conc-pending")
	opts := baseAuthorityOptions(t)
	opts.Production.TerminalWorkStore = store
	opts.Production.TerminalWorkOwnerID = "owner-1"
	opts.Production.RequestRegistrations = []authority.RequestRegistration{{
		Descriptor: phase53Req{id: "quota-old"}.Describe(),
		Priority:   authority.RequestPriorityQuotaBudgetRate,
		Provider:   phase53Req{id: "quota-old"},
	}}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	if runtimebundle.CandidateTerminalWorkProcessor(built) == nil {
		t.Fatal("expected terminal work processor wired by Build")
	}
	gen := runtimebundle.CandidateSnapshotGeneration(built).CurrentExecutable()
	if gen == nil {
		t.Fatal("expected executable")
	}
	gen.Retain()
	gen.AddPendingProvider("quota-old")
	if runtimebundle.CandidateSnapshotGeneration(built).CanRemoveProvider("quota-old") {
		t.Fatal("pending ref must block provider removal")
	}
	nextProd := opts.Production
	nextProd.RequestRegistrations = []authority.RequestRegistration{{
		Descriptor: phase53Req{id: "quota-new"}.Describe(),
		Priority:   authority.RequestPriorityQuotaBudgetRate,
		Provider:   phase53Req{id: "quota-new"},
	}}
	if _, err := runtimebundle.PublishExecutableFromProduction(runtimebundle.CandidateSnapshotGeneration(built), cfg, nextProd, clock); err == nil {
		t.Fatal("publish that drops pending provider must fail CanRemoveProvider validation")
	}
	if runtimebundle.CandidateSnapshotGeneration(built).CurrentExecutable().RequestRegistrations[0].Descriptor.ID != "quota-old" {
		t.Fatal("blocked removal must keep prior executable")
	}

	// Drain pending via the same Publisher.ClearPendingProvider path Build wires
	// from Processor.OnTerminalDone after Complete/Quarantine.
	runtimebundle.CandidateSnapshotGeneration(built).ClearPendingProvider(gen.ID, "quota-old")
	gen.Release()
	if !runtimebundle.CandidateSnapshotGeneration(built).CanRemoveProvider("quota-old") {
		t.Fatal("after pending drain and live release, provider must be removable")
	}
	if _, err := runtimebundle.PublishExecutableFromProduction(runtimebundle.CandidateSnapshotGeneration(built), cfg, nextProd, clock.Add(time.Second)); err != nil {
		t.Fatalf("publish after drain: %v", err)
	}
	if runtimebundle.CandidateSnapshotGeneration(built).CurrentExecutable().RequestRegistrations[0].Descriptor.ID != "quota-new" {
		t.Fatal("incompatible replacement must publish under new provider id")
	}
}

func phase5E2EAdmit(requestID string) authority.RequestAdmission {
	return authority.RequestAdmission{
		RequestID:   requestID,
		ALegID:      "a-1",
		Perspective: metering.PerspectiveCustomer,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveCustomer,
			Boundary:    metering.BoundaryFrontendIngress,
			Lifecycle:   metering.LifecycleLogicalRequest,
		},
	}
}

type phase53Conc struct{ id string }

func (s phase53Conc) Describe() authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID: s.id, Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage: authority.StageLeaseAdmit, Strength: authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func (s phase53Conc) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 1}, nil
}

func (s phase53Conc) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 2}, nil
}
func (s phase53Conc) ReleaseLease(context.Context, authority.LeaseRelease) error { return nil }
func (s phase53Conc) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}
