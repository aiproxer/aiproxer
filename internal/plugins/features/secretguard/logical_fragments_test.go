package secretguard

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func requireValidCanonicalCall(t *testing.T, call *aipapi.Call) {
	t.Helper()
	if err := call.Validate(); err != nil {
		t.Fatalf("canonical fixture is invalid: %v", err)
	}
}

func TestWalkLogicalFragments_PreservesCanonicalContextAndLocations(t *testing.T) {
	t.Parallel()

	toolSchema := json.RawMessage(`{"type":"object","properties":{"api_key":{"type":"string"}}}`)
	toolResult := json.RawMessage(`{"api_key":"opaque-result"}`)
	call := &aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart("user prompt text"),
				{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"api_key":"opaque-prompt"}`)},
			},
		}, {
			Role: aipapi.RoleTool,
			Parts: []aipapi.Part{
				{Kind: aipapi.PartToolResult, ToolCallID: "call-1", Text: "tool result text", Content: toolResult},
			},
		}},
		Tools: []aipapi.ToolDef{{
			Name:        "lookup",
			Description: "lookup description",
			Parameters:  toolSchema,
		}},
	}
	requireValidCanonicalCall(t, call)

	want := []struct {
		location string
		kind     FragmentKind
		raw      string
	}{
		{location: "messages[0].parts[0]", kind: FragmentText, raw: "user prompt text"},
		{location: "messages[0].parts[1]", kind: FragmentJSON, raw: `{"api_key":"opaque-prompt"}`},
		{location: "messages[1].parts[0]", kind: FragmentText, raw: "tool result text"},
		{location: "messages[1].parts[0]", kind: FragmentJSON, raw: `{"api_key":"opaque-result"}`},
	}

	budget := newScanBudget(1024)
	fragments := walkLogicalFragments(call, budget)
	if len(fragments) != len(want) {
		t.Fatalf("fragment count: got %d want %d: %#v", len(fragments), len(want), fragments)
	}
	for i, fragment := range fragments {
		if got := fragment.Location; got != want[i].location {
			t.Errorf("fragment %d location: got %q want %q", i, got, want[i].location)
		}
		if got := fragment.Kind; got != want[i].kind {
			t.Errorf("fragment %d kind: got %v want %v", i, got, want[i].kind)
		}
		if got := fragment.textValue(); got != want[i].raw {
			t.Errorf("fragment %d raw: got %q want %q", i, got, want[i].raw)
		}
	}

	// Equal location/text values are still separate logical occurrences because
	// the text and raw JSON fields belong to different request content fields.
	if got := fragments[2].Location; got != fragments[3].Location {
		t.Fatalf("tool-result fields must retain their canonical location: %q vs %q", got, fragments[3].Location)
	}
	if reflect.DeepEqual(fragments[2].Raw, fragments[3].Raw) {
		t.Fatal("text and JSON tool-result fields must remain distinct fragments")
	}
}

func TestWalkLogicalFragments_MessageAuthorityRestrictsProvenanceAndBudget(t *testing.T) {
	t.Parallel()

	call := &aipapi.Call{
		Instructions: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "instruction secret"}},
		}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "system secret"}}},
			{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"token":"developer secret"}`)}}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "assistant secret"}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{
				aipapi.TextPart("user prompt"),
				{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"api_key":"user json"}`)},
			}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{
				aipapi.TextPart("tool output"),
				{Kind: aipapi.PartToolResult, ToolCallID: "call-1", Text: "tool result text", Content: json.RawMessage(`{"token":"tool result json"}`)},
			}},
		},
		Tools: []aipapi.ToolDef{{
			Name:        "tool secret",
			Description: "tool description secret",
			Parameters:  json.RawMessage(`{"api_key":"tool schema secret"}`),
		}},
	}
	requireValidCanonicalCall(t, call)

	budget := newScanBudget(len("user prompt") + len(`{"api_key":"user json"}`) + len("tool output") + len("tool result text") + len(`{"token":"tool result json"}`))
	fragments := walkLogicalFragments(call, budget)
	want := []string{
		"user prompt",
		`{"api_key":"user json"}`,
		"tool output",
		"tool result text",
		`{"token":"tool result json"}`,
	}
	if len(fragments) != len(want) {
		t.Fatalf("fragment count: got %d want %d: %#v", len(fragments), len(want), fragments)
	}
	for i, fragment := range fragments {
		if got := fragment.textValue(); got != want[i] {
			t.Errorf("fragment %d: got %q want %q", i, got, want[i])
		}
	}
	if budget.limitHit {
		t.Fatal("eligible fragments should fit the shared budget")
	}
	if got, wantBytes := budget.used, budget.maxBytes; got != wantBytes {
		t.Fatalf("budget used: got %d want %d", got, wantBytes)
	}
}

func TestWalkLogicalFragments_ItemAuthoritySkipsUnknownRoleRobustness(t *testing.T) {
	t.Parallel()

	// Unknown item roles are rejected by the canonical item schema. Keep this
	// direct walker robustness fixture separate from validated SDK cases.
	call := &aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, Role: aipapi.Role("unknown"), Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "unknown secret"}}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "user prompt"}}},
	}}
	fragments := walkLogicalFragments(call, newScanBudget(1024))
	if len(fragments) != 1 || fragments[0].Location != "items[1].content[0]" || fragments[0].textValue() != "user prompt" {
		t.Fatalf("unknown role traversal: got %#v", fragments)
	}
}

func TestWalkLogicalFragments_ItemAuthoritySkipsNonTextualContentParts(t *testing.T) {
	t.Parallel()

	// Non-textual content parts (media refs and model-produced reasoning,
	// refusal, summary, annotation and reference payloads) are excluded from
	// scanning and from scan-budget accounting; only the eligible text part is
	// emitted.
	call := &aipapi.Call{Items: []aipapi.Item{{
		Kind: aipapi.ItemKindMessage,
		Role: aipapi.RoleUser,
		Content: []aipapi.ContentPart{
			{Kind: aipapi.ContentPartText, Text: "eligible prompt"},
			{Kind: aipapi.ContentPartImageRef, ImageRef: "img://opaque", ImageMIME: "image/png"},
			{Kind: aipapi.ContentPartFileRef, FileRef: "file://opaque", FileMIME: "text/plain", FileName: "notes.txt"},
			{Kind: aipapi.ContentPartVideoRef, VideoRef: "video://opaque", VideoMIME: "video/mp4"},
			{Kind: aipapi.ContentPartRefusal, Refusal: "refusal body"},
			{Kind: aipapi.ContentPartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "reasoning body"}},
			{Kind: aipapi.ContentPartSummary, Summary: "summary body"},
			{Kind: aipapi.ContentPartAnnotation, Annotation: &aipapi.AnnotationPart{Type: "note", Data: json.RawMessage(`{"note":true}`)}},
			{Kind: aipapi.ContentPartAssistantRef, AssistantRef: "assistant-ref"},
		},
	}}}
	requireValidCanonicalCall(t, call)

	budget := newScanBudget(1024)
	fragments := walkLogicalFragments(call, budget)
	if len(fragments) != 1 {
		t.Fatalf("fragment count: got %d want 1: %#v", len(fragments), fragments)
	}
	if got := fragments[0].Location; got != "items[0].content[0]" {
		t.Fatalf("fragment location: got %q want %q", got, "items[0].content[0]")
	}
	if got := fragments[0].textValue(); got != "eligible prompt" {
		t.Fatalf("fragment text: got %q want %q", got, "eligible prompt")
	}
	if got, want := budget.used, len("eligible prompt"); got != want {
		t.Fatalf("budget used: got %d want %d", got, want)
	}
}

func TestWalkLogicalFragments_ItemAuthorityRestrictsProvenanceAndBudget(t *testing.T) {
	t.Parallel()

	call := &aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "assistant history"}}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{
			{Kind: aipapi.ContentPartText, Text: "item user prompt"},
			{Kind: aipapi.ContentPartJSON, Text: `{"api_key":"item prompt json"}`},
		}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleTool, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "item tool message"}}},
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"token":"model argument"}`)}},
		{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{
			CallID: "call-1", Name: "lookup", Output: `{"token":"item tool output"}`,
		}},
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "call-2", Name: "lookup", Arguments: json.RawMessage(`{"token":"model argument 2"}`)}},
		{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{
			CallID: "call-2", Name: "lookup",
			Parts: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "item tool result text"},
				{Kind: aipapi.ContentPartJSON, Text: `{"api_key":"item result json"}`},
			},
		}},
		{Kind: aipapi.ItemKindItemReference, Reference: &aipapi.ItemReference{ID: "prior"}},
	}}
	requireValidCanonicalCall(t, call)

	want := []string{
		"item user prompt",
		`{"api_key":"item prompt json"}`,
		"item tool message",
		`{"token":"item tool output"}`,
		"item tool result text",
		`{"api_key":"item result json"}`,
	}
	budget := newScanBudget(len("item user prompt") + len(`{"api_key":"item prompt json"}`) + len("item tool message") + len(`{"token":"item tool output"}`) + len("item tool result text") + len(`{"api_key":"item result json"}`))
	fragments := walkLogicalFragments(call, budget)
	if len(fragments) != len(want) {
		t.Fatalf("fragment count: got %d want %d: %#v", len(fragments), len(want), fragments)
	}
	for i, fragment := range fragments {
		if got := fragment.textValue(); got != want[i] {
			t.Errorf("fragment %d: got %q want %q", i, got, want[i])
		}
	}
	if budget.limitHit {
		t.Fatal("eligible item fragments should fit the shared budget")
	}
	if got, wantBytes := budget.used, budget.maxBytes; got != wantBytes {
		t.Fatalf("budget used: got %d want %d", got, wantBytes)
	}
}

func TestWalkLogicalFragments_ExcludedOnlyContentDoesNotConsumeBudget(t *testing.T) {
	t.Parallel()

	call := &aipapi.Call{
		Instructions: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "instruction only"}}}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "system only"}}},
			{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"token":"developer only"}`)}}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "assistant only"}}},
		},
		Tools: []aipapi.ToolDef{{Name: "tool only", Description: "description only", Parameters: json.RawMessage(`{"token":"schema only"}`)}},
	}
	requireValidCanonicalCall(t, call)

	budget := newScanBudget(1)
	if fragments := walkLogicalFragments(call, budget); len(fragments) != 0 {
		t.Fatalf("excluded content produced fragments: %#v", fragments)
	}
	if budget.used != 0 {
		t.Fatalf("excluded content consumed budget: got %d", budget.used)
	}
	if budget.limitHit {
		t.Fatal("excluded content must not trigger the scan limit")
	}
}

func TestWalkLogicalFragments_TextUsesImmutableStringRepresentation(t *testing.T) {
	const content = "immutable text fragment"
	call := &aipapi.Call{Messages: []aipapi.Message{{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: content}},
	}}}

	fragments := walkLogicalFragments(call, newScanBudget(len(content)))
	if len(fragments) != 1 {
		t.Fatalf("fragments=%d, want 1", len(fragments))
	}
	fragment := fragments[0]
	if fragment.Kind != FragmentText {
		t.Fatalf("kind=%v, want text", fragment.Kind)
	}
	if fragment.Text != content {
		t.Fatalf("text=%q, want %q", fragment.Text, content)
	}
	if fragment.Raw != nil {
		t.Fatalf("text fragment retained a byte representation of length %d", len(fragment.Raw))
	}
}

func TestWalkLogicalFragments_ItemAuthorityCoversMessagesAndToolResults(t *testing.T) {
	t.Parallel()

	call := &aipapi.Call{Items: []aipapi.Item{
		{
			Kind: aipapi.ItemKindMessage,
			Role: aipapi.RoleUser,
			Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "item message text"},
				{Kind: aipapi.ContentPartJSON, Text: `{"credentials":{"token":"item-json"}}`},
			},
		},
		{
			Kind:     aipapi.ItemKindToolCall,
			ToolCall: &aipapi.ToolCallItem{CallID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"token":"item-arguments"}`)},
		},
		{
			Kind: aipapi.ItemKindToolResult,
			ToolResult: &aipapi.ToolResultItem{
				CallID: "call-1", Name: "lookup",
				Output: `{"token":"item-output"}`,
			},
		},
		{
			Kind:     aipapi.ItemKindToolCall,
			ToolCall: &aipapi.ToolCallItem{CallID: "call-2", Name: "lookup", Arguments: json.RawMessage(`{"token":"item-arguments-2"}`)},
		},
		{
			Kind: aipapi.ItemKindToolResult,
			ToolResult: &aipapi.ToolResultItem{
				CallID: "call-2", Name: "lookup",
				Parts: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "item result text"},
					{Kind: aipapi.ContentPartJSON, Text: `{"token":"item-result-json"}`},
				},
			},
		},
	}}
	requireValidCanonicalCall(t, call)
	want := []struct {
		location string
		kind     FragmentKind
		raw      string
	}{
		{location: "items[0].content[0]", kind: FragmentText, raw: "item message text"},
		{location: "items[0].content[1]", kind: FragmentJSON, raw: `{"credentials":{"token":"item-json"}}`},
		{location: "items[2].tool_result.output", kind: FragmentText, raw: `{"token":"item-output"}`},
		{location: "items[4].tool_result.parts[0]", kind: FragmentText, raw: "item result text"},
		{location: "items[4].tool_result.parts[1]", kind: FragmentJSON, raw: `{"token":"item-result-json"}`},
	}

	fragments := walkLogicalFragments(call, newScanBudget(1024))
	if len(fragments) != len(want) {
		t.Fatalf("item fragment count: got %d want %d", len(fragments), len(want))
	}
	for i, fragment := range fragments {
		if fragment.Location != want[i].location || fragment.Kind != want[i].kind || fragment.textValue() != want[i].raw {
			t.Fatalf("item fragment %d mismatch: got location=%q kind=%v raw_len=%d", i, fragment.Location, fragment.Kind, len(fragment.rawBytes()))
		}
	}

	fragments[0].setRaw([]byte("item message replacement"))
	fragments[1].setRaw([]byte(`{"credentials":{"token":"item-json-replacement"}}`))
	if got := call.Items[0].Content[0].Text; got != "item message replacement" {
		t.Fatalf("item message replacement was not committed: got %q", got)
	}
	if got := call.Items[0].Content[1].Text; got != `{"credentials":{"token":"item-json-replacement"}}` {
		t.Fatalf("item JSON replacement was not committed: got %q", got)
	}
}

func TestWalkLogicalFragments_AdmitsWholeFragmentsAgainstSharedBudget(t *testing.T) {
	t.Parallel()

	first := "first logical text"
	second := `{"api_key":"second"}`
	call := &aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart(first),
				{Kind: aipapi.PartJSON, Content: json.RawMessage(second)},
			},
		}},
	}

	budget := newScanBudget(len(first) + len(second) - 1)
	fragments := walkLogicalFragments(call, budget)
	if len(fragments) != 1 {
		t.Fatalf("admitted fragments: got %d want 1: %#v", len(fragments), fragments)
	}
	if got := fragments[0].textValue(); got != first {
		t.Fatalf("first fragment: got %q want %q", got, first)
	}
	if !budget.limitHit {
		t.Fatal("expected whole-fragment admission to set the scan limit")
	}
	if got, want := budget.used, len(first); got != want {
		t.Fatalf("budget used: got %d want %d", got, want)
	}

	// Detector passes consume the same admitted set. Reusing the fragments does
	// not reserve their original bytes a second time for derived JSON values.
	for range fragments {
		if got, want := budget.used, len(first); got != want {
			t.Fatalf("shared budget changed while reusing admitted fragment: got %d want %d", got, want)
		}
	}
}

func TestWalkLogicalFragments_IdenticalBytesInDistinctFieldsCountSeparately(t *testing.T) {
	t.Parallel()

	value := "same logical bytes"
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{
			aipapi.TextPart(value),
			aipapi.TextPart(value),
		}}},
	}

	budget := newScanBudget(len(value) * 2)
	fragments := walkLogicalFragments(call, budget)
	if len(fragments) != 2 {
		t.Fatalf("fragment count: got %d want 2", len(fragments))
	}
	if got, want := budget.used, len(value)*2; got != want {
		t.Fatalf("identical fields must be charged separately: got %d want %d", got, want)
	}
}

func TestScanCall_UsesOneAdmissionForJSONScalarInspection(t *testing.T) {
	t.Parallel()

	raw := `{"api_key":"opaque"}`
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
		Kind:    aipapi.PartJSON,
		Content: json.RawMessage(raw),
	}}}}}
	m := newRecordingJSONMatcher()
	out, err := scanCall(t.Context(), call, m, modeScan, len(raw))
	if err != nil {
		t.Fatal(err)
	}
	if out.ScanLimitHit {
		t.Fatal("exact scalar inspection must stay within the raw-fragment budget")
	}
	if got, want := out.BytesScanned, len(raw); got != want {
		t.Fatalf("bytes scanned: got %d want %d", got, want)
	}
	if len(m.scanTokens) == 0 {
		t.Fatal("expected exact JSON inspection to retain key/value context")
	}

	limited := newRecordingJSONMatcher()
	out, err = scanCall(t.Context(), call, limited, modeScan, len(raw)-1)
	if err != nil {
		t.Fatal(err)
	}
	if !out.ScanLimitHit {
		t.Fatal("whole raw JSON fragment should trigger the existing scan limit")
	}
	if len(limited.scanTokens) != 0 {
		t.Fatalf("over-budget fragment must not be inspected: %#v", limited.scanTokens)
	}
}
