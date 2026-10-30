package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

type attemptSessionTestPromptSource struct{ id string }

func (attemptSessionTestPromptSource) DrainPromptCacheObservations() []promptcache.Observation {
	return nil
}

type attemptSessionTestPromptController struct{ id string }

func (attemptSessionTestPromptController) Renew(context.Context, promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	return promptcache.RenewResponse{}, nil
}

func (attemptSessionTestPromptController) Release(context.Context, promptcache.ReleaseRequest) error {
	return nil
}

type attemptSessionReplacementResourceStream struct {
	events        []aipapi.Event
	recvErr       error
	usageEvidence []aipapi.Event
	usageDrained  bool
	closed        bool
	controllerID  string
}

func (s *attemptSessionReplacementResourceStream) Recv(context.Context) (aipapi.Event, error) {
	if len(s.events) > 0 {
		ev := s.events[0]
		s.events = s.events[1:]
		return ev, nil
	}
	if s.recvErr != nil {
		err := s.recvErr
		s.recvErr = nil
		return aipapi.Event{}, err
	}
	return aipapi.Event{}, io.EOF
}

func (s *attemptSessionReplacementResourceStream) Close() error {
	s.closed = true
	return nil
}

func (*attemptSessionReplacementResourceStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (s *attemptSessionReplacementResourceStream) DrainUsageEvidence() []aipapi.Event {
	if s.usageDrained {
		return nil
	}
	s.usageDrained = true
	return append([]aipapi.Event(nil), s.usageEvidence...)
}

func (*attemptSessionReplacementResourceStream) DrainPromptCacheObservations() []promptcache.Observation {
	return nil
}

type attemptSessionTestObserver struct {
	finishCount int
	outcome     response.StreamOutcome
}

func (*attemptSessionTestObserver) Observe(context.Context, aipapi.Event) error { return nil }

func (o *attemptSessionTestObserver) Finish(_ context.Context, outcome response.StreamOutcome) error {
	o.finishCount++
	o.outcome = outcome
	return nil
}

type attemptSessionTestObserverFactory struct {
	observers []*attemptSessionTestObserver
}

func (attemptSessionTestObserverFactory) ID() string                        { return "attempt-session-test-observer" }
func (attemptSessionTestObserverFactory) Order() int                        { return 0 }
func (attemptSessionTestObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (f *attemptSessionTestObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	o := &attemptSessionTestObserver{}
	f.observers = append(f.observers, o)
	return o, nil
}

func TestTryReplacementIterationInstallsFreshAttemptResources(t *testing.T) {
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	oldStream := &attemptSessionReplacementResourceStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventToolCallStarted, ToolCallID: "old-call", ToolName: "tool"},
		},
		recvErr: aipapi.RecoverablePreOutputError(errors.New("old backend recv")),
		usageEvidence: []aipapi.Event{{
			Kind: aipapi.EventUsageDelta, OutputTokens: 7,
			Accounting: aipapi.UsageAccountingMetadata{DedupeKey: "old-usage"},
		}},
		controllerID: "old-backend",
	}
	newStream := &attemptSessionReplacementResourceStream{
		events:       []aipapi.Event{{Kind: aipapi.EventResponseStarted}},
		controllerID: "new-backend",
	}
	var controllerCalls []string
	controller := func(id string) (func(context.Context, promptcache.RenewRequest) (promptcache.RenewResponse, error), func(context.Context, promptcache.ReleaseRequest) error) {
		return func(context.Context, promptcache.RenewRequest) (promptcache.RenewResponse, error) {
				controllerCalls = append(controllerCalls, id)
				return promptcache.RenewResponse{}, nil
			}, func(context.Context, promptcache.ReleaseRequest) error {
				controllerCalls = append(controllerCalls, id+":release")
				return nil
			}
	}
	oldRenew, oldRelease := controller("old-backend")
	newRenew, newRelease := controller("new-backend")
	backend := func(stream *attemptSessionReplacementResourceStream, renew func(context.Context, promptcache.RenewRequest) (promptcache.RenewResponse, error), release func(context.Context, promptcache.ReleaseRequest) error) execbackend.Backend {
		return execbackend.Backend{
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return stream, nil
			},
			RenewPromptCache:   renew,
			ReleasePromptCache: release,
		}
	}
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"old": backend(oldStream, oldRenew, oldRelease),
		"new": backend(newStream, newRenew, newRelease),
	}
	observerFactory := &attemptSessionTestObserverFactory{}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			StreamObserverFactories: []response.StreamObserverFactory{observerFactory},
		}),
	})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 3
	ex.toolCallFinalizers = []toolcall.Finalizer{mutFin{}}
	stream, err := ex.Execute(context.Background(), &aipapi.Call{
		ID:    "attempt-resource-replacement",
		Route: aipapi.RouteIntent{Selector: "old:model|new:model"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
		Tools: []aipapi.ToolDef{{Name: "tool"}},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()

	rs, ok := stream.(*retryRecvStream)
	if !ok {
		t.Fatalf("stream type = %T, want *retryRecvStream", stream)
	}
	if ev, err := rs.Recv(context.Background()); err != nil || ev.Kind != aipapi.EventResponseStarted {
		t.Fatalf("initial Recv = (%+v, %v), want response_started", ev, err)
	}
	oldAttempt := rs.attempt.snapshot()
	if oldAttempt == nil {
		t.Fatal("initial production assembly did not install an attempt session")
	}
	if !oldAttempt.accounting.usageObserved {
		t.Fatal("old production attempt did not record provider usage evidence")
	}
	if _, ok := oldAttempt.internalUsageKeys["old-usage"]; !ok {
		t.Fatal("old production attempt did not retain its dedupe key")
	}
	if oldAttempt.promptCacheSource != oldStream {
		t.Fatal("old prompt-cache source was not derived from the old backend stream")
	}
	oldController, ok := oldAttempt.promptCacheController.(backendPromptCacheController)
	if !ok {
		t.Fatalf("old prompt-cache controller = %T, want backend controller", oldAttempt.promptCacheController)
	}
	if _, err := oldController.Renew(context.Background(), promptcache.RenewRequest{}); err != nil {
		t.Fatalf("old prompt-cache controller: %v", err)
	}

	if _, err := rs.Recv(context.Background()); err != nil {
		t.Fatalf("replacement Recv: %v", err)
	}
	newAttempt := rs.attempt.snapshot()
	if newAttempt == nil || newAttempt == oldAttempt {
		t.Fatal("replacement did not install a distinct attempt session")
	}
	if !oldStream.closed {
		t.Fatal("old backend stream was not closed when recv failover swallowed it")
	}
	if oldAttempt.toolFinal != nil && (len(oldAttempt.toolFinal.active) != 0 || len(oldAttempt.toolFinal.passThrough) != 0 || len(oldAttempt.toolFinal.completed) != 0 || len(oldAttempt.toolFinal.drain) != 0) {
		t.Fatal("old attempt tool finalizer state survived replacement")
	}
	if len(observerFactory.observers) < 2 || observerFactory.observers[0].finishCount != 1 || observerFactory.observers[0].outcome != response.OutcomeReplaced {
		t.Fatalf("old final stream observation finish = %+v, want exactly one replaced finish", observerFactory.observers)
	}
	if observerFactory.observers[1].finishCount != 0 {
		t.Fatal("replacement final stream observation must remain open")
	}
	if oldAttempt.finalStreamObs == newAttempt.finalStreamObs {
		t.Fatal("old final stream observation was not finished as replaced")
	}
	if newAttempt.accounting.usageObserved {
		t.Fatal("replacement accounting inherited old provider usage")
	}
	if len(newAttempt.internalUsageKeys) != 0 {
		t.Fatal("replacement usage dedupe state inherited old keys")
	}
	if newAttempt.toolFinal == nil || len(newAttempt.toolFinal.active) != 0 || len(newAttempt.toolFinal.passThrough) != 0 || len(newAttempt.toolFinal.completed) != 0 || len(newAttempt.toolFinal.drain) != 0 {
		t.Fatal("replacement tool finalizer was not constructed clean")
	}
	if newAttempt.promptCacheSource != newStream {
		t.Fatal("replacement prompt-cache source was not derived from replacement backend stream")
	}
	newController, ok := newAttempt.promptCacheController.(backendPromptCacheController)
	if !ok {
		t.Fatalf("replacement prompt-cache controller = %T, want backend controller", newAttempt.promptCacheController)
	}
	if _, err := newController.Renew(context.Background(), promptcache.RenewRequest{}); err != nil {
		t.Fatalf("replacement prompt-cache controller: %v", err)
	}
	if len(controllerCalls) != 2 || controllerCalls[0] != "old-backend" || controllerCalls[1] != "new-backend" {
		t.Fatalf("prompt-cache controllers used = %v, want old then new backend", controllerCalls)
	}
}

func TestAttemptSessionConstructsItsTerminalOnce(t *testing.T) {
	first := newAttemptSession(attemptSessionInput{})
	if first == nil || first.terminal == nil {
		t.Fatal("expected attempt session to own an attempt terminal")
	}
	if first.terminal.Owner().Scope() != sdkterminal.ScopeAttempt {
		t.Fatalf("attempt terminal scope = %v, want %v", first.terminal.Owner().Scope(), sdkterminal.ScopeAttempt)
	}
}

func TestAttemptSessionsHaveDistinctAttemptTerminals(t *testing.T) {
	first := newAttemptSession(attemptSessionInput{})
	second := newAttemptSession(attemptSessionInput{})
	if first.terminal == second.terminal {
		t.Fatal("each B-leg attempt must own a fresh terminal")
	}
	if first.terminal.Owner().Scope() != sdkterminal.ScopeAttempt || second.terminal.Owner().Scope() != sdkterminal.ScopeAttempt {
		t.Fatal("attempt terminals must both be ScopeAttempt")
	}
}

func TestAttemptSlotSnapshotsAndSwapsPointers(t *testing.T) {
	first := newAttemptSession(attemptSessionInput{})
	second := newAttemptSession(attemptSessionInput{})
	var slot attemptSlot
	slot.mu.Lock()
	slot.current = first
	slot.mu.Unlock()
	if slot.snapshot() != first {
		t.Fatal("snapshot did not return installed attempt")
	}
	ready := &readyAttempt{session: second, state: readyStatePrepared}
	if got, published := slot.swapIfOpen(ready); !published || got != first || slot.snapshot() != second {
		t.Fatal("swap did not publish replacement atomically")
	}
}

func TestAttemptSessionTakeInnerIsIdempotent(t *testing.T) {
	inner := aipapi.NewFixedEventStream(nil)
	session := newAttemptSession(attemptSessionInput{inner: inner})
	if got := session.takeInner(); got != inner {
		t.Fatalf("first take = %T, want installed stream", got)
	}
	if got := session.takeInner(); got != nil {
		t.Fatalf("second take = %T, want nil after ownership transfer", got)
	}
}

func TestAttemptSessionOwnsAttemptLocalResources(t *testing.T) {
	accounting := newAttemptAccountingTracker(time.Unix(1, 0))
	toolFinal := &toolCallAssembler{}
	finalObs := &extensions.FinalStreamObservationSession{}
	source := attemptSessionTestPromptSource{}
	controller := attemptSessionTestPromptController{}
	session := newAttemptSession(attemptSessionInput{
		accounting:            accounting,
		toolFinal:             toolFinal,
		promptCacheSource:     source,
		promptCacheController: controller,
		finalStreamObs:        finalObs,
	})
	if session.accounting != accounting {
		t.Fatal("attempt accounting must be owned by the attempt session")
	}
	if session.toolFinal != toolFinal {
		t.Fatal("tool finalizer must be owned by the attempt session")
	}
	if session.promptCacheSource != source || session.promptCacheController != controller {
		t.Fatal("prompt-cache resources must be owned by the attempt session")
	}
	if session.finalStreamObs != finalObs {
		t.Fatal("final stream observation must be owned by the attempt session")
	}
}

// activeControlToolActivation builds a genuinely active attempt-local control
// activation so replacement tests can prove the trusted owner is created per
// attempt and never reused across a replacement.
func activeControlToolActivation(t *testing.T, toolName string) *controlToolActivation {
	t.Helper()
	spec := controltool.Spec{
		Tool:         aipapi.ToolDef{Name: toolName, Parameters: json.RawMessage(`{"type":"object"}`)},
		Instruction:  controltool.Instruction{Role: aipapi.RoleSystem, Text: "Call " + toolName + " only when complete."},
		MaxArgsBytes: controltool.DefaultMaxArgsBytes,
	}
	_, projection, err := controltool.Project(aipapi.Call{
		ID:       "attempt-session-replacement",
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("work")}}},
	}, spec, aipapi.NewBackendCaps(aipapi.CapabilityTools))
	if err != nil {
		t.Fatalf("project generic control spec: %v", err)
	}
	if !projection.Active() {
		t.Fatal("control fixture must be eligible")
	}
	return &controlToolActivation{providerID: "generic.control." + toolName, projection: projection}
}

func TestAttemptSessionReplacementDoesNotReuseAttemptLocalResources(t *testing.T) {
	oldControl := activeControlToolActivation(t, "old_control")
	oldAccounting := newAttemptAccountingTracker(time.Unix(1, 0))
	oldAccounting.observeUsage(aipapi.Event{Kind: aipapi.EventUsageDelta, OutputTokens: 2})
	oldToolFinal := &toolCallAssembler{
		active:      map[string]*toolCallBuffer{"old": {}},
		passThrough: map[string]struct{}{"old": {}},
		completed:   map[string]struct{}{"old": {}},
		drain:       []aipapi.Event{{Kind: aipapi.EventToolCallFinished, ToolCallID: "old"}},
	}
	oldPromptSource := attemptSessionTestPromptSource{id: "old-backend"}
	oldPromptController := attemptSessionTestPromptController{id: "old-backend"}
	oldFinalObs := &extensions.FinalStreamObservationSession{}
	old := newAttemptSession(attemptSessionInput{
		accounting:            oldAccounting,
		toolFinal:             oldToolFinal,
		promptCacheSource:     oldPromptSource,
		promptCacheController: oldPromptController,
		finalStreamObs:        oldFinalObs,
		controlTool:           oldControl,
	})
	newAccounting := newAttemptAccountingTracker(time.Unix(2, 0))
	newToolFinal := &toolCallAssembler{}
	newPromptSource := attemptSessionTestPromptSource{id: "new-backend"}
	newPromptController := attemptSessionTestPromptController{id: "new-backend"}
	newFinalObs := &extensions.FinalStreamObservationSession{}
	replacement := newAttemptSession(attemptSessionInput{
		accounting:            newAccounting,
		toolFinal:             newToolFinal,
		promptCacheSource:     newPromptSource,
		promptCacheController: newPromptController,
		finalStreamObs:        newFinalObs,
	})

	var slot attemptSlot
	slot.mu.Lock()
	slot.current = old
	slot.mu.Unlock()
	ready := &readyAttempt{session: replacement, state: readyStatePrepared}
	if got, published := slot.swapIfOpen(ready); !published || got != old || slot.require() != replacement {
		t.Fatal("replacement must atomically publish the new attempt session")
	}
	old.discardSidebandState()
	old.finalStreamObs.Finish(context.Background(), response.OutcomeReplaced)

	if replacement.accounting.usageObserved {
		t.Fatal("replacement accounting inherited old provider usage")
	}
	if len(replacement.toolFinal.active) != 0 || len(replacement.toolFinal.passThrough) != 0 || len(replacement.toolFinal.completed) != 0 || len(replacement.toolFinal.drain) != 0 {
		t.Fatal("replacement tool finalizer inherited old buffered state")
	}
	if replacement.promptCacheSource == old.promptCacheSource || replacement.promptCacheController == old.promptCacheController {
		t.Fatal("replacement prompt-cache resources must come from the replacement backend")
	}
	if replacement.finalStreamObs == old.finalStreamObs {
		t.Fatal("replacement final observation must not reuse the old session")
	}
	if !oldControl.active() {
		t.Fatal("the replaced attempt must still own its own active control activation")
	}
	if replacement.controlTool != nil || replacement.controlTool.active() {
		t.Fatal("replacement must not inherit the replaced attempt's control-tool activation")
	}
	if old.toolCallAssembler() != nil {
		t.Fatal("old tool finalizer was not discarded during replacement cleanup")
	}
}

// testInstallSlot publishes a session directly for test setup, under proper lock.
// Production must use publishReady/swapIfOpen with ready capability.
func testInstallSlot(slot *attemptSlot, s *attemptSession) {
	if slot == nil {
		return
	}
	slot.mu.Lock()
	old := slot.current
	if s != nil && old != nil {
		if s.billingEnabled == nil {
			s.billingEnabled = old.billingEnabled
		}
		if s.operatorRateRef == nil {
			s.operatorRateRef = old.operatorRateRef
		}
		if s.billingWorkload == nil {
			s.billingWorkload = old.billingWorkload
		}
		if s.observeBillingLeg == nil {
			s.observeBillingLeg = old.observeBillingLeg
		}
		if s.appendBillingLeg == nil {
			s.appendBillingLeg = old.appendBillingLeg
		}
		if s.finalizeBilling == nil {
			s.finalizeBilling = old.finalizeBilling
		}
		if s.appendBillingLegFn == nil {
			s.appendBillingLegFn = old.appendBillingLegFn
		}
		if s.emitBackendEgressFn == nil {
			s.emitBackendEgressFn = old.emitBackendEgressFn
		}
		if s.recordAttemptLoggedFn == nil {
			s.recordAttemptLoggedFn = old.recordAttemptLoggedFn
		}
		if s.now == nil {
			s.now = old.now
		}
		if s.aScope == nil {
			s.aScope = old.aScope
		}
	}
	slot.current = s
	slot.mu.Unlock()
}

// testReadySession returns the underlying session for test assertions.
// Production must use narrow BLeg()/Candidate() etc.
func testReadySession(r *readyAttempt) *attemptSession {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session
}
