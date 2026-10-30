package openailegacy_test

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/reqbody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// Task 1.2 characterization: freeze OpenAI Chat outer + shared pipe order
// (requirements 1, 2, 17; design 1, 2, 14, 16).
// Shared oracle: body read -> metadata-only pre-admission resolver/header selector
// (absent here) -> shared preflight -> TryAdmit -> guarded RouteFromBodyModel/
// Decode -> post-decode/traffic -> execute. RouteFromBodyModel=true with
// header-wins precedence, independently fixed from the Responses lane.

type orderingRouteExec struct {
	called   bool
	selector string
}

func (e *orderingRouteExec) Execute(_ context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	e.called = true
	if call != nil {
		e.selector = call.Route.Selector
	}
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (e *orderingRouteExec) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

func (e *orderingRouteExec) WallClock() func() time.Time { return nil }

func TestOpenAILegacy_OrderingMethodPathBodyAdmissionPreflightPrecedence(t *testing.T) {
	t.Parallel()

	t.Run("method before path", func(t *testing.T) {
		t.Parallel()
		h := &openailegacy.Handler{Exec: &orderingRouteExec{}, DefaultRouteSelector: "stub:gpt-4o-mini"}
		req := httptest.NewRequest(http.MethodGet, "/v1/other", strings.NewReader(`{"model":"x"}`))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d want 405", rr.Code)
		}
	})

	t.Run("path before body", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini"}
		body := bytes.Repeat([]byte("a"), int(reqbody.DefaultMaxBytes)+1)
		req := httptest.NewRequest(http.MethodPost, "/v1/other", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status=%d want 404 (unknown path owns request even for oversized body)", rr.Code)
		}
		if exec.called {
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
		exec := &orderingRouteExec{}
		h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeAdmission: limiter}
		body := bytes.Repeat([]byte("a"), int(reqbody.DefaultMaxBytes)+1)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d want 413 (body limit owns oversized body even when admission saturated)", rr.Code)
		}
		if exec.called {
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
		exec := &orderingRouteExec{}
		h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeAdmission: limiter}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{`))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400 (shared preflight owns malformed JSON even when admission saturated)", rr.Code)
		}
		msg, _ := decodeOpenAIAPIError(t, rr.Body.Bytes())
		if msg != "invalid request JSON" {
			t.Fatalf("message=%q want invalid request JSON", msg)
		}
		if exec.called {
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
		exec := &orderingRouteExec{}
		h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeAdmission: limiter}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(readGolden(t, "create_text_nonstream.json")))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("status=%d want 429", rr.Code)
		}
		if got := rr.Header().Get("Retry-After"); got != decodeqos.RetryAfterSeconds {
			t.Fatalf("Retry-After=%q want %q", got, decodeqos.RetryAfterSeconds)
		}
		if exec.called {
			t.Fatal("executor ran after admission reject")
		}
	})

	t.Run("decode failure before execute", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini"}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400", rr.Code)
		}
		if exec.called {
			t.Fatal("executor ran after decode failure")
		}
	})
}

func TestOpenAILegacy_HeaderSelectorPrecedenceAndBodyDefault(t *testing.T) {
	t.Parallel()
	chatBody := func(model string) string {
		return `{"model":` + strconvQuote(model) + `,"messages":[{"role":"user","content":"hi"}]}`
	}
	newHandler := func(exec *orderingRouteExec) *openailegacy.Handler {
		return &openailegacy.Handler{
			Exec:                 exec,
			DefaultRouteSelector: "stub:default",
			RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
		}
	}

	t.Run("header wins over body model", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := newHandler(exec)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody("stub:from-body")))
		req.Header.Set(openailegacy.HeaderRouteSelector, "stub:from-header")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if exec.selector != "stub:from-header" {
			t.Fatalf("selector=%q want header value", exec.selector)
		}
	})

	t.Run("body model used when header absent", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := newHandler(exec)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody("stub:from-body")))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if exec.selector != "stub:from-body" {
			t.Fatalf("selector=%q want body model", exec.selector)
		}
	})

	t.Run("unknown model falls back to default", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := newHandler(exec)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(readGolden(t, "create_text_nonstream.json")))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		// Golden model gpt-4o-mini carries no stub prefix, so RouteFromBodyModel
		// must fall back to the configured default rather than an empty selector.
		if exec.selector != "stub:default" {
			t.Fatalf("selector=%q want default", exec.selector)
		}
	})

	t.Run("no legacy whole-body resolver bypass: header still wins", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := newHandler(exec)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"stub:from-body","messages":[{"role":"user","content":"hi"}],"x_route_hint":"stub:evil"}`))
		req.Header.Set(openailegacy.HeaderRouteSelector, "stub:from-header")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if exec.selector != "stub:from-header" {
			t.Fatalf("selector=%q want header (opaque body hint must not bypass precedence)", exec.selector)
		}
	})
}

func TestOpenAILegacy_TrafficCarriesOriginalBodyBeforeExecute(t *testing.T) {
	t.Parallel()
	exec := &orderingRouteExec{}
	body := readGolden(t, "create_text_nonstream.json")
	obs := &captureObserver{}
	h := &openailegacy.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:gpt-4o-mini",
		TrafficPorts:         traffic.PortBundle{Obs: obs},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !exec.called {
		t.Fatal("executor not called")
	}
	if obs.event.Leg != traffic.LegCTP {
		t.Fatalf("traffic leg=%q want client_to_proxy", obs.event.Leg)
	}
	if !bytes.Equal(obs.event.Body, body) {
		t.Fatalf("traffic body=%q want original %q", obs.event.Body, body)
	}
}

// Task 7.2 characterization: prove OpenAI Legacy outer ordering (Method -> MatchPath)
// is preserved before candidate logic, and no universal auth or content-type sequence is forced.
func TestOpenAILegacy_CandidateGatesOrderingPrecedence(t *testing.T) {
	t.Parallel()

	t.Run("no forced universal auth or media check", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := &openailegacy.Handler{
			Exec:                 exec,
			DefaultRouteSelector: "stub:default",
		}
		// Request without Authorization header and with text/plain Content-Type must NOT
		// be rejected by outer 401 or 415. OpenAI Legacy does not enforce outer auth
		// or outer JSON media check.
		body := readGolden(t, "create_text_nonstream.json")
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "text/plain") // non-application/json
		// no Authorization header
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusUnsupportedMediaType {
			t.Fatalf("status=%d must not be 401 or 415 (no forced outer auth/content-type sequence)", rr.Code)
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d want 200 OK (body: %s)", rr.Code, rr.Body.String())
		}
		if !exec.called {
			t.Fatal("executor was not called")
		}
	})

	t.Run("method precedes candidate gates", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := &openailegacy.Handler{
			Exec:                 exec,
			DefaultRouteSelector: "stub:default",
		}
		req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", strings.NewReader(`{}`))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d want 405 Method Not Allowed", rr.Code)
		}
		if exec.called {
			t.Fatal("executor ran after method reject")
		}
	})

	t.Run("path precedes candidate gates", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := &openailegacy.Handler{
			Exec:                 exec,
			DefaultRouteSelector: "stub:default",
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/unknown", strings.NewReader(`{}`))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status=%d want 404 Not Found", rr.Code)
		}
		if exec.called {
			t.Fatal("executor ran after path reject")
		}
	})
}

func strconvQuote(s string) string {
	return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
}
