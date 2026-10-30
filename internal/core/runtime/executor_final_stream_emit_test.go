package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	secureapp "github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type emitTestObserver struct {
	events   []aipapi.Event
	outcomes []response.StreamOutcome
	finishN  atomic.Int64
	mu       sync.Mutex
}

func (o *emitTestObserver) Observe(_ context.Context, ev aipapi.Event) error {
	o.mu.Lock()
	o.events = append(o.events, ev)
	o.mu.Unlock()
	return nil
}

func (o *emitTestObserver) Finish(_ context.Context, outcome response.StreamOutcome) error {
	o.finishN.Add(1)
	o.mu.Lock()
	o.outcomes = append(o.outcomes, outcome)
	o.mu.Unlock()
	return nil
}

type emitTestObserverFactory struct {
	observers []*emitTestObserver
	obsMu     sync.Mutex
}

func (f *emitTestObserverFactory) ID() string                        { return "emit-test-obs" }
func (f *emitTestObserverFactory) Order() int                        { return 0 }
func (f *emitTestObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (f *emitTestObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	obs := &emitTestObserver{}
	f.obsMu.Lock()
	f.observers = append(f.observers, obs)
	f.obsMu.Unlock()
	return obs, nil
}

func (f *emitTestObserverFactory) snapshot() []*emitTestObserver {
	f.obsMu.Lock()
	defer f.obsMu.Unlock()
	out := make([]*emitTestObserver, len(f.observers))
	copy(out, f.observers)
	return out
}

type equalReplaceGate struct{}

func (equalReplaceGate) ID() string                        { return "equal-replace" }
func (equalReplaceGate) Order() int                        { return 0 }
func (equalReplaceGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (equalReplaceGate) Handle(_ context.Context, _ completion.Meta, buf completion.Buffered, _ completion.Services) (completion.Outcome, error) {
	events := buf.Events()
	return completion.ReplaceOutcome(append([]aipapi.Event(nil), events...)), nil
}

type failingSecureRecorderEmit struct{ err error }

func (f failingSecureRecorderEmit) RecordClientTurnAfterGate(context.Context, secureapp.ClientTurnRecordInput) error {
	return nil
}

func (f failingSecureRecorderEmit) RecordPostHookStreamEvent(context.Context, secureapp.StreamEventRecordInput) error {
	return f.err
}

func setupEmitObserverStream(t *testing.T, auth *recordingAuthorityService, factory *emitTestObserverFactory, gates []completion.Gate) (*Executor, *retryRecvStream) {
	t.Helper()
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{
		call:   accountingapp.CountResult{InputTokens: 7, TotalTokens: 7},
		output: accountingapp.CountResult{OutputTokens: 3, TotalTokens: 10},
	}, accountingstream.Config{})
	bus := hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			CompletionGates:         gates,
			StreamObserverFactories: []response.StreamObserverFactory{factory},
		}),
	})
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "emit-obs", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-emit",
			aLegID:   aLegID,
		}),
		attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-emit", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
		responsePipeline: newResponsePipeline(),
	}
	bindTestRuntimeOwners(rs, ex)
	views, viewsOK := rs.facts.viewsFor(context.Background())
	if err := rs.responsePipeline.openFinalStreamObservation(context.Background(), rs.facts, rs.attempt.require(), views, viewsOK, rs.terminal.committed()); err != nil {
		t.Fatalf("open observer: %v", err)
	}
	return ex, rs
}

func TestEmitClientFacingObserved_recoverDrainFinishObservedAndReleased(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-emit-drain",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		settleErr: errors.New("settle boom"),
		status:    controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &emitTestObserverFactory{}
	_, rs := setupEmitObserverStream(t, auth, factory, []completion.Gate{gatedLeakPassGate{}})

	first, err := testRecvOne(context.Background(), rs, aipapi.Event{Kind: aipapi.EventResponseFinished})
	if err != nil {
		t.Fatalf("handleRecvSuccess: %v", err)
	}
	if first.Kind != aipapi.EventUsageDelta {
		t.Fatalf("first=%#v want usage_delta", first)
	}

	finish, err := rs.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if finish.Kind != aipapi.EventResponseFinished {
		t.Fatalf("finish kind=%q", finish.Kind)
	}

	observers := factory.snapshot()
	if len(observers) == 0 {
		t.Fatal("expected observer")
	}
	obs := observers[len(observers)-1]
	obs.mu.Lock()
	sawFinished := false
	sawUsage := false
	for _, ev := range obs.events {
		switch ev.Kind {
		case aipapi.EventResponseFinished:
			sawFinished = true
		case aipapi.EventUsageDelta:
			sawUsage = true
		}
	}
	outcomes := append([]response.StreamOutcome(nil), obs.outcomes...)
	obs.mu.Unlock()
	if !sawUsage {
		t.Fatal("synthesized usage must pass through the response observation boundary")
	}
	if !sawFinished {
		t.Fatal("recoverDrain pop must Observe response_finished")
	}
	if obs.finishN.Load() != 1 || len(outcomes) != 1 || outcomes[0] != response.OutcomeSuccessReleased {
		t.Fatalf("want Finish(success_released) once; finishN=%d outcomes=%#v", obs.finishN.Load(), outcomes)
	}
}

func TestEmitClientFacingObserved_mandatoryBeforeEmitFinishFailed(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-emit-fail",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &emitTestObserverFactory{}
	ex, rs := setupEmitObserverStream(t, auth, factory, nil)
	ex.StreamUsage = nil
	recErr := errors.New("recorder boom")
	ex.SecureSessionRecorder = failingSecureRecorderEmit{err: recErr}
	ex.SecureSessionRecordingMandatory = true
	rs = withTestRecvFacts(rs, func(f recvTurnFacts) recvTurnFacts {
		f.secureTurnOK = true
		return f
	})
	bindTestRuntimeOwners(rs, ex)

	_, err := testRecvOne(context.Background(), rs, aipapi.Event{Kind: aipapi.EventResponseFinished})
	if err == nil {
		t.Fatal("want mandatory recorder error")
	}
	observers := factory.snapshot()
	if len(observers) == 0 {
		t.Fatal("expected observer")
	}
	obs := observers[0]
	obs.mu.Lock()
	outcomes := append([]response.StreamOutcome(nil), obs.outcomes...)
	obs.mu.Unlock()
	if len(outcomes) != 1 || outcomes[0] != response.OutcomeFailed {
		t.Fatalf("mandatory beforeEmit failure must Finish(failed); outcomes=%#v", outcomes)
	}
}

func TestEmitClientFacingObserved_RemembersAfterSuccessfulRecording(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-remember-ok",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &emitTestObserverFactory{}
	_, rs := setupEmitObserverStream(t, auth, factory, nil)
	rs.responsePipeline.customer = newCustomerEvidenceAccumulator()

	attempt := rs.attempt.require()
	out, recording, err := rs.responsePipeline.observeClientFacing(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"}, responseEventInput{
		facts: rs.facts, attempt: attempt, recovery: rs.recovery,
		pm: sdkhooks.PartMeta{}, committed: rs.terminal.committed(), now: rs.responsePipeline.nowTime(), finishBeforeRelease: true,
	})
	if err != nil {
		cmd := sdkterminal.CommandPartialError
		if recording.mandatory() {
			cmd = sdkterminal.CommandFrontendEncoderFailure
		}
		rs.terminal.terminalizePartialFailure(context.Background(), rs.responsePipeline, rs.facts.terminalFacts(), attempt, cmd, attemptReasonDetail(err), err)
		t.Fatalf("emit: %v", err)
	}
	if aipapi.OutputCommitted(out) {
		rs.terminal.markOutputCommittedForAttempt(out, attempt, rs.recovery)
	}
	if out.Delta != "hello" {
		t.Fatalf("delta=%q", out.Delta)
	}
	text, _, _, n := rs.responsePipeline.customer.Snapshot()
	if text != "hello" || n != 1 {
		t.Fatalf("customer evidence text=%q n=%d; remember must run after successful recording", text, n)
	}
	observers := factory.snapshot()
	if len(observers) == 0 || len(observers[0].events) != 1 {
		t.Fatalf("final-stream observation must still see the event; observers=%d", len(observers))
	}
}

func TestEmitClientFacingObserved_MandatoryFailureDoesNotRemember(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-remember-fail",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &emitTestObserverFactory{}
	ex, rs := setupEmitObserverStream(t, auth, factory, nil)
	rs.responsePipeline.customer = newCustomerEvidenceAccumulator()
	recErr := errors.New("recorder boom")
	ex.SecureSessionRecorder = failingSecureRecorderEmit{err: recErr}
	ex.SecureSessionRecordingMandatory = true
	rs = withTestRecvFacts(rs, func(f recvTurnFacts) recvTurnFacts {
		f.secureTurnOK = true
		return f
	})
	bindTestRuntimeOwners(rs, ex)

	attempt := rs.attempt.require()
	out, recording, err := rs.responsePipeline.observeClientFacing(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "undelivered"}, responseEventInput{
		facts: rs.facts, attempt: attempt, recovery: rs.recovery,
		pm: sdkhooks.PartMeta{}, committed: rs.terminal.committed(), now: rs.responsePipeline.nowTime(), finishBeforeRelease: true,
	})
	if err != nil {
		cmd := sdkterminal.CommandPartialError
		if recording.mandatory() {
			cmd = sdkterminal.CommandFrontendEncoderFailure
		}
		rs.terminal.terminalizePartialFailure(context.Background(), rs.responsePipeline, rs.facts.terminalFacts(), attempt, cmd, attemptReasonDetail(err), err)
	}
	if aipapi.OutputCommitted(out) {
		rs.terminal.markOutputCommittedForAttempt(out, attempt, rs.recovery)
	}
	if !errors.Is(err, recErr) {
		t.Fatalf("err=%v want recorder boom", err)
	}
	text, _, _, n := rs.responsePipeline.customer.Snapshot()
	if text != "" || n != 0 {
		t.Fatalf("undelivered output must not enter customer evidence; text=%q n=%d", text, n)
	}
}

func TestEmitClientFacingObserved_equalContentGateReplaceCyclesLifecycle(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-equal-gate",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &emitTestObserverFactory{}
	_, rs := setupEmitObserverStream(t, auth, factory, []completion.Gate{equalReplaceGate{}})

	first, err := testRecvOne(context.Background(), rs, aipapi.Event{Kind: aipapi.EventResponseFinished})
	if err != nil {
		t.Fatalf("finish: err=%v", err)
	}
	if first.Kind != aipapi.EventUsageDelta {
		t.Fatalf("with StreamUsage, first client event should be synthesized usage; got %#v", first)
	}
	finish, err := rs.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv finish: %v", err)
	}
	if finish.Kind != aipapi.EventResponseFinished {
		t.Fatalf("finish kind=%q", finish.Kind)
	}

	observers := factory.snapshot()
	if len(observers) < 2 {
		t.Fatalf("equal-content replace must cycle lifecycle; opens=%d", len(observers))
	}
	if observers[0].finishN.Load() != 1 {
		t.Fatalf("first observer Finish once; got %d", observers[0].finishN.Load())
	}
	observers[0].mu.Lock()
	firstOutcomes := append([]response.StreamOutcome(nil), observers[0].outcomes...)
	observers[0].mu.Unlock()
	if len(firstOutcomes) != 1 || firstOutcomes[0] != response.OutcomeGateReplaced {
		t.Fatalf("first lifecycle want gate_replaced; got %#v", firstOutcomes)
	}
	last := observers[len(observers)-1]
	last.mu.Lock()
	sawFinished := false
	sawUsage := false
	for _, ev := range last.events {
		switch ev.Kind {
		case aipapi.EventResponseFinished:
			sawFinished = true
		case aipapi.EventUsageDelta:
			sawUsage = true
		}
	}
	lastOutcomes := append([]response.StreamOutcome(nil), last.outcomes...)
	last.mu.Unlock()
	if !sawUsage {
		t.Fatal("replacement lifecycle must observe synthesized usage")
	}
	if !sawFinished {
		t.Fatal("replacement lifecycle must observe response_finished")
	}
	if len(lastOutcomes) != 1 || lastOutcomes[0] != response.OutcomeSuccessReleased {
		t.Fatalf("replacement lifecycle want success_released; got %#v", lastOutcomes)
	}
}

func TestStreamObserverMeta_clonesScope(t *testing.T) {
	t.Parallel()
	orig := scope.PrincipalScopeView{
		PrincipalID: scope.Known("orig-principal"),
		Roles:       []string{"reader"},
	}
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID:     "t1",
			aLegID:      "a1",
			recvViews:   execctx.Views{Scope: orig.Clone()},
			recvViewsOK: true,
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b1", Seq: 1}, authorityCandidate(), authorityLifecycle{}),
	}
	// Poison context must not affect frozen facts.
	poison := scope.PrincipalScopeView{PrincipalID: scope.Known("poison-principal"), Roles: []string{"poison"}}
	ctx := execctx.WithViews(context.Background(), execctx.Views{Scope: poison.Clone()})
	attempt := rs.attempt.require()
	views, viewsOK := rs.facts.viewsFor(ctx)
	meta := rs.responsePipeline.streamObserverMeta(rs.facts, attempt, views, viewsOK)
	if meta.Scope.PrincipalID.String() != "orig-principal" {
		t.Fatalf("meta scope=%q", meta.Scope.PrincipalID)
	}
	meta.Scope.PrincipalID = scope.Known("mutated")
	if len(meta.Scope.Roles) > 0 {
		meta.Scope.Roles[0] = "mutated-role"
	}
	if orig.PrincipalID.String() != "orig-principal" {
		t.Fatalf("source scope mutated: %#v", orig)
	}
	if orig.Roles[0] != "reader" {
		t.Fatalf("source roles mutated: %#v", orig.Roles)
	}
}

type orderedEmitObserver struct {
	mu                 sync.Mutex
	ops                []string
	observeAfterFinish bool
	finished           bool
	observeEntered     chan struct{}
	releaseObserve     chan struct{}
	blockOnce          sync.Once
}

func (o *orderedEmitObserver) Observe(_ context.Context, ev aipapi.Event) error {
	if o.observeEntered != nil {
		o.blockOnce.Do(func() { close(o.observeEntered) })
	}
	if o.releaseObserve != nil {
		<-o.releaseObserve
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		o.observeAfterFinish = true
	}
	o.ops = append(o.ops, "observe:"+string(ev.Kind))
	return nil
}

func (o *orderedEmitObserver) Finish(_ context.Context, outcome response.StreamOutcome) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finished = true
	o.ops = append(o.ops, "finish:"+string(outcome))
	return nil
}

type orderedEmitObserverFactory struct {
	obs   *orderedEmitObserver
	mode  sdkhooks.FailureMode
	obsMu sync.Mutex
	n     int
}

func (f *orderedEmitObserverFactory) ID() string                        { return "ordered-emit-obs" }
func (f *orderedEmitObserverFactory) Order() int                        { return 0 }
func (f *orderedEmitObserverFactory) FailureMode() sdkhooks.FailureMode { return f.mode }
func (f *orderedEmitObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	f.obsMu.Lock()
	defer f.obsMu.Unlock()
	f.n++
	return f.obs, nil
}

type nthOpenFailFactory struct {
	failAfter int64
	mode      sdkhooks.FailureMode
	opens     atomic.Int64
	observers []*emitTestObserver
	obsMu     sync.Mutex
}

func (f *nthOpenFailFactory) ID() string                        { return "nth-open-fail" }
func (f *nthOpenFailFactory) Order() int                        { return 0 }
func (f *nthOpenFailFactory) FailureMode() sdkhooks.FailureMode { return f.mode }
func (f *nthOpenFailFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	n := f.opens.Add(1)
	if n > f.failAfter {
		return nil, errors.New("open fail-closed")
	}
	obs := &emitTestObserver{}
	f.obsMu.Lock()
	f.observers = append(f.observers, obs)
	f.obsMu.Unlock()
	return obs, nil
}

func (f *nthOpenFailFactory) snapshot() []*emitTestObserver {
	f.obsMu.Lock()
	defer f.obsMu.Unlock()
	out := make([]*emitTestObserver, len(f.observers))
	copy(out, f.observers)
	return out
}

type failClosedObserveFactory struct {
	obs   *emitTestObserver
	err   error
	opens atomic.Int64
}

func (f *failClosedObserveFactory) ID() string                        { return "fail-closed-observe" }
func (f *failClosedObserveFactory) Order() int                        { return 0 }
func (f *failClosedObserveFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (f *failClosedObserveFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	f.opens.Add(1)
	return failClosedObserve{inner: f.obs, err: f.err}, nil
}

type failClosedObserve struct {
	inner *emitTestObserver
	err   error
}

func (o failClosedObserve) Observe(ctx context.Context, ev aipapi.Event) error {
	if o.inner != nil {
		_ = o.inner.Observe(ctx, ev)
	}
	return o.err
}

func (o failClosedObserve) Finish(ctx context.Context, outcome response.StreamOutcome) error {
	if o.inner != nil {
		return o.inner.Finish(ctx, outcome)
	}
	return nil
}

func TestEmitClientFacingObserved_concurrentCloseRecv(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-race",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	obs := &orderedEmitObserver{
		observeEntered: make(chan struct{}),
		releaseObserve: make(chan struct{}),
	}
	factory := &orderedEmitObserverFactory{obs: obs, mode: sdkhooks.FailOpen}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	bus := hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			StreamObserverFactories: []response.StreamObserverFactory{factory},
		}),
	})
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "emit-race", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-race",
			aLegID:   aLegID,
		}),
		attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-race", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
		responsePipeline: newResponsePipeline(),
	}
	bindTestRuntimeOwners(rs, ex)
	views, viewsOK := rs.facts.viewsFor(context.Background())
	if err := rs.responsePipeline.openFinalStreamObservation(context.Background(), rs.facts, rs.attempt.require(), views, viewsOK, rs.terminal.committed()); err != nil {
		t.Fatalf("open observer: %v", err)
	}
	// Non-terminal drain event so Recv Observes before markFinished; Close must serialize Finish.
	rs.responsePipeline.recoverDrain = []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "race"}}

	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = rs.Recv(ctx)
	})
	select {
	case <-obs.observeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Observe did not start")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- rs.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return")
	}
	time.Sleep(20 * time.Millisecond)
	obs.mu.Lock()
	if obs.finished {
		obs.mu.Unlock()
		t.Fatal("Finish callback must not run until Observe completes")
	}
	obs.mu.Unlock()
	close(obs.releaseObserve)
	wg.Wait()
	deadline := time.After(2 * time.Second)
	for {
		obs.mu.Lock()
		doneFinish := obs.finished
		ops := append([]string(nil), obs.ops...)
		after := obs.observeAfterFinish
		obs.mu.Unlock()
		if doneFinish {
			if after {
				t.Fatalf("Observe must never run after Finish; ops=%v", ops)
			}
			if len(ops) < 2 || ops[0] != "observe:text_delta" {
				t.Fatalf("want observe then finish; ops=%v", ops)
			}
			var finishN int
			var finishOutcome string
			for _, op := range ops[1:] {
				if len(op) > 7 && op[:7] == "finish:" {
					finishN++
					finishOutcome = op[7:]
				}
			}
			if finishN != 1 || response.StreamOutcome(finishOutcome) != response.OutcomeClosed {
				t.Fatalf("want exactly Finish(closed); ops=%v", ops)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("Finish callback never ran; ops=%v", ops)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestCycleFinalStreamObservation_precommitOpenFailClosedSurfaces(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-cycle-open",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &nthOpenFailFactory{failAfter: 1, mode: sdkhooks.FailClosed}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	bus := hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			CompletionGates:         []completion.Gate{equalReplaceGate{}},
			StreamObserverFactories: []response.StreamObserverFactory{factory},
		}),
	})
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "cycle-open", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-cycle-open",
			aLegID:   aLegID,
		}),
		attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-cycle-open", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
		responsePipeline: newResponsePipeline(),
	}
	bindTestRuntimeOwners(rs, ex)
	views, viewsOK := rs.facts.viewsFor(context.Background())
	if err := rs.responsePipeline.openFinalStreamObservation(context.Background(), rs.facts, rs.attempt.require(), views, viewsOK, rs.terminal.committed()); err != nil {
		t.Fatalf("initial open: %v", err)
	}
	_, err := testRecvOne(context.Background(), rs, aipapi.Event{Kind: aipapi.EventResponseFinished})
	if err == nil {
		t.Fatal("precommit fail-closed Open after gate_replaced must surface")
	}
	observers := factory.snapshot()
	if len(observers) != 1 {
		t.Fatalf("original lifecycle opens=%d", len(observers))
	}
	observers[0].mu.Lock()
	outcomes := append([]response.StreamOutcome(nil), observers[0].outcomes...)
	observers[0].mu.Unlock()
	if len(outcomes) != 1 || outcomes[0] != response.OutcomeGateReplaced {
		t.Fatalf("original must Finish(gate_replaced); got %#v", outcomes)
	}
}

func TestCycleFinalStreamObservation_postcommitOpenFailClosedBestEffort(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-cycle-post",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &nthOpenFailFactory{failAfter: 1, mode: sdkhooks.FailClosed}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	bus := hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			CompletionGates:         []completion.Gate{equalReplaceGate{}},
			StreamObserverFactories: []response.StreamObserverFactory{factory},
		}),
	})
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "cycle-post", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-cycle-post",
			aLegID:   aLegID,
		}),
		attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-cycle-post", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
		responsePipeline: newResponsePipeline(),
	}
	views, viewsOK := rs.facts.viewsFor(context.Background())
	if err := rs.responsePipeline.openFinalStreamObservation(context.Background(), rs.facts, rs.attempt.require(), views, viewsOK, rs.terminal.committed()); err != nil {
		t.Fatalf("initial open: %v", err)
	}
	rs.terminal.markCommitted(rs.attempt.snapshot())
	first, err := testRecvOne(context.Background(), rs, aipapi.Event{Kind: aipapi.EventResponseFinished})
	if err != nil {
		t.Fatalf("postcommit open failure must stay best-effort; err=%v", err)
	}
	if first.Kind != aipapi.EventResponseFinished {
		t.Fatalf("want response_finished; got %#v", first)
	}
}

func TestEmitClientFacingObserved_failClosedObserveAbortsFinishFailed(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-obs-fail",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	inner := &emitTestObserver{}
	factory := &failClosedObserveFactory{obs: inner, err: errors.New("observe boom")}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	bus := hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			StreamObserverFactories: []response.StreamObserverFactory{factory},
		}),
	})
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "obs-fail", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-obs-fail",
			aLegID:   aLegID,
		}),
		attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-obs-fail", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
		responsePipeline: newResponsePipeline(),
	}
	bindTestRuntimeOwners(rs, ex)
	views, viewsOK := rs.facts.viewsFor(context.Background())
	if err := rs.responsePipeline.openFinalStreamObservation(context.Background(), rs.facts, rs.attempt.require(), views, viewsOK, rs.terminal.committed()); err != nil {
		t.Fatalf("open: %v", err)
	}
	_, err := testRecvOne(context.Background(), rs, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
	if err == nil {
		t.Fatal("want fail-closed Observe error")
	}
	inner.mu.Lock()
	outcomes := append([]response.StreamOutcome(nil), inner.outcomes...)
	inner.mu.Unlock()
	if len(outcomes) != 1 || outcomes[0] != response.OutcomeFailed {
		t.Fatalf("any aborting Observe must Finish(failed); outcomes=%#v", outcomes)
	}
	if !rs.terminal.finished() {
		t.Fatal("fail-closed Observe abort must markFinished so further Recv cannot continue")
	}
	if _, err2 := rs.Recv(context.Background()); !errors.Is(err2, io.EOF) {
		t.Fatalf("post-abort Recv must be terminal EOF, got %v", err2)
	}
}

func TestIdleEOFRecoveryWarning_observedViaEmitClientFacing(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-idle-warn",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &emitTestObserverFactory{}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	bus := hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			StreamObserverFactories: []response.StreamObserverFactory{factory},
		}),
	})
	start := time.Unix(1, 0)
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "idle-warn", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-idle-warn",
			aLegID:   aLegID,
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-idle-warn", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()), newAttemptAccountingTracker(start)),
		responsePipeline: &responsePipeline{bus: bus, seenEvents: []aipapi.Event{
			{Kind: aipapi.EventTextDelta, Delta: "hello"},
		}},
		recovery: &recoveryController{recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{
			Enabled:     true,
			IdleTimeout: time.Second,
			EmitWarning: true,
		}, start)},
	}
	bindTestRuntimeOwners(rs, ex)
	rs.responsePipeline.visibleText.WriteString("hello")
	rs.recovery.recoverPolicy.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"}, start.Add(time.Second))
	views, viewsOK := rs.facts.viewsFor(context.Background())
	if err := rs.responsePipeline.openFinalStreamObservation(context.Background(), rs.facts, rs.attempt.require(), views, viewsOK, rs.terminal.committed()); err != nil {
		t.Fatalf("open: %v", err)
	}

	ev, err := testRecvError(context.Background(), rs, context.DeadlineExceeded)
	if err != nil {
		t.Fatalf("idle recovery: err=%v", err)
	}
	if ev.Kind != aipapi.EventWarning || ev.WarningCode != "proxy_stream_recovery" {
		t.Fatalf("want recovery warning; got %#v", ev)
	}
	observers := factory.snapshot()
	if len(observers) == 0 {
		t.Fatal("expected observer")
	}
	obs := observers[0]
	obs.mu.Lock()
	sawWarn := false
	for _, e := range obs.events {
		if e.Kind == aipapi.EventWarning && e.WarningCode == "proxy_stream_recovery" {
			sawWarn = true
		}
	}
	obs.mu.Unlock()
	if !sawWarn {
		t.Fatal("idle recovery warning must pass through the response observation boundary")
	}

	// EOF recovery warning path
	factory2 := &emitTestObserverFactory{}
	ex2, _, aLegID2 := newAuthorityRuntimeTestExecutor(t, auth)
	bus2 := hooks.New(hooks.Config{})
	ex2.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus2, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			StreamObserverFactories: []response.StreamObserverFactory{factory2},
		}),
	})
	rs2 := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "eof-warn", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-eof-warn",
			aLegID:   aLegID2,
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-eof-warn", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex2, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()), newAttemptAccountingTracker(start)),
		responsePipeline: &responsePipeline{seenEvents: []aipapi.Event{
			{Kind: aipapi.EventTextDelta, Delta: "hello"},
		}},
		recovery: &recoveryController{recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{
			Enabled:     true,
			IdleTimeout: time.Second,
			EmitWarning: true,
		}, start)},
	}
	bindTestRuntimeOwners(rs2, ex2)
	rs2.responsePipeline.visibleText.WriteString("hello")
	rs2.recovery.recoverPolicy.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"}, start.Add(time.Second))
	views2, views2OK := rs2.facts.viewsFor(context.Background())
	if err := rs2.responsePipeline.openFinalStreamObservation(context.Background(), rs2.facts, rs2.attempt.require(), views2, views2OK, rs2.terminal.committed()); err != nil {
		t.Fatalf("open eof: %v", err)
	}
	ev2, err := testRecvEOF(context.Background(), rs2)
	if err != nil {
		t.Fatalf("handleRecvEOF: %v", err)
	}
	if ev2.Kind != aipapi.EventWarning || ev2.WarningCode != "proxy_stream_recovery" {
		t.Fatalf("want EOF recovery warning; got %#v", ev2)
	}
	obs2 := factory2.snapshot()[0]
	obs2.mu.Lock()
	sawEOFWarn := false
	for _, e := range obs2.events {
		if e.Kind == aipapi.EventWarning && e.WarningCode == "proxy_stream_recovery" {
			sawEOFWarn = true
		}
	}
	obs2.mu.Unlock()
	if !sawEOFWarn {
		t.Fatal("EOF recovery warning must pass through the response observation boundary")
	}
}
