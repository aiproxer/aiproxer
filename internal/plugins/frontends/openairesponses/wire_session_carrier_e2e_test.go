package openairesponses_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

type logCaptureSpy struct {
	mu      sync.Mutex
	records []string
}

func (s *logCaptureSpy) Enabled(context.Context, slog.Level) bool { return true }
func (s *logCaptureSpy) Handle(_ context.Context, r slog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" ")
		b.WriteString(a.Key)
		b.WriteString("=")
		fmt.Fprintf(&b, "%v", a.Value.Any())
		return true
	})
	s.records = append(s.records, b.String())
	return nil
}
func (s *logCaptureSpy) WithAttrs(attrs []slog.Attr) slog.Handler { return s }
func (s *logCaptureSpy) WithGroup(name string) slog.Handler       { return s }

func (s *logCaptureSpy) Contains(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.records {
		if strings.Contains(rec, substr) {
			return true
		}
	}
	return false
}

type metricsSpy struct {
	newCalls    atomic.Int64
	resumeCalls atomic.Int64
	deniedCalls atomic.Int64
	deniedCodes []string
	mu          sync.Mutex
}

func (m *metricsSpy) ObserveBeginTurnNew()    { m.newCalls.Add(1) }
func (m *metricsSpy) ObserveBeginTurnResume() { m.resumeCalls.Add(1) }
func (m *metricsSpy) ObserveBeginTurnDenied(code string) {
	m.deniedCalls.Add(1)
	m.mu.Lock()
	m.deniedCodes = append(m.deniedCodes, code)
	m.mu.Unlock()
}
func (m *metricsSpy) ObserveStorageUnavailable()                  {}
func (m *metricsSpy) ObserveActivityTouch(float64)                {}
func (m *metricsSpy) ObserveRecorderClientTurnFailed(bool)        {}
func (m *metricsSpy) ObserveRecorderStreamEventFailed(bool, bool) {}

// TestSecureSessionE2E_WireFirstTurn_ResumeCanonicalAndWire implements Task 9.4 E2E:
//  1. Wire first turn creates a secure session, builds SessionResponseCarrier,
//     and emits exact current session/resume headers via sessionwire.WriteSessionResponseCarrier.
//  2. Next canonical request resumes successfully using the emitted headers (same session ID and A-leg ID).
//  3. Next wire request resumes successfully using the emitted headers (same session ID and A-leg ID).
//  4. Denials on invalid resume tokens produce mapped HTTP status without leaking secrets.
//  5. Assert resume token is absent from all logs, metrics, debug output, and HTTP bodies (Requirements 14, 18, 22).
func TestSecureSessionE2E_WireFirstTurn_ResumeCanonicalAndWire(t *testing.T) {
	t.Parallel()

	loggerSpy := &logCaptureSpy{}
	logger := slog.New(loggerSpy)
	metrics := &metricsSpy{}

	capture := new(sync.Map)
	opts := testkit.SecureSessionStubExecutorOptions{
		Now: func() time.Time { return time.Unix(6000, 0).UTC() },
	}
	ex := testkit.NewStubExecutorWithSecureSession(t, opts, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), capture)
	ex.SecureSessionMetrics = metrics
	ex.Log = logger

	// Build a wire HTTP handler that runs the wire lifecycle:
	// BuildSessionInput -> PrepareSecureSession -> ExecuteBeginTurn -> ResponseCarrier -> WriteSessionResponseCarrier
	wireHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var parsed struct {
			Model    string            `json:"model"`
			Input    string            `json:"input"`
			Metadata map[string]string `json:"metadata"`
		}
		if len(bodyBytes) > 0 {
			_ = json.Unmarshal(bodyBytes, &parsed)
		}

		sessInput, err := sessionwire.BuildSessionInput(r.Header, parsed.Metadata, sessionwire.SessionInputOptions{
			MaxFactBytes: aipapi.MaxAuthoritativeSessionIDBytes + aipapi.MaxResumeTokenBytes + 512,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		prep, err := ex.PrepareSecureSession(r.Context(), runtime.SecureSessionPrepInput{
			TraceID: "wire-turn-" + r.Header.Get("X-Request-ID"),
			Session: sessInput,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		br, err := prep.ExecuteBeginTurn(prep.Context())
		if err != nil {
			http.Error(w, "session denied: "+err.Error(), http.StatusBadRequest)
			return
		}

		carrier := prep.ResponseCarrier(br)

		// Emit exact current session/resume headers
		sessionwire.WriteSessionResponseCarrier(w, carrier)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "ok",
			"session": carrier.AuthoritativeSessionID,
			"is_new":  br.IsNew,
			"aleg_id": carrier.ALegID,
		})
	})

	canonicalHandler := &openairesponses.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}

	principal := execview.PrincipalView{ID: "e2e-wire-principal"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", withPrincipal(canonicalHandler, principal))
	mux.Handle("/wire/responses", withPrincipal(wireHandler, principal))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// ------------------------------------------------------------------------
	// TURN 1: Wire First Turn (Creates New Session)
	// ------------------------------------------------------------------------
	req1Body := `{"model":"gpt-4o-mini","input":"turn 1 from wire"}`
	req1, err := http.NewRequest(http.MethodPost, srv.URL+"/wire/responses", strings.NewReader(req1Body))
	if err != nil {
		t.Fatal(err)
	}
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Request-ID", "req-1")

	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp1.Body.Close() }()

	if resp1.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp1.Body)
		t.Fatalf("turn 1 wire status %d body=%s", resp1.StatusCode, b)
	}

	sid1 := strings.TrimSpace(resp1.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	aLeg1 := strings.TrimSpace(resp1.Header.Get(sessionwire.HeaderALegID))
	tok1 := strings.TrimSpace(resp1.Header.Get(sessionwire.HeaderResumeToken))

	if sid1 == "" {
		t.Fatal("turn 1 wire missing authoritative session ID header")
	}
	if aLeg1 == "" {
		t.Fatal("turn 1 wire missing A-leg ID header")
	}
	if tok1 == "" {
		t.Fatal("turn 1 wire missing new-session resume token header")
	}

	resp1Bytes, _ := io.ReadAll(resp1.Body)
	if strings.Contains(string(resp1Bytes), tok1) {
		t.Fatal("turn 1 wire body leaked resume token")
	}
	if metrics.newCalls.Load() != 1 {
		t.Fatalf("expected 1 new session metric call, got %d", metrics.newCalls.Load())
	}

	// ------------------------------------------------------------------------
	// TURN 2: Next Canonical Request Resumes Successfully
	// ------------------------------------------------------------------------
	req2Body := `{"model":"gpt-4o-mini","input":"turn 2 canonical resume"}`
	req2, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(req2Body))
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set(sessionwire.HeaderAuthoritativeSessionID, sid1)
	req2.Header.Set(sessionwire.HeaderResumeToken, tok1)

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp2.Body)
		t.Fatalf("turn 2 canonical resume status %d body=%s", resp2.StatusCode, b)
	}

	sid2 := strings.TrimSpace(resp2.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	aLeg2 := strings.TrimSpace(resp2.Header.Get(sessionwire.HeaderALegID))
	tok2 := strings.TrimSpace(resp2.Header.Get(sessionwire.HeaderResumeToken))

	if sid2 != sid1 {
		t.Fatalf("turn 2 canonical session ID mismatch: want %q got %q", sid1, sid2)
	}
	if aLeg2 != aLeg1 {
		t.Fatalf("turn 2 canonical A-leg mismatch: want %q got %q", aLeg1, aLeg2)
	}
	if tok2 != "" {
		t.Fatalf("turn 2 canonical resumed turn must not issue new resume token, got %q", tok2)
	}

	// Verify backend attempt received the resumed turn facts without the resume token
	c2 := testkit.MustAIPCall(t, mustLoad(capture, "last"))
	if c2.Session.ALegID != aLeg1 {
		t.Fatalf("backend attempt A-leg mismatch: want %q got %q", aLeg1, c2.Session.ALegID)
	}
	if strings.TrimSpace(c2.Session.ResumeToken) != "" {
		t.Fatalf("backend attempt must NEVER receive resume token, got %q", c2.Session.ResumeToken)
	}

	resp2Bytes, _ := io.ReadAll(resp2.Body)
	if strings.Contains(string(resp2Bytes), tok1) {
		t.Fatal("turn 2 canonical body leaked resume token")
	}
	if metrics.resumeCalls.Load() != 1 {
		t.Fatalf("expected 1 resume metric call, got %d", metrics.resumeCalls.Load())
	}

	// ------------------------------------------------------------------------
	// TURN 3: Next Wire Request Resumes Successfully
	// ------------------------------------------------------------------------
	req3Body := `{"model":"gpt-4o-mini","input":"turn 3 wire resume"}`
	req3, err := http.NewRequest(http.MethodPost, srv.URL+"/wire/responses", strings.NewReader(req3Body))
	if err != nil {
		t.Fatal(err)
	}
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("X-Request-ID", "req-3")
	req3.Header.Set(sessionwire.HeaderAuthoritativeSessionID, sid1)
	req3.Header.Set(sessionwire.HeaderResumeToken, tok1)

	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp3.Body.Close() }()

	if resp3.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp3.Body)
		t.Fatalf("turn 3 wire resume status %d body=%s", resp3.StatusCode, b)
	}

	sid3 := strings.TrimSpace(resp3.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	aLeg3 := strings.TrimSpace(resp3.Header.Get(sessionwire.HeaderALegID))
	tok3 := strings.TrimSpace(resp3.Header.Get(sessionwire.HeaderResumeToken))

	if sid3 != sid1 {
		t.Fatalf("turn 3 wire session ID mismatch: want %q got %q", sid1, sid3)
	}
	if aLeg3 != aLeg1 {
		t.Fatalf("turn 3 wire A-leg mismatch: want %q got %q", aLeg1, aLeg3)
	}
	if tok3 != "" {
		t.Fatalf("turn 3 wire resumed turn must not emit resume token, got %q", tok3)
	}
	if metrics.resumeCalls.Load() != 2 {
		t.Fatalf("expected 2 resume metric calls, got %d", metrics.resumeCalls.Load())
	}

	// ------------------------------------------------------------------------
	// TURN 4: Denial on Invalid Resume Token
	// ------------------------------------------------------------------------
	const bogusToken = "bogus-invalid-resume-token-999"
	req4Body := `{"model":"gpt-4o-mini","input":"turn 4 invalid token"}`
	req4, err := http.NewRequest(http.MethodPost, srv.URL+"/wire/responses", strings.NewReader(req4Body))
	if err != nil {
		t.Fatal(err)
	}
	req4.Header.Set("Content-Type", "application/json")
	req4.Header.Set("X-Request-ID", "req-4")
	req4.Header.Set(sessionwire.HeaderAuthoritativeSessionID, sid1)
	req4.Header.Set(sessionwire.HeaderResumeToken, bogusToken)

	resp4, err := http.DefaultClient.Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp4.Body.Close() }()

	if resp4.StatusCode != http.StatusBadRequest {
		t.Fatalf("turn 4 expected 400 Bad Request, got %d", resp4.StatusCode)
	}
	resp4Bytes, _ := io.ReadAll(resp4.Body)
	if strings.Contains(string(resp4Bytes), bogusToken) {
		t.Fatal("turn 4 denial leaked bogus token in response body")
	}
	if strings.Contains(string(resp4Bytes), tok1) {
		t.Fatal("turn 4 denial leaked valid token in response body")
	}

	// ------------------------------------------------------------------------
	// TELEMETRY REDACTION ASSERTIONS (Requirements 14.7, 22.3)
	// Assert token absent from logs, metrics, and debug output.
	// ------------------------------------------------------------------------
	if loggerSpy.Contains(tok1) {
		t.Fatalf("CRITICAL SECURITY DEFECT: raw resume token %q leaked into structured logs!", tok1)
	}
	if loggerSpy.Contains(bogusToken) {
		t.Fatalf("CRITICAL SECURITY DEFECT: bogus resume token %q leaked into structured logs!", bogusToken)
	}

	// Verify metrics codes do not contain the token
	metrics.mu.Lock()
	for _, code := range metrics.deniedCodes {
		if strings.Contains(code, tok1) || strings.Contains(code, bogusToken) {
			t.Fatalf("metrics denial code leaked token: %q", code)
		}
	}
	metrics.mu.Unlock()
}

// TestSecureSessionE2E_CanonicalFirstTurn_ResumeWire proves cross-flow parity:
// Canonical first turn creates the session; wire next turn resumes successfully.
func TestSecureSessionE2E_CanonicalFirstTurn_ResumeWire(t *testing.T) {
	t.Parallel()

	capture := new(sync.Map)
	opts := testkit.SecureSessionStubExecutorOptions{
		Now: func() time.Time { return time.Unix(7000, 0).UTC() },
	}
	ex := testkit.NewStubExecutorWithSecureSession(t, opts, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), capture)

	wireHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessInput, err := sessionwire.BuildSessionInput(r.Header, nil, sessionwire.SessionInputOptions{
			MaxFactBytes: 1024,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		prep, err := ex.PrepareSecureSession(r.Context(), runtime.SecureSessionPrepInput{
			TraceID: "wire-resume-" + r.Header.Get("X-Request-ID"),
			Session: sessInput,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		br, err := prep.ExecuteBeginTurn(prep.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		carrier := prep.ResponseCarrier(br)
		sessionwire.WriteSessionResponseCarrier(w, carrier)
		w.WriteHeader(http.StatusOK)
	})

	canonicalHandler := &openairesponses.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}

	principal := execview.PrincipalView{ID: "e2e-cross-principal"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", withPrincipal(canonicalHandler, principal))
	mux.Handle("/wire/responses", withPrincipal(wireHandler, principal))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Turn 1: Canonical creates session
	req1, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-4o-mini","input":"canonical turn 1"}`))
	if err != nil {
		t.Fatal(err)
	}
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp1.Body.Close() }()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp1.StatusCode)
	}

	sid := strings.TrimSpace(resp1.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	aLeg := strings.TrimSpace(resp1.Header.Get(sessionwire.HeaderALegID))
	tok := strings.TrimSpace(resp1.Header.Get(sessionwire.HeaderResumeToken))
	if sid == "" || aLeg == "" || tok == "" {
		t.Fatalf("missing carriers from canonical: sid=%q aleg=%q tok_len=%d", sid, aLeg, len(tok))
	}

	// Turn 2: Wire resumes session
	req2, err := http.NewRequest(http.MethodPost, srv.URL+"/wire/responses", strings.NewReader(`{"model":"gpt-4o-mini","input":"wire resume turn 2"}`))
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set(sessionwire.HeaderAuthoritativeSessionID, sid)
	req2.Header.Set(sessionwire.HeaderResumeToken, tok)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp2.Body)
		t.Fatalf("wire resume status %d body=%s", resp2.StatusCode, b)
	}

	if gotSID := resp2.Header.Get(sessionwire.HeaderAuthoritativeSessionID); gotSID != sid {
		t.Fatalf("session ID mismatch: want %q got %q", sid, gotSID)
	}
	if gotALeg := resp2.Header.Get(sessionwire.HeaderALegID); gotALeg != aLeg {
		t.Fatalf("A-leg ID mismatch: want %q got %q", aLeg, gotALeg)
	}
	if gotTok := resp2.Header.Get(sessionwire.HeaderResumeToken); gotTok != "" {
		t.Fatalf("resumed turn must not issue resume token: got %q", gotTok)
	}
}
