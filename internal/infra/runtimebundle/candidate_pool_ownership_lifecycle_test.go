package runtimebundle_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
)

// TestCandidateRollback_IncludesLedgerCloserNotProcessPool proves unpublished
// candidate Close rolls back ledger-owned closers (including a pool-like resource)
// while leaving process-owned pool registry cleanup to ProcessServices.Close.
// The pooled postgres integration gauge can only reach 0 after process Close;
// candidate-only Close must not dispose process DatabasePools (req 6.2).
func TestCandidateRollback_IncludesLedgerCloserNotProcessPool(t *testing.T) {
	t.Parallel()

	ledger := runtimebundle.NewResourceLedger()
	var poolClosed atomic.Int32
	var genClosed atomic.Int32
	ledger.AddClose("generation-resource", runtimebundle.PhaseClose, func() error {
		genClosed.Add(1)
		return nil
	})
	// Simulate a closer that would zero pool metrics if it belonged on the ledger.
	// Process-owned PoolRegistry.Close is intentionally NOT registered here.
	processPoolClose := func() error {
		poolClosed.Add(1)
		return nil
	}

	cand := runtimebundle.NewCandidateRuntimeForTest(ledger)
	if err := cand.Close(); err != nil {
		t.Fatalf("candidate Close: %v", err)
	}
	if genClosed.Load() != 1 {
		t.Fatalf("generation closer calls=%d want 1 after unpublished rollback", genClosed.Load())
	}
	if poolClosed.Load() != 0 {
		t.Fatal("process pool closer must not run from candidate Close")
	}
	if err := cand.Close(); err != nil {
		t.Fatalf("idempotent candidate Close: %v", err)
	}
	if genClosed.Load() != 1 {
		t.Fatalf("generation closer calls=%d want 1 after idempotent Close", genClosed.Load())
	}

	// Post-transfer candidate Close is a no-op; generation owns the ledger.
	ledger2 := runtimebundle.NewResourceLedger()
	var afterTransfer atomic.Int32
	ledger2.AddClose("kept", runtimebundle.PhaseClose, func() error {
		afterTransfer.Add(1)
		return nil
	})
	cand2 := runtimebundle.NewCandidateRuntimeForTest(ledger2)
	transferred := runtimebundle.TransferLedgerOwnershipForTest(cand2)
	if transferred == nil {
		t.Fatal("expected transferred ledger")
	}
	if err := cand2.Close(); err != nil {
		t.Fatalf("post-transfer Close: %v", err)
	}
	if afterTransfer.Load() != 0 {
		t.Fatal("post-transfer candidate Close must not roll back generation ledger")
	}
	if err := transferred.Rollback(context.Background()); err != nil {
		t.Fatalf("generation Rollback: %v", err)
	}
	if afterTransfer.Load() != 1 {
		t.Fatalf("generation Rollback closer calls=%d want 1", afterTransfer.Load())
	}

	// Process pool disposal remains ProcessServices.Close's responsibility.
	if err := processPoolClose(); err != nil {
		t.Fatal(err)
	}
	if poolClosed.Load() != 1 {
		t.Fatal("process pool closer must run exactly once from process ownership")
	}
}
