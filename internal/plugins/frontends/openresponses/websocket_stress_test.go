package openresponses_test

// Task 6.5 focused stress suite. Each test asserts one of the Task 6.5
// invariants under load: bounded queue, no duplicate execution, no retained
// local state beyond bounds, exactly one terminal per turn, prompt bounded
// termination of a blocked in-flight turn, and no goroutine escape.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	"go.uber.org/goleak"
)

// blockingWriteWrapper is a data-frame writer seam that blocks the Nth socket
// write until released, simulating a slow client that stops reading.
type blockingWriteWrapper struct {
	mu      sync.Mutex
	n       int
	blockAt int
	release <-chan struct{}
}

func newBlockingWriteWrapper(blockAt int, release <-chan struct{}) *blockingWriteWrapper {
	return &blockingWriteWrapper{blockAt: blockAt, release: release}
}

func (w *blockingWriteWrapper) Wrap(next func([]byte) error) func([]byte) error {
	return func(data []byte) error {
		w.mu.Lock()
		w.n++
		block := w.n == w.blockAt
		w.mu.Unlock()
		if block {
			<-w.release
		}
		return next(data)
	}
}

func (w *blockingWriteWrapper) calls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

// TestWebSocketStress_AgeLimitBoundsInflightBlockedTurn verifies the bounded
// session guarantee: a downstream turn blocked on a live backend, with the
// client staying connected, must be terminated by the configured connection age
// instead of hanging past it. The blocked stream must be closed exactly once.
func TestWebSocketStress_AgeLimitBoundsInflightBlockedTurn(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	blocked := &streamingEventStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: "slow"},
			{Kind: aipapi.EventResponseFinished},
		},
		wait: release,
	}
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{blocked}}
	srv, counters := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_age", now: time.Unix(1_700_000_500, 0)}, func(h *openresponses.WebSocketHandlerConfig) {
		h.Config.WebSocket.MaxConnectionAge = "500ms"
		h.Config.WebSocket.IdleTimeout = "5m"
	})
	conn := wsDial(t, srv, nil)
	defer func() { _ = conn.Close() }()

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	eventually(t, 3*time.Second, func() bool { return exec.count() == 1 })

	// The client stays connected and silent. Only the bounded connection age
	// may release the blocked downstream stream.
	eventually(t, 5*time.Second, func() bool {
		snap := counters.Snapshot()
		return snap.AgeExpired == 1 && snap.SessionsClosed == 1
	})
	if blocked.closeCount() != 1 {
		t.Fatalf("blocked stream closed %d times, want exactly 1", blocked.closeCount())
	}
	if exec.count() != 1 {
		t.Fatalf("executor called %d times, want 1 (no retry after cancellation)", exec.count())
	}
}

// TestWebSocketStress_SlowWriterBackpressureBoundsBuffering verifies that a
// slow client (blocked socket write) stops the runner from buffering the whole
// backend stream and never multiplexes a second turn.
func TestWebSocketStress_SlowWriterBackpressureBoundsBuffering(t *testing.T) {
	const events = 40
	script := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
	}
	for i := range events {
		script = append(script, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: fmt.Sprintf("d-%02d", i)})
	}
	script = append(script, aipapi.Event{Kind: aipapi.EventResponseFinished})

	seen := make(chan int, events+8)
	stream := &streamingEventStream{events: script, recvSeen: seen}
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{stream}}

	release := make(chan struct{})
	writer := newBlockingWriteWrapper(3, release)
	srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_slow", now: time.Now()}, func(h *openresponses.WebSocketHandlerConfig) {
		h.WriteTextWrapper = writer.Wrap
	})
	conn := wsDial(t, srv, nil)
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)

	eventually(t, 3*time.Second, func() bool { return writer.calls() >= 3 })

	// While the client-side write is blocked, the runner must not read the whole
	// backend stream into memory: reads stop at the blocked write.
	buffered := 0
	draining := true
	for draining {
		select {
		case <-seen:
			buffered++
			if buffered > 6 {
				draining = false
			}
		default:
			draining = false
		}
	}
	if buffered > 6 {
		t.Fatalf("runner buffered %d backend events behind a blocked write", buffered)
	}
	if exec.count() != 1 {
		t.Fatalf("slow writer triggered extra executions: %d", exec.count())
	}

	close(release)
	frames := wsReadUntilTerminal(t, conn, 5*time.Second)
	_ = conn.Close()
	if countFrameType(frames, "response.completed") != 1 {
		t.Fatalf("expected exactly one terminal: %v", frameTypes(frames))
	}
	for i := range events {
		want := fmt.Sprintf("d-%02d", i)
		if !containsDelta(frames, want) {
			t.Fatalf("missing delta %q: %v", want, frameTypes(frames))
		}
	}
	if exec.count() != 1 {
		t.Fatalf("executor calls=%d after drain, want 1", exec.count())
	}
}

// TestWebSocketStress_QueueSaturationOrderNoDuplicateExecution bursts a large
// number of creates behind one blocked turn and asserts: no multiplexing while
// the first turn is active, every queued turn executes exactly once in order,
// and each turn emits exactly one terminal.
func TestWebSocketStress_QueueSaturationOrderNoDuplicateExecution(t *testing.T) {
	const queued = 40
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	blocked := &streamingEventStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: "first"},
			{Kind: aipapi.EventResponseFinished},
		},
		wait: release,
	}
	streams := []aipapi.EventStream{blocked}
	for i := range queued {
		streams = append(streams, fixedStream(
			aipapi.Event{Kind: aipapi.EventResponseStarted},
			aipapi.Event{Kind: aipapi.EventMessageStarted},
			aipapi.Event{Kind: aipapi.EventTextDelta, Delta: fmt.Sprintf("turn-%02d", i)},
			aipapi.Event{Kind: aipapi.EventResponseFinished},
		))
	}
	exec := &wsTurnExecutor{streams: streams}
	srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_q", now: time.Now()}, nil)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"first"}`)
	eventually(t, 3*time.Second, func() bool { return exec.count() == 1 })
	for i := range queued {
		wsText(t, conn, fmt.Sprintf(`{"type":"response.create","model":"gpt-4o","input":"turn %d"}`, i))
	}
	time.Sleep(150 * time.Millisecond)
	if exec.count() != 1 {
		t.Fatalf("queued burst multiplexed turns: calls=%d, want 1 while blocked", exec.count())
	}

	close(release)
	released = true
	frames1 := wsReadUntilTerminal(t, conn, 5*time.Second)
	if !containsDelta(frames1, "first") || countFrameType(frames1, "response.completed") != 1 {
		t.Fatalf("first turn wrong: %v", frameTypes(frames1))
	}
	for i := range queued {
		frames := wsReadUntilTerminal(t, conn, 5*time.Second)
		want := fmt.Sprintf("turn-%02d", i)
		if !containsDelta(frames, want) {
			t.Fatalf("queued turn %d output missing %q: %v", i, want, frameTypes(frames))
		}
		if countFrameType(frames, "response.completed") != 1 {
			t.Fatalf("queued turn %d terminal count != 1: %v", i, frameTypes(frames))
		}
	}
	_ = conn.Close()
	if exec.count() != 1+queued {
		t.Fatalf("executor calls=%d, want %d (no duplicate execution)", exec.count(), 1+queued)
	}
	for i := range queued {
		want := fmt.Sprintf("turn %d", i)
		call := exec.callAt(1 + i)
		if call == nil || len(call.Items) == 0 || len(call.Items[0].Content) == 0 || call.Items[0].Content[0].Text != want {
			t.Fatalf("executor call %d input mismatch, want %q", 1+i, want)
		}
	}
}

// TestWebSocketStress_LocalStoreSaturationEvictsOldestBounds saturates the
// connection-local continuation store and asserts the retained-state invariant:
// after any number of terminal records, the store keeps at most MaxRecords of
// the newest records, evicts the oldest, bounds bytes, and clears everything on
// session close.
func TestWebSocketStress_LocalStoreSaturationEvictsOldestBounds(t *testing.T) {
	const turns = 10
	streams := make([]aipapi.EventStream, turns)
	for i := range streams {
		streams[i] = fixedStream(
			aipapi.Event{Kind: aipapi.EventResponseStarted},
			aipapi.Event{Kind: aipapi.EventMessageStarted},
			aipapi.Event{Kind: aipapi.EventTextDelta, Delta: fmt.Sprintf("t-%02d", i)},
			aipapi.Event{Kind: aipapi.EventResponseFinished},
		)
	}
	exec := &wsTurnExecutor{streams: streams}
	ids := &seqIDs{now: time.Unix(1_700_000_600, 0)}

	limits := aipcont.StorageLimits{MaxRecords: 4, MaxBytes: 64 << 10, MaxRecordBytes: 16 << 20, MaxChainDepth: 64}
	var tracking *trackingLocalStore
	var connScope aipcont.Scope
	srv, counters := newWSContinuationServer(t, exec, ids, openresponses.WSLocalContinuationConfig{
		Enabled: true,
		Limits:  limits,
		StoreFactory: func(scope aipcont.Scope) aipcont.Store {
			connScope = scope
			tracking = &trackingLocalStore{Store: openresponses.NewWSLocalStore(scope, limits)}
			return tracking
		},
	})
	conn := wsDial(t, srv, nil)

	for i := range turns {
		wsText(t, conn, fmt.Sprintf(`{"type":"response.create","model":"gpt-4o","input":"turn %d"}`, i))
		wsReadUntilTerminal(t, conn, 5*time.Second)
	}
	if tracking == nil {
		t.Fatal("no connection-local store was allocated")
	}
	if len(tracking.puts) != turns {
		t.Fatalf("recorded %d terminal records, want %d", len(tracking.puts), turns)
	}

	// While the session is open the store is bounded: only the newest MaxRecords
	// of the recorded turns remain and the oldest are evicted.
	found := 0
	for i := range turns {
		id := aipcont.ResponseID(validProxyID(fmt.Sprintf("turn-%03d", i+1)))
		if _, err := tracking.Get(context.Background(), connScope, id); err == nil {
			found++
		}
	}
	if found != limits.MaxRecords {
		t.Fatalf("retained %d records, want %d (bounded local state)", found, limits.MaxRecords)
	}
	if _, err := tracking.Get(context.Background(), connScope, aipcont.ResponseID(validProxyID("turn-001"))); err != aipcont.ErrPreviousResponseNotFound {
		t.Fatalf("oldest record Get err=%v, want %v", err, aipcont.ErrPreviousResponseNotFound)
	}
	if _, err := tracking.Get(context.Background(), connScope, aipcont.ResponseID(validProxyID("turn-010"))); err != nil {
		t.Fatalf("newest record should resolve, err=%v", err)
	}

	// Session close clears all connection-local state: no retained records and
	// no further store work after close.
	_ = conn.Close()
	eventually(t, 3*time.Second, func() bool { return counters.Snapshot().SessionsClosed == 1 })
	if !tracking.closed {
		t.Fatal("connection-local store was not closed")
	}
	if _, err := tracking.Get(context.Background(), connScope, aipcont.ResponseID(validProxyID("turn-010"))); err != aipcont.ErrStoreClosed {
		t.Fatalf("Get after close err=%v, want %v", err, aipcont.ErrStoreClosed)
	}
}

// TestWebSocketStress_DisconnectRaceCancelsExactlyOnce repeatedly disconnects a
// client while a downstream turn is blocked and asserts the stream is canceled
// exactly once, the executor never runs a duplicate turn, and the session closes.
func TestWebSocketStress_DisconnectRaceCancelsExactlyOnce(t *testing.T) {
	for i := range 20 {
		blocked := &streamingEventStream{
			events: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "slow"},
				{Kind: aipapi.EventResponseFinished},
			},
			wait: make(chan struct{}),
		}
		exec := &wsTurnExecutor{streams: []aipapi.EventStream{blocked}}
		srv, counters := newWSTurnServer(t, exec, deterministicResponseMetadata{id: fmt.Sprintf("resp_dc_%d", i), now: time.Now()}, nil)
		conn := wsDial(t, srv, nil)
		wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
		eventually(t, 3*time.Second, func() bool { return exec.count() == 1 })
		_ = conn.Close()
		eventually(t, 3*time.Second, func() bool { return counters.Snapshot().SessionsClosed == 1 })
		if blocked.closeCount() != 1 {
			t.Fatalf("iteration %d: blocked stream closed %d times, want exactly 1", i, blocked.closeCount())
		}
		if exec.count() != 1 {
			t.Fatalf("iteration %d: executor called %d times, want 1", i, exec.count())
		}
	}
}

// TestWebSocketStress_RepeatedTerminalExactlyOnePerTurn drives many turns whose
// backend streams repeat the finished terminal and asserts the frontend owns
// terminal ownership: exactly one completed terminal per turn.
func TestWebSocketStress_RepeatedTerminalExactlyOnePerTurn(t *testing.T) {
	const turns = 20
	streams := make([]aipapi.EventStream, turns)
	for i := range streams {
		streams[i] = fixedStream(
			aipapi.Event{Kind: aipapi.EventResponseStarted},
			aipapi.Event{Kind: aipapi.EventResponseFinished},
			aipapi.Event{Kind: aipapi.EventResponseFinished},
			aipapi.Event{Kind: aipapi.EventResponseFinished},
		)
	}
	exec := &wsTurnExecutor{streams: streams}
	srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_dupterm", now: time.Now()}, nil)
	conn := wsDial(t, srv, nil)
	for i := range turns {
		wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
		frames := wsReadUntilTerminal(t, conn, 5*time.Second)
		if countFrameType(frames, "response.completed") != 1 {
			t.Fatalf("turn %d: expected exactly one completed terminal, got %v", i, frameTypes(frames))
		}
	}
	_ = conn.Close()
	if exec.count() != turns {
		t.Fatalf("executor calls=%d, want %d", exec.count(), turns)
	}
}

// TestWebSocketStress_NoGoroutineLeakRepetition opens, drives, and closes many
// sessions with local continuation enabled and asserts no goroutine escapes the
// frontend package. Run with -count for repetition.
func TestWebSocketStress_NoGoroutineLeakRepetition(t *testing.T) {
	defer goleak.VerifyNone(t)

	for i := range 6 {
		exec := &wsTurnExecutor{streams: []aipapi.EventStream{fixedStream(
			aipapi.Event{Kind: aipapi.EventResponseStarted},
			aipapi.Event{Kind: aipapi.EventMessageStarted},
			aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "leak"},
			aipapi.Event{Kind: aipapi.EventResponseFinished},
		)}}
		srv, counters := newWSTurnServer(t, exec, deterministicResponseMetadata{id: fmt.Sprintf("resp_leak_%d", i), now: time.Unix(1_700_000_700, 0)}, nil)
		conn := wsDial(t, srv, nil)
		wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
		wsReadUntilTerminal(t, conn, 3*time.Second)
		_ = conn.Close()
		eventually(t, 3*time.Second, func() bool { return counters.Snapshot().SessionsClosed == 1 })
		srv.Close()
	}
}
