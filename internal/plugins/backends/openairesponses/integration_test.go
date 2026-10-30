package openairesponses_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestIntegration_refbackendStreamingText(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	call := aipapi.Call{
		ID: "int1",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "gpt-4o-mini"},
		Key:     "openai-responses:gpt-4o-mini",
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(col.Text.String(), "stream-ok") {
		t.Fatalf("text: %q", col.Text.String())
	}
}

func TestIntegration_refbackendDefaultVerbosityWithoutRequestOverride(t *testing.T) {
	t.Parallel()
	var requestBody []byte
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{OnRequestBody: func(body []byte) {
		requestBody = append([]byte(nil), body...)
	}}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test", DefaultVerbosity: aipapi.VerbosityMedium})
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Text struct {
			Verbosity string `json:"verbosity"`
		} `json:"text"`
	}
	if err := json.Unmarshal(requestBody, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Text.Verbosity != "medium" {
		t.Fatalf("default nested verbosity = %q, body=%s", decoded.Text.Verbosity, requestBody)
	}
}

func TestIntegration_refbackendNonStreamUsage(t *testing.T) {
	t.Parallel()
	const inner = `{"id":"resp_usage","object":"response","created_at":1715620000,"status":"completed","model":"gpt-4o-mini","usage":{"input_tokens":3,"output_tokens":7,"total_tokens":10,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}},"output":[{"type":"message","id":"msg_out","status":"completed","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`
	wrapped := `{"type":"response.completed","sequence_number":1,"response":` + inner + `}`
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		StreamSSE: "event: response.completed\ndata: " + wrapped + "\n\ndata: [DONE]\n\n",
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	call := aipapi.Call{
		ID: "int2",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("ping")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "gpt-4o-mini"},
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if col.InputTokens != 3 || col.OutputTokens != 7 {
		t.Fatalf("usage: in=%d out=%d", col.InputTokens, col.OutputTokens)
	}
	if col.Text.String() != "done" {
		t.Fatalf("text: %q", col.Text.String())
	}
}

func TestIntegration_refbackendStreamAssistantMediaCollected(t *testing.T) {
	t.Parallel()
	const inner = `{"id":"resp_mm_stream","object":"response","created_at":1715620000,"status":"completed","model":"gpt-4o-mini","output":[{"type":"message","id":"m1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"see"},{"type":"input_image","image_url":"https://cdn.example.com/out.png"},{"type":"input_file","file_id":"file-out-1"}]}]}`
	wrapped := `{"type":"response.completed","sequence_number":1,"response":` + inner + `}`
	sse := "event: response.completed\ndata: " + wrapped + "\n\ndata: [DONE]\n\n"
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{StreamSSE: sse}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	call := aipapi.Call{
		ID: "mm-stream",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "gpt-4o-mini"},
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "see" {
		t.Fatalf("text: %q", col.Text.String())
	}
	if len(col.AssistantMedia) != 2 {
		t.Fatalf("assistant media: %#v", col.AssistantMedia)
	}
	if col.AssistantMedia[0].Kind != aipapi.PartImageRef || col.AssistantMedia[0].ImageRef != "https://cdn.example.com/out.png" {
		t.Fatalf("image part: %#v", col.AssistantMedia[0])
	}
	if col.AssistantMedia[1].Kind != aipapi.PartFileRef || col.AssistantMedia[1].FileRef != "file-out-1" {
		t.Fatalf("file part: %#v", col.AssistantMedia[1])
	}
}

func TestIntegration_refbackendMultimodalRequestBody(t *testing.T) {
	t.Parallel()
	var captured string
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(b []byte) { captured = string(b) },
		NonStreamJSON: `{"id":"x","object":"response","created_at":1,"status":"completed","model":"gpt-4o-mini","output":[{"type":"message","id":"m","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`,
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	call := aipapi.Call{
		ID: "mm",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart("x"),
				{Kind: aipapi.PartImageRef, ImageRef: "https://example.com/i.png"},
				aipapi.FilePart("data:application/pdf;base64,QUFB", "application/pdf", "f.pdf"),
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	// Non-streaming path from SDK still POSTs; refbackend switches on "stream":true in body.
	// ParamsForCall does not set stream — NewStreaming adds stream:true in SDK.
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(captured, "input_image") || !strings.Contains(captured, "input_file") {
		t.Fatalf("expected multimodal request markers, got: %s", captured)
	}
}

func TestIntegration_refbackendToolCallStream(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"q":{"type":"integer"}}}`)
	const toolStreamSSE = "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_int_t\",\"call_id\":\"call_fc\",\"name\":\"get_weather\",\"status\":\"in_progress\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":1,\"item_id\":\"fc_int_t\",\"output_index\":0,\"delta\":\"{\\\"q\\\":\"}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":2,\"item_id\":\"fc_int_t\",\"output_index\":0,\"delta\":\"1}\"}\n\n" +
		"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"sequence_number\":3,\"item_id\":\"fc_int_t\",\"output_index\":0,\"name\":\"get_weather\",\"arguments\":\"{\\\"q\\\":1}\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":4,\"response\":{\"id\":\"r_tool\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-4o-mini\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc_int_t\",\"name\":\"get_weather\",\"arguments\":\"{\\\"q\\\":1}\"}]}}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{StreamSSE: toolStreamSSE}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	call := aipapi.Call{
		ID: "tool-int",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("weather?")},
		}},
		Tools: []aipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  schema,
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "get_weather"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	tcs := col.OrderedToolCalls()
	if len(tcs) != 1 {
		t.Fatalf("tool calls: %+v", tcs)
	}
	if tcs[0].Name != "get_weather" || tcs[0].ID != "fc_int_t" {
		t.Fatalf("tool summary: %+v", tcs[0])
	}
	if tcs[0].Arguments != `{"q":1}` {
		t.Fatalf("arguments: %q", tcs[0].Arguments)
	}
}

func TestIntegration_refbackend429_singleUpstreamHTTPAttemptWhenSDKMaxRetriesZero(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	rb := refbackend.NewHandler(refbackend.Config{
		ForcedHTTPStatus: http.StatusTooManyRequests,
		ForcedRetryAfter: "1",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		rb.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:       srv.URL + "/v1",
		APIKey:        "sk-test",
		HTTPClient:    srv.Client(),
		SDKMaxRetries: new(int),
	})
	call := aipapi.Call{
		ID: "rl",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "gpt-4o-mini"},
	}
	_, err := be.Open(context.Background(), call, cand)
	if err == nil {
		t.Fatal("expected Open error from 429 refbackend (single credential exhausted)")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected recoverable pre-output, got: %v", err)
	}
	if n := reqs.Load(); n != 1 {
		t.Fatalf("upstream HTTP attempts: %d want 1", n)
	}
}

func parseBearerAuth(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

func TestIntegration_multiKey429ThenSuccessOnSecondCredential(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	ok := refbackend.NewHandler(refbackend.Config{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		switch parseBearerAuth(r) {
		case "sk-429":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "7201")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate","type":"requests","code":"rate_limit_exceeded"}}`))
			return
		default:
			ok.ServeHTTP(w, r)
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:       srv.URL + "/v1",
		APIKey:        "sk-429",
		APIKeys:       []string{"sk-429", "sk-ok"},
		HTTPClient:    srv.Client(),
		SDKMaxRetries: new(int),
	})
	call := aipapi.Call{
		ID: "rot429",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = es.Close() })
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(col.Text.String(), "stream-ok") {
		t.Fatalf("text: %q", col.Text.String())
	}
	if n := reqs.Load(); n != 2 {
		t.Fatalf("upstream HTTP attempts: %d want 2", n)
	}
}

func TestIntegration_multiKey401ThenSuccessOnSecondCredential(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	ok := refbackend.NewHandler(refbackend.Config{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		switch parseBearerAuth(r) {
		case "sk-bad":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"incorrect api key","type":"invalid_request_error","code":"invalid_api_key"}}`))
			return
		default:
			ok.ServeHTTP(w, r)
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:       srv.URL + "/v1",
		APIKey:        "sk-bad",
		APIKeys:       []string{"sk-bad", "sk-ok"},
		HTTPClient:    srv.Client(),
		SDKMaxRetries: new(int),
	})
	call := aipapi.Call{
		ID: "rot401",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = es.Close() })
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(col.Text.String(), "stream-ok") {
		t.Fatalf("text: %q", col.Text.String())
	}
	if n := reqs.Load(); n != 2 {
		t.Fatalf("upstream HTTP attempts: %d want 2", n)
	}
}

func TestIntegration_multiKeyAllRateLimitedRecoverablePreOutput(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	rb := refbackend.NewHandler(refbackend.Config{
		ForcedHTTPStatus: http.StatusTooManyRequests,
		ForcedRetryAfter: "7201",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		rb.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:       srv.URL + "/v1",
		APIKey:        "a",
		APIKeys:       []string{"a", "b"},
		HTTPClient:    srv.Client(),
		SDKMaxRetries: new(int),
	})
	call := aipapi.Call{
		ID: "exhaust",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	_, err := be.Open(context.Background(), call, cand)
	if err == nil {
		t.Fatal("expected error")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected recoverable pre-output, got: %v", err)
	}
	if n := reqs.Load(); n != 2 {
		t.Fatalf("upstream HTTP attempts: %d want 2", n)
	}
}

func TestIntegration_refbackend500_recoverablePreOutputWhenSDKMaxRetriesZero(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	rb := refbackend.NewHandler(refbackend.Config{
		ForcedHTTPStatus: http.StatusInternalServerError,
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		rb.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:       srv.URL + "/v1",
		APIKey:        "sk-test",
		HTTPClient:    srv.Client(),
		SDKMaxRetries: new(int),
	})
	call := aipapi.Call{
		ID: "ise",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "gpt-4o-mini"},
	}
	_, err := be.Open(context.Background(), call, cand)
	if err == nil {
		t.Fatal("expected Open error from 500 refbackend")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected recoverable pre-output, got: %v", err)
	}
	if !strings.Contains(err.Error(), "openai-responses: ") {
		t.Fatalf("expected backend ID prefix in error, got: %v", err)
	}
	if n := reqs.Load(); n != 1 {
		t.Fatalf("upstream HTTP attempts: %d want 1 (no credential rotation on 500)", n)
	}
}

func TestIntegration_multiKeyFirstReturns400NoRotation(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	ok := refbackend.NewHandler(refbackend.Config{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		if parseBearerAuth(r) == "sk-badreq" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"bad","type":"invalid_request_error","code":"bad"}}`))
			return
		}
		ok.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:       srv.URL + "/v1",
		APIKey:        "sk-badreq",
		APIKeys:       []string{"sk-badreq", "sk-ok"},
		HTTPClient:    srv.Client(),
		SDKMaxRetries: new(int),
	})
	call := aipapi.Call{
		ID: "no-rot-400",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	_, err := be.Open(context.Background(), call, cand)
	if err == nil {
		t.Fatal("expected error")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("did not expect recoverable pre-output for 400: %v", err)
	}
	if !strings.Contains(err.Error(), "openai-responses: ") {
		t.Fatalf("expected backend ID prefix in error, got: %v", err)
	}
	if n := reqs.Load(); n != 1 {
		t.Fatalf("upstream HTTP attempts: %d want 1 (no second credential try)", n)
	}
}
