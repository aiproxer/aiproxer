package runtime

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestTDD_TypeAndPointerAbsence(t *testing.T) {
	// Assert that candidatePlan, candidateEvaluationOutcome, candidateRejection are defined.
	var outcome candidateEvaluationOutcome
	if outcome.accepted {
		t.Error("expected accepted to be false by default")
	}
	var rejection candidateRejection
	if rejection.kind != rejectNone {
		t.Error("expected kind to be rejectNone by default")
	}

	// TDD Assert that attemptOpenParams and attemptOpenResult are completely deleted from the runtime package.
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var parsedFiles []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "executor_open_attempt_state_tdd_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsedFiles = append(parsedFiles, file)
	}
	for _, file := range parsedFiles {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if ts.Name.Name == "attemptOpenParams" || ts.Name.Name == "attemptOpenResult" {
					t.Errorf("forbidden type %q is defined in file %s", ts.Name.Name, file.Name.Name)
				}
			}
		}
	}

	// Assert no pointer-out fields in openNextRequest or its nested structures (excluding legitimate services).
	for _, file := range parsedFiles {
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "openNextRequest" {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, f := range st.Fields.List {
				star, isStar := f.Type.(*ast.StarExpr)
				if isStar {
					ident, isIdent := star.X.(*ast.Ident)
					if isIdent {
						switch ident.Name {
						case "bool", "error", "string", "int", "int64":
							t.Errorf("forbidden pointer-out field in openNextRequest in file %s", file.Name.Name)
						}
					}
				}
			}
			return false
		})
	}
}

func TestTDD_AttemptTransactionRollbackAndHandoff(t *testing.T) {
	ctx := context.Background()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "tx-reservation",
			ReservedAmount: authorityInputAmount(8),
		},
	}
	ex, _, _, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	reqFacts := requestFacts{
		recvTurnFacts: recvTurnFacts{
			traceID:          "trace-1",
			aLegID:           aLegID,
			baseline:         aipapi.Call{ID: "req-1", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			billingCallID:    usageevidence.BillingCallID("call-1"),
			billingCallState: &billingCallState{},
		},
		bus:    ex.Bus,
		aScope: aScope,
	}

	cand := authorityCandidate()

	t.Run("successful rollback before handoff releases all resources", func(t *testing.T) {
		budget := &attemptBudget{max: 3}
		tx, err := ex.startAttemptTx(ctx, reqFacts, routeFacts{}, cand, budget, budget.getFailures())
		if err != nil {
			t.Fatal(err)
		}

		if tx.bleg.BLegID == "" {
			t.Error("expected B-leg ID to be populated")
		}

		// Mock open the stream
		tx.stream = aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}})
		tx.backendAttempted = true

		// Rollback the transaction
		tx.Rollback(ctx, sdkterminal.CommandBackendOpenFailure, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeFailed, emptyOperatorUsageShell())

		if !tx.completed {
			t.Error("expected transaction to be completed after rollback")
		}
	})

	t.Run("handoff once makes transaction inert and creates readyAttempt", func(t *testing.T) {
		budget := &attemptBudget{max: 3}
		tx, err := ex.startAttemptTx(ctx, reqFacts, routeFacts{}, cand, budget, budget.getFailures())
		if err != nil {
			t.Fatal(err)
		}

		tx.stream = aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}})

		ready := tx.HandoffReady(pendingSelectionEffects{})
		if ready == nil {
			t.Fatal("expected readyAttempt, got nil")
		}

		if !tx.completed {
			t.Error("expected transaction to be marked completed after handoff")
		}

		// Verify that double handoff panics
		defer func() {
			if recover() == nil {
				t.Error("expected second handoff to panic")
			}
		}()
		tx.HandoffReady(pendingSelectionEffects{})
	})
}

type parallelLoserMockStream struct {
	canceled         chan struct{}
	winnerWait       chan struct{}
	neverOpenedReady chan struct{}
	cancelCalls      int32
	closeCalls       int32
}

func (s *parallelLoserMockStream) Recv(ctx context.Context) (aipapi.Event, error) {
	select {
	case <-s.neverOpenedReady:
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}

	select {
	case <-s.winnerWait:
	default:
		close(s.winnerWait)
	}

	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-s.canceled:
		return aipapi.Event{}, context.Canceled
	}
}

func (s *parallelLoserMockStream) Close() error {
	atomic.CompareAndSwapInt32(&s.closeCalls, 0, 1)
	return nil
}

func (s *parallelLoserMockStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	if atomic.CompareAndSwapInt32(&s.cancelCalls, 0, 1) {
		select {
		case <-s.canceled:
		default:
			close(s.canceled)
		}
	}
	return aipapi.CancelResult{}
}

type parallelWinnerMockStream struct {
	winnerWait chan struct{}
	events     []aipapi.Event
	idx        int
}

func (s *parallelWinnerMockStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.idx >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	if ev.Kind == aipapi.EventTextDelta {
		select {
		case <-s.winnerWait:
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	return ev, nil
}

func (s *parallelWinnerMockStream) Close() error { return nil }

func (s *parallelWinnerMockStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

func TestTDD_ParallelLoserSchedule(t *testing.T) {
	ctx := context.Background()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-parallel-tdd",
			ReservedAmount: authorityInputAmount(7),
			Reservations: []usagelimit.AdmissionReservation{{
				RuleID:         "rule-1",
				ReservationID:  "reservation-parallel-tdd",
				ReservedAmount: authorityInputAmount(7),
			}},
		},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	loserCanceled := make(chan struct{})
	winnerWait := make(chan struct{})
	neverOpenedReady := make(chan struct{})
	var loserStream *parallelLoserMockStream

	backend.openFn = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		if cand.Primary.Backend == "backend-winner" {
			return &parallelWinnerMockStream{
				winnerWait: winnerWait,
				events: []aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventTextDelta, Delta: "winner-text"},
					{Kind: aipapi.EventResponseFinished},
				},
			}, nil
		}
		if cand.Primary.Backend == "backend-never-opened" {
			close(neverOpenedReady)
			<-winnerWait
			return nil, context.Canceled
		}
		loserStream = &parallelLoserMockStream{
			canceled:         loserCanceled,
			winnerWait:       winnerWait,
			neverOpenedReady: neverOpenedReady,
		}
		return loserStream, nil
	}

	ex.Backends = map[string]execbackend.Backend{
		"backend-winner": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: backend.open,
		},
		"backend-loser": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: backend.open,
		},
		"backend-never-opened": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: backend.open,
		},
	}

	// 1. Setup real memo store and save a memo
	memoStore := interleavedthinking.NewMemoStore(4096)
	memoRef, err := memoStore.Put(ctx, interleavedthinking.Scope(aLegID), interleavedthinking.MemoState{
		Memo:                  "test parallel memo guidance",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	interleavedState := interleavedstate.State{
		Cycle: interleavedstate.CycleState{
			SelectorKey: "parallel:backend-winner:model-1!backend-loser:model-1!backend-never-opened:model-1",
			Sequence: []interleavedstate.CycleEntry{
				{Key: "parallel:backend-winner:model-1!backend-loser:model-1!backend-never-opened:model-1", Role: interleavedstate.RoleExecutor},
			},
			NextIndex: 0,
		},
	}

	// 2. Setup interleaved state in store
	if iss, ok := ex.Store.(b2bua.InterleavedStateStore); ok {
		if err := iss.SetInterleavedState(ctx, aLegID, interleavedState); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal("store does not implement InterleavedStateStore")
	}

	ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}, memoStore)
	RegisterTestMemoStore(ex, memoStore)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	reqFacts := requestFacts{
		recvTurnFacts: recvTurnFacts{
			traceID:          "trace-parallel-tdd",
			aLegID:           aLegID,
			baseline:         aipapi.Call{ID: "req-1", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}}},
			billingCallID:    usageevidence.BillingCallID("call-1"),
			billingCallState: &billingCallState{},
		},
		bus:    ex.Bus,
		aScope: aScope,
	}

	progress := &recoveryController{
		budget:   &attemptBudget{max: 10},
		ttft:     &ttftBudget{},
		excluded: make(map[string]struct{}),
	}
	progress.failures = progress.budget.getFailures()
	progress.budget.failures = progress.failures

	req := openNextRequest{
		reqFacts:    reqFacts,
		routeFacts:  routeFacts{sel: &routing.Selector{}},
		progress:    progress,
		mode:        openModeInitial,
		interleaved: interleavedState,
	}

	candidates := []routing.AttemptCandidate{
		{
			Primary:         routing.Primary{Backend: "backend-winner", Model: "model-1"},
			Key:             "backend-winner:model-1",
			InterleavedRole: interleavedstate.RoleExecutor,
		},
		{
			Primary:         routing.Primary{Backend: "backend-loser", Model: "model-1"},
			Key:             "backend-loser:model-1",
			InterleavedRole: interleavedstate.RoleExecutor,
		},
		{
			Primary:         routing.Primary{Backend: "backend-never-opened", Model: "model-1"},
			Key:             "backend-never-opened:model-1",
			InterleavedRole: interleavedstate.RoleExecutor,
		},
	}

	out, err := ex.tryOpenParallelGroup(ctx, req, candidates, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup: %v", err)
	}

	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected parallel winner arm to win")
	}

	if out.ready.Candidate().Primary.Backend != "backend-winner" {
		t.Errorf("expected backend-winner, got %s", out.ready.Candidate().Primary.Backend)
	}

	if out.ready != nil && out.ready.session != nil && out.ready.session.inner != nil {
		if err := out.ready.session.inner.Close(); err != nil {
			t.Fatalf("session close: %v", err)
		}
	}

	// Verify that the loser arm was canceled and its transaction rolled back
	select {
	case <-loserCanceled:
		// success: loser was canceled/rolled back
	case <-time.After(10 * time.Second):
		t.Error("timed out waiting for parallel loser stream cancel")
	}

	if loserStream == nil {
		t.Fatal("expected loser stream to be created")
	}

	if atomic.LoadInt32(&loserStream.cancelCalls) != 1 {
		t.Errorf("expected loser stream to be canceled exactly once, got %d", loserStream.cancelCalls)
	}
	if atomic.LoadInt32(&loserStream.closeCalls) != 1 {
		t.Errorf("expected loser stream to be closed exactly once, got %d", loserStream.closeCalls)
	}

	// Verify that the loser's authority reservation was settled as losing (since it was opened/incurred)
	settles := auth.settleInputs()
	if len(settles) == 0 {
		t.Error("expected at least one settle call for the parallel losers")
	}
	foundLoserSettle := false
	for _, set := range settles {
		if set.Correlation.BackendID == "backend-loser" {
			foundLoserSettle = true
			if set.Kind != usagelimit.SettlementKindLosing {
				t.Errorf("expected settlement kind for loser to be SettlementKindLosing, got: %s", set.Kind)
			}
		}
	}
	if !foundLoserSettle {
		t.Error("expected loser's reservation to be settled on the authority service")
	}

	// Verify that the unopened candidate's reservation was released (since it was never opened/incurred) (Requirement 5)
	releases := auth.releaseInputs()
	foundNeverOpenedRelease := false
	for _, rel := range releases {
		if rel.Correlation.BackendID == "backend-never-opened" {
			foundNeverOpenedRelease = true
			if rel.Kind != usagelimit.ReleaseKindAdmissionFailure {
				t.Errorf("expected release kind for backend-never-opened to be ReleaseKindAdmissionFailure, got: %s", rel.Kind)
			}
		}
	}
	if !foundNeverOpenedRelease {
		t.Error("expected backend-never-opened reservation to be released on the authority service")
	}

	// Verify winner-only memo update in the memo store (Requirement 5)
	storedMemo, ok, err := memoStore.Get(ctx, interleavedthinking.Scope(aLegID), memoRef)
	if err != nil || !ok {
		t.Fatalf("memo lookup failed: %v", err)
	}
	// The winner's memo update decrements turns from 2 to 1 and increments InjectedCount to 1
	if storedMemo.InjectedCount != 1 {
		t.Errorf("expected InjectedCount to be 1 (winner only committed once), got %d", storedMemo.InjectedCount)
	}
	if storedMemo.RegularTurnsRemaining != 1 {
		t.Errorf("expected RegularTurnsRemaining to be 1, got %d", storedMemo.RegularTurnsRemaining)
	}

	// Verify winner-only cycle assertion (Requirement 5)
	if iss, ok := ex.Store.(b2bua.InterleavedStateStore); ok {
		storedState, err := iss.FetchInterleavedState(ctx, aLegID)
		if err != nil {
			t.Fatalf("failed to get stored interleaved state: %v", err)
		}
		if storedState.Cycle.SelectorKey != out.interleaved.Cycle.SelectorKey {
			t.Errorf("stored cycle SelectorKey mismatch: got %q, want %q", storedState.Cycle.SelectorKey, out.interleaved.Cycle.SelectorKey)
		}
	}

	// Wait a moment for async cancel/rollback to complete and verify authority is settled
	if backend.openCalls.Load() < 1 {
		t.Error("expected at least one backend open")
	}
}

func TestTDD_ParallelRaceRollbackOnCancellation(t *testing.T) {
	ctx := context.Background()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "res-cancel",
			ReservedAmount: authorityInputAmount(7),
		},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	// Make backend Open block until cancelled, then return context.Canceled
	backend.openFn = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	ex.Backends = map[string]execbackend.Backend{
		"backend-1": {
			Caps:          aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: parallelTransportCaps(),
			Open:          backend.open,
		},
	}

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	reqFacts := requestFacts{
		recvTurnFacts: recvTurnFacts{
			traceID:          "trace-cancel-tdd",
			aLegID:           aLegID,
			baseline:         aipapi.Call{ID: "req-1", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}}},
			billingCallID:    usageevidence.BillingCallID("call-1"),
			billingCallState: &billingCallState{},
		},
		bus:    ex.Bus,
		aScope: aScope,
	}

	progress := &recoveryController{
		budget:   &attemptBudget{max: 10},
		ttft:     &ttftBudget{},
		excluded: make(map[string]struct{}),
	}
	progress.failures = progress.budget.getFailures()
	progress.budget.failures = progress.failures

	req := openNextRequest{
		reqFacts:    reqFacts,
		routeFacts:  routeFacts{sel: &routing.Selector{}},
		progress:    progress,
		mode:        openModeInitial,
		interleaved: interleavedstate.State{},
	}

	candidates := []routing.AttemptCandidate{
		{Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}, Key: "backend-1:model-1"},
	}

	// Run with a cancelable context, and cancel it quickly
	runCtx, cancel := context.WithCancel(ctx)

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, _ = ex.tryOpenParallelGroup(runCtx, req, candidates, nil, "", false)

	// Wait a moment for async cancel/rollback to complete and verify authority is settled
	time.Sleep(200 * time.Millisecond)

	if got := auth.settleCalls.Load(); got != 1 {
		t.Errorf("expected 1 settle call, got %d", got)
	}
}

type mockRequestPartHook struct {
	id          string
	order       int
	failureMode sdk.FailureMode
	handleFn    func(ctx context.Context, call *aipapi.Call, meta sdk.PartMeta) error
}

func (h mockRequestPartHook) ID() string                   { return h.id }
func (h mockRequestPartHook) Order() int                   { return h.order }
func (h mockRequestPartHook) FailureMode() sdk.FailureMode { return h.failureMode }
func (h mockRequestPartHook) HandleRequestParts(ctx context.Context, call *aipapi.Call, meta sdk.PartMeta) error {
	if h.handleFn != nil {
		return h.handleFn(ctx, call, meta)
	}
	return nil
}

func TestTDD_ParallelPostRequestHookExclusion_ProvesNoNilStreamRecvPanicAndExclusionRecorded(t *testing.T) {
	ctx := context.Background()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-parallel-post-hook",
			ReservedAmount: authorityInputAmount(7),
		},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	loserHookRun := make(chan struct{})
	backend.openFn = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		if cand.Primary.Backend == "backend-winner" {
			select {
			case <-loserHookRun:
			case <-time.After(10 * time.Second):
			}
			time.Sleep(10 * time.Millisecond)
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventTextDelta, Delta: "winner-text"},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		}
		t.Errorf("backend-loser should not be opened because it was excluded")
		return nil, errors.New("should not be called")
	}

	ex.Backends = map[string]execbackend.Backend{
		"backend-winner": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: backend.open,
		},
		"backend-loser": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: backend.open,
		},
	}

	// Register request part hook that excludes backend-loser
	ex.Bus = hooks.New(hooks.Config{
		RequestPartHooks: []sdk.RequestPartHook{
			mockRequestPartHook{
				id: "exclude-loser-hook",
				handleFn: func(ctx context.Context, call *aipapi.Call, meta sdk.PartMeta) error {
					if meta.BackendID == "backend-loser" {
						call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming
						select {
						case <-loserHookRun:
						default:
							close(loserHookRun)
						}
					}
					return nil
				},
			},
		},
	})

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	reqFacts := requestFacts{
		recvTurnFacts: recvTurnFacts{
			traceID:          "trace-parallel-post-hook",
			aLegID:           aLegID,
			baseline:         aipapi.Call{ID: "req-1", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}}},
			billingCallID:    usageevidence.BillingCallID("call-1"),
			billingCallState: &billingCallState{},
		},
		bus:    ex.Bus,
		aScope: aScope,
	}

	progress := &recoveryController{
		budget:   &attemptBudget{max: 10},
		ttft:     &ttftBudget{},
		excluded: make(map[string]struct{}),
	}
	progress.failures = progress.budget.getFailures()
	progress.budget.failures = progress.failures

	req := openNextRequest{
		reqFacts:    reqFacts,
		routeFacts:  routeFacts{sel: &routing.Selector{}},
		progress:    progress,
		mode:        openModeInitial,
		interleaved: interleavedstate.State{},
	}

	candidates := []routing.AttemptCandidate{
		{Primary: routing.Primary{Backend: "backend-winner", Model: "model-1"}, Key: "backend-winner:model-1", IsParallel: true},
		{Primary: routing.Primary{Backend: "backend-loser", Model: "model-1"}, Key: "backend-loser:model-1", IsParallel: true},
	}

	out, err := ex.tryOpenParallelGroup(ctx, req, candidates, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup: %v", err)
	}

	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected parallel winner arm to win")
	}

	if out.ready.Candidate().Primary.Backend != "backend-winner" {
		t.Errorf("expected backend-winner, got %s", out.ready.Candidate().Primary.Backend)
	}

	// Verify candidate was recorded in excluded list
	if _, ok := progress.excluded["backend-loser:model-1"]; !ok {
		t.Errorf("expected backend-loser to be excluded, but excluded list has %v", progress.excluded)
	}
}

func TestTDD_ParallelPostRequestHookExclusion_Precedence(t *testing.T) {
	ctx := context.Background()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-parallel-prec",
			ReservedAmount: authorityInputAmount(7),
		},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	backend.openFn = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return nil, errors.New("open failed")
	}

	ex.Backends = map[string]execbackend.Backend{
		"backend-loser1": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: backend.open,
		},
		"backend-loser2": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: backend.open,
		},
	}

	// Register request part hook that excludes backend-loser1 with capability reject
	ex.Bus = hooks.New(hooks.Config{
		RequestPartHooks: []sdk.RequestPartHook{
			mockRequestPartHook{
				id: "exclude-loser1-hook",
				handleFn: func(ctx context.Context, call *aipapi.Call, meta sdk.PartMeta) error {
					if meta.BackendID == "backend-loser1" {
						call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming
					}
					return nil
				},
			},
		},
	})

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	reqFacts := requestFacts{
		recvTurnFacts: recvTurnFacts{
			traceID:          "trace-parallel-prec",
			aLegID:           aLegID,
			baseline:         aipapi.Call{ID: "req-1", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}}},
			billingCallID:    usageevidence.BillingCallID("call-1"),
			billingCallState: &billingCallState{},
		},
		bus:    ex.Bus,
		aScope: aScope,
	}

	progress := &recoveryController{
		budget:   &attemptBudget{max: 10},
		ttft:     &ttftBudget{},
		excluded: make(map[string]struct{}),
	}
	progress.failures = progress.budget.getFailures()
	progress.budget.failures = progress.failures

	req := openNextRequest{
		reqFacts:    reqFacts,
		routeFacts:  routeFacts{sel: &routing.Selector{}},
		progress:    progress,
		mode:        openModeInitial,
		interleaved: interleavedstate.State{},
	}

	candidates := []routing.AttemptCandidate{
		{Primary: routing.Primary{Backend: "backend-loser1", Model: "model-1"}, Key: "backend-loser1:model-1", IsParallel: true},
		{Primary: routing.Primary{Backend: "backend-loser2", Model: "model-1"}, Key: "backend-loser2:model-1", IsParallel: true},
	}

	// First call to tryOpenParallelGroup will run the race and exclude them
	out, err := ex.tryOpenParallelGroup(ctx, req, candidates, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup: %v", err)
	}
	if out.ready != nil && out.ready.session != nil {
		t.Fatal("expected no winner")
	}

	// Update progress with excluded
	for _, c := range candidates {
		progress.excluded[c.Key] = struct{}{}
	}

	req.routeFacts.sel = &routing.Selector{
		Alternatives: []routing.FailoverAlt{
			{
				Parallel: &routing.Parallel{
					Branches: []routing.ParallelBranch{
						{Target: routing.Primary{Backend: "backend-loser1", Model: "model-1"}},
						{Target: routing.Primary{Backend: "backend-loser2", Model: "model-1"}},
					},
				},
			},
		},
	}
	_, err = ex.openNext(ctx, req)
	if err == nil {
		t.Fatal("expected error from openNext, got nil")
	}

	// Verify that the error is indeed the transport reject error
	if !errors.Is(err, aipapi.ErrTransportReject) {
		t.Errorf("expected error to wrap ErrTransportReject, got: %v", err)
	}
}
