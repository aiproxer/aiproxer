package tiktoken

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Differential tests justify the spec-owned normalized walker change in
// countCallTokens (Task 1.3 normalized item trajectory): the item-authority
// walker must produce the exact same estimate for a legacy-message-authority
// call and its equivalent item-authority call across text, tool, JSON, and
// multimodal content shapes. Both authority forms flow through the single
// aipapi.NormalizedItems traversal, so a legacy call and its hand-built item
// equivalent must count identically (no authority-dependent drift).

// legacyEquivalentItemCall builds the item-authority call that mirrors legacy's
// Instructions + Messages content parts and the shared Tools/ToolChoice surface.
func legacyEquivalentItemCall(legacy aipapi.Call) aipapi.Call {
	var items []aipapi.Item
	for i, msg := range legacy.Instructions {
		role := msg.Role
		if role == "" {
			role = aipapi.RoleSystem
		}
		items = append(items, aipapi.Item{
			Kind:    aipapi.ItemKindMessage,
			ID:      "inst-" + string(rune('0'+i)),
			Status:  aipapi.ItemStatusCompleted,
			Role:    role,
			Content: partsToContentPartsForTest(msg.Parts),
		})
	}
	for i, msg := range legacy.Messages {
		items = append(items, aipapi.Item{
			Kind:    aipapi.ItemKindMessage,
			ID:      "msg-" + string(rune('0'+i)),
			Status:  aipapi.ItemStatusCompleted,
			Role:    msg.Role,
			Content: partsToContentPartsForTest(msg.Parts),
		})
	}
	item := legacy
	item.Items = items
	return item
}

// partsToContentPartsForTest mirrors aipapi.partsToContentParts for the content
// shapes the local counter can estimate (text, JSON, image ref).
func partsToContentPartsForTest(parts []aipapi.Part) []aipapi.ContentPart {
	var out []aipapi.ContentPart
	for _, p := range parts {
		switch p.Kind {
		case aipapi.PartText:
			out = append(out, aipapi.ContentPart{Kind: aipapi.ContentPartText, Text: p.Text})
		case aipapi.PartJSON:
			out = append(out, aipapi.ContentPart{
				Kind: aipapi.ContentPartJSON,
				Text: string(p.Content),
				Annotation: &aipapi.AnnotationPart{
					Type: "json_content",
					Data: p.Content,
				},
			})
		case aipapi.PartImageRef:
			var ann *aipapi.AnnotationPart
			if len(p.Content) > 0 {
				ann = &aipapi.AnnotationPart{Type: "image_detail", Data: p.Content}
			}
			out = append(out, aipapi.ContentPart{
				Kind:       aipapi.ContentPartImageRef,
				ImageRef:   p.ImageRef,
				ImageMIME:  p.ImageMIME,
				Annotation: ann,
			})
		}
	}
	return out
}

func assertLegacyItemDifferential(t *testing.T, legacy aipapi.Call) {
	t.Helper()
	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base", Image: ImageConfig{MaxDecodedBytes: 1 << 20}})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}
	item := legacyEquivalentItemCall(legacy)
	if !item.HasItemAuthority() {
		t.Fatal("equivalent item call does not use item authority")
	}
	legacyRes, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: legacy})
	if err != nil {
		t.Fatalf("CountCall(legacy) error = %v", err)
	}
	itemRes, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: item})
	if err != nil {
		t.Fatalf("CountCall(item) error = %v", err)
	}
	if legacyRes.InputTokens != itemRes.InputTokens {
		t.Fatalf("differential drift: legacy %d tokens vs item %d tokens", legacyRes.InputTokens, itemRes.InputTokens)
	}
	if itemRes.TotalTokens != itemRes.InputTokens {
		t.Fatalf("item TotalTokens = %d, want InputTokens %d", itemRes.TotalTokens, itemRes.InputTokens)
	}
}

func TestCountCallDifferential_Text(t *testing.T) {
	t.Parallel()
	legacy := aipapi.Call{
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("answer tersely")}}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello world")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("hi there")}},
		},
	}
	assertLegacyItemDifferential(t, legacy)
}

func TestCountCallDifferential_JSON(t *testing.T) {
	t.Parallel()
	legacy := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart("parse"),
				{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"city":"warsaw","units":["c","f"]}`)},
			},
		}},
	}
	assertLegacyItemDifferential(t, legacy)
}

func TestCountCallDifferential_Multimodal(t *testing.T) {
	t.Parallel()
	legacy := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("describe"), imagePart(dataURIPNG(t, 32, 32), "low")},
		}},
	}
	assertLegacyItemDifferential(t, legacy)
}

func TestCountCallDifferential_Tool(t *testing.T) {
	t.Parallel()
	legacy := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("weather")}}},
		Tools: []aipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Get weather for a city.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "get_weather"},
	}
	assertLegacyItemDifferential(t, legacy)
}

// TestCountCallDifferential_Mixed proves a call mixing text, JSON, multimodal,
// and the tool surface counts identically across authority forms.
func TestCountCallDifferential_Mixed(t *testing.T) {
	t.Parallel()
	legacy := aipapi.Call{
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("be brief")}}},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart("summarize the image"),
				{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"tags":["a","b"]}`)},
				imagePart(dataURIPNG(t, 32, 32), "auto"),
			},
		}},
		Tools: []aipapi.ToolDef{{
			Name:        "lookup",
			Description: "Look up a value.",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
	}
	assertLegacyItemDifferential(t, legacy)
}

// TestCountCallItemAuthority_ToolResultCountsOutput proves the item-authority
// walker counts tool-result output text (the legacy message walker could not
// represent tool results in a countable form), so item calls with tool results
// stay estimable after the normalized-walker change.
func TestCountCallItemAuthority_ToolResultCountsOutput(t *testing.T) {
	t.Parallel()
	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}
	call := aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "call the tool"}},
			},
			{
				Kind: aipapi.ItemKindToolResult, ID: "t1", Status: aipapi.ItemStatusCompleted,
				ToolResult: &aipapi.ToolResultItem{CallID: "call_1", Name: "lookup", Output: "warsaw, poland"},
			},
		},
	}
	with, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: call})
	if err != nil {
		t.Fatalf("CountCall(item tool result) error = %v", err)
	}
	// Base (message only) minus the tool-result output tokens must be positive.
	base := aipapi.Call{Items: call.Items[:1]}
	without, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: base})
	if err != nil {
		t.Fatalf("CountCall(base item) error = %v", err)
	}
	if with.InputTokens <= without.InputTokens {
		t.Fatalf("tool-result output tokens = %d, want > %d", with.InputTokens, without.InputTokens)
	}
}
