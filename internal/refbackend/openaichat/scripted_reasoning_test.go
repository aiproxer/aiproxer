package openaichat_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
)

func TestScriptedResponder_reasoningAndTools(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		Responder: refbackend.ScriptedResponder([]refbackend.ScriptedTurn{{
			VisibleText: "hi",
			Reasoning:   "think",
			ToolID:      "call_1",
			ToolName:    "lookup",
			ToolArgs:    `{"q":"x"}`,
		}}),
	}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(openaiChatMinimalBody))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	s := string(b)
	for _, need := range []string{`"reasoning_content":"think"`, `"id":"call_1"`, `"name":"lookup"`} {
		if !strings.Contains(s, need) {
			t.Fatalf("json missing %s: %s", need, s)
		}
	}

	resp2, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if !strings.Contains(string(b2), "script exhausted") && resp2.StatusCode != http.StatusInternalServerError {
		// second call exhausts single-turn script
		if resp2.StatusCode != http.StatusInternalServerError {
			t.Fatalf("expected script exhaustion, got %d %s", resp2.StatusCode, b2)
		}
	}
}
