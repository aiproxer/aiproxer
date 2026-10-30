package openresponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/reqbody"
	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// Task 1.2 characterization: freeze OpenResponses outer order and shared pipe
// order (requirements 1, 2, 17; design 1, 2, 14).
// Outer oracle (handler.go): method -> auth -> JSON media-type -> frontendpipe.
// Shared oracle inside the pipe: body read -> header selector -> shared
// preflight -> TryAdmit -> guarded Decode -> AfterDecode (prepareCreateState)/
// traffic -> execute. A future candidate lane must preserve outer auth/media
// precedence and must not reorder method/path/auth/content-type errors.

type orderingOpenResponsesExec struct {
	calls    int
	lastCall *aipapi.Call
}

func (m *orderingOpenResponsesExec) Execute(_ context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	m.calls++
	m.lastCall = call
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

type orderingOpenResponsesObserver struct {
	event traffic.Observation
	calls int
}

func (o *orderingOpenResponsesObserver) OnObservation(_ context.Context, ev traffic.Observation) error {
	o.calls++
	o.event = ev
	return nil
}

func openResponsesAuthedHandler(exec *orderingOpenResponsesExec, extra func(*openresponses.HandlerConfig)) *openresponses.Handler {
	cfg := openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           &mockAuthorizer{authenticated: true},
		Executor:             exec,
	}
	if extra != nil {
		extra(&cfg)
	}
	return openresponses.NewHandler(cfg)
}

func decodeOpenResponsesWireError(t *testing.T, body []byte) (typ, code, message string) {
	t.Helper()
	var payload proto.WireErrorEnvelope
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode wire error: %v body=%s", err, body)
	}
	return payload.Error.Type, payload.Error.Code, payload.Error.Message
}

func TestOpenResponses_OuterMethodAuthMediaPrecedence(t *testing.T) {
	t.Parallel()

	t.Run("method before auth and media", func(t *testing.T) {
		t.Parallel()
		auth := &mockAuthorizer{authenticated: false}
		exec := &orderingOpenResponsesExec{}
		h := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Authorizer:           auth,
			Executor:             exec,
		})
		req := httptest.NewRequest(http.MethodGet, "/openresponses/v1/responses", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d want 405 (outer method owns non-POST even without auth/media)", rec.Code)
		}
		if auth.calls != 0 {
			t.Fatalf("auth calls=%d want 0 (method precedes auth)", auth.calls)
		}
		if exec.calls != 0 {
			t.Fatal("executor ran after method reject")
		}
	})

	t.Run("auth before media", func(t *testing.T) {
		t.Parallel()
		auth := &mockAuthorizer{authenticated: false}
		exec := &orderingOpenResponsesExec{}
		h := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Authorizer:           auth,
			Executor:             exec,
		})
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{"model":"gpt-4o","input":"hi"}`))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d want 401 (outer auth owns denied request even with bad media type)", rec.Code)
		}
		typ, code, _ := decodeOpenResponsesWireError(t, rec.Body.Bytes())
		if typ != "authentication_error" || code != "unauthorized" {
			t.Fatalf("type=%q code=%q want authentication_error/unauthorized", typ, code)
		}
		if exec.calls != 0 {
			t.Fatal("executor ran after auth reject")
		}
	})

	t.Run("auth before body read", func(t *testing.T) {
		t.Parallel()
		auth := &mockAuthorizer{authenticated: false}
		exec := &orderingOpenResponsesExec{}
		h := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Authorizer:           auth,
			Executor:             exec,
		})
		body := []byte(`{invalid json`)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Body = &countingReadCloser{reader: bytes.NewReader(body)}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d want 401 (auth precedes body validation)", rec.Code)
		}
		if exec.calls != 0 {
			t.Fatal("executor ran after auth reject")
		}
		if rc, ok := req.Body.(*countingReadCloser); ok && rc.reads != 0 {
			t.Fatalf("body reads=%d want 0 (auth precedes body read)", rc.reads)
		}
	})

	t.Run("media before pipe body and admission", func(t *testing.T) {
		t.Parallel()
		limiter := decodeqos.New(1, math.MaxInt64)
		release, ok, err := limiter.TryAcquire(t.Context(), 0)
		if err != nil || !ok {
			t.Fatalf("pre-acquire: ok=%v err=%v", ok, err)
		}
		defer release()
		exec := &orderingOpenResponsesExec{}
		h := openResponsesAuthedHandler(exec, func(cfg *openresponses.HandlerConfig) {
			cfg.DecodeAdmission = limiter
		})
		body := bytes.Repeat([]byte("a"), int(reqbody.DefaultMaxBytes)+1)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("status=%d want 415 (outer media owns bad content-type even for oversized body with saturated admission)", rec.Code)
		}
		typ, code, msg := decodeOpenResponsesWireError(t, rec.Body.Bytes())
		if typ != "invalid_request_error" || code != "unsupported_media_type" {
			t.Fatalf("type=%q code=%q want invalid_request_error/unsupported_media_type", typ, code)
		}
		if msg != "Request Content-Type must be application/json" {
			t.Fatalf("message=%q", msg)
		}
		if exec.calls != 0 {
			t.Fatal("executor ran after media reject")
		}
	})

	t.Run("media error shape frozen", func(t *testing.T) {
		t.Parallel()
		exec := &orderingOpenResponsesExec{}
		h := openResponsesAuthedHandler(exec, nil)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{"model":"gpt-4o","input":"hi"}`))
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		// Sanity: charset-suffixed JSON media type is accepted; this guards the
		// exact mime.ParseMediaType outer check from drifting to strict equality.
		if rec.Code == http.StatusUnsupportedMediaType {
			t.Fatalf("charset JSON wrongly rejected: %s", rec.Body.String())
		}
	})
}

func TestOpenResponses_SharedPipeBodyAdmissionDecodePrecedence(t *testing.T) {
	t.Parallel()

	t.Run("path before body", func(t *testing.T) {
		t.Parallel()
		exec := &orderingOpenResponsesExec{}
		h := openResponsesAuthedHandler(exec, nil)
		body := bytes.Repeat([]byte("a"), int(reqbody.DefaultMaxBytes)+1)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/unknown", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d want 404 (path owns unknown route even for oversized body)", rec.Code)
		}
		if exec.calls != 0 {
			t.Fatal("executor ran after path reject")
		}
	})

	t.Run("body before admission", func(t *testing.T) {
		t.Parallel()
		limiter := decodeqos.New(1, math.MaxInt64)
		release, ok, err := limiter.TryAcquire(t.Context(), 0)
		if err != nil || !ok {
			t.Fatalf("pre-acquire: ok=%v err=%v", ok, err)
		}
		defer release()
		exec := &orderingOpenResponsesExec{}
		h := openResponsesAuthedHandler(exec, func(cfg *openresponses.HandlerConfig) {
			cfg.DecodeAdmission = limiter
		})
		body := bytes.Repeat([]byte("a"), int(reqbody.DefaultMaxBytes)+1)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d want 413 (body limit owns oversized body even when admission saturated)", rec.Code)
		}
		typ, code, msg := decodeOpenResponsesWireError(t, rec.Body.Bytes())
		if typ != "invalid_request_error" || code != "body_too_large" || msg != "Request body exceeds max limit" {
			t.Fatalf("type=%q code=%q msg=%q", typ, code, msg)
		}
		if exec.calls != 0 {
			t.Fatal("executor ran for oversized body")
		}
	})

	t.Run("preflight before admission", func(t *testing.T) {
		t.Parallel()
		limiter := decodeqos.New(1, math.MaxInt64)
		release, ok, err := limiter.TryAcquire(t.Context(), 0)
		if err != nil || !ok {
			t.Fatalf("pre-acquire: ok=%v err=%v", ok, err)
		}
		defer release()
		exec := &orderingOpenResponsesExec{}
		h := openResponsesAuthedHandler(exec, func(cfg *openresponses.HandlerConfig) {
			cfg.DecodeAdmission = limiter
		})
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400 (shared preflight owns malformed JSON even when admission saturated)", rec.Code)
		}
		typ, code, _ := decodeOpenResponsesWireError(t, rec.Body.Bytes())
		if typ != "invalid_request_error" || code != "bad_request" {
			t.Fatalf("type=%q code=%q want invalid_request_error/bad_request", typ, code)
		}
		if exec.calls != 0 {
			t.Fatal("executor ran after preflight failure")
		}
	})

	t.Run("admission before decode", func(t *testing.T) {
		t.Parallel()
		limiter := decodeqos.New(1, math.MaxInt64)
		release, ok, err := limiter.TryAcquire(t.Context(), 0)
		if err != nil || !ok {
			t.Fatalf("pre-acquire: ok=%v err=%v", ok, err)
		}
		defer release()
		exec := &orderingOpenResponsesExec{}
		h := openResponsesAuthedHandler(exec, func(cfg *openresponses.HandlerConfig) {
			cfg.DecodeAdmission = limiter
		})
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{"model":"gpt-4o","input":"hi"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusTooManyRequests && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d want 429/503 admission reject", rec.Code)
		}
		if rec.Code == http.StatusTooManyRequests {
			if got := rec.Header().Get("Retry-After"); got != decodeqos.RetryAfterSeconds {
				t.Fatalf("Retry-After=%q want %q", got, decodeqos.RetryAfterSeconds)
			}
		}
		if exec.calls != 0 {
			t.Fatal("executor ran after admission reject")
		}
	})

	t.Run("decode failure before execute", func(t *testing.T) {
		t.Parallel()
		exec := &orderingOpenResponsesExec{}
		h := openResponsesAuthedHandler(exec, nil)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400", rec.Code)
		}
		if exec.calls != 0 {
			t.Fatal("executor ran after decode failure")
		}
	})
}

func TestOpenResponses_HeaderSelectorPrecedence(t *testing.T) {
	t.Parallel()
	newHandler := func(exec *orderingOpenResponsesExec) *openresponses.Handler {
		return openResponsesAuthedHandler(exec, func(cfg *openresponses.HandlerConfig) {
			cfg.DefaultRouteSelector = "stub:default"
			cfg.RoutePrefixes = []string{"stub"}
		})
	}

	t.Run("header wins over body model", func(t *testing.T) {
		t.Parallel()
		exec := &orderingOpenResponsesExec{}
		h := newHandler(exec)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{"model":"stub:from-body","input":"hi"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-AIP-Route", "stub:from-header")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK && rec.Code != http.StatusBadGateway {
			// Status depends on executor stream collection; selector assertion
			// below is authoritative for routing precedence.
			t.Logf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if exec.calls == 0 {
			t.Fatalf("executor not reached: status=%d body=%s", rec.Code, rec.Body.String())
		}
		if exec.lastCall.Route.Selector != "stub:from-header" {
			t.Fatalf("selector=%q want header value", exec.lastCall.Route.Selector)
		}
	})

	t.Run("body model used when header absent", func(t *testing.T) {
		t.Parallel()
		exec := &orderingOpenResponsesExec{}
		h := newHandler(exec)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{"model":"stub:from-body","input":"hi"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if exec.calls == 0 {
			t.Fatalf("executor not reached: status=%d body=%s", rec.Code, rec.Body.String())
		}
		if exec.lastCall.Route.Selector != "stub:from-body" {
			t.Fatalf("selector=%q want body model", exec.lastCall.Route.Selector)
		}
	})
}

func TestOpenResponses_TrafficCarriesOriginalBody(t *testing.T) {
	t.Parallel()
	exec := &orderingOpenResponsesExec{}
	obs := &orderingOpenResponsesObserver{}
	h := openResponsesAuthedHandler(exec, func(cfg *openresponses.HandlerConfig) {
		cfg.TrafficPorts = traffic.PortBundle{Obs: obs}
	})
	body := []byte(`{"model":"gpt-4o","input":"hi"}`)
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if exec.calls == 0 {
		t.Fatalf("executor not reached: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if obs.calls == 0 {
		t.Fatal("traffic observer not called (post-decode traffic must precede execute)")
	}
	if obs.event.Leg != traffic.LegCTP {
		t.Fatalf("traffic leg=%q want client_to_proxy", obs.event.Leg)
	}
	if !bytes.Equal(obs.event.Body, body) {
		t.Fatalf("traffic body=%q want original %q", obs.event.Body, body)
	}
}

// Task 7.2 characterization: prove OpenResponses outer auth/media check remains
// where it is, and candidate gates evaluate strictly after outer checks
// (Method -> Auth -> Media -> MatchPath).
func TestOpenResponses_CandidateGatesOrderingPrecedence(t *testing.T) {
	t.Parallel()

	t.Run("outer method precedes auth media and candidate gates", func(t *testing.T) {
		t.Parallel()
		auth := &mockAuthorizer{authenticated: false}
		exec := &orderingOpenResponsesExec{}
		h := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Authorizer:           auth,
			Executor:             exec,
		})
		req := httptest.NewRequest(http.MethodGet, "/openresponses/v1/responses", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d want 405 Method Not Allowed", rec.Code)
		}
		if auth.calls != 0 {
			t.Fatalf("auth calls=%d want 0 (method precedes auth)", auth.calls)
		}
		if exec.calls != 0 {
			t.Fatalf("exec calls=%d want 0 (method precedes execution)", exec.calls)
		}
	})

	t.Run("outer auth precedes media and candidate gates", func(t *testing.T) {
		t.Parallel()
		auth := &mockAuthorizer{authenticated: false}
		exec := &orderingOpenResponsesExec{}
		h := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Authorizer:           auth,
			Executor:             exec,
		})
		// Even with valid Content-Type and large body candidate payload, unauthenticated request must yield 401.
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{"model":"gpt-4o","input":"test"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d want 401 Unauthorized", rec.Code)
		}
		typ, code, _ := decodeOpenResponsesWireError(t, rec.Body.Bytes())
		if typ != "authentication_error" || code != "unauthorized" {
			t.Fatalf("type=%q code=%q want authentication_error/unauthorized", typ, code)
		}
		if exec.calls != 0 {
			t.Fatalf("exec calls=%d want 0 (auth precedes candidate/execution)", exec.calls)
		}
	})

	t.Run("outer media precedes path and candidate gates", func(t *testing.T) {
		t.Parallel()
		exec := &orderingOpenResponsesExec{}
		h := openResponsesAuthedHandler(exec, nil)
		// Non-JSON Content-Type must yield 415 Unsupported Media Type before path or candidate logic.
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{"model":"gpt-4o","input":"test"}`))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("status=%d want 415 Unsupported Media Type", rec.Code)
		}
		typ, code, msg := decodeOpenResponsesWireError(t, rec.Body.Bytes())
		if typ != "invalid_request_error" || code != "unsupported_media_type" {
			t.Fatalf("type=%q code=%q want invalid_request_error/unsupported_media_type", typ, code)
		}
		if msg != "Request Content-Type must be application/json" {
			t.Fatalf("message=%q", msg)
		}
		if exec.calls != 0 {
			t.Fatalf("exec calls=%d want 0 (media precedes candidate/execution)", exec.calls)
		}
	})

	t.Run("path check precedes candidate gates", func(t *testing.T) {
		t.Parallel()
		exec := &orderingOpenResponsesExec{}
		h := openResponsesAuthedHandler(exec, nil)
		// Unknown path with auth + valid media must yield 404 before candidate logic.
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/unknown_route", strings.NewReader(`{"model":"gpt-4o","input":"test"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d want 404 Not Found", rec.Code)
		}
		if exec.calls != 0 {
			t.Fatalf("exec calls=%d want 0 (path precedes candidate/execution)", exec.calls)
		}
	})
}
