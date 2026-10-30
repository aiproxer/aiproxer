package runtimebundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestPhase45_ProcessDueUpdatesPromGaugesAndTransitionCounters(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 10, 10, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "phase45-prom-proc",
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
		TerminalWorkOwnerID:      "prom-worker",
		TerminalWorkTickInterval: time.Hour,
		TerminalWorkClaimLimit:   10,
	}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	if err := built.Executor().TerminalWork.AcceptSettleFailure(context.Background(), terminalworkapp.SettleFailureInput{
		RequestID:  "req-prom-proc",
		AttemptID:  "a-1",
		ProviderID: "quota",
		Handles:    []string{"h1"},
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "quota"},
	}); err != nil {
		t.Fatal(err)
	}
	before := gatherTerminalWork(t, built)
	if err := runtimebundle.CandidateTerminalWorkProcessor(built).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	after := gatherTerminalWork(t, built)
	if after.transitionsTotal <= before.transitionsTotal {
		t.Fatalf("transitions_total before=%v after=%v want increase", before.transitionsTotal, after.transitionsTotal)
	}
	if after.completed < 1 {
		t.Fatalf("completed gauge=%v want >=1 after ProcessDue", after.completed)
	}
	done, err := store.List(context.Background(), workstore.Query{
		RequestID: "req-prom-proc",
		State:     sdk.WorkStateCompleted,
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(done.Records) == 0 {
		t.Fatal("expected completed work")
	}
	if after.backlog > before.backlog {
		t.Fatalf("backlog rose after complete: before=%v after=%v", before.backlog, after.backlog)
	}
}

type twGather struct {
	backlog          float64
	completed        float64
	transitionsTotal float64
}

func gatherTerminalWork(t *testing.T, built *runtimebundle.CandidateHTTPCompile) twGather {
	t.Helper()
	families, err := built.Metrics().Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var out twGather
	for _, f := range families {
		switch f.GetName() {
		case "aip_terminal_work_backlog":
			if len(f.Metric) > 0 && f.Metric[0].Gauge != nil {
				out.backlog = f.Metric[0].Gauge.GetValue()
			}
		case "aip_terminal_work_completed":
			if len(f.Metric) > 0 && f.Metric[0].Gauge != nil {
				out.completed = f.Metric[0].Gauge.GetValue()
			}
		case "aip_terminal_work_transitions_total":
			for _, m := range f.Metric {
				if m.Counter != nil {
					out.transitionsTotal += m.Counter.GetValue()
				}
			}
		}
	}
	return out
}
