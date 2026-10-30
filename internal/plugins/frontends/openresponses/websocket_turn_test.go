package openresponses_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/gorilla/websocket"
	"go.uber.org/goleak"
)

// wsTurnExecutor is the Task 6.2 scripted executor: each Execute call consumes
// one scripted stream or error, records the canonical call, and reports the
// sequential call count so tests can prove one-active-turn execution.
type wsTurnExecutor struct {
	mu            sync.Mutex
	streams       []aipapi.EventStream
	errs          []error
	calls         []*aipapi.Call
	lastCallOrder []string
}

func (e *wsTurnExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	idx := len(e.calls)
	e.calls = append(e.calls, call)
	if len(call.Items) > 0 && len(call.Items[0].Content) > 0 {
		e.lastCallOrder = append(e.lastCallOrder, call.Items[0].Content[0].Text)
	} else {
		e.lastCallOrder = append(e.lastCallOrder, "")
	}
	if idx < len(e.errs) && e.errs[idx] != nil {
		return nil, e.errs[idx]
	}
	if idx < len(e.streams) {
		return e.streams[idx], nil
	}
	return nil, errors.New("no scripted stream")
}

func (e *wsTurnExecutor) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

func (e *wsTurnExecutor) callAt(i int) *aipapi.Call {
	e.mu.Lock()
	defer e.mu.Unlock()
	if i >= len(e.calls) {
		return nil
	}
	return e.calls[i]
}

// wsTextFrame is one parsed client-visible text frame plus its raw JSON bytes.
type wsTextFrame struct {
	raw  []byte
	data map[string]any
}

func fixedStream(events ...aipapi.Event) aipapi.EventStream {
	return aipapi.NewFixedEventStream(events)
}

func wsText(t *testing.T, conn *websocket.Conn, msg string) {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
		t.Fatalf("ws client write failed: %v", err)
	}
}

func wsReadTextFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration) wsTextFrame {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("ws read frame: %v", err)
		}
		if mt != websocket.TextMessage {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("ws frame is not a JSON object: %s", data)
		}
		return wsTextFrame{raw: append([]byte(nil), data...), data: m}
	}
}

func isWSTerminalType(typ string) bool {
	switch typ {
	case "response.completed", "response.incomplete", "response.failed":
		return true
	}
	return false
}

// wsReadUntilTerminal reads text frames until a terminal response event or a
// classified error envelope. It returns every frame read, including the terminal.
func wsReadUntilTerminal(t *testing.T, conn *websocket.Conn, timeout time.Duration) []wsTextFrame {
	t.Helper()
	var frames []wsTextFrame
	for {
		f := wsReadTextFrame(t, conn, timeout)
		frames = append(frames, f)
		typ, _ := f.data["type"].(string)
		if typ == "error" || isWSTerminalType(typ) {
			return frames
		}
	}
}

func frameTypes(frames []wsTextFrame) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i], _ = f.data["type"].(string)
	}
	return out
}

func countFrameType(frames []wsTextFrame, want string) int {
	n := 0
	for _, typ := range frameTypes(frames) {
		if typ == want {
			n++
		}
	}
	return n
}

func containsDelta(frames []wsTextFrame, want string) bool {
	for _, f := range frames {
		if d, ok := f.data["delta"].(string); ok && d == want {
			return true
		}
	}
	return false
}

func wsErrorEnvelope(t *testing.T, f wsTextFrame) (code, param string) {
	t.Helper()
	if typ, _ := f.data["type"].(string); typ != "error" {
		t.Fatalf("expected error envelope, got type %q: %s", typ, f.raw)
	}
	errObj, ok := f.data["error"].(map[string]any)
	if !ok {
		t.Fatalf("error envelope missing error object: %s", f.raw)
	}
	code, _ = errObj["code"].(string)
	param, _ = errObj["param"].(string)
	return code, param
}

func canonicalJSON(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// newWSTurnServer wires a real upgrade handler to a Task 6.2 SessionRunner and
// returns a live test server plus its transport counters.
func newWSTurnServer(t *testing.T, exec *wsTurnExecutor, ids deterministicResponseMetadata, mutate func(*openresponses.WebSocketHandlerConfig)) (*httptest.Server, *openresponses.WSCounters) {
	t.Helper()
	var execView openresponses.ExecutorView
	if exec != nil {
		execView = exec
	}
	runner := openresponses.NewSessionRunner(openresponses.SessionRunnerConfig{
		Executor:         execView,
		ResponseIDSource: ids,
		ResponseClock:    ids,
	})
	hcfg := openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               wsTestConfig(nil),
		Runner:               runner,
	}
	if mutate != nil {
		mutate(&hcfg)
	}
	handler := openresponses.NewWebSocketHandler(hcfg)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, handler.Counters()
}

func TestWebSocketTurn_ValidCreateEmitsProtocolStreamEvents(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{fixedStream(
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)}}
	srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_ws_1", now: time.Unix(1_700_000_100, 0)}, nil)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hello"}`)
	frames := wsReadUntilTerminal(t, conn, 3*time.Second)
	_ = conn.Close()

	want := []string{
		"response.output_item.added",
		"response.created",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}
	got := frameTypes(frames)
	if len(got) != len(want) {
		t.Fatalf("event count=%d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frame %d type=%q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
	for i, f := range frames {
		seq, _ := f.data["sequence_number"].(float64)
		if int(seq) != i {
			t.Fatalf("frame %d has sequence_number=%v, want %d", i, f.data["sequence_number"], i)
		}
		if !strings.HasPrefix(string(f.raw), "{") {
			t.Fatalf("frame %d is not a plain JSON object (SSE framing leaked): %s", i, f.raw)
		}
	}
	for _, raw := range frames {
		if strings.Contains(string(raw.raw), "[DONE]") || strings.Contains(string(raw.raw), "event:") {
			t.Fatalf("SSE-only framing leaked into a WebSocket frame: %s", raw.raw)
		}
	}

	terminal := frames[len(frames)-1]
	res, _ := terminal.data["response"].(map[string]any)
	if res == nil {
		t.Fatalf("terminal frame missing response resource: %s", terminal.raw)
	}
	if store, _ := res["store"].(bool); store {
		t.Fatalf("expected store=false in WS response resource, got %v", res["store"])
	}
	if id, _ := res["id"].(string); id != "resp_ws_1" {
		t.Fatalf("response id=%q, want resp_ws_1", id)
	}
	if exec.count() != 1 {
		t.Fatalf("executor calls=%d, want 1", exec.count())
	}
	call := exec.callAt(0)
	if call == nil {
		t.Fatal("executor received no call")
	}
	if call.Invocation.Operation != aipapi.OperationOpenResponsesCreate ||
		call.Invocation.DeliveryMode != aipapi.DeliveryModeStreaming ||
		call.Invocation.TransportMode != aipapi.TransportModeStreaming {
		t.Fatalf("unexpected invocation: %+v", call.Invocation)
	}
	if call.Route.Selector != "gpt-4o" {
		t.Fatalf("route selector=%q, want gpt-4o", call.Route.Selector)
	}
	if len(call.Items) != 1 || call.Items[0].Role != aipapi.RoleUser ||
		len(call.Items[0].Content) != 1 || call.Items[0].Content[0].Text != "hello" {
		t.Fatalf("canonical items differ from the HTTP decode path: %+v", call.Items)
	}
}

func TestWebSocketTurn_EventParityWithHTTPSSE(t *testing.T) {
	scripted := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventResponseFinished},
	}
	ids := deterministicResponseMetadata{id: "resp_parity", now: time.Unix(1_700_000_200, 0)}

	// HTTP/SSE side: the same canonical events through the same state machine.
	sseHandler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Executor:             &streamingExecutor{stream: fixedStream(scripted...)},
		ResponseIDSource:     ids,
		ResponseClock:        ids,
	})
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses",
		strings.NewReader(`{"model":"gpt-4o","input":"hello","stream":true,"store":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	sseHandler.ServeHTTP(rec, req)

	var sseEvents []map[string]any
	for line := range strings.SplitSeq(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatalf("invalid SSE data payload: %s", payload)
		}
		sseEvents = append(sseEvents, m)
	}
	if len(sseEvents) == 0 {
		t.Fatal("SSE handler produced no data events")
	}

	// WebSocket side: the same scripted events through the same state machine.
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{fixedStream(scripted...)}}
	srv, _ := newWSTurnServer(t, exec, ids, nil)
	conn := wsDial(t, srv, nil)
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hello","store":false}`)
	wsFrames := wsReadUntilTerminal(t, conn, 3*time.Second)
	_ = conn.Close()

	if len(wsFrames) != len(sseEvents) {
		t.Fatalf("WS frame count=%d, SSE event count=%d", len(wsFrames), len(sseEvents))
	}
	for i := range wsFrames {
		// completed_at is real wall-clock time captured at different instants on
		// each path; a second boundary between the sequential runs can make it
		// differ by 1, which is not a parity violation. Every other field
		// (including the deterministic created_at from the injected clock) must
		// match exactly.
		ws := maps.Clone(wsFrames[i].data)
		sse := maps.Clone(sseEvents[i])
		delete(ws, "completed_at")
		delete(sse, "completed_at")
		if resp, ok := ws["response"].(map[string]any); ok {
			respCopy := maps.Clone(resp)
			delete(respCopy, "completed_at")
			ws["response"] = respCopy
		}
		if resp, ok := sse["response"].(map[string]any); ok {
			respCopy := maps.Clone(resp)
			delete(respCopy, "completed_at")
			sse["response"] = respCopy
		}
		if canonicalJSON(ws) != canonicalJSON(sse) {
			t.Fatalf("frame %d differs between WebSocket and SSE\nWS:  %s\nSSE: %s",
				i, canonicalJSON(ws), canonicalJSON(sse))
		}
	}
	if wsFrames[len(wsFrames)-1].data["type"] != "response.completed" {
		t.Fatalf("WS terminal type=%v, want response.completed", wsFrames[len(wsFrames)-1].data["type"])
	}
	if exec.count() != 1 {
		t.Fatalf("executor calls=%d, want 1", exec.count())
	}
}

func TestWebSocketTurn_SequentialTurnsExecuteInOrder(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{
		fixedStream(
			aipapi.Event{Kind: aipapi.EventResponseStarted},
			aipapi.Event{Kind: aipapi.EventMessageStarted},
			aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "one"},
			aipapi.Event{Kind: aipapi.EventResponseFinished},
		),
		fixedStream(
			aipapi.Event{Kind: aipapi.EventResponseStarted},
			aipapi.Event{Kind: aipapi.EventMessageStarted},
			aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "two"},
			aipapi.Event{Kind: aipapi.EventResponseFinished},
		),
	}}
	srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_seq", now: time.Unix(1_700_000_300, 0)}, nil)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"first"}`)
	frames1 := wsReadUntilTerminal(t, conn, 3*time.Second)
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"second"}`)
	frames2 := wsReadUntilTerminal(t, conn, 3*time.Second)
	_ = conn.Close()

	if exec.count() != 2 {
		t.Fatalf("executor calls=%d, want 2", exec.count())
	}
	if !containsDelta(frames1, "one") {
		t.Fatalf("first turn missing its delta: %v", frameTypes(frames1))
	}
	if !containsDelta(frames2, "two") {
		t.Fatalf("second turn missing its delta: %v", frameTypes(frames2))
	}
	if countFrameType(frames1, "response.completed") != 1 || countFrameType(frames2, "response.completed") != 1 {
		t.Fatalf("expected exactly one terminal per turn")
	}
	if got := exec.callAt(0).Items[0].Content[0].Text; got != "first" {
		t.Fatalf("turn 1 input=%q, want first", got)
	}
	if got := exec.callAt(1).Items[0].Content[0].Text; got != "second" {
		t.Fatalf("turn 2 input=%q, want second", got)
	}
}

func TestWebSocketTurn_SequentialStressManyTurns(t *testing.T) {
	const turns = 25
	streams := make([]aipapi.EventStream, turns)
	for i := range streams {
		streams[i] = fixedStream(
			aipapi.Event{Kind: aipapi.EventResponseStarted},
			aipapi.Event{Kind: aipapi.EventMessageStarted},
			aipapi.Event{Kind: aipapi.EventTextDelta, Delta: fmt.Sprintf("turn-%d", i)},
			aipapi.Event{Kind: aipapi.EventResponseFinished},
		)
	}
	exec := &wsTurnExecutor{streams: streams}
	srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_stress", now: time.Now()}, nil)
	conn := wsDial(t, srv, nil)

	for i := range turns {
		wsText(t, conn, fmt.Sprintf(`{"type":"response.create","model":"gpt-4o","input":"turn %d"}`, i))
		frames := wsReadUntilTerminal(t, conn, 5*time.Second)
		if !containsDelta(frames, fmt.Sprintf("turn-%d", i)) {
			t.Fatalf("turn %d produced wrong output: %v", i, frameTypes(frames))
		}
		if countFrameType(frames, "response.completed") != 1 {
			t.Fatalf("turn %d did not terminate exactly once", i)
		}
	}
	_ = conn.Close()
	if exec.count() != turns {
		t.Fatalf("executor calls=%d, want %d", exec.count(), turns)
	}
	for i := range turns {
		if got := exec.callAt(i).Items[0].Content[0].Text; got != fmt.Sprintf("turn %d", i) {
			t.Fatalf("turn %d input=%q", i, got)
		}
	}
}

func TestWebSocketTurn_ConcurrentCreatesCannotStartTwoActiveStreams(t *testing.T) {
	release := make(chan struct{})
	blocked := &streamingEventStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: "slow"},
			{Kind: aipapi.EventResponseFinished},
		},
		wait: release,
	}
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{
		blocked,
		fixedStream(
			aipapi.Event{Kind: aipapi.EventResponseStarted},
			aipapi.Event{Kind: aipapi.EventMessageStarted},
			aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "second"},
			aipapi.Event{Kind: aipapi.EventResponseFinished},
		),
	}}
	srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_nm", now: time.Unix(1_700_000_400, 0)}, nil)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"one"}`)
	eventually(t, 3*time.Second, func() bool { return exec.count() == 1 })

	// A second create while the first is still streaming must not start a stream.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"two"}`)
	time.Sleep(100 * time.Millisecond)
	if exec.count() != 1 {
		t.Fatalf("second create started a stream while one was active: calls=%d", exec.count())
	}

	close(release)
	frames1 := wsReadUntilTerminal(t, conn, 3*time.Second)
	if !containsDelta(frames1, "slow") {
		t.Fatalf("first turn output missing: %v", frameTypes(frames1))
	}
	frames2 := wsReadUntilTerminal(t, conn, 3*time.Second)
	if !containsDelta(frames2, "second") {
		t.Fatalf("second turn output missing: %v", frameTypes(frames2))
	}
	_ = conn.Close()
	if exec.count() != 2 {
		t.Fatalf("executor calls=%d, want 2", exec.count())
	}
}

func TestWebSocketTurn_BoundedQueueBackpressure(t *testing.T) {
	release := make(chan struct{})
	blocked := &streamingEventStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: "slow"},
			{Kind: aipapi.EventResponseFinished},
		},
		wait: release,
	}
	streams := []aipapi.EventStream{blocked}
	for i := range 5 {
		streams = append(streams, fixedStream(
			aipapi.Event{Kind: aipapi.EventResponseStarted},
			aipapi.Event{Kind: aipapi.EventMessageStarted},
			aipapi.Event{Kind: aipapi.EventTextDelta, Delta: fmt.Sprintf("burst-%d", i)},
			aipapi.Event{Kind: aipapi.EventResponseFinished},
		))
	}
	exec := &wsTurnExecutor{streams: streams}
	srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_bp", now: time.Unix(1_700_000_500, 0)}, nil)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"one"}`)
	eventually(t, 3*time.Second, func() bool { return exec.count() == 1 })

	// Burst five more creates while the first turn is blocked. The session queue
	// is bounded (max_queued_turns=1) and the read pump stops reading once it is
	// full, so the burst cannot start a second active stream.
	for i := range 5 {
		wsText(t, conn, fmt.Sprintf(`{"type":"response.create","model":"gpt-4o","input":"burst %d"}`, i))
	}
	time.Sleep(100 * time.Millisecond)
	if exec.count() != 1 {
		t.Fatalf("burst started extra streams while one turn was active: calls=%d", exec.count())
	}

	close(release)
	frames1 := wsReadUntilTerminal(t, conn, 3*time.Second)
	if !containsDelta(frames1, "slow") {
		t.Fatalf("first turn output missing: %v", frameTypes(frames1))
	}
	for i := range 5 {
		frames := wsReadUntilTerminal(t, conn, 3*time.Second)
		want := fmt.Sprintf("burst-%d", i)
		if !containsDelta(frames, want) {
			t.Fatalf("burst turn %d output missing %q: %v", i, want, frameTypes(frames))
		}
		if countFrameType(frames, "response.completed") != 1 {
			t.Fatalf("burst turn %d did not terminate exactly once", i)
		}
	}
	_ = conn.Close()
	if exec.count() != 6 {
		t.Fatalf("executor calls=%d, want 6 (one blocked + five queued)", exec.count())
	}
}

func TestWebSocketTurn_InvalidEnvelopesKeepConnectionAlive(t *testing.T) {
	cases := []struct {
		name      string
		msg       string
		wantCode  string
		wantParam string
	}{
		{"malformed json", `not-json`, "invalid_request", ""},
		{"non object", `[1,2,3]`, "invalid_request", ""},
		{"missing type", `{"model":"gpt-4o","input":"hi"}`, "invalid_message_type", "type"},
		{"wrong type", `{"type":"response.completed"}`, "invalid_message_type", "type"},
		{"forbidden stream", `{"type":"response.create","stream":true,"model":"gpt-4o","input":"hi"}`, "field_not_allowed", "stream"},
		{"forbidden stream_options", `{"type":"response.create","stream_options":{"include_usage":true},"model":"gpt-4o","input":"hi"}`, "field_not_allowed", "stream_options"},
		{"forbidden background", `{"type":"response.create","background":false,"model":"gpt-4o","input":"hi"}`, "field_not_allowed", "background"},
		{"parent reference", `{"type":"response.create","model":"gpt-4o","previous_response_id":"resp_abc","input":"hi"}`, "previous_response_not_found", "previous_response_id"},
		{"store true", `{"type":"response.create","model":"gpt-4o","store":true,"input":"hi"}`, "unsupported_parameter", "store"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &wsTurnExecutor{streams: []aipapi.EventStream{fixedStream(
				aipapi.Event{Kind: aipapi.EventResponseStarted},
				aipapi.Event{Kind: aipapi.EventMessageStarted},
				aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "alive"},
				aipapi.Event{Kind: aipapi.EventResponseFinished},
			)}}
			srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_inv", now: time.Unix(1_700_000_600, 0)}, nil)
			conn := wsDial(t, srv, nil)

			wsText(t, conn, tc.msg)
			f := wsReadTextFrame(t, conn, 3*time.Second)
			code, param := wsErrorEnvelope(t, f)
			if code != tc.wantCode {
				t.Fatalf("error code=%q, want %q (frame: %s)", code, tc.wantCode, f.raw)
			}
			if tc.wantParam != "" && param != tc.wantParam {
				t.Fatalf("error param=%q, want %q (frame: %s)", param, tc.wantParam, f.raw)
			}
			if exec.count() != 0 {
				t.Fatalf("invalid turn reached the executor: calls=%d", exec.count())
			}

			// The connection must survive the classified error and still serve turns.
			wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"next"}`)
			frames := wsReadUntilTerminal(t, conn, 3*time.Second)
			if !containsDelta(frames, "alive") {
				t.Fatalf("connection did not survive the invalid turn: %v", frameTypes(frames))
			}
			_ = conn.Close()
			if exec.count() != 1 {
				t.Fatalf("executor calls=%d after follow-up, want 1", exec.count())
			}
		})
	}
}

func TestWebSocketTurn_StoreFalseAcceptedAndEchoed(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{fixedStream(
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ok"},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)}}
	srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_store_false", now: time.Unix(1_700_000_700, 0)}, nil)
	conn := wsDial(t, srv, nil)

	// store:false is the pinned connection-local WebSocket form; it must be
	// accepted and echoed in the response resource, not rejected.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","store":false,"input":"hi"}`)
	frames := wsReadUntilTerminal(t, conn, 3*time.Second)
	_ = conn.Close()

	terminal := frames[len(frames)-1]
	res, _ := terminal.data["response"].(map[string]any)
	if res == nil {
		t.Fatalf("terminal missing response: %s", terminal.raw)
	}
	if store, _ := res["store"].(bool); store {
		t.Fatalf("expected store=false echoed in response, got %v", res["store"])
	}
	if exec.count() != 1 {
		t.Fatalf("executor calls=%d, want 1", exec.count())
	}
}

func TestWebSocketTurn_PreOutputBackendErrorEmitsClassifiedError(t *testing.T) {
	exec := &wsTurnExecutor{
		errs: []error{errors.New("native secret: postgres://u:p@db")},
		streams: []aipapi.EventStream{nil, fixedStream(
			aipapi.Event{Kind: aipapi.EventResponseStarted},
			aipapi.Event{Kind: aipapi.EventMessageStarted},
			aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "alive"},
			aipapi.Event{Kind: aipapi.EventResponseFinished},
		)},
	}
	srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_err", now: time.Unix(1_700_000_800, 0)}, nil)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	f := wsReadTextFrame(t, conn, 3*time.Second)
	code, _ := wsErrorEnvelope(t, f)
	if code != "backend_error" {
		t.Fatalf("error code=%q, want backend_error (frame: %s)", code, f.raw)
	}
	if strings.Contains(string(f.raw), "secret") || strings.Contains(string(f.raw), "postgres") {
		t.Fatalf("classified error leaked an internal message: %s", f.raw)
	}

	// Connection stays alive after the classified pre-output failure.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"next"}`)
	frames := wsReadUntilTerminal(t, conn, 3*time.Second)
	if !containsDelta(frames, "alive") {
		t.Fatalf("connection did not survive the backend error: %v", frameTypes(frames))
	}
	_ = conn.Close()
	if exec.count() != 2 {
		t.Fatalf("executor calls=%d, want 2", exec.count())
	}
}

func TestWebSocketTurn_PreOutputStreamErrorEmitsClassifiedError(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{&streamingEventStream{err: errors.New("native secret")}}}
	srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_stream_err", now: time.Unix(1_700_000_900, 0)}, nil)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	f := wsReadTextFrame(t, conn, 3*time.Second)
	code, _ := wsErrorEnvelope(t, f)
	if code != "backend_error" {
		t.Fatalf("error code=%q, want backend_error (frame: %s)", code, f.raw)
	}
	if strings.Contains(string(f.raw), "secret") {
		t.Fatalf("classified error leaked an internal message: %s", f.raw)
	}
	_ = conn.Close()
}

func TestWebSocketTurn_PostOutputFailureEmitsFailedTerminalOnce(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{&streamingEventStream{
		events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}},
		err:    errors.New("native secret"),
	}}}
	srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_fail", now: time.Unix(1_700_001_000, 0)}, nil)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	frames := wsReadUntilTerminal(t, conn, 3*time.Second)
	_ = conn.Close()

	got := frameTypes(frames)
	if len(got) == 0 || got[0] != "response.output_item.added" {
		t.Fatalf("first frame=%q, want response.output_item.added (committed output): %v", got[0], got)
	}
	if got[len(got)-1] != "response.failed" {
		t.Fatalf("terminal frame=%q, want response.failed: %v", got[len(got)-1], got)
	}
	if countFrameType(frames, "response.failed") != 1 {
		t.Fatalf("expected exactly one failed terminal: %v", got)
	}
	if strings.Contains(string(frames[len(frames)-1].raw), "secret") {
		t.Fatalf("terminal failed event leaked an internal message: %s", frames[len(frames)-1].raw)
	}
	if exec.count() != 1 {
		t.Fatalf("executor calls=%d, want 1", exec.count())
	}
}

func TestWebSocketTurn_DuplicateBackendTerminalEmitsSingleTerminal(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{fixedStream(
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)}}
	srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_dup", now: time.Unix(1_700_001_100, 0)}, nil)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	frames := wsReadUntilTerminal(t, conn, 3*time.Second)
	if countFrameType(frames, "response.completed") != 1 {
		t.Fatalf("expected exactly one terminal, got: %v", frameTypes(frames))
	}

	// No frame may follow the terminal: the frontend owns terminal ownership.
	_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("received a frame after the terminal event")
	}
	_ = conn.Close()
	if exec.count() != 1 {
		t.Fatalf("executor calls=%d, want 1", exec.count())
	}
}

func TestWebSocketTurn_DisconnectCancelsInflightTurn(t *testing.T) {
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
	srv, counters := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_cancel", now: time.Unix(1_700_001_200, 0)}, nil)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	eventually(t, 3*time.Second, func() bool { return exec.count() == 1 })

	// Client disconnects while the turn is blocked on downstream output.
	_ = conn.Close()

	eventually(t, 3*time.Second, func() bool {
		return counters.Snapshot().SessionsClosed == 1
	})
	if blocked.closeCount() != 1 {
		t.Fatalf("backend stream closed %d times, want exactly 1", blocked.closeCount())
	}
}

func TestWebSocketTurn_ShutdownCancelsInflightTurn(t *testing.T) {
	shutdownCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	srv, counters := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_shutdown", now: time.Unix(1_700_001_300, 0)},
		func(h *openresponses.WebSocketHandlerConfig) { h.ShutdownCtx = shutdownCtx })
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	eventually(t, 3*time.Second, func() bool { return exec.count() == 1 })

	cancel()
	eventually(t, 3*time.Second, func() bool {
		return counters.Snapshot().SessionsClosed == 1
	})
	if blocked.closeCount() != 1 {
		t.Fatalf("backend stream closed %d times, want exactly 1", blocked.closeCount())
	}
}

func TestWebSocketTurn_NilExecutorEmitsClassifiedErrorAndKeepsConnection(t *testing.T) {
	srv, _ := newWSTurnServer(t, nil, deterministicResponseMetadata{id: "resp_noop", now: time.Unix(1_700_001_400, 0)}, nil)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	f := wsReadTextFrame(t, conn, 3*time.Second)
	code, _ := wsErrorEnvelope(t, f)
	if code != "operation_not_implemented" {
		t.Fatalf("error code=%q, want operation_not_implemented (frame: %s)", code, f.raw)
	}

	// Still alive: another invalid (or valid) message must not hang the session.
	wsText(t, conn, `{"type":"response.create","stream":true,"model":"gpt-4o","input":"hi"}`)
	f = wsReadTextFrame(t, conn, 3*time.Second)
	if code2, _ := wsErrorEnvelope(t, f); code2 != "field_not_allowed" {
		t.Fatalf("second error code=%q, want field_not_allowed", code2)
	}
	_ = conn.Close()
}

func TestWebSocketTurn_NoGoroutineLeak(t *testing.T) {
	defer goleak.VerifyNone(t)

	exec := &wsTurnExecutor{streams: []aipapi.EventStream{fixedStream(
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)}}
	srv, counters := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_leak", now: time.Unix(1_700_001_500, 0)}, nil)
	conn := wsDial(t, srv, nil)
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	wsReadUntilTerminal(t, conn, 3*time.Second)
	_ = conn.Close()
	eventually(t, 3*time.Second, func() bool {
		return counters.Snapshot().SessionsClosed == 1
	})
	srv.Close()
}
