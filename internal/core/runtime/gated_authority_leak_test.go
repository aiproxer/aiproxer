package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// gatedLeakPassGate is a completion gate that passes the original buffered
// completion through unchanged. It makes the gated path drain a
// response_finished as the emitted out event (out.Kind == EventResponseFinished),
// which is the shape that exercises the gated-completion authority leak in
// handleGatedPath. testPassGate lives in the runtime_test external package, so a
// package-runtime copy is required to drive handleRecvSuccess directly.
type gatedLeakPassGate struct{}

func (gatedLeakPassGate) ID() string                        { return "gated-leak-pass" }
func (gatedLeakPassGate) Order() int                        { return 0 }
func (gatedLeakPassGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gatedLeakPassGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

// TestHandleGatedPathAuthorityLeakOnCompletion asserts that a successful gated
// completion (completion gates active, response_finished drained through
// handleGatedPath) finalizes the usage-authority reservation the same way the
// no-gates handleResponseFinishedPath does: a final settle on success, and a
// ReleaseKindLosing release when the final settle fails. Without the fix,
// handleGatedPath only records attempt success and marks the stream finished via
// emitGateDrained; it never runs finalizeTokenAccounting or a final
// settle/release, so a reserved authority stays locked until the accounting
// window resets.
//
// Three subtests cover the reachable completion shapes:
//   - settle_success_final_settles: StreamUsage == nil so finalizeTokenAccounting
//     settles Final and returns ok=false; the reservation must be settled once
//     (Final) and not released.
//   - settle_failure_releases: StreamUsage == nil and settle fails; the failed
//     Final settle must be followed by a ReleaseKindLosing release.
//   - settle_failure_synthesized_usage_releases: StreamUsage reconstructs usage
//     so finalizeTokenAccounting returns ok=true (synthesized usage); the failed
//     Final settle must still be followed by a ReleaseKindLosing release and the
//     synthesized usage event must be returned to the client.
func TestHandleGatedPathAuthorityLeakOnCompletion(t *testing.T) {
	t.Parallel()

	const reservationID = "reservation-gated-leak"

	makeAuth := func(settleErr error) *recordingAuthorityService {
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

	// setupGatedStream wires a retryRecvStream with a reserved authority and a
	// completion-gates runtime snapshot carrying the passthrough gate, so a
	// response_finished dispatched through handleRecvSuccess routes to
	// handleGatedPath and drains the finish as the emitted out event.
	setupGatedStream := func(t *testing.T, auth *recordingAuthorityService) (*Executor, *retryRecvStream) {
		t.Helper()
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		bus := hooks.New(hooks.Config{})
		rs := &retryRecvStream{
			terminal: newTurnTerminal(),
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: aipapi.Call{ID: "request-gated-leak", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
				traceID:  "trace-gated-leak",
				aLegID:   aLegID,
			}),
			attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-gated-leak", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
			responsePipeline: newResponsePipeline(),
		}
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
			FeaturePlanes: freezeBundle(testFeatureBundle{
				CompletionGates: []completion.Gate{gatedLeakPassGate{}},
			}),
		})
		bindTestRuntimeOwners(rs, ex)
		return ex, rs
	}

	// assertSettledNotReleased verifies the normal gated completion settled the
	// reservation with a single Final settle and did not release it.
	assertSettledNotReleased := func(t *testing.T, auth *recordingAuthorityService, rs *retryRecvStream, ev aipapi.Event) {
		t.Helper()
		if ev.Kind != aipapi.EventResponseFinished {
			t.Fatalf("event kind = %q, want response_finished (fall-through branch)", ev.Kind)
		}
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (gated completion must finalize authority, not leak)", auth.settleCalls.Load())
		}
		if got := auth.lastSettle(); got.Kind != usagelimit.SettlementKindFinal {
			t.Fatalf("settle kind = %q, want final", got.Kind)
		}
		if auth.releaseCalls.Load() != 0 {
			t.Fatalf("release calls = %d, want 0 (successful final settle must not release)", auth.releaseCalls.Load())
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled=true after final settle on gated completion")
		}
	}

	// assertReleasedAfterFailedSettle verifies the failed final settle is the
	// only settle and the reservation was released with ReleaseKindLosing so it
	// does not stay locked until the accounting window resets.
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

	t.Run("settle_success_final_settles", func(t *testing.T) {
		t.Parallel()
		auth := makeAuth(nil)
		_, rs := setupGatedStream(t, auth)

		ev, err := testRecvOne(context.Background(), rs, aipapi.Event{Kind: aipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("handleRecvSuccess: %v", err)
		}
		assertSettledNotReleased(t, auth, rs, ev)
	})

	t.Run("settle_failure_releases", func(t *testing.T) {
		t.Parallel()
		auth := makeAuth(errors.New("settle boom"))
		_, rs := setupGatedStream(t, auth)

		ev, err := testRecvOne(context.Background(), rs, aipapi.Event{Kind: aipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("handleRecvSuccess: %v", err)
		}
		assertReleasedAfterFailedSettle(t, auth, rs)
		if ev.Kind != aipapi.EventResponseFinished {
			t.Fatalf("event kind = %q, want response_finished (fall-through branch)", ev.Kind)
		}
	})

	t.Run("settle_failure_synthesized_usage_releases", func(t *testing.T) {
		t.Parallel()
		auth := makeAuth(errors.New("settle boom"))
		ex, rs := setupGatedStream(t, auth)
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
}
