package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// TestRecoverDrainAuthorityLeakOnSettleFailure asserts that the recoverDrain
// response_finished path in Recv releases the usage-authority reservation when
// the final settle fails. Without the fix, finalizeTokenAccounting swallows the
// settle failure (the settleAttemptAuthority wrapper only flips authoritySettled
// on success) and returns success, so the recoverDrain path returns through
// either the synthesized-usage (ok) branch or the response_finished fall-through
// without any !authoritySettled release, leaving the reservation locked until the
// window resets. This is the same leak class as the already-fixed
// handleResponseFinishedPath site (S8), mirrored here for the drain path.
//
// Both return branches are covered because both are reachable with the stub:
//   - ok_branch_synthesized_usage: StreamUsage reconstructs usage so
//     finalizeTokenAccounting returns ok=true and the drain path emits
//     synthesized usage, then returns.
//   - fallthrough_branch_no_stream_usage: StreamUsage == nil so
//     finalizeTokenAccounting settles and returns ok=false; the drain path
//     marks finished and returns the original finish event.
func TestRecoverDrainAuthorityLeakOnSettleFailure(t *testing.T) {
	t.Parallel()

	const reservationID = "reservation-recover-drain-leak"

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
				baseline: aipapi.Call{ID: "request-recover-drain-leak", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
				traceID:  "trace-recover-drain-leak",
				aLegID:   aLegID,
			}),
			attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-recover-drain-leak", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
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

		// Queue a response_finished in recoverDrain so Recv takes the drain path
		// rather than pulling from the backend stream.
		rs.responsePipeline.recoverDrain = []aipapi.Event{{Kind: aipapi.EventResponseFinished}}

		ev, err := rs.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv: %v", err)
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

		// Queue a response_finished in recoverDrain so Recv takes the drain path.
		rs.responsePipeline.recoverDrain = []aipapi.Event{{Kind: aipapi.EventResponseFinished}}

		ev, err := rs.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		// The fall-through returns the original finish event unchanged and marks
		// the stream finished.
		if ev.Kind != aipapi.EventResponseFinished {
			t.Fatalf("event kind = %q, want response_finished (fall-through branch)", ev.Kind)
		}
		if !rs.terminal.finished() {
			t.Fatal("expected stream marked finished after the drain fall-through branch")
		}
		assertReleasedAfterFailedSettle(t, auth, rs)
	})
}

// TestIdleRecoveryFinishAuthorityLeakOnSettleFailure asserts that the
// DecisionFinishPostOutput idle-recovery path in handleRecvError releases the
// usage-authority reservation when the final settle fails. Without the fix,
// finalizeTokenAccounting swallows the settle failure and returns success, so
// the idle-recovery path marks finished and returns the finish event without
// any !authoritySettled release, leaving the reservation locked. This is the
// same leak class as the already-fixed handleResponseFinishedPath site (S8),
// mirrored here for the idle-recovery finish path.
func TestIdleRecoveryFinishAuthorityLeakOnSettleFailure(t *testing.T) {
	t.Parallel()

	const reservationID = "reservation-idle-finish-leak"
	auth := &recordingAuthorityService{
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
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{
		call:   accountingapp.CountResult{InputTokens: 7, TotalTokens: 7},
		output: accountingapp.CountResult{OutputTokens: 3, TotalTokens: 10},
	}, accountingstream.Config{})
	start := time.Unix(1, 0)
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "request-idle-finish-leak", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-idle-finish-leak",
			aLegID:   aLegID,
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{
			admissionInput:  testAuthorityAdmissionInput(7),
			admissionResult: auth.admitResult,
		}, authorityCandidate()), newAttemptAccountingTracker(start)),
		responsePipeline: &responsePipeline{seenEvents: []aipapi.Event{
			{Kind: aipapi.EventTextDelta, Delta: "hello"},
		}},
		recovery: &recoveryController{recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{
			Enabled:     true,
			IdleTimeout: time.Second,
		}, start)},
	}
	bindTestRuntimeOwners(rs, ex)
	rs.responsePipeline.visibleText.WriteString("hello")
	rs.recovery.recoverPolicy.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"}, start.Add(time.Second))

	// handleRecvError now defers response_finished authority finalization to the recoverDrain
	// drain path on the next Recv call (single-owner invariant matches handleRecvEOF). It returns a
	// zero event with cont=false so Recv returns to the caller and re-enters at the recoverDrain
	// drain check; the drain path finalizes via the centralized helper (settle + losing release
	// fallback) and emits the synthesized usage_delta, fixing both the leak and the client-reporting
	// consistency issue.
	ev, err := testRecvError(context.Background(), rs, context.DeadlineExceeded)
	if err != nil {
		t.Fatalf("handleRecvError: %v", err)
	}
	if ev.Kind != "" {
		t.Fatalf("deferred idle recovery event kind = %q, want empty (finish stays in recoverDrain)", ev.Kind)
	}
	if auth.settleCalls.Load() != 0 || auth.releaseCalls.Load() != 0 {
		t.Fatalf("settle=%d release=%d, want 0/0 (finalization deferred to the drain path)", auth.settleCalls.Load(), auth.releaseCalls.Load())
	}
	if len(rs.responsePipeline.recoverDrain) == 0 {
		t.Fatal("recoverDrain should hold the deferred finish after handleRecvError")
	}

	// Drain recoverDrain via Recv; the drain path finalizes via the centralized helper, emits the
	// synthesized usage_delta, and releases the reservation when the final settle fails.
	var finishEv aipapi.Event
	var sawSynthesizedUsage bool
	for {
		ev, err := rs.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if ev.Kind == aipapi.EventUsageDelta {
			sawSynthesizedUsage = true
			continue
		}
		if ev.Kind == aipapi.EventResponseFinished {
			finishEv = ev
			break
		}
	}
	if finishEv.Kind != aipapi.EventResponseFinished {
		t.Fatalf("event kind = %q, want response_finished", finishEv.Kind)
	}
	if !sawSynthesizedUsage {
		t.Fatal("expected the drain path to emit the synthesized usage_delta before the finish (client-reporting consistency fix)")
	}

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
