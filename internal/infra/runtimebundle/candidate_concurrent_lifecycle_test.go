package runtimebundle_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
)

// TestCandidateRuntime_ConcurrentQuiesceClose_NilAndLedgerBound proves the
// supported CandidateRuntime owner contract after Task 4.2 removed the
// aggregate closer fallback: nil/zero-value concurrent Quiesce/Close is
// race-safe, and ledger-bound concurrent Quiesce/Close runs each phase
// exactly once. ProcessServices survival across candidate teardown is covered
// by candidate compile/rollback tests (e.g. candidacy compile + cursor SDK
// close/rollback), not by inventing a local unused closer here.
func TestCandidateRuntime_ConcurrentQuiesceClose_NilAndLedgerBound(t *testing.T) {
	t.Parallel()

	t.Run("nil_and_zero_value_race_safe", func(t *testing.T) {
		t.Parallel()
		const goroutines = 64
		for range 8 {
			var nilCand *runtimebundle.CandidateHTTPCompile
			zero := &runtimebundle.CandidateHTTPCompile{}
			var wg sync.WaitGroup
			wg.Add(goroutines * 4)
			for range goroutines {
				go func() {
					defer wg.Done()
					_ = nilCand.Quiesce(context.Background())
				}()
				go func() {
					defer wg.Done()
					_ = nilCand.Close()
				}()
				go func() {
					defer wg.Done()
					_ = zero.Quiesce(context.Background())
				}()
				go func() {
					defer wg.Done()
					_ = zero.Close()
				}()
			}
			wg.Wait()
			if err := zero.Quiesce(context.Background()); err != nil {
				t.Fatalf("repeated zero Quiesce: %v", err)
			}
			if err := zero.Close(); err != nil {
				t.Fatalf("repeated zero Close: %v", err)
			}
		}
	})

	t.Run("ledger_bound_exactly_once", func(t *testing.T) {
		t.Parallel()
		var quiesced, closed atomic.Int32
		ledger := runtimebundle.NewResourceLedger()
		ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error {
			quiesced.Add(1)
			return nil
		})
		ledger.AddClose("be", runtimebundle.PhaseClose, func() error {
			closed.Add(1)
			return nil
		})
		cand := runtimebundle.NewCandidateRuntimeForTest(ledger)

		var wg sync.WaitGroup
		const n = 32
		wg.Add(n * 2)
		errs := make(chan error, n*2)
		for range n {
			go func() {
				defer wg.Done()
				errs <- cand.Quiesce(context.Background())
			}()
			go func() {
				defer wg.Done()
				errs <- cand.Close()
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent lifecycle error: %v", err)
			}
		}
		if quiesced.Load() != 1 {
			t.Fatalf("quiesce ran %d times, want exactly 1", quiesced.Load())
		}
		if closed.Load() != 1 {
			t.Fatalf("close phase ran %d times, want exactly 1", closed.Load())
		}

		// Repeated calls return stable outcomes without re-running ledger work.
		if err := cand.Quiesce(context.Background()); err != nil {
			t.Fatalf("repeated Quiesce: %v", err)
		}
		if err := cand.Close(); err != nil {
			t.Fatalf("repeated Close: %v", err)
		}
		if quiesced.Load() != 1 || closed.Load() != 1 {
			t.Fatalf("idempotent after concurrent stress: quiesced=%d closed=%d", quiesced.Load(), closed.Load())
		}
	})
}
