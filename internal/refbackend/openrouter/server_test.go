package openrouter_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openrouter"
)

func TestHandler_chatCompletionsNonStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/chat/completions", strings.NewReader(`{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
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
	if !strings.Contains(string(body), "or-ok") {
		t.Fatalf("body: %s", body)
	}
}

func TestHandler_chatCompletionsStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/chat/completions", strings.NewReader(`{"model":"openai/gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "or-stream-ok") {
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

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/responses", strings.NewReader(`{"model":"openai/gpt-4o-mini","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "or-ok") {
		t.Fatalf("body: %s", body)
	}
}

func TestHandler_responsesStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/responses", strings.NewReader(`{"model":"openai/gpt-4o-mini","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "or-stream-ok") {
		t.Fatalf("body: %s", body)
	}
}

func TestHandler_missingBearerReturns401(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/chat/completions", strings.NewReader(`{}`))
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

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer sk-test")
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

func TestHandler_headerCapture(t *testing.T) {
	t.Parallel()
	var captured http.Header
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestHeaders: func(h http.Header) { captured = h.Clone() },
	}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("HTTP-Referer", "https://myapp.com")
	req.Header.Set("X-OpenRouter-Title", "MyApp")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if captured.Get("Http-Referer") != "https://myapp.com" {
		t.Fatalf("HTTP-Referer: %q", captured.Get("Http-Referer"))
	}
	if captured.Get("X-Openrouter-Title") != "MyApp" {
		t.Fatalf("X-OpenRouter-Title: %q", captured.Get("X-Openrouter-Title"))
	}
}

func TestHandler_bodyCapture(t *testing.T) {
	t.Parallel()
	var captured string
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(b []byte) { captured = string(b) },
	}))
	t.Cleanup(srv.Close)

	payload := `{"model":"openai/gpt-4o-mini","messages":[],"provider":{"order":["OpenAI"]}}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if !strings.Contains(captured, `"provider"`) {
		t.Fatalf("body: %s", captured)
	}
}
