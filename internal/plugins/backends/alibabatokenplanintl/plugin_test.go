package alibabatokenplanintl_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/alibabatokenplanintl"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const streamSSE = "event: message_start\ndata: " +
	`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"qwen3.7-plus","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
	"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"

func TestNewDiscoversModelsFromDedicatedCatalogAndPrefixesThem(t *testing.T) {
	t.Parallel()
	var modelRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catalog/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		modelRequests++
		if got := r.Header.Get("Authorization"); got != "Bearer env-key" {
			t.Fatalf("authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "qwen3.7-plus"}}})
	}))
	defer srv.Close()

	be := backend.New(backend.Config{APIKey: "env-key", ModelsBaseURL: srv.URL + "/catalog", HTTPClient: srv.Client()})
	snapshot, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if modelRequests != 1 || len(snapshot.Models) != 1 {
		t.Fatalf("requests=%d models=%d", modelRequests, len(snapshot.Models))
	}
	if got := snapshot.Models[0].CanonicalID; got != "alibaba-token-plan-intl/qwen3.7-plus" {
		t.Fatalf("canonical id = %q", got)
	}
}

func TestNewNormalizesNestedAlibabaModelAndMapsThinking(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		effort         string
		wantThinking   string
		wantBeta       bool
		forbidThinking bool
	}{
		{name: "none disables", effort: "none", wantThinking: `"thinking":{"type":"disabled"}`},
		{name: "high enables", effort: "high", wantThinking: `"thinking":{"type":"enabled"}`, wantBeta: true},
		{name: "absent unchanged", forbidThinking: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var body string
			var betaHeader string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				betaHeader = r.Header.Get("anthropic-beta")
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
					return
				}
				body = string(b)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(streamSSE))
			}))
			defer srv.Close()

			zero := 0
			be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: "env-key", HTTPClient: srv.Client(), SDKMaxRetries: &zero})
			call := aipapi.Call{
				Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("question")}}},
				Options:  aipapi.GenerationOptions{ReasoningEffort: tc.effort},
			}
			cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backend.ID, Model: "alibaba/qwen3.8-max-preview"}}
			stream, err := be.Open(context.Background(), call, cand)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := aipapi.Collect(context.Background(), stream); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body, `"model":"qwen3.8-max-preview"`) || strings.Contains(body, `"model":"alibaba/`) {
				t.Fatalf("model not normalized: %s", body)
			}
			if tc.forbidThinking {
				if strings.Contains(body, `"thinking"`) {
					t.Fatalf("thinking must remain absent: %s", body)
				}
			} else if !strings.Contains(body, tc.wantThinking) {
				t.Fatalf("thinking mismatch: %s", body)
			}
			const interleavedThinkingBeta = "interleaved-thinking-2025-05-14"
			if tc.wantBeta && betaHeader != interleavedThinkingBeta {
				t.Fatalf("anthropic-beta = %q, want %q", betaHeader, interleavedThinkingBeta)
			}
			if !tc.wantBeta && betaHeader != "" {
				t.Fatalf("anthropic-beta = %q, want empty", betaHeader)
			}
		})
	}
}

func TestNewAdvertisesReasoningCapability(t *testing.T) {
	t.Parallel()
	be := backend.New(backend.Config{BaseURL: "https://example.test", APIKey: "env-key"})
	if _, ok := be.Caps[aipapi.CapabilityReasoning]; !ok {
		t.Fatal("Alibaba Token Plan must advertise reasoning so effort survives negotiation")
	}
	if be.ResolveCaps == nil {
		t.Fatal("expected ResolveCaps")
	}
	caps := be.ResolveCaps(
		context.Background(),
		aipapi.Call{Options: aipapi.GenerationOptions{ReasoningEffort: "high"}},
		routing.AttemptCandidate{Primary: routing.Primary{Backend: backend.ID, Model: "qwen3.8-max-preview"}},
	)
	if _, ok := caps[aipapi.CapabilityReasoning]; !ok {
		t.Fatal("resolved Alibaba model caps must preserve reasoning")
	}
}

func TestNewForwardsThinkingEventsAsReasoning(t *testing.T) {
	t.Parallel()
	const reasoningSSE = "event: message_start\ndata: " +
		`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"qwen3.7-plus","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"Plan","signature":"sig"}}` + "\n\n" +
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" carefully"}}` + "\n\n" +
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"reasoning_delta","reasoning":" now"}}` + "\n\n" +
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n" +
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Answer"}}` + "\n\n" +
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":1}` + "\n\n" +
		"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(reasoningSSE))
	}))
	defer srv.Close()

	zero := 0
	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: "env-key", HTTPClient: srv.Client(), SDKMaxRetries: &zero})
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("question")}}}}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backend.ID, Model: "qwen3.7-plus"}}
	stream, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if got := collected.Reasoning.String(); got != "Plan carefully now" {
		t.Fatalf("reasoning = %q", got)
	}
	if got := collected.Text.String(); got != "Answer" {
		t.Fatalf("text = %q", got)
	}
}

func TestNewPreservesStructuredToolHistoryAndOmitsToolChoice(t *testing.T) {
	t.Parallel()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		body = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(streamSSE))
	}))
	defer srv.Close()

	zero := 0
	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: "env-key", HTTPClient: srv.Client(), SDKMaxRetries: &zero})
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "call_1", ToolName: "bash", Content: json.RawMessage(`{"command":"git status"}`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call_1", Text: "clean"}}},
		},
		Tools:      []aipapi.ToolDef{{Name: "bash", Description: "Run shell", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backend.ID, Model: "qwen3.7-plus"}}
	stream, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"role":"assistant"`) || !strings.Contains(body, `"type":"tool_use"`) || !strings.Contains(body, `"type":"tool_result"`) {
		t.Fatalf("structured tool history lost: %s", body)
	}
	if strings.Contains(body, `"tool_choice"`) {
		t.Fatalf("Token Plan rejects tool_choice: %s", body)
	}
}

func TestNewNormalizesNonSystemRolesToUser(t *testing.T) {
	t.Parallel()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		body = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(streamSSE))
	}))
	defer srv.Close()

	zero := 0
	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: "env-key", HTTPClient: srv.Client(), SDKMaxRetries: &zero})
	call := aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("rules")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("history")}},
		{Role: aipapi.Role("developer"), Parts: []aipapi.Part{aipapi.TextPart("extra")}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("question")}},
	}}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backend.ID, Model: "qwen3.7-plus"}}
	stream, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if strings.Count(body, `"role":"user"`) != 1 || strings.Contains(body, `"role":"assistant"`) || strings.Contains(body, `"role":"developer"`) {
		t.Fatalf("roles not normalized: %s", body)
	}
	if !strings.Contains(body, `"system":[{"text":"rules"`) {
		t.Fatalf("system missing: %s", body)
	}
}

func TestNewMergesMultipleToolResults(t *testing.T) {
	t.Parallel()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		body = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(streamSSE))
	}))
	defer srv.Close()

	zero := 0
	be := backend.New(backend.Config{BaseURL: srv.URL, APIKey: "env-key", HTTPClient: srv.Client(), SDKMaxRetries: &zero})
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("run commands")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartJSON, ToolCallID: "call_1", ToolName: "bash", Content: json.RawMessage(`{"command":"git status"}`)},
				{Kind: aipapi.PartJSON, ToolCallID: "call_2", ToolName: "bash", Content: json.RawMessage(`{"command":"git diff"}`)},
			}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call_1", Text: "clean"}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call_2", Text: "diff clean"}}},
		},
		Tools: []aipapi.ToolDef{{Name: "bash", Description: "Run shell", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backend.ID, Model: "qwen3.7-plus"}}
	stream, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"tool_use_id":"call_1"`) || !strings.Contains(body, `"tool_use_id":"call_2"`) {
		t.Fatalf("tool results lost: %s", body)
	}
}
