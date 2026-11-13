package anthropic_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/anthropicmessages"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestIntegration_refbackendMissingAPIKeyOpenFails(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: ""})
	call := aipapi.Call{
		ID: "auth",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	_, err := be.Open(context.Background(), call, cand)
	if err == nil {
		t.Fatal("expected error when no credentials to build pool")
	}
}

// Minimal Anthropic SSE with one text block streaming "stream-ok" (SDK-parseable).
const refbackendStreamTextSSE = "event: message_start\ndata: " +
	`{"type":"message_start","message":{"id":"m_stream","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}` +
	"\n\n" +
	"event: content_block_start\ndata: " +
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` +
	"\n\n" +
	"event: content_block_delta\ndata: " +
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"stream-ok"}}` +
	"\n\n" +
	"event: content_block_stop\ndata: " +
	`{"type":"content_block_stop","index":0}` +
	"\n\n" +
	"event: message_stop\ndata: " +
	`{"type":"message_stop"}` +
	"\n\n"

func TestIntegration_refbackendStreamingText(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		StreamSSE: refbackendStreamTextSSE,
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey})
	call := aipapi.Call{
		ID: "int1",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "claude-3-5-haiku-20241022"},
		Key:     "anthropic:claude-3-5-haiku-20241022",
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

func TestIntegration_refbackendDefaultStreamCompletes(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey})
	call := aipapi.Call{
		ID: "int-def",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("ping")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "claude-3-5-haiku-20241022"},
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if !col.FinishReceived {
		t.Fatal("expected response_finished")
	}
	if col.Text.String() != "" {
		t.Fatalf("default refbackend stream has no text deltas; got %q", col.Text.String())
	}
	_ = col
}

func TestIntegration_refbackendStreamUsage(t *testing.T) {
	t.Parallel()
	usageDelta := "event: message_start\ndata: " +
		`{"type":"message_start","message":{"id":"m_u","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}` +
		"\n\n" +
		"event: content_block_start\ndata: " +
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` +
		"\n\n" +
		"event: content_block_delta\ndata: " +
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}` +
		"\n\n" +
		"event: content_block_stop\ndata: " +
		`{"type":"content_block_stop","index":0}` +
		"\n\n" +
		"event: message_delta\ndata: " +
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":""},"usage":{"input_tokens":3,"output_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"server_tool_use":{"web_fetch_requests":0,"web_search_requests":0}}}` +
		"\n\n" +
		"event: message_stop\ndata: " +
		`{"type":"message_stop"}` +
		"\n\n"

	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		StreamSSE: usageDelta,
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey})
	call := aipapi.Call{
		ID: "int2",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("ping")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "claude-3-5-haiku-20241022"},
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

func TestIntegration_refbackendMultimodalRequestBody(t *testing.T) {
	t.Parallel()
	var captured string
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(b []byte) { captured = string(b) },
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey})
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
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(captured, `"type":"image"`) || !strings.Contains(captured, `"type":"document"`) {
		t.Fatalf("expected multimodal request markers, got: %s", captured)
	}
}

// Wire-shaped SSE for tool_use + input_json_delta (SDK-parseable), ending with message_stop.
const refbackendToolUseStreamSSE = "event: message_start\ndata: " +
	`{"type":"message_start","message":{"id":"m_tool","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}` +
	"\n\n" +
	"event: content_block_start\ndata: " +
	`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{}}}` +
	"\n\n" +
	"event: content_block_delta\ndata: " +
	`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"NYC\"}"}}` +
	"\n\n" +
	"event: content_block_stop\ndata: " +
	`{"type":"content_block_stop","index":0}` +
	"\n\n" +
	"event: message_stop\ndata: " +
	`{"type":"message_stop"}` +
	"\n\n"

func TestIntegration_refbackendToolUseStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		StreamSSE: refbackendToolUseStreamSSE,
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey})
	call := aipapi.Call{
		ID: "tool-int",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("weather?")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "claude-3-5-haiku-20241022"},
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	tools := col.OrderedToolCalls()
	if len(tools) != 1 {
		t.Fatalf("tool calls: %+v", tools)
	}
	if tools[0].ID != "toolu_01" || tools[0].Name != "get_weather" || tools[0].Arguments != `{"city":"NYC"}` {
		t.Fatalf("tool summary: %+v", tools[0])
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
		BaseURL:       srv.URL,
		APIKey:        testkit.SyntheticAnthropicAPIKey,
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
		Primary: routing.Primary{Backend: backend.ID, Model: "claude-3-5-haiku-20241022"},
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

func parseAnthropicAPIKey(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}

func TestIntegration_multiKey429ThenSuccessOnSecondCredential(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	ok := refbackend.NewHandler(refbackend.Config{StreamSSE: refbackendStreamTextSSE})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		switch parseAnthropicAPIKey(r) {
		case "sk-429":
			w.Header().Set("Content-Type", "application/json")
			// Strictly above the proxy-wide 1h quota-switch threshold:
			// at-or-below hints are a transient same-key throttle.
			w.Header().Set("Retry-After", "7200")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
			return
		default:
			ok.ServeHTTP(w, r)
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:       srv.URL,
		APIKey:        "sk-429",
		APIKeys:       []string{"sk-429", testkit.SyntheticAnthropicAPIKey},
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
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
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
	ok := refbackend.NewHandler(refbackend.Config{StreamSSE: refbackendStreamTextSSE})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		switch parseAnthropicAPIKey(r) {
		case "sk-bad":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid"}}`))
			return
		default:
			ok.ServeHTTP(w, r)
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:       srv.URL,
		APIKey:        "sk-bad",
		APIKeys:       []string{"sk-bad", testkit.SyntheticAnthropicAPIKey},
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
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
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

func TestIntegration_paramsErrorDoesNotRotateCredentials(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	ok := refbackend.NewHandler(refbackend.Config{StreamSSE: refbackendStreamTextSSE})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		ok.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:       srv.URL,
		APIKey:        "sk-a",
		APIKeys:       []string{"sk-a", "sk-b"},
		HTTPClient:    srv.Client(),
		SDKMaxRetries: new(int),
	})
	call := aipapi.Call{
		ID: "params",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	// No model on candidate or call extensions — ParamsForCall fails before any HTTP.
	cand := routing.AttemptCandidate{Primary: routing.Primary{}}
	_, err := be.Open(context.Background(), call, cand)
	if err == nil {
		t.Fatal("expected params error")
	}
	if strings.Contains(err.Error(), "model is required") == false {
		t.Fatalf("unexpected error: %v", err)
	}
	if reqs.Load() != 0 {
		t.Fatalf("unexpected HTTP attempts: %d", reqs.Load())
	}
}

// TestIntegration_nativeForwardsReasoningEffort pins the request-level control
// the frontend now decodes: a canonical effort reaches the upstream as
// `output_config.effort`, not as a thinking switch.
func TestIntegration_nativeForwardsReasoningEffort(t *testing.T) {
	t.Parallel()
	var captured string
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(b []byte) { captured = string(b) },
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey})
	call := aipapi.Call{
		ID: "effort",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Options: aipapi.GenerationOptions{ReasoningEffort: "xhigh"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-sonnet-5-5"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	var body struct {
		OutputConfig struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
		Thinking any `json:"thinking"`
	}
	if err := json.Unmarshal([]byte(captured), &body); err != nil {
		t.Fatalf("captured body: %v", err)
	}
	if body.OutputConfig.Effort != "xhigh" {
		t.Fatalf("output_config.effort = %q, want xhigh; body: %s", body.OutputConfig.Effort, captured)
	}
	if body.Thinking != nil {
		t.Fatalf("native effort must not become a thinking switch: %s", captured)
	}
}

// TestIntegration_compatibleDoesNotForwardReasoningEffort keeps third-party
// Anthropic-compatible upstreams on their previous wire shape.
func TestIntegration_compatibleDoesNotForwardReasoningEffort(t *testing.T) {
	t.Parallel()
	var captured string
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(b []byte) { captured = string(b) },
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey, CompatibleModeAuth: true})
	call := aipapi.Call{
		ID: "effort-compatible",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Options: aipapi.GenerationOptions{ReasoningEffort: "high"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-sonnet-5-5"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(captured, "output_config") {
		t.Fatalf("compatible upstream received output_config: %s", captured)
	}
}
