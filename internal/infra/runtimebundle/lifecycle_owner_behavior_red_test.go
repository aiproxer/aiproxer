package runtimebundle_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
)

// Task 7.1 executable lifecycle behavior targets for the canonical owner graph.
// Success-path at-most-once cases may already pass; retryable close through the
// real GenerationBundle/ResourceLedger path is the intentional RED (req 8.8).

func TestLifecycleOwner_UnpublishedRollbackCleansEachResourceOnce(t *testing.T) {
	t.Parallel()
	var a, b, c atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("a", runtimebundle.PhaseClose, func() error { a.Add(1); return nil })
	ledger.AddClose("b", runtimebundle.PhaseQuiesce, func() error { b.Add(1); return nil })
	ledger.AddClose("c", runtimebundle.PhasePrepare, func() error { c.Add(1); return nil })
	cand := runtimebundle.NewCandidateRuntimeForTest(ledger)

	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}
	if a.Load() != 1 || b.Load() != 1 || c.Load() != 1 {
		t.Fatalf("rollback cleans a=%d b=%d c=%d want 1 each", a.Load(), b.Load(), c.Load())
	}
	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}
	if a.Load() != 1 || b.Load() != 1 || c.Load() != 1 {
		t.Fatalf("second Close double-cleaned a=%d b=%d c=%d", a.Load(), b.Load(), c.Load())
	}
}

func TestLifecycleOwner_QuiesceCloseAtMostOnceUnderConcurrentCalls(t *testing.T) {
	t.Parallel()
	var quiesced, closed atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error {
		quiesced.Add(1)
		return nil
	})
	ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		closed.Add(1)
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_ = bundle.Quiesce(context.Background())
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = bundle.Close()
		}()
	}
	close(start)
	wg.Wait()

	if quiesced.Load() != 1 {
		t.Fatalf("quiesce executions=%d want 1", quiesced.Load())
	}
	if closed.Load() != 1 {
		t.Fatalf("close executions=%d want 1", closed.Load())
	}
}

func TestLifecycleOwner_ProcessOwnedRemainOpenAcrossGenerationClose(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	ledger := runtimebundle.NewResourceLedger()
	var closed atomic.Int32
	ledger.AddClose("gen", runtimebundle.PhaseClose, func() error {
		closed.Add(1)
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)
	if err := bundle.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 1 {
		t.Fatalf("generation close=%d", closed.Load())
	}
	if ps.Closed() {
		t.Fatal("process-owned resources must remain open after generation Close")
	}
}

func TestLifecycleOwner_Close_RetryableFailureMustReexecuteCleanup(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("flaky", runtimebundle.PhaseClose, func() error {
		if calls.Add(1) == 1 {
			return errors.New("temp-close")
		}
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	if err := bundle.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	err1 := bundle.Close()
	if err1 == nil {
		t.Fatal("first Close must fail")
	}
	err2 := bundle.Close()
	if err2 != nil {
		t.Fatalf("retry Close must succeed after transient failure, got %v (calls=%d)", err2, calls.Load())
	}
	if calls.Load() != 2 {
		t.Fatalf("cleanup executions=%d want 2; duplicate once/error cache defeated retry", calls.Load())
	}
}

// TestLifecycleOwner_DirectBundleClose_PanicContract documents the current
// isolation boundary: ResourceLedger.safeLedgerStop converts cleanup panics to
// errors before GenerationBundle returns. Final target still requires retryable
// reclaim (no permanent claim). Recover here so an escaping panic cannot obscure
// the rest of the RED suite; prefer Generation→Manager.RetireGeneration coverage
// for the retireGeneration safeClose boundary (task 7.3).
func TestLifecycleOwner_DirectBundleClose_PanicContract(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("boom", runtimebundle.PhaseClose, func() error {
		n := calls.Add(1)
		if n == 1 {
			panic("cleanup boom")
		}
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	var err1 error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("panic escaped GenerationBundle.Close before ledger isolation boundary: %v", recovered)
			}
		}()
		err1 = bundle.Close()
	}()
	if err1 == nil {
		t.Fatal("first Close must surface isolated panic as error (ledger boundary)")
	}
	err2 := bundle.Close()
	if err2 != nil {
		t.Fatalf("retry Close after panic isolation: %v (calls=%d); final owner must not permanently claim cleanup", err2, calls.Load())
	}
	if calls.Load() != 2 {
		t.Fatalf("cleanup claims=%d want 2 (fail-then-retry); cached wrapper made retry a no-op", calls.Load())
	}
}
