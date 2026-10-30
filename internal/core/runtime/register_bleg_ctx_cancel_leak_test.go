package runtime

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type trackingManagedStream struct {
	mu   sync.Mutex
	log  []string
	done chan struct{}
}

func newTrackingManagedStream() *trackingManagedStream {
	return &trackingManagedStream{done: make(chan struct{})}
}

func (s *trackingManagedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-s.done:
		return aipapi.Event{}, io.EOF
	}
}

func (s *trackingManagedStream) Cancel(_ context.Context, cause leglifecycle.CancelCause) leglifecycle.CancelResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "cancel:"+string(cause.Kind))
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeProvider}
}

func (s *trackingManagedStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "close")
	return nil
}

func (s *trackingManagedStream) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

func TestOpenInitialAttempt_ContextCanceledBeforeRegisterBLeg_DeterministicCleanup(t *testing.T) {
	t.Parallel()

	const reservationID = "res-serial-ctx-cancel"
	auth := reservedAuthorityRecorder(reservationID)
	capture := &abortJoinCapture{}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	wireAbortBilling(ex, capture)

	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	stream := newTrackingManagedStream()
	ctx, cancel := context.WithCancel(context.Background())

	backend.openFn = func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		// Cancel ctx immediately before RegisterBLeg is invoked
		cancel()
		return stream, nil
	}

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}
	rng := routing.NewSeededRng(1)
	progress := newRecoveryController(recoveryControllerInput{
		budget:   &attemptBudget{max: 3},
		sel:      sel,
		session:  &routing.SessionRoutingState{},
		excluded: map[string]struct{}{},
		rng:      rng,
	})
	plan := &routePlanState{
		routeFacts: routeFacts{
			sel: sel,
			rng: rng,
		},
		progress: progress,
	}
	call := &aipapi.Call{
		ID:    "request-leak-serial",
		Route: aipapi.RouteIntent{Selector: "backend-1:model-1"},
		Session: aipapi.SessionRef{
			ALegID: aLegID,
		},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
		Messages: testMinimalUserMessages(),
	}
	preSession := session.SessionView{
		ALegID: aLegID,
	}
	ibt, err := newIdentityBoundTurn(
		"trace-leak-serial",
		call,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		aipworkspace.WorkspaceView{},
		b2bua.ALegRecord{ALegID: aLegID},
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		preSession,
	)
	if err != nil {
		t.Fatal(err)
	}
	prep := &preparedRequest{
		bus:           hooks.New(hooks.Config{}),
		identity:      ibt,
		call:          ibt.call,
		aScope:        aScope,
		billingCallID: callID,
	}

	_, err = ex.openInitialAttempt(ctx, prep, plan)
	if err == nil {
		t.Fatal("expected error from openInitialAttempt with canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("openInitialAttempt err = %v, want context.Canceled", err)
	}

	// 1. Stream is canceled and closed exactly once
	if got, want := stream.calls(), []string{"cancel:context_done", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stream calls = %v, want %v", got, want)
	}

	// 2. Authority is settled exactly once without leaks
	if got := auth.releaseCalls.Load(); got != 0 {
		t.Fatalf("release calls = %d, want 0", got)
	}
	if got := auth.settleCalls.Load(); got != 1 {
		t.Fatalf("settle calls = %d, want 1", got)
	}
	settle := auth.lastSettle()
	if settle.ReservationID != reservationID {
		t.Fatalf("settle reservation ID = %q, want %q", settle.ReservationID, reservationID)
	}
	if settle.Kind != usagelimit.SettlementKindSwallowed {
		t.Fatalf("settle kind = %q, want swallowed", settle.Kind)
	}

	// 3. Billing leg is appended exactly once
	_, legs := capture.snapshot()
	if len(legs) != 1 {
		t.Fatalf("captured legs = %d, want 1", len(legs))
	}
	if legs[0].Outcome != usageevidence.LegOutcomeCanceled {
		t.Fatalf("leg outcome = %s, want %s", legs[0].Outcome, usageevidence.LegOutcomeCanceled)
	}

	// 4. Subsequent CancelALeg does not double-cancel the stream
	if err := coord.CancelALeg(context.Background(), aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatal(err)
	}
	if got, want := stream.calls(), []string{"cancel:context_done", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stream calls after CancelALeg = %v, want %v (must not double-cancel)", got, want)
	}
}

func TestParallelRace_ContextCanceledBeforeRegisterBLeg_DeterministicCleanup(t *testing.T) {
	t.Parallel()

	const reservationID = "res-parallel-ctx-cancel"
	auth := reservedAuthorityRecorder(reservationID)
	capture := &abortJoinCapture{}
	ex, _, backend, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)
	wireAbortBilling(ex, capture)

	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	stream := newTrackingManagedStream()
	ctx, cancel := context.WithCancel(context.Background())

	backend.openFn = func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		// Cancel ctx immediately before RegisterBLeg
		cancel()
		return stream, nil
	}

	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	req.reqFacts.aScope = aScope
	req.reqFacts.billingCallID = callID
	req.reqFacts.billingCallState = newBillingCallState(callID)

	err = runRaceInGoroutine(t, 5*time.Second, func() error {
		_, err := ex.tryOpenParallelGroup(ctx, req, []routing.AttemptCandidate{authorityCandidate()}, nil, "", false)
		return err
	})
	if err == nil {
		t.Fatal("expected parallel race error from canceled context")
	}

	for start := time.Now(); time.Since(start) < 2*time.Second; time.Sleep(5 * time.Millisecond) {
		_, legs := capture.snapshot()
		if len(legs) >= 1 && len(stream.calls()) >= 2 && auth.settleCalls.Load() >= 1 {
			break
		}
	}

	// 1. Stream is canceled and closed exactly once
	if got, want := stream.calls(), []string{"cancel:context_done", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stream calls = %v, want %v", got, want)
	}

	// 2. Authority is settled exactly once without leaks
	if got := auth.releaseCalls.Load(); got != 0 {
		t.Fatalf("release calls = %d, want 0", got)
	}
	if got := auth.settleCalls.Load(); got != 1 {
		t.Fatalf("settle calls = %d, want 1", got)
	}
	settle := auth.lastSettle()
	if settle.ReservationID != reservationID {
		t.Fatalf("settle reservation ID = %q, want %q", settle.ReservationID, reservationID)
	}
	if settle.Kind != usagelimit.SettlementKindLosing {
		t.Fatalf("settle kind = %q, want losing", settle.Kind)
	}

	// 3. Billing leg is appended exactly once
	_, legs := capture.snapshot()
	if len(legs) != 1 {
		t.Fatalf("captured legs = %d, want 1", len(legs))
	}
	if legs[0].CallID != callID {
		t.Fatalf("leg CallID = %s, want %s", legs[0].CallID, callID)
	}

	// 4. Subsequent CancelALeg does not double-cancel the stream
	if err := coord.CancelALeg(context.Background(), aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatal(err)
	}
	if got, want := stream.calls(), []string{"cancel:context_done", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stream calls after CancelALeg = %v, want %v (must not double-cancel)", got, want)
	}
}

func TestExecutor_Execute_ContextCanceledBeforeRegisterBLeg_Serial_DisposesStreamOnce(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lc := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	stream := newTrackingManagedStream()
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.ALegLifecycle = lc

	ctx, cancel := context.WithCancel(context.Background())
	ex.Backends = map[string]execbackend.Backend{
		"managed": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				cancel()
				return stream, nil
			},
		},
	}

	_, err = ex.Execute(ctx, &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "serial-ctx-cancel"},
		Route:   aipapi.RouteIntent{Selector: "managed:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute err = %v, want context.Canceled", err)
	}
	if got, want := stream.calls(), []string{"cancel:context_done", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stream calls = %v, want %v", got, want)
	}
}
