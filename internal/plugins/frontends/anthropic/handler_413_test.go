package anthropic_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/reqbody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

func TestHandler_requestBodyTooLarge_returns413(t *testing.T) {
	t.Parallel()
	exec := &recordingExecutor{}
	h := &anthropic.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:claude",
	}
	body := bytes.Repeat([]byte("c"), int(reqbody.DefaultMaxBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if exec.called {
		t.Fatal("executor was called for oversized body")
	}
}

func TestHandler_JSONGuardRejectsDeepBodyBeforeExecutor(t *testing.T) {
	t.Parallel()
	const marker = "MALICIOUS_MARKER"
	exec := &recordingExecutor{}
	h := &anthropic.Handler{Exec: exec, DefaultRouteSelector: "stub:claude"}
	body := strings.Repeat("[", 129) + `"` + marker + `"` + strings.Repeat("]", 129)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if exec.called {
		t.Fatal("executor was called for deeply nested JSON")
	}
	if strings.Contains(rr.Body.String(), marker) {
		t.Fatalf("response echoed malicious marker: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid request JSON") {
		t.Fatalf("response body = %s, want invalid request JSON", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "could not read request body") {
		t.Fatalf("response body used read-error message: %s", rr.Body.String())
	}
}

func TestHandler_JSONGuardDepthExactAcceptsAndRejectsPlusOne(t *testing.T) {
	t.Parallel()
	nest := func(depth int, leaf string) string {
		return strings.Repeat("[", depth) + leaf + strings.Repeat("]", depth)
	}

	t.Run("exact_128", func(t *testing.T) {
		t.Parallel()
		exec := &recordingExecutor{}
		h := &anthropic.Handler{Exec: exec, DefaultRouteSelector: "stub:claude-3-5-haiku-20241022"}
		body := `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"ping"}],"_nest":` + nest(127, "1") + `}`
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
		}
		if !exec.called {
			t.Fatal("executor was not called for exact depth 128")
		}
	})

	t.Run("depth_129", func(t *testing.T) {
		t.Parallel()
		const marker = "NEST_MARKER"
		exec := &recordingExecutor{}
		h := &anthropic.Handler{Exec: exec, DefaultRouteSelector: "stub:claude-3-5-haiku-20241022"}
		body := `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"ping"}],"_nest":` + nest(128, `"`+marker+`"`) + `}`
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
		}
		if exec.called {
			t.Fatal("executor was called for depth 129")
		}
		if strings.Contains(rr.Body.String(), marker) {
			t.Fatalf("response echoed marker: %s", rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "invalid request JSON") {
			t.Fatalf("body=%s", rr.Body.String())
		}
	})
}

func TestHandler_JSONGuardRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()
	exec := &recordingExecutor{}
	h := &anthropic.Handler{Exec: exec, DefaultRouteSelector: "stub:claude"}
	body := []byte(`{"model":"claude","max_tokens":1,"messages":[{"role":"user","content":"`)
	body = append(body, 0xff)
	body = append(body, `"}]}`...)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if exec.called {
		t.Fatal("executor was called for invalid UTF-8")
	}
	if bytes.Contains(rr.Body.Bytes(), []byte{0xff}) {
		t.Fatal("response echoed invalid UTF-8 byte")
	}
}

func TestHandler_JSONGuardAcceptsDuplicateMembers(t *testing.T) {
	t.Parallel()
	exec := &recordingExecutor{}
	h := &anthropic.Handler{Exec: exec, DefaultRouteSelector: "stub:claude-3-5-haiku-20241022"}
	body := `{"model":"bad","model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"ping"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if !exec.called {
		t.Fatal("executor was not called for duplicate-member envelope")
	}
}

func TestHandler_invalidJSONDoesNotEchoBodyMarker(t *testing.T) {
	t.Parallel()
	const marker = "MALICIOUS_MARKER"
	exec := &recordingExecutor{}
	h := &anthropic.Handler{Exec: exec, DefaultRouteSelector: "stub:claude"}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"`+marker+`"}]`))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if exec.called {
		t.Fatal("executor was called for invalid JSON")
	}
	if strings.Contains(rr.Body.String(), marker) {
		t.Fatalf("response echoed malicious marker: %s", rr.Body.String())
	}
}

func TestHandler_validMinimalRequestReachesExecutorAndEmitsOriginalBody(t *testing.T) {
	t.Parallel()
	exec := &recordingExecutor{}
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"ping"}]
}`)
	obs := &captureObserver{}
	h := &anthropic.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:claude-3-5-haiku-20241022",
		TrafficPorts:         traffic.PortBundle{Obs: obs},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if !exec.called {
		t.Fatal("executor was not called for valid request")
	}
	if obs.event.Leg != traffic.LegCTP {
		t.Fatalf("traffic leg: %q", obs.event.Leg)
	}
	if !bytes.Equal(obs.event.Body, body) {
		t.Fatalf("traffic body = %q, want original %q", obs.event.Body, body)
	}
}

type recordingExecutor struct{ called bool }

func (e *recordingExecutor) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	e.called = true
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (e *recordingExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

func (e *recordingExecutor) WallClock() func() time.Time { return nil }

type captureObserver struct{ event traffic.Observation }

func (o *captureObserver) OnObservation(_ context.Context, event traffic.Observation) error {
	o.event = event
	return nil
}
