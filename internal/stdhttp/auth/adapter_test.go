package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

type stubCoreAuthenticator struct {
	dec       auth.Decision
	err       error
	lastMeta  *auth.InboundCallMeta
	callCount int
}

func (s *stubCoreAuthenticator) Authenticate(ctx context.Context, req auth.InboundCallMeta) (auth.Decision, error) {
	_ = ctx
	s.callCount++
	s.lastMeta = &req
	if s.err != nil {
		return auth.Decision{}, s.err
	}
	return s.dec, nil
}

type captureSink struct {
	events     []auth.AuthDecisionEvent
	fail       error
	authCallNo int
}

func (c *captureSink) OnAuthDecision(_ context.Context, ev auth.AuthDecisionEvent) error {
	c.authCallNo++
	c.events = append(c.events, ev)
	return c.fail
}
func (c *captureSink) OnSessionStart(context.Context, auth.SessionStartEvent) error { return nil }

// testOSID, orderSink, and remoteStub are shared by package auth tests (adapter, frontend bundle)
// and integration-tagged tests in integration_test.go.
type testOSID struct {
	snap coreauth.OSIdentitySnapshot
	err  error
}

func (o testOSID) Current(ctx context.Context) (coreauth.OSIdentitySnapshot, error) {
	_ = ctx
	return o.snap, o.err
}

type orderSink struct {
	steps  []string
	events int
}

func (o *orderSink) OnAuthDecision(context.Context, auth.AuthDecisionEvent) error {
	o.steps = append(o.steps, "event")
	o.events++
	return nil
}
func (o *orderSink) OnSessionStart(context.Context, auth.SessionStartEvent) error { return nil }

type remoteStub struct {
	decision auth.Decision
	err      error
}

func (r remoteStub) Decide(ctx context.Context, req auth.InboundCallMeta) (auth.Decision, error) {
	_ = ctx
	_ = req
	return r.decision, r.err
}

func TestPolicyProvider_allow_propagatesPrincipal(t *testing.T) {
	t.Parallel()
	stub := &stubCoreAuthenticator{dec: auth.Decision{Outcome: auth.OutcomeAllow, Principal: execview.PrincipalView{ID: "u1"}}}
	sink := &captureSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	pol := PolicySnapshot{AccessMode: auth.AccessSingleUser, HandlerKind: auth.HandlerLocalNoop, RequiredLevel: auth.LevelNone}
	p := NewPolicyProvider(stub, disp, pol, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat", nil)
	var innerCtx context.Context
	h := Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		innerCtx = r.Context()
		_, _ = rec.WriteString("ok")
	}))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	pg, ok := httpauth.PrincipalFromContext(innerCtx)
	if !ok || pg.ID != "u1" {
		t.Fatalf("principal %+v ok=%v", pg, ok)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events: %d", len(sink.events))
	}
	if sink.events[0].Outcome != auth.OutcomeAllow {
		t.Fatalf("outcome: %q", sink.events[0].Outcome)
	}
}

func TestPolicyProvider_deny_doesNotReachInner(t *testing.T) {
	t.Parallel()
	stub := &stubCoreAuthenticator{dec: auth.Decision{Outcome: auth.OutcomeDeny, ReasonCode: "missing_api_key"}}
	sink := &captureSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	pol := PolicySnapshot{AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey}
	p := NewPolicyProvider(stub, disp, pol, nil)
	var sawInner bool
	rec := httptest.NewRecorder()
	Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { sawInner = true })).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if sawInner {
		t.Fatal("inner ran")
	}
	if rec.Code != http.StatusUnauthorized {
		// local_api_key + missing/invalid key maps to 401 via default terminal status
		t.Fatalf("code %d want 401 (Unauthorized) for missing_api_key deny", rec.Code)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events: %d", len(sink.events))
	}
}

func TestPolicyProvider_Middleware_recordsBearerInMeta(t *testing.T) {
	t.Parallel()
	stub := &stubCoreAuthenticator{dec: auth.Decision{Outcome: auth.OutcomeDeny, ReasonCode: "invalid_api_key"}}
	p := NewPolicyProvider(stub, nil, PolicySnapshot{AccessMode: auth.AccessSingleUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey}, nil)
	r := httptest.NewRequest(http.MethodGet, "/v1/foo", nil)
	r.Header.Set("Authorization", "Bearer secret_value_not_logged_here")
	_, _ = p.Authenticate(r.Context(), httptest.NewRecorder(), r)
	if stub.lastMeta == nil {
		t.Fatal("no meta")
	}
	if stub.lastMeta.AuthorizationBearer != "secret_value_not_logged_here" {
		t.Fatalf("bearer: %q", stub.lastMeta.AuthorizationBearer)
	}
}

func TestPolicyProvider_eventFailClosed_returns503(t *testing.T) {
	t.Parallel()
	stub := &stubCoreAuthenticator{dec: auth.Decision{Outcome: auth.OutcomeAllow, Principal: execview.PrincipalView{ID: "u1"}}}
	sink := &captureSink{fail: context.DeadlineExceeded}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureFailClosed)
	p := NewPolicyProvider(stub, disp, PolicySnapshot{AccessMode: auth.AccessSingleUser, HandlerKind: auth.HandlerLocalNoop, RequiredLevel: auth.LevelNone}, nil)
	var sawInner bool
	rec := httptest.NewRecorder()
	Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { sawInner = true })).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, "/v1/", nil),
	)
	if sawInner {
		t.Fatal("inner ran")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code %d want 503", rec.Code)
	}
}

func TestPolicyProvider_rendererByFrontend(t *testing.T) {
	t.Parallel()
	stub := &stubCoreAuthenticator{dec: auth.Decision{Outcome: auth.OutcomeDeny, ReasonCode: "remote_denied"}}
	disp := coreauth.NewEventDispatcher(&captureSink{}, coreauth.EventFailureBestEffort)
	rend := &stubRenderer{tail: "custom-gemini"}
	p := &PolicyProvider{
		Auth:   stub,
		Events: disp,
		Policy: PolicySnapshot{AccessMode: auth.AccessSingleUser, HandlerKind: auth.HandlerRemote, RequiredLevel: auth.LevelNone},
		RendererByFrontend: map[string]httpauth.AuthErrorRenderer{
			"gemini": rend,
		},
		Renderer: DefaultAuthErrorRenderer{},
	}
	rec := httptest.NewRecorder()
	Middleware(nil, []httpauth.Provider{p}, http.NotFoundHandler()).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, "/v1beta/models", nil),
	)
	if !bytesContains(rec.Body.Bytes(), []byte("custom-gemini")) {
		t.Fatalf("body %q", rec.Body.String())
	}
	// Unregistered frontend: default JSON body, not the stub tail
	p.RendererByFrontend = nil
	rec2 := httptest.NewRecorder()
	Middleware(nil, []httpauth.Provider{p}, http.NotFoundHandler()).ServeHTTP(
		rec2, httptest.NewRequest(http.MethodGet, "/v1beta/models", nil),
	)
	if bytesContains(rec2.Body.Bytes(), []byte("custom-gemini")) {
		t.Fatalf("expected default body, got %q", rec2.Body.String())
	}
}

type stubRenderer struct{ tail string }

func (s *stubRenderer) RenderAuthError(_ context.Context, in httpauth.AuthErrorRenderInput) httpauth.AuthErrorRenderResult {
	return httpauth.AuthErrorRenderResult{Status: http.StatusForbidden, Body: []byte(`{"error":"` + s.tail + `"}`), ContentType: "application/json; charset=utf-8"}
}

func bytesContains(b, sub []byte) bool {
	if len(sub) == 0 {
		return true
	}
	if len(b) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == string(sub) {
			return true
		}
	}
	return false
}

func TestDefaultFrontendIDFromRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want string
	}{
		{"/v1beta/x", "gemini"},
		{"/v1beta1/x", "gemini"},
		{"/v1/messages", "anthropic"},
		{"/v1/chat/completions", "openai_compatible"},
		{"/v1/responses", "openai_compatible"},
		{"/v1/models", "openai_compatible"},
		{"/anthropic/custom", "anthropic"},
		{"/nope", ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if got := DefaultFrontendIDFromRequest(r); got != tc.want {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestInboundMeta_traceID(t *testing.T) {
	t.Parallel()
	ctx := diag.WithTraceID(context.Background(), "tid-1")
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/x", nil)
	m := inboundMetaFromRequest(r, "fe")
	if m.TraceID != "tid-1" {
		t.Fatalf("trace: %q", m.TraceID)
	}
}

func TestInboundMetaFromRequest_authorizationBearer_bearerSchemeOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{
			name:   "bearer",
			header: "Bearer secret_value_not_logged_here",
			want:   "secret_value_not_logged_here",
		},
		{
			name:   "bearer_lower",
			header: "bearer token-16chars-min_",
			want:   "token-16chars-min_",
		},
		{
			name:   "basic",
			header: "Basic dGVzdA==",
			want:   "",
		},
		{
			name:   "digest",
			header: "Digest username=\"x\", realm=\"y\"",
			want:   "",
		},
		{
			name:   "bearer_empty_token",
			header: "Bearer   ",
			want:   "",
		},
		{
			name:   "no_scheme",
			header: "not-a-scheme token",
			want:   "",
		},
		{
			name:   "empty",
			header: "",
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			m := inboundMetaFromRequest(r, "fe")
			if m.AuthorizationBearer != tc.want {
				t.Fatalf("AuthorizationBearer: got %q want %q", m.AuthorizationBearer, tc.want)
			}
		})
	}
}

func TestInboundMetaFromRequest_xAPIKey(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("x-api-key", "ant-key")
	m := inboundMetaFromRequest(r, "anthropic")
	if m.AuthorizationBearer != "ant-key" {
		t.Fatalf("AuthorizationBearer: got %q", m.AuthorizationBearer)
	}
}

func TestPolicyProvider_authenticatorError_returns500(t *testing.T) {
	t.Parallel()
	stub := &stubCoreAuthenticator{err: fmt.Errorf("db connection refused")}
	p := NewPolicyProvider(stub, nil, PolicySnapshot{AccessMode: auth.AccessSingleUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	res, err := p.Authenticate(req.Context(), rec, req)
	if err == nil {
		t.Fatal("want non-nil error when authenticator fails")
	}
	if res.Type != httpauth.TypeContinue {
		t.Fatalf("result type should be zero/continue on error path, got %q", res.Type)
	}
}

func freshSyntheticBearer(t *testing.T) string {
	t.Helper()
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return "sk-test-" + hex.EncodeToString(b[:])
}

// echoBearerErrAuthenticator returns an error that echoes the raw bearer it received.
type echoBearerErrAuthenticator struct {
	wrapWith error // optional canonical sentinel to wrap (Canceled / DeadlineExceeded)
	lastErr  error
}

func (e *echoBearerErrAuthenticator) Authenticate(_ context.Context, req auth.InboundCallMeta) (auth.Decision, error) {
	msg := fmt.Sprintf("malicious authenticator saw bearer=%s", req.AuthorizationBearer)
	var err error
	if e.wrapWith != nil {
		err = fmt.Errorf("%s: %w", msg, e.wrapWith)
	} else {
		err = errors.New(msg)
	}
	e.lastErr = err
	return auth.Decision{}, err
}

func TestPolicyProvider_Authenticate_sanitizesAuthenticatorErrorContainingBearer(t *testing.T) {
	t.Parallel()
	bearer := freshSyntheticBearer(t)
	stub := &echoBearerErrAuthenticator{}
	p := NewPolicyProvider(stub, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)

	_, err := p.Authenticate(req.Context(), httptest.NewRecorder(), req)
	if err == nil {
		t.Fatal("want non-nil error when authenticator fails")
	}
	if stub.lastErr == nil {
		t.Fatal("authenticator did not record an error")
	}
	if !strings.Contains(stub.lastErr.Error(), bearer) {
		t.Fatalf("test setup: authenticator error must echo bearer; got %q", stub.lastErr.Error())
	}
	if strings.Contains(err.Error(), bearer) {
		t.Fatalf("PolicyProvider error must not contain raw bearer; got %q", err.Error())
	}
	if strings.Contains(err.Error(), "malicious authenticator") {
		t.Fatalf("PolicyProvider error must not retain malicious text; got %q", err.Error())
	}
	if errors.Is(err, stub.lastErr) {
		t.Fatal("returned error must not retain the malicious authenticator error in the Unwrap chain")
	}
}

func TestPolicyProvider_Authenticate_sanitizesWrappedCanceledAndDeadline(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		wrap   error
		wantIs error
	}{
		{name: "canceled", wrap: context.Canceled, wantIs: context.Canceled},
		{name: "deadline", wrap: context.DeadlineExceeded, wantIs: context.DeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bearer := freshSyntheticBearer(t)
			stub := &echoBearerErrAuthenticator{wrapWith: tc.wrap}
			p := NewPolicyProvider(stub, nil, PolicySnapshot{
				AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
			}, nil)
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer "+bearer)

			_, err := p.Authenticate(req.Context(), httptest.NewRecorder(), req)
			if err == nil {
				t.Fatal("want non-nil error")
			}
			if !errors.Is(err, tc.wantIs) {
				t.Fatalf("want errors.Is(..., %v); got %v", tc.wantIs, err)
			}
			if strings.Contains(err.Error(), bearer) {
				t.Fatalf("returned error must not contain bearer; got %q", err.Error())
			}
			if strings.Contains(err.Error(), "malicious authenticator") {
				t.Fatalf("returned error must not retain malicious text; got %q", err.Error())
			}
			if errors.Is(err, stub.lastErr) {
				t.Fatal("returned error must not retain the malicious wrapped authenticator error")
			}
			if u := errors.Unwrap(err); u != nil && u != tc.wantIs {
				t.Fatalf("unexpected unwrap target %v (%T)", u, u)
			}
		})
	}
}

func TestPolicyProvider_Middleware_authenticatorError_omitsBearerFromLogsAndBody(t *testing.T) {
	t.Parallel()
	bearer := freshSyntheticBearer(t)
	stub := &echoBearerErrAuthenticator{}
	p := NewPolicyProvider(stub, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)

	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelError}))
	var sawInner bool
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	Middleware(log, []httpauth.Provider{p}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		sawInner = true
	})).ServeHTTP(rec, req)

	if sawInner {
		t.Fatal("inner handler must not run on authenticator error")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("HTTP status: got %d want 500", rec.Code)
	}
	body := rec.Body.String()
	logged := logBuf.String()
	for _, needle := range []string{bearer, "malicious authenticator"} {
		if strings.Contains(body, needle) {
			t.Fatalf("response body must not contain %q; body=%q", needle, body)
		}
		if strings.Contains(logged, needle) {
			t.Fatalf("auth middleware logs must not contain %q; log=%q", needle, logged)
		}
	}
	if !strings.Contains(body, "authentication failed") {
		t.Fatalf("expected stable failure body, got %q", body)
	}
}

func TestPolicyProvider_unknownOutcome_deniesViaReject(t *testing.T) {
	t.Parallel()
	stub := &stubCoreAuthenticator{dec: auth.Decision{
		Outcome:    auth.DecisionOutcome("future_outcome"),
		ReasonCode: "",
	}}
	sink := &captureSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	p := NewPolicyProvider(stub, disp, PolicySnapshot{AccessMode: auth.AccessSingleUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	res, err := p.Authenticate(req.Context(), rec, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != httpauth.TypeReject {
		t.Fatalf("want TypeReject, got %q", res.Type)
	}
	if res.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", res.HTTPStatus)
	}
	if res.ContentType != "application/json; charset=utf-8" {
		t.Fatalf("ContentType: %q", res.ContentType)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sink.events))
	}
}

func TestAuthDecisionEventMapping(t *testing.T) {
	t.Parallel()
	now := time.Unix(1, 0).UTC()
	d := auth.Decision{
		Outcome:    auth.OutcomeAllow,
		ReasonCode: "",
		Principal:  execview.PrincipalView{ID: "a", DisplayName: "A", Roles: []string{"r1"}, Claims: map[string]string{"k": "v"}},
		Device:     auth.DeviceIdentity{ID: "d1", KeyID: "k1", Fingerprint: "fp1"},
	}
	pol := PolicySnapshot{HandlerKind: auth.HandlerLocalNoop, RequiredLevel: auth.LevelNone, AccessMode: auth.AccessSingleUser}
	ev := authDecisionEvent(now, "t1", pol, auth.InboundCallMeta{Frontend: "fe1"}, d, nil)
	if ev.PrincipalID != "a" || ev.DeviceID != "d1" {
		t.Fatalf("ev: %+v", ev)
	}
	if ev.PrincipalSafeClaims == nil || ev.PrincipalSafeClaims["k"] != "" {
		t.Fatalf("PrincipalSafeClaims must list keys with empty values, got %#v", ev.PrincipalSafeClaims)
	}
}
