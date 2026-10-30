package runtime

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Claim table:
// | TestEmptyVisibleTurnDetection | clean reason + zero produced visible output only; reasoning-only counts, text/tool/media/truncation/refusal never | req: clean terminal, empty or clean reason, zero text/tool/media, reasoning-only empty | accept truncated/refusal or ignore produced facts -> FAIL |
// | TestFinalizeResponseFinishedAuthorityRecordsEmptyVisibleTurn | the surviving attempt's clean terminal increments the optional sink once and logs one bounded decision; produced text blocks the count | req: counter increments at the terminal chokepoint | drop the observation call / count a produced turn -> FAIL |
// | TestFinalizeResponseFinishedAuthority_InterleavedThinkerNeverCountsEmptyVisibleTurn | a thinker's clean terminal never enters the client-visible empty-turn signal | req: hidden B-leg completion is not a client turn | drop the thinker guard -> FAIL |

// emptyTurnRecordingSink is a MetricsSink fake that also observes the optional
// empty-visible-turn extension.
type emptyTurnRecordingSink struct {
	turns   atomic.Int64
	backend atomic.Value
}

func (s *emptyTurnRecordingSink) OnAttemptRecorded(aipapi.AttemptOutcome, string) {}
func (s *emptyTurnRecordingSink) OnBackendOpenDuration(string, float64)           {}
func (s *emptyTurnRecordingSink) OnTransportNegotiation(aipapi.Operation, aipapi.TransportMode, string) {
}
func (s *emptyTurnRecordingSink) OnCancellation(CancellationObservation) {}
func (s *emptyTurnRecordingSink) OnEmptyVisibleTurn(backend string) {
	s.turns.Add(1)
	s.backend.Store(backend)
}

func producedAttempt(events ...aipapi.Event) *attemptSession {
	attempt := newAttemptSession(attemptSessionInput{})
	for _, ev := range events {
		attempt.observeProducedOutput(ev)
	}
	return attempt
}

func TestEmptyVisibleTurnDetection(t *testing.T) {
	t.Parallel()
	clean := aipapi.Event{Kind: aipapi.EventResponseFinished}
	cases := []struct {
		name    string
		attempt *attemptSession
		finish  aipapi.Event
		want    bool
	}{
		{name: "empty clean finish", attempt: newAttemptSession(attemptSessionInput{}), finish: clean, want: true},
		{name: "reasoning only counts as empty", attempt: producedAttempt(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "thinking"}), finish: clean, want: true},
		{name: "text delta is visible", attempt: producedAttempt(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"}), finish: clean, want: false},
		{name: "tool call is visible", attempt: producedAttempt(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "call-1"}), finish: clean, want: false},
		{name: "assistant media is visible", attempt: producedAttempt(aipapi.Event{Kind: aipapi.EventAssistantImageRef, AssistantRef: "img-1"}), finish: clean, want: false},
		{name: "output-limit truncation is not clean", attempt: newAttemptSession(attemptSessionInput{}), finish: aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: "incomplete", FinishReason: "max_tokens"}, want: false},
		{name: "refusal is not clean", attempt: newAttemptSession(attemptSessionInput{}), finish: aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "content_filter"}, want: false},
		{name: "empty reason is clean", attempt: newAttemptSession(attemptSessionInput{}), finish: aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: ""}, want: true},
		{name: "canonical clean reason is clean", attempt: newAttemptSession(attemptSessionInput{}), finish: aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "stop"}, want: true},
		{name: "completed status is authoritative", attempt: newAttemptSession(attemptSessionInput{}), finish: aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: "completed", FinishReason: "content_filter"}, want: true},
		{name: "nil attempt never counts", attempt: nil, finish: clean, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := emptyVisibleTurn(tc.attempt, tc.finish); got != tc.want {
				t.Fatalf("emptyVisibleTurn() = %t, want %t", got, tc.want)
			}
		})
	}
}

// emptyTurnWiringStream builds one directly assembled recv stream fixture whose
// terminal and pipeline dependencies are bound exactly as production binds
// them, with an unbound authority lifecycle (nothing to reserve or settle).
func emptyTurnWiringStream(t *testing.T, executor *Executor, aLegID string) *retryRecvStream {
	t.Helper()
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "request-empty-turn", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			aLegID:   aLegID,
			traceID:  "trace-empty-turn",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, authorityCandidate(), authorityLifecycle{}, newAttemptAccountingTracker(time.Unix(1, 0))),
	}
	installTestTurnTerminal(stream)
	bindTestRuntimeOwners(stream, executor)
	return stream
}

func TestFinalizeResponseFinishedAuthorityRecordsEmptyVisibleTurn(t *testing.T) {
	t.Parallel()

	executor, _, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	var logs bytes.Buffer
	executor.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	sink := &emptyTurnRecordingSink{}
	executor.Metrics = sink

	stream := emptyTurnWiringStream(t, executor, aLegID)
	if _, _, err := stream.terminal.finalizeResponseFinishedAuthority(context.Background(),
		aipapi.Event{Kind: aipapi.EventResponseFinished}, stream.facts.terminalFacts(), stream.attempt.snapshot(), stream.responsePipeline); err != nil {
		t.Fatalf("finalizeResponseFinishedAuthority: %v", err)
	}
	if got := sink.turns.Load(); got != 1 {
		t.Fatalf("empty visible turn counter = %d, want 1", got)
	}
	if got, _ := sink.backend.Load().(string); got != "backend-1" {
		t.Fatalf("counter backend label = %q, want backend-1", got)
	}
	if got := strings.Count(logs.String(), `"msg":"empty_visible_turn"`); got != 1 {
		t.Fatalf("empty_visible_turn decision log lines = %d, want 1; logs=%s", got, logs.String())
	}

	// A produced visible delta means the turn is not empty, so the same clean
	// terminal must not increment the counter again.
	stream = emptyTurnWiringStream(t, executor, aLegID)
	prepared := stream.responsePipeline.prepareRecvEvent(context.Background(), stream.facts, stream.attempt.snapshot(),
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"}, nil)
	if prepared.err != nil {
		t.Fatalf("prepareRecvEvent: %v", prepared.err)
	}
	if _, _, err := stream.terminal.finalizeResponseFinishedAuthority(context.Background(),
		aipapi.Event{Kind: aipapi.EventResponseFinished}, stream.facts.terminalFacts(), stream.attempt.snapshot(), stream.responsePipeline); err != nil {
		t.Fatalf("finalizeResponseFinishedAuthority after text: %v", err)
	}
	if got := sink.turns.Load(); got != 1 {
		t.Fatalf("counter incremented for a turn with produced text: %d", got)
	}
}

// TestFinalizeResponseFinishedAuthority_InterleavedThinkerNeverCountsEmptyVisibleTurn
// pins the thinker exclusion on the same terminal chokepoint: a thinker's clean
// terminal completes only its hidden B-leg, so it must never enter the
// client-visible empty-turn signal even though it produced no visible output.
func TestFinalizeResponseFinishedAuthority_InterleavedThinkerNeverCountsEmptyVisibleTurn(t *testing.T) {
	t.Parallel()

	executor, _, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	sink := &emptyTurnRecordingSink{}
	executor.Metrics = sink

	stream := emptyTurnWiringStream(t, executor, aLegID)
	stream.terminal.setInterleavedThinker()
	if _, _, err := stream.terminal.finalizeResponseFinishedAuthority(context.Background(),
		aipapi.Event{Kind: aipapi.EventResponseFinished}, stream.facts.terminalFacts(), stream.attempt.snapshot(), stream.responsePipeline); err != nil {
		t.Fatalf("finalizeResponseFinishedAuthority: %v", err)
	}
	if got := sink.turns.Load(); got != 0 {
		t.Fatalf("an interleaved thinker terminal counted an empty visible turn, counter=%d", got)
	}
}
