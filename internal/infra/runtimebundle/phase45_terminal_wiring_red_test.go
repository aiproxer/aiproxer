package runtimebundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Composition-root wiring proof without production ForTest seams:
// injection + IntentService accept path + readiness/metrics publish.
func TestPhase45_BuildWiresTerminalWorkIntoExecutorAndReadiness(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "phase45-wire",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := baseAuthorityConfig(false, "fail_closed")
	cfg.Observability.Metrics.Enabled = true
	opts := baseAuthorityOptions(t)
	opts.Testing.Clock = func() time.Time { return clock }
	opts.Production = runtimebundle.ProductionOptions{
		TerminalWorkStore: store,
		TerminalWorkProviders: []terminalworkapp.EffectProvider{
			stubEffectProvider{id: "quota"},
		},
		TerminalWorkOwnerID: "bundle-phase45",
	}
	_, cand := mustProcessAndCandidate(t, cfg, opts)
	if cand.Executor() == nil || cand.Executor().TerminalWork == nil {
		t.Fatal("CompileCandidate must inject IntentService into Executor.AccountingRuntime.TerminalWork")
	}
	if runtimebundle.CandidateTerminalWorkProcessor(cand) == nil || !runtimebundle.CandidateTerminalWorkProcessor(cand).Running() {
		t.Fatal("CompileCandidate must own processor lifecycle (running after compile)")
	}
	if runtimebundle.CandidateTerminalWorkQueries(cand) == nil || runtimebundle.CandidateTerminalWorkMetrics(cand) == nil {
		t.Fatal("CompileCandidate must expose QueryService and MetricsObserver")
	}
	if runtimebundle.CandidateReadinessReport(cand) == nil {
		t.Fatal("expected ReadinessReport")
	}
	if cand.Metrics() == nil || cand.Metrics().TerminalWork == nil {
		t.Fatal("metrics.Bundle must wire TerminalWorkProm")
	}

	// Persist durable intent via injected IntentService (no ForTest settle API).
	if err := cand.Executor().TerminalWork.AcceptSettleFailure(context.Background(), terminalworkapp.SettleFailureInput{
		RequestID:  "req-wire",
		AttemptID:  "a-1",
		TraceID:    "tr-wire",
		ProviderID: "quota",
		Handles:    []string{"quota-h"},
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "quota"},
	}); err != nil {
		t.Fatal(err)
	}
	page, err := runtimebundle.CandidateTerminalWorkQueries(cand).List(context.Background(), terminalworkapp.WorkQuery{
		RequestID: "req-wire",
		Class:     terminalworkapp.QueryClassPendingTerminalWork,
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) == 0 {
		t.Fatal("expected durable pending work after AcceptSettleFailure")
	}
	snap, err := runtimebundle.CandidateTerminalWorkMetrics(cand).Snapshot(context.Background())
	if err != nil {
		t.Fatal("BacklogKnown want true")
	}
	if snap.Backlog < 1 {
		t.Fatalf("readiness Backlog=%d want >=1", snap.Backlog)
	}
	publishCandidateTerminalWorkMetrics(t, cand)
	families, err := cand.Metrics().Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	foundBacklog := false
	for _, f := range families {
		if f.GetName() == "aip_terminal_work_backlog" {
			foundBacklog = true
			if len(f.Metric) == 0 || f.Metric[0].Gauge == nil || f.Metric[0].Gauge.GetValue() < 1 {
				t.Fatalf("published backlog gauge not updated")
			}
		}
	}
	if !foundBacklog {
		t.Fatal("missing aip_terminal_work_backlog after publish")
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
	}
	if !found {
		t.Fatal("expected terminal_recovery component")
	}
	_ = sdk.WorkStatePending
}
