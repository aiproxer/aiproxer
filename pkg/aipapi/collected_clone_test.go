package aipapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCloneCollected_DeepIndependence(t *testing.T) {
	t.Parallel()
	in := populatedCollectedFixture()
	out := CloneCollected(&in)

	if out.Text.String() != "hello" || out.Reasoning.String() != "reasoning" {
		t.Fatalf("builder fields lost: text=%q reasoning=%q", out.Text.String(), out.Reasoning.String())
	}
	if !out.FinishReceived || out.FinishReason != "stop" || out.InputTokens != 11 || out.Currency != "USD" {
		t.Fatalf("scalar fields not preserved: %+v", out)
	}
	if out.ToolArgs["tool"].String() != "args" || out.ToolNames["tool"] != "lookup" || out.ToolCallOrder[0] != "tool" || out.Warnings[0] != "warning" {
		t.Fatalf("tool/warning fields not preserved: %+v", out)
	}
	if out.TerminalError == nil || out.TerminalError.ErrorMessage != "failed" || out.TerminalError.Reasoning == nil || out.TerminalError.Item == nil {
		t.Fatalf("terminal error fields not preserved: %+v", out.TerminalError)
	}
	// The result is a writable heap copy (builders valid after return).
	out.Text.WriteString("-changed")
	out.ToolArgs["tool"].WriteString("-changed")

	// Mutate every remaining mutable interior of the clone; the source must stay intact.
	out.ToolNames["tool"] = "changed"
	out.ToolCallOrder[0] = "changed"
	out.Warnings[0] = "changed"
	out.TerminalError.Opaque[0] = 'X'
	out.TerminalError.Reasoning.Opaque[0] = 'X'
	out.TerminalError.Item.Content[0].Annotation.Data[0] = 'X'
	out.TerminalError.Item.ToolCall.Arguments[0] = 'X'
	out.TerminalError.UsageScopes[0] = ScopedUsageDelta{InputTokens: 99}
	out.AssistantMedia[0].Content[0] = 'X'
	out.AssistantMedia[0].Reasoning.Opaque[0] = 'X'
	out.ReasoningParts[0].Opaque[0] = 'X'

	if in.Text.String() != "hello" || in.Reasoning.String() != "reasoning" || in.ToolArgs["tool"].String() != "args" {
		t.Fatal("builder clone shares mutable state")
	}
	if in.ToolNames["tool"] != "lookup" || in.ToolCallOrder[0] != "tool" || in.Warnings[0] != "warning" {
		t.Fatal("map/slice clone shares mutable state")
	}
	if in.TerminalError.Opaque[0] != 'o' || in.TerminalError.Reasoning.Opaque[0] != 'r' || in.TerminalError.Item.Content[0].Annotation.Data[0] != 'a' || in.TerminalError.Item.ToolCall.Arguments[0] != '{' {
		t.Fatal("terminal nested clone shares mutable state")
	}
	if in.TerminalError.UsageScopes[0].InputTokens != 1 {
		t.Fatal("usage scope clone shares mutable state")
	}
	if in.AssistantMedia[0].Content[0] != 'm' || in.AssistantMedia[0].Reasoning.Opaque[0] != 'p' || in.ReasoningParts[0].Opaque[0] != 'p' {
		t.Fatal("media/reasoning clone shares mutable state")
	}
}

func TestCloneCollected_NilAndEmptyPreserved(t *testing.T) {
	t.Parallel()
	var zero Collected
	out := CloneCollected(&zero)
	if out == nil {
		t.Fatal("clone of zero value must not be nil")
	}
	if out.Text.Len() != 0 || out.Reasoning.Len() != 0 {
		t.Fatal("zero builders must remain empty")
	}
	if out.ToolArgs != nil || out.ToolNames != nil || out.ToolCallOrder != nil || out.Warnings != nil || out.AssistantMedia != nil || out.ReasoningParts != nil || out.TerminalError != nil {
		t.Fatalf("nil collections must stay nil: %+v", out)
	}
	if CloneCollected(nil) != nil {
		t.Fatal("CloneCollected(nil) must return nil")
	}

	// Empty-but-non-nil collections stay empty and non-nil (presence preserved).
	empty := Collected{
		ToolNames:      map[string]string{},
		ToolCallOrder:  []string{},
		Warnings:       []string{},
		AssistantMedia: []Part{},
		ReasoningParts: []ReasoningPart{},
	}
	outE := CloneCollected(&empty)
	if outE.ToolNames == nil || outE.ToolCallOrder == nil || outE.Warnings == nil || outE.AssistantMedia == nil || outE.ReasoningParts == nil {
		t.Fatalf("empty non-nil collections collapsed to nil: %+v", outE)
	}
	if len(outE.ToolNames) != 0 || len(outE.ToolCallOrder) != 0 || len(outE.Warnings) != 0 || len(outE.AssistantMedia) != 0 || len(outE.ReasoningParts) != 0 {
		t.Fatalf("empty clone gained entries: %+v", outE)
	}
	// The cloned map must not alias the source map.
	outE.ToolNames["tool"] = "lookup"
	if _, ok := empty.ToolNames["tool"]; ok {
		t.Fatal("empty map clone shares state")
	}
}

func TestCloneCollectedInto_ValueHostWritable(t *testing.T) {
	t.Parallel()
	in := populatedCollectedFixture()
	var out Collected
	CloneCollectedInto(&out, &in)
	if out.Text.String() != "hello" || out.Reasoning.String() != "reasoning" || out.ToolArgs["tool"].String() != "args" {
		t.Fatalf("into clone lost fields: %+v", out)
	}
	out.Text.WriteString("!")
	out.Reasoning.WriteString("!")
	if in.Text.String() != "hello" || in.Reasoning.String() != "reasoning" {
		t.Fatal("into clone shares builder state")
	}
	out.ToolNames["tool"] = "changed"
	if in.ToolNames["tool"] != "lookup" {
		t.Fatal("into clone shares map state")
	}
}

func populatedCollectedFixture() Collected {
	var c Collected
	c.Text.WriteString("hello")
	c.Reasoning.WriteString("reasoning")
	c.ToolArgs = map[string]*strings.Builder{"tool": {}}
	c.ToolArgs["tool"].WriteString("args")
	c.ToolNames = map[string]string{"tool": "lookup"}
	c.ToolCallOrder = []string{"tool"}
	c.Warnings = []string{"warning"}
	c.InputTokens = 11
	c.FinishReceived = true
	c.FinishReason = "stop"
	c.Currency = "USD"
	c.TerminalError = &Event{
		Kind:         EventError,
		ErrorMessage: "failed",
		Opaque:       []byte("opaque"),
		Reasoning: &ReasoningPart{
			Opaque:           json.RawMessage(`reasoning`),
			Summary:          json.RawMessage(`summary`),
			Content:          json.RawMessage(`content`),
			EncryptedContent: json.RawMessage(`null`),
		},
		Item: &Item{
			Kind:     ItemKindToolCall,
			ToolCall: &ToolCallItem{CallID: "call", Name: "lookup", Arguments: json.RawMessage(`{}`)},
			Content:  []ContentPart{{Kind: ContentPartAnnotation, Annotation: &AnnotationPart{Data: json.RawMessage(`annotation`)}}},
		},
		UsageScopes: []ScopedUsageDelta{{InputTokens: 1}},
	}
	c.AssistantMedia = []Part{{Kind: PartJSON, Content: json.RawMessage(`media`), Reasoning: &ReasoningPart{Opaque: json.RawMessage(`part`)}}}
	c.ReasoningParts = []ReasoningPart{{Opaque: json.RawMessage(`part-list`)}}
	return c
}
