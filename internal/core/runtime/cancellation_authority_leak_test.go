package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// TestPersistCancellationBillingUsageAuthorityLeak locks in the fix for the
// usage-authority reservation leak on the cancellation path.
//
// Root cause: persistCancellationBilling guarded on accounting.usageObserved and
// returned early, skipping settleAttemptAuthority entirely. usageObserved flips to
// true when a backend EventUsageDelta is processed mid-stream (handleRecvSuccess),
// but that mid-stream delta does NOT settle the strict reservation (the final settle
// runs at response_finished via finalizeTokenAccounting). So a cancel after a usage
// delta but before response_finished left the reservation locked until the accounting
// window resets.
//
// The fix settles the reservation with the observed usage (Cancellation kind) when
// usageObserved is true, no-ops when the reservation is already settled, and releases
// with ReleaseKindLosing when the settle fails (mirroring finalizeResponseFinishedAuthority).
func TestPersistCancellationBillingUsageAuthorityLeak(t *testing.T) {
	t.Parallel()

	const reservationID = "reservation-cancel-usage-leak"

	// usageDelta stands in for the mid-stream backend EventUsageDelta that
	// handleRecvSuccess observes (flipping accounting.usageObserved=true) without
	// settling the strict reservation.
	usageDelta := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   5,
		OutputTokens:  2,
		TotalTokens:   7,
		CostNanoUnits: 42,
		Currency:      "USD",
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}

	// setupStream stages a retryRecvStream with a reserved authority and a mid-stream
	// usage delta already observed, then returns the stream. It does NOT settle the
	// reservation (the leak precondition: usage observed but not yet finalized).
	setupStream := func(t *testing.T, auth *recordingAuthorityService) *retryRecvStream {
		t.Helper()
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		rs := &retryRecvStream{
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: aipapi.Call{
					ID:         "request-cancel-usage-leak",
					Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions},
				},
				traceID: "trace-cancel-usage-leak",
				aLegID:  aLegID,
			}),
			attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-cancel-usage-leak", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(7),
				admissionResult: auth.admitResult,
			}, authorityCandidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
			responsePipeline: &responsePipeline{seenEvents: []aipapi.Event{usageDelta}},
		}
		// Simulate handleRecvSuccess having processed the EventUsageDelta: this flips
		// accounting.usageObserved=true (the leak trigger) without settling the reservation.
		testAttemptSession(rs).accounting.observeUsage(usageDelta)
		if !testAttemptSession(rs).accounting.usageObserved {
			t.Fatal("test staging: usageObserved must be true after observeUsage")
		}
		return rs
	}

	// cancelAfterUsageAuth builds a recording authority service with the supplied settle
	// error (nil settleErr means settle succeeds).
	cancelAfterUsageAuth := func(settleErr error) *recordingAuthorityService {
		return &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  reservationID,
				ReservedAmount: authorityInputAmount(7),
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
			settleErr: settleErr,
			status:    controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
	}

	t.Run("cancel_after_usage_settles_cancellation", func(t *testing.T) {
		t.Parallel()
		auth := cancelAfterUsageAuth(nil)
		rs := setupStream(t, auth)

		rs.terminal.persistCancellationBilling(context.Background(), rs.attempt.snapshot(), "client canceled", rs.facts.terminalFacts(), rs.responsePipeline)

		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (cancel after usage must settle the reservation, not leak)", auth.settleCalls.Load())
		}
		got := auth.lastSettle()
		if got.Kind != usagelimit.SettlementKindCancellation {
			t.Fatalf("settle kind = %q, want cancellation", got.Kind)
		}
		if !got.ClientCanceled {
			t.Fatal("expected client canceled settlement")
		}
		if got.ReservationID != reservationID {
			t.Fatalf("settle reservation ID = %q, want %q", got.ReservationID, reservationID)
		}
		if got.FinalUsage.Value != 5 {
			t.Fatalf("final usage = %d, want 5 (observed usage delta input tokens, not the admission estimate)", got.FinalUsage.Value)
		}
		if auth.releaseCalls.Load() != 0 {
			t.Fatalf("release calls = %d, want 0 (successful settle must not release)", auth.releaseCalls.Load())
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled=true after successful cancellation settle")
		}
	})

	t.Run("cancel_after_usage_failed_settle_releases_losing", func(t *testing.T) {
		t.Parallel()
		auth := cancelAfterUsageAuth(errors.New("settle boom"))
		rs := setupStream(t, auth)

		rs.terminal.persistCancellationBilling(context.Background(), rs.attempt.snapshot(), "client canceled", rs.facts.terminalFacts(), rs.responsePipeline)

		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (single failed cancellation settle)", auth.settleCalls.Load())
		}
		if got := auth.lastSettle(); got.Kind != usagelimit.SettlementKindCancellation {
			t.Fatalf("settle kind = %q, want cancellation", got.Kind)
		}
		if auth.releaseCalls.Load() != 1 {
			t.Fatalf("release calls = %d, want 1 (reservation must be released when settle failed, not leaked)", auth.releaseCalls.Load())
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
	})

	t.Run("cancel_after_usage_no_double_settle_when_already_settled", func(t *testing.T) {
		t.Parallel()
		auth := cancelAfterUsageAuth(nil)
		rs := setupStream(t, auth)
		// Simulate a prior final settle (e.g. response_finished already finalized the
		// reservation) so authoritySettled is true before the cancellation billing call.
		testAttemptSession(rs).authority.control.mu.Lock()
		testAttemptSession(rs).authority.control.terminal = authorityTerminalSettled
		testAttemptSession(rs).authority.control.mu.Unlock()

		rs.terminal.persistCancellationBilling(context.Background(), rs.attempt.snapshot(), "client canceled", rs.facts.terminalFacts(), rs.responsePipeline)

		// With authoritative usage available (usageObserved=true) and the reservation
		// already settled, the cancellation path calls ReconcileAuthoritative instead
		// of no-op double-settling. The reconcile sends a Final/Authoritative settle
		// with authoritative Sequence so the store applies an authoritative adjustment,
		// not a replay of the prior settle.
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (authoritative reconcile on already-settled reservation)", auth.settleCalls.Load())
		}
		got := auth.lastSettle()
		if got.Kind != usagelimit.SettlementKindFinal {
			t.Fatalf("reconcile settle kind = %q, want final", got.Kind)
		}
		if got.Authority != usagelimit.AuthorityLevelAuthoritative {
			t.Fatalf("reconcile settle authority = %q, want authoritative", got.Authority)
		}
		if got.ClientCanceled {
			t.Fatal("reconcile settle must not carry client canceled (it is an authoritative adjustment)")
		}
		if got.ReservationID != reservationID {
			t.Fatalf("reconcile settle reservation ID = %q, want %q unchanged", got.ReservationID, reservationID)
		}
		if got.Sequence != usagelimit.SettlementSequence(usagelimit.SettlementKindFinal, usagelimit.AuthorityLevelAuthoritative) {
			t.Fatalf("reconcile settle sequence = %d, want authoritative final sequence", got.Sequence)
		}
		if auth.releaseCalls.Load() != 0 {
			t.Fatalf("release calls = %d, want 0 (reconcile must not release)", auth.releaseCalls.Load())
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled to remain true")
		}
	})

	t.Run("cancel_after_usage_with_canceled_ctx_settles_without_leak", func(t *testing.T) {
		t.Parallel()
		auth := cancelAfterUsageAuth(nil)
		rs := setupStream(t, auth)

		// A canceled client context must not abort the post-output settlement
		// (requirement 10.4, 11.7). The lifecycle detaches cancellation and adds
		// its own cleanup deadline, so settlement can still apply despite ctx.Err().
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if ctx.Err() == nil {
			t.Fatal("test staging: ctx must be canceled")
		}

		rs.terminal.persistCancellationBilling(ctx, rs.attempt.snapshot(), "client canceled", rs.facts.terminalFacts(), rs.responsePipeline)

		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (canceled ctx must not prevent settlement)", auth.settleCalls.Load())
		}
		got := auth.lastSettle()
		if got.Kind != usagelimit.SettlementKindCancellation {
			t.Fatalf("settle kind = %q, want cancellation", got.Kind)
		}
		if !got.ClientCanceled {
			t.Fatal("expected client canceled settlement")
		}
		if got.FinalUsage.Value != 5 {
			t.Fatalf("final usage = %d, want 5 (observed usage, not admission estimate)", got.FinalUsage.Value)
		}
		if auth.releaseCalls.Load() != 0 {
			t.Fatalf("release calls = %d, want 0 (successful settle must not release)", auth.releaseCalls.Load())
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled=true after cancellation settle despite canceled ctx")
		}
	})

	t.Run("cancel_after_usage_failed_settle_with_canceled_ctx_releases_losing", func(t *testing.T) {
		t.Parallel()
		auth := cancelAfterUsageAuth(errors.New("settle boom"))
		rs := setupStream(t, auth)

		// Even with a canceled ctx, the losing-fallback release must run on a
		// non-canceled context so the reservation is not leaked (requirement 11.7).
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		rs.terminal.persistCancellationBilling(ctx, rs.attempt.snapshot(), "client canceled", rs.facts.terminalFacts(), rs.responsePipeline)

		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (single failed cancellation settle)", auth.settleCalls.Load())
		}
		if auth.releaseCalls.Load() != 1 {
			t.Fatalf("release calls = %d, want 1 (losing release must run despite canceled ctx, not leak)", auth.releaseCalls.Load())
		}
		rel := auth.lastRelease()
		if rel.Kind != usagelimit.ReleaseKindLosing {
			t.Fatalf("release kind = %q, want %q", rel.Kind, usagelimit.ReleaseKindLosing)
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled=true after fallback release despite canceled ctx")
		}
	})

	t.Run("settle_stays_unsettled_when_store_genuinely_unavailable", func(t *testing.T) {
		t.Parallel()
		// When both settle AND losing-release fail (genuine store unavailability,
		// not cancellation), the lifecycle must NOT mark settled so a later
		// non-canceled finalize can retry (requirement 10.4).
		auth := &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  reservationID,
				ReservedAmount: authorityInputAmount(7),
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
			settleErr:  errors.New("store unavailable"),
			releaseErr: errors.New("store unavailable"),
			status:     controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		rs := &retryRecvStream{
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: aipapi.Call{
					ID:         "request-store-unavailable",
					Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions},
				},
				traceID: "trace-store-unavailable",
				aLegID:  aLegID,
			}),
			attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-store-unavailable", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(7),
				admissionResult: auth.admitResult,
			}, authorityCandidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
			responsePipeline: &responsePipeline{seenEvents: []aipapi.Event{usageDelta}},
		}
		testAttemptSession(rs).accounting.observeUsage(usageDelta)

		rs.terminal.persistCancellationBilling(context.Background(), rs.attempt.snapshot(), "client canceled", rs.facts.terminalFacts(), rs.responsePipeline)

		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
		}
		if auth.releaseCalls.Load() != 1 {
			t.Fatalf("release calls = %d, want 1 (losing release attempted)", auth.releaseCalls.Load())
		}
		if testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled=false when both settle and release failed (store unavailable), so a later finalize can retry")
		}
	})
}
