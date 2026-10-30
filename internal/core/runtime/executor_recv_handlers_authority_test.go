package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	secureapp "github.com/aiproxer/aiproxer/internal/core/securesession/app"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

// TestHandleRecvSuccessErrorExitsReleaseAuthority asserts that every error exit in
// handleRecvSuccess settles (or, where already settled, does not double-settle) the
// usage-authority reservation instead of returning to the client with the reservation
// still active. These are success events that fail a downstream policy/hook/gate or the
// mandatory secure-session recorder; usage has already been observed, so the matching
// cleanup is a partial settlement (same helper the sibling handleRecvError surfaced-
// failure path uses), not a release.
func TestHandleRecvSuccessErrorExitsReleaseAuthority(t *testing.T) {
	t.Parallel()

	makeAuth := func(reservationID string) *recordingAuthorityService {
		return &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  reservationID,
				ReservedAmount: authorityInputAmount(7),
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
			status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
	}

	// setupRecvSuccessStream builds a retryRecvStream with a reserved authority and the
	// supplied hook bus. The returned executor is wired to the same authority service so
	// callers can attach a runtime snapshot, secure-session recorder, or mandatory flag.
	setupRecvSuccessStream := func(t *testing.T, auth *recordingAuthorityService, bus *hooks.Bus) (*Executor, *retryRecvStream) {
		t.Helper()
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		ex.Bus = bus
		rs := &retryRecvStream{
			terminal: newTurnTerminal(),
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: aipapi.Call{ID: "request-recv", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
				traceID:  "trace-recv",
				aLegID:   aLegID,
			}),
			attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-recv", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
			responsePipeline: newResponsePipeline(),
		}
		bindTestRuntimeOwners(rs, ex)
		return ex, rs
	}

	assertSettledNotLeaked := func(t *testing.T, auth *recordingAuthorityService, rs *retryRecvStream, err error, cont bool) {
		t.Helper()
		if err == nil {
			t.Fatal("expected error from handleRecvSuccess")
		}
		if cont {
			t.Fatal("expected cont=false so the error surfaces to the client")
		}
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (authority reservation must be settled, not leaked)", auth.settleCalls.Load())
		}
		if auth.releaseCalls.Load() != 0 {
			t.Fatalf("release calls = %d, want 0 (success-event failures settle partial usage)", auth.releaseCalls.Load())
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled=true after authority cleanup")
		}
	}

	t.Run("tool_policy_failure", func(t *testing.T) {
		t.Parallel()
		auth := makeAuth("reservation-tool-policy")
		bus := hooks.New(hooks.Config{})
		ex, rs := setupRecvSuccessStream(t, auth, bus)
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
			FeaturePlanes: freezeBundle(testFeatureBundle{
				ToolCallPolicies: []toolpolicy.Policy{denyingToolPolicyStub{}},
			}),
		})
		bindTestRuntimeOwners(rs, ex)
		ev := aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "call-1", ToolName: "search"}
		_, err := testRecvOne(context.Background(), rs, ev)
		assertSettledNotLeaked(t, auth, rs, err, false)
	})

	t.Run("tool_reactor_failure", func(t *testing.T) {
		t.Parallel()
		auth := makeAuth("reservation-tool-reactor")
		reactorErr := errors.New("reactor boom")
		bus := hooks.New(hooks.Config{
			ToolReactors:           []sdkhooks.ToolReactor{failingToolReactorStub{err: reactorErr}},
			ToolReactorErrorPolicy: sdkhooks.ToolReactorErrorsFailClosed,
		})
		_, rs := setupRecvSuccessStream(t, auth, bus)
		// Nil RuntimeSnapshot => applyToolPolicies returns nil, so the reactor error is reached.
		ev := aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "call-1", ToolName: "search"}
		_, err := testRecvOne(context.Background(), rs, ev)
		assertSettledNotLeaked(t, auth, rs, err, false)
	})

	t.Run("response_part_hook_failure", func(t *testing.T) {
		t.Parallel()
		auth := makeAuth("reservation-resp-hook")
		hookErr := errors.New("resp part hook boom")
		bus := hooks.New(hooks.Config{
			ResponsePartHooks: []sdkhooks.ResponsePartHook{failingResponsePartHookStub{err: hookErr}},
		})
		_, rs := setupRecvSuccessStream(t, auth, bus)
		ev := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hi"}
		_, err := testRecvOne(context.Background(), rs, ev)
		assertSettledNotLeaked(t, auth, rs, err, false)
	})

	t.Run("completion_gate_failure", func(t *testing.T) {
		t.Parallel()
		auth := makeAuth("reservation-gate")
		bus := hooks.New(hooks.Config{})
		gateErr := errors.New("gate boom")
		ex, rs := setupRecvSuccessStream(t, auth, bus)
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
			FeaturePlanes: freezeBundle(testFeatureBundle{
				CompletionGates: []completion.Gate{failingCompletionGateStub{err: gateErr}},
			}),
		})
		bindTestRuntimeOwners(rs, ex)
		ev := aipapi.Event{Kind: aipapi.EventResponseFinished}
		_, err := testRecvOne(context.Background(), rs, ev)
		assertSettledNotLeaked(t, auth, rs, err, false)
	})

	t.Run("mandatory_recorder_failure_gated", func(t *testing.T) {
		t.Parallel()
		auth := makeAuth("reservation-recorder-gated")
		bus := hooks.New(hooks.Config{})
		recErr := errors.New("recorder boom")
		ex, rs := setupRecvSuccessStream(t, auth, bus)
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
			FeaturePlanes: freezeBundle(testFeatureBundle{
				CompletionGates: []completion.Gate{replaceCompletionGateStub{}},
			}),
		})
		ex.SecureSessionRecorder = failingSecureRecorderStub{err: recErr}
		ex.SecureSessionRecordingMandatory = true
		rs = withTestRecvFacts(rs, func(f recvTurnFacts) recvTurnFacts {
			f.secureTurnOK = true
			return f
		})
		bindTestRuntimeOwners(rs, ex)
		ev := aipapi.Event{Kind: aipapi.EventResponseFinished}
		_, err := testRecvOne(context.Background(), rs, ev)
		if err == nil {
			t.Fatal("expected mandatory recorder failure")
		}
		if !errors.Is(err, recErr) {
			t.Fatalf("error = %v, want recorder error", err)
		}
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (gated recorder failure must settle authority, not leak)", auth.settleCalls.Load())
		}
		if auth.releaseCalls.Load() != 0 {
			t.Fatalf("release calls = %d, want 0", auth.releaseCalls.Load())
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled=true after gated recorder failure")
		}
	})

	t.Run("mandatory_recorder_failure_nongated", func(t *testing.T) {
		t.Parallel()
		auth := makeAuth("reservation-recorder-nongated")
		bus := hooks.New(hooks.Config{})
		recErr := errors.New("recorder boom")
		ex, rs := setupRecvSuccessStream(t, auth, bus)
		ex.SecureSessionRecorder = failingSecureRecorderStub{err: recErr}
		ex.SecureSessionRecordingMandatory = true
		rs = withTestRecvFacts(rs, func(f recvTurnFacts) recvTurnFacts {
			f.secureTurnOK = true
			return f
		})
		bindTestRuntimeOwners(rs, ex)
		ev := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hi"}
		_, err := testRecvOne(context.Background(), rs, ev)
		if err == nil {
			t.Fatal("expected mandatory recorder failure")
		}
		if !errors.Is(err, recErr) {
			t.Fatalf("error = %v, want recorder error", err)
		}
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (non-gated recorder failure must settle authority, not leak)", auth.settleCalls.Load())
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled=true after non-gated recorder failure")
		}
	})

	// mandatory_recorder_failure_nongated_already_settled drives the response_finished
	// fall-through: finalizeTokenAccounting settles the reservation (Final) and returns
	// ok=false, then beforeEmitClientFacing fails under SecureSessionRecordingMandatory.
	// The cleanup must NOT settle a second time; the reservation was already finalized.
	t.Run("mandatory_recorder_failure_nongated_already_settled", func(t *testing.T) {
		t.Parallel()
		auth := makeAuth("reservation-recorder-already-settled")
		bus := hooks.New(hooks.Config{})
		recErr := errors.New("recorder boom")
		ex, rs := setupRecvSuccessStream(t, auth, bus)
		ex.SecureSessionRecorder = failingSecureRecorderStub{err: recErr}
		ex.SecureSessionRecordingMandatory = true
		rs = withTestRecvFacts(rs, func(f recvTurnFacts) recvTurnFacts {
			f.secureTurnOK = true
			return f
		})
		// StreamUsage == nil => finalizeTokenAccounting settles Final and returns ok=false,
		// so the handler falls through to the client-facing recorder that then fails.
		ex.StreamUsage = nil
		bindTestRuntimeOwners(rs, ex)
		ev := aipapi.Event{Kind: aipapi.EventResponseFinished}
		_, err := testRecvOne(context.Background(), rs, ev)
		if err == nil {
			t.Fatal("expected mandatory recorder failure")
		}
		if !errors.Is(err, recErr) {
			t.Fatalf("error = %v, want recorder error", err)
		}
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (must not double-settle when finalizeTokenAccounting already settled)", auth.settleCalls.Load())
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("authoritySettled should remain true after the recorder failure")
		}
	})
}

// denyingToolPolicyStub denies every tool event so applyToolPolicies fails.
type denyingToolPolicyStub struct{}

func (denyingToolPolicyStub) ID() string                        { return "deny-tool" }
func (denyingToolPolicyStub) Order() int                        { return 0 }
func (denyingToolPolicyStub) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (denyingToolPolicyStub) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionDeny, nil
}

// failingResponsePartHookStub returns a fail-closed error from HandleEvent.
type failingResponsePartHookStub struct{ err error }

func (failingResponsePartHookStub) ID() string                        { return "fail-resp-part" }
func (failingResponsePartHookStub) Order() int                        { return 0 }
func (failingResponsePartHookStub) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (h failingResponsePartHookStub) HandleEvent(context.Context, *aipapi.Event, sdkhooks.PartMeta) error {
	return h.err
}

// failingToolReactorStub returns a fail-closed error from HandleToolEvent.
type failingToolReactorStub struct{ err error }

func (failingToolReactorStub) ID() string { return "fail-reactor" }
func (failingToolReactorStub) Order() int { return 0 }
func (h failingToolReactorStub) HandleToolEvent(context.Context, aipapi.ToolEvent, sdkhooks.ToolMeta) (sdkhooks.ToolDecision, aipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, aipapi.ToolEvent{}, h.err
}

// failingCompletionGateStub returns a fail-closed error from Handle so completionGatedEmit
// surfaces a non-continue error on the finish event.
type failingCompletionGateStub struct{ err error }

func (failingCompletionGateStub) ID() string                        { return "fail-gate" }
func (failingCompletionGateStub) Order() int                        { return 0 }
func (failingCompletionGateStub) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (g failingCompletionGateStub) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.Outcome{}, g.err
}

// replaceCompletionGateStub replaces the buffered finish with a synthesized stream so the
// gated path produces client-facing output and reaches beforeEmitClientFacing.
type replaceCompletionGateStub struct{}

func (replaceCompletionGateStub) ID() string                        { return "replace-gate" }
func (replaceCompletionGateStub) Order() int                        { return 0 }
func (replaceCompletionGateStub) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (replaceCompletionGateStub) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.ReplaceOutcome([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventTextDelta, Delta: "replaced"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

// failingSecureRecorderStub fails every post-hook stream-event recording and succeeds for
// client-turn recording, satisfying SecureSessionRecorder (app.GateRecording).
type failingSecureRecorderStub struct{ err error }

func (failingSecureRecorderStub) RecordClientTurnAfterGate(context.Context, secureapp.ClientTurnRecordInput) error {
	return nil
}

func (r failingSecureRecorderStub) RecordPostHookStreamEvent(context.Context, secureapp.StreamEventRecordInput) error {
	return r.err
}
