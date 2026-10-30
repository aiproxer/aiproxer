package runtime

// Gate-off characterization for the empty-visible-turn corrective retry
// (spec: empty-visible-turn-retry, task 1; requirements 1.1-1.2; design
// Testing Strategy "Gate-off characterization first").
//
// This file pins TODAY'S behavior of the unchanged receive loop for the three
// finish shapes the corrective retry will later classify: a clean empty finish,
// a contentful finish, and a reasoning-only finish (plus the degenerate shape
// carrying usage evidence). Tasks 2-9 change the loop behind a default-off
// gate; this test must stay green and byte-identical through all of them. It
// therefore exercises the baseline (zero-value) executor configuration and
// names no gate field: the fixture seam accepts any executor, so a later task
// can run the same scripts with the gate explicitly disabled without editing
// this file.
//
// Claim table:
// | TestEmptyVisibleTurnRetryGateOffCharacterization | with the gate at its default, the client observes exactly the backend's canonical frames in order (no drop, no duplicate, no reorder; the gate-off path may hold frames internally as long as it releases them unchanged), usage is forwarded once and observed once, and the existing signals count only the accepted winner: `aip_empty_visible_turn_total` fires once for a clean empty or reasoning-only turn, never for a contentful one, with one `AttemptSuccess` attempt metric and one accepted-success attempt record | spec empty-visible-turn-retry req 1.1-1.2, design Testing Strategy gate-off characterization; existing `empty_visible_turn` signal | swallowing/duplicating/reordering a released frame, double-observing usage, counting an empty turn for contentful output, or a second attempt record -> FAIL |

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// emptyVisibleTurnRetryGateOffSink records the two existing executor signals
// this characterization pins: the optional empty-visible-turn extension and the
// attempt-outcome metric. It is deliberately not the future retry observer.
type emptyVisibleTurnRetryGateOffSink struct {
	mu         sync.Mutex
	emptyTurns []string
	attempts   []emptyVisibleTurnRetryGateOffAttemptMetric
}

type emptyVisibleTurnRetryGateOffAttemptMetric struct {
	outcome aipapi.AttemptOutcome
	backend string
}

func (s *emptyVisibleTurnRetryGateOffSink) OnAttemptRecorded(outcome aipapi.AttemptOutcome, backend string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, emptyVisibleTurnRetryGateOffAttemptMetric{outcome: outcome, backend: backend})
}

func (s *emptyVisibleTurnRetryGateOffSink) OnBackendOpenDuration(string, float64) {}

func (s *emptyVisibleTurnRetryGateOffSink) OnTransportNegotiation(aipapi.Operation, aipapi.TransportMode, string) {
}

func (s *emptyVisibleTurnRetryGateOffSink) OnCancellation(CancellationObservation) {}

func (s *emptyVisibleTurnRetryGateOffSink) OnEmptyVisibleTurn(backend string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emptyTurns = append(s.emptyTurns, backend)
}

func (s *emptyVisibleTurnRetryGateOffSink) emptyTurnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.emptyTurns)
}

func (s *emptyVisibleTurnRetryGateOffSink) emptyTurnBackend() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.emptyTurns) == 0 {
		return ""
	}
	return s.emptyTurns[0]
}

func (s *emptyVisibleTurnRetryGateOffSink) attemptMetrics() []emptyVisibleTurnRetryGateOffAttemptMetric {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]emptyVisibleTurnRetryGateOffAttemptMetric(nil), s.attempts...)
}

// emptyVisibleTurnRetryGateOffCase is one scripted backend stream plus the
// gate-off behavior it must keep producing.
type emptyVisibleTurnRetryGateOffCase struct {
	name string
	// script is the backend's canonical stream, in order.
	script []aipapi.Event
	// wantEmptyVisibleTurns counts the existing aip_empty_visible_turn_total
	// increments: one for an accepted clean empty or reasoning-only winner,
	// zero for a contentful one.
	wantEmptyVisibleTurns int
	// wantUsageOutputTokens is the attempt-local usage accounting total.
	wantUsageOutputTokens int64
}

func emptyVisibleTurnRetryGateOffUsage(input, output, total int) aipapi.Event {
	return aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   input,
		OutputTokens:  output,
		TotalTokens:   total,
		UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
	}
}

func emptyVisibleTurnRetryGateOffCases() []emptyVisibleTurnRetryGateOffCase {
	return []emptyVisibleTurnRetryGateOffCase{
		{
			name: "clean empty finish",
			script: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			},
			wantEmptyVisibleTurns: 1,
		},
		{
			name: "empty finish with usage evidence",
			script: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				emptyVisibleTurnRetryGateOffUsage(3, 5, 8),
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			},
			wantEmptyVisibleTurns: 1,
			wantUsageOutputTokens: 5,
		},
		{
			name: "contentful finish with usage evidence",
			script: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "hello"},
				emptyVisibleTurnRetryGateOffUsage(3, 5, 8),
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			},
			wantEmptyVisibleTurns: 0,
			wantUsageOutputTokens: 5,
		},
		{
			name: "reasoning-only finish with usage evidence",
			script: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventReasoningDelta, Delta: "thinking"},
				emptyVisibleTurnRetryGateOffUsage(4, 6, 10),
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			},
			wantEmptyVisibleTurns: 1,
			wantUsageOutputTokens: 6,
		},
	}
}

// emptyVisibleTurnRetryGateOffFixture is one scripted receive stream wired to
// the real terminal, pipeline, accounting, and metrics seams, with nothing held
// and no gate decision made.
type emptyVisibleTurnRetryGateOffFixture struct {
	executor *Executor
	aLegID   string
	stream   *retryRecvStream
	attempt  *attemptSession
}

// newEmptyVisibleTurnRetryGateOffFixture drives the supplied script through the
// real receive loop over the supplied executor. The executor is a construction
// parameter on purpose: later tasks can run the identical scripts under an
// explicitly disabled gate without touching this file, and the baseline caller
// keeps the zero-value (disabled by default) configuration.
func newEmptyVisibleTurnRetryGateOffFixture(t *testing.T, ex *Executor, aLegID string, script []aipapi.Event) *emptyVisibleTurnRetryGateOffFixture {
	t.Helper()
	if ex == nil {
		t.Fatal("gate-off fixture requires an executor")
	}
	bleg, err := ex.Store.NextBLeg(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("allocate b-leg: %v", err)
	}
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{
				ID:         "request-empty-visible-turn-retry-gate-off",
				Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions},
				Messages:   testMinimalUserMessages(),
			},
			aLegID:  aLegID,
			traceID: "trace-empty-visible-turn-retry-gate-off",
		}),
		attempt: testAttemptSlot(
			bleg,
			authorityCandidate(),
			authorityLifecycle{},
			newAttemptAccountingTracker(time.Unix(1, 0)),
		),
	}
	installTestTurnTerminal(stream)
	bindTestRuntimeOwners(stream, ex)
	testStoreInner(stream, aipapi.NewFixedEventStream(script))
	return &emptyVisibleTurnRetryGateOffFixture{
		executor: ex,
		aLegID:   aLegID,
		stream:   stream,
		attempt:  stream.attempt.snapshot(),
	}
}

// drain reads the stream to its end and returns the client-visible frames in
// the order the client observed them.
func (f *emptyVisibleTurnRetryGateOffFixture) drain(t *testing.T) []aipapi.Event {
	t.Helper()
	var released []aipapi.Event
	for range 32 {
		ev, err := f.stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return released
		}
		if err != nil {
			t.Fatalf("Recv after %d released event(s): %v", len(released), err)
		}
		released = append(released, ev)
	}
	t.Fatalf("receive loop did not end after 32 events")
	return nil
}

func TestEmptyVisibleTurnRetryGateOffCharacterization(t *testing.T) {
	t.Parallel()
	for _, tc := range emptyVisibleTurnRetryGateOffCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &emptyVisibleTurnRetryGateOffSink{}
			ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
			ex.Metrics = sink
			runEmptyVisibleTurnRetryGateOffCase(t, ex, aLegID, sink, tc)
		})
	}
}

// runEmptyVisibleTurnRetryGateOffCase drives one case over the supplied
// executor and asserts the full gate-off contract. It is the reusable seam for
// a later task that runs the identical scripts under an explicitly disabled
// gate: build the executor, install the sink, and call this function.
func runEmptyVisibleTurnRetryGateOffCase(t *testing.T, ex *Executor, aLegID string, sink *emptyVisibleTurnRetryGateOffSink, tc emptyVisibleTurnRetryGateOffCase) {
	t.Helper()
	fixture := newEmptyVisibleTurnRetryGateOffFixture(t, ex, aLegID, tc.script)
	if fixture.stream.terminal.emptyVisibleTurnRetryEnabled() {
		t.Fatal("gate-off characterization requires the corrective retry gate to be off")
	}

	released := fixture.drain(t)

	// The client observes exactly the backend's canonical frames, in
	// order: nothing held, dropped, duplicated, or reordered.
	if !reflect.DeepEqual(released, tc.script) {
		t.Fatalf("client-visible sequence = %#v, want %#v", released, tc.script)
	}

	// Existing empty-visible-turn signal: exactly the accepted winner,
	// labelled with the candidate backend.
	if got := sink.emptyTurnCount(); got != tc.wantEmptyVisibleTurns {
		t.Fatalf("empty-visible-turn count = %d, want %d", got, tc.wantEmptyVisibleTurns)
	}
	if tc.wantEmptyVisibleTurns > 0 {
		if got := sink.emptyTurnBackend(); got != "backend-1" {
			t.Fatalf("empty-visible-turn backend = %q, want backend-1", got)
		}
	}

	// Existing attempt metric: one accepted winner, no extra attempt.
	attemptMetrics := sink.attemptMetrics()
	if len(attemptMetrics) != 1 || attemptMetrics[0].outcome != aipapi.AttemptSuccess || attemptMetrics[0].backend != "backend-1" {
		t.Fatalf("attempt metrics = %#v, want exactly one AttemptSuccess for backend-1", attemptMetrics)
	}

	// Usage handling: each usage frame is forwarded once (asserted above)
	// and observed exactly once by attempt-local accounting.
	if got := fixture.attempt.accountingSnapshot().OutputTokens; got != tc.wantUsageOutputTokens {
		t.Fatalf("accounted output tokens = %d, want %d", got, tc.wantUsageOutputTokens)
	}

	// Terminal vocabulary: an accepted clean finish for request and attempt.
	request, attempt := testTerminalOwners(fixture.stream)
	for name, term := range map[string]*streamTerminal{"request": request, "attempt": attempt} {
		if term == nil {
			t.Fatalf("%s terminal is nil", name)
		}
		outcome, ok := term.Owner().Outcome()
		if !ok || outcome.Command != sdkterminal.CommandNormalFinish {
			t.Fatalf("%s terminal outcome = %+v, ok=%v, want %s", name, outcome, ok, sdkterminal.CommandNormalFinish)
		}
	}

	// Existing accounting: one persisted accepted-success attempt.
	records, err := fixture.executor.Store.LoadAttempts(context.Background(), fixture.aLegID)
	if err != nil {
		t.Fatalf("LoadAttempts: %v", err)
	}
	if len(records) != 1 || records[0].Outcome != aipapi.AttemptSuccess || records[0].BackendID != "backend-1" {
		t.Fatalf("attempt records = %#v, want exactly one AttemptSuccess for backend-1", records)
	}
}
