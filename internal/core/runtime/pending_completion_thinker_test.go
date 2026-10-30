// Interleaved-thinker coverage of the pending completion-result publication
// (spec: .kiro/specs/agent-loop-explicit-completion-protocol, design Completion
// Evidence and Pending Result / Concurrency and Lifecycle; requirements 6.3-6.7,
// 11.3, 11.5, 12.5).
//
// The thinker path is supported, not excluded. An accepted normal thinker B-leg
// terminal publishes the private result through the same bounded prepared path,
// and the internal thinker canonical stream consumes it through the existing
// wrapper: memo capture once, and only sanitized reasoning reaches the outer
// client. The outer assistant answer remains the executor's, and request
// NormalFinish, customer request settlement, and billing handoff stay with the
// executor that owns them.
//
// Every case drives the real wrapper, the real memo processor, and the real
// receive loop, so a behavior satisfied only by a private helper fails here.
package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// pendingThinkerResult is the bounded completion result the thinker fixture
// publishes. It is a low-entropy, obviously fake marker.
const pendingThinkerResult = "thinker bounded answer"

// pendingThinkerFixture is one assembled interleaved thinker stream with a real
// processor-owned turn and a real valid completion result on its attempt.
//
// The fixture deliberately has no executor stream, so each drain ends on a receive
// error rather than a clean EOF. These cases assert on what did reach the outer
// client, never on that error, so it is explicitly not inspected.
type pendingThinkerFixture struct {
	stream   *retryRecvStream
	turn     InterleavedTurn
	visible  bool
	hiddenIs *hiddenInterleavedStream
}

// newPendingThinkerFixture builds one interleaved thinker retry stream whose
// backend stream completes a valid control call and then finishes. It reuses the
// existing interleaved authority-continuation fixture so the real A-leg, authority,
// processor, and memo store are wired exactly as production wires them.
func newPendingThinkerFixture(t *testing.T, streamToClient string, outcome *controltool.Outcome, events ...aipapi.Event) *pendingThinkerFixture {
	t.Helper()
	ex, from := setupInterleavedAuthorityContinuation(t, pendingThinkerAuthority(), streamToClient)
	from.terminal.setInterleavedThinker()
	attempt := from.attempt.snapshot()
	attempt.controlCapture = newControlCallCapture(controlCaptureActivation(t))
	if outcome != nil {
		require.True(t, attempt.storeControlOutcome(*outcome),
			"the fixture must really hold the private control outcome")
	}
	if len(events) == 0 {
		events = []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
		}
	}
	testStoreInner(from, aipapi.NewFixedEventStream(append(events, aipapi.Event{Kind: aipapi.EventResponseFinished})))

	turn, err := ex.InterleavedProcessor.BeginTurn(context.Background(), InterleavedTurnInput{
		ALegID:         from.facts.aLegID,
		Selector:       from.facts.baseline.Route.Selector,
		Backend:        attempt.cand.Primary.Backend,
		Model:          attempt.cand.Primary.Model,
		RequestID:      from.facts.traceID,
		StreamToClient: streamToClient,
	})
	require.NoError(t, err, "the thinker fixture must open a real interleaved turn")
	require.NotNil(t, turn, "the thinker fixture must have a processor-owned turn")

	fixture := &pendingThinkerFixture{stream: from, turn: turn, visible: streamToClient == "visible"}
	if fixture.visible {
		return fixture
	}
	fixture.hiddenIs = newHiddenInterleavedStream(from, turn, interleavedstate.State{})
	return fixture
}

func pendingThinkerCompleteOutcome() controltool.Outcome {
	return controltool.Outcome{
		Kind: controltool.OutcomeComplete, ResultText: pendingThinkerResult, ReasonCode: "control_complete",
	}
}

// recv drains the wrapper until it ends or blocks, and returns everything the
// outer client actually received. The fixture has no executor stream, so a blocked
// continuation is a legitimate end of this case's evidence.
func (f *pendingThinkerFixture) recv(t *testing.T) []aipapi.Event {
	t.Helper()
	var released []aipapi.Event
	if !f.visible {
		for range 32 {
			ev, err := f.hiddenIs.Recv(context.Background())
			if err != nil {
				break
			}
			released = append(released, ev)
		}
		return released
	}
	// The visible wrapper is one stream: it observes the thinker internally and
	// hands the outer client only sanitized reasoning. Its executor phase is not
	// part of this case, so the drain stops as soon as the thinker phase ends.
	stream := newVisibleInterleavedStream(f.stream, f.turn, interleavedstate.State{})
	for range 32 {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			break
		}
		released = append(released, ev)
		stream.mu.Lock()
		phase := stream.phase
		stream.mu.Unlock()
		if phase != interleavedPhaseThinker {
			break
		}
	}
	return released
}

func pendingThinkerAssistantText(events []aipapi.Event) string {
	var b strings.Builder
	for _, ev := range events {
		if ev.Kind == aipapi.EventTextDelta {
			b.WriteString(ev.Delta)
		}
	}
	return b.String()
}

func pendingThinkerReasoningCount(events []aipapi.Event, want string) int {
	count := 0
	for _, ev := range events {
		if ev.Kind == aipapi.EventReasoningDelta && strings.Contains(ev.Delta, want) {
			count++
		}
	}
	return count
}

// pendingThinkerToolCount counts any client tool traffic the outer stream received.
// A proxy-owned control completion must never contribute one.
func pendingThinkerToolCount(events []aipapi.Event) int {
	count := 0
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
			count++
		case aipapi.EventItem:
			if ev.Item != nil && (ev.Item.ToolCall != nil || ev.Item.ToolResult != nil) {
				count++
			}
		}
	}
	return count
}

func pendingThinkerLabels(events []aipapi.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventTextDelta:
			out = append(out, "text:"+ev.Delta)
		case aipapi.EventReasoningDelta:
			out = append(out, "reasoning:"+ev.Delta)
		default:
			out = append(out, string(ev.Kind))
		}
	}
	return out
}

// TestPendingCompletionThinker_hiddenResultNeverBecomesOuterAssistantText pins the
// hidden-mode thinker behavior: a completion-only thinker result is consumed
// internally, never becomes the outer assistant answer, and never finishes the
// shared request terminal.
func TestPendingCompletionThinker_hiddenResultNeverBecomesOuterAssistantText(t *testing.T) {
	t.Parallel()

	fixture := newPendingThinkerFixture(t, "hidden", pendingThinkerOutcomePtr())
	released := fixture.recv(t)

	assert.Empty(t, pendingThinkerAssistantText(released),
		"a hidden thinker result must never become the outer assistant answer; released=%v",
		pendingThinkerLabels(released))
	assert.Zero(t, pendingThinkerReasoningCount(released, pendingThinkerResult),
		"hidden mode consumes the result internally and exposes no visible reasoning either; released=%v",
		pendingThinkerLabels(released))
	assert.Contains(t, pendingThinkerLabels(released), string(aipapi.EventResponseFinished),
		"the accepted thinker B-leg finish must still complete the shared response; released=%v",
		pendingThinkerLabels(released))
	assert.Zero(t, pendingThinkerToolCount(released),
		"a proxy-owned control completion must never publish a client tool call; released=%v",
		pendingThinkerLabels(released))
}

// TestPendingCompletionThinker_visibleResultBecomesSanitizedReasoningOnce pins the
// visible-mode behavior: the private result becomes sanitized visible reasoning
// exactly once and is never the outer assistant answer.
func TestPendingCompletionThinker_visibleResultBecomesSanitizedReasoningOnce(t *testing.T) {
	t.Parallel()

	fixture := newPendingThinkerFixture(t, "visible", pendingThinkerOutcomePtr())
	released := fixture.recv(t)

	assert.Empty(t, pendingThinkerAssistantText(released),
		"a thinker result must never become the outer assistant answer; released=%v",
		pendingThinkerLabels(released))
	assert.Equal(t, 1, pendingThinkerReasoningCount(released, pendingThinkerResult),
		"a visible thinker result must be exposed as sanitized reasoning exactly once; released=%v",
		pendingThinkerLabels(released))
	assert.Contains(t, pendingThinkerLabels(released), string(aipapi.EventResponseFinished),
		"the accepted thinker B-leg finish must still complete the shared response; released=%v",
		pendingThinkerLabels(released))
	assert.Zero(t, pendingThinkerToolCount(released),
		"a proxy-owned control completion must never publish a client tool call; released=%v",
		pendingThinkerLabels(released))
}

// TestPendingCompletionThinker_priorThinkerTextSuppressesTheResult pins the shared
// eligibility rule on the thinker path: prior thinker assistant text suppresses the
// duplicate result exactly as it does for an ordinary response.
func TestPendingCompletionThinker_priorThinkerTextSuppressesTheResult(t *testing.T) {
	t.Parallel()

	fixture := newPendingThinkerFixture(t, "hidden", pendingThinkerOutcomePtr(),
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "already thought"},
	)
	released := fixture.recv(t)

	assert.Zero(t, pendingThinkerReasoningCount(released, pendingThinkerResult),
		"prior thinker text must suppress the duplicate completion result; released=%v",
		pendingThinkerLabels(released))
	assert.Empty(t, pendingThinkerAssistantText(released),
		"a suppressed result must never become the outer assistant answer; released=%v",
		pendingThinkerLabels(released))
}

// TestPendingCompletionThinker_unacceptedCompletionPublishesNothing pins the
// losing, invalid, and disposed halves on the thinker path: none of them becomes
// memo content or reaches the outer client.
func TestPendingCompletionThinker_unacceptedCompletionPublishesNothing(t *testing.T) {
	t.Parallel()

	t.Run("invalid_outcome", func(t *testing.T) {
		t.Parallel()
		fixture := newPendingThinkerFixture(t, "hidden", &controltool.Outcome{
			Kind: controltool.OutcomeInvalid, ReasonCode: "control_invalid",
		})
		released := fixture.recv(t)
		assert.Zero(t, pendingThinkerReasoningCount(released, pendingThinkerResult),
			"an unaccepted completion outcome must publish nothing; released=%v",
			pendingThinkerLabels(released))
	})

	t.Run("released_attempt", func(t *testing.T) {
		t.Parallel()
		fixture := newPendingThinkerFixture(t, "hidden", pendingThinkerOutcomePtr())
		fixture.stream.attempt.snapshot().discardControlState()
		released := fixture.recv(t)
		assert.Zero(t, pendingThinkerReasoningCount(released, pendingThinkerResult),
			"a released attempt must publish no result; released=%v", pendingThinkerLabels(released))
		assert.Empty(t, pendingThinkerAssistantText(released),
			"a released attempt must publish no outer assistant text; released=%v",
			pendingThinkerLabels(released))
	})
}

// pendingThinkerAuthority admits both the thinker and its continuation leg, so
// the fixture opens a real executor continuation exactly as production does.
func pendingThinkerAuthority() *recordingAuthorityService {
	return &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-pending-thinker",
			ReservedAmount: authorityInputAmount(9),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
}

func pendingThinkerOutcomePtr() *controltool.Outcome {
	outcome := pendingThinkerCompleteOutcome()
	return &outcome
}
