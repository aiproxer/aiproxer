// Four-finish-route, drain-stranding, and cancellation regressions for the pending
// completion-result publication (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Completion Evidence
// and Pending Result / Concurrency and Lifecycle; requirements 6.3-6.7, 11.3, 11.5,
// 12.5).
//
// The accepted terminal can be reached from four distinct finish routes: the
// ordinary raw finish, the completion-gate finish preflight, the recovery drain,
// and the gate drain. Every one of them must converge on the same pending
// publication, release the accepted finish exactly once, and never strand the
// private release queue behind Recv EOF.
//
// Every ordering assertion here is made from a channel barrier or from recorded
// event order. No case sleeps to coordinate a race.
package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// pendingRouteGuard bounds the drain assertions. It only turns a hang into a
// failure; every ordering assertion is made from recorded order or a channel
// barrier, never from this timeout.
const pendingRouteGuard = 30 * time.Second

// pendingRouteResult is the bounded completion result every route case publishes.
const pendingRouteResult = "route bounded answer"

// pendingRouteStream replays a fixed event list through the real receive loop.
type pendingRouteStream struct {
	events []aipapi.Event
	fail   error
	idx    int
}

func (s *pendingRouteStream) Recv(context.Context) (aipapi.Event, error) {
	if s.idx < len(s.events) {
		ev := s.events[s.idx]
		s.idx++
		return ev, nil
	}
	if s.fail != nil {
		return aipapi.Event{}, s.fail
	}
	return aipapi.Event{}, io.EOF
}

func (*pendingRouteStream) Close() error { return nil }

func (*pendingRouteStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// pendingRouteStream builds one real receive stream with a valid pending completion
// on its attempt, then drives it exactly like a caller would.
type pendingRouteRig struct {
	stream *retryRecvStream
	pipe   *responsePipeline
	term   *turnTerminal
}

func newPendingRouteRig(t *testing.T, events []aipapi.Event, fail error) *pendingRouteRig {
	t.Helper()
	_, from := setupInterleavedAuthorityContinuation(t, pendingThinkerAuthority(), "hidden")
	from.terminal.setInterleavedThinker()
	attempt := from.attempt.snapshot()
	attempt.controlCapture = newControlCallCapture(controlCaptureActivation(t))
	require.True(t, attempt.storeControlOutcome(controltool.Outcome{
		Kind: controltool.OutcomeComplete, ResultText: pendingRouteResult, ReasonCode: "control_complete",
	}))
	testStoreInner(from, &pendingRouteStream{events: events, fail: fail})
	return &pendingRouteRig{stream: from, pipe: from.responsePipeline, term: from.terminal}
}

func pendingRouteCompletionOnly() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventResponseFinished},
	}
}

// drain reads the stream to its end and returns every event the client received.
func pendingRouteDrain(t *testing.T, stream *retryRecvStream) []aipapi.Event {
	t.Helper()
	var released []aipapi.Event
	for range 64 {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			break
		}
		released = append(released, ev)
	}
	return released
}

func pendingRouteLabels(events []aipapi.Event) []string { return pendingThinkerLabels(events) }

func pendingRouteCountText(events []aipapi.Event, want string) int {
	count := 0
	for _, ev := range events {
		if ev.Kind == aipapi.EventTextDelta && ev.Delta == want {
			count++
		}
	}
	return count
}

func pendingRouteIndexOf(events []aipapi.Event, kind aipapi.EventKind) int {
	for i, ev := range events {
		if ev.Kind == kind {
			return i
		}
	}
	return -1
}

// TestPendingRoute_ordinaryFinishRoutePublishesTheResultOnce pins the ordinary raw
// finish route: the accepted result reaches the client exactly once, strictly
// before the finish, and the stream ends cleanly.
func TestPendingRoute_ordinaryFinishRoutePublishesTheResultOnce(t *testing.T) {
	t.Parallel()

	rig := newPendingRouteRig(t, pendingRouteCompletionOnly(), nil)
	released := pendingRouteDrain(t, rig.stream)

	require.NoError(t, aipapi.ValidateEventSequence(released),
		"the published result must keep the canonical sequence legal; released=%v", pendingRouteLabels(released))
	assert.Equal(t, 1, pendingRouteCountText(released, pendingRouteResult),
		"the accepted terminal must publish the result exactly once; released=%v", pendingRouteLabels(released))
	textAt, finishAt := pendingRouteIndexOf(released, aipapi.EventTextDelta), pendingRouteIndexOf(released, aipapi.EventResponseFinished)
	require.GreaterOrEqual(t, textAt, 0, "the result must be released; released=%v", pendingRouteLabels(released))
	require.GreaterOrEqual(t, finishAt, 0, "the finish must be released; released=%v", pendingRouteLabels(released))
	assert.Less(t, textAt, finishAt, "the result must precede the finish; released=%v", pendingRouteLabels(released))
	assert.Equal(t, 1, pendingRouteIndexCount(released, aipapi.EventResponseFinished),
		"the accepted finish must be released exactly once; released=%v", pendingRouteLabels(released))
}

// TestPendingRoute_releaseQueueIsDrainedBeforeEOF pins the drain-starvation rule: a
// private release queue is never stranded behind a finished stream, so every
// prepared event reaches the client before Recv reports the end.
func TestPendingRoute_releaseQueueIsDrainedBeforeEOF(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, pendingRouteResult)
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)
	claimPublication(t, p, prep.prepared)
	reservation, ok := p.reservePendingPublication(prep.prepared, false)
	require.True(t, ok, "the claimed candidate must be reservable")
	require.True(t, p.stageReservedPublication(reservation, attempt, []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: pendingRouteResult},
		{Kind: aipapi.EventResponseFinished},
	}, aipapi.Event{}, pendingCustomerAbsent), "the live reservation must install its own batch")
	require.True(t, p.activateReservedPendingPublication(prep.prepared, attempt),
		"a queue-only fixture activates its staged batch explicitly")

	var released []aipapi.Event
	for {
		ev, ok := p.popPendingCompletionRelease()
		if !ok {
			break
		}
		released = append(released, ev)
	}
	require.NoError(t, aipapi.ValidateEventSequence(released),
		"a fully drained publication must be a legal canonical sequence; released=%v", pendingRouteLabels(released))
	assert.Equal(t, 1, pendingRouteCountText(released, pendingRouteResult),
		"a fully drained publication must contain the result exactly once")
}

// pendingRouteCloseBarrier is one Close racing the private preparation. It proves a
// deterministic barrier, not a sleep: the release channel is closed only after the
// preparation has been observed entering, and the assertion is made on the closed
// fence result.
type pendingRouteCloseBarrier struct {
	mu       sync.Mutex
	entered  chan struct{}
	release  chan struct{}
	closed   bool
	once     sync.Once
	fenceHit int
}

func newPendingRouteCloseBarrier() *pendingRouteCloseBarrier {
	return &pendingRouteCloseBarrier{entered: make(chan struct{}), release: make(chan struct{})}
}

// fence reports the receive-side publication fence: a closed publication window
// means the value must not be published. It blocks on the barrier release so the
// case can establish the Close winner at a deterministic point.
func (b *pendingRouteCloseBarrier) fence() bool {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fenceHit++
	return !b.closed
}

func (b *pendingRouteCloseBarrier) fenceHits() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.fenceHit
}

// pendingRouteRetainedCandidate runs the REAL preparation seam so the response
// genuinely RETAINS this candidate before the fence is consulted.
//
// A pre-staging withdrawal disposes the expected OWNER's retained state, so a
// candidate only the caller knows about would prove nothing: the disposition and
// the conservative observer finish it triggers are exactly the facts under test.
func pendingRouteRetainedCandidate(t *testing.T, p *responsePipeline, attempt *attemptSession) *pendingCompletion {
	t.Helper()
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared, "the real preparation must really retain a candidate")
	require.Same(t, prep.prepared, p.pendingPreparedSnapshot(),
		"the candidate a withdrawal disposes must be the state the response really retains")
	return prep.prepared
}

// TestPendingRoute_closeWinnerBeforePublicationSuppressesTheResult pins the Close
// winner rule: once the publication window is closed, the fence refuses the
// publication and no result is released, even though a valid result existed. A
// refused expected publication is reported as a WITHDRAWAL, so settlement and
// billing handoff stop instead of continuing for a batch that will not be released.
func TestPendingRoute_closeWinnerBeforePublicationSuppressesTheResult(t *testing.T) {
	t.Parallel()

	barrier := newPendingRouteCloseBarrier()
	attempt := pendingDrainAttempt(t, pendingRouteResult)
	observer := pendingOpenObserver(t, attempt)
	terminal := newTurnTerminal()
	p := newResponsePipeline()
	recorder := &pendingFailingRecorder{}
	p.secureSessionRecorder = recorder

	// The barrier's fence closes itself before the first publication check returns,
	// so the Close winner is already established at that boundary.
	barrier.closed = true
	close(barrier.release)
	require.ErrorIs(t, terminal.stagePendingCompletion(
		context.WithoutCancel(context.Background()), attempt, p, pendingDrainFacts().terminalFacts(),
		pendingPublication{
			prepared:    pendingRouteRetainedCandidate(t, p, attempt),
			facts:       pendingDrainFacts(),
			callerCtx:   context.Background(),
			publishable: barrier.fence,
		}), errPendingPublicationWithdrawn,
		"a closed fence must report the withdrawal, never a success that settles a batch nobody will release")

	_, ok := p.popPendingCompletionRelease()
	assert.False(t, ok, "a Close winner must leave no client result")
	assert.Empty(t, recorder.recordedKinds(),
		"the fence must be consulted before any recorded, remembered, or queued work")
	assert.Nil(t, p.pendingPreparedSnapshot(),
		"a Close winner must dispose the retained candidate instead of stranding it")
	assert.Equal(t, 1, observer.finishCount(),
		"the abandoned publication must finish the deferred observer exactly once")
	assert.Equal(t, 1, barrier.fenceHits(),
		"the fence must be consulted before any recorded, remembered, or queued work")
}

// pendingRouteCancelBarrier cancels the live caller context while the publication is
// fenced, so the fence must observe the cancellation the detached terminal cleanup
// context would otherwise mask.
type pendingRouteCancelBarrier struct {
	entered chan struct{}
	release chan struct{}
	cancel  context.CancelFunc
	ctx     context.Context
	once    sync.Once
}

func newPendingRouteCancelBarrier(ctx context.Context) *pendingRouteCancelBarrier {
	cancelCtx, cancel := context.WithCancel(ctx)
	return &pendingRouteCancelBarrier{entered: make(chan struct{}), release: make(chan struct{}), cancel: cancel, ctx: cancelCtx}
}

func (b *pendingRouteCancelBarrier) fence() bool {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return b.ctx.Err() == nil
}

func (b *pendingRouteCancelBarrier) awaitEntry(t *testing.T) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(pendingRouteGuard):
		t.Fatal("the publication fence was never reached")
	}
}

func (b *pendingRouteCancelBarrier) unblock() { close(b.release) }

// TestPendingRoute_cancelledCallerIsNotMaskedByTheDetachedContext pins the
// cancellation fence: a client cancellation that already won suppresses the
// publication, even though the terminal owner runs request effects on its own
// detached cleanup context, and the suppression is reported as a withdrawal so no
// settlement or billing handoff proceeds for it.
func TestPendingRoute_cancelledCallerIsNotMaskedByTheDetachedContext(t *testing.T) {
	t.Parallel()

	callerCtx := t.Context()
	barrier := newPendingRouteCancelBarrier(callerCtx)
	attempt := pendingDrainAttempt(t, pendingRouteResult)
	observer := pendingOpenObserver(t, attempt)
	terminal := newTurnTerminal()
	p := newResponsePipeline()
	recorder := &pendingFailingRecorder{}
	p.secureSessionRecorder = recorder

	published := make(chan error, 1)
	go func() {
		published <- terminal.stagePendingCompletion(
			context.WithoutCancel(context.Background()), attempt, p, pendingDrainFacts().terminalFacts(),
			pendingPublication{
				prepared:    pendingRouteRetainedCandidate(t, p, attempt),
				facts:       pendingDrainFacts(),
				callerCtx:   callerCtx,
				publishable: barrier.fence,
			})
	}()
	barrier.awaitEntry(t)
	barrier.cancel()
	barrier.unblock()

	select {
	case err := <-published:
		require.ErrorIs(t, err, errPendingPublicationWithdrawn,
			"a cancelled caller must report the withdrawal, never a success that settles a batch nobody will release")
	case <-time.After(pendingRouteGuard):
		t.Fatal("the publication did not finish after cancellation")
	}

	_, ok := p.popPendingCompletionRelease()
	assert.False(t, ok, "a cancelled caller must leave no client result")
	assert.Empty(t, recorder.recordedKinds(),
		"a cancelled caller must be observed before any recorded work")
	assert.Nil(t, p.pendingPreparedSnapshot(),
		"a cancelled caller must dispose the retained candidate instead of stranding it")
	assert.Equal(t, 1, observer.finishCount(),
		"the abandoned publication must finish the deferred observer exactly once")
}

// pendingRouteGateChainGate is a pass gate whose call count proves the response was
// gated exactly once on the finish route.
type pendingRouteGateChainGate struct{ calls int }

func (g *pendingRouteGateChainGate) ID() string { return "pending-route-gate" }

func (g *pendingRouteGateChainGate) Order() int { return 0 }

func (g *pendingRouteGateChainGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (g *pendingRouteGateChainGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	g.calls++
	return completion.PassOriginalOutcome(), nil
}

// TestPendingRoute_gateFinishRoutePublishesThroughTheGatedCandidate pins the gated
// finish route: the pending result participates in the existing gate chain exactly
// once and reaches the client exactly once before the finish.
func TestPendingRoute_gateFinishRoutePublishesThroughTheGatedCandidate(t *testing.T) {
	t.Parallel()

	gate := &pendingRouteGateChainGate{}
	p := newResponsePipeline()
	p.completionBufferLimits = completion.BufferLimits{MaxEvents: 64}
	attempt := pendingDrainAttempt(t, pendingRouteResult)

	gated := p.applyCompletionGates(context.Background(), []completion.Gate{gate}, pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, false)
	require.NoError(t, gated.err)
	require.NotNil(t, gated.pending,
		"the gated finish route must converge on the pending publication")
	assert.True(t, gated.finishPreflight,
		"the gated finish route still owns the terminal chokepoint call")
	assert.Equal(t, 1, gate.calls, "the gate chain must run exactly once for the whole response")
	assert.Equal(t, aipapi.EventResponseStarted, gated.pending.events[0].Kind,
		"the held candidate must start with the buffered ordinary lifecycle")
	assert.Equal(t, 1, pendingRouteCountText(gated.pending.events, pendingRouteResult),
		"the held candidate must contain the eligible result exactly once")
	assert.Nil(t, p.gateBuf, "the prepared value owns the whole evaluated sequence")
	assert.Nil(t, p.gateDrain, "the prepared value must not install an ordinary gated drain")
}

// TestPendingRoute_gateLivePassthroughPreservesOrdinaryOutput pins the live/overflow
// half: once the completion gate already failed open to live passthrough, the
// prepared value carries only the bounded terminal suffix and the ordinary output
// that already streamed is untouched.
func TestPendingRoute_gateLivePassthroughPreservesOrdinaryOutput(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	p.gateLive = true
	attempt := pendingDrainAttempt(t, pendingRouteResult)

	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)
	assert.True(t, prep.prepared.publishing(),
		"the live gate must not suppress an otherwise eligible result")
	assert.Equal(t, 1, pendingRouteCountText(prep.prepared.events, pendingRouteResult),
		"the live gate must prepare the result exactly once")
	assert.Nil(t, p.gateBuf, "the prepared value owns the terminal suffix")
}

// TestPendingRoute_repeatedTerminalCallsNeverRepublishTheResult pins the repeated
// terminal call rule: a second accepted-terminal attempt on the same prepared value
// finds nothing to publish, so the result never reaches the client twice.
func TestPendingRoute_repeatedTerminalCallsNeverRepublishTheResult(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, pendingRouteResult)
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)

	claimPublication(t, p, prep.prepared)
	again, claimedAgain := p.takePendingPublication()
	assert.False(t, claimedAgain, "a repeated terminal call must find nothing to publish")
	assert.Nil(t, again, "a repeated terminal call must not hand out the candidate twice")

	reservation, reserved := p.reservePendingPublication(prep.prepared, false)
	require.True(t, reserved, "the claimed candidate must be reservable")
	require.True(t, p.stageReservedPublication(reservation, attempt, prep.prepared.events, aipapi.Event{}, pendingCustomerAbsent),
		"the live reservation must install its own batch")
	require.True(t, p.activateReservedPendingPublication(prep.prepared, attempt),
		"a queue-only fixture activates the staged batch explicitly")
	var delivered int
	for {
		if _, ok := p.popPendingCompletionRelease(); !ok {
			break
		}
		delivered++
	}
	assert.Equal(t, 1, pendingRouteCountText(prep.prepared.events, pendingRouteResult),
		"the prepared sequence must contain the result exactly once")
	assert.Equal(t, len(prep.prepared.events), delivered,
		"every prepared event must be delivered exactly once")
}

func pendingRouteIndexCount(events []aipapi.Event, kind aipapi.EventKind) int {
	count := 0
	for _, ev := range events {
		if ev.Kind == kind {
			count++
		}
	}
	return count
}

// TestPendingRoute_thinkerTerminalDoesNotSettleCustomerRequestAuthority pins the
// thinker authority split: an accepted thinker B-leg terminal must not settle
// customer request authority or end the shared A-leg, because the executor owns both.
func TestPendingRoute_thinkerTerminalDoesNotSettleCustomerRequestAuthority(t *testing.T) {
	t.Parallel()

	rig := newPendingRouteRig(t, pendingRouteCompletionOnly(), nil)
	require.True(t, rig.term.isInterleavedThinker(), "the fixture must be an interleaved thinker")
	_ = pendingRouteDrain(t, rig.stream)
	assert.False(t, rig.term.finished(),
		"the thinker must not complete the shared request terminal")
}

// TestPendingRoute_customerUsageCountsTheResultWhileOperatorUsageDoesNot pins the
// plane split on the REAL receive loop: the accepted result is ordinary
// customer-visible assistant content, so the customer projection counts it, while
// the operator/B-leg authority usage is preserved exactly as the real preparation
// produced it and the publication never re-derives it.
//
// The delivery runs through real retryRecvStream.Recv, because staging alone
// releases nothing: a customer-release assertion driven off a private queue would
// not prove the client actually received the result.
func TestPendingRoute_customerUsageCountsTheResultWhileOperatorUsageDoesNot(t *testing.T) {
	t.Parallel()

	collector := &pendingRealCollector{
		callCount: accountingapp.CountResult{InputTokens: 2, TotalTokens: 2},
		outCount:  accountingapp.CountResult{OutputTokens: 7, TotalTokens: 9},
	}
	fixture := newPendingRealStream(t, pendingRealOptions{
		collector: collector,
		result:    pendingRouteResult,
		events:    pendingRouteCompletionOnly(),
	})
	pipe := fixture.pipe

	released, err := fixture.drain(t)
	require.True(t, errors.Is(err, io.EOF),
		"a fully delivered private publication ends the stream at EOF; released=%v err=%v",
		pendingRouteLabels(released), err)
	require.NoError(t, aipapi.ValidateEventSequence(released),
		"the published result must keep the canonical sequence legal; released=%v", pendingRouteLabels(released))

	// The operator/B-leg authority evidence is preserved exactly as the real
	// preparation produced it: no rerun of token accounting or backend observation
	// happened for the publication.
	authority := pipe.lastAuthorityUsageSnapshot()
	require.NotEqual(t, aipapi.EventKind(""), authority.Kind, "the real operator authority usage must exist")
	assert.Equal(t, 7, authority.OutputTokens,
		"the accepted publication must not change the prepared operator authority usage")

	// The customer plane now sees the released result text.
	assert.Equal(t, pendingRouteResult, pipe.releasedOutputText(),
		"the published result must be ordinary released customer content")

	// The customer plane was explicitly refreshed from the accepted post-hook,
	// post-gate content, and it is the released result text that refreshes it.
	var reconstructedOutput string
	var reconstructedCalls int
	pipe.bindCustomerUsage(func(_ context.Context, text string, _ []aipapi.Event) aipapi.Event {
		reconstructedOutput, reconstructedCalls = text, reconstructedCalls+1
		return aipapi.Event{Kind: aipapi.EventUsageDelta, OutputTokens: len(text), TotalTokens: len(text)}
	})
	customer := pipe.resolveCustomerUsageForTerminal(context.Background(), authority, pendingDrainFacts().terminalFacts())
	assert.Equal(t, 1, reconstructedCalls,
		"the customer plane must be resolved from the released content exactly once")
	assert.Equal(t, pendingRouteResult, reconstructedOutput,
		"the customer plane must count the accepted result text")
	assert.Equal(t, len(pendingRouteResult), customer.OutputTokens,
		"the customer projection must reflect the accepted result, not the prepared operator usage")
	assert.Equal(t, aipapi.EventUsageDelta, pipe.lastCustomerUsageSnapshot().Kind,
		"the customer projection must be refreshed before any customer usage release")

	textAt := pendingRouteIndexOf(released, aipapi.EventTextDelta)
	require.GreaterOrEqual(t, textAt, 0, "the result must be released; released=%v", pendingRouteLabels(released))
	assert.Equal(t, pendingRouteResult, released[textAt].Delta,
		"the released result must be the bounded completion text")
}
