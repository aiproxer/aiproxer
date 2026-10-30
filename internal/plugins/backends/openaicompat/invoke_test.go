package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicred"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3/option"
)

func TestOpenChat_nonStreamingUsesNonStreamEndpoint(t *testing.T) {
	t.Parallel()
	rec := newInvokeRecorder()
	srv := newInvokeServer(t, rec)
	cli := openaicred.NewClient(srv.URL, "sk-test", srv.Client(), new(int))

	call := invokeTestCall()
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	es, err := OpenChat(context.Background(), cli, InvokeRequest{
		ProviderID: "test",
		Call:       call,
		Candidate:  invokeTestCandidate("gpt-test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainInvokeEvents(t, es)
	if !hasTextDelta(events, "chat-ns-ok") {
		t.Fatalf("expected chat non-stream text, got %+v", events)
	}

	body := rec.lastBody(t)
	if bytes.Contains(body, []byte(`"stream":true`)) {
		t.Fatalf("non-streaming chat sent stream:true body=%s", string(body))
	}
	if bytes.Contains(body, []byte("stream_options")) {
		t.Fatalf("non-streaming chat sent stream_options body=%s", string(body))
	}
}

func TestOpenChat_emptyTransportModeDefaultsToStreaming(t *testing.T) {
	t.Parallel()
	rec := newInvokeRecorder()
	srv := newInvokeServer(t, rec)
	cli := openaicred.NewClient(srv.URL, "sk-test", srv.Client(), new(int))

	es, err := OpenChat(context.Background(), cli, InvokeRequest{
		ProviderID: "test",
		Call:       invokeTestCall(),
		Candidate:  invokeTestCandidate("gpt-test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainInvokeEvents(t, es)
	if !hasTextDelta(events, "chat-stream-ok") {
		t.Fatalf("expected chat stream text, got %+v", events)
	}

	body := rec.lastBody(t)
	if !bytes.Contains(body, []byte(`"stream":true`)) {
		t.Fatalf("streaming chat did not send stream:true body=%s", string(body))
	}
}

func TestOpenResponses_nonStreamingReturnsTextAndUsage(t *testing.T) {
	t.Parallel()
	rec := newInvokeRecorder()
	srv := newInvokeServer(t, rec)
	cli := openaicred.NewClient(srv.URL, "sk-test", srv.Client(), new(int))

	call := invokeTestCall()
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	es, err := OpenResponses(context.Background(), cli, InvokeRequest{
		ProviderID: "test",
		Call:       call,
		Candidate:  invokeTestCandidate("gpt-test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainInvokeEvents(t, es)
	if !hasTextDelta(events, "responses-ns-ok") {
		t.Fatalf("expected responses non-stream text, got %+v", events)
	}
	if !hasUsage(events) {
		t.Fatalf("expected responses usage, got %+v", events)
	}
}

func TestOpenResponses_emptyTransportModeDefaultsToStreaming(t *testing.T) {
	t.Parallel()
	rec := newInvokeRecorder()
	srv := newInvokeServer(t, rec)
	cli := openaicred.NewClient(srv.URL, "sk-test", srv.Client(), new(int))

	es, err := OpenResponses(context.Background(), cli, InvokeRequest{
		ProviderID: "test",
		Call:       invokeTestCall(),
		Candidate:  invokeTestCandidate("gpt-test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainInvokeEvents(t, es)
	if !hasTextDelta(events, "responses-stream-ok") {
		t.Fatalf("expected responses stream text, got %+v", events)
	}

	body := rec.lastBody(t)
	if !bytes.Contains(body, []byte(`"stream":true`)) {
		t.Fatalf("streaming responses did not send stream:true body=%s", string(body))
	}
}

func TestOpenChat_forwardsSDKOptions(t *testing.T) {
	t.Parallel()
	rec := newInvokeRecorder()
	srv := newInvokeServer(t, rec)
	cli := openaicred.NewClient(srv.URL, "sk-test", srv.Client(), new(int))

	call := invokeTestCall()
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	_, err := OpenChat(context.Background(), cli, InvokeRequest{
		ProviderID: "test",
		Call:       call,
		Candidate:  invokeTestCandidate("gpt-test"),
		SDKOptions: []option.RequestOption{option.WithJSONSet("max_tokens", 17), option.WithJSONDel("stream_options")},
	})
	if err != nil {
		t.Fatal(err)
	}

	body := rec.lastBody(t)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal body: %v body=%s", err, string(body))
	}
	if string(payload["max_tokens"]) != "17" {
		t.Fatalf("max_tokens = %s, want 17 body=%s", payload["max_tokens"], string(body))
	}
	if _, ok := payload["stream_options"]; ok {
		t.Fatalf("stream_options should be deleted body=%s", string(body))
	}
}

func TestOpenChat_paramsErrorPropagates(t *testing.T) {
	t.Parallel()
	_, err := OpenChat(context.Background(), openaicred.NewClient("http://127.0.0.1", "sk-test", nil, new(int)), InvokeRequest{
		ProviderID: "test",
		Call:       invokeTestCall(),
	})
	if err == nil {
		t.Fatal("expected missing model error")
	}
	if !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("error = %v", err)
	}
}

type invokeRecorder struct {
	mu   sync.Mutex
	body []byte
}

func newInvokeRecorder() *invokeRecorder { return &invokeRecorder{} }

func (r *invokeRecorder) record(body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.body = append(r.body[:0], body...)
}

func (r *invokeRecorder) lastBody(t *testing.T) []byte {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.body) == 0 {
		t.Fatal("no request body captured")
	}
	return append([]byte(nil), r.body...)
}

func newInvokeServer(t *testing.T, rec *invokeRecorder) *httptest.Server {
	t.Helper()
	return newInvokeServerWithHook(t, rec, nil)
}

func newInvokeServerWithHook(t *testing.T, rec *invokeRecorder, hook func(*http.Request)) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if hook != nil {
			hook(r)
		}
		rec.record(body)
		streaming := bytes.Contains(body, []byte(`"stream":true`))
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions") && streaming:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"chat-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"chat-stream-ok\"},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chat-ns","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"chat-ns-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
		case strings.HasSuffix(r.URL.Path, "/responses") && streaming:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: response.completed\n")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_stream\",\"object\":\"response\",\"created_at\":1715620000,\"status\":\"completed\",\"model\":\"gpt-test\",\"output\":[{\"type\":\"message\",\"id\":\"msg\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"responses-stream-ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case strings.HasSuffix(r.URL.Path, "/responses"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"resp_ns","object":"response","created_at":1715620000,"status":"completed","model":"gpt-test","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"responses-ns-ok"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func invokeTestCall() aipapi.Call {
	return aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}}}
}

func invokeTestCandidate(model string) routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: model}}
}

func drainInvokeEvents(t *testing.T, es aipapi.ManagedEventStream) []aipapi.Event {
	t.Helper()
	defer func() { _ = es.Close() }()
	events := []aipapi.Event{}
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

func hasUsage(events []aipapi.Event) bool {
	for _, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta {
			return true
		}
	}
	return false
}
