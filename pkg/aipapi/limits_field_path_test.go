package aipapi_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestCallValidate_sizeLimitFieldPaths pins the diagnostic field paths of
// envelope size violations. Those labels are rendered on the error path only, so
// this test keeps the deferred rendering byte-identical to the eager form for
// every part and tool branch.
func TestCallValidate_sizeLimitFieldPaths(t *testing.T) {
	t.Parallel()

	okPart := func() aipapi.Part { return aipapi.TextPart("ok") }
	validMessages := []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{okPart()}}}

	cases := []struct {
		name  string
		call  aipapi.Call
		field string
	}{
		{
			name: "message part text",
			call: aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{okPart()}},
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{okPart(), aipapi.TextPart(strings.Repeat("x", aipapi.MaxPartTextBytes+1))}},
			}},
			field: "Messages[1].Parts[1].Text",
		},
		{
			name: "message part image ref",
			call: aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
				Kind:     aipapi.PartImageRef,
				ImageRef: strings.Repeat("r", aipapi.MaxRefStringBytes+1),
			}}}}},
			field: "Messages[0].Parts[0].ImageRef",
		},
		{
			name: "instruction part file mime",
			call: aipapi.Call{Messages: validMessages, Instructions: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
				Kind:     aipapi.PartFileRef,
				FileRef:  "file:///tmp/x",
				FileMIME: strings.Repeat("m", aipapi.MaxRefStringBytes+1),
			}}}}},
			field: "Instructions[0].Parts[0].FileMIME",
		},
		{
			name: "message part tool call id",
			call: aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: strings.Repeat("c", aipapi.MaxRefStringBytes+1),
			}}}}},
			field: "Messages[0].Parts[0].ToolCallID",
		},
		{
			name: "message part tool result text",
			call: aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: "call_text",
				Text:       strings.Repeat("x", aipapi.MaxPartTextBytes+1),
			}}}}},
			field: "Messages[0].Parts[0].Text",
		},
		{
			name: "message part json content",
			call: aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
				Kind:    aipapi.PartJSON,
				Content: json.RawMessage(strings.Repeat("1", aipapi.MaxPartJSONBytes+1)),
			}}}}},
			field: "Messages[0].Parts[0].Content",
		},
		{
			name:  "tool name whitespace",
			call:  aipapi.Call{Messages: validMessages, Tools: []aipapi.ToolDef{{Name: " fn"}}},
			field: "Tools[0].Name",
		},
		{
			name: "tool description",
			call: aipapi.Call{Messages: validMessages, Tools: []aipapi.ToolDef{
				{Name: "a"},
				{Name: "b", Description: strings.Repeat("d", aipapi.MaxToolDescriptionBytes+1)},
			}},
			field: "Tools[1].Description",
		},
		{
			name: "tool parameters",
			call: aipapi.Call{Messages: validMessages, Tools: []aipapi.ToolDef{
				{Name: "a"},
				{Name: "b"},
				{Name: "c", Parameters: json.RawMessage(strings.Repeat("p", aipapi.MaxToolParametersBytes+1))},
			}},
			field: "Tools[2].Parameters",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.call.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			var ve *aipapi.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error type = %T (%v), want *aipapi.ValidationError", err, err)
			}
			if ve.Field != tc.field {
				t.Fatalf("ValidationError.Field = %q, want %q", ve.Field, tc.field)
			}
			if ve.Message == "" {
				t.Fatalf("ValidationError for %q carries no message", tc.field)
			}
		})
	}
}

// TestCallValidate_toolResultTextBoundary pins the canonical tool-result text
// bound. The ABI now preserves this carrier, so admission must not accept a
// payload that the legacy message carrier never bounded.
func TestCallValidate_toolResultTextBoundary(t *testing.T) {
	t.Parallel()
	atLimit := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
		Kind:       aipapi.PartToolResult,
		ToolCallID: "call_text",
		Text:       strings.Repeat("x", aipapi.MaxPartTextBytes),
	}}}}}
	if err := atLimit.Validate(); err != nil {
		t.Fatalf("tool-result text exactly at the canonical bound must be accepted: %v", err)
	}
	overLimit := atLimit
	overLimit.Messages = []aipapi.Message{{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
		Kind:       aipapi.PartToolResult,
		ToolCallID: "call_text",
		Text:       strings.Repeat("x", aipapi.MaxPartTextBytes+1),
	}}}}
	err := overLimit.Validate()
	if err == nil {
		t.Fatal("tool-result text over the canonical bound must be refused")
	}
	var ve *aipapi.ValidationError
	if !errors.As(err, &ve) || ve.Field != "Messages[0].Parts[0].Text" {
		t.Fatalf("validation error=%v, want Messages[0].Parts[0].Text", err)
	}
}
