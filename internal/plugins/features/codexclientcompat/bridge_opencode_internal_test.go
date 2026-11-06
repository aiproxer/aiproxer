package codexclientcompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestParseFunctionCallID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		part     aipapi.Part
		wantID   string
		wantName string
		wantOK   bool
	}{
		{
			name:     "responses style with call_id and name",
			part:     aipapi.Part{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"toolA"}`)},
			wantID:   "call_1",
			wantName: "toolA",
			wantOK:   true,
		},
		{
			name:     "chat completions style with id and function.name",
			part:     aipapi.Part{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function","id":"call_2","function":{"name":"toolB"}}`)},
			wantID:   "call_2",
			wantName: "toolB",
			wantOK:   true,
		},
		{
			name:     "responses style missing name",
			part:     aipapi.Part{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"call_3"}`)},
			wantID:   "call_3",
			wantName: "",
			wantOK:   true,
		},
		{
			name:     "whitespace call id is trimmed",
			part:     aipapi.Part{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"  call_4  ","name":"toolD"}`)},
			wantID:   "call_4",
			wantName: "toolD",
			wantOK:   true,
		},
		{
			name:     "canonical top-level identity with an argument document",
			part:     aipapi.Part{Kind: aipapi.PartJSON, ToolCallID: "call_5", ToolName: "toolE", Content: json.RawMessage(`{"a":1}`)},
			wantID:   "call_5",
			wantName: "toolE",
			wantOK:   true,
		},
		{
			name:   "non-function type rejected",
			part:   aipapi.Part{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"text"}`)},
			wantOK: false,
		},
		{
			name:   "empty content rejected",
			part:   aipapi.Part{Kind: aipapi.PartJSON},
			wantOK: false,
		},
		{
			name:   "invalid json rejected",
			part:   aipapi.Part{Kind: aipapi.PartJSON, Content: json.RawMessage(`not-json`)},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, name, ok := parseFunctionCallID(tc.part)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (id=%q name=%q)", ok, tc.wantOK, id, name)
			}
			if id != tc.wantID {
				t.Fatalf("id = %q, want %q", id, tc.wantID)
			}
			if name != tc.wantName {
				t.Fatalf("name = %q, want %q", name, tc.wantName)
			}
		})
	}
}

func TestIsFunctionCallPartRequiresIDAndName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		part aipapi.Part
		want bool
	}{
		{"both present", aipapi.Part{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"toolA"}`)}, true},
		{"chat style both present", aipapi.Part{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function","id":"call_2","function":{"name":"toolB"}}`)}, true},
		{"missing name", aipapi.Part{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"call_3"}`)}, false},
		{"missing id", aipapi.Part{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","name":"toolA"}`)}, false},
		{"non-function type", aipapi.Part{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"text"}`)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isFunctionCallPart(tc.part); got != tc.want {
				t.Fatalf("isFunctionCallPart = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCollectKnownToolCallIDsAcceptsBothStyles(t *testing.T) {
	t.Parallel()
	msgs := []aipapi.Message{
		{
			Role: aipapi.RoleAssistant,
			Parts: []aipapi.Part{
				{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"resp_id","name":"toolA"}`)},
				{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function","id":"chat_id","function":{"name":"toolB"}}`)},
				{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","name":"noID"}`)},
				{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"text","text":"hi"}`)},
			},
		},
	}
	known := collectKnownToolCallIDs(msgs)
	if _, ok := known["resp_id"]; !ok {
		t.Errorf("missing responses-style id %q: %v", "resp_id", known)
	}
	if _, ok := known["chat_id"]; !ok {
		t.Errorf("missing chat-completions-style id %q: %v", "chat_id", known)
	}
	if len(known) != 2 {
		t.Errorf("known = %v, want exactly 2 entries", known)
	}
}

func TestConvertOrphanedToolResultsPreservesPartOrder(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"a","name":"toolA"}`)},
					{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"c","name":"toolC"}`)},
				},
			},
			{
				Role: aipapi.RoleTool,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartToolResult, ToolCallID: "a", Content: json.RawMessage(`{"out":"A"}`)},
					{Kind: aipapi.PartToolResult, ToolCallID: "orphan", Content: json.RawMessage(`{"out":"B"}`)},
					{Kind: aipapi.PartToolResult, ToolCallID: "c", Content: json.RawMessage(`{"out":"C"}`)},
				},
			},
		},
	}

	convertOrphanedToolResults(call)

	// Expected order preserves the original A -> B -> C sequence: the known
	// results bracket the converted orphan instead of all known results being
	// flushed after it.
	wantRoles := []aipapi.Role{aipapi.RoleAssistant, aipapi.RoleTool, aipapi.RoleSystem, aipapi.RoleTool}
	if len(call.Messages) != len(wantRoles) {
		t.Fatalf("messages = %d, want %d: %#v", len(call.Messages), len(wantRoles), call.Messages)
	}
	for i, want := range wantRoles {
		if call.Messages[i].Role != want {
			t.Fatalf("messages[%d].Role = %q, want %q", i, call.Messages[i].Role, want)
		}
	}
	if got := call.Messages[1].Parts[0].ToolCallID; got != "a" {
		t.Fatalf("messages[1].Parts[0].ToolCallID = %q, want %q", got, "a")
	}
	if !strings.Contains(messageText(call.Messages[2]), "Prior tool output") {
		t.Fatalf("messages[2] = %#v, want System orphan conversion", call.Messages[2])
	}
	if got := call.Messages[3].Parts[0].ToolCallID; got != "c" {
		t.Fatalf("messages[3].Parts[0].ToolCallID = %q, want %q", got, "c")
	}
}

func TestConvertOrphanedToolResultsLeavesKnownOnlyMessagesUntouched(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"a","name":"toolA"}`)},
				},
			},
			{
				Role: aipapi.RoleTool,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartToolResult, ToolCallID: "a", Content: json.RawMessage(`{"out":"A"}`)},
				},
			},
		},
	}
	convertOrphanedToolResults(call)
	if len(call.Messages) != 2 {
		t.Fatalf("messages = %d, want 2: %#v", len(call.Messages), call.Messages)
	}
	if call.Messages[1].Role != aipapi.RoleTool || len(call.Messages[1].Parts) != 1 {
		t.Fatalf("messages[1] = %#v, want single Tool([A])", call.Messages[1])
	}
}
