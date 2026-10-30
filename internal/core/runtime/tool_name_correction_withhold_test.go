package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// Spec: tool-name-correction Task 2.3 (requirements 2.1, 2.4, 3.7): a withholding
// attempt's would-be client output is held in its bounded ordered queue and reaches
// the client only by release - at the finish intercept, on overflow, at an end of
// stream, or before a later finalizer error. Cancellation discards it.

// twFloodEvents is comfortably past the withhold queue's event bound.
const twFloodEvents = 300

func twCallEvents(id, name, delta string, idx int) []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: name, MessageIndex: idx},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, ToolName: name, MessageIndex: idx, Delta: delta},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: id, ToolName: name, MessageIndex: idx},
	}
}

// twSteeredHeld is the preamble and the steered call's own lifecycle.
func twSteeredHeld() []aipapi.Event {
	held := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}}
	return append(held, twCallEvents("c1", correctionName, `{"q":"x"}`, 1)...)
}

// twSteeredLater is everything the backend produces after the steered call.
func twSteeredLater() []aipapi.Event {
	later := []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "after"}}
	later = append(later, twCallEvents("c2", steerToolName, `{"q":"y"}`, 2)...)
	return append(later, aipapi.Event{Kind: aipapi.EventUsageDelta, OutputTokens: 7})
}

func twSteeredScript() []aipapi.Event { return append(twSteeredHeld(), twSteeredLater()...) }

// twScriptedStream yields a fixed prefix and then a test-fed tail. Every tail
// read signals on entered, so a test can prove the receive loop processed the
// previous events and went back to the backend with nothing delivered.
type twScriptedStream struct {
	prefix    []aipapi.Event
	tail      chan aipapi.Event
	entered   chan struct{}
	closeOnce sync.Once
}

func newTWScriptedStream(prefix ...aipapi.Event) *twScriptedStream {
	return &twScriptedStream{prefix: prefix, tail: make(chan aipapi.Event, 16), entered: make(chan struct{}, 16)}
}

func (s *twScriptedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if len(s.prefix) > 0 {
		ev := s.prefix[0]
		s.prefix = s.prefix[1:]
		return ev, nil
	}
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case ev, ok := <-s.tail:
		if !ok {
			return aipapi.Event{}, io.EOF
		}
		return ev, nil
	}
}

func (s *twScriptedStream) push(evs ...aipapi.Event) {
	for _, ev := range evs {
		s.tail <- ev
	}
}

func (s *twScriptedStream) Close() error { s.closeOnce.Do(func() { close(s.tail) }); return nil }

func (*twScriptedStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// awaitBackendReads waits until the receive loop has gone back to the backend n
// times, proving the events it already consumed were fully processed; the
// timeout only guards a hang, it is never the synchronization.
func (h *twHarness) awaitBackendReads(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-h.script.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("receive loop did not return to the backend")
		}
	}
}

// twSteeringWriter keeps steering availability real enough for terminalization to
// deactivate overlays through the harness factory.
type twSteeringWriter struct{}

func (twSteeringWriter) Put(context.Context, steering.PutRequest) (steering.State, error) {
	return steering.State{}, nil
}

func (twSteeringWriter) Deactivate(context.Context, steering.OverlayID) (steering.State, error) {
	return steering.State{}, nil
}

// twHarness layers the correction wiring over the shared characterization stream:
// the steer gate, an assembler with the case's finalizers, recording, and the log.
type twHarness struct {
	stream   *retryRecvStream
	attempt  *attemptSession
	recorder *pendingFailingRecorder
	logs     *strings.Builder
	script   *twScriptedStream
}

func newTWHarness(t *testing.T, inner aipapi.ManagedEventStream, finalizers ...toolcall.Finalizer) *twHarness {
	t.Helper()
	s := newCharacterizationStream(t, inner)
	t.Cleanup(func() { _ = s.Close() })
	s.terminal.supportsContinuation = true
	s.terminal.steeringWriterFactory = func(context.Context, string, SteeringWriterResolver) (steering.Writer, error) {
		return twSteeringWriter{}, nil
	}
	s.terminal.conversationReader = &staticReader{}
	s.facts.secureTurnOK = true
	attempt := s.attempt.require()
	attempt.toolFinal = newToolCallAssembler(finalizers, 64*1024, steerCatalog())
	recorder := &pendingFailingRecorder{}
	logs := &strings.Builder{}
	s.responsePipeline.secureSessionRecorder = recorder
	s.responsePipeline.log = slog.New(slog.NewTextHandler(logs, nil))
	return &twHarness{stream: s, attempt: attempt, recorder: recorder, logs: logs}
}

type twRecvResult struct {
	ev  aipapi.Event
	err error
}

var errTWLaterFinalizer = errors.New("tw-later-finalizer-error")

// twErrorFin is one later finalizer failure, the cause a drain error carries.
type twErrorFin struct{ order int }

func (twErrorFin) ID() string    { return "tw-error-fin" }
func (f *twErrorFin) Order() int { return f.order }

func (twErrorFin) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{}, errTWLaterFinalizer
}

func twSteerFin() toolcall.Finalizer {
	return &steerFin{id: "steer", order: 10, result: steerResult()}
}

// TestToolNameCorrectionWithhold_HoldsUntilCanceled proves the hold: the steered
// call and the later output reached neither the client nor the recorder while the
// withheld usage was still accounted, and cancellation discarded them.
func TestToolNameCorrectionWithhold_HoldsUntilCanceled(t *testing.T) {
	t.Parallel()
	inner := newTWScriptedStream(twSteeredHeld()...)
	h := newTWHarness(t, inner, twSteerFin())
	h.script = inner
	for range 2 {
		if _, err := h.stream.Recv(context.Background()); err != nil {
			t.Fatalf("recv preamble: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan twRecvResult, 1)
	go func() {
		ev, err := h.stream.Recv(ctx)
		result <- twRecvResult{ev: ev, err: err}
	}()
	h.awaitBackendReads(t, 1)
	select {
	case blocked := <-result:
		t.Fatalf("client received (%s, %v) while withholding", blocked.ev.Kind, blocked.err)
	default:
	}
	later := twSteeredLater()
	h.script.push(later...)
	h.awaitBackendReads(t, len(later))
	if name, events := h.attempt.withheld.name, h.attempt.withheld.events; name != correctionName || len(events) != 3+len(later) {
		t.Fatalf("withheld = (%q, %d events), want the steered name and the call plus later output", name, len(events))
	}
	if tokens := h.attempt.accountingSnapshot().OutputTokens; tokens != 7 {
		t.Fatalf("accounted output tokens = %d, want 7: withheld usage must still be accounted", tokens)
	}
	// The recorder is the second client-facing surface: nothing the hold retains
	// may reach it either. The two preamble events are the only output the client
	// has seen so far, so the recorder must still report exactly those two.
	if recorded := h.recorder.recordedKinds(); len(recorded) != 2 {
		t.Fatalf("recorded %d events %v while withholding, want only the two preamble events", len(recorded), recorded)
	}

	cancel()
	var canceled twRecvResult
	select {
	case canceled = <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not release the blocked recv")
	}
	if canceled.ev.Kind != "" || !errors.Is(canceled.err, context.Canceled) {
		t.Fatalf("canceled recv = (%#v, %v), want no event and context.Canceled", canceled.ev, canceled.err)
	}
	if name, events := h.attempt.withheld.name, h.attempt.withheld.events; name != "" || len(events) != 0 || h.attempt.isWithholding() {
		t.Fatalf("canceled state = (%q, %d, withholding=%v), want discarded", name, len(events), h.attempt.isWithholding())
	}
}

// TestToolNameCorrectionWithhold_ReleasePaths drives the ways a withhold ends
// without a continuation: the backend finish, an end of stream, either bound's
// overflow, and a later finalizer error all deliver what was held, in original
// order and recorded exactly once, before the ending.
func TestToolNameCorrectionWithhold_ReleasePaths(t *testing.T) {
	finish := aipapi.Event{Kind: aipapi.EventResponseFinished}
	big := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: strings.Repeat("x", maxWithheldBytes/2+1)}
	flood := twSteeredHeld()
	for i := range twFloodEvents {
		flood = append(flood, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "later-" + strconv.Itoa(i)})
	}
	flood = append(flood, finish)
	cases := []struct {
		name       string
		script     []aipapi.Event
		finalizers []toolcall.Finalizer
		want       []aipapi.Event
		wantErr    error
		wantLog    bool
	}{
		{name: "backend finish releases in order", script: append(twSteeredScript(), finish), finalizers: []toolcall.Finalizer{twSteerFin()}, want: append(twSteeredScript(), finish), wantErr: io.EOF},
		{name: "end of stream releases before the end", script: twSteeredScript(), finalizers: []toolcall.Finalizer{twSteerFin()}, want: twSteeredScript(), wantErr: io.EOF},
		{name: "event bound overflow releases in order", script: flood, finalizers: []toolcall.Finalizer{twSteerFin()}, want: flood, wantErr: io.EOF, wantLog: true},
		{name: "byte bound overflow releases in order", script: append(twSteeredHeld(), big, big, finish), finalizers: []toolcall.Finalizer{twSteerFin()}, want: append(twSteeredHeld(), big, big, finish), wantErr: io.EOF, wantLog: true},
		{
			name: "later finalizer error delivers the call first", script: twSteeredHeld(), want: twSteeredHeld(), wantErr: errTWLaterFinalizer,
			finalizers: []toolcall.Finalizer{twSteerFin(), &steerDeclarerFin{order: 20, result: steerPassResult()}, &twErrorFin{order: 30}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newTWHarness(t, aipapi.NewFixedEventStream(tc.script), tc.finalizers...)
			var got []aipapi.Event
			for {
				ev, err := h.stream.Recv(context.Background())
				if err != nil {
					if !errors.Is(err, tc.wantErr) {
						t.Fatalf("terminal recv error = %v, want %v", err, tc.wantErr)
					}
					break
				}
				got = append(got, ev)
			}
			if !slices.EqualFunc(got, tc.want, func(a, b aipapi.Event) bool { return a.Kind == b.Kind && a.Delta == b.Delta }) {
				t.Fatalf("delivered %+v, want %+v", got, tc.want)
			}
			if recorded := len(h.recorder.recordedKinds()); recorded != len(tc.want) {
				t.Fatalf("recorded %d events, want %d: every released event must be recorded exactly once", recorded, len(tc.want))
			}
			if record := h.logs.String(); (strings.Contains(record, "reason=withhold_overflow") && strings.Contains(record, "outcome=delivered")) != tc.wantLog || strings.Contains(record, correctionName) {
				t.Fatalf("decision record = %q, wantOverflow=%v", record, tc.wantLog)
			}
		})
	}
}

// TestToolNameCorrectionWithhold_QueueBoundAndCleanup pins the event bound's exact
// edge and the retirement chokepoint every cancellation, error, disposal, and
// replacement path uses.
func TestToolNameCorrectionWithhold_QueueBoundAndCleanup(t *testing.T) {
	t.Parallel()
	ev := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}
	bounded := &attemptSession{}
	for range maxWithheldEvents {
		if !bounded.enqueueWithheld(ev) {
			t.Fatal("the event bound must accept up to its bound")
		}
	}
	if bounded.enqueueWithheld(ev) {
		t.Fatal("the event bound must refuse the next append")
	}
	retired := &attemptSession{}
	retired.markWithholding(correctionName, "call one of the available tools")
	if !retired.enqueueWithheld(ev) {
		t.Fatal("the fixture must hold one event")
	}
	clearAttemptToolState(newResponsePipeline(), retired)
	for name, a := range map[string]*attemptSession{"disposed": retired, "replacement": newAttemptSession(attemptSessionInput{})} {
		if a.withheld.name != "" || len(a.withheld.events) != 0 || a.isWithholding() {
			t.Fatalf("%s attempt state = (%q, %d, withholding=%v), want clean", name, a.withheld.name, len(a.withheld.events), a.isWithholding())
		}
	}
}

// twDialect returns a distinct separately allocated exactly 8192-byte dialect.
func twDialect(i int) aipapi.ReasoningDialect {
	suffix := strconv.Itoa(i)
	return aipapi.ReasoningDialect(strings.Repeat("d", aipapi.MaxReasoningDialectBytes-len(suffix)) + suffix)
}

// twBigDialectItem is one EventItem whose content holds 200 parts, each with
// its own 8192-byte dialect and a one-byte text carrier: about 1.6 MiB, past
// the 1 MiB withhold bound on its own.
func twBigDialectItem() aipapi.Event {
	parts := make([]aipapi.ContentPart, 200)
	for i := range parts {
		parts[i] = aipapi.ContentPart{Kind: aipapi.ContentPartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: twDialect(i), Text: "x"}}
	}
	return aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: parts}}
}

// twDialectFlood holds n EventItem events, each with its own 8192-byte item
// reasoning dialect and a one-byte text carrier.
func twDialectFlood(n int) []aipapi.Event {
	events := make([]aipapi.Event, n)
	for i := range events {
		events[i] = aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{Kind: aipapi.ItemKindReasoning, Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{Dialect: twDialect(i), Text: "x"}}}}
	}
	return events
}

// twDialectEventBytes is the accounted size of one twDialectFlood event:
// the same event without a dialect plus the dialect length, computed
// independently of whether PayloadBytes counts the nested dialect.
func twDialectEventBytes() int {
	ev := twDialectFlood(1)[0]
	ev.Item.Reasoning.Reasoning.Dialect = ""
	return ev.PayloadBytes() + aipapi.MaxReasoningDialectBytes
}

// twHeldPrefix builds a wantHeld expectation of total enqueues whose first
// refusal lands at index firstFalse; refusals stay refused, so the tail after
// the bound is all false.
func twHeldPrefix(total, firstFalse int) []bool {
	want := make([]bool, total)
	for i := range want {
		want[i] = i < firstFalse
	}
	return want
}

// TestToolNameCorrectionWithhold_PayloadBoundCountsNonDelta proves D1: the
// withhold memory bound counts every retained payload, not only Delta. A single
// oversized opaque reasoning event overflows on its own, and several opaque
// events overflow once their total passes 1 MiB.
func TestToolNameCorrectionWithhold_PayloadBoundCountsNonDelta(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		events []aipapi.Event
		// wantHeld[i] is whether events[i] must be held; the first false ends
		// the withhold via release in production, here it is the overflow signal.
		wantHeld []bool
	}{
		{
			name: "single oversized opaque overflows alone",
			events: []aipapi.Event{
				{Kind: aipapi.EventReasoningOpaqueDelta, Opaque: make([]byte, maxWithheldBytes+1)},
			},
			wantHeld: []bool{false},
		},
		{
			name: "cumulative opaque overflows past 1 MiB",
			events: []aipapi.Event{
				{Kind: aipapi.EventReasoningOpaqueDelta, Opaque: make([]byte, maxWithheldBytes/3+1)},
				{Kind: aipapi.EventReasoningOpaqueDelta, Opaque: make([]byte, maxWithheldBytes/3+1)},
				{Kind: aipapi.EventReasoningOpaqueDelta, Opaque: make([]byte, maxWithheldBytes/3+1)},
			},
			wantHeld: []bool{true, true, false},
		},
		{
			name: "single 2 MiB warning message overflows alone",
			events: []aipapi.Event{
				{Kind: aipapi.EventWarning, WarningCode: "slow", WarningMessage: strings.Repeat("w", 2<<20)},
			},
			wantHeld: []bool{false},
		},
		{
			name: "single oversized error message overflows alone",
			events: []aipapi.Event{
				{Kind: aipapi.EventError, ErrorCode: "boom", ErrorMessage: strings.Repeat("e", maxWithheldBytes+1)},
			},
			wantHeld: []bool{false},
		},
		{
			name: "single oversized raw usage JSON overflows alone",
			events: []aipapi.Event{
				{Kind: aipapi.EventUsageDelta, RawUsageJSON: strings.Repeat("u", maxWithheldBytes+1)},
			},
			wantHeld: []bool{false},
		},
		{
			name: "usage scopes backing storage overflows alone",
			events: []aipapi.Event{
				{Kind: aipapi.EventUsageDelta, UsageScopes: make([]aipapi.ScopedUsageDelta, 100_000)},
			},
			wantHeld: []bool{false},
		},
		{
			name:     "item content dialects overflow alone",
			events:   []aipapi.Event{twBigDialectItem()},
			wantHeld: []bool{false},
		},
		{
			name:   "many item reasoning dialects overflow cumulatively",
			events: twDialectFlood(200),
			// Each event counts its dialect-free size plus the 8192-byte
			// dialect, so floor(maxWithheldBytes/perEvent) are held before
			// the first refusal (123 today; derived so struct growth does not
			// break the row).
			wantHeld: twHeldPrefix(200, maxWithheldBytes/twDialectEventBytes()),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &attemptSession{}
			for i, ev := range tc.events {
				if got := a.enqueueWithheld(ev); got != tc.wantHeld[i] {
					t.Fatalf("enqueue %d = %v, want %v: non-Delta payload must count toward the bound", i, got, tc.wantHeld[i])
				}
			}
		})
	}
}

// TestToolNameCorrectionWithhold_ReleasePrependsDuringDrain proves D3: when an
// overflow fires on a popDrain event while later assembler events are still
// queued, the release prepends the held output ahead of them, preserving the
// canonical order on the client.
func TestToolNameCorrectionWithhold_ReleasePrependsDuringDrain(t *testing.T) {
	t.Parallel()
	// Held output that must come first: the steered call's lifecycle.
	held := twCallEvents("c1", correctionName, `{"q":"x"}`, 1)
	// The overflow trigger popped from the drain, with its siblings still queued.
	started := aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c2", ToolName: steerToolName, MessageIndex: 2}
	siblings := []aipapi.Event{
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c2", ToolName: steerToolName, MessageIndex: 2, Delta: `{"q":"y"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "c2", ToolName: steerToolName, MessageIndex: 2},
	}
	attempt := &attemptSession{}
	attempt.markWithholding(correctionName, "call one of the available tools")
	for _, ev := range held {
		if !attempt.enqueueWithheld(ev) {
			t.Fatal("fixture must hold the steered call")
		}
	}
	assembler := newToolCallAssembler(
		[]toolcall.Finalizer{&steerFin{id: "steer", order: 10, result: steerPassResult()}}, 0, steerCatalog())
	if assembler == nil {
		t.Fatal("fixture assembler must exist")
	}
	attempt.sidebandMu.Lock()
	attempt.toolFinal = assembler
	attempt.sidebandMu.Unlock()
	// Simulate the drain state at overflow: the trigger was popped, its
	// siblings are still queued.
	assembler.enqueue(siblings...)
	releaseWithheld(attempt, started)
	var got []aipapi.Event
	for {
		ev, ok := assembler.popDrain()
		if !ok {
			break
		}
		got = append(got, ev)
	}
	want := append(append(slices.Clone(held), started), siblings...)
	if !slices.EqualFunc(got, want, func(a, b aipapi.Event) bool {
		return a.Kind == b.Kind && a.ToolCallID == b.ToolCallID && a.Delta == b.Delta
	}) {
		t.Fatalf("drain order = %+v, want held then trigger then siblings in canonical order", got)
	}
}

// withholdErrStream emits a fixed prefix then one non-EOF backend error.
type withholdErrStream struct {
	events []aipapi.Event
	pos    int
	err    error
}

func (s *withholdErrStream) Recv(context.Context) (aipapi.Event, error) {
	if s.pos < len(s.events) {
		ev := s.events[s.pos]
		s.pos++
		return ev, nil
	}
	return aipapi.Event{}, s.err
}

func (s *withholdErrStream) Close() error { return nil }

func (s *withholdErrStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func drainWithholdErrStream(t *testing.T, s *retryRecvStream) ([]aipapi.Event, error) {
	t.Helper()
	var got []aipapi.Event
	for range 64 {
		ev, err := s.Recv(context.Background())
		if err != nil {
			return got, err
		}
		got = append(got, ev)
	}
	t.Fatal("receive loop did not terminate")
	return nil, nil
}

func withholdErrStreamToolNames(events []aipapi.Event) []string {
	var names []string
	for _, ev := range events {
		if ev.Kind == aipapi.EventToolCallStarted {
			names = append(names, ev.ToolName)
		}
	}
	return names
}

// TestToolNameCorrectionWithhold_BackendErrorReleasesFirst proves D2: a
// non-EOF backend error after an accepted steer releases the withheld output
// first, so the client receives the original call and the error matches the
// no-steering baseline, with no retry after output was committed.
func TestToolNameCorrectionWithhold_BackendErrorReleasesFirst(t *testing.T) {
	t.Parallel()
	backendErr := errors.New("backend transport boom")
	recoverableErr := aipapi.RecoverablePreOutputError(errors.New("backend pre-output flap"))
	prefix := append([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
	}, twCallEvents("c1", correctionName, `{"q":"x"}`, 1)...)
	cases := []struct {
		name string
		err  error
	}{
		{name: "non-recoverable", err: backendErr},
		{name: "recoverable", err: recoverableErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			steered := newTWHarness(t, &withholdErrStream{events: slices.Clone(prefix), err: tc.err}, twSteerFin())
			if tc.name == "recoverable" {
				steered.stream.recovery.recoverPolicy = newWithholdErrRecoveryPolicy()
			}
			got, gotErr := drainWithholdErrStream(t, steered.stream)
			baseline := newTWHarness(t, &withholdErrStream{events: slices.Clone(prefix), err: tc.err},
				&steerFin{id: "pass", order: 10, result: steerPassResult()})
			if tc.name == "recoverable" {
				baseline.stream.recovery.recoverPolicy = newWithholdErrRecoveryPolicy()
			}
			want, wantErr := drainWithholdErrStream(t, baseline.stream)
			if !slices.Equal(withholdErrStreamToolNames(got), []string{correctionName}) {
				t.Fatalf("steered client tool calls = %v, want the original call released before the error", withholdErrStreamToolNames(got))
			}
			if !slices.Equal(withholdErrStreamToolNames(got), withholdErrStreamToolNames(want)) {
				t.Fatalf("steered calls = %v, baseline calls = %v: withheld release must match no-steering output", withholdErrStreamToolNames(got), withholdErrStreamToolNames(want))
			}
			if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && wantErr != nil && gotErr.Error() != wantErr.Error()) {
				t.Fatalf("steered err = %v, baseline err = %v: error must match the no-steering outcome", gotErr, wantErr)
			}
		})
	}
}

// TestToolNameCorrectionWithhold_DeepCopiesRetainedEvents proves D2: the
// withhold queue retains a deep copy, so mutating the producer's Opaque,
// UsageScopes, Reasoning, Item and Decision backing storage after the enqueue
// cannot corrupt the replayed event.
func TestToolNameCorrectionWithhold_DeepCopiesRetainedEvents(t *testing.T) {
	t.Parallel()
	build := func() aipapi.Event {
		conf := 0.5
		return aipapi.Event{
			Kind:        aipapi.EventTextDelta,
			Delta:       "held",
			Opaque:      []byte{1, 2, 3},
			UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 1}},
			Reasoning:   &aipapi.ReasoningPart{Dialect: "test.dialect.v1", Text: "reason", Opaque: json.RawMessage(`{"a":1}`)},
			Item: &aipapi.Item{
				Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}},
			},
			Decision: &aipapi.DecisionResult{
				Model:   "m",
				Answers: []aipapi.DecisionAnswer{{QuestionID: "q", Kind: aipapi.DecisionKindNoul, PTrue: 0.5, Probabilities: []float64{1}, Confidence: &conf}},
			},
		}
	}
	ev, want := build(), build()
	attempt := &attemptSession{}
	if !attempt.enqueueWithheld(ev) {
		t.Fatal("fixture must hold one event")
	}
	ev.Opaque[0] = 9
	ev.UsageScopes[0].InputTokens = 999
	ev.Reasoning.Text = "mutated"
	ev.Reasoning.Opaque[0] = 'x'
	ev.Item.Content[0].Text = "mutated"
	ev.Decision.Model = "mutated"
	ev.Decision.Answers[0].PTrue = -1
	ev.Decision.Answers[0].Probabilities[0] = -1
	*ev.Decision.Answers[0].Confidence = -1
	assembler := newToolCallAssembler(
		[]toolcall.Finalizer{&steerFin{id: "steer", order: 10, result: steerPassResult()}}, 0, steerCatalog())
	if assembler == nil {
		t.Fatal("fixture assembler must exist")
	}
	attempt.sidebandMu.Lock()
	attempt.toolFinal = assembler
	attempt.sidebandMu.Unlock()
	releaseWithheld(attempt)
	got, ok := assembler.popDrain()
	if !ok {
		t.Fatal("release must replay the held event")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed event = %+v, want %+v: withhold must retain a deep copy", got, want)
	}
}

func newWithholdErrRecoveryPolicy() *streamrecovery.Policy {
	return streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true}, time.Now())
}
