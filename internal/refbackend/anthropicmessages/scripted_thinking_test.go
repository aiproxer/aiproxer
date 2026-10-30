package anthropicmessages_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	feanthropic "github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/anthropicmessages"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestThinkingTurnSSE_toolStopReasonAndBlocks(t *testing.T) {
	t.Parallel()
	sse := refbackend.ThinkingTurnSSE(refbackend.ThinkingTurn{
		VisibleText:     "checking",
		Thinking:        "need-tool",
		Signature:       "sig-tool",
		IncludeRedacted: true,
		RedactedData:    "opaque-tool",
		ToolID:          "toolu_1",
		ToolName:        "lookup",
		ToolInputJSON:   `{"q":"x"}`,
	})
	for _, need := range []string{
		`"type":"thinking"`,
		`"type":"redacted_thinking"`,
		`"type":"tool_use"`,
		`"id":"toolu_1"`,
		`"name":"lookup"`,
		`"stop_reason":"tool_use"`,
	} {
		if !strings.Contains(sse, need) {
			t.Fatalf("SSE missing %s; bytes=%d", need, len(sse))
		}
	}
	if strings.Contains(sse, `"stop_reason":"end_turn"`) {
		t.Fatal("tool turn must not use end_turn stop_reason")
	}
}

func TestScriptedThinkingResponder_thinkingAndRedacted(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingAPIKey: true,
		Responder: refbackend.ScriptedThinkingResponder([]refbackend.ThinkingTurn{{
			VisibleText:     "ans",
			Thinking:        "plan",
			Signature:       "sig-a",
			IncludeRedacted: true,
			RedactedData:    "opaque-a",
		}}),
	}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(`{"model":"claude-3-5-haiku-20241022","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "sk-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	s := string(b)
	for _, need := range []string{`"type":"thinking"`, `"thinking":"plan"`, `"signature":"sig-a"`, `"type":"redacted_thinking"`, `"data":"opaque-a"`} {
		if !strings.Contains(s, need) {
			t.Fatalf("missing %s in %s", need, s)
		}
	}
}

func TestScriptedThinkingResponder_SSERoundTripViaAnthropicBackend(t *testing.T) {
	t.Parallel()
	turn := refbackend.ThinkingTurn{
		VisibleText: "ans",
		Thinking:    "plan",
		Signature:   "sig-plan",
	}
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingAPIKey: true,
		StreamSSE:          refbackend.ThinkingTurnSSE(turn),
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey})
	call := aipapi.Call{
		ID: "think-sse",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "claude-3-5-haiku-20241022"},
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if col.Text.String() != "ans" {
		t.Fatalf("text=%q want ans", col.Text.String())
	}
	if col.Reasoning.String() != "plan" {
		t.Fatalf("reasoning=%q want plan", col.Reasoning.String())
	}
}

func TestScriptedThinkingResponder_SSEFrontendEncodeRoundTrip_redactedAndTool(t *testing.T) {
	t.Parallel()
	turn := refbackend.ThinkingTurn{
		VisibleText:     "checking",
		Thinking:        "need-tool",
		Signature:       "sig-tool",
		IncludeRedacted: true,
		RedactedData:    "opaque-tool",
		ToolID:          "toolu_1",
		ToolName:        "lookup",
		ToolInputJSON:   `{"q":"x"}`,
	}
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingAPIKey: true,
		StreamSSE:          refbackend.ThinkingTurnSSE(turn),
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey})
	call := aipapi.Call{
		ID: "think-fe-tool",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools: []aipapi.ToolDef{{
			Name:        "lookup",
			Description: "lookup",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
		}},
		Extensions: map[string]json.RawMessage{
			"model": json.RawMessage(`"claude-3-5-haiku-20241022"`),
		},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "claude-3-5-haiku-20241022"},
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rec := httptest.NewRecorder()
	feCall := call
	if err := feanthropic.WriteStreamSSE(context.Background(), rec, &feCall, es, feanthropic.EncodeOptions{MessageID: "msg_rt_tool"}); err != nil {
		t.Fatalf("WriteStreamSSE: %v", err)
	}
	body := rec.Body.String()
	for _, need := range []string{`"type":"redacted_thinking"`, `"type":"tool_use"`, `"id":"toolu_1"`, "message_stop"} {
		if !strings.Contains(body, need) {
			t.Fatalf("frontend body missing %s; bytes=%d", need, len(body))
		}
	}
}

func TestScriptedThinkingResponder_SSEFrontendEncodeRoundTrip(t *testing.T) {
	t.Parallel()
	turn := refbackend.ThinkingTurn{
		VisibleText: "ans",
		Thinking:    "plan",
		Signature:   "sig-plan",
	}
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingAPIKey: true,
		StreamSSE:          refbackend.ThinkingTurnSSE(turn),
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey})
	call := aipapi.Call{
		ID: "think-fe",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Extensions: map[string]json.RawMessage{
			"model": json.RawMessage(`"claude-3-5-haiku-20241022"`),
		},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "claude-3-5-haiku-20241022"},
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rec := httptest.NewRecorder()
	feCall := call
	if err := feanthropic.WriteStreamSSE(context.Background(), rec, &feCall, es, feanthropic.EncodeOptions{MessageID: "msg_rt"}); err != nil {
		t.Fatalf("WriteStreamSSE: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"text":"ans"`) && !strings.Contains(body, "ans") {
		t.Fatalf("frontend body missing ans; len=%d body=%q", len(body), body)
	}
	if !strings.Contains(body, "message_stop") {
		t.Fatalf("frontend body missing message_stop; len=%d", len(body))
	}
}
