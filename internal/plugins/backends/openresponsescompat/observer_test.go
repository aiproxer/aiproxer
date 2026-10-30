package openresponsescompat

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

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// requestObserver is the independent httptest observer: it records raw requests
// with no production codec involvement.
type requestObserver struct {
	mu       sync.Mutex
	requests []observedRequest
}

type observedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

func (o *requestObserver) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.requests)
}

func (o *requestObserver) last(t *testing.T) observedRequest {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.requests) == 0 {
		t.Fatal("observer captured no request")
	}
	r := o.requests[len(o.requests)-1]
	return r
}

func (o *requestObserver) record(method, path string, header http.Header, body []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests = append(o.requests, observedRequest{
		Method: method,
		Path:   path,
		Header: header.Clone(),
		Body:   append([]byte(nil), body...),
	})
}

// newObserverBackend builds a factory-constructed backend whose outbound
// requests are captured by an independent observer.
func newObserverBackend(t *testing.T, cfgExtra string, handler http.HandlerFunc) (execbackend.Backend, *requestObserver) {
	t.Helper()
	obs := &requestObserver{}
	observe := func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		obs.record(r.Method, r.URL.Path, r.Header, body)
		if handler != nil {
			handler(w, r)
			return
		}
		http.Error(w, "no handler", http.StatusInternalServerError)
	}
	srv := httptest.NewServer(http.HandlerFunc(observe))
	t.Cleanup(srv.Close)

	raw := "backend_prefix: my-or\nbase_url: " + srv.URL + "\n" + cfgExtra
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	be, err := Build("or-inst", n, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return be, obs
}

func drainManagedEvents(t *testing.T, es aipapi.ManagedEventStream) []aipapi.Event {
	t.Helper()
	defer func() { _ = es.Close() }()
	var events []aipapi.Event
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			return events
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		events = append(events, ev)
	}
}

func hasTextDelta(events []aipapi.Event, text string) bool {
	for _, ev := range events {
		if ev.Kind == aipapi.EventTextDelta && strings.Contains(ev.Delta, text) {
			return true
		}
	}
	return false
}

func hasEventKind(events []aipapi.Event, kind aipapi.EventKind) bool {
	for _, ev := range events {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

func bodyHasForbiddenField(body []byte, forbidden string) bool {
	return strings.Contains(string(body), forbidden)
}

func TestObserver_ItemAuthorityNonStreamingExactWireShape(t *testing.T) {
	t.Setenv("MY_OR_KEY", "sk-observer-secret")
	be, obs := newObserverBackend(t, "api_key_env_var_root: MY_OR_KEY\n", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeResourceJSON)
	})

	call := itemAuthorityCreateCall()
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatal(err)
	}
	events := drainManagedEvents(t, es)
	if !hasTextDelta(events, "The weather in Paris is sunny.") {
		t.Fatalf("missing text delta in %+v", events)
	}
	if !hasEventKind(events, aipapi.EventUsageDelta) {
		t.Fatalf("missing usage event in %+v", events)
	}

	if obs.count() != 1 {
		t.Fatalf("observer request count = %d, want exactly 1", obs.count())
	}
	req := obs.last(t)
	if req.Method != http.MethodPost {
		t.Fatalf("method = %q, want POST", req.Method)
	}
	if req.Path != "/responses" {
		t.Fatalf("path = %q, want /responses", req.Path)
	}
	if ct := req.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	if auth := req.Header.Get("Authorization"); auth != "Bearer sk-observer-secret" {
		t.Fatalf("authorization = %q", auth)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("body is not valid JSON: %v body=%s", err, string(req.Body))
	}
	if string(payload["model"]) != `"model-x"` {
		t.Fatalf("model = %s", string(payload["model"]))
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(payload["input"], &input); err != nil {
		t.Fatalf("input unmarshal: %v", err)
	}
	wantTypes := []string{"message", "function_call", "function_call_output", "message"}
	if len(input) != len(wantTypes) {
		t.Fatalf("input items = %d, want %d", len(input), len(wantTypes))
	}
	for i, want := range wantTypes {
		if got := string(input[i]["type"]); got != `"`+want+`"` {
			t.Fatalf("input[%d].type = %s, want %q", i, got, want)
		}
	}

	for _, forbidden := range []string{
		"previous_response_id", `"store"`, `"stream"`, `"background"`,
		"proxy_call", "client-session", "auth-session", "resume_secret",
		"openresponses.model", "acme:proprietary",
	} {
		if bodyHasForbiddenField(req.Body, forbidden) {
			t.Fatalf("request forwarded forbidden field %q: %s", forbidden, string(req.Body))
		}
	}
}

func TestObserver_ItemAuthorityNoAuthSendsNoAuthHeader(t *testing.T) {
	be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeResourceJSON)
	})
	es, err := be.Open(context.Background(), itemAuthorityCreateCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = drainManagedEvents(t, es)
	req := obs.last(t)
	if auth := req.Header.Get("Authorization"); auth != "" {
		t.Fatalf("no-auth mode must not send Authorization header, got %q", auth)
	}
}

func TestObserver_ItemAuthorityZeroRoundTripsUnsupported(t *testing.T) {
	cases := []struct {
		name    string
		call    aipapi.Call
		cand    routing.AttemptCandidate
		wantErr error
	}{
		{
			name: "conflicting_authority",
			call: func() aipapi.Call {
				c := itemAuthorityCreateCall()
				c.Messages = []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("y")}}}
				return c
			}(),
			cand:    routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}},
			wantErr: aipapi.ErrInvalidCall,
		},
		{
			// Compaction is now served by the remote compact mapping; this
			// legacy-authority call with no resolvable model must still be
			// rejected before any round trip.
			name:    "compact_operation_missing_model",
			call:    openResponsesCall(aipapi.OperationContextCompaction),
			wantErr: ErrUnrepresentable,
		},
		{
			name:    "unknown_operation",
			call:    openResponsesCall(aipapi.Operation("example.unknown")),
			wantErr: ErrOperationUnsupported,
		},
		{
			name: "video_content",
			call: func() aipapi.Call {
				c := itemAuthorityCreateCall()
				c.Items[0].Content = []aipapi.ContentPart{{Kind: aipapi.ContentPartVideoRef, VideoRef: "https://example.com/v.mp4"}}
				return c
			}(),
			cand:    routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}},
			wantErr: ErrUnrepresentable,
		},
		{
			name: "missing_model",
			call: itemAuthorityCreateCall(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "unexpected request", http.StatusInternalServerError)
			})
			_, err := be.Open(context.Background(), tc.call, tc.cand)
			if err == nil {
				t.Fatal("expected pre-network rejection")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if obs.count() != 0 {
				t.Fatalf("unsupported call caused %d round trips, want 0", obs.count())
			}
		})
	}
}

func TestObserver_ItemAuthorityPathTraversalZeroRoundTrips(t *testing.T) {
	obs := &requestObserver{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		obs.record(r.Method, r.URL.Path, r.Header, body)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	be := NewBackend(BackendSpec{
		ID:             "my-or",
		BaseURL:        srv.URL + "/v1/../x",
		HTTPClient:     srv.Client(),
		RequestLimits:  defaultRequestLimits(),
		ResponseLimits: defaultResponseLimits(),
		Caps:           aipapi.NewBackendCaps(defaultCapabilities...),
		DialectSupport: aipapi.NormalizeDialectSupport(dialectSupportFromConfig(Config{Dialects: defaultDialects()})),
	})
	_, err := be.Open(context.Background(), itemAuthorityCreateCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err == nil {
		t.Fatal("expected path traversal rejection before network work")
	}
	if obs.count() != 0 {
		t.Fatalf("path traversal caused %d round trips, want 0", obs.count())
	}
}

func TestObserver_NonStreamingHTTPStatusClassification(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		wantRecover bool
	}{
		{name: "rate_limit_429", status: http.StatusTooManyRequests, wantRecover: true},
		{name: "server_500", status: http.StatusInternalServerError, wantRecover: true},
		{name: "bad_gateway_502", status: http.StatusBadGateway, wantRecover: true},
		{name: "unauthorized_401", status: http.StatusUnauthorized, wantRecover: false},
		{name: "validation_422", status: http.StatusUnprocessableEntity, wantRecover: false},
		{name: "not_found_404", status: http.StatusNotFound, wantRecover: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "boom", tc.status)
			})
			_, err := be.Open(context.Background(), itemAuthorityCreateCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
			if err == nil {
				t.Fatal("expected error")
			}
			if got := aipapi.IsRecoverablePreOutput(err); got != tc.wantRecover {
				t.Fatalf("recoverable = %v, want %v (err=%v)", got, tc.wantRecover, err)
			}
		})
	}
}

func TestObserver_NonStreamingMalformedContentTypeRejected(t *testing.T) {
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "not json")
	})
	_, err := be.Open(context.Background(), itemAuthorityCreateCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err == nil {
		t.Fatal("expected content-type rejection")
	}
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("error = %v, want ErrMalformedResponse", err)
	}
}

func TestObserver_NonStreamingOversizedBodyRejected(t *testing.T) {
	be, _ := newObserverBackend(t, "response_limits:\n  max_event_bytes: 256\n", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeResourceJSON)
	})
	_, err := be.Open(context.Background(), itemAuthorityCreateCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err == nil {
		t.Fatal("expected oversized body rejection")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v", err)
	}
}

func TestObserver_ItemAuthorityControlZeroRoundTrips(t *testing.T) {
	cases := []struct {
		name string
		call aipapi.Call
	}{
		{
			name: "verbosity",
			call: func() aipapi.Call {
				c := itemAuthorityCreateCall()
				c.Options.Verbosity = aipapi.VerbosityLow
				return c
			}(),
		},
		{
			name: "unsupported_mime",
			call: func() aipapi.Call {
				c := itemAuthorityCreateCall()
				c.Options.ResponseMIMEType = "application/xml"
				return c
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "unexpected request", http.StatusInternalServerError)
			})
			_, err := be.Open(context.Background(), tc.call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
			if err == nil {
				t.Fatal("expected pre-network rejection")
			}
			if !errors.Is(err, ErrUnrepresentable) {
				t.Fatalf("error = %v, want ErrUnrepresentable", err)
			}
			if obs.count() != 0 {
				t.Fatalf("unrepresentable control caused %d round trips, want 0", obs.count())
			}
		})
	}
}

func TestObserver_ItemAuthorityRouteDerivedReasoningEffortForwarded(t *testing.T) {
	be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeResourceJSON)
	})
	call := itemAuthorityCreateCall()
	// The route selector (e.g. model?reasoning_effort=high) is decoded into the
	// canonical option before the backend seam; the backend must forward it.
	call.Options.ReasoningEffort = "high"
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = drainManagedEvents(t, es)

	if obs.count() != 1 {
		t.Fatalf("observer request count = %d, want exactly 1", obs.count())
	}
	req := obs.last(t)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("body is not valid JSON: %v body=%s", err, string(req.Body))
	}
	if got := string(payload["reasoning"]); got != `{"effort":"high"}` {
		t.Fatalf("reasoning = %s, want {\"effort\":\"high\"}", got)
	}
}

func TestObserver_ItemAuthorityStructuredOutputMIMEForwarded(t *testing.T) {
	be, obs := newObserverBackend(t, "capabilities:\n  - streaming\n  - tools\n  - vision\n  - documents\n  - reasoning\n  - structured_outputs\n  - parallel_tool_calls\n  - ordered_items\n  - assistant_phase\n  - item_references\n  - compaction\n  - opaque_extensions\n", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeResourceJSON)
	})
	call := itemAuthorityCreateCall()
	call.Options.ResponseMIMEType = "application/json"
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = drainManagedEvents(t, es)

	req := obs.last(t)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("body is not valid JSON: %v body=%s", err, string(req.Body))
	}
	if got := string(payload["text"]); got != `{"format":{"type":"json_object"}}` {
		t.Fatalf("text = %s, want {\"format\":{\"type\":\"json_object\"}}", got)
	}
}

func TestObserver_NonStreamingWrongObjectClassifiedPreOutput(t *testing.T) {
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"r","object":"response.compaction","status":"completed","model":"m","output":[]}`)
	})
	_, err := be.Open(context.Background(), itemAuthorityCreateCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err == nil {
		t.Fatal("expected wrong-object rejection")
	}
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("error = %v, want ErrMalformedResponse", err)
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("wrong-object mismatch must be classified pre-output for failover: %v", err)
	}
}

func TestObserver_NonStreamingNoEchoOfBodyInError(t *testing.T) {
	secret := "sk-observer-body-secret"
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, "auth failed "+secret, http.StatusUnauthorized)
	})
	_, err := be.Open(context.Background(), itemAuthorityCreateCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error echoed response body secret: %v", err)
	}
}
