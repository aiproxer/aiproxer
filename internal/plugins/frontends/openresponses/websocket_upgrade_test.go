package openresponses_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/gorilla/websocket"
)

// recordingRunner is a Task 6.1 SessionRunner stub: it records every dispatched
// message plus the session's authenticated scope and origin, and keeps the
// connection alive (no turn execution).
type recordingRunner struct {
	mu       sync.Mutex
	calls    int
	messages [][]byte
	origins  []string
	outcomes []sdkauth.DecisionOutcome
}

func (r *recordingRunner) HandleMessage(ctx context.Context, s *openresponses.WSSession, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.messages = append(r.messages, append([]byte(nil), data...))
	r.origins = append(r.origins, s.Origin())
	r.outcomes = append(r.outcomes, s.Auth().Outcome)
	return nil
}

func (r *recordingRunner) snapshot() (calls int, messages [][]byte, origins []string, outcomes []sdkauth.DecisionOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.messages, r.origins, r.outcomes
}

type wsTestEnvelope struct {
	Type   string `json:"type"`
	Status int    `json:"status"`
	Error  struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Param   string `json:"param,omitempty"`
	} `json:"error"`
}

func wsTestConfig(override func(*openresponses.WebSocketConfig)) openresponses.Config {
	ws := openresponses.WebSocketConfig{
		Enabled:          true,
		MaxConnectionAge: openresponses.DefaultMaxConnectionAge,
		IdleTimeout:      openresponses.DefaultIdleTimeout,
		MaxQueuedTurns:   openresponses.DefaultMaxQueuedTurns,
	}
	if override != nil {
		override(&ws)
	}
	return openresponses.Config{
		Profile:   openresponses.DefaultProfile,
		BasePath:  openresponses.DefaultBasePath,
		WebSocket: ws,
	}
}

func newWSTestServer(t *testing.T, hcfg openresponses.WebSocketHandlerConfig) (*httptest.Server, *openresponses.WSCounters) {
	t.Helper()
	handler := openresponses.NewWebSocketHandler(hcfg)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, handler.Counters()
}

func wsDial(t *testing.T, srv *httptest.Server, header http.Header) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http")
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, resp, err := d.Dial(u, header)
	if err != nil {
		t.Fatalf("ws dial failed: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101, got %d", resp.StatusCode)
	}
	return conn
}

func wsDialExpectStatus(t *testing.T, srv *httptest.Server, header http.Header) int {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http")
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	_, resp, err := d.Dial(u, header)
	if err == nil {
		if resp != nil {
			return resp.StatusCode
		}
		return http.StatusSwitchingProtocols
	}
	if resp == nil {
		t.Fatalf("ws dial failed without a response: %v", err)
	}
	return resp.StatusCode
}

func validWSRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "http://aip.test/openresponses/v1/responses", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	return req
}

func eventually(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestWebSocketUpgrade_NonUpgradeGETRejectedSafely(t *testing.T) {
	cfg := wsTestConfig(nil)
	handler := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true, Config: cfg,
	})
	counters := handler.Counters()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://aip.test/openresponses/v1/responses", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-upgrade GET, got %d", rec.Code)
	}
	snap := counters.Snapshot()
	if snap.HandshakeRejected != 1 {
		t.Errorf("handshake_rejected=%d, want 1", snap.HandshakeRejected)
	}
	if snap.SessionsOpened != 0 {
		t.Errorf("sessions_opened=%d, want 0 (no session for rejected GET)", snap.SessionsOpened)
	}
}

func TestWebSocketUpgrade_MethodGateRejectsNonGET(t *testing.T) {
	cfg := wsTestConfig(nil)
	handler := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true, Config: cfg,
	})
	counters := handler.Counters()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://aip.test/openresponses/v1/responses", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	snap := counters.Snapshot()
	if snap.MethodRejected != 1 {
		t.Errorf("method_rejected=%d, want 1", snap.MethodRejected)
	}
	if snap.SessionsOpened != 0 {
		t.Errorf("sessions_opened=%d, want 0", snap.SessionsOpened)
	}
}

func TestWebSocketUpgrade_GorillaHandshakeErrorsUseWireJSON(t *testing.T) {
	cfg := wsTestConfig(nil)
	handler := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true, Config: cfg,
	})
	req := validWSRequest()
	// This passes the handler's presence checks but is rejected by Gorilla's
	// upgrader, exercising the Upgrader.Error hook rather than writeWireError's
	// pre-upgrade validation path.
	req.Header.Set("Sec-WebSocket-Key", "not-a-valid-websocket-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type = %q, want application/json", got)
	}
	var envelope wsTestEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("expected JSON error envelope, got %q: %v", rec.Body.String(), err)
	}
	if envelope.Error.Code != "invalid_upgrade" {
		t.Fatalf("error code = %q, want invalid_upgrade", envelope.Error.Code)
	}
}

func TestWebSocketUpgrade_HandshakeFieldValidation(t *testing.T) {
	cfg := wsTestConfig(nil)
	handler := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true, Config: cfg,
	})
	counters := handler.Counters()

	cases := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"missing connection upgrade", func(r *http.Request) { r.Header.Del("Connection") }},
		{"missing upgrade header", func(r *http.Request) { r.Header.Del("Upgrade") }},
		{"wrong version", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Version", "12") }},
		{"missing key", func(r *http.Request) { r.Header.Del("Sec-WebSocket-Key") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validWSRequest()
			tc.mutate(req)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rec.Code)
			}
		})
	}

	snap := counters.Snapshot()
	if snap.HandshakeRejected != int64(len(cases)) {
		t.Errorf("handshake_rejected=%d, want %d", snap.HandshakeRejected, len(cases))
	}
	if snap.SessionsOpened != 0 {
		t.Errorf("sessions_opened=%d, want 0", snap.SessionsOpened)
	}
}

func TestWebSocketUpgrade_AuthBeforeUpgrade(t *testing.T) {
	cfg := wsTestConfig(nil)
	handler := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               cfg,
		Authorizer:           staticAuth{tenant: "t1", principal: "p1", allow: false},
	})
	counters := handler.Counters()

	rec := httptest.NewRecorder()
	req := validWSRequest()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	snap := counters.Snapshot()
	if snap.AuthRejected != 1 {
		t.Errorf("auth_rejected=%d, want 1", snap.AuthRejected)
	}
	if snap.SessionsOpened != 0 {
		t.Errorf("sessions_opened=%d, want 0 (auth must precede session allocation)", snap.SessionsOpened)
	}
}

func TestWebSocketUpgrade_OriginPolicy(t *testing.T) {
	cases := []struct {
		name           string
		allowedOrigins []string
		originHeader   string
		wantCode       int
	}{
		{"no origin allowed when allowlist empty", nil, "", http.StatusSwitchingProtocols},
		{"origin rejected when allowlist empty", nil, "https://example.com", http.StatusForbidden},
		{"explicit origin allowed", []string{"https://example.com"}, "https://example.com", http.StatusSwitchingProtocols},
		{"origin rejected when not allowlisted", []string{"https://example.com"}, "https://evil.example", http.StatusForbidden},
		{"normalized origin matches", []string{"https://example.com"}, "HTTPS://EXAMPLE.COM/", http.StatusSwitchingProtocols},
		{"non-http scheme rejected", nil, "ftp://example.com", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := wsTestConfig(func(w *openresponses.WebSocketConfig) {
				w.AllowedOrigins = append([]string(nil), tc.allowedOrigins...)
			})
			handler := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
				AllowUnauthenticated: true, Config: cfg,
			})
			counters := handler.Counters()

			var header http.Header
			if tc.originHeader != "" {
				header = http.Header{"Origin": []string{tc.originHeader}}
			}
			if tc.wantCode == http.StatusSwitchingProtocols {
				conn := wsDial(t, newWSTestServerFor(t, handler), header)
				_ = conn.Close()
				// The server publishes SessionsOpened only after the 101 handshake is
				// on the wire (upgrader.Upgrade writes the response before the counter
				// is bumped), so the client can dial and close before the increment
				// lands — a window the race detector widens into the observed
				// sessions_opened=0, want 1 flake (issue #262). Poll like the other
				// session-lifecycle assertions instead of snapshotting immediately.
				eventually(t, 3*time.Second, func() bool {
					return counters.Snapshot().SessionsOpened == 1
				})
				return
			}
			rec := httptest.NewRecorder()
			req := validWSRequest()
			if tc.originHeader != "" {
				req.Header.Set("Origin", tc.originHeader)
			}
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("expected %d, got %d", tc.wantCode, rec.Code)
			}
			snap := counters.Snapshot()
			if snap.OriginRejected != 1 {
				t.Errorf("origin_rejected=%d, want 1", snap.OriginRejected)
			}
			if snap.SessionsOpened != 0 {
				t.Errorf("sessions_opened=%d, want 0", snap.SessionsOpened)
			}
		})
	}
}

func TestWebSocketUpgrade_ValidHandshakeEstablishesBoundedSession(t *testing.T) {
	cfg := wsTestConfig(func(w *openresponses.WebSocketConfig) {
		w.AllowedOrigins = []string{"https://example.com"}
	})
	runner := &recordingRunner{}
	srv, counters := newWSTestServer(t, openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               cfg,
		Runner:               runner,
		Authorizer:           staticAuth{tenant: "t1", principal: "p1", allow: true},
	})

	conn := wsDial(t, srv, http.Header{"Origin": []string{"https://example.com"}})
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","store":false}`)); err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Second, func() bool {
		calls, _, _, _ := runner.snapshot()
		return calls >= 1
	})
	// Terminate from the client side; the server must close exactly once.
	if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	_ = conn.Close()

	eventually(t, 3*time.Second, func() bool {
		snap := counters.Snapshot()
		return snap.SessionsOpened == 1 && snap.SessionsClosed == 1
	})

	calls, messages, origins, outcomes := runner.snapshot()
	if calls != 1 {
		t.Fatalf("runner calls=%d, want 1", calls)
	}
	if string(messages[0]) != `{"type":"response.create","store":false}` {
		t.Errorf("runner message=%q", messages[0])
	}
	if origins[0] != "https://example.com" {
		t.Errorf("runner origin=%q, want https://example.com", origins[0])
	}
	if outcomes[0] != sdkauth.OutcomeAllow {
		t.Errorf("runner auth outcome=%q, want allow", outcomes[0])
	}
}

func TestWebSocketSession_MessageSizeBound(t *testing.T) {
	cfg := wsTestConfig(nil)
	srv, counters := newWSTestServer(t, openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               cfg,
		MaxMessageBytes:      1024,
		Runner: openresponses.NewSessionRunner(openresponses.SessionRunnerConfig{
			MaxMessageBytes: 1024,
		}),
	})
	conn := wsDial(t, srv, nil)

	oversized := []byte(`{"type":"response.create","model":"gpt-4o","input":"` + strings.Repeat("x", 4000) + `"}`)
	if err := conn.WriteMessage(websocket.TextMessage, oversized); err != nil {
		t.Fatalf("client write failed: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var env wsTestEnvelope
	gotLimit := false
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if mt == websocket.TextMessage && json.Unmarshal(data, &env) == nil && env.Error.Code == "limit_exceeded" {
			gotLimit = true
			break
		}
	}
	if !gotLimit {
		t.Fatal("did not receive application-level limit_exceeded envelope")
	}
	if env.Status != http.StatusRequestEntityTooLarge || env.Error.Param != "request_size" {
		t.Fatalf("unexpected size-limit envelope: %+v", env)
	}
	_ = conn.Close()
	eventually(t, 3*time.Second, func() bool {
		return counters.Snapshot().SessionsClosed == 1
	})
}

func TestWebSocketSession_AgeLimitEmitsLimitReached(t *testing.T) {
	cfg := wsTestConfig(func(w *openresponses.WebSocketConfig) {
		w.MaxConnectionAge = "40ms"
		w.IdleTimeout = "5m"
	})
	srv, counters := newWSTestServer(t, openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true, Config: cfg,
	})
	conn := wsDial(t, srv, nil)

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var env wsTestEnvelope
	gotLimit := false
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if mt == websocket.TextMessage && json.Unmarshal(data, &env) == nil {
			if env.Type == "error" && env.Error.Code == "websocket_connection_limit_reached" {
				gotLimit = true
				break
			}
		}
	}
	if !gotLimit {
		t.Fatal("did not receive websocket_connection_limit_reached error envelope")
	}
	if env.Status != http.StatusBadRequest || env.Error.Param != "connection_age" {
		t.Errorf("unexpected envelope: %+v", env)
	}

	eventually(t, 3*time.Second, func() bool {
		snap := counters.Snapshot()
		return snap.AgeExpired == 1 && snap.SessionsClosed == 1
	})
}

func TestWebSocketSession_IdleProbePingKeepsAlivePeer(t *testing.T) {
	cfg := wsTestConfig(func(w *openresponses.WebSocketConfig) {
		w.MaxConnectionAge = "60m"
		w.IdleTimeout = "2s"
	})
	srv, counters := newWSTestServer(t, openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true, Config: cfg,
	})
	conn := wsDial(t, srv, nil)

	var mu sync.Mutex
	var pings int
	conn.SetPingHandler(func(appData string) error {
		mu.Lock()
		pings++
		mu.Unlock()
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(2*time.Second))
	})

	// Remain silent for several idle windows; the server probes with pings that
	// the client answers, so the idle deadline must not close the connection.
	// The window is generous so a pong is always processed before the next probe.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	mu.Lock()
	gotPings := pings
	mu.Unlock()
	if gotPings < 1 {
		t.Errorf("expected at least one server ping on idle, got %d", gotPings)
	}

	// The observation window ended via the client read deadline, not because the
	// server closed the session: ping/pong kept the bounded session alive through
	// many idle windows.
	snap := counters.Snapshot()
	if snap.IdleClosed != 0 || snap.SessionsClosed != 0 {
		t.Fatalf("session closed during idle despite live pongs: %+v", snap)
	}

	// Now go silent; the server must observe the closed peer and release the session.
	_ = conn.Close()
	eventually(t, 3*time.Second, func() bool {
		return counters.Snapshot().SessionsClosed == 1
	})
}

func TestWebSocketSession_IdleUnresponsivePeerClosed(t *testing.T) {
	cfg := wsTestConfig(func(w *openresponses.WebSocketConfig) {
		w.MaxConnectionAge = "60m"
		w.IdleTimeout = "80ms"
	})
	srv, counters := newWSTestServer(t, openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true, Config: cfg,
	})
	conn := wsDial(t, srv, nil)

	// Do not read: the peer never answers the server's ping probe, so the server
	// must classify the connection as dead and close it.
	time.Sleep(600 * time.Millisecond)

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	_ = conn.Close()

	eventually(t, 3*time.Second, func() bool {
		snap := counters.Snapshot()
		return snap.IdleClosed == 1 && snap.SessionsClosed == 1
	})
}

func TestWebSocketSession_ShutdownContextClosesSession(t *testing.T) {
	cfg := wsTestConfig(func(w *openresponses.WebSocketConfig) {
		w.MaxConnectionAge = "60m"
		w.IdleTimeout = "80ms"
	})
	shutdownCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, counters := newWSTestServer(t, openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               cfg,
		ShutdownCtx:          shutdownCtx,
	})
	conn := wsDial(t, srv, nil)

	cancel()

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	_ = conn.Close()

	eventually(t, 3*time.Second, func() bool {
		return counters.Snapshot().SessionsClosed == 1
	})
	if snap := counters.Snapshot(); snap.AgeExpired != 0 || snap.IdleClosed != 0 {
		t.Fatalf("shutdown was misclassified: %+v", snap)
	}
}

func TestWebSocketUpgrade_RejectedAttemptsAllocateNoSession(t *testing.T) {
	cfg := wsTestConfig(nil)
	handler := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               cfg,
		Authorizer:           staticAuth{tenant: "t1", principal: "p1", allow: true},
	})
	counters := handler.Counters()

	// Method gate.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://aip.test/openresponses/v1/responses", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method gate: want 405, got %d", rec.Code)
	}

	// Origin rejection (allowlist empty, Origin present).
	rec = httptest.NewRecorder()
	req := validWSRequest()
	req.Header.Set("Origin", "https://evil.example")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("origin: want 403, got %d", rec.Code)
	}

	// Handshake rejection (missing key).
	rec = httptest.NewRecorder()
	req = validWSRequest()
	req.Header.Del("Sec-WebSocket-Key")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("handshake: want 400, got %d", rec.Code)
	}

	// Auth rejection via a deny authorizer on a fresh handler.
	deny := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               cfg,
		Authorizer:           staticAuth{allow: false},
	})
	rec = httptest.NewRecorder()
	deny.ServeHTTP(rec, validWSRequest())
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("auth: want 401, got %d", rec.Code)
	}

	snap := counters.Snapshot()
	if snap.MethodRejected != 1 || snap.OriginRejected != 1 || snap.HandshakeRejected != 1 || snap.SessionsOpened != 0 {
		t.Errorf("unexpected counters after rejected attempts: %+v", snap)
	}
	if got := deny.Counters().Snapshot().AuthRejected; got != 1 {
		t.Errorf("auth_rejected=%d, want 1", got)
	}
	if got := deny.Counters().Snapshot().SessionsOpened; got != 0 {
		t.Errorf("deny handler sessions_opened=%d, want 0", got)
	}
}

// newWSTestServerFor adapts a pre-built handler into a test server. It exists so
// origin-policy table cases can dial the exact handler under test.
func newWSTestServerFor(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}
