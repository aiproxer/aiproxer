package runtimebundle

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCandidateRuntime_Close_PreTransferRollback_PostTransferNoop(t *testing.T) {
	t.Parallel()
	var order []string
	ledger := NewResourceLedger()
	ledger.AddClose("second", PhaseClose, func() error {
		order = append(order, "second")
		return nil
	})
	ledger.AddClose("first", PhaseClose, func() error {
		order = append(order, "first")
		return nil
	})
	cand := &candidateAssembly{ledger: ledger}
	if err := cand.Close(); err != nil {
		t.Fatalf("pre-transfer Close: %v", err)
	}
	ledger.mu.Lock()
	st := ledger.state
	ledger.mu.Unlock()
	if st != ledgerLifeRolledBack {
		t.Fatal("pre-transfer Close must invoke ResourceLedger.Rollback")
	}
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("rollback order=%v want reverse acquisition [first second]", order)
	}

	ledger2 := NewResourceLedger()
	var closed atomic.Int32
	ledger2.AddClose("kept", PhaseClose, func() error {
		closed.Add(1)
		return nil
	})
	cand2 := &candidateAssembly{ledger: ledger2}
	if cand2.transferLedgerOwnership() == nil {
		t.Fatal("expected ledger transfer")
	}
	if err := cand2.Close(); err != nil {
		t.Fatalf("post-transfer Close: %v", err)
	}
	if err := cand2.RollbackUnpublished(); err != nil {
		t.Fatalf("post-transfer RollbackUnpublished: %v", err)
	}
	if closed.Load() != 0 {
		t.Fatal("post-transfer candidate Close must be a no-op")
	}
	ledger2.mu.Lock()
	st2 := ledger2.state
	ledger2.mu.Unlock()
	if st2 == ledgerLifeRolledBack || st2 == ledgerLifeClosed {
		t.Fatal("post-transfer candidate must not terminate transferred ledger")
	}
}

func TestCandidate_NoOrphanLedger_AfterRollbackUnpublished(t *testing.T) {
	t.Parallel()
	ledger := NewResourceLedger()
	var closed atomic.Int32
	ledger.AddClose("res", PhaseClose, func() error {
		closed.Add(1)
		return nil
	})
	cand := &candidateAssembly{ledger: ledger}
	if err := cand.RollbackUnpublished(); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 1 {
		t.Fatalf("closed=%d", closed.Load())
	}
	if cand.transferLedgerOwnership() != nil {
		t.Fatal("rollback must claim against later transfer (no orphan publish)")
	}
	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 1 {
		t.Fatalf("idempotent close closed=%d", closed.Load())
	}
}

func TestLedger_Rollback_JoinsCleanupErrors_ReverseOrder(t *testing.T) {
	t.Parallel()
	var order []string
	ledger := NewResourceLedger()
	ledger.AddClose("b", PhaseClose, func() error {
		order = append(order, "b")
		return errors.New("cleanup-b")
	})
	ledger.AddClose("a", PhaseClose, func() error {
		order = append(order, "a")
		return errors.New("cleanup-a")
	})
	err := ledger.Rollback(context.Background())
	if err == nil {
		t.Fatal("expected joined cleanup errors")
	}
	if !strings.Contains(err.Error(), "cleanup-a") || !strings.Contains(err.Error(), "cleanup-b") {
		t.Fatalf("joined err=%v", err)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("order=%v", order)
	}
	ledger.mu.Lock()
	st := ledger.state
	ledger.mu.Unlock()
	if st != ledgerLifeRolledBack {
		t.Fatal("expected rollback terminal state")
	}
}
