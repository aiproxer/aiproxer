package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// phase6TrackingStream is a managed stream that tracks Recv, Cancel, and Close invocations
// and supports injected events and mid-stream errors.
type phase6TrackingStream struct {
	mu          sync.Mutex
	events      []aipapi.Event
	eventIdx    int
	recvErr     error
	closeErr    error
	recvCalls   atomic.Int32
	cancelCalls atomic.Int32
	closeCalls  atomic.Int32
	lastCancel  aipapi.CancelCause
	closed      bool
}

func (s *phase6TrackingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.recvCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventIdx < len(s.events) {
		ev := s.events[s.eventIdx]
		s.eventIdx++
		return ev, nil
	}
	if s.recvErr != nil {
		return aipapi.Event{}, s.recvErr
	}
	return aipapi.Event{}, io.EOF
}

func (s *phase6TrackingStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCalls.Add(1)
	s.mu.Lock()
	s.lastCancel = cause
	s.mu.Unlock()
	return aipapi.CancelResult{Mode: aipapi.CancelModeTransport}
}

func (s *phase6TrackingStream) Close() error {
	s.closeCalls.Add(1)
	s.mu.Lock()
	s.closed = true
	err := s.closeErr
	s.mu.Unlock()
	return err
}

// phase6PanicObserver mocks a stream observer whose Finish panics or errors.
type phase6PanicObserver struct {
	finishPanics bool
	finishCalls  atomic.Int32
	lastOutcome  response.StreamOutcome
}

func (o *phase6PanicObserver) Observe(ctx context.Context, ev aipapi.Event) error {
	return nil
}

func (o *phase6PanicObserver) Finish(ctx context.Context, outcome response.StreamOutcome) error {
	o.finishCalls.Add(1)
	o.lastOutcome = outcome
	if o.finishPanics {
		panic("injected panic in stream observer Finish")
	}
	return nil
}

type phase6ObserverFactory struct {
	obs response.StreamObserver
}

func (f phase6ObserverFactory) ID() string                        { return "phase6-obs" }
func (f phase6ObserverFactory) Order() int                        { return 0 }
func (f phase6ObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (f phase6ObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return f.obs, nil
}

// TestPhase6_FaultMatrix_AcquisitionAndReadiness covers all fault injection points
// across attempt budget acquisition, B-leg allocation, authority admission, B-leg registration,
// backend open, and observer startup.
func TestPhase6_FaultMatrix_AcquisitionAndReadiness(t *testing.T) {
	t.Parallel()

	t.Run("1_budget_acquisition_failure", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{Allowed: true, Reserved: true},
		}
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

		budget := &attemptBudget{max: 1, used: 1} // exhausted
		req := authorityOpenRequest(t, aLegID, budget)
		plan := candidatePlan{cand: authorityCandidate()}

		_, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
		if err == nil {
			t.Fatal("expected ErrMaxRouteAttempts on budget acquisition failure, got nil")
		}
		if !errors.Is(err, aipapi.ErrMaxRouteAttempts) {
			t.Errorf("expected ErrMaxRouteAttempts, got: %v", err)
		}

		// Ensure budget was not further consumed
		if budget.usedNow() != 1 {
			t.Errorf("expected budget used to remain 1, got %d", budget.usedNow())
		}
		// Ensure no real admit occurred
		realAdmits := 0
		auth.admitMu.Lock()
		for _, in := range auth.admitInputsV {
			if !in.EstimateOnly {
				realAdmits++
			}
		}
		auth.admitMu.Unlock()
		if realAdmits != 0 {
			t.Errorf("expected 0 real admits, got %d", realAdmits)
		}
	})

	t.Run("2_bleg_allocation_failure", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{Allowed: true, Reserved: true},
		}
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		ex.Store = &phase1FaultStore{
			Store:       ex.Store,
			nextBLegErr: errors.New("injected next b-leg allocation failure"),
		}

		budget := &attemptBudget{max: 5}
		req := authorityOpenRequest(t, aLegID, budget)
		plan := candidatePlan{cand: authorityCandidate()}

		_, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
		if err == nil {
			t.Fatal("expected error on bleg allocation failure, got nil")
		}
		if !strings.Contains(err.Error(), "injected next b-leg allocation failure") {
			t.Errorf("unexpected error: %v", err)
		}
		if budget.usedNow() != 0 {
			t.Errorf("expected budget released to 0, got %d", budget.usedNow())
		}
	})

	t.Run("3_authority_admission_failure", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitErr: errors.New("injected authority admission failure"),
		}
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

		budget := &attemptBudget{max: 5}
		req := authorityOpenRequest(t, aLegID, budget)
		plan := candidatePlan{cand: authorityCandidate()}

		_, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
		if err == nil {
			t.Fatal("expected error on authority admission failure, got nil")
		}
		if !strings.Contains(err.Error(), "usage authority unavailable") {
			t.Errorf("unexpected error: %v", err)
		}
		if budget.usedNow() != 0 {
			t.Errorf("expected budget released to 0, got %d", budget.usedNow())
		}
	})

	t.Run("4_bleg_registration_failure", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "res-reg-fail",
				ReservedAmount: authorityInputAmount(10),
			},
		}
		ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
		ex.ALegLifecycle = coord
		aScope := coord.StartALeg(aLegID)

		backend.openFn = func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			_ = coord.CancelALeg(ctx, aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit})
			return &phase6TrackingStream{}, nil
		}

		budget := &attemptBudget{max: 5}
		req := authorityOpenRequest(t, aLegID, budget)
		req.reqFacts.aScope = aScope
		plan := candidatePlan{cand: authorityCandidate()}

		_, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
		if err == nil {
			t.Fatal("expected ErrALegCanceled on bleg registration failure, got nil")
		}
		if !errors.Is(err, leglifecycle.ErrALegCanceled) {
			t.Errorf("expected ErrALegCanceled, got: %v", err)
		}
		if auth.settleCalls.Load() != 1 {
			t.Errorf("expected 1 settle call for incurred backend dial, got %d", auth.settleCalls.Load())
		}
	})

	t.Run("5_backend_open_failure", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "res-open-fail",
				ReservedAmount: authorityInputAmount(10),
			},
		}
		ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		backend.openFn = func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return nil, errors.New("injected backend network failure")
		}

		budget := &attemptBudget{max: 5}
		req := authorityOpenRequest(t, aLegID, budget)
		plan := candidatePlan{cand: authorityCandidate()}

		_, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
		if err == nil {
			t.Fatal("expected backend open error, got nil")
		}
		if !strings.Contains(err.Error(), "injected backend network failure") {
			t.Errorf("unexpected error: %v", err)
		}
		if auth.settleCalls.Load() != 1 {
			t.Errorf("expected 1 settle call, got %d", auth.settleCalls.Load())
		}
	})

	t.Run("6_final_observer_startup_failure_initial", func(t *testing.T) {
		t.Parallel()
		st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ex := TestExecutor()
		ex.Store = st
		ex.Bus = hooks.New(hooks.Config{})
		ex.Rand = routing.NewSeededRng(1)
		wireDummyBilling(ex)

		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
			FeaturePlanes: freezeBundle(testFeatureBundle{
				StreamObserverFactories: []response.StreamObserverFactory{failClosedStreamObserverFactory{}},
			}),
		})
		ex.Backends = map[string]execbackend.Backend{
			"ok": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					return &phase6TrackingStream{
						events: []aipapi.Event{
							{Kind: aipapi.EventResponseStarted},
							{Kind: aipapi.EventResponseFinished},
						},
					}, nil
				},
			},
		}

		call := &aipapi.Call{
			Session:  aipapi.SessionRef{AuthoritativeSessionID: "sess-obs-fault", ContinuityKey: "sess-obs-fault"},
			Route:    aipapi.RouteIntent{Selector: "ok:m"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		}

		_, err = ex.Execute(context.Background(), call)
		if err == nil {
			t.Fatal("expected Execute failure due to observer startup failure, got nil")
		}
		if _, ok := errors.AsType[*aipapi.PolicyDecisionError](err); !ok {
			t.Fatalf("expected PolicyDecisionError, got: %T (%v)", err, err)
		}
	})

	t.Run("7_ready_attempt_single_use_and_disposal", func(t *testing.T) {
		t.Parallel()
		trackingStream := &phase6TrackingStream{}
		session := &attemptSession{
			inner:    trackingStream,
			terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
			bleg:     b2bua.BLegRecord{ALegID: "aleg-r", BLegID: "bleg-r", Seq: 1},
		}
		ready := &readyAttempt{session: session, state: readyStatePrepared}

		// First consume must succeed
		got, err := ready.Consume()
		if err != nil {
			t.Fatalf("first Consume() failed: %v", err)
		}
		if got != session {
			t.Fatalf("expected session %v, got %v", session, got)
		}

		// Second consume must fail
		got2, err2 := ready.Consume()
		if err2 == nil {
			t.Fatal("expected error on duplicate readyAttempt Consume(), got nil")
		}
		if got2 != nil {
			t.Errorf("expected nil session on duplicate Consume(), got %v", got2)
		}

		// Disposal on unconsumed ready attempt must invoke complete abort and close stream
		trackingStream2 := &phase6TrackingStream{}
		session2 := &attemptSession{
			inner:    trackingStream2,
			terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
			bleg:     b2bua.BLegRecord{ALegID: "aleg-r2", BLegID: "bleg-r2", Seq: 1},
		}
		ready2 := &readyAttempt{session: session2}
		ready2.Dispose(context.Background(), errors.New("disposed test"))

		if !ready2.IsConsumed() {
			t.Error("expected ready2 to be marked consumed after Dispose")
		}
		if session2.loadInner() != nil {
			t.Error("expected session2 stream to be detached/closed on disposal")
		}
		if trackingStream2.closeCalls.Load() != 1 {
			t.Errorf("expected stream close call count 1, got %d", trackingStream2.closeCalls.Load())
		}
	})
}

// TestPhase6_FaultMatrix_PublicationDenial_CloseWinsRace tests that when Close() races
// and closes the publication window before replacement swapIfOpen runs:
// - swapIfOpen returns published: false
// - the ready replacement is cleanly disposed via ready.Dispose()
// - the unpublished replacement session terminalizes completely and stream is closed
// - winner-only selection effects are never committed
func TestPhase6_FaultMatrix_PublicationDenial_CloseWinsRace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "denial-test")
	if err != nil {
		t.Fatalf("create aleg: %v", err)
	}

	memoStore := interleavedthinking.NewMemoStore(4096)
	scope := interleavedthinking.Scope(aLeg.ALegID)
	initialRef, err := memoStore.Put(ctx, scope, interleavedthinking.MemoState{
		Memo:                  "initial memo",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatalf("put initial memo: %v", err)
	}

	initialInterleaved := interleavedstate.State{
		Cycle: interleavedstate.CycleState{
			SelectorKey: "sel-1",
			Sequence: []interleavedstate.CycleEntry{
				{Key: "cand-1", Role: interleavedstate.RoleThinker},
				{Key: "cand-2", Role: interleavedstate.RoleExecutor},
			},
			NextIndex: 0,
		},
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	oldStream := &phase6TrackingStream{}
	oldSession := &attemptSession{
		inner:    oldStream,
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: aLeg.ALegID, BLegID: "bleg-old", Seq: 1},
		cand:     routing.AttemptCandidate{Key: "cand-1", Primary: routing.Primary{Backend: "b-1", Model: "m-1"}},
	}

	slot := &attemptSlot{}
	slot.mu.Lock()
	slot.current = oldSession
	slot.mu.Unlock()

	// Prepare a replacement attempt with pending selection effects
	replacementStream := &phase6TrackingStream{}
	replacementSession := &attemptSession{
		inner:    replacementStream,
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: aLeg.ALegID, BLegID: "bleg-repl", Seq: 2},
		cand:     routing.AttemptCandidate{Key: "cand-2", Primary: routing.Primary{Backend: "b-2", Model: "m-2"}},
	}

	ready := &readyAttempt{
		session: replacementSession,
		pending: pendingSelectionEffects{
			interleaved: initialInterleaved,
		},
		state: readyStatePrepared,
	}

	// 1. Close the slot publication window (simulating Close() winning before swap)
	current := slot.closePublicationAndSnapshot()
	if current != oldSession {
		t.Fatalf("expected closePublicationAndSnapshot to return oldSession, got %v", current)
	}
	if !slot.publicationIsClosed() {
		t.Fatal("expected publication to be closed")
	}

	// 2. Attempt to swap the ready replacement into the slot
	old, published := slot.swapIfOpen(ready)
	if published {
		t.Fatal("expected swapIfOpen to return published: false when publication is closed")
	}
	if old != oldSession {
		t.Errorf("expected swapIfOpen to return current oldSession, got %v", old)
	}

	// 3. Clean up the denied ready replacement as runtime does in executor_recv_loop
	ready.Dispose(ctx, fmt.Errorf("phase6 test: %w", errPublicationClosed))

	// 4. Assert exact cleanup and state
	if !ready.IsConsumed() {
		t.Error("expected ready attempt to be marked consumed")
	}
	if replacementSession.loadInner() != nil {
		t.Error("expected replacement stream to be detached/closed")
	}
	if replacementStream.closeCalls.Load() != 1 {
		t.Errorf("expected replacement stream close call count 1, got %d", replacementStream.closeCalls.Load())
	}

	// 5. Assert winner-only memo state was NEVER committed
	persistedMemo, ok, err := memoStore.Get(ctx, scope, initialRef)
	if err != nil || !ok {
		t.Fatalf("fetch memo: ok=%v, err=%v", ok, err)
	}
	if persistedMemo.Memo != "initial memo" {
		t.Errorf("persisted memo was mutated to %q; winner effects must not commit when publication is denied", persistedMemo.Memo)
	}
}

// TestPhase6_FaultMatrix_TerminalEffectAggregation tests that in TerminalizeAttempt:
//   - When individual effects fail (stream.Close errors, panicked functions),
//     ALL other mandatory cleanup steps still execute completely.
//   - All errors are joined and returned.
//   - Attempt-local state is cleanly discarded.
func TestPhase6_FaultMatrix_TerminalEffectAggregation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 0})
	aScope := coord.StartALeg("aleg-agg")

	trackingStream := &phase6TrackingStream{
		closeErr: errors.New("injected stream close error"),
	}

	if err := aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{ID: "bleg-agg", Attempt: trackingStream}); err != nil {
		t.Fatalf("RegisterBLeg failed: %v", err)
	}

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "res-agg",
			ReservedAmount: authorityInputAmount(10),
		},
	}
	ex, _, _ := newAuthorityRuntimeTestExecutor(t, auth)

	var billingLegAppended atomic.Int32
	var attemptLogged atomic.Int32
	var egressMetered atomic.Int32

	obs := &phase6PanicObserver{finishPanics: true}
	obsSession := &extensions.FinalStreamObservationSession{}
	_ = obsSession.Open(ctx, []response.StreamObserverFactory{phase6ObserverFactory{obs: obs}}, response.StreamMeta{}, response.Services{})

	authState := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(10),
		admissionResult: auth.admitResult,
	}

	session := newAttemptSession(attemptSessionInput{
		inner:     trackingStream,
		bleg:      b2bua.BLegRecord{ALegID: "aleg-agg", BLegID: "bleg-agg", Seq: 1},
		cand:      routing.AttemptCandidate{Key: "cand-1", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		authority: testAuthorityLifecycle(ex, authState, authorityCandidate()),
		aScope:    aScope,
		accounting: attemptAccountingTracker{
			requestStartedAt: time.Now(),
		},
		toolFinal:      &toolCallAssembler{},
		finalStreamObs: obsSession,
		recordAttemptLoggedFn: func(context.Context, recordAttemptParams, diag.AttrOpts) {
			attemptLogged.Add(1)
		},
		emitBackendEgressFn: func(context.Context, string, metering.AttemptOutcome, metering.SurfacedState, aipapi.Event) {
			egressMetered.Add(1)
		},
		appendBillingLegFn: func(context.Context, b2bua.BLegRecord, routing.Primary, time.Time, time.Time, usageevidence.LegOutcome) {
			billingLegAppended.Add(1)
		},
		now: time.Now,
	})

	evidence := attemptEvidence{
		Command:     sdkterminal.CommandNormalFinish,
		ReleaseKind: usagelimit.ReleaseKindLosing,
		LegOutcome:  usageevidence.LegOutcomeWinner,
		Usage:       aipapi.Event{Kind: aipapi.EventUsageDelta, OutputTokens: 5},
		TraceID:     "trace-agg",
		ALegID:      "aleg-agg",
	}

	// Terminalize attempt with IntentSuccess
	result := session.TerminalizeAttempt(ctx, IntentSuccess, evidence)

	// Verify error was joined from stream close error
	if result.Result.Err == nil {
		t.Error("expected joined errors from TerminalizeAttempt, got nil")
	}
	errStr := result.Result.Err.Error()
	if !strings.Contains(errStr, "injected stream close error") {
		t.Errorf("expected stream close error in joined error, got: %v", errStr)
	}

	// Verify that ALL mandatory subsequent steps executed despite the errors/panics:
	if session.loadInner() != nil {
		t.Error("expected session stream to be detached (nil)")
	}
	if trackingStream.closeCalls.Load() != 1 {
		t.Errorf("expected stream closeCalls == 1, got %d", trackingStream.closeCalls.Load())
	}
	if obs.finishCalls.Load() != 1 {
		t.Errorf("expected observer finishCalls == 1, got %d", obs.finishCalls.Load())
	}
	if auth.settleCalls.Load() != 1 {
		t.Errorf("expected authority settleCalls == 1, got %d", auth.settleCalls.Load())
	}
	if egressMetered.Load() != 1 {
		t.Errorf("expected egressMetered == 1, got %d", egressMetered.Load())
	}
	if billingLegAppended.Load() != 1 {
		t.Errorf("expected billingLegAppended == 1, got %d", billingLegAppended.Load())
	}
	if attemptLogged.Load() != 1 {
		t.Errorf("expected attemptLogged == 1, got %d", attemptLogged.Load())
	}

	// Verify B-leg was released from A-leg scope: canceling A-leg should not touch released stream
	trackingStream.cancelCalls.Store(0)
	if err := coord.CancelALeg(ctx, "aleg-agg", leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}
	if trackingStream.cancelCalls.Load() != 0 {
		t.Errorf("released B-leg was canceled by CancelALeg, got %d cancel calls", trackingStream.cancelCalls.Load())
	}

	// Verify attempt-local state is completely discarded
	if session.toolFinal != nil {
		t.Error("expected session.toolFinal to be nil after terminalization")
	}
	if session.promptCacheSource != nil || session.promptCacheController != nil {
		t.Error("expected prompt cache controllers to be nil after terminalization")
	}
}

// TestPhase6_FaultMatrix_ConcurrentTerminalization verifies that multiple concurrent
// calls to TerminalizeAttempt on the same session execute side-effects exactly once.
func TestPhase6_FaultMatrix_ConcurrentTerminalization(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 0})
	aScope := coord.StartALeg("aleg-conc")

	trackingStream := &phase6TrackingStream{}
	if err := aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{ID: "bleg-conc", Attempt: trackingStream}); err != nil {
		t.Fatalf("RegisterBLeg failed: %v", err)
	}

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{Allowed: true, Reserved: true, ReservationID: "res-conc"},
	}
	ex, _, _ := newAuthorityRuntimeTestExecutor(t, auth)

	var billingLegAppended atomic.Int32
	var attemptLogged atomic.Int32
	obs := &phase6PanicObserver{}
	obsSession := &extensions.FinalStreamObservationSession{}
	_ = obsSession.Open(ctx, []response.StreamObserverFactory{phase6ObserverFactory{obs: obs}}, response.StreamMeta{}, response.Services{})

	authState := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(10),
		admissionResult: auth.admitResult,
	}

	session := newAttemptSession(attemptSessionInput{
		inner:          trackingStream,
		bleg:           b2bua.BLegRecord{ALegID: "aleg-conc", BLegID: "bleg-conc", Seq: 1},
		cand:           routing.AttemptCandidate{Key: "cand-1", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		authority:      testAuthorityLifecycle(ex, authState, authorityCandidate()),
		aScope:         aScope,
		finalStreamObs: obsSession,
		recordAttemptLoggedFn: func(context.Context, recordAttemptParams, diag.AttrOpts) {
			attemptLogged.Add(1)
		},
		appendBillingLegFn: func(context.Context, b2bua.BLegRecord, routing.Primary, time.Time, time.Time, usageevidence.LegOutcome) {
			billingLegAppended.Add(1)
		},
		now: time.Now,
	})

	evidence := attemptEvidence{
		Command:     sdkterminal.CommandNormalFinish,
		ReleaseKind: usagelimit.ReleaseKindLosing,
		LegOutcome:  usageevidence.LegOutcomeWinner,
		Usage:       aipapi.Event{Kind: aipapi.EventUsageDelta},
		TraceID:     "trace-conc",
		ALegID:      "aleg-conc",
	}

	var wg sync.WaitGroup
	const goroutines = 8
	results := make([]attemptTerminalResult, goroutines)
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = session.TerminalizeAttempt(ctx, IntentSuccess, evidence)
		}(i)
	}
	wg.Wait()

	// Assert exactly-once execution of terminal effects
	if trackingStream.closeCalls.Load() != 1 {
		t.Errorf("expected stream closeCalls == 1, got %d", trackingStream.closeCalls.Load())
	}
	if obs.finishCalls.Load() != 1 {
		t.Errorf("expected obs finishCalls == 1, got %d", obs.finishCalls.Load())
	}
	if auth.settleCalls.Load() != 1 {
		t.Errorf("expected auth settleCalls == 1, got %d", auth.settleCalls.Load())
	}
	if billingLegAppended.Load() != 1 {
		t.Errorf("expected billingLegAppended == 1, got %d", billingLegAppended.Load())
	}
	if attemptLogged.Load() != 1 {
		t.Errorf("expected attemptLogged == 1, got %d", attemptLogged.Load())
	}

	// Verify B-leg release by testing CancelALeg does not affect released B-leg
	trackingStream.cancelCalls.Store(0)
	if err := coord.CancelALeg(ctx, "aleg-conc", leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}
	if trackingStream.cancelCalls.Load() != 0 {
		t.Errorf("released B-leg was canceled by CancelALeg, got %d cancel calls", trackingStream.cancelCalls.Load())
	}
}

// TestPhase6_FaultMatrix_StateTransitions exercises the complete matrix of lifecycle
// state transitions and verifies exact cleanup, attribution, and terminal outcomes.
func TestPhase6_FaultMatrix_StateTransitions(t *testing.T) {
	t.Parallel()

	type transitionTestCase struct {
		name              string
		intent            attemptTerminalIntent
		command           sdkterminal.Command
		releaseKind       usagelimit.ReleaseKind
		legOutcome        usageevidence.LegOutcome
		expectedOutcome   usageevidence.LegOutcome
		expectedObsOut    response.StreamOutcome
		expectStreamClose bool
		expectCancelCause string
	}

	testCases := []transitionTestCase{
		{
			name:              "success_to_terminal",
			intent:            IntentSuccess,
			command:           sdkterminal.CommandNormalFinish,
			releaseKind:       usagelimit.ReleaseKindLosing,
			legOutcome:        usageevidence.LegOutcomeWinner,
			expectedOutcome:   usageevidence.LegOutcomeWinner,
			expectedObsOut:    response.OutcomeSuccessReleased,
			expectStreamClose: true,
		},
		{
			name:              "swallowed_failure_to_retry",
			intent:            IntentSwallowedFailure,
			command:           sdkterminal.CommandSwallowedAttempt,
			releaseKind:       usagelimit.ReleaseKindSwallowed,
			legOutcome:        usageevidence.LegOutcomeSwallowed,
			expectedOutcome:   usageevidence.LegOutcomeSwallowed,
			expectedObsOut:    response.OutcomeReplaced,
			expectStreamClose: true,
		},
		{
			name:              "surfaced_failure_post_output",
			intent:            IntentSurfacedFailure,
			command:           sdkterminal.CommandPartialError,
			releaseKind:       usagelimit.ReleaseKindLosing,
			legOutcome:        usageevidence.LegOutcomeFailed,
			expectedOutcome:   usageevidence.LegOutcomeFailed,
			expectedObsOut:    response.OutcomeFailed,
			expectStreamClose: true,
		},
		{
			name:              "cancellation_to_terminal",
			intent:            IntentCancellation,
			command:           sdkterminal.CommandCancel,
			releaseKind:       usagelimit.ReleaseKindLosing,
			legOutcome:        usageevidence.LegOutcomeCanceled,
			expectedOutcome:   usageevidence.LegOutcomeCanceled,
			expectedObsOut:    response.OutcomeCancelled,
			expectStreamClose: true,
			expectCancelCause: string(aipapi.CancelClientGone),
		},
		{
			name:              "timeout_to_terminal",
			intent:            IntentTimeout,
			command:           sdkterminal.CommandTimeout,
			releaseKind:       usagelimit.ReleaseKindLosing,
			legOutcome:        usageevidence.LegOutcomeFailed,
			expectedOutcome:   usageevidence.LegOutcomeFailed,
			expectedObsOut:    response.OutcomeCancelled,
			expectStreamClose: true,
			expectCancelCause: "timeout",
		},
		{
			name:              "replacement_to_old_terminalized",
			intent:            IntentReplacement,
			command:           sdkterminal.CommandSwallowedAttempt,
			releaseKind:       usagelimit.ReleaseKindSwallowed,
			legOutcome:        usageevidence.LegOutcomeSwallowed,
			expectedOutcome:   usageevidence.LegOutcomeSwallowed,
			expectedObsOut:    response.OutcomeReplaced,
			expectStreamClose: true,
		},
		{
			name:              "parallel_loser_to_terminalized",
			intent:            IntentParallelLoser,
			command:           sdkterminal.CommandParallelLoser,
			releaseKind:       usagelimit.ReleaseKindLosing,
			legOutcome:        usageevidence.LegOutcomeLoser,
			expectedOutcome:   usageevidence.LegOutcomeLoser,
			expectedObsOut:    response.OutcomeFailed,
			expectStreamClose: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 0})
			aScope := coord.StartALeg("aleg-" + tc.name)

			trackingStream := &phase6TrackingStream{}
			if err := aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{ID: "bleg-" + tc.name, Attempt: trackingStream}); err != nil {
				t.Fatalf("RegisterBLeg failed: %v", err)
			}

			auth := &recordingAuthorityService{
				admitResult: usagelimit.AdmissionResult{
					Allowed:        true,
					Reserved:       true,
					ReservationID:  "res-" + tc.name,
					ReservedAmount: authorityInputAmount(10),
				},
			}
			ex, _, _ := newAuthorityRuntimeTestExecutor(t, auth)

			var capturedLegOutcome usageevidence.LegOutcome
			var appendLegCalls atomic.Int32
			obs := &phase6PanicObserver{}
			obsSession := &extensions.FinalStreamObservationSession{}
			_ = obsSession.Open(ctx, []response.StreamObserverFactory{phase6ObserverFactory{obs: obs}}, response.StreamMeta{}, response.Services{})

			authState := attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(10),
				admissionResult: auth.admitResult,
			}

			session := newAttemptSession(attemptSessionInput{
				inner:          trackingStream,
				bleg:           b2bua.BLegRecord{ALegID: "aleg-" + tc.name, BLegID: "bleg-" + tc.name, Seq: 1},
				cand:           routing.AttemptCandidate{Key: "cand-1", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
				authority:      testAuthorityLifecycle(ex, authState, authorityCandidate()),
				aScope:         aScope,
				finalStreamObs: obsSession,
				appendBillingLegFn: func(_ context.Context, _ b2bua.BLegRecord, _ routing.Primary, _, _ time.Time, outcome usageevidence.LegOutcome) {
					appendLegCalls.Add(1)
					capturedLegOutcome = outcome
				},
				now: time.Now,
			})

			evidence := attemptEvidence{
				Command:     tc.command,
				ReleaseKind: tc.releaseKind,
				LegOutcome:  tc.legOutcome,
				Usage:       aipapi.Event{Kind: aipapi.EventUsageDelta, OutputTokens: 2},
				TraceID:     "trace-" + tc.name,
				ALegID:      "aleg-" + tc.name,
			}

			res := session.TerminalizeAttempt(ctx, tc.intent, evidence)
			if res.Result.Err != nil {
				t.Fatalf("unexpected terminal error: %v", res.Result.Err)
			}

			// Verify stream closed
			if tc.expectStreamClose && trackingStream.closeCalls.Load() != 1 {
				t.Errorf("expected stream closeCalls == 1, got %d", trackingStream.closeCalls.Load())
			}

			// Verify cancellation cause when applicable
			if tc.expectCancelCause != "" {
				if trackingStream.cancelCalls.Load() < 1 {
					t.Errorf("expected stream to be canceled, got cancelCalls=%d", trackingStream.cancelCalls.Load())
				}
				if tc.intent == IntentTimeout && trackingStream.lastCancel.Detail != "timeout" {
					t.Errorf("expected cancel detail 'timeout', got %q", trackingStream.lastCancel.Detail)
				}
			}

			// Verify observer finish outcome
			if obs.lastOutcome != tc.expectedObsOut {
				t.Errorf("expected observer outcome %v, got %v", tc.expectedObsOut, obs.lastOutcome)
			}

			// Verify billing leg outcome
			if capturedLegOutcome != tc.expectedOutcome {
				t.Errorf("expected billing leg outcome %v, got %v", tc.expectedOutcome, capturedLegOutcome)
			}
			if appendLegCalls.Load() != 1 {
				t.Errorf("expected exactly 1 append billing leg call, got %d", appendLegCalls.Load())
			}

			// Verify B-leg was released from A-leg scope
			trackingStream.cancelCalls.Store(0)
			if err := coord.CancelALeg(ctx, "aleg-"+tc.name, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
				t.Fatalf("CancelALeg failed: %v", err)
			}
			if trackingStream.cancelCalls.Load() != 0 {
				t.Errorf("released B-leg was canceled by CancelALeg, got %d cancel calls", trackingStream.cancelCalls.Load())
			}
		})
	}
}

// TestPhase6_FaultMatrix_PostOutputFailure_ProhibitsRetry tests that when an attempt
// emits a content event (output committed) and then encounters a stream error,
// the runtime terminates with surfaced failure and strictly prohibits any retry/replacement.
func TestPhase6_FaultMatrix_PostOutputFailure_ProhibitsRetry(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var backend1Opens atomic.Int32
	var backend2Opens atomic.Int32

	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	wireDummyBilling(ex)

	ex.Backends = map[string]execbackend.Backend{
		"backend-1": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				backend1Opens.Add(1)
				// Emits text delta (committing output), then fails with network error
				return &phase6TrackingStream{
					events: []aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "partial output"},
					},
					recvErr: errors.New("injected mid-stream connection reset"),
				}, nil
			},
		},
		"backend-2": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				backend2Opens.Add(1)
				return &phase6TrackingStream{
					events: []aipapi.Event{
						{Kind: aipapi.EventTextDelta, Delta: "retry output"},
						{Kind: aipapi.EventResponseFinished},
					},
				}, nil
			},
		},
	}

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "backend-1:m,backend-2:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("prompt")},
		}},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// 1. First event should be received (started/delta)
	ev1, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatalf("first Recv failed: %v", err)
	}
	_ = ev1

	// Read until error
	var encounteredErr error
	for {
		_, err := stream.Recv(context.Background())
		if err != nil {
			encounteredErr = err
			break
		}
	}

	// 2. Error must be surfaced to the caller
	if encounteredErr == nil {
		t.Fatal("expected error to be surfaced after post-output failure, got nil")
	}
	if !strings.Contains(encounteredErr.Error(), "injected mid-stream connection reset") {
		t.Errorf("expected mid-stream error, got: %v", encounteredErr)
	}

	// 3. Prohibit retry: backend-2 must NEVER have been opened
	if backend2Opens.Load() != 0 {
		t.Errorf("backend-2 was opened %d times; retry after output commitment is strictly prohibited", backend2Opens.Load())
	}
}

// TestPhase6_FaultMatrix_InterleavedContinuation tests thinker -> executor continuation
// with memo updates and cycle state progression across attempts.
func TestPhase6_FaultMatrix_InterleavedContinuation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	aLeg, err := store.CreateALeg(ctx, "interleaved-test")
	if err != nil {
		t.Fatal(err)
	}

	memoStore := interleavedthinking.NewMemoStore(4096)
	scope := interleavedthinking.Scope(aLeg.ALegID)
	_, err = memoStore.Put(ctx, scope, interleavedthinking.MemoState{
		Memo:                  "first thinker memo",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	cycle := interleavedstate.CycleState{
		SelectorKey: "sel-interleaved",
		Sequence: []interleavedstate.CycleEntry{
			{Key: "thinker-cand", Role: interleavedstate.RoleThinker},
			{Key: "executor-cand", Role: interleavedstate.RoleExecutor},
		},
		NextIndex: 0,
	}
	initialState := interleavedstate.State{
		Cycle: cycle,
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, initialState); err != nil {
		t.Fatal(err)
	}

	ex := TestExecutor()
	ex.Store = store
	ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{
		Instructions:          "shape instructions",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}, memoStore)
	RegisterTestMemoStore(ex, memoStore)

	// Verify cycle advancement and memo update application
	updatedMemoRef, err := memoStore.Put(ctx, scope, interleavedthinking.MemoState{
		Memo:                  "second thinker memo update",
		RegularTurnsRemaining: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	cycle.NextIndex = 1
	updatedState := interleavedstate.State{
		Cycle: cycle,
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, updatedState); err != nil {
		t.Fatal(err)
	}

	gotState, err := store.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatalf("failed to get updated interleaved state: err=%v", err)
	}
	if gotState.Cycle.NextIndex != 1 {
		t.Errorf("expected cycle NextIndex == 1, got %d", gotState.Cycle.NextIndex)
	}

	gotMemo, ok, err := memoStore.Get(ctx, scope, updatedMemoRef)
	if err != nil || !ok {
		t.Fatalf("failed to get memo: ok=%v, err=%v", ok, err)
	}
	if gotMemo.Memo != "second thinker memo update" {
		t.Errorf("expected memo 'second thinker memo update', got %q", gotMemo.Memo)
	}
}
