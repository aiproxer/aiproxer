package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// TestInterleavedAbortExecutorHandoff_ReleasesExecutorLegAuthority reproduces L1b: the L1
// fix populated the executor attemptSession on the retryRecvStream returned by
// openInterleavedExecutorContinuation, which correctly handles the normal Recv-to-EOF and
// error paths but exposed a leak on the abort path. In beginExecutorContinuation, when
// handoffAborted returns non-nil AFTER the executor open succeeded (so executor authority is
// reserved) but BEFORE the first Recv, abortExecutorHandoff is invoked while s.executor is
// still nil (it is only assigned on the success path), so the normal
// closeActiveInner/finishWithCleanup executor cleanup never runs for this exec stream.
// abortExecutorHandoff closed the inner and marked the stream finished but never released
// exec attempt authority, leaking the freshly admitted executor-leg reservation.
//
// This stages that exact window: the executor open succeeds and reserves
// "reservation-executor-abort", then handoffAborted returns io.EOF (the combined stream is
// marked finished) before any Recv, routing into the abort branch. The executor-leg
// reservation must be released with ReleaseKindSwallowed (no client-facing output was
// produced before the abort), matching the sibling L1/L8 release sites.
func TestInterleavedAbortExecutorHandoff_ReleasesExecutorLegAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-executor-abort",
			ReservedAmount: authorityInputAmount(9),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")
	_ = ex

	// Wrap the thinker leg in the hidden interleaved stream. A nil recorder makes
	// captureAndPersistThinkerMemo return early, so the handoff reaches
	// openInterleavedExecutorContinuation without requiring a captured memo.
	s := newHiddenInterleavedStream(from, nil, interleavedstate.State{})

	// Mark the combined stream finished so handoffAborted returns io.EOF AFTER the
	// executor open succeeds (populating the executor attemptSession) but BEFORE the first Recv,
	// routing beginExecutorContinuation into the abort branch. s.executor is still nil
	// at this point, so the normal executor cleanup path cannot release the reservation.
	s.mu.Lock()
	s.finished = true
	s.mu.Unlock()

	_, err := s.beginExecutorContinuation(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("beginExecutorContinuation error = %v, want io.EOF (handoff aborted)", err)
	}

	if got, want := auth.releaseCalls.Load(), int64(0); got != want {
		t.Fatalf("release calls = %d, want %d (incurred executor-leg must settle on abort)", got, want)
	}
	if got, want := auth.settleCalls.Load(), int64(1); got != want {
		t.Fatalf("settle calls = %d, want %d (executor-leg reservation must settle on abort, not leak)", got, want)
	}
	settle := auth.lastSettle()
	if settle.ReservationID != "reservation-executor-abort" {
		t.Fatalf("settled reservation ID = %q, want reservation-executor-abort (the executor-leg reservation)", settle.ReservationID)
	}
	if settle.Kind != usagelimit.SettlementKindSwallowed {
		t.Fatalf("settle kind = %q, want swallowed (no client-facing output was produced before the abort)", settle.Kind)
	}
}

func TestInterleavedThinkerError_FinalizesAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-thinker-error",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")
	installTestTurnTerminal(from)
	from.terminal.setInterleavedThinker()
	testAttemptSession(from).authority = ex.newAttemptAuthorityLifecycle(attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}, testAttemptSession(from).cand)

	testStoreInner(from, aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventError, ErrorCode: "thinker_failed", ErrorMessage: "thinker-failed"},
	}))

	s := newHiddenInterleavedStream(from, nil, interleavedstate.State{})

	_, err := s.Recv(context.Background())
	if err != nil {
		t.Fatalf("expected nil error on EventError return, got %v", err)
	}

	if got, want := auth.releaseCalls.Load()+auth.settleCalls.Load(), int64(1); got != want {
		t.Fatalf("release+settle calls = %d, want %d (thinker authority reservation must be finalized)", got, want)
	}
}

func TestInterleavedThinkerEOF_Truncated_NoContinuation(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-thinker-truncated",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")
	installTestTurnTerminal(from)
	from.terminal.setInterleavedThinker()
	testAttemptSession(from).authority = ex.newAttemptAuthorityLifecycle(attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}, testAttemptSession(from).cand)

	// Truncated thinker EOF: no response_finished, so the request accounting claim stays false.
	testStoreInner(from, aipapi.NewFixedEventStream(nil))

	s := newHiddenInterleavedStream(from, nil, interleavedstate.State{})

	_, err := s.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF on truncated thinker, got %v", err)
	}

	s.mu.Lock()
	execStream := s.executor
	s.mu.Unlock()
	if execStream != nil {
		t.Fatal("expected executor continuation to not be opened for truncated thinker EOF")
	}

	reqTerm, _ := testTerminalOwners(from)
	if reqTerm.Owner().State() == sdkterminal.StateOpen {
		t.Fatal("expected request terminal to be terminalized (not StateOpen) due to truncated EOF")
	}

	if got, want := auth.releaseCalls.Load()+auth.settleCalls.Load(), int64(1); got != want {
		t.Fatalf("release+settle calls = %d, want %d (thinker authority reservation must be finalized)", got, want)
	}
}

func TestInterleavedThinkerCancel_FinalizesAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-thinker-cancel",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")
	installTestTurnTerminal(from)
	from.terminal.setInterleavedThinker()
	testAttemptSession(from).authority = ex.newAttemptAuthorityLifecycle(attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}, testAttemptSession(from).cand)
	testStoreInner(from, aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
	}))

	s := newHiddenInterleavedStream(from, nil, interleavedstate.State{})
	_ = s.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelClientGone})

	if got, want := auth.releaseCalls.Load()+auth.settleCalls.Load(), int64(1); got != want {
		t.Fatalf("release+settle calls = %d, want %d (thinker cancel must finalize authority)", got, want)
	}
}

func TestInterleavedThinkerResponseFinished_OpensContinuation(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-executor-continue",
			ReservedAmount: authorityInputAmount(9),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")
	installTestTurnTerminal(from)
	from.terminal.setInterleavedThinker()
	testAttemptSession(from).authority = ex.newAttemptAuthorityLifecycle(attemptAuthorityState{
		admissionInput: testAuthorityAdmissionInput(5),
		admissionResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-thinker-ok",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
	}, testAttemptSession(from).cand)
	testStoreInner(from, aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseFinished},
	}))

	s := newHiddenInterleavedStream(from, nil, interleavedstate.State{})

	// First Recv consumes response_finished (attempt-only) and may hand off; drain until EOF.
	for {
		_, err := s.Recv(context.Background())
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("Recv: %v", err)
			}
			break
		}
	}

	s.mu.Lock()
	phase := s.phase
	s.mu.Unlock()
	if phase != interleavedPhaseExecutor {
		t.Fatalf("phase = %v, want executor (response_finished must open continuation)", phase)
	}
	_ = ex
}

// gateEnteredBlockStream signals when Recv is entered, then blocks until Cancel/Close
// or the caller context ends. Used to park the first executor Recv after assignment.
type gateEnteredBlockStream struct {
	entered     chan struct{}
	enteredOnce sync.Once
	done        chan struct{}
	doneOnce    sync.Once
}

func (g *gateEnteredBlockStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	g.enteredOnce.Do(func() { close(g.entered) })
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-g.done:
		return aipapi.Event{}, context.Canceled
	}
}

func (g *gateEnteredBlockStream) wake() {
	g.doneOnce.Do(func() { close(g.done) })
}

func (g *gateEnteredBlockStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	g.wake()
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (g *gateEnteredBlockStream) Close() error {
	g.wake()
	return nil
}

func setupInterleavedAssignedExecutorGate(t *testing.T) (
	*interleavedContinuationStream,
	*recordingAuthorityService,
	*abortJoinCapture,
	<-chan struct{},
) {
	t.Helper()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-executor-assigned",
			ReservedAmount: authorityInputAmount(9),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	capture := &abortJoinCapture{}
	ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")
	wireAbortBilling(ex, capture)

	entered := make(chan struct{})
	gate := &gateEnteredBlockStream{entered: entered, done: make(chan struct{})}
	ex.Backends = map[string]execbackend.Backend{
		"backend-1": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			EnforcesMaxOutputTokens: true,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return gate, nil
			},
		},
	}

	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	from = withTestRecvFacts(from, func(f recvTurnFacts) recvTurnFacts {
		f.billingCallID = callID
		return f
	})
	from = stampStreamIdentity(from)
	installTestTurnTerminal(from)
	from.terminal.setInterleavedThinker()
	installTestTurnTerminal(from)
	testAttemptSession(from).authority = ex.newAttemptAuthorityLifecycle(attemptAuthorityState{
		admissionInput: testAuthorityAdmissionInput(5),
		admissionResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-thinker-assigned",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
	}, testAttemptSession(from).cand)
	testStoreInner(from, aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseFinished},
	}))

	s := newHiddenInterleavedStream(from, nil, interleavedstate.State{})
	return s, auth, capture, entered
}

func drainInterleavedUntilErr(s *interleavedContinuationStream) error {
	for {
		_, err := s.Recv(context.Background())
		if err != nil {
			return err
		}
	}
}

func waitAssignedExecutorRecv(t *testing.T, s *interleavedContinuationStream, entered <-chan struct{}, done <-chan error) (thinkerID, execID string) {
	t.Helper()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("Recv finished before first executor Recv: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first executor Recv after assignment")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transitionInFlight {
		t.Fatal("transitionInFlight must be cleared before first executor Recv")
	}
	if s.phase != interleavedPhaseExecutor {
		t.Fatalf("phase = %v, want executor after assignment", s.phase)
	}
	if s.executor == nil {
		t.Fatal("executor continuation must be assigned before first Recv")
	}
	thinkerID = testAttemptSession(s.thinker).bleg.BLegID
	execID = testAttemptSession(s.executor).bleg.BLegID
	if thinkerID == "" || execID == "" {
		t.Fatalf("missing B-leg IDs thinker=%q executor=%q", thinkerID, execID)
	}
	return thinkerID, execID
}

func assertAssignedCancelCloseBilling(t *testing.T, auth *recordingAuthorityService, capture *abortJoinCapture, thinkerID, execID string) {
	t.Helper()

	calls, legs := capture.snapshot()
	if len(calls) != 1 {
		t.Fatalf("call-closure appends = %d, want exactly 1", len(calls))
	}
	wantIDs := map[string]bool{thinkerID: true, execID: true}
	gotIDs := map[string]bool{}
	for _, id := range calls[0].ExpectedBLegIDs {
		gotIDs[id] = true
	}
	for id := range wantIDs {
		if !gotIDs[id] {
			t.Fatalf("closure ExpectedBLegIDs = %#v, missing %q (thinker+executor required)", calls[0].ExpectedBLegIDs, id)
		}
	}
	if len(calls[0].ExpectedBLegIDs) < 2 {
		t.Fatalf("closure ExpectedBLegIDs = %#v, want at least thinker+executor", calls[0].ExpectedBLegIDs)
	}

	seen := map[string]bool{}
	for _, leg := range legs {
		seen[leg.BLegID] = true
	}
	for id := range wantIDs {
		if !seen[id] {
			t.Fatalf("terminal leg rows missing %q in %#v", id, legs)
		}
	}
	if _, err := usageevidence.JoinCompleteCall(calls[0], legs); err != nil {
		t.Fatalf("thinker+executor cancel/close path must remain joinable: %v", err)
	}

	if got := auth.releaseCalls.Load() + auth.settleCalls.Load(); got < 2 {
		t.Fatalf("release+settle calls = %d, want >= 2 (thinker and executor authority finalized)", got)
	}
}

// TestInterleavedCancel_AfterExecutorAssigned_BlocksFirstRecv proves Cancel after
// assignment (transitionInFlight already cleared) wakes the blocked first executor
// Recv and seals exactly one joinable call closure covering thinker+executor.
func TestInterleavedCancel_AfterExecutorAssigned_BlocksFirstRecv(t *testing.T) {
	t.Parallel()

	s, auth, capture, entered := setupInterleavedAssignedExecutorGate(t)

	done := make(chan error, 1)
	go func() { done <- drainInterleavedUntilErr(s) }()

	thinkerID, execID := waitAssignedExecutorRecv(t, s, entered, done)
	_ = s.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelClientGone})

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Recv must fail after Cancel during first executor Recv")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not return after Cancel during first executor Recv")
	}

	assertAssignedCancelCloseBilling(t, auth, capture, thinkerID, execID)
}

// TestInterleavedClose_AfterExecutorAssigned_BlocksFirstRecv proves Close after
// assignment seals exactly one joinable call closure covering thinker+executor.
func TestInterleavedClose_AfterExecutorAssigned_BlocksFirstRecv(t *testing.T) {
	t.Parallel()

	s, auth, capture, entered := setupInterleavedAssignedExecutorGate(t)

	done := make(chan error, 1)
	go func() { done <- drainInterleavedUntilErr(s) }()

	thinkerID, execID := waitAssignedExecutorRecv(t, s, entered, done)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Recv must fail after Close during first executor Recv")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not return after Close during first executor Recv")
	}

	assertAssignedCancelCloseBilling(t, auth, capture, thinkerID, execID)
}

func TestInterleavedLifecycle_ExactSingleExecution_AbortCancelClose(t *testing.T) {
	t.Parallel()

	t.Run("thinker_close_exact_once", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "res-thinker-close",
				ReservedAmount: authorityInputAmount(5),
			},
			status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
		capture := &abortJoinCapture{}
		ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")
		wireAbortBilling(ex, capture)
		callID, err := usageevidence.NewBillingCallID()
		if err != nil {
			t.Fatal(err)
		}
		from = withTestRecvFacts(from, func(f recvTurnFacts) recvTurnFacts {
			f.billingCallID = callID
			return f
		})
		from = stampStreamIdentity(from)
		installTestTurnTerminal(from)
		from.terminal.setInterleavedThinker()
		bindTurnTerminalRuntime(from.terminal, ex)
		testAttemptSession(from).authority = ex.newAttemptAuthorityLifecycle(attemptAuthorityState{
			admissionInput:  testAuthorityAdmissionInput(5),
			admissionResult: auth.admitResult,
		}, testAttemptSession(from).cand)

		s := newHiddenInterleavedStream(from, nil, interleavedstate.State{})
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		if got := auth.releaseCalls.Load() + auth.settleCalls.Load(); got != 1 {
			t.Fatalf("authority finalized count = %d, want 1", got)
		}
		calls, legs := capture.snapshot()
		if len(calls) != 1 {
			t.Fatalf("call closures = %d, want 1", len(calls))
		}
		if len(legs) != 1 {
			t.Fatalf("terminal legs = %d, want 1", len(legs))
		}
	})

	t.Run("thinker_cancel_exact_once", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "res-thinker-cancel",
				ReservedAmount: authorityInputAmount(5),
			},
			status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
		capture := &abortJoinCapture{}
		ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")
		wireAbortBilling(ex, capture)
		callID, err := usageevidence.NewBillingCallID()
		if err != nil {
			t.Fatal(err)
		}
		from = withTestRecvFacts(from, func(f recvTurnFacts) recvTurnFacts {
			f.billingCallID = callID
			return f
		})
		from = stampStreamIdentity(from)
		installTestTurnTerminal(from)
		from.terminal.setInterleavedThinker()
		bindTurnTerminalRuntime(from.terminal, ex)
		testAttemptSession(from).authority = ex.newAttemptAuthorityLifecycle(attemptAuthorityState{
			admissionInput:  testAuthorityAdmissionInput(5),
			admissionResult: auth.admitResult,
		}, testAttemptSession(from).cand)

		s := newHiddenInterleavedStream(from, nil, interleavedstate.State{})
		_ = s.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelClientGone})

		if got := auth.releaseCalls.Load() + auth.settleCalls.Load(); got != 1 {
			t.Fatalf("authority finalized count = %d, want 1", got)
		}
		calls, legs := capture.snapshot()
		if len(calls) != 1 {
			t.Fatalf("call closures = %d, want 1", len(calls))
		}
		if len(legs) != 1 {
			t.Fatalf("terminal legs = %d, want 1", len(legs))
		}
	})

	t.Run("abort_executor_handoff_exact_once", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "res-exec-abort",
				ReservedAmount: authorityInputAmount(9),
			},
			status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
		capture := &abortJoinCapture{}
		ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")
		wireAbortBilling(ex, capture)
		callID, err := usageevidence.NewBillingCallID()
		if err != nil {
			t.Fatal(err)
		}
		from = withTestRecvFacts(from, func(f recvTurnFacts) recvTurnFacts {
			f.billingCallID = callID
			return f
		})
		from = stampStreamIdentity(from)
		installTestTurnTerminal(from)
		from.terminal.setInterleavedThinker()
		bindTurnTerminalRuntime(from.terminal, ex)
		testAttemptSession(from).authority = ex.newAttemptAuthorityLifecycle(attemptAuthorityState{
			admissionInput: testAuthorityAdmissionInput(5),
			admissionResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "res-thinker-ok",
				ReservedAmount: authorityInputAmount(5),
			},
		}, testAttemptSession(from).cand)
		testStoreInner(from, aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseFinished},
		}))

		s := newHiddenInterleavedStream(from, nil, interleavedstate.State{})
		s.mu.Lock()
		s.finished = true
		s.mu.Unlock()

		_, err = s.beginExecutorContinuation(context.Background())
		if !errors.Is(err, io.EOF) {
			t.Fatalf("beginExecutorContinuation error = %v, want io.EOF", err)
		}

		thinkerID := testAttemptSession(s.thinker).bleg.BLegID
		if thinkerID == "" {
			t.Fatal("missing thinker B-leg ID")
		}

		calls, legs := capture.snapshot()
		if len(calls) != 1 {
			t.Fatalf("call closures = %d, want 1", len(calls))
		}
		if len(legs) != 2 {
			t.Fatalf("terminal legs = %d, want 2 (thinker+executor)", len(legs))
		}
		if _, err := usageevidence.JoinCompleteCall(calls[0], legs); err != nil {
			t.Fatalf("aborted handoff must yield joinable call closure: %v", err)
		}
		if got := auth.releaseCalls.Load() + auth.settleCalls.Load(); got < 2 {
			t.Fatalf("authority finalized = %d, want >= 2", got)
		}
	})
}
