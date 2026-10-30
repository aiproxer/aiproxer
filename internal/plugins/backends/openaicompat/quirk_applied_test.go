package openaicompat

// Claim table for strict-provider quirk application counting.
//
// | Test | Claim | Source | Mutant -> result |
// | --- | --- | --- | --- |
// | TestQuirkApplied_ChatInjectionObservedOnce | enabled quirk that injects reasoning_content on >=1 message reports exactly one openai.chat.reasoning_content_on_tool_calls observation | task deliverable 2 (count when the quirk rewrites the wire) | delete the observe call in OpenChat -> FAIL |
// | TestQuirkApplied_ChatEnabledWithoutRewriteSilent | enabled quirk that injects nothing (tool-less request; reasoning already present) reports nothing | task deliverable 2 (NOT merely when enabled) | observe whenever enabled -> FAIL |
// | TestQuirkApplied_ChatDisabledSilent | quirk off reports nothing even when the request is injectable | CompatibleQuirks zero value = unchanged behaviour | observe unconditionally -> FAIL |
// | TestQuirkApplied_ResponsesReorderObservedOnce | trapped history that is actually reordered reports exactly one openai.responses.tool_output_adjacency observation | task deliverable 2 (adjacency normalization applied) | delete the observe call in OpenResponses -> FAIL |
// | TestQuirkApplied_ResponsesHealthySilent | healthy history left byte-identical reports nothing | task deliverable 2 (NOT merely when enabled) | observe whenever enabled -> FAIL |
// | TestQuirkApplied_ResponsesRefusalNotCounted | adjacency refusal surfaces ErrResponsesAdjacencyRefused without touching the wire and reports nothing | task deliverable 2 (refusals already surface as terminal errors with telemetry; avoid double counting) | observe on the refusal path -> FAIL |

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicred"
	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type recordingQuirkObserver struct {
	mu    sync.Mutex
	calls []providerprofiles.QuirkID
}

func (o *recordingQuirkObserver) ObserveQuirkApplied(quirk providerprofiles.QuirkID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, quirk)
}

func (o *recordingQuirkObserver) snapshot() []providerprofiles.QuirkID {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]providerprofiles.QuirkID(nil), o.calls...)
}

// quirkAppliedInvoke drives OpenChat or OpenResponses (non-streaming) against a
// stub upstream, returning the captured wire body. A refusal returns the error
// with an empty body and fails the test if the stub was hit.
func quirkAppliedInvoke(t *testing.T, flavor Flavor, call aipapi.Call, cand routing.AttemptCandidate, q CompatibleQuirks, obs QuirkObserver) ([]byte, error) {
	t.Helper()
	var captured []byte
	var stubBody string
	switch flavor {
	case FlavorResponses:
		stubBody = `{"id":"r","object":"response","created_at":1,"status":"completed","model":"m","output":[{"type":"message","id":"m1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	default:
		stubBody = `{"id":"q","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = append([]byte(nil), b...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, stubBody)
	}))
	t.Cleanup(srv.Close)
	cli := openaicred.NewClient(srv.URL, "sk-test", srv.Client(), new(int))
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	req := InvokeRequest{ProviderID: "q", Call: call, Candidate: cand, Quirks: q, QuirkObserver: obs}
	var es aipapi.ManagedEventStream
	var err error
	if flavor == FlavorResponses {
		es, err = OpenResponses(context.Background(), cli, req)
	} else {
		es, err = OpenChat(context.Background(), cli, req)
	}
	if err != nil {
		if len(captured) != 0 {
			t.Fatalf("failed request must not reach the wire, body=%s", captured)
		}
		return nil, err
	}
	drainQuirkEvents(t, es)
	if len(captured) == 0 {
		t.Fatal("no body captured")
	}
	return captured, nil
}

func TestQuirkApplied_ChatInjectionObservedOnce(t *testing.T) {
	t.Parallel()
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	obs := &recordingQuirkObserver{}
	if _, err := quirkAppliedInvoke(t, FlavorChat, quirkChatToolCall(), cand, CompatibleQuirks{ReasoningContentOnToolCalls: true}, obs); err != nil {
		t.Fatalf("OpenChat: %v", err)
	}
	if got := obs.snapshot(); len(got) != 1 || got[0] != providerprofiles.QuirkOpenAIChatReasoningContent {
		t.Fatalf("want exactly one %q observation, got %v", providerprofiles.QuirkOpenAIChatReasoningContent, got)
	}
}

func TestQuirkApplied_ChatEnabledWithoutRewriteSilent(t *testing.T) {
	t.Parallel()
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	// Tool-less request: the quirk early-returns, the wire is untouched.
	toolLess := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("just text")}},
		},
	}
	obs := &recordingQuirkObserver{}
	if _, err := quirkAppliedInvoke(t, FlavorChat, toolLess, cand, CompatibleQuirks{ReasoningContentOnToolCalls: true}, obs); err != nil {
		t.Fatalf("OpenChat: %v", err)
	}
	if got := obs.snapshot(); len(got) != 0 {
		t.Fatalf("tool-less request injects nothing, want silence, got %v", got)
	}
	// Reasoning already present: nothing injected, wire untouched.
	preserved := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "real-thought"}},
					{Kind: aipapi.PartJSON, ToolCallID: "call_1", ToolName: "w", Content: []byte(`{"id":"call_1","type":"function","function":{"name":"w","arguments":"{}"}}`)},
				},
			},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	obs2 := &recordingQuirkObserver{}
	if _, err := quirkAppliedInvoke(t, FlavorChat, preserved, cand, CompatibleQuirks{ReasoningContentOnToolCalls: true}, obs2); err != nil {
		t.Fatalf("OpenChat: %v", err)
	}
	if got := obs2.snapshot(); len(got) != 0 {
		t.Fatalf("present reasoning injects nothing, want silence, got %v", got)
	}
}

func TestQuirkApplied_ChatDisabledSilent(t *testing.T) {
	t.Parallel()
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	obs := &recordingQuirkObserver{}
	if _, err := quirkAppliedInvoke(t, FlavorChat, quirkChatToolCall(), cand, CompatibleQuirks{}, obs); err != nil {
		t.Fatalf("OpenChat: %v", err)
	}
	if got := obs.snapshot(); len(got) != 0 {
		t.Fatalf("quirk off must stay silent, got %v", got)
	}
}

func TestQuirkApplied_ResponsesReorderObservedOnce(t *testing.T) {
	t.Parallel()
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	obs := &recordingQuirkObserver{}
	if _, err := quirkAppliedInvoke(t, FlavorResponses, quirkResponsesTrapped(), cand, CompatibleQuirks{ToolOutputAdjacency: true}, obs); err != nil {
		t.Fatalf("OpenResponses: %v", err)
	}
	if got := obs.snapshot(); len(got) != 1 || got[0] != providerprofiles.QuirkOpenAIResponsesToolAdjacency {
		t.Fatalf("want exactly one %q observation, got %v", providerprofiles.QuirkOpenAIResponsesToolAdjacency, got)
	}
}

func TestQuirkApplied_ResponsesHealthySilent(t *testing.T) {
	t.Parallel()
	healthy := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("go")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "c1", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"c1","name":"w","arguments":"{}"}`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "c1", Content: []byte(`"ok"`)}}},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	obs := &recordingQuirkObserver{}
	if _, err := quirkAppliedInvoke(t, FlavorResponses, healthy, cand, CompatibleQuirks{ToolOutputAdjacency: true}, obs); err != nil {
		t.Fatalf("OpenResponses: %v", err)
	}
	if got := obs.snapshot(); len(got) != 0 {
		t.Fatalf("healthy history is byte-identical, want silence, got %v", got)
	}
}

func TestQuirkApplied_ResponsesRefusalNotCounted(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("start")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "cA", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cA","name":"w","arguments":"{}"}`)}}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Opaque: json.RawMessage(`{"id":"rs_1","summary":[]}`)}},
				{Kind: aipapi.PartJSON, ToolCallID: "cB", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cB","name":"w","arguments":"{}"}`)},
			}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cA", Content: []byte(`"ok-a"`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cB", Content: []byte(`"ok-b"`)}}},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	obs := &recordingQuirkObserver{}
	_, err := quirkAppliedInvoke(t, FlavorResponses, call, cand, CompatibleQuirks{ToolOutputAdjacency: true}, obs)
	if !errors.Is(err, ErrResponsesAdjacencyRefused) {
		t.Fatalf("must surface the classified refusal, got %v", err)
	}
	if got := obs.snapshot(); len(got) != 0 {
		t.Fatalf("refusals already surface as terminal errors with telemetry, want silence, got %v", got)
	}
}
