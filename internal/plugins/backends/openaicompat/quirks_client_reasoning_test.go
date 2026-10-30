package openaicompat

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Claim: stripped callA,reasoning,callB,outA,outB opens as
// callA,callB,outA,outB with no refusal (req 5.1). Still-illegal remainders
// keep existing refusal, proven by this package's unchanged quirk refusal
// tests (req 5.2, 5.3: never reorder, refuse instead).

// strippedToolLoopCall is the canonical callA,reasoning,callB,outA,outB shape
// after the per-attempt log_skip strip: callA,callB,outA,outB.
func strippedToolLoopCall() aipapi.Call {
	withReasoning := aipapi.Call{
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
	if !aipapi.StripReasoningForAttempt(&withReasoning) {
		panic("fixture must contain strippable reasoning")
	}
	return withReasoning
}

func responsesInputOrder(t *testing.T, body []byte) []string {
	t.Helper()
	var payload struct {
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal wire body: %v body=%s", err, body)
	}
	order := make([]string, 0, len(payload.Input))
	for _, it := range payload.Input {
		order = append(order, it.Type+":"+it.CallID)
	}
	return order
}

func TestQuirks_ResponsesStrippedToolLoopPasses(t *testing.T) {
	t.Parallel()
	call := strippedToolLoopCall()
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	body := quirkCaptureResponsesBody(t, call, cand, CompatibleQuirks{ToolOutputAdjacency: true})
	order := responsesInputOrder(t, body)
	want := []string{
		// The plain user message marshals without a type discriminator.
		":", "function_call:cA", "function_call:cB",
		"function_call_output:cA", "function_call_output:cB",
	}
	if !slices.Equal(order, want) {
		t.Fatalf("stripped wire order = %v, want %v body=%s", order, want, body)
	}
}
