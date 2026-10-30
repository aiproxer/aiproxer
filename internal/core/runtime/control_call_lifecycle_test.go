// Attempt-lifecycle isolation and cleanup of the private control-call state for
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Concurrency and
// Lifecycle / Response Interception Capture State; requirements 4.7, 8.2-8.7,
// 10.3, 12.5).
//
// The interception seam owns one capture, one handoff, and one normalized outcome
// per attempt. This file certifies the lifetime half of that contract: every
// cancellation, close, loss, and replacement releases the bounded state, no
// losing attempt can keep or resurrect a private result, and a replacement owner
// starts from its own activation rather than an inherited call ID or argument
// buffer.
//
// Every case drives the seams the production paths use — the response
// preparation boundary, the attempt session lifecycle owner, the public
// retryRecvStream close path, the parallel loser release, and the replacement
// swap — so a cleanup satisfied only by a synthetic boolean fails here. Every
// state assertion reads the attempt's private control state under the same lock
// the production code uses, because lifecycle cleanup and the provider handoff
// run on different goroutines.
package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// controlSecretMarker is a low-entropy, obviously fake marker. Cases that must
// prove a private value never escapes use it, so secret scanning sees a planted
// test marker rather than an API-key-shaped literal.
const controlSecretMarker = "PRIVATE_PRIVATE_PRIVATE"

// controlDeadlockGuard bounds the two "cleanup must not wait" assertions. It is a
// deadlock guard for a cleanup that wrongly waits on a provider handoff, never a
// synchronization step: every ordering assertion in this file is made from
// channel barriers, and this timeout only turns a hang into a failure.
const controlDeadlockGuard = 30 * time.Second

func controlCompleteOutcome() controltool.Outcome {
	return controltool.Outcome{
		Kind:       controltool.OutcomeComplete,
		ResultText: "the " + controlSecretMarker + " answer",
		ReasonCode: "control_complete",
	}
}

var errControlHandoffEscaped = errors.New("a completed control call must stay inside the private protocol path")

// --- locked state inspection --------------------------------------------------

// controlRetained is one coherent snapshot of everything an attempt still holds
// about its private control call. It is read under the attempt's control lock,
// because lifecycle cleanup and a provider handoff run on different goroutines;
// a bare field read from the test goroutine would be a data race rather than an
// observation.
type controlRetained struct {
	capturePresent bool
	released       bool
	argsLen        int
	callID         string
	claimedN       int
	outcome        *controltool.Outcome
	handled        bool
}

func controlRetainedOf(a *attemptSession) controlRetained {
	if a == nil {
		return controlRetained{}
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	got := controlRetained{
		capturePresent: a.controlCapture != nil,
		released:       a.controlReleased,
		handled:        a.controlHandled,
	}
	if a.controlCapture != nil {
		got.argsLen = len(a.controlCapture.args)
		got.callID = a.controlCapture.callID
		got.claimedN = a.controlCapture.claimedN
	}
	if a.controlOutcome != nil {
		outcome := *a.controlOutcome
		got.outcome = &outcome
	}
	return got
}

// controlCorrelates reports whether this attempt still holds id as one of its
// bounded control correlation keys.
func controlCorrelates(a *attemptSession, id string) bool {
	if a == nil {
		return false
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.controlCapture == nil {
		return false
	}
	return a.controlCapture.correlates(id)
}

// controlOutcomeText returns the private pending result, or "" when there is
// none. It is a snapshot, so the caller never aliases attempt-owned storage.
func controlOutcomeText(a *attemptSession) string {
	if got := controlRetainedOf(a).outcome; got != nil {
		return got.ResultText
	}
	return ""
}

// controlRetainsNothing asserts the deterministic release property: no argument
// bytes, no raw call ID, no correlation digest, and no pending outcome survive.
func controlRetainsNothing(t *testing.T, a *attemptSession, what string) {
	t.Helper()
	got := controlRetainedOf(a)
	assert.True(t, got.released, "%s must mark the private control state released", what)
	assert.Zero(t, got.argsLen, "%s must release the private argument buffer", what)
	assert.Empty(t, got.callID, "%s must release the raw control call ID", what)
	assert.Zero(t, got.claimedN, "%s must release every correlation digest", what)
	assert.Nil(t, got.outcome, "%s must leave no private control result behind", what)
}

// controlNeverReachesOrdinaryPath asserts that no client-path observer saw any
// proxy-owned control traffic, in either direction.
func controlNeverReachesOrdinaryPath(t *testing.T, rig *interceptRig, released []aipapi.Event) {
	t.Helper()
	for name, seen := range map[string][]string{
		"finalizer":     rig.finalizer.finalized(),
		"tool_policy":   rig.policy.observed(),
		"reactor":       rig.reactor.observed(),
		"response_hook": rig.respHook.observed(),
		"client_events": interceptReleasedLabels(released),
		"ptc":           rig.traffic.observed(sdktraffic.LegPTC),
	} {
		assert.Zero(t, countContaining(seen, interceptToolName),
			"%s must never see a proxy-owned control call; seen=%v", name, seen)
	}
}

// controlConsumed sets the terminal defaults a consumed attempt carries in
// production, so a lifecycle call in these cases means what it means on the
// real stream.
func controlConsumed(a *attemptSession) *attemptSession {
	if a == nil {
		return nil
	}
	a.releaseKind = ""
	a.defaultCommand = sdkterminal.CommandCancel
	a.defaultLegOutcome = usageevidence.LegOutcomeCanceled
	return a
}

// --- fixtures -----------------------------------------------------------------

// controlFacts is the receive-loop fact set the response seam and the public
// stream share.
func controlFacts() recvTurnFacts {
	return testRecvTurnFacts(recvTurnFacts{
		traceID: "trace-intercept-1",
		aLegID:  "aleg-intercept-1",
		baseline: aipapi.Call{
			ID:    "request-control-1",
			Route: aipapi.RouteIntent{Selector: "control-a:model-a"},
			Invocation: aipapi.Invocation{
				Operation:    aipapi.OperationOpenAIChatCompletions,
				DeliveryMode: aipapi.DeliveryModeStreaming,
			},
			Messages: testMinimalUserMessages(),
		},
	})
}

// controlSession builds a real attempt session carrying a frozen activation, the
// same way the request path builds one at candidate open.
func controlSession(seq int, cand routing.AttemptCandidate, activation *controlToolActivation, inner aipapi.ManagedEventStream) *attemptSession {
	return controlConsumed(newAttemptSession(attemptSessionInput{
		inner:          inner,
		bleg:           b2bua.BLegRecord{ALegID: "aleg-intercept-1", BLegID: "bleg-intercept-1", Seq: seq},
		cand:           cand,
		controlTool:    activation,
		billingCallID:  usageevidence.BillingCallID("call-control-1"),
		finalStreamObs: &extensions.FinalStreamObservationSession{},
	}))
}

func controlCandidate(backend string) routing.AttemptCandidate {
	return routing.AttemptCandidate{Key: backend + ":model-a", Primary: routing.Primary{Backend: backend, Model: "model-a"}}
}

// controlPartialCapture drives a real, partially captured control call through
// the real response boundary and proves the attempt really holds the state the
// lifecycle cases then have to release.
func controlPartialCapture(t *testing.T, p *responsePipeline, attempt *attemptSession) {
	t.Helper()
	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		interceptStart("control-1", interceptToolName),
		interceptArgsDelta("control-1", `{"note":"half`),
	} {
		prepared := p.prepareRecvEvent(context.Background(), controlFacts(), attempt, ev, nil)
		require.NoError(t, prepared.err, "%s must not fail the response", ev.Kind)
		if ev.ToolName == interceptToolName || ev.ToolCallID != "" {
			require.True(t, prepared.swallowed, "%s belongs to the private protocol path", ev.Kind)
		} else {
			require.False(t, prepared.swallowed, "%s is ordinary traffic", ev.Kind)
		}
	}
	got := controlRetainedOf(attempt)
	require.Positive(t, got.argsLen, "the fixture must really hold a partial argument buffer")
	assert.Equal(t, "control-1", got.callID, "the fixture must really hold the raw control call ID")
	assert.Equal(t, 1, got.claimedN, "the fixture must really hold one correlation key")
}

// controlOpenCapture drives a control start carrying the complete bounded
// arguments, so a later finish is the one legitimate handoff for this attempt.
func controlOpenCapture(t *testing.T, p *responsePipeline, attempt *attemptSession) {
	t.Helper()
	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		interceptStart("control-1", interceptToolName),
		interceptArgsDelta("control-1", interceptArgs),
	} {
		prepared := p.prepareRecvEvent(context.Background(), controlFacts(), attempt, ev, nil)
		require.NoError(t, prepared.err, "%s must not fail the response", ev.Kind)
		require.Equal(t, ev.ToolCallID != "" || ev.ToolName == interceptToolName, prepared.swallowed,
			"only proxy-owned control traffic belongs to the private protocol path; %s", ev.Kind)
	}
	got := controlRetainedOf(attempt)
	require.Positive(t, got.argsLen, "the fixture must really hold the argument buffer")
	assert.Equal(t, "control-1", got.callID, "the fixture must really hold the raw control call ID")
}

// controlCompleteCapture drives one full, valid control call and returns the
// private result the later terminal owner would read.
func controlCompleteCapture(t *testing.T, p *responsePipeline, attempt *attemptSession) string {
	t.Helper()
	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		interceptStart("control-1", interceptToolName),
		interceptArgsDelta("control-1", interceptArgs),
		interceptFinish("control-1"),
	} {
		prepared := p.prepareRecvEvent(context.Background(), controlFacts(), attempt, ev, nil)
		require.NoError(t, prepared.err, "%s must not fail the response", ev.Kind)
		if ev.ToolName == interceptToolName || ev.ToolCallID != "" {
			require.True(t, prepared.swallowed, "%s belongs to the private protocol path", ev.Kind)
		} else {
			require.False(t, prepared.swallowed, "%s is ordinary traffic", ev.Kind)
		}
	}
	text := controlOutcomeText(attempt)
	require.NotEmpty(t, text, "the fixture must really hold a private result")
	return text
}

// controlBlockingProvider is the pinned provider with a deterministic barrier
// inside the handler, so a lifecycle cleanup can be observed while the provider
// call is still in flight.
type controlBlockingProvider struct {
	outcome controltool.Outcome
	err     error

	entered chan struct{}
	release chan struct{}
	once    sync.Once

	mu      sync.Mutex
	handled int
	calls   []controltool.CompletedCall
}

func newControlBlockingProvider() *controlBlockingProvider {
	return &controlBlockingProvider{
		outcome: controlCompleteOutcome(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (p *controlBlockingProvider) ID() string {
	panic("the response path must use the frozen provider identity")
}

func (p *controlBlockingProvider) Spec() controltool.Spec {
	panic("the response path must use the frozen projection, never a live spec read")
}

func (p *controlBlockingProvider) Handle(_ context.Context, call controltool.CompletedCall, _ controltool.Meta) (controltool.Outcome, error) {
	p.mu.Lock()
	p.handled++
	p.calls = append(p.calls, controltool.CompletedCall{
		ToolCallID: call.ToolCallID,
		ToolName:   call.ToolName,
		ArgsJSON:   append([]byte(nil), call.ArgsJSON...),
	})
	p.mu.Unlock()
	p.once.Do(func() { close(p.entered) })
	<-p.release
	if p.err != nil {
		return controltool.Outcome{}, p.err
	}
	return p.outcome, nil
}

func (p *controlBlockingProvider) handleCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handled
}

// observedCalls snapshots the calls the provider actually received, taken after
// the handoff goroutine is joined so the read never races the handler.
func (p *controlBlockingProvider) observedCalls() []controltool.CompletedCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]controltool.CompletedCall, len(p.calls))
	copy(out, p.calls)
	return out
}

// controlAwaitCleanup runs cleanup on its own goroutine and fails the test if it
// cannot finish, so a cleanup that holds or waits on the handoff is a failure
// rather than a hang.
func controlAwaitCleanup(t *testing.T, what string, cleanup func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		cleanup()
	}()
	select {
	case <-done:
	case <-time.After(controlDeadlockGuard):
		t.Fatalf("%s did not finish: cleanup must not wait on an in-flight provider handoff", what)
	}
}

// --- cases --------------------------------------------------------------------

// TestControlCallLifecycle_cancelCloseAndReplacementReleasePartialCapture is the
// deterministic release case for design Concurrency and Lifecycle: cancellation,
// the public stream close, and a replacement transition each drop the raw
// arguments, the raw call ID, every correlation digest, and any pending result,
// and none of them turns a captured call into ordinary client tool execution.
func TestControlCallLifecycle_cancelCloseAndReplacementReleasePartialCapture(t *testing.T) {
	t.Parallel()

	t.Run("cancel", func(t *testing.T) {
		t.Parallel()

		provider := &interceptProvider{outcome: controlCompleteOutcome()}
		rig := newInterceptRig(t, provider, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
		rig.attempt.storeInner(&errCancelCloseStream{})
		controlConsumed(rig.attempt)
		controlPartialCapture(t, rig.p, rig.attempt)

		rig.attempt.cancelViaLifecycle(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "client canceled"})

		controlRetainsNothing(t, rig.attempt, "an attempt cancellation")
		assert.Zero(t, provider.handleCount(), "cancellation must never invoke the control provider")
		assert.Empty(t, rig.finalizer.finalized(), "a canceled control call must never become ordinary client tool execution")
		assert.NotNil(t, rig.attempt.controlTool, "cancellation must not erase the immutable activation")
		controlNeverReachesOrdinaryPath(t, rig, nil)
	})

	t.Run("public_stream_close", func(t *testing.T) {
		t.Parallel()

		provider := &interceptProvider{outcome: controlCompleteOutcome()}
		activation := interceptActivation(t, provider, controltool.DefaultMaxArgsBytes)
		stream := stampStreamIdentity(&retryRecvStream{
			facts:            controlFacts(),
			attempt:          attemptSlot{current: controlSession(1, controlCandidate("control-a"), activation, &errCancelCloseStream{})},
			responsePipeline: newResponsePipeline(),
		})
		rig := newInterceptRigWithActivation(t, activation)
		rig.attempt = stream.attempt.require()
		controlPartialCapture(t, stream.responsePipeline, rig.attempt)

		require.NoError(t, stream.Close(), "the public stream close must succeed")

		controlRetainsNothing(t, stream.attempt.require(), "a public stream close")
		assert.Zero(t, provider.handleCount(), "close must never invoke the control provider")
		assert.Empty(t, rig.finalizer.finalized(), "a closed control call must never become ordinary client tool execution")
	})

	t.Run("replacement_transition", func(t *testing.T) {
		t.Parallel()

		provider := &interceptProvider{outcome: controlCompleteOutcome()}
		activation := interceptActivation(t, provider, controltool.DefaultMaxArgsBytes)
		rig := newInterceptRigWithActivation(t, activation)
		controlPartialCapture(t, rig.p, rig.attempt)

		// The real replacement seam: the retired attempt's private state is
		// released, then a fresh session built from the same trusted activation is
		// published for the new B-leg.
		replaced := rig.attempt
		clearAttemptToolState(rig.p, replaced)
		rig.attempt = controlSession(2, controlCandidate("control-b"), activation, &errCancelCloseStream{})

		// The replaced attempt released everything it held.
		controlRetainsNothing(t, replaced, "the attempt a replacement replaced")

		// The replacement owner starts from the trusted activation alone: a live,
		// unreleased capture, no correlation key, no argument bytes, no result.
		replacement := rig.attempt
		fresh := controlRetainedOf(replacement)
		assert.NotNil(t, replacement.controlCapture, "an active activation must still obtain a capture on the replacement owner")
		assert.False(t, fresh.released, "a replacement attempt must not start out released")
		assert.Zero(t, fresh.argsLen, "a replacement owner must inherit no argument buffer")
		assert.Empty(t, fresh.callID, "a replacement owner must inherit no raw control call ID")
		assert.Nil(t, fresh.outcome, "a replacement owner must inherit no private result")
		assert.False(t, fresh.handled, "a replacement owner must not inherit the replaced attempt's handoff")
		assert.False(t, controlCorrelates(replacement, "control-1"),
			"a replacement owner must inherit no correlation key from the attempt it replaced")
		assert.Zero(t, provider.handleCount(), "a replacement transition must never invoke the control provider")
	})
}

// TestControlCallLifecycle_normalCloseKeepsAValidResultAndDisposalDropsIt pins
// the difference the terminal owner depends on: a normal response finish keeps a
// valid private result, and the attempt's own terminal cleanup is what discards
// it. Nothing else may discard a valid completion, and nothing may keep it past
// the attempt.
func TestControlCallLifecycle_normalCloseKeepsAValidResultAndDisposalDropsIt(t *testing.T) {
	t.Parallel()

	provider := &interceptProvider{outcome: controlCompleteOutcome()}
	rig := newInterceptRig(t, provider, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
	rig.attempt.storeInner(&errCancelCloseStream{})
	ctx := context.Background()

	text := controlCompleteCapture(t, rig.p, rig.attempt)
	require.Equal(t, 1, provider.handleCount(), "the handler runs exactly once per response")

	prepared := rig.p.prepareRecvEvent(ctx, controlFacts(), rig.attempt, aipapi.Event{Kind: aipapi.EventResponseFinished}, nil)
	require.NoError(t, prepared.err, "a normal response finish must not fail the response")
	assert.False(t, prepared.swallowed, "the terminal event itself is ordinary and keeps streaming")

	assert.Equal(t, text, controlOutcomeText(rig.attempt),
		"a normal response finish must preserve a valid private result for the terminal owner")
	retained := controlRetainedOf(rig.attempt)
	assert.True(t, retained.handled, "the single permitted handoff is recorded")
	assert.Zero(t, retained.argsLen, "the handed-off argument buffer is released at the capture")

	// The attempt's own terminal cleanup is the disposal point.
	rig.attempt.TerminalizeAttempt(ctx, IntentSuccess, attemptEvidence{
		Command:    sdkterminal.CommandNormalFinish,
		LegOutcome: usageevidence.LegOutcomeWinner,
	})

	controlRetainsNothing(t, rig.attempt, "an attempt's terminal cleanup")
	assert.Equal(t, 1, provider.handleCount(), "terminal cleanup must never re-invoke the provider")
}

// TestControlCallLifecycle_cleanupDuringInFlightHandlerNeitherWaitsNorResurrects
// is the concurrency case. Lifecycle cleanup runs while the pinned provider is
// still inside the handoff: it must not wait for the handler, and when the
// handler finally returns it must not restore a result onto an attempt that no
// longer owns protocol state.
func TestControlCallLifecycle_cleanupDuringInFlightHandlerNeitherWaitsNorResurrects(t *testing.T) {
	t.Parallel()

	provider := newControlBlockingProvider()
	rig := newInterceptRigWithActivation(t, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
	ctx := context.Background()
	// A start with its complete bounded arguments, so the finish below is the one
	// legitimate handoff and the only event that can reach the provider.
	controlOpenCapture(t, rig.p, rig.attempt)

	// The receive-side handoff runs on its own goroutine, exactly like the backend
	// Recv loop, and parks inside the pinned provider.
	handoff := make(chan error, 1)
	go func() {
		swallowed, err := rig.p.divertControlCall(ctx, rig.attempt, interceptFinish("control-1"))
		if !swallowed {
			handoff <- errControlHandoffEscaped
			return
		}
		handoff <- err
	}()

	select {
	case <-provider.entered:
	case <-time.After(controlDeadlockGuard):
		t.Fatal("the pinned provider was never reached")
	}
	assert.True(t, controlRetainedOf(rig.attempt).handled,
		"the in-flight handoff must already be claimed before cleanup runs")

	// Cleanup must complete while the handler is still blocked.
	controlAwaitCleanup(t, "cleanup during an in-flight control handoff", func() {
		clearAttemptToolState(rig.p, rig.attempt)
	})
	controlRetainsNothing(t, rig.attempt, "cleanup racing an in-flight handoff")

	close(provider.release)
	select {
	case err := <-handoff:
		require.NoError(t, err, "the handoff itself stays a private protocol event")
	case <-time.After(controlDeadlockGuard):
		t.Fatal("the control handoff did not return")
	}

	assert.Equal(t, 1, provider.handleCount(), "the provider must have run exactly once")
	controlRetainsNothing(t, rig.attempt, "a handoff that returned after cleanup")
	assert.NotContains(t, controlOutcomeText(rig.attempt), controlSecretMarker,
		"a handler that returns after cleanup must not restore the private result")

	// The provider itself saw a valid, fully owned call: the result was dropped
	// because the attempt lost ownership, not because the handoff was malformed.
	calls := provider.observedCalls()
	require.Len(t, calls, 1, "the in-flight handoff must have reached the pinned provider exactly once")
	assert.Equal(t, "control-1", calls[0].ToolCallID)
	assert.Equal(t, interceptArgs, string(calls[0].ArgsJSON),
		"the handed-off call must own its argument bytes independently of the released capture")

	// The closed owner fails closed: no reopen, and no fall-through to ordinary
	// client execution.
	released, err := rig.drive(ctx, interceptStart("control-1", interceptToolName))
	require.Error(t, err, "an event that reaches a released owner must fail closed rather than continue")
	assert.Empty(t, released, "a released owner must never release proxy-owned control traffic to the client")
	assert.Empty(t, rig.finalizer.finalized(), "a released owner must never fall through to ordinary tool execution")
}

// TestControlCallLifecycle_repeatedCleanupIsIdempotentAndNeverReopens pins the
// cleanup entry point under repetition: the release is idempotent, it does not
// reopen the capture, and it never re-arms the handoff bookkeeping it consumed.
// The owner stays visible afterwards, so late ownership is never hidden.
func TestControlCallLifecycle_repeatedCleanupIsIdempotentAndNeverReopens(t *testing.T) {
	t.Parallel()

	provider := &interceptProvider{outcome: controlCompleteOutcome()}
	rig := newInterceptRig(t, provider, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
	ctx := context.Background()
	controlPartialCapture(t, rig.p, rig.attempt)

	// The same cleanup runs again from every lifecycle owner: the terminal effect
	// tail and the replacement seam both reach it.
	for range 3 {
		clearAttemptToolState(rig.p, rig.attempt)
		rig.attempt.discardSidebandState()
	}

	controlRetainsNothing(t, rig.attempt, "repeated cleanup")
	assert.NotNil(t, rig.attempt.controlCapture, "repeated cleanup must keep the owner visible instead of hiding late ownership")
	assert.NotNil(t, rig.attempt.controlTool, "repeated cleanup must not erase the immutable activation")

	released, err := rig.drive(ctx, interceptArgsDelta("control-1", interceptArgs))
	require.Error(t, err, "a released owner must fail closed on any further control event")
	assert.Empty(t, released, "a released owner must never release control traffic to the client")
	assert.Zero(t, provider.handleCount(), "a released owner must never reopen the capture and invoke the provider again")
	controlNeverReachesOrdinaryPath(t, rig, released)
}

// TestControlCallLifecycle_parallelLoserDisposalCannotAffectTheWinner pins design
// Concurrency and Lifecycle: parallel candidates hold independent activations and
// captures, and disposing the loser releases only the loser's state. The winner
// keeps its own private result, and the loser's call ID stays unknown to it.
func TestControlCallLifecycle_parallelLoserDisposalCannotAffectTheWinner(t *testing.T) {
	t.Parallel()

	winnerProvider := &interceptProvider{outcome: controlCompleteOutcome()}
	loserProvider := &interceptProvider{outcome: controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: "loser_invalid"}}
	winnerActivation := interceptActivation(t, winnerProvider, controltool.DefaultMaxArgsBytes)
	loserActivation := interceptActivation(t, loserProvider, controltool.DefaultMaxArgsBytes)
	winner := controlSession(1, controlCandidate("control-winner"), winnerActivation, &errCancelCloseStream{})
	loser := controlSession(2, controlCandidate("control-loser"), loserActivation, &errCancelCloseStream{})
	loserRig := newInterceptRigWithActivation(t, loserActivation)
	loserRig.attempt = loser
	winnerRig := newInterceptRigWithActivation(t, winnerActivation)
	winnerRig.attempt = winner

	require.NotSame(t, winner.controlCapture, loser.controlCapture,
		"two racing attempts must never share one capture")
	require.NotSame(t, winner.controlTool, loser.controlTool,
		"two racing attempts must hold independent activations")

	// The loser opens a control call and never finishes it, under its own ID,
	// while the winner completes one. Both private states exist at the same time.
	loserCtx := context.Background()
	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		interceptStart("loser-control-1", interceptToolName),
		interceptArgsDelta("loser-control-1", `{"note":"loser`),
	} {
		prepared := loserRig.p.prepareRecvEvent(loserCtx, controlFacts(), loser, ev, nil)
		require.NoError(t, prepared.err)
		if ev.ToolCallID != "" {
			require.True(t, prepared.swallowed, "%s belongs to the private protocol path", ev.Kind)
		}
	}
	require.Positive(t, controlRetainedOf(loser).argsLen, "the loser must really hold a partial capture")
	winnerText := controlCompleteCapture(t, winnerRig.p, winner)

	// The real parallel loser release, through a real ready attempt.
	loserReady := newReadyAttempt(loser, pendingSelectionEffects{})
	ex := &Executor{}
	require.NoError(t, ex.releaseLosers(loserCtx, nil, []*parallelLeg{{cand: controlCandidate("control-loser"), ready: loserReady}}))

	controlRetainsNothing(t, loser, "a parallel loser disposal")
	assert.Zero(t, loserProvider.handleCount(), "a losing attempt must never invoke its own provider")
	assert.True(t, loserReady.IsConsumed(), "loser disposal consumes the ready attempt exactly once")

	assert.Equal(t, winnerText, controlOutcomeText(winner),
		"the winning attempt must keep its own private result through a loser's disposal")
	assert.False(t, controlCorrelates(winner, "loser-control-1"),
		"a winner must never inherit a losing attempt's correlation key")
	assert.Equal(t, 1, winnerProvider.handleCount(), "only the winning attempt may use its private result")

	// The winner's own completion is still the only private result in play.
	released, err := winnerRig.drive(loserCtx, aipapi.Event{Kind: aipapi.EventResponseFinished})
	require.NoError(t, err, "the winning attempt keeps its own lifecycle")
	assert.Equal(t, winnerText, controlOutcomeText(winner),
		"a normal response finish must keep the winner's private result")
	controlNeverReachesOrdinaryPath(t, winnerRig, released)
}

// TestControlCallLifecycle_cancelRevokesAPendingResultWithoutReinvoking pins the
// authoritative cancellation rule (requirements 8.5, 8.6): a client cancellation
// is not missing completion work, so the private result is released with the
// attempt and the provider is never asked again.
func TestControlCallLifecycle_cancelRevokesAPendingResultWithoutReinvoking(t *testing.T) {
	t.Parallel()

	provider := &interceptProvider{outcome: controlCompleteOutcome()}
	rig := newInterceptRig(t, provider, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
	rig.attempt.storeInner(&errCancelCloseStream{})
	controlConsumed(rig.attempt)
	text := controlCompleteCapture(t, rig.p, rig.attempt)
	require.Equal(t, text, controlOutcomeText(rig.attempt))

	rig.attempt.cancelViaLifecycle(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "client canceled"})

	controlRetainsNothing(t, rig.attempt, "a client cancellation")
	assert.Equal(t, 1, provider.handleCount(), "cancellation must never re-invoke the control provider")
	controlNeverReachesOrdinaryPath(t, rig, nil)
}
