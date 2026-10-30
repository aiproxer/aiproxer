package gemini_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/gemini"
	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/gemini"
	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestIntegration_refbackendMissingAPIKeyOpenFails(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: "", HTTPClient: srv.Client()})
	call := aipapi.Call{
		ID: "auth",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	_, err := be.Open(context.Background(), call, cand)
	if err == nil {
		t.Fatal("expected error when API key is missing for Google AI backend")
	}
}

func TestIntegration_refbackendStreamingText(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: "fake-key", HTTPClient: srv.Client()})
	call := aipapi.Call{
		ID: "int1",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "gemini-2.0-flash"},
		Key:     "gemini:gemini-2.0-flash",
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

func TestIntegration_refbackendStreamUsage(t *testing.T) {
	t.Parallel()
	const sse = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"done\"}]}}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":7}}\n\n"
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		StreamSSE: sse,
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client()})
	call := aipapi.Call{
		ID: "int2",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("ping")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "gemini-2.0-flash"},
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

	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
	pngB64 := base64.StdEncoding.EncodeToString(png)
	pdfB64 := base64.StdEncoding.EncodeToString(pdf)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client()})
	call := aipapi.Call{
		ID: "mm",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart("x"),
				{Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64," + pngB64},
				aipapi.FilePart("data:application/pdf;base64,"+pdfB64, "application/pdf", "f.pdf"),
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(captured, "inlineData") && !strings.Contains(captured, "inline_data") {
		t.Fatalf("expected inline multimodal payload, got: %s", captured)
	}
}

func TestIntegration_refbackendToolCallStream(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)
	const sse = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"get_temp\",\"args\":{\"city\":\"NYC\"},\"id\":\"call_gem_1\"}}]}}]}\n\n"
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{StreamSSE: sse}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client()})
	call := aipapi.Call{
		ID: "tool-int",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("weather?")},
		}},
		Tools: []aipapi.ToolDef{{
			Name:        "get_temp",
			Description: "Temperature",
			Parameters:  schema,
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "get_temp"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
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
	if tcs[0].Name != "get_temp" || tcs[0].ID != "call_gem_1" {
		t.Fatalf("tool: %+v", tcs[0])
	}
	if tcs[0].Arguments != `{"city":"NYC"}` {
		t.Fatalf("args: %q", tcs[0].Arguments)
	}
}

func TestIntegration_refbackend429_singleUpstreamHTTPAttempt(t *testing.T) {
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

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client()})
	call := aipapi.Call{
		ID: "rl",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	_, err := be.Open(context.Background(), call, cand)
	if err == nil {
		t.Fatal("expected Open error from 429 refbackend (single credential exhausted)")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected recoverable pre-output, got: %v", err)
	}
	if n := reqs.Load(); n != 1 {
		t.Fatalf("upstream HTTP attempts: %d want 1 (genai should not mask first 429)", n)
	}
}

func parseGeminiAPIKey(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("x-goog-api-key"))
}

func TestIntegration_multiKey429ThenSuccessOnSecondCredential(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	var keysMu sync.Mutex
	var keys []string
	ok := refbackend.NewHandler(refbackend.Config{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		k := parseGeminiAPIKey(r)
		keysMu.Lock()
		keys = append(keys, k)
		keysMu.Unlock()
		if k == "key-429" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "7200")
			w.WriteHeader(http.StatusTooManyRequests)
			// google.golang.org/genai does not surface HTTP Retry-After on [genai.APIError]; real
			// Google errors may carry google.rpc.RetryInfo in JSON details (see errors_classify.go).
			// The delay is strictly above the proxy-wide 1h quota-switch
			// threshold: at-or-below hints are a transient same-key throttle.
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"rate","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"7200s"}]}}`))
			return
		}
		ok.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:    srv.URL,
		APIKey:     "key-429",
		APIKeys:    []string{"key-429", "key-ok"},
		HTTPClient: srv.Client(),
	})
	call := aipapi.Call{
		ID: "rot429",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
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
	keysMu.Lock()
	got := append([]string(nil), keys...)
	keysMu.Unlock()
	if len(got) != 2 || got[0] != "key-429" || got[1] != "key-ok" {
		t.Fatalf("credential sequence: %#v", got)
	}
}

func TestIntegration_multiKey401ThenSuccessOnSecondCredential(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	ok := refbackend.NewHandler(refbackend.Config{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		k := parseGeminiAPIKey(r)
		if k == "key-bad" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":401,"message":"API key not valid","status":"INVALID_ARGUMENT"}}`))
			return
		}
		ok.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:    srv.URL,
		APIKey:     "key-bad",
		APIKeys:    []string{"key-bad", "key-ok"},
		HTTPClient: srv.Client(),
	})
	call := aipapi.Call{
		ID: "rot401",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
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
