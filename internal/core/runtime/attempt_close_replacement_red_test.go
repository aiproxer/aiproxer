package runtime

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

type closeReplacementRaceStream struct {
	cancelCalls atomic.Int32
	closeCalls  atomic.Int32
}

// TestRetryRecvStreamClosePublicationMidpointDoesNotEnterRecovery is RED until
// Recv observes that Close has closed the attempt publication window. The
// request is already committed, but Close has not yet published terminal
// completion; a nil current inner must therefore finish as EOF/cancellation,
// never as a recovery-turn-committed error.
func TestRetryRecvStreamClosePublicationMidpointDoesNotEnterRecovery(t *testing.T) {
	var openCalls atomic.Int32
	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "close-midpoint", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-close-midpoint",
			aLegID:   "a-leg-close-midpoint",
		}),
		terminal:         newTurnTerminal(),
		responsePipeline: newResponsePipeline(),
		recovery: &recoveryController{
			opener: func(context.Context, replacementOpenRequest) (replacementOpenResult, error) {
				openCalls.Add(1)
				return replacementOpenResult{opened: true}, nil
			},
		},
		attempt: testAttemptSlot(
			b2bua.BLegRecord{BLegID: "close-midpoint-bleg", Seq: 1},
			authorityCandidate(),
			authorityLifecycle{},
		),
	}
	testStoreInner(rs, &closeReplacementRaceStream{})
	current := rs.attempt.closePublicationAndSnapshot()
	if current == nil || current != rs.attempt.snapshot() {
		t.Fatal("Close midpoint must retain the current attempt snapshot")
	}
	if inner := current.takeInner(); inner == nil {
		t.Fatal("Close midpoint must remove the current inner stream")
	}
	rs.terminal.markCommitted(current)

	_, err := rs.Recv(context.Background())
	if err != nil && !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "a-leg canceled") {
		t.Fatalf("blocked Close midpoint returned %v", err)
	}
	if got := openCalls.Load(); got != 0 {
		t.Fatalf("replacement opener calls = %d, want 0 after Close publication", got)
	}
}

func (*closeReplacementRaceStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *closeReplacementRaceStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCalls.Add(1)
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (s *closeReplacementRaceStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

// TestRetryRecvStreamCloseDuringReplacementOpenDoesNotPublishAttempt is RED until
// a replacement that finishes Open after Close loses publication ownership and is
// cleaned up as an ephemeral attempt.
func TestRetryRecvStreamCloseDuringReplacementOpenDoesNotPublishAttempt(t *testing.T) {
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "replacement-reservation",
			ReservedAmount: authorityInputAmount(8),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	budget := &attemptBudget{max: 3}
	baseline := aipapi.Call{
		ID:    "request-1",
		Route: aipapi.RouteIntent{Selector: "backend-1:model-1"},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
		Messages: testMinimalUserMessages(),
	}
	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	oldStream := &closeReplacementRaceStream{}
	replacementStream := &closeReplacementRaceStream{}
	replacementOpenEntered := make(chan struct{})
	releaseReplacementOpen := make(chan struct{})
	backend.openFn = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		close(replacementOpenEntered)
		<-releaseReplacementOpen
		return replacementStream, nil
	}

	oldAdmission := auth.admitResult
	oldAdmission.ReservationID = "old-reservation"
	oldAuthority := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(8),
		admissionResult: oldAdmission,
	}
	terminal := newTurnTerminal()
	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: baseline,
			aLegID:   aLegID,
			traceID:  "trace-1",
		}),
		terminal: terminal,
		recovery: newRecoveryController(recoveryControllerInput{
			e:              ex,
			affinityStore:  ex.AffinityStore,
			log:            ex.Log,
			streamRecovery: ex.StreamRecovery,
			opener:         newReplacementOpener(ex, hooks.New(hooks.Config{}), terminal.aLegScope()),
			budget:         budget,
			sel:            sel,
			session:        &routing.SessionRoutingState{},
			excluded:       map[string]struct{}{},
			rng:            routing.NewSeededRng(1),
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{ALegID: aLegID, BLegID: "old-bleg", Seq: 1},
			authorityCandidate(),
			testAuthorityLifecycle(ex, oldAuthority, authorityCandidate()),
		),
		responsePipeline: newResponsePipeline(),
	}

	testStoreInner(rs, oldStream)
	oldAttempt := rs.attempt.snapshot()

	const replacementOpenTimeout = 5 * time.Second
	var releaseOnce sync.Once
	releaseOpen := func() { releaseOnce.Do(func() { close(releaseReplacementOpen) }) }
	defer releaseOpen()

	type replacementResult struct {
		opened bool
		err    error
	}
	resultCh := make(chan replacementResult, 1)
	go func() {
		testRetirePriorAttempt(rs)

		plan, err := rs.recovery.tryReplacementIteration(context.Background(), rs.facts.terminalFacts(), rs.attempt.require(), rs.terminal.committed())
		if err == nil && plan.opened {
			if regErr := rs.terminal.registerReplacement(context.Background(), plan.open, plan.next); err == nil {
				err = regErr
			}
			if err == nil {
				ready := plan.next
				if _, published := rs.attempt.swapIfOpen(ready); !published {
					rs.terminal.cleanupUnpublishedReplacement(context.Background(), plan.next)
				}
			}
		}
		resultCh <- replacementResult{opened: plan.opened, err: err}
	}()

	select {
	case <-replacementOpenEntered:
	case <-time.After(replacementOpenTimeout):
		t.Fatal("replacement backend Open did not enter its barrier")
	}

	if err := rs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !rs.terminal.finished() {
		t.Fatal("request must be finished after Close terminalizes the old stream")
	}
	if got := oldStream.closeCalls.Load(); got != 1 {
		t.Fatalf("old stream Close calls = %d, want 1", got)
	}

	releaseOpen()
	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("tryReplacementIteration: %v", result.err)
		}
	case <-time.After(replacementOpenTimeout):
		t.Fatal("tryReplacementIteration did not return after replacement Open was released")
	}

	if current := rs.attempt.snapshot(); current != oldAttempt || current == nil || current.loadInner() != nil {
		t.Fatal("replacement attempt was published after Close")
	}
	if got := replacementStream.cancelCalls.Load(); got != 1 {
		t.Fatalf("replacement stream Cancel calls = %d, want 1", got)
	}
	if got := replacementStream.closeCalls.Load(); got != 1 {
		t.Fatalf("replacement stream Close calls = %d, want 1", got)
	}
	if !rs.terminal.finished() {
		t.Fatal("request must remain finished after replacement Open completes")
	}

	settles := auth.settleInputs()
	releases := auth.releaseInputs()
	if got := len(settles) + len(releases); got != 2 {
		t.Fatalf("authority terminal calls = %d, want 2 (old and fresh replacement reservations)", got)
	}
	containsSettle := func(id string) bool {
		for _, input := range settles {
			if input.ReservationID == id {
				return true
			}
		}
		return false
	}
	containsRelease := func(id string) bool {
		for _, input := range releases {
			if input.ReservationID == id {
				return true
			}
		}
		return false
	}
	for _, id := range []string{"old-reservation", "replacement-reservation"} {
		if !containsSettle(id) && !containsRelease(id) {
			t.Fatalf("authority reservation %q was neither settled nor released", id)
		}
	}
}

func TestRetryRecvStreamCloseDuringReplacementOpen_AppendsReplacementLegExactlyOnce(t *testing.T) {
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "replacement-reservation",
			ReservedAmount: authorityInputAmount(8),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	wireDummyBilling(ex)

	var mu sync.Mutex
	var appendedLegs []usageevidence.CallLegUsageRecord
	ex.TerminalUsageSink = testTerminalSink{
		appendLeg: func(ctx context.Context, record usageevidence.CallLegUsageRecord) error {
			mu.Lock()
			defer mu.Unlock()
			appendedLegs = append(appendedLegs, record)
			return nil
		},
	}

	budget := &attemptBudget{max: 3}
	baseline := aipapi.Call{
		ID:    "request-1",
		Route: aipapi.RouteIntent{Selector: "backend-1:model-1"},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
		Messages: testMinimalUserMessages(),
	}
	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	replacementStream := &closeReplacementRaceStream{}
	replacementOpenEntered := make(chan struct{})
	releaseReplacementOpen := make(chan struct{})
	backend.openFn = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		close(replacementOpenEntered)
		<-releaseReplacementOpen
		return replacementStream, nil
	}

	oldAdmission := auth.admitResult
	oldAdmission.ReservationID = "old-reservation"
	oldAuthority := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(8),
		admissionResult: oldAdmission,
	}
	terminal := newTurnTerminal()
	bindTurnTerminalRuntime(terminal, ex)
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatalf("new billing call id: %v", err)
	}
	callState := newBillingCallState(callID)
	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline:         baseline,
			aLegID:           aLegID,
			traceID:          "trace-1",
			billingCallState: callState,
			billingCallID:    callID,
		}),
		terminal: terminal,
		recovery: newRecoveryController(recoveryControllerInput{
			e:              ex,
			affinityStore:  ex.AffinityStore,
			log:            ex.Log,
			streamRecovery: ex.StreamRecovery,
			opener:         newReplacementOpener(ex, hooks.New(hooks.Config{}), terminal.aLegScope()),
			budget:         budget,
			sel:            sel,
			session:        &routing.SessionRoutingState{},
			excluded:       map[string]struct{}{},
			rng:            routing.NewSeededRng(1),
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{ALegID: aLegID, BLegID: "old-bleg", Seq: 1},
			authorityCandidate(),
			testAuthorityLifecycle(ex, oldAuthority, authorityCandidate()),
		),
		responsePipeline: newResponsePipelineForExecutor(ex),
	}

	testRetirePriorAttempt(rs)

	const replacementOpenTimeout = 5 * time.Second
	var releaseOnce sync.Once
	releaseOpen := func() { releaseOnce.Do(func() { close(releaseReplacementOpen) }) }
	defer releaseOpen()

	type recvResult struct {
		ev  aipapi.Event
		err error
	}
	resultCh := make(chan recvResult, 1)
	go func() {
		ev, err := rs.Recv(context.Background())
		resultCh <- recvResult{ev: ev, err: err}
	}()

	select {
	case <-replacementOpenEntered:
	case <-time.After(replacementOpenTimeout):
		t.Fatal("replacement backend Open did not enter its barrier")
	}

	if err := rs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	releaseOpen()
	select {
	case result := <-resultCh:
		t.Logf("Recv returned ev=%+v err=%v", result.ev, result.err)
		if result.err != nil && !errors.Is(result.err, io.EOF) {
			t.Fatalf("Recv: %v", result.err)
		}
	case <-time.After(replacementOpenTimeout):
		t.Fatal("Recv did not return after replacement Open was released")
	}

	mu.Lock()
	defer mu.Unlock()

	t.Logf("appendedLegs count=%d: %+v", len(appendedLegs), appendedLegs)

	var replacementLegCount int
	var replacementBLegID string
	for _, leg := range appendedLegs {
		if leg.BLegID != "old-bleg" {
			replacementLegCount++
			replacementBLegID = leg.BLegID
		}
	}
	if replacementLegCount != 1 {
		t.Fatalf("replacement BLegID %q appended %d times, want exactly 1 (total legs=%d: %+v)", replacementBLegID, replacementLegCount, len(appendedLegs), appendedLegs)
	}
}
