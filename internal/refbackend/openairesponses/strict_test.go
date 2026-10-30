package openairesponses_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
)

func TestStrictToolOutputAdjacency_violations(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{StrictToolOutputAdjacency: true}))
	t.Cleanup(srv.Close)

	post := func(body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer sk-test")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// Message between call and output -> 400.
	trapped := `{"model":"deepseek-flash","input":[{"type":"function_call","call_id":"call_1","name":"w","arguments":"{}"},{"role":"user","content":"trapped"},{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`
	code, body := post(trapped)
	if code != http.StatusBadRequest || !strings.Contains(body, "No tool output found for tool call call_1") {
		t.Fatalf("trapped item must 400, got %d %s", code, body)
	}
	// Healthy adjacency -> 200.
	healthy := `{"model":"deepseek-flash","input":[{"type":"function_call","call_id":"call_1","name":"w","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`
	if code, _ := post(healthy); code != http.StatusOK {
		t.Fatalf("healthy input must pass, got %d", code)
	}
	// Off by default -> trapped passes.
	srv2 := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv2.Close)
	req, _ := http.NewRequest(http.MethodPost, srv2.URL+"/v1/responses", strings.NewReader(trapped))
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default emulator must not enforce strictness, got %d", resp.StatusCode)
	}
}
