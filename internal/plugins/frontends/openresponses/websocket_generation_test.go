package openresponses_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	"github.com/gorilla/websocket"
	"go.uber.org/goleak"
	"gopkg.in/yaml.v3"
)

// failAtWriter is a deterministic data-frame writer wrapper: it counts every
// socket data-frame write and returns a transport error on the Nth, delegating
// all other writes to the socket. Tests inject it via
// WebSocketHandlerConfig.WriteTextWrapper to force writer-failure closure paths.
type failAtWriter struct {
	mu     sync.Mutex
	failAt int
	calls  int
}

func newFailAtWriter(failAt int) *failAtWriter {
	return &failAtWriter{failAt: failAt}
}

func (w *failAtWriter) Wrap(next func([]byte) error) func([]byte) error {
	return func(data []byte) error {
		w.mu.Lock()
		w.calls++
		fail := w.failAt > 0 && w.calls == w.failAt
		w.mu.Unlock()
		if fail {
			return errors.New("injected writer failure")
		}
		return next(data)
	}
}

func (w *failAtWriter) callCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

// wsReadUntilError reads text frames until the connection terminates and returns
// every text frame received. It is used when a closure path ends the session
// mid-stream, so the read must tolerate a terminal error instead of failing.
func wsReadUntilError(t *testing.T, conn *websocket.Conn, timeout time.Duration) []wsTextFrame {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	var frames []wsTextFrame
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return frames
		}
		if mt != websocket.TextMessage {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		frames = append(frames, wsTextFrame{raw: append([]byte(nil), data...), data: m})
	}
}

// wsMountYAMLNode converts a YAML config string into a yaml.Node for Mount.
func wsMountYAMLNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	for n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 || n.Content[0] == nil {
			t.Fatal("empty yaml document")
		}
		n = *n.Content[0]
	}
	return n
}

// wsDialPath upgrades a WebSocket connection to an explicit path on srv.
func wsDialPath(t *testing.T, srv *httptest.Server, path string, header http.Header) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + path
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, resp, err := d.Dial(u, header)
	if err != nil {
		t.Fatalf("ws dial %s failed: %v", path, err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101, got %d", resp.StatusCode)
	}
	return conn
}

// aipsdkExecutorAdapter satisfies the wider aipsdk.ExecutorView surface while
// delegating Execute to the narrow openresponses ExecutorView used by runners.
type aipsdkExecutorAdapter struct {
	openresponses.ExecutorView
}

func (aipsdkExecutorAdapter) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

func (aipsdkExecutorAdapter) WallClock() func() time.Time { return nil }

func TestWebSocketSession_GenerationContextQuiesceClosesSessionsStreamAndStoreOnce(t *testing.T) {
	genCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocked := &streamingEventStream{
		events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}},
		wait:   make(chan struct{}),
	}
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{blocked}}

	lc := defaultLocalContinuation()
	var captured aipcont.Store
	lc.StoreFactory = func(scope aipcont.Scope) aipcont.Store {
		inner := openresponses.NewWSLocalStore(scope, lc.Limits)
		captured = &trackingLocalStore{Store: inner}
		return captured
	}

	srv, counters := newWSTestServer(t, openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               wsTestConfig(nil),
		Runner:               openresponses.NewSessionRunner(openresponses.SessionRunnerConfig{Executor: exec}),
		ShutdownCtx:          genCtx,
		LocalContinuation:    &lc,
	})
	conn := wsDial(t, srv, nil)
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	eventually(t, 3*time.Second, func() bool { return exec.count() == 1 })
	if captured == nil {
		t.Fatal("connection-local store was not allocated")
	}

	cancel()

	eventually(t, 3*time.Second, func() bool {
		return counters.Snapshot().SessionsClosed == 1
	})
	if blocked.closeCount() != 1 {
		t.Fatalf("backend stream closed %d times on generation quiesce, want 1", blocked.closeCount())
	}
	ts, ok := captured.(*trackingLocalStore)
	if !ok {
		t.Fatalf("unexpected local store type %T", captured)
	}
	if !ts.isClosed() {
		t.Fatal("connection-local store was not closed exactly once on generation quiesce")
	}
	wsReadUntilError(t, conn, 3*time.Second)
	_ = conn.Close()
}

func TestWebSocketSession_GenerationContextNewGenerationUnaffected(t *testing.T) {
	genA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	genB := t.Context()

	srvA, countersA := newWSTestServer(t, openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               wsTestConfig(nil),
		ShutdownCtx:          genA,
	})
	srvB, countersB := newWSTestServer(t, openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               wsTestConfig(nil),
		ShutdownCtx:          genB,
	})
	connA := wsDial(t, srvA, nil)
	connB := wsDial(t, srvB, nil)

	cancelA()

	eventually(t, 3*time.Second, func() bool {
		return countersA.Snapshot().SessionsClosed == 1
	})
	if countersB.Snapshot().SessionsClosed != 0 {
		t.Fatalf("generation A quiesce closed generation B sessions: %+v", countersB.Snapshot())
	}

	// Generation B's session must remain open: a short read times out rather
	// than observing a close frame.
	_ = connB.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, err := connB.ReadMessage()
	if err == nil {
		t.Fatal("generation B session produced a frame while idle")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("generation B session closed by generation A quiesce: %v", err)
	}
	_ = connA.Close()
	_ = connB.Close()
}

func TestWebSocketGeneration_NoCrossGenerationWork(t *testing.T) {
	genOld := t.Context()
	genNew := t.Context()

	execOld := &wsTurnExecutor{streams: []aipapi.EventStream{successStream("old-gen")}}
	execNew := &wsTurnExecutor{streams: []aipapi.EventStream{successStream("new-gen")}}

	srvOld, _ := newWSTurnServer(t, execOld, deterministicResponseMetadata{id: "resp_old", now: time.Unix(1_700_006_000, 0)},
		func(h *openresponses.WebSocketHandlerConfig) { h.ShutdownCtx = genOld })
	srvNew, _ := newWSTurnServer(t, execNew, deterministicResponseMetadata{id: "resp_new", now: time.Unix(1_700_006_001, 0)},
		func(h *openresponses.WebSocketHandlerConfig) { h.ShutdownCtx = genNew })

	connOld := wsDial(t, srvOld, nil)
	wsText(t, connOld, `{"type":"response.create","model":"gpt-4o","input":"old"}`)
	framesOld := wsReadUntilTerminal(t, connOld, 3*time.Second)
	if !containsDelta(framesOld, "old-gen") {
		t.Fatalf("old-generation turn output missing: %v", frameTypes(framesOld))
	}

	connNew := wsDial(t, srvNew, nil)
	wsText(t, connNew, `{"type":"response.create","model":"gpt-4o","input":"new"}`)
	framesNew := wsReadUntilTerminal(t, connNew, 3*time.Second)
	if !containsDelta(framesNew, "new-gen") {
		t.Fatalf("new-generation turn output missing: %v", frameTypes(framesNew))
	}

	if execOld.count() != 1 || execNew.count() != 1 {
		t.Fatalf("cross-generation executor work: old=%d new=%d, want 1 each", execOld.count(), execNew.count())
	}
	if got := execOld.callAt(0).Items[0].Content[0].Text; got != "old" {
		t.Fatalf("old-generation turn routed to wrong executor input %q", got)
	}
	if got := execNew.callAt(0).Items[0].Content[0].Text; got != "new" {
		t.Fatalf("new-generation turn routed to wrong executor input %q", got)
	}
	_ = connOld.Close()
	_ = connNew.Close()
}

func TestWebSocketClose_WriterFailureClosesStreamAndSessionOnce(t *testing.T) {
	script := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventResponseFinished},
	}
	cases := []struct {
		name       string
		failAt     int
		wantFrames int
	}{
		{"pre-commit", 1, 0},
		{"post-commit", 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stream := &streamingEventStream{events: script}
			exec := &wsTurnExecutor{streams: []aipapi.EventStream{stream}}
			writer := newFailAtWriter(tc.failAt)

			handler := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
				AllowUnauthenticated: true,
				Config:               wsTestConfig(nil),
				Runner:               openresponses.NewSessionRunner(openresponses.SessionRunnerConfig{Executor: exec}),
				WriteTextWrapper:     writer.Wrap,
			})
			srv := httptest.NewServer(handler)
			t.Cleanup(srv.Close)
			conn := wsDial(t, srv, nil)

			wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
			frames := wsReadUntilError(t, conn, 3*time.Second)
			_ = conn.Close()

			if len(frames) != tc.wantFrames {
				t.Fatalf("client frames=%d, want %d: %v", len(frames), tc.wantFrames, frameTypes(frames))
			}
			if tc.wantFrames == 1 {
				if got := frames[0].data["type"]; got != "response.output_item.added" {
					t.Fatalf("committed frame type=%v, want response.output_item.added", got)
				}
			}
			if exec.count() != 1 {
				t.Fatalf("executor calls=%d, want 1 (no retry after writer failure)", exec.count())
			}
			if stream.closeCount() != 1 {
				t.Fatalf("backend stream closed %d times, want exactly 1", stream.closeCount())
			}
			if writer.callCount() < tc.failAt {
				t.Fatalf("writer did not reach the failure point: calls=%d", writer.callCount())
			}
		})
	}
}

func TestWebSocketTurn_PostOutputDisconnectNoRetryNoDuplicateTerminal(t *testing.T) {
	script := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "done"},
		{Kind: aipapi.EventResponseFinished},
	}
	stream := &streamingEventStream{events: script}
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{stream}}
	srv, counters := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_after", now: time.Unix(1_700_007_000, 0)}, nil)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	frames := wsReadUntilTerminal(t, conn, 3*time.Second)
	if countFrameType(frames, "response.completed") != 1 {
		t.Fatalf("expected exactly one completed terminal, got %v", frameTypes(frames))
	}
	if !containsDelta(frames, "done") {
		t.Fatalf("turn output missing: %v", frameTypes(frames))
	}

	// Abrupt disconnect after output must not retry or produce another terminal.
	_ = conn.Close()
	eventually(t, 3*time.Second, func() bool {
		return counters.Snapshot().SessionsClosed == 1
	})
	if exec.count() != 1 {
		t.Fatalf("executor calls=%d after post-output disconnect, want 1 (no retry)", exec.count())
	}
	if stream.closeCount() != 1 {
		t.Fatalf("backend stream closed %d times, want exactly 1", stream.closeCount())
	}
}

func TestWebSocketConnectionLimit_AgeExpiryClosesLocalStoreExactlyOnce(t *testing.T) {
	cfg := wsTestConfig(func(w *openresponses.WebSocketConfig) {
		w.MaxConnectionAge = "40ms"
		w.IdleTimeout = "5m"
	})
	lc := defaultLocalContinuation()
	var capturedMu sync.Mutex
	var captured aipcont.Store
	lc.StoreFactory = func(scope aipcont.Scope) aipcont.Store {
		inner := openresponses.NewWSLocalStore(scope, lc.Limits)
		store := aipcont.Store(&trackingLocalStore{Store: inner})
		capturedMu.Lock()
		captured = store
		capturedMu.Unlock()
		return store
	}
	getCaptured := func() aipcont.Store {
		capturedMu.Lock()
		defer capturedMu.Unlock()
		return captured
	}
	srv, counters := newWSTestServer(t, openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               cfg,
		LocalContinuation:    &lc,
	})
	conn := wsDial(t, srv, nil)
	// The store is allocated in the handler goroutine after the upgrade
	// response, so it may not be visible to the test goroutine the instant the
	// dial returns.
	eventually(t, 3*time.Second, func() bool { return getCaptured() != nil })

	frames := wsReadUntilError(t, conn, 3*time.Second)
	_ = conn.Close()

	if len(frames) == 0 {
		t.Fatal("age expiry closed the connection without the classified error envelope")
	}
	if code, _ := wsErrorEnvelope(t, frames[len(frames)-1]); code != "websocket_connection_limit_reached" {
		t.Fatalf("age expiry code=%q, want websocket_connection_limit_reached", code)
	}
	eventually(t, 3*time.Second, func() bool {
		snap := counters.Snapshot()
		return snap.AgeExpired == 1 && snap.SessionsClosed == 1
	})
	store := getCaptured()
	ts, ok := store.(*trackingLocalStore)
	if !ok {
		t.Fatalf("unexpected local store type %T", store)
	}
	if !ts.isClosed() {
		t.Fatal("connection-local store was not closed exactly once on age expiry")
	}
}

func TestMount_GenerationContextClosesWebSocketSessions(t *testing.T) {
	genCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{successStream("mounted")}}
	mux := http.NewServeMux()
	if err := openresponses.Mount(mux, aipsdk.FrontendMountOptions{
		AllowUnauthenticated: true,
		PluginCfg: wsMountYAMLNode(t, `
base_path: /openresponses/v1
websocket:
  enabled: true
  max_connection_age: 60m
  idle_timeout: 5m
  max_queued_turns: 1
`),
		Exec:              aipsdkExecutorAdapter{ExecutorView: exec},
		DefaultRoute:      "gpt-4o",
		GenerationContext: genCtx,
	}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	conn := wsDialPath(t, srv, "/openresponses/v1/responses", nil)

	// The mount wires a real SessionRunner: a turn executes through the executor.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	frames := wsReadUntilTerminal(t, conn, 3*time.Second)
	if !containsDelta(frames, "mounted") {
		t.Fatalf("mounted session output missing: %v", frameTypes(frames))
	}
	if exec.count() != 1 {
		t.Fatalf("executor calls=%d, want 1", exec.count())
	}

	cancel()
	wsReadUntilError(t, conn, 3*time.Second)
	_ = conn.Close()
}

func TestWebSocketLifecycle_NoGoroutineLeak(t *testing.T) {
	defer goleak.VerifyNone(t)

	// Writer-failure session.
	stream := &streamingEventStream{events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}}}
	writer := newFailAtWriter(1)
	handler := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               wsTestConfig(nil),
		Runner:               openresponses.NewSessionRunner(openresponses.SessionRunnerConfig{Executor: &wsTurnExecutor{streams: []aipapi.EventStream{stream}}}),
		WriteTextWrapper:     writer.Wrap,
	})
	srv := httptest.NewServer(handler)
	conn := wsDial(t, srv, nil)
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	wsReadUntilError(t, conn, 3*time.Second)
	_ = conn.Close()
	srv.Close()

	// Generation-quiesce session.
	genCtx, cancel := context.WithCancel(context.Background())
	blocked := &streamingEventStream{events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}}, wait: make(chan struct{})}
	exec2 := &wsTurnExecutor{streams: []aipapi.EventStream{blocked}}
	h2 := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               wsTestConfig(nil),
		Runner:               openresponses.NewSessionRunner(openresponses.SessionRunnerConfig{Executor: exec2}),
		ShutdownCtx:          genCtx,
	})
	srv2 := httptest.NewServer(h2)
	conn2 := wsDial(t, srv2, nil)
	wsText(t, conn2, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	eventually(t, 3*time.Second, func() bool { return exec2.count() == 1 })
	cancel()
	wsReadUntilError(t, conn2, 3*time.Second)
	_ = conn2.Close()
	srv2.Close()
}
