package openresponses

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestResponseResource_OfficialGolden(t *testing.T) {
	t.Parallel()
	resPath := filepath.Join("testdata", "official_examples", "ResponseResource.json")
	officialBytes, err := os.ReadFile(resPath)
	if err != nil {
		t.Fatalf("failed to read official ResponseResource.json: %v", err)
	}

	var officialWire WireResponseResource
	if err := json.Unmarshal(officialBytes, &officialWire); err != nil {
		t.Fatalf("failed to unmarshal official ResponseResource.json: %v", err)
	}

	now := time.Unix(1719900000, 0)
	completed := time.Unix(1719900002, 0)
	env := EnvelopeMetadata{
		ResponseID:  "resp_5a3e04d550c84a63a1d4fc4e3e206abb",
		CreatedAt:   now,
		CompletedAt: &completed,
		Model:       "gpt-4o-2024-06-13",
		Status:      "completed",
	}

	trajectory := []aipapi.Item{
		{
			ID:     "msg_f8cbe04b16844dbea2f8c9eb7a5a0c1b",
			Kind:   aipapi.ItemKindMessage,
			Status: aipapi.ItemStatusCompleted,
			Role:   aipapi.RoleAssistant,
			Content: []aipapi.ContentPart{
				{
					Kind: aipapi.ContentPartText,
					Text: "Here is an example of a response object in the specified format. Every required property is present and populated with readable, example values. Log probability details are minimized for readability.",
				},
			},
		},
	}

	usage := UsageStats{
		InputTokens:  25,
		OutputTokens: 42,
		TotalTokens:  67,
	}

	options := aipapi.GenerationOptions{}

	wireRes, jsonBytes, err := BuildResponseResource(env, trajectory, usage, options, nil)
	if err != nil {
		t.Fatalf("BuildResponseResource failed: %v", err)
	}

	if wireRes.ID != env.ResponseID {
		t.Fatalf("expected ID %q, got %q", env.ResponseID, wireRes.ID)
	}
	if wireRes.Object != "response" {
		t.Fatalf("expected object response, got %q", wireRes.Object)
	}

	var decodedMap map[string]any
	if err := json.Unmarshal(jsonBytes, &decodedMap); err != nil {
		t.Fatalf("failed to unmarshal built JSON: %v", err)
	}

	// Verify required presence fields exist in raw JSON map even if null
	requiredKeys := []string{
		"id", "object", "created_at", "status", "completed_at", "model", "output",
		"parallel_tool_calls", "reasoning", "store", "background", "temperature",
		"text", "tool_choice", "tools", "top_p", "truncation", "usage", "metadata",
		"service_tier", "max_output_tokens", "max_tool_calls", "instructions",
		"previous_response_id", "error", "incomplete_details", "safety_identifier",
		"prompt_cache_key", "prompt_cache_retention",
	}

	for _, k := range requiredKeys {
		if _, exists := decodedMap[k]; !exists {
			t.Errorf("required presence key %q missing from built ResponseResource JSON", k)
		}
	}
}

func TestResponseResource_RequiredPresence(t *testing.T) {
	t.Parallel()
	env := EnvelopeMetadata{
		ResponseID: "resp_123",
		CreatedAt:  time.Now(),
		Model:      "gpt-4o",
		Status:     "completed",
	}

	_, jsonBytes, err := BuildResponseResource(env, nil, UsageStats{}, aipapi.GenerationOptions{}, nil)
	if err != nil {
		t.Fatalf("BuildResponseResource failed: %v", err)
	}

	var rawMap map[string]any
	if err := json.Unmarshal(jsonBytes, &rawMap); err != nil {
		t.Fatalf("failed to unmarshal built response JSON: %v", err)
	}

	// instructions and reasoning must be null when absent
	if val, ok := rawMap["instructions"]; !ok || val != nil {
		t.Errorf("instructions must be present and null, got %v", val)
	}
	if val, ok := rawMap["reasoning"]; !ok || val != nil {
		t.Errorf("reasoning must be present and null, got %v", val)
	}
	// tools must be [] when absent
	if val, ok := rawMap["tools"]; !ok || val == nil {
		t.Errorf("tools must be present and array, got %v", val)
	}
}

// TestResponseResource_20260424RequiredFidelity locks the pinned-profile
// required presence the official 2026-04-24 responseResourceSchema demands:
// the sampling controls presence_penalty/frequency_penalty/top_logprobs, the
// text.format object, and the usage detail counters (even at zero).
func TestResponseResource_20260424RequiredFidelity(t *testing.T) {
	t.Parallel()
	env := EnvelopeMetadata{
		ResponseID: "resp_fid",
		CreatedAt:  time.Unix(1719900000, 0),
		Model:      "gpt-4o-mini",
		Status:     "completed",
	}
	_, jsonBytes, err := BuildResponseResource(env, nil, UsageStats{}, aipapi.GenerationOptions{}, nil)
	if err != nil {
		t.Fatalf("BuildResponseResource failed: %v", err)
	}
	var rawMap map[string]any
	if err := json.Unmarshal(jsonBytes, &rawMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, key := range []string{"presence_penalty", "frequency_penalty", "top_logprobs"} {
		val, ok := rawMap[key]
		if !ok {
			t.Errorf("required key %q missing", key)
			continue
		}
		if n, ok := val.(float64); !ok || n != 0 {
			t.Errorf("%s = %v (%T), want 0", key, val, val)
		}
	}

	text, ok := rawMap["text"].(map[string]any)
	if !ok {
		t.Fatalf("text field = %v (%T), want object", rawMap["text"], rawMap["text"])
	}
	format, ok := text["format"].(map[string]any)
	if !ok {
		t.Fatalf("text.format missing: %v", text)
	}
	if ft, ok := format["type"].(string); !ok || ft != "text" {
		t.Fatalf("text.format.type = %v, want text", format["type"])
	}

	usage, ok := rawMap["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage = %v (%T), want object", rawMap["usage"], rawMap["usage"])
	}
	inDetail, ok := usage["input_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("usage.input_tokens_details missing: %v", usage)
	}
	if cached, ok := inDetail["cached_tokens"].(float64); !ok || cached != 0 {
		t.Errorf("usage.input_tokens_details.cached_tokens = %v, want 0", inDetail["cached_tokens"])
	}
	outDetail, ok := usage["output_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("usage.output_tokens_details missing: %v", usage)
	}
	if rt, ok := outDetail["reasoning_tokens"].(float64); !ok || rt != 0 {
		t.Errorf("usage.output_tokens_details.reasoning_tokens = %v, want 0", outDetail["reasoning_tokens"])
	}
}

// TestResponseResource_OutputTextAnnotationsRequired locks that assistant
// output_text content parts carry the annotations array the pinned profile's
// outputTextContentSchema requires in a response resource.
func TestResponseResource_OutputTextAnnotationsRequired(t *testing.T) {
	t.Parallel()
	env := EnvelopeMetadata{
		ResponseID: "resp_ann",
		CreatedAt:  time.Unix(1719900000, 0),
		Model:      "gpt-4o-mini",
		Status:     "completed",
	}
	trajectory := []aipapi.Item{
		{
			ID:     "msg_1",
			Kind:   aipapi.ItemKindMessage,
			Status: aipapi.ItemStatusCompleted,
			Role:   aipapi.RoleAssistant,
			Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "hello"},
			},
		},
	}
	wireRes, _, err := BuildResponseResource(env, trajectory, UsageStats{}, aipapi.GenerationOptions{}, nil)
	if err != nil {
		t.Fatalf("BuildResponseResource failed: %v", err)
	}
	if len(wireRes.Output) != 1 {
		t.Fatalf("output items=%d", len(wireRes.Output))
	}
	var parts []WireContentPart
	if err := json.Unmarshal(wireRes.Output[0].Content, &parts); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("content parts=%d", len(parts))
	}
	if string(parts[0].Annotations) != `[]` {
		t.Errorf("output_text annotations = %s, want []", string(parts[0].Annotations))
	}
}

// TestResponseResource_ToolCallArgumentsWireString locks that a function_call
// output item's arguments field is a JSON string on the pinned profile, not a
// raw JSON object.
func TestResponseResource_ToolCallArgumentsWireString(t *testing.T) {
	t.Parallel()
	env := EnvelopeMetadata{
		ResponseID: "resp_fc",
		CreatedAt:  time.Unix(1719900000, 0),
		Model:      "gpt-4o-mini",
		Status:     "completed",
	}
	trajectory := []aipapi.Item{
		{
			ID:     "fc_1",
			Kind:   aipapi.ItemKindToolCall,
			Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{
				CallID:    "call_1",
				Name:      "get_weather",
				Arguments: []byte(`{"location":"San Francisco, CA"}`),
			},
		},
	}
	wireRes, _, err := BuildResponseResource(env, trajectory, UsageStats{}, aipapi.GenerationOptions{}, nil)
	if err != nil {
		t.Fatalf("BuildResponseResource failed: %v", err)
	}
	args := wireRes.Output[0].Arguments
	if len(args) == 0 {
		t.Fatal("function_call arguments missing")
	}
	var s string
	if err := json.Unmarshal(args, &s); err != nil {
		t.Fatalf("arguments must be a JSON string, got %s", string(args))
	}
	if s != `{"location":"San Francisco, CA"}` {
		t.Errorf("arguments string = %q", s)
	}
}

func TestResponseResource_TrajectoryOrdering(t *testing.T) {
	t.Parallel()
	env := EnvelopeMetadata{
		ResponseID: "resp_seq",
		CreatedAt:  time.Now(),
		Model:      "gpt-4o",
		Status:     "completed",
	}

	trajectory := []aipapi.Item{
		{
			ID:     "msg_1",
			Kind:   aipapi.ItemKindMessage,
			Status: aipapi.ItemStatusCompleted,
			Role:   aipapi.RoleUser,
			Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "Question"},
			},
		},
		{
			ID:     "call_1",
			Kind:   aipapi.ItemKindToolCall,
			Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{
				CallID:    "call_1",
				Name:      "search",
				Arguments: []byte(`{"q":"test"}`),
			},
		},
	}

	wireRes, _, err := BuildResponseResource(env, trajectory, UsageStats{}, aipapi.GenerationOptions{}, nil)
	if err != nil {
		t.Fatalf("BuildResponseResource failed: %v", err)
	}

	if len(wireRes.Output) != 2 {
		t.Fatalf("expected 2 output items, got %d", len(wireRes.Output))
	}

	if wireRes.Output[0].Type != "message" || wireRes.Output[1].Type != "function_call" {
		t.Fatalf("output order mismatch: %+v", wireRes.Output)
	}
}

func TestResponseResource_MissingCompletionUsesBuildTime(t *testing.T) {
	t.Parallel()
	created := time.Unix(1700000000, 0)
	env := EnvelopeMetadata{
		ResponseID: "resp_ts",
		CreatedAt:  created,
		Model:      "gpt-4o",
		Status:     "completed",
	}
	before := time.Now().Unix()
	wireRes, _, err := BuildResponseResource(env, nil, UsageStats{}, aipapi.GenerationOptions{}, nil)
	after := time.Now().Unix()
	if err != nil {
		t.Fatalf("BuildResponseResource failed: %v", err)
	}

	if wireRes.CompletedAt == nil {
		t.Fatal("expected CompletedAt when status is completed")
	}
	if got := *wireRes.CompletedAt; got < before || got > after {
		t.Fatalf("expected CompletedAt between build bounds [%d,%d], got %d", before, after, got)
	}
	if *wireRes.CompletedAt == created.Unix() {
		t.Fatalf("missing CompletedAt must not copy CreatedAt: %d", *wireRes.CompletedAt)
	}
}

func TestResponseResource_BuildFailures(t *testing.T) {
	t.Parallel()
	env := EnvelopeMetadata{
		ResponseID: "", // empty ID
		CreatedAt:  time.Now(),
		Model:      "gpt-4o",
	}

	_, _, err := BuildResponseResource(env, nil, UsageStats{}, aipapi.GenerationOptions{}, nil)
	if err == nil {
		t.Fatalf("expected error for empty response ID, got nil")
	}
}
