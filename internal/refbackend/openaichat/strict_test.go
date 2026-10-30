package openaichat_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
)

func TestStrictReasoningContent_violations(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{StrictReasoningContent: true}))
	t.Cleanup(srv.Close)

	post := func(body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(body))
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

	// Tool-call turn without reasoning_content -> 400.
	missing := `{"model":"deepseek-chat","tools":[{"type":"function","function":{"name":"w"}}],"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"w","arguments":"{}"}}]}]}`
	code, body := post(missing)
	if code != http.StatusBadRequest || !strings.Contains(body, "reasoning_content") {
		t.Fatalf("missing field must 400, got %d %s", code, body)
	}
	// Plain assistant turn without reasoning_content also 400s when tools are
	// present: DeepSeek requires passback on all turns, not only tool-call
	// turns. Empty string counts as present.
	plainMissing := `{"model":"deepseek-chat","tools":[{"type":"function","function":{"name":"w"}}],"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"just text"}]}`
	if code, body := post(plainMissing); code != http.StatusBadRequest || !strings.Contains(body, "reasoning_content") {
		t.Fatalf("plain turn missing field must 400, got %d %s", code, body)
	}
	// Empty string counts as present -> 200.
	empty := `{"model":"deepseek-chat","tools":[{"type":"function","function":{"name":"w"}}],"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"reasoning_content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"w","arguments":"{}"}}]}]}`
	if code, _ := post(empty); code != http.StatusOK {
		t.Fatalf("empty reasoning_content must pass, got %d", code)
	}
	// No tools -> never a violation.
	plain := `{"model":"deepseek-chat","messages":[{"role":"assistant","content":"hi"}]}`
	if code, _ := post(plain); code != http.StatusOK {
		t.Fatalf("no-tools request must pass, got %d", code)
	}
	// Off by default -> missing field passes.
	srv2 := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv2.Close)
	req, _ := http.NewRequest(http.MethodPost, srv2.URL+"/v1/chat/completions", strings.NewReader(missing))
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
