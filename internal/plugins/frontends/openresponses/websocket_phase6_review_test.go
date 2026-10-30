package openresponses_test

// Phase 6 review repair tests (black-box over real sockets and config).
//   - finding 2: an in-flight turn killed by the connection-age limit must emit
//     websocket_connection_limit_reached exactly once, before the session closes;
//   - finding 4: websocket.max_queued_bytes is a validated per-session queued-byte
//     bound coupled to the message/queue limits, and the safe default is unchanged;
//   - finding 6: origin relaxation is development-only and gated on an explicit
//     allow_any_origin flag; it is never the default.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"
)

// TestWebSocketAge_InFlightTurnEmitsLimitReachedExactlyOnce drives a blocked
// downstream turn while the client stays connected and silent, then asserts the
// bounded connection age emits the websocket_connection_limit_reached error
// envelope exactly once and only then closes the session.
func TestWebSocketAge_InFlightTurnEmitsLimitReachedExactlyOnce(t *testing.T) {
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
	srv, counters := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_age_inflight", now: time.Unix(1_700_000_500, 0)}, func(h *openresponses.WebSocketHandlerConfig) {
		h.Config.WebSocket.MaxConnectionAge = "400ms"
		h.Config.WebSocket.IdleTimeout = "5m"
	})
	conn := wsDial(t, srv, nil)
	defer func() { _ = conn.Close() }()

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hi"}`)
	eventually(t, 3*time.Second, func() bool { return exec.count() == 1 })

	// The client stays connected and silent. Only the connection age may release
	// the blocked turn; the session must emit the limit-reached envelope before
	// the close frame, exactly once.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var env wsTestEnvelope
	limitCount := 0
	readBeforeClose := false
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if mt == websocket.TextMessage && json.Unmarshal(data, &env) == nil {
			if env.Type == "error" && env.Error.Code == "websocket_connection_limit_reached" {
				limitCount++
				readBeforeClose = true
			}
		}
	}
	if !readBeforeClose {
		t.Fatal("in-flight age expiry did not emit websocket_connection_limit_reached before close")
	}
	if limitCount != 1 {
		t.Fatalf("websocket_connection_limit_reached emitted %d times, want exactly 1", limitCount)
	}
	if env.Status != http.StatusBadRequest || env.Error.Param != "connection_age" {
		t.Errorf("unexpected envelope: %+v", env)
	}

	eventually(t, 3*time.Second, func() bool {
		snap := counters.Snapshot()
		return snap.AgeExpired == 1 && snap.SessionsClosed == 1
	})
	if got := counters.Snapshot().AgeExpired; got != 1 {
		t.Fatalf("age_expired=%d, want exactly 1", got)
	}
	if blocked.closeCount() != 1 {
		t.Fatalf("blocked stream closed %d times, want exactly 1", blocked.closeCount())
	}
	if exec.count() != 1 {
		t.Fatalf("executor called %d times, want 1 (no retry after age expiry)", exec.count())
	}
}

// TestWebSocketConfig_QueuedByteBoundValidation verifies the per-session
// queued-byte bound: the safe default is unchanged, explicit zero is rejected,
// a bound below the one-message floor is rejected (it could never admit the
// message already in hand and would deadlock the read pump), and an oversized
// bound is rejected.
func TestWebSocketConfig_QueuedByteBoundValidation(t *testing.T) {
	t.Parallel()

	var defaultNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &defaultNode); err != nil {
		t.Fatal(err)
	}
	cfg, err := openresponses.DecodeConfig(defaultNode)
	if err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
	if cfg.WebSocket.MaxQueuedBytes != openresponses.DefaultMaxQueuedBytes {
		t.Fatalf("default max_queued_bytes=%d, want %d", cfg.WebSocket.MaxQueuedBytes, openresponses.DefaultMaxQueuedBytes)
	}

	valid := []string{
		`websocket: { max_queued_bytes: 16777216 }`,
		`websocket: { max_queued_bytes: 268435456 }`,
	}
	for _, y := range valid {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(y), &node); err != nil {
			t.Fatal(err)
		}
		got, err := openresponses.DecodeConfig(node)
		if err != nil {
			t.Fatalf("valid max_queued_bytes rejected for %q: %v", y, err)
		}
		if got.WebSocket.MaxQueuedBytes <= 0 {
			t.Fatalf("valid max_queued_bytes produced %d for %q", got.WebSocket.MaxQueuedBytes, y)
		}
	}

	rejected := []string{
		`websocket: { max_queued_bytes: 0 }`,
		`websocket: { max_queued_bytes: -4 }`,
		`websocket: { max_queued_bytes: 1048576 }`,
		`websocket: { max_queued_bytes: 300000000 }`,
	}
	for _, y := range rejected {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(y), &node); err != nil {
			t.Fatal(err)
		}
		if _, err := openresponses.DecodeConfig(node); err == nil {
			t.Errorf("expected max_queued_bytes rejection for %q, got nil", y)
		}
	}
}

// TestWebSocketConfig_OriginRelaxationGate verifies finding 6's config gate:
// allow_any_origin without an explicit development_mode is rejected, and the
// default configuration is never origin-relaxed.
func TestWebSocketConfig_OriginRelaxationGate(t *testing.T) {
	t.Parallel()

	rejected := []string{
		`websocket: { allow_any_origin: true }`,
		`websocket: { allow_any_origin: true, development_mode: false }`,
	}
	for _, y := range rejected {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(y), &node); err != nil {
			t.Fatal(err)
		}
		if _, err := openresponses.DecodeConfig(node); err == nil {
			t.Errorf("expected origin-relaxation rejection for %q, got nil", y)
		}
	}

	accepted := []string{
		`websocket: { development_mode: true }`,
		`websocket: { development_mode: true, allow_any_origin: true }`,
		`websocket: { development_mode: false, allow_any_origin: false }`,
		`websocket: { allowed_origins: ["https://example.com"] }`,
	}
	for _, y := range accepted {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(y), &node); err != nil {
			t.Fatal(err)
		}
		if _, err := openresponses.DecodeConfig(node); err != nil {
			t.Errorf("valid origin config rejected for %q: %v", y, err)
		}
	}

	var defaultNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &defaultNode); err != nil {
		t.Fatal(err)
	}
	cfg, err := openresponses.DecodeConfig(defaultNode)
	if err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
	if cfg.WebSocket.DevelopmentMode || cfg.WebSocket.AllowAnyOrigin {
		t.Fatalf("default config must never be origin-relaxed: %+v", cfg.WebSocket)
	}
}

// TestWebSocketUpgrade_DevModeOriginRelaxation verifies the runtime policy: with
// an explicit development_mode + allow_any_origin, any syntactically valid HTTP(S)
// origin is accepted even with an empty allowlist, while malformed origins are
// still rejected for hygiene. A programmatically-built config that bypasses
// validation must never relax origin policy (defense in depth).
func TestWebSocketUpgrade_DevModeOriginRelaxation(t *testing.T) {
	cfg := wsTestConfig(func(w *openresponses.WebSocketConfig) {
		w.AllowedOrigins = nil
		w.DevelopmentMode = true
		w.AllowAnyOrigin = true
	})
	handler := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true, Config: cfg,
	})
	srv := newWSTestServerFor(t, handler)
	counters := handler.Counters()

	// Any syntactically valid HTTP(S) origin is accepted despite the empty allowlist.
	conn := wsDial(t, srv, http.Header{"Origin": []string{"http://localhost:5173"}})
	_ = conn.Close()
	eventually(t, 3*time.Second, func() bool {
		snap := counters.Snapshot()
		return snap.SessionsOpened == 1 && snap.OriginRejected == 0
	})
	if got := counters.Snapshot().OriginRejected; got != 0 {
		t.Fatalf("origin_rejected=%d, want 0 under dev-mode any-origin", got)
	}

	// Defense in depth: a config bypassing validation that sets allow_any_origin
	// without development_mode must not relax origin policy.
	strict := wsTestConfig(func(w *openresponses.WebSocketConfig) {
		w.AllowedOrigins = nil
		w.AllowAnyOrigin = true
		w.DevelopmentMode = false
	})
	strictHandler := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true, Config: strict,
	})
	rec := httptest.NewRecorder()
	req := validWSRequest()
	req.Header.Set("Origin", "https://evil.example")
	strictHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bypassed-validation allow_any_origin relaxed the policy: got %d, want 403", rec.Code)
	}

	// Malformed origins stay rejected even in dev mode (input hygiene).
	hHandler := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true, Config: cfg,
	})
	rec = httptest.NewRecorder()
	req = validWSRequest()
	req.Header.Set("Origin", "https://user:pass@example.com")
	hHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("dev-mode any-origin accepted an origin with credentials: got %d, want 403", rec.Code)
	}
}
