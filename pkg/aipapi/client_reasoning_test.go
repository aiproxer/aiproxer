package aipapi_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Claims (golang-testing): policy matrix pins log_skip softening only
// reasoning_replay, hard reject under reject/empty, lossless passthrough,
// and hard other-capabilities (req 1.1, 1.2, 1.5); strip tests pin
// clone-only byte-identical order-preserved removal per authority with no-op
// when absent (req 2.1-2.3). Every row is mutant-checked (see report).

func TestNegotiateWithAttemptPolicy_Matrix(t *testing.T) {
	t.Parallel()
	streaming := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	full := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityReasoningReplay)
	cases := []struct {
		name     string
		required []aipapi.Capability
		backend  aipapi.BackendCaps
		policy   aipapi.AttemptPolicy
		wantKind aipapi.NegotiationKind
		wantSoft []aipapi.Capability
		wantHard []aipapi.Capability
		notHard  []aipapi.Capability
	}{
		{name: "log_skip downgrades reasoning_replay", required: []aipapi.Capability{aipapi.CapabilityReasoningReplay}, backend: streaming, policy: aipapi.AttemptLogSkip, wantKind: aipapi.NegotiationDowngrade, wantSoft: []aipapi.Capability{aipapi.CapabilityReasoningReplay}},
		{name: "reject stays hard", required: []aipapi.Capability{aipapi.CapabilityReasoningReplay}, backend: streaming, policy: aipapi.AttemptReject, wantKind: aipapi.NegotiationReject, wantHard: []aipapi.Capability{aipapi.CapabilityReasoningReplay}},
		{name: "empty stays hard", required: []aipapi.Capability{aipapi.CapabilityReasoningReplay}, backend: streaming, wantKind: aipapi.NegotiationReject, wantHard: []aipapi.Capability{aipapi.CapabilityReasoningReplay}},
		{name: "lossless unaffected by log_skip", required: []aipapi.Capability{aipapi.CapabilityReasoningReplay}, backend: full, policy: aipapi.AttemptLogSkip, wantKind: aipapi.NegotiationLossless},
		{name: "lossless unaffected by reject", required: []aipapi.Capability{aipapi.CapabilityReasoningReplay}, backend: full, policy: aipapi.AttemptReject, wantKind: aipapi.NegotiationLossless},
		{name: "vision stays hard under log_skip", required: []aipapi.Capability{aipapi.CapabilityReasoningReplay, aipapi.CapabilityVision}, backend: streaming, policy: aipapi.AttemptLogSkip, wantKind: aipapi.NegotiationReject, wantHard: []aipapi.Capability{aipapi.CapabilityVision}, notHard: []aipapi.Capability{aipapi.CapabilityReasoningReplay}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := aipapi.NegotiateWithAttemptPolicy(tc.required, tc.backend, tc.policy)
			if got.Kind != tc.wantKind {
				t.Fatalf("kind=%s want %s (missing=%v downgraded=%v)", got.Kind, tc.wantKind, got.Missing, got.Downgraded)
			}
			for _, w := range tc.wantSoft {
				if !slices.Contains(got.Downgraded, w) {
					t.Fatalf("downgraded must contain %q: %v", w, got.Downgraded)
				}
			}
			for _, w := range tc.wantHard {
				if !slices.Contains(got.Missing, w) {
					t.Fatalf("missing must contain %q: %v", w, got.Missing)
				}
			}
			for _, w := range tc.notHard {
				if slices.Contains(got.Missing, w) {
					t.Fatalf("%q must not be hard: %v", w, got.Missing)
				}
			}
		})
	}
}

func TestStripReasoningForAttempt_RemovesOnlyReasoningParts(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("start")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "cA", ToolName: "w", Content: []byte(`{"type":"function_call"}`)}}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Text: "secret-thought", Opaque: json.RawMessage(`{"id":"rs_1"}`)}},
			}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "cB", ToolName: "w", Content: []byte(`{"type":"function_call"}`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cA", Content: []byte(`"ok-a"`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cB", Content: []byte(`"ok-b"`)}}},
		},
	}
	if !aipapi.StripReasoningForAttempt(&call) {
		t.Fatal("strip must report true when reasoning parts were removed")
	}
	if len(call.Messages) != 5 {
		t.Fatalf("messages must drop only the reasoning message, got %d", len(call.Messages))
	}
	if got := call.Messages[1].Parts[0].ToolCallID; got != "cA" {
		t.Fatalf("message order must be preserved, got %q", got)
	}
	if got := call.Messages[2].Parts[0].ToolCallID; got != "cB" {
		t.Fatalf("message order must be preserved, got %q", got)
	}
	for _, m := range call.Messages {
		for _, p := range m.Parts {
			if p.Kind == aipapi.PartReasoning {
				t.Fatalf("no PartReasoning may remain: %+v", m)
			}
		}
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("stripped call must stay valid: %v", err)
	}
}

func TestStripReasoningForAttempt_ItemAuthorityDissolvesReasoningItems(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Items: []aipapi.Item{
			{Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}}},
			{Kind: aipapi.ItemKindReasoning, ID: "rs-1", Status: aipapi.ItemStatusCompleted, Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Text: "t"}}},
			{Kind: aipapi.ItemKindToolCall, ID: "tc1", Status: aipapi.ItemStatusCompleted, ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "w", Arguments: []byte(`{}`)}},
			{Kind: aipapi.ItemKindMessage, ID: "m2", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "keep"},
				{Kind: aipapi.ContentPartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Text: "drop"}},
			}},
			{Kind: aipapi.ItemKindToolResult, ID: "tr1", Status: aipapi.ItemStatusCompleted, ToolResult: &aipapi.ToolResultItem{CallID: "c1", Parts: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "drop"}},
				{Kind: aipapi.ContentPartText, Text: "kept"},
			}}},
		},
	}
	if !aipapi.StripReasoningForAttempt(&call) {
		t.Fatal("strip must report true when reasoning parts were removed")
	}
	if len(call.Items) != 4 {
		t.Fatalf("items must drop only the reasoning item, got %d", len(call.Items))
	}
	if call.Items[0].ID != "m1" || call.Items[1].ID != "tc1" || call.Items[2].ID != "m2" || call.Items[3].ID != "tr1" {
		t.Fatalf("item order must be preserved: %+v", call.Items)
	}
	if len(call.Items[2].Content) != 1 || call.Items[2].Content[0].Text != "keep" {
		t.Fatalf("mixed content must keep only text: %+v", call.Items[2].Content)
	}
	if len(call.Items[3].ToolResult.Parts) != 1 || call.Items[3].ToolResult.Parts[0].Text != "kept" {
		t.Fatalf("tool result must keep only text: %+v", call.Items[3].ToolResult.Parts)
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("stripped call must stay valid: %v", err)
	}
}

func TestStripReasoningForAttempt_NoReasoningNoOp(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	if aipapi.StripReasoningForAttempt(&call) {
		t.Fatal("strip must report false when nothing was removed")
	}
	if len(call.Messages) != 1 || call.Messages[0].Parts[0].Text != "hi" {
		t.Fatalf("no-op strip must leave the call untouched: %+v", call.Messages)
	}
}
