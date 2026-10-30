package runtimebundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type stubEffectProvider struct {
	id string
}

func (p stubEffectProvider) ProviderID() string { return p.id }
func (p stubEffectProvider) SupportedKinds() []sdk.WorkKind {
	return []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}
}
func (p stubEffectProvider) Version() string { return "1" }
func (p stubEffectProvider) Invoke(context.Context, terminalwork.WorkRecord, string) error {
	return nil
}

func TestBuild_TerminalWorkOwnershipFromProductionOptions(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "tw-build"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Production = runtimebundle.ProductionOptions{
		TerminalWorkStore: store,
		TerminalWorkProviders: []terminalworkapp.EffectProvider{
			stubEffectProvider{id: "prov-a"},
		},
		TerminalWorkOwnerID:       "bundle-worker",
		TerminalWorkTickInterval:  50 * time.Millisecond,
		TerminalWorkRenewInterval: 25 * time.Millisecond,
	}
	_, cand := mustProcessAndCandidate(t, cfg, opts)
	if runtimebundle.CandidateTerminalWorkProcessor(cand) == nil {
		t.Fatal("expected TerminalWorkProcessor ownership")
	}
	if runtimebundle.CandidateTerminalWorkRegistry(cand) == nil {
		t.Fatal("expected TerminalWorkRegistry ownership")
	}
	if _, err := runtimebundle.CandidateTerminalWorkRegistry(cand).Resolve("prov-a", sdk.WorkKindSettleRequestProvider); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !runtimebundle.CandidateTerminalWorkProcessor(cand).Running() {
		t.Fatal("expected running after CompileCandidate (composition-root owns Start)")
	}
	assertTerminalRecoveryConfiguredReady(t, cand)
	if runtimebundle.CandidateTerminalWorkQueries(cand) == nil || runtimebundle.CandidateTerminalWorkMetrics(cand) == nil {
		t.Fatal("expected QueryService and MetricsObserver ownership")
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtimebundle.CandidateTerminalWorkProcessor(cand).Shutdown(shutCtx); err != nil {
		t.Fatal(err)
	}
}

func TestBuild_TerminalWorkAbsentWithoutStore(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	_, cand := mustProcessAndCandidate(t, cfg, opts)
	if runtimebundle.CandidateTerminalWorkProcessor(cand) != nil || runtimebundle.CandidateTerminalWorkRegistry(cand) != nil {
		t.Fatal("terminal work must stay nil without injected store")
	}
	assertTerminalRecoveryDisabled(t, cand)
}

func TestBuild_TerminalWorkUnresolvedProviders(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "tw-unresolved",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := terminalwork.WorkRecord{
		WorkID:         "w-ghost",
		SourceKey:      terminalwork.SourceKey{IdentityVersion: 1, Key: "sk-ghost"},
		PayloadVersion: 1,
		Kind:           sdk.WorkKindSettleRequestProvider,
		State:          sdk.WorkStateIntent,
		ProviderID:     "ghost",
		Lifecycle:      terminalwork.LifecycleCorrelation{RequestID: "r", AttemptID: "a", TraceID: "t"},
		Versions:       terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "ghost", RatingID: "r1"},
		Payload:        []byte(`{}`),
	}
	if err := store.AppendIntent(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PromotePending(context.Background(), terminalwork.PromotePendingCommand{WorkID: rec.WorkID, Now: clock}); err != nil {
		t.Fatal(err)
	}
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Production = runtimebundle.ProductionOptions{
		TerminalWorkStore:   store,
		TerminalWorkOwnerID: "bundle-worker",
	}
	opts.Testing.Clock = func() time.Time { return clock }
	_, cand := mustProcessAndCandidate(t, cfg, opts)
	if err := runtimebundle.CandidateTerminalWorkProcessor(cand).ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	ids := runtimebundle.CandidateTerminalWorkProcessor(cand).UnresolvedProviderIDs()
	if len(ids) != 1 || ids[0] != "ghost" {
		t.Fatalf("unresolved=%v", ids)
	}
	report, err := runtimebundle.CandidateReadinessReport(cand).Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range report.Components {
		if c.Component != cp.ReadinessComponentTerminalRecovery {
			continue
		}
		found = true
		if c.State != cp.CapabilityDegraded || c.Reason != cp.ReasonPendingTerminalWork {
			t.Fatalf("terminal_recovery=%+v want degraded/pending_terminal_work", c)
		}
		if len(c.ProviderIDs) != 1 || c.ProviderIDs[0] != "ghost" {
			t.Fatalf("ProviderIDs=%v", c.ProviderIDs)
		}
	}
	if !found {
		t.Fatal("expected terminal_recovery component")
	}
}

func assertTerminalRecoveryConfiguredReady(t *testing.T, cand *runtimebundle.CandidateHTTPCompile) {
	t.Helper()
	if runtimebundle.CandidateReadinessReport(cand) == nil {
		t.Fatal("expected ReadinessReport")
	}
	report, err := runtimebundle.CandidateReadinessReport(cand).Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range report.Components {
		if c.Component != cp.ReadinessComponentTerminalRecovery {
			continue
		}
		found = true
		if c.State != cp.CapabilityReady && c.State != cp.CapabilityDegraded {
			t.Fatalf("terminal_recovery state=%q want ready|degraded", c.State)
		}
		if c.Reason == cp.ReasonBackingUnavailable {
			t.Fatalf("store must be ready; got reason=%q", c.Reason)
		}
	}
	if !found {
		t.Fatal("expected terminal_recovery component")
	}
}

func assertTerminalRecoveryDisabled(t *testing.T, cand *runtimebundle.CandidateHTTPCompile) {
	t.Helper()
	if runtimebundle.CandidateReadinessReport(cand) == nil {
		t.Fatal("expected ReadinessReport")
	}
	report, err := runtimebundle.CandidateReadinessReport(cand).Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range report.Components {
		if c.Component != cp.ReadinessComponentTerminalRecovery {
			continue
		}
		if c.State != cp.CapabilityDisabled {
			t.Fatalf("terminal_recovery state=%q want disabled", c.State)
		}
		return
	}
	t.Fatal("expected terminal_recovery component")
}

// publishCandidateTerminalWorkMetrics applies MetricsObserver snapshot gauges onto
// the candidate metrics.Bundle TerminalWorkProm series (canonical observer path).
func publishCandidateTerminalWorkMetrics(t *testing.T, cand *runtimebundle.CandidateHTTPCompile) {
	t.Helper()
	if cand == nil || runtimebundle.CandidateTerminalWorkMetrics(cand) == nil || cand.Metrics() == nil || cand.Metrics().TerminalWork == nil {
		return
	}
	snap, err := runtimebundle.CandidateTerminalWorkMetrics(cand).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("TerminalWorkMetrics.Snapshot: %v", err)
	}
	cand.Metrics().TerminalWork.ApplySnapshot(metrics.TerminalWorkSnapshot{
		Backlog:      snap.Backlog,
		OldestAgeSec: snap.OldestAge.Seconds(),
		Pending:      snap.Pending,
		Retrying:     snap.Retrying,
		Quarantined:  snap.Quarantined,
		Completed:    snap.Completed,
		Claimed:      snap.Claimed,
	})
}
