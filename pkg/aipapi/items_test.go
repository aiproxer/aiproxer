package aipapi_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestOrderedCanonicalItems_TableValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		call    aipapi.Call
		wantErr bool
		errSub  string
	}{
		{
			name: "valid message item",
			call: aipapi.Call{
				ID: "call-1",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindMessage,
						ID:     "item-1",
						Status: aipapi.ItemStatusCompleted,
						Role:   aipapi.RoleUser,
						Content: []aipapi.ContentPart{
							{Kind: aipapi.ContentPartText, Text: "Hello world"},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid tool call and result identity items",
			call: aipapi.Call{
				ID: "call-tools",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindToolCall,
						ID:     "tc-1",
						Status: aipapi.ItemStatusCompleted,
						ToolCall: &aipapi.ToolCallItem{
							CallID:    "call_abc123",
							Name:      "get_weather",
							Arguments: json.RawMessage(`{"location":"San Francisco"}`),
						},
					},
					{
						Kind:   aipapi.ItemKindToolResult,
						ID:     "tr-1",
						Status: aipapi.ItemStatusCompleted,
						ToolResult: &aipapi.ToolResultItem{
							CallID: "call_abc123",
							Name:   "get_weather",
							Output: `{"temperature": 68}`,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid item reference and compaction",
			call: aipapi.Call{
				ID: "call-ref-compact",
				Items: []aipapi.Item{
					{
						Kind:      aipapi.ItemKindItemReference,
						ID:        "ref-1",
						Status:    aipapi.ItemStatusCompleted,
						Reference: &aipapi.ItemReference{ID: "msg-prev-456"},
					},
					{
						Kind:   aipapi.ItemKindCompaction,
						ID:     "cmp-1",
						Status: aipapi.ItemStatusCompleted,
						Compaction: &aipapi.CompactionItem{
							EncapsulatedID: "enc-789",
							Dialect:        "compact.v1",
							Opaque:         json.RawMessage(`{"compressed":true}`),
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid reasoning item",
			call: aipapi.Call{
				ID: "call-reasoning",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindReasoning,
						ID:     "rs-1",
						Status: aipapi.ItemStatusCompleted,
						Reasoning: &aipapi.ReasoningItem{
							Reasoning: &aipapi.ReasoningPart{
								Dialect: "standard",
								Text:    "Thinking steps...",
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "malformed message item - missing role",
			call: aipapi.Call{
				ID: "call-err-1",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindMessage,
						Status: aipapi.ItemStatusCompleted,
						Content: []aipapi.ContentPart{
							{Kind: aipapi.ContentPartText, Text: "No role"},
						},
					},
				},
			},
			wantErr: true,
			errSub:  "role is required",
		},
		{
			name: "malformed message item - empty content",
			call: aipapi.Call{
				ID: "call-err-2",
				Items: []aipapi.Item{
					{
						Kind:    aipapi.ItemKindMessage,
						Role:    aipapi.RoleUser,
						Status:  aipapi.ItemStatusCompleted,
						Content: []aipapi.ContentPart{},
					},
				},
			},
			wantErr: true,
			errSub:  "content part is required",
		},
		{
			name: "malformed tool call item - missing tool call struct",
			call: aipapi.Call{
				ID: "call-err-3",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindToolCall,
						Status: aipapi.ItemStatusCompleted,
					},
				},
			},
			wantErr: true,
			errSub:  "tool call data is required",
		},
		{
			name: "malformed tool call item - missing CallID",
			call: aipapi.Call{
				ID: "call-err-4",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindToolCall,
						Status: aipapi.ItemStatusCompleted,
						ToolCall: &aipapi.ToolCallItem{
							Name:      "get_weather",
							Arguments: json.RawMessage(`{}`),
						},
					},
				},
			},
			wantErr: true,
			errSub:  "call ID is required",
		},
		{
			name: "malformed item reference - missing reference struct or ID",
			call: aipapi.Call{
				ID: "call-err-5",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindItemReference,
						Status: aipapi.ItemStatusCompleted,
					},
				},
			},
			wantErr: true,
			errSub:  "reference ID is required",
		},
		{
			name: "malformed extension item - missing namespace",
			call: aipapi.Call{
				ID: "call-err-6",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindExtension,
						Status: aipapi.ItemStatusCompleted,
						Extension: &aipapi.OpaqueExtension{
							Namespace: "",
							Type:      "my_ext",
							Data:      json.RawMessage(`{}`),
						},
					},
				},
			},
			wantErr: true,
			errSub:  "namespace is required",
		},
		{
			name: "malformed extension item - invalid JSON data",
			call: aipapi.Call{
				ID: "call-err-7",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindExtension,
						Status: aipapi.ItemStatusCompleted,
						Extension: &aipapi.OpaqueExtension{
							Namespace: "org.aip",
							Type:      "custom",
							Data:      json.RawMessage(`invalid json`),
						},
					},
				},
			},
			wantErr: true,
			errSub:  "must be valid JSON",
		},
		{
			name: "variant violation - message item with tool_call field",
			call: aipapi.Call{
				ID: "call-var-1",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindMessage,
						Role:   aipapi.RoleUser,
						Status: aipapi.ItemStatusCompleted,
						Content: []aipapi.ContentPart{
							{Kind: aipapi.ContentPartText, Text: "hi"},
						},
						ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "t1"},
					},
				},
			},
			wantErr: true,
			errSub:  "must not contain non-message variant fields",
		},
		{
			name: "variant violation - tool_call item with content",
			call: aipapi.Call{
				ID: "call-var-2",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindToolCall,
						Status: aipapi.ItemStatusCompleted,
						Content: []aipapi.ContentPart{
							{Kind: aipapi.ContentPartText, Text: "invalid"},
						},
						ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "t1"},
					},
				},
			},
			wantErr: true,
			errSub:  "must not contain non-tool_call variant fields",
		},
		{
			name: "invalid enum - role",
			call: aipapi.Call{
				ID: "call-enum-1",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindMessage,
						Role:   aipapi.Role("superadmin"),
						Status: aipapi.ItemStatusCompleted,
						Content: []aipapi.ContentPart{
							{Kind: aipapi.ContentPartText, Text: "hi"},
						},
					},
				},
			},
			wantErr: true,
			errSub:  "invalid role",
		},
		{
			name: "invalid enum - status",
			call: aipapi.Call{
				ID: "call-enum-2",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindMessage,
						Role:   aipapi.RoleUser,
						Status: aipapi.ItemStatus("pending"),
						Content: []aipapi.ContentPart{
							{Kind: aipapi.ContentPartText, Text: "hi"},
						},
					},
				},
			},
			wantErr: true,
			errSub:  "invalid item status",
		},
		{
			name: "invalid phase on user message",
			call: aipapi.Call{
				ID: "call-phase-1",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindMessage,
						Role:   aipapi.RoleUser,
						Phase:  aipapi.AssistantPhaseCommentary,
						Status: aipapi.ItemStatusCompleted,
						Content: []aipapi.ContentPart{
							{Kind: aipapi.ContentPartText, Text: "hi"},
						},
					},
				},
			},
			wantErr: true,
			errSub:  "phase is only allowed on assistant message items",
		},
		{
			name: "invalid phase on tool_call item",
			call: aipapi.Call{
				ID: "call-phase-2",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindToolCall,
						Phase:  aipapi.AssistantPhaseFinalAnswer,
						Status: aipapi.ItemStatusCompleted,
						ToolCall: &aipapi.ToolCallItem{
							CallID: "c1",
							Name:   "fn",
						},
					},
				},
			},
			wantErr: true,
			errSub:  "phase is only allowed on assistant message items",
		},
		{
			name: "tool result ambiguity - both output and parts set",
			call: aipapi.Call{
				ID: "call-ambiguity-1",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindToolCall,
						ID:     "tc-1",
						Status: aipapi.ItemStatusCompleted,
						ToolCall: &aipapi.ToolCallItem{
							CallID: "call-amb",
							Name:   "calc",
						},
					},
					{
						Kind:   aipapi.ItemKindToolResult,
						ID:     "tr-1",
						Status: aipapi.ItemStatusCompleted,
						ToolResult: &aipapi.ToolResultItem{
							CallID: "call-amb",
							Name:   "calc",
							Output: "42",
							Parts: []aipapi.ContentPart{
								{Kind: aipapi.ContentPartText, Text: "42"},
							},
						},
					},
				},
			},
			wantErr: true,
			errSub:  "tool result must specify output or parts, not both",
		},
		{
			name: "tool result empty - neither output nor parts set",
			call: aipapi.Call{
				ID: "call-ambiguity-2",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindToolCall,
						ID:     "tc-1",
						Status: aipapi.ItemStatusCompleted,
						ToolCall: &aipapi.ToolCallItem{
							CallID: "call-amb2",
							Name:   "calc",
						},
					},
					{
						Kind:   aipapi.ItemKindToolResult,
						ID:     "tr-1",
						Status: aipapi.ItemStatusCompleted,
						ToolResult: &aipapi.ToolResultItem{
							CallID: "call-amb2",
							Name:   "calc",
						},
					},
				},
			},
			wantErr: true,
			errSub:  "tool result must specify output or parts",
		},
		{
			name: "extension namespace whitespace",
			call: aipapi.Call{
				ID: "call-ext-space",
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindExtension,
						Status: aipapi.ItemStatusCompleted,
						Extension: &aipapi.OpaqueExtension{
							Namespace: "invalid namespace",
							Type:      "t1",
							Data:      json.RawMessage(`{}`),
						},
					},
				},
			},
			wantErr: true,
			errSub:  "must not contain whitespace",
		},
		{
			name: "trajectory violation - duplicate item IDs",
			call: aipapi.Call{
				ID: "call-dup-id",
				Items: []aipapi.Item{
					{
						Kind:    aipapi.ItemKindMessage,
						ID:      "item-dup",
						Role:    aipapi.RoleUser,
						Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "1"}},
					},
					{
						Kind:    aipapi.ItemKindMessage,
						ID:      "item-dup",
						Role:    aipapi.RoleAssistant,
						Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "2"}},
					},
				},
			},
			wantErr: true,
			errSub:  "duplicate item ID",
		},
		{
			name: "trajectory violation - forward item reference",
			call: aipapi.Call{
				ID: "call-forward-ref",
				Items: []aipapi.Item{
					{
						Kind:      aipapi.ItemKindItemReference,
						ID:        "ref-1",
						Reference: &aipapi.ItemReference{ID: "future-item"},
					},
					{
						Kind:    aipapi.ItemKindMessage,
						ID:      "future-item",
						Role:    aipapi.RoleUser,
						Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "future"}},
					},
				},
			},
			wantErr: true,
			errSub:  "forward item reference",
		},
		{
			name: "trajectory violation - duplicate call ID in tool calls",
			call: aipapi.Call{
				ID: "call-dup-callid",
				Items: []aipapi.Item{
					{
						Kind:     aipapi.ItemKindToolCall,
						ID:       "tc-1",
						ToolCall: &aipapi.ToolCallItem{CallID: "c-dup", Name: "tool1"},
					},
					{
						Kind:     aipapi.ItemKindToolCall,
						ID:       "tc-2",
						ToolCall: &aipapi.ToolCallItem{CallID: "c-dup", Name: "tool2"},
					},
				},
			},
			wantErr: true,
			errSub:  "duplicate call ID",
		},
		{
			name: "trajectory violation - orphan tool result",
			call: aipapi.Call{
				ID: "call-orphan-result",
				Items: []aipapi.Item{
					{
						Kind:       aipapi.ItemKindToolResult,
						ID:         "tr-1",
						ToolResult: &aipapi.ToolResultItem{CallID: "unmatched-call", Output: "res"},
					},
				},
			},
			wantErr: true,
			errSub:  "orphan tool result",
		},
		{
			name: "empty items slice in item authority",
			call: aipapi.Call{
				ID:    "call-empty-items",
				Items: []aipapi.Item{},
			},
			wantErr: true,
			errSub:  "at least one item is required unless previous_response_id is set",
		},
		{
			name: "conflicting authority - items + messages",
			call: aipapi.Call{
				ID: "call-conflict-1",
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("msg")}},
				},
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindMessage,
						Role:   aipapi.RoleUser,
						Status: aipapi.ItemStatusCompleted,
						Content: []aipapi.ContentPart{
							{Kind: aipapi.ContentPartText, Text: "item"},
						},
					},
				},
			},
			wantErr: true,
			errSub:  "conflicting raw item and legacy message authorities",
		},
		{
			name: "conflicting authority - items + instructions",
			call: aipapi.Call{
				ID: "call-conflict-2",
				Instructions: []aipapi.Message{
					{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("sys")}},
				},
				Items: []aipapi.Item{
					{
						Kind:   aipapi.ItemKindMessage,
						Role:   aipapi.RoleUser,
						Status: aipapi.ItemStatusCompleted,
						Content: []aipapi.ContentPart{
							{Kind: aipapi.ContentPartText, Text: "item"},
						},
					},
				},
			},
			wantErr: true,
			errSub:  "conflicting raw item and legacy message authorities",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.call.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && tt.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errSub) {
					t.Fatalf("expected error containing %q, got %v", tt.errSub, err)
				}
			}
		})
	}
}

func TestJSONDepthLimit(t *testing.T) {
	t.Parallel()
	// Build JSON with 70 nested braces
	var sb strings.Builder
	for range 70 {
		sb.WriteString(`{"a":`)
	}
	sb.WriteString("1")
	for range 70 {
		sb.WriteString("}")
	}

	call := aipapi.Call{
		ID: "json-depth-test",
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindToolCall,
				Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{
					CallID:    "c1",
					Name:      "test",
					Arguments: json.RawMessage(sb.String()),
				},
			},
		},
	}

	err := call.Validate()
	if err == nil {
		t.Fatal("expected error for excessive JSON depth, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds maximum JSON depth") {
		t.Fatalf("expected JSON depth error, got: %v", err)
	}
}

func TestHasItemAuthority(t *testing.T) {
	t.Parallel()
	callLegacy := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	if callLegacy.HasItemAuthority() {
		t.Fatal("nil Items slice must be legacy message authority")
	}

	callItemEmpty := aipapi.Call{
		Items: []aipapi.Item{},
	}
	if !callItemEmpty.HasItemAuthority() {
		t.Fatal("non-nil Items slice must be item authority")
	}
}

func TestOrderedCanonicalItems_Bounds(t *testing.T) {
	t.Parallel()
	tooManyItems := make([]aipapi.Item, aipapi.MaxItems+1)
	for i := range tooManyItems {
		tooManyItems[i] = aipapi.Item{
			Kind:   aipapi.ItemKindMessage,
			Role:   aipapi.RoleUser,
			Status: aipapi.ItemStatusCompleted,
			Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: fmt.Sprintf("item-%d", i)},
			},
		}
	}

	call := aipapi.Call{
		ID:    "bound-test",
		Items: tooManyItems,
	}

	err := call.Validate()
	if err == nil {
		t.Fatal("expected error for exceeding MaxItems, got nil")
	}
	if !strings.Contains(err.Error(), "at most 4096 items") {
		t.Fatalf("expected MaxItems error message, got: %v", err)
	}
}

func TestWalkers_OpaqueDataInspection(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "opaque-walk-test",
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindExtension,
				Status: aipapi.ItemStatusCompleted,
				Extension: &aipapi.OpaqueExtension{
					Namespace: "com.acme",
					Type:      "secret_payload",
					Data:      json.RawMessage(`{"secret":"api_key_12345"}`),
				},
			},
			{
				Kind:   aipapi.ItemKindToolCall,
				Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{
					CallID:    "call-1",
					Name:      "vault_read",
					Arguments: json.RawMessage(`{"key":"db_password"}`),
				},
			},
		},
	}

	var visitedOpaque []string
	err := aipapi.WalkCallOpaqueData(call, func(field string, data []byte) error {
		visitedOpaque = append(visitedOpaque, string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error walking opaque data: %v", err)
	}

	if len(visitedOpaque) != 2 {
		t.Fatalf("expected 2 opaque data visits, got %d: %v", len(visitedOpaque), visitedOpaque)
	}

	if !strings.Contains(visitedOpaque[0], "api_key_12345") || !strings.Contains(visitedOpaque[1], "db_password") {
		t.Fatalf("sensitive opaque data was not visited: %v", visitedOpaque)
	}
}

func TestCallReasoningPayloadBytes_ItemAuthority(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "reasoning-bytes-test",
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindReasoning,
				Status: aipapi.ItemStatusCompleted,
				Reasoning: &aipapi.ReasoningItem{
					Reasoning: &aipapi.ReasoningPart{
						Dialect:   "standard",
						Text:      "think1",                            // 6 bytes
						Signature: "sig1",                              // 4 bytes
						Opaque:    json.RawMessage(`{"opaque":"val"}`), // 16 bytes
					},
				},
			},
		},
	}

	bytesCount := aipapi.CallReasoningPayloadBytes(&call)
	expected := int64(6 + 4 + 16)
	if bytesCount != expected {
		t.Fatalf("expected CallReasoningPayloadBytes = %d, got %d", expected, bytesCount)
	}
}

func TestCloneCall_ItemAuthority(t *testing.T) {
	t.Parallel()
	orig := aipapi.Call{
		ID: "clone-test",
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindExtension,
				Status: aipapi.ItemStatusCompleted,
				Extension: &aipapi.OpaqueExtension{
					Namespace: "ns.test",
					Type:      "type1",
					Data:      json.RawMessage(`{"a":1}`),
				},
			},
		},
	}

	cloned := aipapi.CloneCall(orig)

	// Mutate original data
	orig.Items[0].Extension.Data[0] = 'X'

	if string(cloned.Items[0].Extension.Data) != `{"a":1}` {
		t.Fatalf("cloned extension Data was mutated, got: %s", string(cloned.Items[0].Extension.Data))
	}
}
