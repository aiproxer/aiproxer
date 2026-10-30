// Real receive-loop integration regressions for the pending completion-result
// publication (spec: .kiro/specs/agent-loop-explicit-completion-protocol, design
// Completion Evidence and Pending Result / Response Interception Placement /
// Concurrency and Lifecycle; requirements 6.3-6.7, 11.3, 11.5, 12.5).
//
// Every case here drives the REAL retryRecvStream.Recv through the real terminal
// owner with real token accounting, real customer reconstruction, real
// recording, and real observation. Nothing seeds private preparation state, and
// nothing installs a reconstructor after publication: a production wiring defect
// therefore fails here instead of hiding behind a helper fixture.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	secureapp "github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// pendingRealResult is the bounded completion result the real receive-loop cases
// publish. It is a low-entropy, obviously fake marker.
const pendingRealResult = "real bounded answer"

// pendingRealCountText counts text deltas that carry want.
func pendingRealCountText(events []aipapi.Event, want string) int {
	count := 0
	for _, ev := range events {
		if ev.Kind == aipapi.EventTextDelta && ev.Delta == want {
			count++
		}
	}
	return count
}

func pendingRealIndexOf(events []aipapi.Event, kind aipapi.EventKind) int {
	for i, ev := range events {
		if ev.Kind == kind {
			return i
		}
	}
	return -1
}

func pendingRealCountKind(events []aipapi.Event, kind aipapi.EventKind) int {
	count := 0
	for _, ev := range events {
		if ev.Kind == kind {
			count++
		}
	}
	return count
}

func pendingRealLabels(events []aipapi.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventTextDelta:
			out = append(out, "text:"+ev.Delta)
		case aipapi.EventUsageDelta:
			out = append(out, "usage")
		default:
			out = append(out, string(ev.Kind))
		}
	}
	return out
}

// pendingRealCollector is the customer/operator stream counter. It reports the
// exact released customer text it was asked to count, so a case can prove the
// published result participates in the customer projection.
type pendingRealCollector struct {
	mu        sync.Mutex
	calls     int
	lastText  string
	callCount accountingapp.CountResult
	outCount  accountingapp.CountResult
}

func (c *pendingRealCollector) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return c.callCount, nil
}

func (c *pendingRealCollector) CountOutput(_ context.Context, in accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.lastText = in.Text
	return c.outCount, nil
}

func (c *pendingRealCollector) outputText() (string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastText, c.calls
}

// pendingRealAuthority admits one reserved attempt so the real terminal owner
// runs its genuine attempt and request settlement.
func pendingRealAuthority() *recordingAuthorityService {
	return &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-pending-real",
			ReservedAmount: authorityInputAmount(11),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
}

// pendingRealEventStream replays a fixed canonical prefix through the real
// receive loop. It is a passive backend stream, not a publication helper.
type pendingRealEventStream struct {
	events []aipapi.Event
	fail   error
	idx    int
}

func (s *pendingRealEventStream) Recv(context.Context) (aipapi.Event, error) {
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

func (*pendingRealEventStream) Close() error { return nil }

func (*pendingRealEventStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// pendingRealStream is one assembled, non-thinker receive stream with real token
// accounting and a real private completion result on its attempt.
type pendingRealStream struct {
	executor  *Executor
	stream    *retryRecvStream
	pipe      *responsePipeline
	term      *turnTerminal
	attempt   *attemptSession
	auth      *recordingAuthorityService
	collector *pendingRealCollector
	observer  *pendingObserver
}

// pendingRealOptions configures one real-stream fixture.
type pendingRealOptions struct {
	// call supplies the actual decoded frontend invocation before receive ownership is bound.
	call      *aipapi.Call
	events    []aipapi.Event
	collector *pendingRealCollector
	// counter replaces the collector at the token-accounting seam, so a case can
	// install its own real counting barrier without duplicating the fixture.
	counter   accountingstream.Counter
	settleErr error
	// settleRequestErr fails the REQUEST-terminal settlement seam, which is the
	// effect that runs after a successful stage and before activation. The
	// attempt-level authority Settle is a different seam and cannot fail it.
	settleRequestErr error
	// settleRequestHook replaces that same REQUEST-terminal settlement seam with
	// a case-owned probe, so a case can prove whether a withdrawn publication still
	// settled customer authority. It is the effect that runs after staging and
	// before activation.
	settleRequestHook func(context.Context, []metering.Fact) error
	gates             []completion.Gate
	observer          bool
	usageObserver     usage.Observer
	trafficObserver   sdktraffic.Observer
	// decision replaces the terminal decision provider, so a case can drive the
	// accepted-terminal chokepoint to a specific decision kind.
	decision terminaldecision.Provider
	// recorder and mandatory install a real secure-session stream recorder, so a
	// case can hold or reject the publication preflight on the real terminal path.
	recorder  secureapp.GateRecording
	mandatory bool
	// recoveryPolicy installs the real stream-recovery policy the receive loop
	// observes client events through, so a case can read which phase the policy
	// believes the response is in through its own Decide* behavior.
	recoveryPolicy *streamrecovery.Policy
	// result overrides the bounded private completion result the fixture attempt
	// holds, so a case in another file can drive the real receive loop with its
	// own marker instead of duplicating the whole fixture.
	result string
}

func newPendingRealStream(t *testing.T, opts pendingRealOptions) *pendingRealStream {
	t.Helper()
	auth := pendingRealAuthority()
	if opts.settleErr != nil {
		auth.settleErr = opts.settleErr
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	baseline := aipapi.Call{ID: "request-pending-real", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming}, Messages: testMinimalUserMessages()}
	if opts.call != nil {
		baseline = *opts.call
		if baseline.Session.ALegID != "" {
			aLegID = baseline.Session.ALegID
		}
	}
	switch {
	case opts.counter != nil:
		ex.StreamUsage = accountingstream.New(opts.counter, accountingstream.Config{})
	case opts.collector != nil:
		ex.StreamUsage = accountingstream.New(opts.collector, accountingstream.Config{})
	}
	bus := hooks.New(hooks.Config{})
	if len(opts.gates) > 0 || opts.usageObserver != nil || opts.trafficObserver != nil {
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
			FeaturePlanes:   freezeBundle(testFeatureBundle{CompletionGates: opts.gates}),
			UsageObserver:   opts.usageObserver,
			TrafficObserver: opts.trafficObserver,
		})
	}
	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID:      "trace-pending-real",
			aLegID:       aLegID,
			baseline:     baseline,
			secureTurnOK: true,
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{ALegID: aLegID, BLegID: "bleg-pending-real", Seq: 1},
			authorityCandidate(),
			testAuthorityLifecycle(ex, attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(11),
				admissionResult: auth.admitResult,
			}, authorityCandidate()),
		),
		responsePipeline: newResponsePipeline(),
	}
	bindTestRuntimeOwners(rs, ex)
	if len(opts.gates) > 0 || opts.usageObserver != nil || opts.trafficObserver != nil {
		rs.responsePipeline.bus = bus
		rs.responsePipeline.runtimeSnapshot = ex.RuntimeSnapshot
	}
	if opts.settleRequestErr != nil {
		rs.terminal.settleRequestAuthority = func(context.Context, []metering.Fact) error {
			return opts.settleRequestErr
		}
	}
	if opts.settleRequestHook != nil {
		rs.terminal.settleRequestAuthority = opts.settleRequestHook
	}
	if opts.recoveryPolicy != nil {
		rs.recovery = &recoveryController{recoverPolicy: opts.recoveryPolicy}
	}
	if opts.decision != nil {
		rs.terminal.terminalDecisionProvider = opts.decision
		rs.terminal.terminalDecisionProviderID = opts.decision.ID()
		rs.terminal.terminalDecisionProviderHasID = true
	}
	if opts.recorder != nil {
		rs.responsePipeline.secureSessionRecorder = opts.recorder
		rs.responsePipeline.secureRecordingMandatory = opts.mandatory
	}
	attempt := rs.attempt.snapshot()
	attempt.controlCapture = newControlCallCapture(controlCaptureActivation(t))
	result := opts.result
	if result == "" {
		result = pendingRealResult
	}
	require.True(t, attempt.storeControlOutcome(controltool.Outcome{
		Kind: controltool.OutcomeComplete, ResultText: result, ReasonCode: "control_complete",
	}))
	fixture := &pendingRealStream{
		executor: ex, stream: rs, pipe: rs.responsePipeline, term: rs.terminal,
		attempt: attempt, auth: auth, collector: opts.collector,
	}
	if opts.observer {
		fixture.observer = pendingOpenObserver(t, attempt)
	}
	testStoreInner(rs, &pendingRealEventStream{events: opts.events, fail: nil})
	return fixture
}

func (f *pendingRealStream) drain(t *testing.T) ([]aipapi.Event, error) {
	t.Helper()
	var released []aipapi.Event
	var lastErr error
	for range 64 {
		ev, err := f.stream.Recv(context.Background())
		if err != nil {
			lastErr = err
			break
		}
		released = append(released, ev)
	}
	return released, lastErr
}

// TestPendingReal_recvPublishesResultThenCustomerUsageThenFinish is the core
// production-wiring regression for requirements 6.3, 6.7 and 11.3: the REAL
// receive loop publishes the accepted result, then the REFRESHED customer usage
// that the real AuthorityPrepare produced, then the accepted finish. Nothing here
// seeds preparation state or installs a reconstructor.
func TestPendingReal_recvPublishesResultThenCustomerUsageThenFinish(t *testing.T) {
	t.Parallel()

	collector := &pendingRealCollector{
		callCount: accountingapp.CountResult{InputTokens: 13, TotalTokens: 13},
		outCount:  accountingapp.CountResult{OutputTokens: 5, TotalTokens: 18},
	}
	fixture := newPendingRealStream(t, pendingRealOptions{
		observer:  true,
		collector: collector,
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventResponseFinished},
		},
	})

	released, err := fixture.drain(t)
	// A fully drained private publication ends the stream at EOF, which is normal
	// drain termination and not a failure: the accepted finish must be released
	// first, and only then may Recv report the end of the stream.
	require.GreaterOrEqual(t, pendingRealIndexOf(released, aipapi.EventResponseFinished), 0,
		"the accepted finish must be released before the stream ends; released=%v", pendingRealLabels(released))
	require.True(t, errors.Is(err, io.EOF),
		"the accepted response must end cleanly at EOF; released=%v err=%v", pendingRealLabels(released), err)
	require.NoError(t, aipapi.ValidateEventSequence(released),
		"the published batch must stay canonically legal; released=%v", pendingRealLabels(released))

	assert.Equal(t, 1, pendingRealCountText(released, pendingRealResult),
		"the accepted result must reach the client exactly once; released=%v", pendingRealLabels(released))

	textAt := pendingRealIndexOf(released, aipapi.EventTextDelta)
	usageAt := pendingRealIndexOf(released, aipapi.EventUsageDelta)
	finishAt := pendingRealIndexOf(released, aipapi.EventResponseFinished)
	require.GreaterOrEqual(t, textAt, 0, "the result must be released; released=%v", pendingRealLabels(released))
	require.GreaterOrEqual(t, finishAt, 0, "the finish must be released; released=%v", pendingRealLabels(released))
	require.GreaterOrEqual(t, usageAt, 0,
		"the real AuthorityPrepare produced usage, so the refreshed customer usage must be released; released=%v",
		pendingRealLabels(released))
	assert.Less(t, textAt, usageAt, "the result must precede the refreshed customer usage; released=%v", pendingRealLabels(released))
	assert.Less(t, usageAt, finishAt, "the refreshed customer usage must precede the finish; released=%v", pendingRealLabels(released))
	assert.Equal(t, 1, pendingRealCountKind(released, aipapi.EventResponseFinished),
		"the accepted finish must be released exactly once; released=%v", pendingRealLabels(released))

	usage := released[usageAt]
	assert.Equal(t, 5, usage.OutputTokens,
		"the released customer usage must be the reconstructed customer quantity; released=%v", pendingRealLabels(released))
	assert.Zero(t, usage.CostNanoUnits, "no customer money may be released; released=%v", pendingRealLabels(released))

	text, calls := collector.outputText()
	assert.Positive(t, calls, "the real customer reconstruction must run for this terminal")
	assert.Contains(t, text, pendingRealResult,
		"the customer projection must count the published result as ordinary assistant content")

	authority := fixture.pipe.lastAuthorityUsageSnapshot()
	require.NotEqual(t, aipapi.EventKind(""), authority.Kind, "the real operator authority usage must exist")
	assert.Equal(t, 5, authority.OutputTokens,
		"the operator authority usage is reconstructed once and is not re-derived by the publication")
	assert.Equal(t, 1, fixture.observer.countOf(aipapi.EventResponseFinished),
		"the final observer must see the finish exactly once; observed=%v", fixture.observer.observedKinds())
	assert.Equal(t, 1, fixture.observer.finishCount(),
		"the final observer must be finished exactly once")
}

// TestPendingReal_recorderFailureSurfacesThroughRealRecv pins requirement 5.6 /
// design Failure Behavior on the REAL terminal path: a mandatory secure-session
// recorder failure on the published batch must surface as a Recv error and must
// never deliver a client result.
func TestPendingReal_recorderFailureSurfacesThroughRealRecv(t *testing.T) {
	t.Parallel()

	for name, failOn := range map[string]aipapi.EventKind{
		"result":    aipapi.EventTextDelta,
		"finish":    aipapi.EventResponseFinished,
		"lifecycle": aipapi.EventResponseStarted,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newPendingRealStream(t, pendingRealOptions{
				events: []aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				},
			})
			fixture.pipe.secureSessionRecorder = &pendingFailingRecorder{failOn: failOn}
			fixture.pipe.secureRecordingMandatory = true

			released, err := fixture.drain(t)
			require.Error(t, err, "a mandatory recorder failure must surface through the real Recv; released=%v",
				pendingRealLabels(released))
			assert.Zero(t, pendingRealCountText(released, pendingRealResult),
				"a rejected batch must deliver no client result; released=%v", pendingRealLabels(released))
		})
	}
}

// TestPendingReal_settlementFailureLeavesNoQueuedResult pins the staging/activation
// split on the REAL terminal path: a request settlement failure after a successful
// stage must leave no queued client result for a later Recv.
func TestPendingReal_settlementFailureLeavesNoQueuedResult(t *testing.T) {
	t.Parallel()

	fixture := newPendingRealStream(t, pendingRealOptions{
		settleRequestErr: errors.New("pending real settle boom"),
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventResponseFinished},
		},
	})

	released, err := fixture.drain(t)
	require.Error(t, err, "a failed settlement must fail the terminal; released=%v", pendingRealLabels(released))
	assert.False(t, fixture.pipe.pendingPublicationActive(),
		"a failed settlement must never activate the private drain")
	_, queued := fixture.pipe.pendingPublicationHead()
	assert.False(t, queued, "no queued private result may survive a failed settlement")
	assert.Nil(t, fixture.pipe.pendingPreparedSnapshot(),
		"a failed settlement must not leave a retained candidate behind")
}

// TestPendingReal_closeBetweenQueuedDrainEventsDiscardsRemainder pins the physical
// drain fence on the REAL stream: once Close wins, the remaining private events
// can never be released by a later Recv.
func TestPendingReal_closeBetweenQueuedDrainEventsDiscardsRemainder(t *testing.T) {
	t.Parallel()

	fixture := newPendingRealStream(t, pendingRealOptions{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventResponseFinished},
		},
	})

	// First Recv activates and drains the leading private events.
	first, err := fixture.stream.Recv(context.Background())
	require.NoError(t, err, "the first private event must be released; err=%v", err)
	require.NotEqual(t, aipapi.EventKind(""), first.Kind)

	// Real Close wins the publication window.
	require.NoError(t, fixture.stream.Close())

	for range 16 {
		ev, rerr := fixture.stream.Recv(context.Background())
		if rerr != nil {
			break
		}
		assert.NotEqual(t, aipapi.EventTextDelta, ev.Kind,
			"a Close winner must never release queued private result text")
		if ev.Kind == aipapi.EventResponseFinished {
			break
		}
	}
	_, queued := fixture.pipe.pendingPublicationHead()
	assert.False(t, queued, "Close must discard the undelivered remainder")
}

// pendingRealGateFence is a real completion gate whose call count proves the real
// gated finish route evaluates the chain exactly once.
type pendingRealGateFence struct{ calls int }

func (g *pendingRealGateFence) ID() string                        { return "pending-real-gate" }
func (g *pendingRealGateFence) Order() int                        { return 0 }
func (g *pendingRealGateFence) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (g *pendingRealGateFence) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	g.calls++
	return completion.PassOriginalOutcome(), nil
}

// TestPendingReal_gatedFinishRoutePreservesRealFinishMetadata pins the gated
// finish route on the REAL stream: the accepted finish keeps its real metadata and
// is emitted exactly once, and the gate chain runs exactly once.
func TestPendingReal_gatedFinishRoutePreservesRealFinishMetadata(t *testing.T) {
	t.Parallel()

	gate := &pendingRealGateFence{}
	collector := &pendingRealCollector{
		callCount: accountingapp.CountResult{InputTokens: 4, TotalTokens: 4},
		outCount:  accountingapp.CountResult{OutputTokens: 6, TotalTokens: 10},
	}
	fixture := newPendingRealStream(t, pendingRealOptions{
		collector: collector,
		gates:     []completion.Gate{gate},
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
		},
	})

	released, err := fixture.drain(t)
	require.GreaterOrEqual(t, pendingRealIndexOf(released, aipapi.EventResponseFinished), 0,
		"the accepted finish must be released before the stream ends; released=%v", pendingRealLabels(released))
	require.True(t, errors.Is(err, io.EOF),
		"the gated accepted response must end cleanly at EOF; released=%v err=%v",
		pendingRealLabels(released), err)
	assert.Equal(t, 1, gate.calls, "the completion gate chain must run exactly once for the whole response")
	assert.Equal(t, 1, pendingRealCountText(released, pendingRealResult),
		"the gated route must publish the result exactly once; released=%v", pendingRealLabels(released))
	assert.Equal(t, 1, pendingRealCountKind(released, aipapi.EventResponseFinished),
		"the gated route must emit the finish exactly once; released=%v", pendingRealLabels(released))
	finishAt := pendingRealIndexOf(released, aipapi.EventResponseFinished)
	require.GreaterOrEqual(t, finishAt, 0, "released=%v", pendingRealLabels(released))
	assert.Equal(t, "stop", released[finishAt].FinishReason,
		"the accepted finish must keep its real metadata through the gated publication")
}

// TestPendingReal_observerFinishesOnceOnCompletedDrain pins requirement 6.7's
// observation ownership: the deferred final-stream observer is finished exactly
// once, successfully, at the actual finish delivery.
func TestPendingReal_observerFinishesOnceOnCompletedDrain(t *testing.T) {
	t.Parallel()

	collector := &pendingRealCollector{
		callCount: accountingapp.CountResult{InputTokens: 2, TotalTokens: 2},
		outCount:  accountingapp.CountResult{OutputTokens: 3, TotalTokens: 5},
	}
	fixture := newPendingRealStream(t, pendingRealOptions{
		observer:  true,
		collector: collector,
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventResponseFinished},
		},
	})
	require.NotNil(t, fixture.observer, "the fixture must carry a real observer")

	released, err := fixture.drain(t)
	require.GreaterOrEqual(t, pendingRealIndexOf(released, aipapi.EventResponseFinished), 0,
		"the accepted finish must be released before the stream ends; released=%v", pendingRealLabels(released))
	require.True(t, errors.Is(err, io.EOF),
		"a fully delivered publication ends the stream at EOF; released=%v err=%v",
		pendingRealLabels(released), err)
	assert.Equal(t, 1, fixture.observer.countOf(aipapi.EventTextDelta),
		"the observer must see the published result exactly once; observed=%v", fixture.observer.observedKinds())
	assert.Equal(t, 1, fixture.observer.finishCount(), "the observer must be finished exactly once")
	assert.Equal(t, response.OutcomeSuccessReleased, fixture.observer.lastOutcome(),
		"a fully delivered publication finishes the observer successfully")
}

// pendingFirstDecision captures the actual first provider input, before publication.
type pendingFirstDecision struct {
	calls int
	first terminaldecision.Input
}

func (d *pendingFirstDecision) ID() string { return "pending-first-decision" }
func (d *pendingFirstDecision) Decide(_ context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	d.calls++
	if d.calls == 1 {
		d.first = in
	}
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "pending_allow"}, nil
}

func TestPendingReal_explicitNameCannotBypassAmbiguousHistory(t *testing.T) {
	for _, kind := range []aipapi.EventKind{aipapi.EventItem, aipapi.EventToolCallFinished} {
		t.Run(string(kind), func(t *testing.T) {
			d := &pendingFirstDecision{}
			gate := &pendingTextOnlyGate{}
			conflict := aipapi.Event{Kind: kind, ToolCallID: "conflict", ToolName: "get_stock"}
			if kind == aipapi.EventItem {
				conflict.Item = &aipapi.Item{Kind: aipapi.ItemKindToolResult, Status: aipapi.ItemStatusCompleted, ToolResult: &aipapi.ToolResultItem{CallID: "conflict", Name: "get_stock"}}
			}
			f := newPendingRealStream(t, pendingRealOptions{decision: d, gates: []completion.Gate{gate}, events: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventToolCallStarted, ToolCallID: "safe", ToolName: "get_weather"},
				{Kind: aipapi.EventToolCallFinished, ToolCallID: "safe"},
				conflict,
				{Kind: aipapi.EventResponseFinished, FinishReason: "actual_conflict"},
			}})
			f.stream.facts.baseline.Items = []aipapi.Item{{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted, ToolCall: &aipapi.ToolCallItem{CallID: "conflict", Name: "get_weather"}}}
			released, err := f.drain(t)
			require.ErrorIs(t, err, io.EOF)
			assert.Zero(t, pendingRealCountText(released, pendingRealResult))
			require.Equal(t, 1, d.calls)
			assert.Equal(t, 1, gate.calls)
			assert.Contains(t, d.first.Evidence.Actions, terminaldecision.ActionFact{CallID: "safe", Kind: aipapi.ItemKindToolCall, Name: "get_weather", Status: aipapi.ItemStatusCompleted})
			for _, action := range d.first.Evidence.Actions {
				assert.NotEqual(t, "get_stock", action.Name)
			}
		})
	}
}

func TestPendingReal_liveOverflowRetainsOrdinaryBoundarySafety(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unfinished", true: "completed"}[completed], func(t *testing.T) {
			gate := &pendingTextOnlyGate{}
			d := &pendingFirstDecision{}
			events := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventReasoningDelta, Delta: "ordinary reasoning"}, {Kind: aipapi.EventToolCallStarted, ToolCallID: "live", ToolName: "get_weather"}}
			if completed {
				events = append(events, aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "live"})
			}
			events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "actual_live"})
			f := newPendingRealStream(t, pendingRealOptions{decision: d, gates: []completion.Gate{gate}, events: events})
			f.pipe.completionBufferLimits = completion.BufferLimits{MaxEvents: 2}
			released, err := f.drain(t)
			require.ErrorIs(t, err, io.EOF)
			assert.Zero(t, gate.calls)
			require.Equal(t, 1, d.calls)
			want := 0
			if completed {
				want = 1
			}
			assert.Equal(t, want, pendingRealCountText(released, pendingRealResult))
			assert.Equal(t, 1, pendingRealCountKind(released, aipapi.EventResponseFinished))
			assert.Equal(t, "actual_live", released[pendingRealIndexOf(released, aipapi.EventResponseFinished)].FinishReason)
			assert.Equal(t, events[:len(events)-1], released[:len(events)-1])
		})
	}
}

func TestPendingReal_historyCannotCompleteCurrentHeldAction(t *testing.T) {
	d := &pendingFirstDecision{}
	f := newPendingRealStream(t, pendingRealOptions{decision: d, gates: []completion.Gate{&pendingTextOnlyGate{}}, events: []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "reused", ToolName: "get_weather"},
		{Kind: aipapi.EventResponseFinished},
	}})
	f.stream.facts.baseline.Items = []aipapi.Item{{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted, ToolCall: &aipapi.ToolCallItem{CallID: "reused", Name: "get_weather"}}}
	released, err := f.drain(t)
	require.ErrorIs(t, err, io.EOF)
	assert.Zero(t, pendingRealCountText(released, pendingRealResult))
	require.Equal(t, 1, d.calls)
	assert.Contains(t, d.first.Evidence.Actions, terminaldecision.ActionFact{CallID: "reused", Kind: aipapi.ItemKindToolCall, Name: "get_weather", Status: aipapi.ItemStatusInProgress})
}

func TestPendingReal_mixedHeldActionsRemainIndividuallyRepresentable(t *testing.T) {
	d := &pendingFirstDecision{}
	gate := &pendingTextOnlyGate{}
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "done", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "done"},
		{Kind: aipapi.EventItem, Item: &aipapi.Item{Kind: aipapi.ItemKindToolResult, Status: aipapi.ItemStatusCompleted, ToolResult: &aipapi.ToolResultItem{CallID: "done"}}},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "current", ToolName: "get_stock"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "orphan"},
		{Kind: aipapi.EventResponseFinished, FinishReason: "actual_mixed"},
	}
	f := newPendingRealStream(t, pendingRealOptions{decision: d, gates: []completion.Gate{gate}, events: events})
	released, err := f.drain(t)
	require.ErrorIs(t, err, io.EOF)
	assert.Zero(t, pendingRealCountText(released, pendingRealResult))
	require.Equal(t, 1, d.calls)
	assert.Equal(t, 1, gate.calls)
	assert.Contains(t, d.first.Evidence.Actions, terminaldecision.ActionFact{CallID: "done", Kind: aipapi.ItemKindToolCall, Name: "get_weather", Status: aipapi.ItemStatusCompleted})
	assert.Contains(t, d.first.Evidence.Actions, terminaldecision.ActionFact{CallID: "done", Kind: aipapi.ItemKindToolResult, Name: "get_weather", Status: aipapi.ItemStatusCompleted})
	assert.Contains(t, d.first.Evidence.Actions, terminaldecision.ActionFact{CallID: "current", Kind: aipapi.ItemKindToolCall, Name: "get_stock", Status: aipapi.ItemStatusInProgress})
	assert.Equal(t, events, released)
}

func TestPendingReal_capacityPrioritizesCurrentUnsafeActions(t *testing.T) {
	d := &pendingFirstDecision{}
	events := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}}
	for i := range 10 {
		id := string(rune('a' + i))
		events = append(events, aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: "get_weather"}, aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: id})
	}
	events = append(events, aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "current", ToolName: "get_stock"}, aipapi.Event{Kind: aipapi.EventResponseFinished})
	f := newPendingRealStream(t, pendingRealOptions{decision: d, gates: []completion.Gate{&pendingTextOnlyGate{}}, events: events})
	f.stream.facts.baseline.Items = []aipapi.Item{{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted, ToolCall: &aipapi.ToolCallItem{CallID: "history", Name: "get_weather"}}}
	released, err := f.drain(t)
	require.ErrorIs(t, err, io.EOF)
	assert.Zero(t, pendingRealCountText(released, pendingRealResult))
	require.Equal(t, 1, d.calls)
	assert.Contains(t, d.first.Evidence.Actions, terminaldecision.ActionFact{CallID: "current", Kind: aipapi.ItemKindToolCall, Name: "get_stock", Status: aipapi.ItemStatusInProgress})
	for _, action := range d.first.Evidence.Actions {
		assert.NotEqual(t, "history", action.CallID)
	}
	assert.Equal(t, events, released)
}

func TestPendingReal_authoritativeGateTextWithoutFinishDrainsToRealEOF(t *testing.T) {
	replacement := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventTextDelta, Delta: "authoritative without finish"}}
	gate := &pendingDrainGate{out: completion.ReplaceOutcome(replacement)}
	f := newPendingRealStream(t, pendingRealOptions{gates: []completion.Gate{gate}, events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventResponseFinished, FinishReason: "removed_by_gate"}}})
	released, err := f.drain(t)
	require.ErrorIs(t, err, io.EOF)
	assert.Equal(t, 1, gate.calls)
	assert.Equal(t, replacement, released)
	for _, ev := range released {
		assert.NotEmpty(t, ev.Kind)
	}
}

func TestPendingReal_releasedNameConflictSuppressesHeldExplicitFinish(t *testing.T) {
	d := &pendingFirstDecision{}
	gate := &pendingTextOnlyGate{}
	f := newPendingRealStream(t, pendingRealOptions{decision: d, gates: []completion.Gate{gate}, events: []aipapi.Event{{Kind: aipapi.EventToolCallFinished, ToolCallID: "released", ToolName: "get_stock"}, {Kind: aipapi.EventResponseFinished, FinishReason: "actual_released_conflict"}}})
	f.pipe.rememberClientEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	f.pipe.rememberClientEvent(aipapi.Event{Kind: aipapi.EventMessageStarted})
	f.pipe.rememberClientEvent(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "released", ToolName: "get_weather"})
	released, err := f.drain(t)
	require.ErrorIs(t, err, io.EOF)
	assert.Zero(t, pendingRealCountText(released, pendingRealResult))
	require.Equal(t, 1, d.calls)
	assert.Equal(t, 1, gate.calls)
	for _, action := range d.first.Evidence.Actions {
		assert.NotEqual(t, "get_stock", action.Name)
	}
	require.Equal(t, 1, pendingRealCountKind(released, aipapi.EventResponseFinished))
	assert.Equal(t, "actual_released_conflict", released[pendingRealIndexOf(released, aipapi.EventResponseFinished)].FinishReason)
}

func TestPendingReal_completedHeldTruncationAloneSuppressesResult(t *testing.T) {
	d := &pendingFirstDecision{}
	gate := &pendingTextOnlyGate{}
	events := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}}
	for i := range terminaldecision.MaxEvidenceActions + 1 {
		id := string(rune('a' + i))
		events = append(events, aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: "get_weather"}, aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: id})
	}
	events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "actual_truncated"})
	f := newPendingRealStream(t, pendingRealOptions{decision: d, gates: []completion.Gate{gate}, events: events})
	released, err := f.drain(t)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 1, d.calls)
	assert.Equal(t, 1, gate.calls)
	assert.Zero(t, pendingRealCountText(released, pendingRealResult))
	assert.Equal(t, events, released)
	for _, action := range d.first.Evidence.Actions {
		assert.Equal(t, aipapi.ItemStatusCompleted, action.Status)
	}
}

func TestPendingReal_overflowHistoryCannotCompleteCurrentReleasedAction(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unfinished", true: "completed"}[completed], func(t *testing.T) {
			decision := &pendingFirstDecision{}
			gate := &pendingTextOnlyGate{}
			events := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventReasoningDelta, Delta: "ordinary reasoning"}, {Kind: aipapi.EventToolCallStarted, ToolCallID: "reused", ToolName: "get_weather"}}
			if completed {
				events = append(events, aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "reused"})
			}
			events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "actual_overflow_history"})
			fixture := newPendingRealStream(t, pendingRealOptions{decision: decision, gates: []completion.Gate{gate}, events: events})
			fixture.pipe.completionBufferLimits = completion.BufferLimits{MaxEvents: 2}
			fixture.stream.facts.baseline.Items = []aipapi.Item{{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted, ToolCall: &aipapi.ToolCallItem{CallID: "reused", Name: "get_weather"}}}
			released, err := fixture.drain(t)
			require.ErrorIs(t, err, io.EOF)
			require.Zero(t, gate.calls)
			require.Equal(t, 1, decision.calls)
			status := aipapi.ItemStatusInProgress
			count := 0
			if completed {
				status = aipapi.ItemStatusCompleted
				count = 1
			}
			assert.Equal(t, count, pendingRealCountText(released, pendingRealResult))
			assert.Equal(t, events[:len(events)-1], released[:len(events)-1])
			assert.Equal(t, 1, pendingRealCountKind(released, aipapi.EventResponseFinished))
			assert.Equal(t, "actual_overflow_history", released[pendingRealIndexOf(released, aipapi.EventResponseFinished)].FinishReason)
			assert.Contains(t, decision.first.Evidence.Actions, terminaldecision.ActionFact{CallID: "reused", Kind: aipapi.ItemKindToolCall, Name: "get_weather", Status: status})
		})
	}
}

func TestPendingReal_heldUnsafeIdentitiesSurviveReleasedCapacity(t *testing.T) {
	for _, reversed := range []bool{false, true} {
		t.Run(map[bool]string{false: "reused_first", true: "new_first"}[reversed], func(t *testing.T) {
			d := &pendingFirstDecision{}
			events := []aipapi.Event{
				{Kind: aipapi.EventToolCallStarted, ToolCallID: "a", ToolName: "get_weather"},
				{Kind: aipapi.EventToolCallStarted, ToolCallID: "current", ToolName: "get_stock"},
				{Kind: aipapi.EventResponseFinished, FinishReason: "actual_capacity"},
			}
			if reversed {
				events[0], events[1] = events[1], events[0]
			}
			gate := &pendingTextOnlyGate{}
			f := newPendingRealStream(t, pendingRealOptions{decision: d, gates: []completion.Gate{gate}, events: events})
			f.pipe.rememberClientEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
			f.pipe.rememberClientEvent(aipapi.Event{Kind: aipapi.EventMessageStarted})
			for i := range terminaldecision.MaxEvidenceActions {
				id := string(rune('a' + i))
				f.pipe.rememberClientEvent(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: "get_weather"})
				f.pipe.rememberClientEvent(aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: id})
			}
			released, err := f.drain(t)
			require.ErrorIs(t, err, io.EOF)
			require.Equal(t, 1, d.calls)
			assert.Equal(t, 1, gate.calls)
			assert.Equal(t, events, released)
			require.Zero(t, pendingRealCountText(released, pendingRealResult))
			assert.Contains(t, d.first.Evidence.Actions, terminaldecision.ActionFact{CallID: "a", Kind: aipapi.ItemKindToolCall, Name: "get_weather", Status: aipapi.ItemStatusInProgress})
			assert.Contains(t, d.first.Evidence.Actions, terminaldecision.ActionFact{CallID: "current", Kind: aipapi.ItemKindToolCall, Name: "get_stock", Status: aipapi.ItemStatusInProgress})
		})
	}
}

// pendingPreparedOnlyCounter succeeds for authority preparation, then fails the
// later private preview at CountCall, which produces no replacement event.
type pendingPreparedOnlyCounter struct{ calls int }

func (c *pendingPreparedOnlyCounter) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	c.calls++
	if c.calls > 1 {
		return accountingapp.CountResult{}, errors.New("private customer preview unavailable")
	}
	return accountingapp.CountResult{InputTokens: 7, TotalTokens: 7}, nil
}

func (*pendingPreparedOnlyCounter) CountOutput(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{OutputTokens: 3, TotalTokens: 3}, nil
}

func TestPendingReal_preparedCustomerUsageSurvivesUnavailablePreview(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(map[bool]string{false: "nil_callback", true: "real_callback"}[bound], func(t *testing.T) {
			counter := &pendingPreparedOnlyCounter{}
			recorder := &pendingFailingRecorder{}
			traffic := &pendingCustomerTraffic{}
			var settled []metering.Fact
			f := newPendingRealStream(t, pendingRealOptions{counter: counter, recorder: recorder, mandatory: true, trafficObserver: traffic, events: pendingRealFinishEvents(), settleRequestHook: func(_ context.Context, facts []metering.Fact) error { settled = append(settled, facts...); return nil }})
			meter := pendingRealInstallMetering(t, f)
			if bound {
				f.pipe.bindCustomerUsage(func(ctx context.Context, text string, events []aipapi.Event) aipapi.Event {
					return reconstructCustomerUsageForResponse(ctx, f.pipe.streamUsage, f.pipe.log, f.stream.facts, f.stream.attempt.snapshot(), text, events)
				})
			}
			released, err := f.drain(t)
			require.ErrorIs(t, err, io.EOF)
			require.Greater(t, counter.calls, 1, "real preparation must precede the failing private preview")
			assert.Equal(t, 1, pendingRealCountText(released, pendingRealResult))
			at := pendingRealIndexOf(released, aipapi.EventUsageDelta)
			require.GreaterOrEqual(t, at, 0, "genuine prepared customer usage must survive empty private reconstruction")
			assert.Equal(t, 7, released[at].InputTokens)
			assert.Equal(t, 3, released[at].OutputTokens)
			assert.Zero(t, released[at].CostNanoUnits)
			assert.Equal(t, aipapi.UsagePlaneClientVisible, released[at].Accounting.Plane)
			assert.Equal(t, 1, recorder.countOf(aipapi.EventUsageDelta))
			assert.Less(t, at, pendingRealIndexOf(released, aipapi.EventResponseFinished))
			ptcAt := pendingRealIndexOf(traffic.ptc, aipapi.EventUsageDelta)
			require.GreaterOrEqual(t, ptcAt, 0)
			assert.Equal(t, released[at], traffic.ptc[ptcAt])
			require.Len(t, settled, 1)
			assert.Equal(t, quantitiesFromUsageEvent(released[at]), settled[0].Quantities)
			require.Len(t, meter.Facts(), 1)
			assert.Equal(t, settled[0], meter.Facts()[0])
			for i, ev := range traffic.ptc {
				expected, err := json.Marshal(streamEventWire(ev))
				require.NoError(t, err)
				assert.JSONEq(t, string(expected), recorder.payload[i])
			}
		})
	}
}

func TestPendingReal_absentPreparationDoesNotInventCustomerUsage(t *testing.T) {
	f := newPendingRealStream(t, pendingRealOptions{events: pendingRealFinishEvents()})
	require.Nil(t, f.pipe.streamUsage, "without reconstruction AuthorityPrepare legitimately returns usageOK false")
	released, err := f.drain(t)
	require.ErrorIs(t, err, io.EOF)
	assert.Equal(t, 1, pendingRealCountText(released, pendingRealResult))
	assert.Zero(t, pendingRealCountKind(released, aipapi.EventUsageDelta))
}

// pendingContinueDecision drives the real chokepoint once, then accepts B2.
type pendingContinueDecision struct{ inputs []terminaldecision.Input }

func (*pendingContinueDecision) ID() string { return "pending-real-continue" }
func (d *pendingContinueDecision) Decide(_ context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	d.inputs = append(d.inputs, in)
	if len(d.inputs) == 1 {
		intent := continuationIntent()
		return terminaldecision.Decision{Kind: terminaldecision.DecisionContinue, ReasonCode: "continue", Continue: &intent}, nil
	}
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "b2_stop"}, nil
}

func TestPendingReal_actualContinuationWithdrawsB1Result(t *testing.T) {
	for _, ownResult := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing_signal", true: "distinct_signal"}[ownResult], func(t *testing.T) {
			term, stream, b1, _, _ := newContinuationRedHarness(t, nil)
			d := &pendingContinueDecision{}
			term.terminalDecisionProvider = d
			term.terminalDecisionProviderID = d.ID()
			term.terminalDecisionProviderHasID = true
			b1.controlTool = controlCaptureActivation(t)
			b1.controlCapture = newControlCallCapture(b1.controlTool)
			require.True(t, b1.storeControlOutcome(controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "private B1 result", ReasonCode: "control_complete"}))
			events := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventToolCallStarted, ToolCallID: "done", ToolName: "get_weather"}, {Kind: aipapi.EventToolCallFinished, ToolCallID: "done"}, {Kind: aipapi.EventResponseFinished}}
			b1.storeInner(&pendingRealEventStream{events: events})
			var b2 *attemptSession
			opens := 0
			stream.recovery.opener = func(_ context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
				opens++
				assert.Same(t, b1, stream.attempt.snapshot(), "B1 must remain current during B2 preparation")
				assert.Nil(t, stream.responsePipeline.pendingPreparedSnapshot(), "B1 candidate must be withdrawn before continuation transaction")
				assert.Zero(t, countToolEvents(req.pinnedFacts.baseline), "completed ordinary actions must not replay into B2")
				out := continuationOpenResult(t, b1)
				b2 = out.ready.session
				b2.controlTool = controlCaptureActivation(t)
				b2.controlCapture = newControlCallCapture(b2.controlTool)
				if ownResult {
					require.True(t, b2.storeControlOutcome(controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "distinct B2 result", ReasonCode: "control_complete"}))
				}
				b2.storeInner(&pendingRealEventStream{events: pendingRealFinishEvents()})
				return out, nil
			}
			fixture := &pendingRealStream{stream: stream, pipe: stream.responsePipeline, term: term, attempt: b1}
			released, err := fixture.drain(t)
			require.ErrorIs(t, err, io.EOF)
			require.Equal(t, 1, opens)
			require.Same(t, b2, stream.attempt.snapshot())
			require.Len(t, d.inputs, 2)
			assert.Equal(t, b1.bleg.BLegID, d.inputs[0].Request.BLegID)
			assert.Equal(t, b2.bleg.BLegID, d.inputs[1].Request.BLegID)
			assert.Equal(t, d.inputs[0].Request.RequestID, d.inputs[1].Request.RequestID)
			assert.Equal(t, d.inputs[0].Request.ALegID, d.inputs[1].Request.ALegID)
			assert.Equal(t, uint8(b1.bleg.Seq), d.inputs[0].Continuation.Attempt)
			assert.Equal(t, d.inputs[1].Evidence.Lineage.Attempt, d.inputs[1].Continuation.Attempt)
			assert.True(t, d.inputs[0].Evidence.ExplicitCompletion)
			assert.True(t, d.inputs[0].Evidence.ExplicitCompletionExpected)
			assert.Equal(t, ownResult, d.inputs[1].Evidence.ExplicitCompletion)
			assert.True(t, d.inputs[1].Evidence.ExplicitCompletionExpected)
			assert.Equal(t, continuationIntent().TrajectoryRef, d.inputs[1].Evidence.Lineage.TrajectoryRef)
			assert.Equal(t, continuationIntent().ControlRef, d.inputs[1].Evidence.Lineage.ProgressRef)
			assert.Greater(t, d.inputs[1].Evidence.Lineage.Attempt, d.inputs[0].Evidence.Lineage.Attempt)
			assert.Contains(t, d.inputs[0].Evidence.Actions, terminaldecision.ActionFact{CallID: "done", Name: "get_weather", Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted})
			assert.Contains(t, d.inputs[1].Evidence.Actions, terminaldecision.ActionFact{CallID: "done", Name: "get_weather", Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted})
			assert.NotContains(t, d.inputs[1].Evidence.CandidateText, "private B1 result")
			assert.NotContains(t, d.inputs[1].Evidence.RecentText, "private B1 result")
			assert.Zero(t, pendingRealCountText(released, "private B1 result"))
			want := 0
			if ownResult {
				want = 1
			}
			assert.Equal(t, want, pendingRealCountText(released, "distinct B2 result"))
			assert.Equal(t, 1, pendingRealCountKind(released, aipapi.EventToolCallStarted))
			assert.Equal(t, 1, pendingRealCountKind(released, aipapi.EventToolCallFinished))
			assert.Equal(t, 1, pendingRealCountKind(released, aipapi.EventResponseFinished))
			assert.Equal(t, 1, pendingRealCountKind(released, aipapi.EventResponseStarted))
			require.NoError(t, aipapi.ValidateEventSequence(released))
		})
	}
}

// pendingResultCounter derives customer counts from the observed canonical text.
// It deliberately reports zero output during B-leg authority preparation, before
// any private completion result has been released.
type pendingResultCounter struct{ texts []string }

func (*pendingResultCounter) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{InputTokens: 7, TotalTokens: 7}, nil
}

func (c *pendingResultCounter) CountOutput(_ context.Context, in accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	c.texts = append(c.texts, in.Text)
	return accountingapp.CountResult{OutputTokens: len(in.Text), TotalTokens: len(in.Text)}, nil
}

type pendingCustomerTraffic struct{ ptc, btp []aipapi.Event }

func (c *pendingCustomerTraffic) OnObservation(_ context.Context, in sdktraffic.Observation) error {
	var ev aipapi.Event
	if err := json.Unmarshal(in.Body, &ev); err != nil {
		return err
	}
	if in.Leg == sdktraffic.LegPTC {
		c.ptc = append(c.ptc, ev)
	}
	if in.Leg == sdktraffic.LegBTP {
		c.btp = append(c.btp, ev)
	}
	return nil
}

func TestPendingReal_customerQuantityMatchesRecvCollectRecordingPTCAndSettlement(t *testing.T) {
	for _, route := range []string{"ordinary", "gated", "recovery_drain", "gate_drain"} {
		for _, collect := range []bool{false, true} {
			name := route + map[bool]string{false: "/recv", true: "/collect"}[collect]
			t.Run(name, func(t *testing.T) {
				counter := &pendingResultCounter{}
				recorder := &pendingFailingRecorder{}
				traffic := &pendingCustomerTraffic{}
				var settled []metering.Fact
				opts := pendingRealOptions{counter: counter, recorder: recorder, mandatory: true, trafficObserver: traffic, events: pendingRealFinishEvents(), settleRequestHook: func(_ context.Context, facts []metering.Fact) error { settled = append(settled, facts...); return nil }}
				if route == "gated" {
					opts.gates = []completion.Gate{&pendingRealGateFence{}}
				}
				f := newPendingRealStream(t, opts)
				meter := pendingRealInstallMetering(t, f)

				billingCapture := pendingRealInstallBilling(t, f)
				if route == "recovery_drain" || route == "gate_drain" {
					finish := aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: route}
					testStoreInner(f.stream, &pendingRealEventStream{})
					if route == "recovery_drain" {
						f.pipe.appendRecoveryDrain(aipapi.Event{Kind: aipapi.EventResponseStarted}, aipapi.Event{Kind: aipapi.EventMessageStarted}, finish)
					} else {
						f.pipe.setGateDrain([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, finish})
					}
				}
				var wire []aipapi.Event
				if collect {
					out, err := aipapi.Collect(context.Background(), f.stream)
					require.NoError(t, err)
					assert.Equal(t, pendingRealResult, out.Text.String())
					assert.Equal(t, len(pendingRealResult), out.OutputTokens)
					assert.Equal(t, 7, out.InputTokens)
					assert.True(t, out.FinishReceived)
					_, err = f.stream.Recv(context.Background())
					require.ErrorIs(t, err, io.EOF)
				} else {
					released, err := f.drain(t)
					require.ErrorIs(t, err, io.EOF)
					wire = released
					require.NoError(t, aipapi.ValidateEventSequence(wire))
					assert.Equal(t, 1, pendingRealCountText(wire, pendingRealResult))
					at := pendingRealIndexOf(wire, aipapi.EventUsageDelta)
					require.GreaterOrEqual(t, at, 0)
					assert.Equal(t, len(pendingRealResult), wire[at].OutputTokens)
					assert.Less(t, pendingRealIndexOf(wire, aipapi.EventTextDelta), at)
					assert.Less(t, at, pendingRealIndexOf(wire, aipapi.EventResponseFinished))
				}
				require.Contains(t, counter.texts, pendingRealResult, "quantity must derive from the real candidate evidence")
				usageAt := pendingRealIndexOf(traffic.ptc, aipapi.EventUsageDelta)
				require.GreaterOrEqual(t, usageAt, 0)
				chosen := traffic.ptc[usageAt]
				assert.Equal(t, len(pendingRealResult), chosen.OutputTokens)
				assert.Equal(t, aipapi.UsagePlaneClientVisible, chosen.Accounting.Plane)
				require.Len(t, settled, 1)
				assert.Equal(t, quantitiesFromUsageEvent(chosen), settled[0].Quantities)
				assert.Nil(t, settled[0].Money)
				assert.Zero(t, f.pipe.lastAuthorityUsageSnapshot().OutputTokens, "operator reconstruction must retain pre-publication quantity")
				assert.Equal(t, 1, recorder.countOf(aipapi.EventUsageDelta))
				assert.Equal(t, 1, pendingRealCountKind(traffic.ptc, aipapi.EventUsageDelta))
				assert.Equal(t, 1, pendingRealCountText(traffic.ptc, pendingRealResult))
				assert.Zero(t, pendingRealCountText(traffic.btp, pendingRealResult), "synthetic customer result is never provider traffic")
				require.Len(t, recorder.payload, len(traffic.ptc))
				for i, ev := range traffic.ptc {
					expected, err := json.Marshal(streamEventWire(ev))
					require.NoError(t, err)
					assert.JSONEq(t, string(expected), recorder.payload[i], "recorder canonical projection must match physical PTC")
				}
				egress := meter.Facts()
				require.Len(t, egress, 1)
				assert.Equal(t, settled[0], egress[0], "request settlement must use the actual durable frontend egress fact")

				require.Len(t, billingCapture.completed(), 1, "actual finish must seal exactly one completed record")
			})
		}
	}
}

// pendingRealInstallMetering keeps the production frontend-egress conversion,
// durable append, and request settlement path intact. Only its recorder port is
// captured; no test-created egress fact substitutes for runtime evidence.
func pendingRealInstallMetering(t *testing.T, f *pendingRealStream) *recordingMeter {
	t.Helper()
	rec := &recordingMeter{}
	f.executor.MeteringRecorder = rec
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{Call: f.stream.facts.baseline, CheckpointID: "fe-pending-real", StreamID: "fe-pending-real-stream", Now: time.Unix(1, 0).UTC()})
	require.NoError(t, err)
	withTestRecvFacts(f.stream, func(facts recvTurnFacts) recvTurnFacts { facts.metering = holder; return facts })
	f.term.meteringRecorderPresent = true
	return rec
}

func TestPendingReal_providerQuantityRemainsSeparateFromResultCustomerQuantity(t *testing.T) {
	counter := &pendingResultCounter{}
	traffic := &pendingCustomerTraffic{}
	probe := &pendingLifetimeUsageProbe{}
	provider := aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 41, OutputTokens: 91, TotalTokens: 132, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative}}
	events := pendingRealFinishEvents()
	events = append(events[:2], provider, events[2])
	f := newPendingRealStream(t, pendingRealOptions{counter: counter, trafficObserver: traffic, usageObserver: probe, events: events})
	released, err := f.drain(t)
	require.ErrorIs(t, err, io.EOF)
	authority := f.pipe.lastAuthorityUsageSnapshot()
	assert.Equal(t, 41, authority.InputTokens)
	assert.Equal(t, 91, authority.OutputTokens)
	btpAt := pendingRealIndexOf(traffic.btp, aipapi.EventUsageDelta)
	require.GreaterOrEqual(t, btpAt, 0)
	assert.Equal(t, provider, traffic.btp[btpAt], "B-leg capture must preserve exact provider evidence")
	customerAt := -1
	for i, ev := range released {
		if ev.Kind == aipapi.EventUsageDelta && ev.Accounting.Plane == aipapi.UsagePlaneClientVisible {
			customerAt = i
		}
	}
	require.GreaterOrEqual(t, customerAt, 0)
	assert.Equal(t, len(pendingRealResult), released[customerAt].OutputTokens)
	assert.Equal(t, 7, released[customerAt].InputTokens)
	assert.Zero(t, released[customerAt].CostNanoUnits)
	assert.NotEqual(t, authority.OutputTokens, released[customerAt].OutputTokens)
	assert.Equal(t, 1, pendingRealCountText(released, pendingRealResult))
	assert.Equal(t, 1, pendingRealCountKind(released, aipapi.EventResponseFinished))
	var customer []usage.Event
	for _, ev := range probe.seen() {
		if ev.OutputTokens == len(pendingRealResult) {
			customer = append(customer, ev)
		}
	}
	require.Len(t, customer, 1)
	assert.Equal(t, f.attempt.bleg.BLegID, customer[0].BLegID)
	assert.Equal(t, int(f.attempt.bleg.Seq), customer[0].AttemptSeq)
}

// PendingResponsesConsumerFixtureForTest constructs the real pending Recv from
// the frontend-decoded call. This test-only export bridges the external consumer
// test package without introducing a production import cycle or SDK seam.
func PendingResponsesConsumerFixtureForTest(t *testing.T, call *aipapi.Call, suppress bool) (aipapi.EventStream, func()) {
	t.Helper()
	counter := &pendingResultCounter{}
	traffic := &pendingCustomerTraffic{}
	recorder := &pendingFailingRecorder{}
	var settled []metering.Fact
	events := pendingRealFinishEvents()
	if suppress {
		events = append(events[:2], aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "already answered"}, events[2])
	}
	f := newPendingRealStream(t, pendingRealOptions{call: call, counter: counter, trafficObserver: traffic, recorder: recorder, mandatory: true, events: events, settleRequestHook: func(_ context.Context, facts []metering.Fact) error { settled = append(settled, facts...); return nil }})
	meter := pendingRealInstallMetering(t, f)
	return f.stream, func() {
		assert.Equal(t, *call, f.stream.facts.baseline)
		want := pendingRealResult
		if suppress {
			want = "already answered"
		}
		at := pendingRealIndexOf(traffic.ptc, aipapi.EventUsageDelta)
		require.GreaterOrEqual(t, at, 0)
		assert.Less(t, at, pendingRealIndexOf(traffic.ptc, aipapi.EventResponseFinished))
		assert.Equal(t, 1, pendingRealCountText(traffic.ptc, want))
		assert.Equal(t, 1, recorder.countOf(aipapi.EventUsageDelta))
		require.Len(t, settled, 1)
		assert.Equal(t, quantitiesFromUsageEvent(traffic.ptc[at]), settled[0].Quantities)
		assert.Equal(t, settled, meter.Facts())
		assert.Contains(t, counter.texts, want)
	}
}
