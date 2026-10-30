package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// TestHandleResponseFinishedAuthorityLeakOnSettleFailure asserts that the normal
// response_finished completion path releases the usage-authority reservation when
// the final settle fails. Without the fix, finalizeTokenAccounting swallows the
// settle failure (the settleAttemptAuthority wrapper only flips authoritySettled
// on success) and returns success, so handleResponseFinishedPath returns through
// either the synthesized-usage (ok) branch or the default fall-through without any
// !authoritySettled release, leaving the reservation locked until the window resets.
//
// Both return branches are covered because both are reachable with the stub:
//   - ok_branch_synthesized_usage: StreamUsage reconstructs usage so
//     finalizeTokenAccounting returns ok=true and the handler emits synthesized
//     usage, then returns.
//   - fallthrough_branch_no_stream_usage: StreamUsage == nil so
//     finalizeTokenAccounting settles and returns ok=false; the handler falls
//     through to the default client-event emit and returns.
func TestHandleResponseFinishedAuthorityLeakOnSettleFailure(t *testing.T) {
	t.Parallel()

	const reservationID = "reservation-finished-leak"

	makeFailingSettleAuth := func() *recordingAuthorityService {
		return &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  reservationID,
				ReservedAmount: authorityInputAmount(7),
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
			settleErr: errors.New("settle boom"),
			status:    controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
	}

	setupStream := func(t *testing.T, auth *recordingAuthorityService) (*Executor, *retryRecvStream) {
		t.Helper()
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		rs := &retryRecvStream{
			terminal: newTurnTerminal(),
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: aipapi.Call{ID: "request-finished-leak", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
				traceID:  "trace-finished-leak",
				aLegID:   aLegID,
			}),
			attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-finished-leak", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
			responsePipeline: newResponsePipeline(),
		}
		bindTestRuntimeOwners(rs, ex)
		return ex, rs
	}

	// assertReleasedAfterFailedSettle verifies the reservation was released with
	// ReleaseKindLosing (the attempt completed but could not be settled, a losing
	// accounting outcome) and that the failed final settle is the only settle.
	assertReleasedAfterFailedSettle := func(t *testing.T, auth *recordingAuthorityService, rs *retryRecvStream) {
		t.Helper()
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (single failed final settle)", auth.settleCalls.Load())
		}
		if got := auth.lastSettle(); got.Kind != usagelimit.SettlementKindFinal {
			t.Fatalf("settle kind = %q, want final", got.Kind)
		}
		if auth.releaseCalls.Load() != 1 {
			t.Fatalf("release calls = %d, want 1 (reservation must be released when final settle failed, not leaked)", auth.releaseCalls.Load())
		}
		rel := auth.lastRelease()
		if rel.Kind != usagelimit.ReleaseKindLosing {
			t.Fatalf("release kind = %q, want %q", rel.Kind, usagelimit.ReleaseKindLosing)
		}
		if rel.ReservationID != reservationID {
			t.Fatalf("release reservation ID = %q, want %q", rel.ReservationID, reservationID)
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled=true after fallback release so later handlers cannot double-release")
		}
	}

	t.Run("ok_branch_synthesized_usage", func(t *testing.T) {
		t.Parallel()
		auth := makeFailingSettleAuth()
		ex, rs := setupStream(t, auth)
		ex.StreamUsage = accountingstream.New(&stubStreamCounter{
			call:   accountingapp.CountResult{InputTokens: 7, TotalTokens: 7},
			output: accountingapp.CountResult{OutputTokens: 3, TotalTokens: 10},
		}, accountingstream.Config{})
		bindTestRuntimeOwners(rs, ex)

		ev, err := testRecvOne(context.Background(), rs, aipapi.Event{Kind: aipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("handleRecvSuccess: %v", err)
		}
		// The ok branch returns the synthesized usage event, not the finish event.
		if ev.Kind != aipapi.EventUsageDelta {
			t.Fatalf("event kind = %q, want usage_delta (synthesized usage from ok branch)", ev.Kind)
		}
		assertReleasedAfterFailedSettle(t, auth, rs)
	})

	t.Run("fallthrough_branch_no_stream_usage", func(t *testing.T) {
		t.Parallel()
		auth := makeFailingSettleAuth()
		ex, rs := setupStream(t, auth)
		ex.StreamUsage = nil

		ev, err := testRecvOne(context.Background(), rs, aipapi.Event{Kind: aipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("handleRecvSuccess: %v", err)
		}
		// The fall-through returns the original finish event unchanged.
		if ev.Kind != aipapi.EventResponseFinished {
			t.Fatalf("event kind = %q, want response_finished (fall-through branch)", ev.Kind)
		}
		assertReleasedAfterFailedSettle(t, auth, rs)
	})
}
