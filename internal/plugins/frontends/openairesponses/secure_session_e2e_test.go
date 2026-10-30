package openairesponses_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func jsonBody(model string, input string, stream bool, meta map[string]string) string {
	m := map[string]any{
		"model": model, "input": input, "stream": stream,
	}
	if len(meta) > 0 {
		m["metadata"] = meta
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func withPrincipal(h http.Handler, p execview.PrincipalView) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := execview.WithPrincipal(r.Context(), p)
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withPrincipalFromHeader sets execview principal from X-Test-Principal (E2E only).
func withPrincipalFromHeader(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pid := strings.TrimSpace(r.Header.Get("X-Test-Principal"))
		if pid == "" {
			http.Error(w, "missing X-Test-Principal", http.StatusInternalServerError)
			return
		}
		ctx := execview.WithPrincipal(r.Context(), execview.PrincipalView{ID: pid})
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

type mutableWorkspaceResolver struct {
	mu sync.Mutex
	id string
}

func (m *mutableWorkspaceResolver) setID(id string) {
	m.mu.Lock()
	m.id = id
	m.mu.Unlock()
}

func (m *mutableWorkspaceResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	m.mu.Lock()
	id := m.id
	m.mu.Unlock()
	return aipworkspace.WorkspaceView{ID: id}, nil
}

func newSecureSessionE2EServer(t *testing.T, principalID string, opts testkit.SecureSessionStubExecutorOptions) (*httptest.Server, *sync.Map) {
	t.Helper()
	capture := new(sync.Map)
	ex := testkit.NewStubExecutorWithSecureSession(t, opts, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), capture)
	h := &openairesponses.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", withPrincipal(h, execview.PrincipalView{ID: principalID}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, capture
}

func mustLoad(m *sync.Map, key string) any {
	v, ok := m.Load(key)
	if !ok {
		panic("missing capture key " + key)
	}
	return v
}

func openCount(m *sync.Map) int {
	v, ok := m.Load("openCount")
	if !ok {
		return 0
	}
	n, _ := v.(int)
	return n
}

// --- 11.1 ---

func TestSecureSessionE2E_openairesponses_createResume_sameALeg_noTokenOnBackend(t *testing.T) {
	t.Parallel()
	srv, capture := newSecureSessionE2EServer(t, "owner-11-1", testkit.SecureSessionStubExecutorOptions{
		Now: func() time.Time { return time.Unix(5000, 0).UTC() },
	})

	req1, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "first", false, nil)))
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
		b, _ := io.ReadAll(resp1.Body)
		t.Fatalf("first status %d body=%s", resp1.StatusCode, b)
	}
	sid := strings.TrimSpace(resp1.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	tok := strings.TrimSpace(resp1.Header.Get(sessionwire.HeaderResumeToken))
	if sid == "" || tok == "" {
		t.Fatalf("missing session carriers: sid=%q tok_len=%d", sid, len(tok))
	}
	_, _ = io.Copy(io.Discard, resp1.Body)

	c1 := testkit.MustAIPCall(t, mustLoad(capture, "last"))
	if c1.Session.ALegID == "" {
		t.Fatalf("expected backend a-leg on first open")
	}
	if strings.TrimSpace(c1.Session.ResumeToken) != "" {
		t.Fatalf("backend must not see resume token, got %q", c1.Session.ResumeToken)
	}
	aleg1 := c1.Session.ALegID

	meta := map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: sid,
		sessionwire.MetaKeyResumeToken:            tok,
	}
	req2, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "second", false, meta)))
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp2.Body)
		t.Fatalf("second status %d body=%s", resp2.StatusCode, b)
	}
	_, _ = io.Copy(io.Discard, resp2.Body)

	c2 := testkit.MustAIPCall(t, mustLoad(capture, "last"))
	if c2.Session.ALegID != aleg1 {
		t.Fatalf("resume must reuse A-leg: want %q got %q", aleg1, c2.Session.ALegID)
	}
	if strings.TrimSpace(c2.Session.ResumeToken) != "" {
		t.Fatalf("backend attempt 2 must not include resume token")
	}
}

// --- 11.2 ---

func TestSecureSessionE2E_denials_missingPrincipal(t *testing.T) {
	t.Parallel()
	capture := new(sync.Map)
	ex := testkit.NewStubExecutorWithSecureSession(t, testkit.SecureSessionStubExecutorOptions{
		Now: func() time.Time { return time.Unix(6000, 0).UTC() },
	}, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), capture)
	h := &openairesponses.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	capture.Store("openCount", 0)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "x", false, nil)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
	if openCount(capture) != 0 {
		t.Fatal("backend must not open without principal")
	}
}

func TestSecureSessionE2E_denials_wrongOwner(t *testing.T) {
	t.Parallel()
	capture := new(sync.Map)
	ex := testkit.NewStubExecutorWithSecureSession(t, testkit.SecureSessionStubExecutorOptions{
		Now: func() time.Time { return time.Unix(7000, 0).UTC() },
	}, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), capture)
	h := &openairesponses.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", withPrincipalFromHeader(h))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req0, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "seed", false, nil)))
	req0.Header.Set("Content-Type", "application/json")
	req0.Header.Set("X-Test-Principal", "owner-a")
	resp0, err := http.DefaultClient.Do(req0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp0.Body.Close() }()
	sid := strings.TrimSpace(resp0.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	tok := strings.TrimSpace(resp0.Header.Get(sessionwire.HeaderResumeToken))
	_, _ = io.ReadAll(resp0.Body)
	if sid == "" || tok == "" {
		t.Fatalf("seed sid=%q tok=%q", sid, tok)
	}
	capture.Store("openCount", 0)
	meta := map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: sid,
		sessionwire.MetaKeyResumeToken:            tok,
	}
	req1, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "hack", false, meta)))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Test-Principal", "owner-b")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp1.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp1.Body)
	if resp1.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", resp1.StatusCode)
	}
	if openCount(capture) != 0 {
		t.Fatal("backend must not open on owner mismatch")
	}
}

func TestSecureSessionE2E_denials_malformedResumeToken(t *testing.T) {
	t.Parallel()
	srv, capture := newSecureSessionE2EServer(t, "owner-mal", testkit.SecureSessionStubExecutorOptions{
		Now: func() time.Time { return time.Unix(7100, 0).UTC() },
	})
	req0, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "seed", false, nil)))
	req0.Header.Set("Content-Type", "application/json")
	resp0, err := http.DefaultClient.Do(req0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp0.Body.Close() }()
	sid := strings.TrimSpace(resp0.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	_, _ = io.ReadAll(resp0.Body)
	capture.Store("openCount", 0)
	meta := map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: sid,
		sessionwire.MetaKeyResumeToken:            "not-valid-token-material",
	}
	req1, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "x", false, meta)))
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp1.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp1.Body)
	if resp1.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", resp1.StatusCode)
	}
	if openCount(capture) != 0 {
		t.Fatal("backend must not open")
	}
}

func TestSecureSessionE2E_denials_sessionIdHintMismatch(t *testing.T) {
	t.Parallel()
	srv, capture := newSecureSessionE2EServer(t, "owner-hint", testkit.SecureSessionStubExecutorOptions{
		Now: func() time.Time { return time.Unix(7200, 0).UTC() },
	})
	req0, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "seed", false, nil)))
	req0.Header.Set("Content-Type", "application/json")
	resp0, err := http.DefaultClient.Do(req0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp0.Body.Close() }()
	tok := strings.TrimSpace(resp0.Header.Get(sessionwire.HeaderResumeToken))
	_, _ = io.ReadAll(resp0.Body)
	capture.Store("openCount", 0)
	meta := map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: "wrong-sid-hint",
		sessionwire.MetaKeyResumeToken:            tok,
	}
	req1, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "x", false, meta)))
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp1.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp1.Body)
	if resp1.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", resp1.StatusCode)
	}
	if openCount(capture) != 0 {
		t.Fatal("backend must not open")
	}
}

func TestSecureSessionE2E_denials_expiredResumeWindow(t *testing.T) {
	t.Parallel()
	clock := time.Unix(7300, 0).UTC()
	srv, capture := newSecureSessionE2EServer(t, "owner-exp", testkit.SecureSessionStubExecutorOptions{
		Now: func() time.Time { return clock },
		ManagerConfig: app.ManagerConfig{
			ResumeWindow: 10 * time.Minute,
		},
	})
	req0, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "seed", false, nil)))
	req0.Header.Set("Content-Type", "application/json")
	resp0, err := http.DefaultClient.Do(req0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp0.Body.Close() }()
	sid := strings.TrimSpace(resp0.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	tok := strings.TrimSpace(resp0.Header.Get(sessionwire.HeaderResumeToken))
	_, _ = io.ReadAll(resp0.Body)
	clock = clock.Add(11 * time.Minute)
	capture.Store("openCount", 0)
	meta := map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: sid,
		sessionwire.MetaKeyResumeToken:            tok,
	}
	req1, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "late", false, meta)))
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp1.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp1.Body)
	if resp1.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", resp1.StatusCode)
	}
	if openCount(capture) != 0 {
		t.Fatal("backend must not open after resume window")
	}
}

func TestSecureSessionE2E_denials_workspaceMismatch(t *testing.T) {
	t.Parallel()
	ws := &mutableWorkspaceResolver{id: "ws-a"}
	capture := new(sync.Map)
	ex := testkit.NewStubExecutorWithSecureSession(t, testkit.SecureSessionStubExecutorOptions{
		Now:       func() time.Time { return time.Unix(7400, 0).UTC() },
		Workspace: ws,
	}, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), capture)
	h := &openairesponses.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", withPrincipal(h, execview.PrincipalView{ID: "ws-owner"}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req0, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "seed", false, nil)))
	req0.Header.Set("Content-Type", "application/json")
	resp0, err := http.DefaultClient.Do(req0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp0.Body.Close() }()
	sid := strings.TrimSpace(resp0.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	tok := strings.TrimSpace(resp0.Header.Get(sessionwire.HeaderResumeToken))
	_, _ = io.ReadAll(resp0.Body)
	ws.setID("ws-b")
	capture.Store("openCount", 0)
	meta := map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: sid,
		sessionwire.MetaKeyResumeToken:            tok,
	}
	req1, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "resume", false, meta)))
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp1.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp1.Body)
	if resp1.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", resp1.StatusCode)
	}
	if openCount(capture) != 0 {
		t.Fatal("backend must not open on workspace mismatch")
	}
}

func TestSecureSessionE2E_denials_policyReadiness(t *testing.T) {
	t.Parallel()
	fk := testkit.SecureSessionTestFingerprintKey()
	mem := memory.New(memory.Options{SimulateDurable: true})
	fake := &testkit.FakeSecureSessionStore{Delegate: mem, CheckReadinessErr: domain.ErrPolicyUnavailable}
	capture := new(sync.Map)
	ex := testkit.NewStubExecutorWithSecureSession(t, testkit.SecureSessionStubExecutorOptions{
		Now:            func() time.Time { return time.Unix(7600, 0).UTC() },
		SecureStore:    fake,
		FingerprintKey: fk,
	}, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), capture)
	h := &openairesponses.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	srv := httptest.NewServer(withPrincipal(h, execview.PrincipalView{ID: "pol-user"}))
	t.Cleanup(srv.Close)
	capture.Store("openCount", 0)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "p", false, nil)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != http.StatusInternalServerError {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 503 or 500 got %d body=%s", resp.StatusCode, b)
	}
	if openCount(capture) != 0 {
		t.Fatal("backend must not open when readiness fails")
	}
}

// --- 11.4 ---

func TestSecureSessionE2E_streaming_preOutputFailover_HTTP(t *testing.T) {
	t.Parallel()
	capture := new(sync.Map)
	ex := testkit.NewStubExecutorWithSecureSession(t, testkit.SecureSessionStubExecutorOptions{
		Now:      func() time.Time { return time.Unix(11000, 0).UTC() },
		RandSeed: 2,
		Backends: map[string]execbackend.Backend{
			"bad": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
				},
			},
			"ok": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					return aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "stream-ok"},
						{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 5},
						{Kind: aipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), capture)
	h := &openairesponses.Handler{Exec: ex, DefaultRouteSelector: "stub:x"}
	srv := httptest.NewServer(withPrincipal(h, execview.PrincipalView{ID: "stream-user"}))
	t.Cleanup(srv.Close)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "hi", true, nil)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(openairesponses.HeaderRouteSelector, "bad:g1|ok:g2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if !strings.Contains(s, "stream-ok") {
		t.Fatalf("expected failover stream body, got len=%d", len(body))
	}
	if !strings.Contains(s, "input_tokens") && !strings.Contains(s, "output_tokens") {
		t.Fatalf("expected usage fields in SSE body, got snippet=%q", truncate(s, 400))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

type failClientTurnRecorder struct{}

func (failClientTurnRecorder) RecordClientTurnAfterGate(context.Context, app.ClientTurnRecordInput) error {
	return errors.New("client turn record failed")
}

func (failClientTurnRecorder) RecordPostHookStreamEvent(context.Context, app.StreamEventRecordInput) error {
	return nil
}

func TestSecureSessionE2E_mandatoryPreOutputRecorder_HTTP(t *testing.T) {
	t.Parallel()
	capture := new(sync.Map)
	ex := testkit.NewStubExecutorWithSecureSession(t, testkit.SecureSessionStubExecutorOptions{
		Now:                             func() time.Time { return time.Unix(12000, 0).UTC() },
		SecureSessionRecorder:           failClientTurnRecorder{},
		SecureSessionRecordingMandatory: true,
	}, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), capture)
	h := &openairesponses.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	srv := httptest.NewServer(withPrincipal(h, execview.PrincipalView{ID: "mand-pre"}))
	t.Cleanup(srv.Close)
	capture.Store("openCount", 0)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "x", false, nil)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != http.StatusInternalServerError {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 503 or 500 got %d body=%s", resp.StatusCode, b)
	}
	if openCount(capture) != 0 {
		t.Fatal("no backend open")
	}
}

type failPostOutputStreamRecorder struct {
	sawText bool
}

func (f *failPostOutputStreamRecorder) RecordClientTurnAfterGate(context.Context, app.ClientTurnRecordInput) error {
	return nil
}

func (f *failPostOutputStreamRecorder) RecordPostHookStreamEvent(_ context.Context, in app.StreamEventRecordInput) error {
	if in.EventKind == string(aipapi.EventTextDelta) {
		f.sawText = true
	}
	if f.sawText && in.EventKind == string(aipapi.EventResponseFinished) {
		return errors.New("recorder failed after output")
	}
	return nil
}

func TestSecureSessionE2E_mandatoryPostOutputRecorder_stream_HTTP(t *testing.T) {
	t.Parallel()
	rec := &failPostOutputStreamRecorder{}
	capture := new(sync.Map)
	ex := testkit.NewStubExecutorWithSecureSession(t, testkit.SecureSessionStubExecutorOptions{
		Now:                             func() time.Time { return time.Unix(13000, 0).UTC() },
		SecureSessionRecorder:           rec,
		SecureSessionRecordingMandatory: true,
	}, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), capture)
	h := &openairesponses.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	srv := httptest.NewServer(withPrincipal(h, execview.PrincipalView{ID: "mand-post"}))
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "x", true, nil)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) == 0 {
		t.Fatal("empty body")
	}
	if !rec.sawText {
		t.Fatal("expected text delta through recorder")
	}
	// Mandatory post-output: stream should not complete with a normal terminal completed envelope.
	if strings.Contains(string(body), `"type":"response.completed"`) && !strings.Contains(string(body), "error") {
		t.Fatal("expected stream to fail or surface error after mandatory recorder failure")
	}
}

func TestSecureSessionE2E_transcript_nonStream_recordsOrdered(t *testing.T) {
	t.Parallel()
	mem := memory.New(memory.Options{SimulateDurable: true})
	rec, err := app.NewRecorder(mem)
	if err != nil {
		t.Fatal(err)
	}
	capture := new(sync.Map)
	ex := testkit.NewStubExecutorWithSecureSession(t, testkit.SecureSessionStubExecutorOptions{
		Now:                   func() time.Time { return time.Unix(14000, 0).UTC() },
		SecureStore:           mem,
		SecureSessionRecorder: rec,
	}, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), capture)
	h := &openairesponses.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	srv := httptest.NewServer(withPrincipal(h, execview.PrincipalView{ID: "tr-owner"}))
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBody("gpt-4o-mini", "hello transcript", false, nil)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d body=%s", resp.StatusCode, b)
	}
	sid := strings.TrimSpace(resp.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	_, _ = io.ReadAll(resp.Body)
	if sid == "" {
		t.Fatal("missing session id")
	}
	items, err := mem.Transcript(t.Context(), domain.SessionID(sid), domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) < 2 {
		t.Fatalf("expected transcript rows, got %d", len(items))
	}
	for i := 1; i < len(items); i++ {
		if items[i].Seq <= items[i-1].Seq {
			t.Fatalf("transcript seq not monotonic: %+v then %+v", items[i-1], items[i])
		}
	}
}
