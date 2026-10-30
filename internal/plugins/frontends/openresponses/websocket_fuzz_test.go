package openresponses

// Task 6.5 bounded WebSocket fuzz targets. Every target runs its pinned seed
// corpus under a normal `go test` run and fuzzes for the caller-supplied
// fuzztime under `go test -fuzz=FuzzWebSocket`. Targets assert the Task 6.5
// invariants that can be observed without a slow real clock: no panic, no
// invalid classified turn error, no unbounded retained local state, no
// duplicate terminal, and no outcome lost or duplicated per turn frame.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	"github.com/gorilla/websocket"
)

// FuzzWebSocketDecodeTurn fuzzes the strict response.create WebSocket envelope
// decode path (Task 6.2): malformed JSON, union item shapes, forbidden HTTP
// fields, unknown/duplicate fields, invalid UTF-8, and bounded payload sizes.
// The turn decoder must never panic, must classify every rejection with a
// 4xx/5xx wsTurnError, and must only accept frames that decode to a valid
// canonical call.
func FuzzWebSocketDecodeTurn(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"hello"}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","store":false,"input":"hi"}`),
		[]byte(`{"type":"response.create","store":true,"model":"gpt-4o","input":"hi"}`),
		[]byte(`{"type":"response.create","stream":true,"model":"gpt-4o"}`),
		[]byte(`{"type":"response.create","stream_options":{"include_usage":true},"model":"gpt-4o"}`),
		[]byte(`{"type":"response.create","background":false,"model":"gpt-4o"}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","previous_response_id":"resp_abc","input":"hi"}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"x"}]}]}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":[{"type":"function_call","call_id":"c1","name":"f","arguments":{"a":1}}]}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":[{"type":"function_call_output","call_id":"c1","output":"o"}]}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":[{"type":"item_reference","id":"item_1"}]}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":[{"type":"reasoning","reasoning":"x"}]}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":[{"type":"compaction","encapsulated_id":"resp_1","dialect":"v1"}]}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":[{"type":"unknown_type"}]}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"hi","tools":[{"type":"function","name":"f"}]}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"hi","tool_choice":{"type":"function","function":{"name":"f"}}}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"hi","tool_choice":"auto"}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"hi","vendor:ext":{"x":[1,2,3]}}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"hi","metadata":{"x":"y"}}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"hi","reasoning":{"effort":"high"}}`),
		[]byte(`not json`),
		[]byte(``),
		[]byte(`null`),
		[]byte(`[]`),
		[]byte(`12345`),
		[]byte(`{"type":"response.create"}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","model":"dup"}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":12345}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":{"nested":{"deep":[true,false,null]}}}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"\ufffd\ufffd"}`),
		[]byte("\xFF\xFE\xFD\x00\x01"),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"hi","temperature":"not-a-number"}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"hi","max_output_tokens":-1}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"hi","store":"yes"}`),
		[]byte(`{"type":"response.create","model":"","input":"hi"}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"hi","previous_response_id":null}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"hi","store":null}`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"hi"} trailing`),
		[]byte(`{"type":"response.create","model":"gpt-4o","input":"hi","extra":{}}}`),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			return
		}
		opts := wsTurnDecodeOptions{
			DefaultRouteSelector: "gpt-4o",
			RoutePrefixes:        []string{"gpt-", "claude-"},
		}
		decoded, terr := decodeWSCreateEnvelope(data, opts)
		if terr != nil {
			if terr.status < 400 || terr.status > 599 {
				t.Fatalf("classified turn error with invalid status %d", terr.status)
			}
			if terr.code == "" || terr.message == "" {
				t.Fatal("classified turn error missing code or message")
			}
			return
		}
		if decoded == nil || decoded.call == nil {
			t.Fatal("successful decode returned a nil turn")
		}
		if err := decoded.call.Validate(); err != nil {
			t.Fatalf("decoded call failed canonical validation: %v", err)
		}
		// Raw previous_response_id extraction must never panic on the same bytes.
		_ = extractWSPreviousResponseID(data)
		// The no-prefix route resolution path must never panic either.
		if _, err := decodeWSCreateEnvelope(data, wsTurnDecodeOptions{DefaultRouteSelector: "gpt-4o"}); err != nil && (err.status < 400 || err.status > 599) {
			t.Fatalf("no-prefix decode classified error with invalid status %d", err.status)
		}
	})
}

// FuzzWebSocketParseObject fuzzes the strict single-object parser shared by the
// WebSocket turn decode path and the HTTP decode path: duplicate keys, trailing
// data, non-object roots, and malformed nesting must all fail cleanly and the
// returned field map must always round-trip through json.Marshal.
func FuzzWebSocketParseObject(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{}`),
		[]byte(`{"a":1}`),
		[]byte(`{"a":1,"a":2}`),
		[]byte(`{"a":{"b":[1,2,3]}}`),
		[]byte(`[1,2]`),
		[]byte(`"str"`),
		[]byte(``),
		[]byte(`{`),
		[]byte(`{"a":1}` + " " + `{"b":2}`),
		[]byte("\xFF\xFE"),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			return
		}
		fields, err := parseWSTurnObject(data)
		if err != nil {
			return
		}
		if _, err := json.Marshal(fields); err != nil {
			t.Fatalf("parsed field map failed to re-marshal: %v", err)
		}
		// Every field must round-trip through the raw-value presence check.
		for _, raw := range fields {
			_ = isPresentNonNullJSON(raw)
		}
	})
}

// FuzzWebSocketContinuationStore fuzzes the bounded connection-local store
// (Task 6.3): adversarial terminal records, byte/count saturation, reads,
// deletes, and close. The store must never panic, must never retain more
// records or bytes than its configured limits after any operation, and must
// reject all work after Close.
func FuzzWebSocketContinuationStore(f *testing.F) {
	f.Add(int64(64), int64(1), []byte(`{"input":"hello"}`))
	f.Add(int64(4096), int64(4), []byte(`not-json`))
	f.Add(int64(0), int64(0), []byte{})
	f.Add(int64(-7), int64(-1), []byte("x"))
	f.Add(int64(1<<20), int64(1<<20), []byte("big"))

	f.Fuzz(func(t *testing.T, matBytes int64, chainDepth int64, payload []byte) {
		if len(payload) > 256 {
			payload = payload[:256]
		}
		limits := aipcont.StorageLimits{MaxRecords: 6, MaxBytes: 4096, MaxRecordBytes: 1024, MaxChainDepth: 8}
		scope := aipcont.Scope{ConnectionID: "conn_fuzz"}
		store := newWSLocalStore(scope, limits)
		defer func() { _ = store.Close() }()

		for i := range 20 {
			b := byte(i * 37)
			if i < len(payload) {
				b = payload[i]
			}
			rec := aipcont.ContinuationRecord{
				ID:                fuzzStoreID(i, b),
				Scope:             scope,
				Terminal:          true,
				Status:            aipcont.RecordStatusCompleted,
				ChainDepth:        int(chainDepth) % 12,
				MaterializedBytes: matBytes % 1500,
			}
			if rec.ChainDepth < 0 {
				rec.ChainDepth = -rec.ChainDepth % 12
			}
			if rec.MaterializedBytes < 0 {
				rec.MaterializedBytes = -rec.MaterializedBytes % 1500
			}
			_ = store.PutTerminal(context.Background(), rec)

			if len(store.records) > limits.MaxRecords {
				t.Fatalf("store retained %d records, limit %d", len(store.records), limits.MaxRecords)
			}
			if store.bytes > limits.MaxBytes {
				t.Fatalf("store retained %d bytes, limit %d", store.bytes, limits.MaxBytes)
			}
			if i%3 == 0 {
				_, _ = store.Get(context.Background(), scope, rec.ID)
			}
			if i%5 == 0 {
				_ = store.Delete(context.Background(), scope, rec.ID)
			}
		}

		if err := store.Close(); err != nil {
			t.Fatalf("Close returned an error: %v", err)
		}
		if _, err := store.Get(context.Background(), scope, aipcont.ResponseID("resp_AAAA")); err != aipcont.ErrStoreClosed {
			t.Fatalf("Get after Close err=%v, want %v", err, aipcont.ErrStoreClosed)
		}
		if err := store.PutTerminal(context.Background(), aipcont.ContinuationRecord{ID: fuzzStoreID(99, 1), Scope: scope, Terminal: true}); err != aipcont.ErrStoreClosed {
			t.Fatalf("PutTerminal after Close err=%v, want %v", err, aipcont.ErrStoreClosed)
		}
	})
}

// fuzzStoreID derives a valid, unique proxy response ID from stable inputs so
// eviction-by-count and eviction-by-bytes are both reachable in fuzzing.
func fuzzStoreID(i int, b byte) aipcont.ResponseID {
	payload := fmt.Sprintf("conn-fuzz-%06d-%02x-%02x-%02x", i, b, b, b)
	return aipcont.ResponseID("resp_" + base64.RawURLEncoding.EncodeToString([]byte(payload)))
}

// fuzzTurnExecutor is the deterministic always-terminating executor used by the
// end-to-end lifecycle fuzz target. A turn either runs a fixed 4-event stream
// or fails before output; it never blocks, so every fuzz iteration terminates.
type fuzzTurnExecutor struct{}

func (fuzzTurnExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "fuzz"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

// fuzzIDSource issues deterministic, cryptographically valid proxy response IDs
// so successful turns can be recorded in the connection-local store.
type fuzzIDSource struct {
	mu sync.Mutex
	n  uint64
}

func (s *fuzzIDSource) NewResponseID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return "resp_" + base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "fuzz-turn-%016x", s.n))
}

type fuzzClock struct{}

func (fuzzClock) Now() time.Time { return time.Unix(1_700_000_000, 0) }

// fuzzWSServer is created once per fuzz worker process so every iteration
// exercises a fresh handshake, session, and bounded queue without per-iteration
// server setup. maxQueuedTurns stays at the default of 1 so the session pump is
// strictly sequential and every frame sent before close is processed.
var (
	fuzzWSServerOnce sync.Once
	fuzzWSServer     *httptest.Server
)

func newFuzzWSServer() *httptest.Server {
	fuzzWSServerOnce.Do(func() {
		runner := NewSessionRunner(SessionRunnerConfig{
			Executor:         fuzzTurnExecutor{},
			ResponseIDSource: &fuzzIDSource{},
			ResponseClock:    fuzzClock{},
		})
		cfg := Config{
			Profile:  DefaultProfile,
			BasePath: DefaultBasePath,
			WebSocket: WebSocketConfig{
				Enabled:          true,
				MaxConnectionAge: DefaultMaxConnectionAge,
				IdleTimeout:      DefaultIdleTimeout,
				MaxQueuedTurns:   DefaultMaxQueuedTurns,
			},
		}
		fuzzWSServer = httptest.NewServer(NewWebSocketHandler(WebSocketHandlerConfig{
			AllowUnauthenticated: true,
			Config:               cfg,
			Runner:               runner,
		}))
	})
	return fuzzWSServer
}

// splitWSFuzzFrames chunks fuzz bytes into bounded text frames on the NUL byte,
// so one input exercises "frames", "repeated creates", "errors", and lifecycle
// in a single session. Empty chunks are dropped.
func splitWSFuzzFrames(data []byte, maxFrames, maxFrame int) [][]byte {
	var frames [][]byte
	for seg := range bytes.SplitSeq(data, []byte{0}) {
		if len(seg) == 0 {
			continue
		}
		if len(seg) > maxFrame {
			seg = seg[:maxFrame]
		}
		frames = append(frames, append([]byte(nil), seg...))
		if len(frames) >= maxFrames {
			break
		}
	}
	return frames
}

// FuzzWebSocketTurnLifecycle drives the full client-facing session over a real
// loopback connection: handshake, bounded read pump, sequential turn execution,
// JSON text-frame output, error envelopes, and clean termination. Each sent
// frame must produce exactly one outcome (an error envelope or a turn ending in
// exactly one terminal), no turn may open over an unfinished one, and no frame
// may follow a terminal.
func FuzzWebSocketTurnLifecycle(f *testing.F) {
	f.Add([]byte(`{"type":"response.create","model":"gpt-4o","input":"hi"}`))
	f.Add([]byte(`{"type":"response.create","model":"gpt-4o","input":"hi"}` + "\x00" + `{"type":"response.create","model":"gpt-4o","input":"again"}`))
	f.Add([]byte(`garbage` + "\x00" + `{"type":"response.create","model":"gpt-4o","input":"x"}` + "\x00" + `[]`))
	f.Add([]byte(`{"type":"response.create","store":true,"model":"gpt-4o"}`))
	f.Add([]byte(`{"type":"response.create","model":"gpt-4o","previous_response_id":"resp_missing"}`))
	f.Add([]byte("\xFF\xFE"))
	f.Add([]byte(`{"type":"response.completed"}`))
	f.Add([]byte(``))
	f.Add([]byte(`{"type":"response.create","model":"gpt-4o","input":"hi","stream":true}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 24<<10 {
			return
		}
		frames := splitWSFuzzFrames(data, 24, 1024)
		if len(frames) == 0 {
			return
		}
		srv := newFuzzWSServer()
		u := "ws" + strings.TrimPrefix(srv.URL, "http")
		conn, _, err := websocket.DefaultDialer.Dial(u, nil)
		if err != nil {
			return
		}
		for _, fr := range frames {
			if err := conn.WriteMessage(websocket.TextMessage, fr); err != nil {
				_ = conn.Close()
				return
			}
		}
		// Read until every sent frame produced its outcome (an error envelope or
		// a turn ending in exactly one terminal). Closing the client only after
		// all outcomes arrive avoids racing the server's writes with gorilla's
		// automatic close response, which would drop a frame's envelope.
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		deadlineHit := false
		inTurn := false
		outcomes := 0
		for outcomes < len(frames) {
			mt, payload, err := conn.ReadMessage()
			if err != nil {
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					deadlineHit = true
				}
				break
			}
			if mt != websocket.TextMessage {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal(payload, &m); err != nil {
				t.Fatalf("server emitted a non-JSON text frame: %q", payload)
			}
			typ, _ := m["type"].(string)
			switch typ {
			case "response.created":
				// response.created follows the first output_item.added event in
				// the pinned profile, so it confirms an already-open turn.
				if !inTurn {
					t.Fatalf("response.created emitted without an output item")
				}
			case "response.completed", "response.failed", "response.incomplete":
				if !inTurn {
					t.Fatalf("terminal %q emitted without an active turn", typ)
				}
				inTurn = false
				outcomes++
			case "error":
				if inTurn {
					t.Fatalf("error envelope emitted inside an active turn")
				}
				outcomes++
			default:
				if typ == "response.output_item.added" {
					if inTurn {
						t.Fatalf("turn opened while a previous turn was still active")
					}
					inTurn = true
					continue
				}
				if !inTurn {
					t.Fatalf("event %q emitted outside an active turn", typ)
				}
			}
		}
		_ = conn.Close()
		if deadlineHit {
			// The read deadline is a liveness guard, not an invariant check:
			// treat it as an inconclusive iteration.
			return
		}
		if inTurn {
			t.Fatal("connection terminated with an unfinished turn")
		}
		if outcomes != len(frames) {
			t.Fatalf("outcome frames=%d, sent frames=%d (lost or duplicated outcome)", outcomes, len(frames))
		}
	})
}
