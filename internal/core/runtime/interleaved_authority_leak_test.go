package runtime

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// setupInterleavedAuthorityContinuation wires an interleaved-enabled executor backed by
// a recording authority service and a single "backend-1" backend, then builds the thinker
// retryRecvStream ("from") that openInterleavedExecutorContinuation hands off from. The
// returned thinker carries an ALeg scope so the continuation's RegisterBLeg branch is
// exercised. streamToClient selects hidden or visible interleaved mode on the executor.
func setupInterleavedAuthorityContinuation(t *testing.T, auth *recordingAuthorityService, streamToClient string) (*Executor, *retryRecvStream) {
	t.Helper()
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)
	ex.ALegLifecycle = coord
	memoStore := interleavedthinking.NewMemoStore(4096)
	ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{
		Instructions:   "think",
		StreamToClient: streamToClient,
		MaxMemoBytes:   4096,
	}, memoStore)
	RegisterTestMemoStore(ex, memoStore)

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	from := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{
				ID:    "request-1",
				Route: aipapi.RouteIntent{Selector: "backend-1:model-1"},
				Invocation: aipapi.Invocation{
					Operation:    aipapi.OperationOpenAIChatCompletions,
					DeliveryMode: aipapi.DeliveryModeStreaming,
				},
				Messages: testMinimalUserMessages(),
			},
			aLegID:  aLegID,
			traceID: "trace-1",
		}),
		recovery: &recoveryController{budget: &attemptBudget{max: 3, used: 0}, sel: sel, session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}, rng: routing.NewSeededRng(1)}, terminal: newTurnTerminalWithALeg(aScope, aLegEndBase),
		attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "thinker-bleg", Seq: 1}, routing.AttemptCandidate{Key: "backend-1:model-1", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}}, authorityLifecycle{}),
		responsePipeline: newResponsePipeline(),
	}
	bindTestRuntimeOwners(from, ex)
	return ex, from
}

// TestOpenInterleavedExecutorContinuation_SettlesExecutorLegAuthority reproduces L1: the
// executor-leg reservation returned by tryPlanOpenOnce/openPlannedCandidate was never
// tracked on the continuation's retryRecvStream because the rs literal omitted
// the executor continuation's attemptSession. finalizeTokenAccounting/recordPartialTokenAccounting then
// settled the wrong lifecycle (a no-op), leaking the freshly admitted reservation on
// every thinker->executor handoff. This drives the continuation in BOTH hidden and
// visible mode and asserts the executor-leg reservation is settled (not leaked) on a
// normal response_finished EOF, with the captured ReservationID observed.
func TestOpenInterleavedExecutorContinuation_SettlesExecutorLegAuthority(t *testing.T) {
	t.Parallel()

	makeAuth := func() *recordingAuthorityService {
		return &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "reservation-executor",
				ReservedAmount: authorityInputAmount(9),
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
			status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
	}

	driveToCompletion := func(t *testing.T, rs *retryRecvStream) {
		t.Helper()
		for {
			if _, err := rs.Recv(context.Background()); err != nil {
				if !errors.Is(err, io.EOF) {
					t.Fatalf("Recv: %v", err)
				}
				return
			}
		}
	}

	assertAccounted := func(t *testing.T, auth *recordingAuthorityService) {
		t.Helper()
		if got := auth.settleCalls.Load() + auth.releaseCalls.Load(); got != 1 {
			t.Fatalf("settle+release calls = %d, want 1 (executor-leg reservation must be settled or released, not leaked)", got)
		}
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (response_finished finalize settles the executor-leg reservation)", auth.settleCalls.Load())
		}
		settle := auth.lastSettle()
		if settle.ReservationID != "reservation-executor" {
			t.Fatalf("settled reservation ID = %q, want reservation-executor", settle.ReservationID)
		}
	}

	t.Run("hidden", func(t *testing.T) {
		t.Parallel()
		auth := makeAuth()
		ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")
		rs, err := ex.openInterleavedExecutorContinuation(context.Background(), from, interleavedstate.State{})
		if err != nil {
			t.Fatalf("openInterleavedExecutorContinuation: %v", err)
		}
		if rs == nil {
			t.Fatal("expected non-nil continuation stream")
		}
		driveToCompletion(t, rs)
		assertAccounted(t, auth)
	})

	t.Run("visible", func(t *testing.T) {
		t.Parallel()
		auth := makeAuth()
		ex, from := setupInterleavedAuthorityContinuation(t, auth, "visible")
		rs, err := ex.openInterleavedExecutorContinuation(context.Background(), from, interleavedstate.State{})
		if err != nil {
			t.Fatalf("openInterleavedExecutorContinuation: %v", err)
		}
		if rs == nil {
			t.Fatal("expected non-nil continuation stream")
		}
		driveToCompletion(t, rs)
		assertAccounted(t, auth)
	})
}

// TestOpenInterleavedExecutorContinuation_RegisterBLegFailureReleasesLocalAuthority
// reproduces L8: when RegisterBLeg fails inside openInterleavedExecutorContinuation (the
// A-leg is canceled mid-flight during backend.Open, after out.authority was admitted),
// the error branch returned without releasing the freshly admitted local out.authority.
// After L1's fix the continuation session is populated only after this branch, so
// here the LOCAL out.authority must be settled (not a stream placeholder). Incurred Open work
// settles with SettlementKindSwallowed; mirrors sibling RegisterBLeg-failure sites.
func TestOpenInterleavedExecutorContinuation_RegisterBLegFailureReleasesLocalAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-executor",
			ReservedAmount: authorityInputAmount(9),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")

	coord := ex.ALegLifecycle
	aLegID := from.facts.aLegID
	// Cancel the A-leg inside backend.Open so RegisterBLeg fails with ErrALegCanceled
	// after out.authority was reserved. A fixed stream is returned so the coordinator's
	// cancel cleanup has a B-leg attempt to tear down.
	be := ex.Backends["backend-1"]
	be.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		if err := coord.CancelALeg(ctx, aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
			return nil, err
		}
		return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
	}
	ex.Backends["backend-1"] = be

	_, err := ex.openInterleavedExecutorContinuation(context.Background(), from, interleavedstate.State{})
	if err == nil {
		t.Fatal("expected openInterleavedExecutorContinuation to surface the RegisterBLeg failure")
	}
	if !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("openInterleavedExecutorContinuation error = %v, want ErrALegCanceled", err)
	}

	if got, want := auth.releaseCalls.Load(), int64(0); got != want {
		t.Fatalf("release calls = %d, want %d (incurred Open must settle, not release)", got, want)
	}
	if got, want := auth.settleCalls.Load(), int64(1); got != want {
		t.Fatalf("settle calls = %d, want %d (local out.authority must settle on RegisterBLeg failure)", got, want)
	}
	settle := auth.lastSettle()
	if settle.ReservationID != "reservation-executor" {
		t.Fatalf("settled reservation ID = %q, want reservation-executor (the local out.authority)", settle.ReservationID)
	}
	if settle.Kind != usagelimit.SettlementKindSwallowed {
		t.Fatalf("settle kind = %q, want swallowed", settle.Kind)
	}
}
