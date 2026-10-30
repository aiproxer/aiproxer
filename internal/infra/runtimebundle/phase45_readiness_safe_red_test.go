package runtimebundle_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

type secretStoreError struct {
	*workstore.MemoryStore
}

func (s secretStoreError) CheckReadiness(context.Context) error {
	return errors.New("connection failed: password=SUPER_SECRET_DB_PWD host=db.internal")
}

type secretListError struct {
	*workstore.MemoryStore
}

func (s secretListError) List(context.Context, terminalwork.ListQuery) (terminalwork.ListPage, error) {
	return terminalwork.ListPage{}, errors.New("list failed: token=SUPER_SECRET_LIST_TOKEN")
}

func TestPhase45_TerminalWorkReadinessIncludesBacklogAndSafeStoreError(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 8, 30, 0, 0, time.UTC)
	mem, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "ready-safe",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	store := secretStoreError{MemoryStore: mem}
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Testing.Clock = func() time.Time { return clock }
	opts.Production = runtimebundle.ProductionOptions{
		TerminalWorkStore: store,
		TerminalWorkProviders: []terminalworkapp.EffectProvider{
			stubEffectProvider{id: "quota"},
		},
		TerminalWorkOwnerID: "ready-worker",
	}
	_, cand := mustProcessAndCandidate(t, cfg, opts)
	if err := cand.Executor().TerminalWork.AcceptSettleFailure(context.Background(), terminalworkapp.SettleFailureInput{
		RequestID:  "req-ready",
		AttemptID:  "a-1",
		ProviderID: "quota",
		Handles:    []string{"h1"},
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "quota"},
	}); err != nil {
		t.Fatal(err)
	}

	snap, err := runtimebundle.CandidateTerminalWorkMetrics(cand).Snapshot(context.Background())
	if err != nil {
		t.Fatal("BacklogKnown want true after successful snapshot")
	}
	if snap.Backlog < 1 {
		t.Fatalf("Backlog=%d want >=1 pending terminal work", snap.Backlog)
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
		if c.State != cp.CapabilityUnavailable && c.State != cp.CapabilityDegraded {
			t.Fatalf("terminal_recovery state=%q want unavailable|degraded", c.State)
		}
		if c.Reason != cp.ReasonBackingUnavailable && c.Reason != cp.ReasonPendingTerminalWork {
			t.Fatalf("Reason=%q want backing_unavailable|pending_terminal_work", c.Reason)
		}
		if strings.Contains(string(c.Reason), "SUPER_SECRET") || strings.Contains(string(c.Reason), "password=") {
			t.Fatalf("Reason leaked raw content: %q", c.Reason)
		}
	}
	if !found {
		t.Fatal("expected terminal_recovery component")
	}
}

func TestPhase45_TerminalWorkReadinessSnapshotErrorSafeAndUnknownBacklog(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 8, 35, 0, 0, time.UTC)
	mem, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "ready-list-fail",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	store := secretListError{MemoryStore: mem}
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Testing.Clock = func() time.Time { return clock }
	opts.Production = runtimebundle.ProductionOptions{
		TerminalWorkStore: store,
		TerminalWorkProviders: []terminalworkapp.EffectProvider{
			stubEffectProvider{id: "quota"},
		},
		TerminalWorkOwnerID: "ready-list-worker",
	}
	_, cand := mustProcessAndCandidate(t, cfg, opts)
	if runtimebundle.CandidateTerminalWorkProcessor(cand) == nil {
		t.Fatal("expected configured processor")
	}
	_, err = runtimebundle.CandidateTerminalWorkMetrics(cand).Snapshot(context.Background())
	if err == nil {
		t.Fatal("BacklogKnown want false when snapshot fails")
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
		if c.Reason != cp.ReasonBackingUnavailable {
			t.Fatalf("Reason=%q want %q", c.Reason, cp.ReasonBackingUnavailable)
		}
		if strings.Contains(string(c.Reason), "SUPER_SECRET") || strings.Contains(string(c.Reason), "token=") {
			t.Fatalf("Reason leaked raw content: %q", c.Reason)
		}
	}
	if !found {
		t.Fatal("expected terminal_recovery component")
	}
}
