package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
)

// TestFlushEconomicCheckpointsAtTerminal_ClassifiesBoundedBudget pins the
// terminal flush budget contract: exceeding economicCheckpointFlushTimeout must
// surface as ErrEconomicCheckpointFlushTimeout while preserving the underlying
// store cause. A loaded host or a race-instrumented run can legitimately exhaust
// the budget, and that is the bound working as designed rather than a defect.
//
// The budget is not raised to make this pass; it is classified so callers and
// tests can tell "the bound held" apart from "the flush was broken".
func TestFlushEconomicCheckpointsAtTerminal_ClassifiesBoundedBudget(t *testing.T) {
	t.Parallel()

	t.Run("deadline_exceeded_is_classified_and_preserves_cause", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()

		// The flush detaches from caller cancellation, so an already-cancelled
		// caller context must not be reported as a budget timeout.
		cancelledCtx, cancelCaller := context.WithCancel(context.Background())
		cancelCaller()

		if !isEconomicCheckpointFlushTimeout(ctx, errors.New("boom")) {
			t.Fatal("expired flush budget must classify as a bounded flush timeout")
		}
		if !isEconomicCheckpointFlushTimeout(ctx, errors.Join(journalstore.ErrSQLiteRetryCanceled, context.DeadlineExceeded)) {
			t.Fatal("store retry cancellation under an expired budget must classify")
		}
		if isEconomicCheckpointFlushTimeout(context.Background(), errors.New("boom")) {
			t.Fatal("live flush budget must not classify an unrelated error")
		}
		if isEconomicCheckpointFlushTimeout(cancelledCtx, errors.New("boom")) {
			t.Fatal("caller cancellation must not be misreported as a budget timeout")
		}
		if isEconomicCheckpointFlushTimeout(ctx, nil) {
			t.Fatal("nil error must not classify as a flush timeout")
		}
	})
}

// TestFlushEconomicCheckpointsAtTerminal_SentinelStaysDistinctFromStoreCauses
// pins the discriminator callers use to tell "the bound held" apart from "the
// flush was broken": matching errors.Is(err, ErrEconomicCheckpointFlushTimeout)
// must never succeed for the store's own retry-cancel sentinel or a bare
// deadline, in either direction of the error chain.
func TestFlushEconomicCheckpointsAtTerminal_SentinelStaysDistinctFromStoreCauses(t *testing.T) {
	t.Parallel()

	for _, cause := range []error{journalstore.ErrSQLiteRetryCanceled, context.DeadlineExceeded} {
		if errors.Is(ErrEconomicCheckpointFlushTimeout, cause) {
			t.Fatalf("flush-budget sentinel must not wrap %v", cause)
		}
		if errors.Is(cause, ErrEconomicCheckpointFlushTimeout) {
			t.Fatalf("%v must not match the flush-budget sentinel", cause)
		}
	}
}
