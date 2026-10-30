package vllm_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/vllm"
)

func TestHandler_health(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/health", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("body: %s", body)
	}
}

func TestHandler_models(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "meta-llama/Llama-3-8B-Instruct") {
		t.Fatalf("body: %s", body)
	}
}

func TestHandler_chatCompletionsNonStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"meta-llama/Llama-3-8B-Instruct","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer vllm-test")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "vllm-ok") {
		t.Fatalf("body: %s", body)
	}
}

func TestHandler_chatCompletionsStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"meta-llama/Llama-3-8B-Instruct","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer vllm-test")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "vllm-ok") {
		t.Fatalf("body: %s", body)
	}
	if !strings.Contains(string(body), "[DONE]") {
		t.Fatal("missing [DONE]")
	}
}

func TestHandler_responsesNonStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"meta-llama/Llama-3-8B-Instruct","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer vllm-test")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "vllm-ok") {
		t.Fatalf("body: %s", body)
	}
}

func TestHandler_responsesStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"meta-llama/Llama-3-8B-Instruct","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer vllm-test")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "vllm-ok") {
		t.Fatalf("body: %s", body)
	}
}

func TestHandler_missingBearerReturns401(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "invalid_api_key") {
		t.Fatalf("expected JSON error body with invalid_api_key, got: %s", body)
	}
}

func TestHandler_forced401(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		ForcedHTTPStatus: http.StatusUnauthorized,
	}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer vllm-test")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestHandler_forced429(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		ForcedHTTPStatus: http.StatusTooManyRequests,
		ForcedRetryAfter: "60",
	}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer vllm-test")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") != "60" {
		t.Fatalf("Retry-After: %q", resp.Header.Get("Retry-After"))
	}
}

func TestHandler_forcedErrorJSON(t *testing.T) {
	t.Parallel()
	custom := `{"error":{"message":"custom forced error","type":"invalid_request_error"}}`
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		ForcedHTTPStatus: http.StatusTooManyRequests,
		ForcedErrorJSON:  custom,
	}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer vllm-test")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "custom forced error") {
		t.Fatalf("body: %s", body)
	}
}

func TestHandler_headerCapture(t *testing.T) {
	t.Parallel()
	var captured http.Header
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestHeaders: func(h http.Header) { captured = h.Clone() },
	}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer vllm-test")
	req.Header.Set("X-Custom", "custom-value")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if captured.Get("X-Custom") != "custom-value" {
		t.Fatalf("X-Custom: %q", captured.Get("X-Custom"))
	}
}

func TestHandler_bodyCapture(t *testing.T) {
	t.Parallel()
	var captured string
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(b []byte) { captured = string(b) },
	}))
	t.Cleanup(srv.Close)

	payload := `{"model":"meta-llama/Llama-3-8B-Instruct","messages":[],"max_tokens":100}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer vllm-test")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if !strings.Contains(captured, `"max_tokens"`) {
		t.Fatalf("body: %s", captured)
	}
}

func TestHandler_credentialCapture(t *testing.T) {
	t.Parallel()
	var captured string
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnAuthorizedCredential: func(s string) { captured = s },
	}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer vllm-secret-key")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if captured != "vllm-secret-key" {
		t.Fatalf("credential: %q", captured)
	}
}

func TestHandler_chatStreamWithUsage(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"x","stream":true,"stream_options":{"include_usage":true},"messages":[]}`))
	req.Header.Set("Authorization", "Bearer vllm-test")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"usage"`) {
		t.Fatalf("expected usage in stream, body: %s", body)
	}
}

func TestHandler_getReturns404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer vllm-test")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestHandler_unknownPathReturns404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/unknown", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer vllm-test")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestHandler_responsesPathNotMatchedByChatSuffix(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"x","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer vllm-test")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "vllm-ok") {
		t.Fatalf("expected responses body, got: %s", body)
	}
}
