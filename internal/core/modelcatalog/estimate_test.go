package modelcatalog_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDefaultSizeEstimator_textOnly_available(t *testing.T) {
	t.Parallel()
	est := modelcatalog.DefaultSizeEstimator{}
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart("hello"),
				aipapi.TextPart(" world"),
			},
		}},
	}
	got := est.Estimate(context.Background(), call)
	if !got.Available {
		t.Fatalf("expected available estimate, got %+v", got)
	}
	if got.Units != "bytes" {
		t.Fatalf("Units: got %q want bytes", got.Units)
	}
	if got.Input != 11 { // "hello" + " world"
		t.Fatalf("Input: got %d want 11", got.Input)
	}
	if got.Basis != modelcatalog.EstimateBasisCanonicalUTF8 {
		t.Fatalf("Basis: got %q", got.Basis)
	}
}

func TestDefaultSizeEstimator_decisionPayloadCounts(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{Decision: &aipapi.DecisionRequest{
		Evidence: json.RawMessage(`"data"`),
		Questions: []aipapi.DecisionQuestion{
			{ID: "yes", Kind: aipapi.DecisionKindNoul, Instructions: json.RawMessage(`"ask"`), TrueCriteria: json.RawMessage(`"ok"`), FalseCriteria: json.RawMessage(`"no"`)},
			{ID: "pick", Kind: aipapi.DecisionKindChoice, Options: []aipapi.DecisionOption{{Name: "a", Description: json.RawMessage(`"one"`)}}},
			{ID: "grade", Kind: aipapi.DecisionKindScore, Levels: []json.RawMessage{json.RawMessage(`"lo"`), json.RawMessage(`"hi"`)}},
		},
	}}
	est := modelcatalog.DefaultSizeEstimator{}
	got := est.EstimateRequestTokens(t.Context(), call)
	// Original JSON bytes: 6 evidence + 5 instruction + 8 noul criteria +
	// 5 option description + 8 score levels + 13 question/option label bytes.
	if !got.Available || got.Units != "bytes" || got.Input != 45 {
		t.Fatalf("decision estimate = %+v, want 45 available bytes", got)
	}
	call.Decision.Evidence = json.RawMessage(`"data-more"`)
	if got := est.Estimate(t.Context(), call); got.Input != 50 {
		t.Fatalf("larger decision estimate = %+v, want 50 bytes", got)
	}
}

func TestDefaultSizeEstimator_decisionCriteriaNamesCountWithoutDescriptions(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{Decision: &aipapi.DecisionRequest{Evidence: json.RawMessage(`"x"`), Questions: []aipapi.DecisionQuestion{
		{ID: "q", Kind: aipapi.DecisionKindChoice, Options: []aipapi.DecisionOption{{Name: "very-long-option"}}},
	}}}
	got := (modelcatalog.DefaultSizeEstimator{}).Estimate(t.Context(), call)
	if !got.Available || got.Input != 20 { // 3 evidence + 1 question ID + 16 option name
		t.Fatalf("decision labels omitted from quote sizing: %+v", got)
	}
}

func TestDefaultSizeEstimator_instructions_counted(t *testing.T) {
	t.Parallel()
	est := modelcatalog.DefaultSizeEstimator{}
	call := aipapi.Call{
		Instructions: []aipapi.Message{{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart("sys")},
		}},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("u")},
		}},
	}
	got := est.Estimate(context.Background(), call)
	if !got.Available || got.Input != 4 { // sys + u
		t.Fatalf("got %+v", got)
	}
}

func TestDefaultSizeEstimator_tools_add_bytes(t *testing.T) {
	t.Parallel()
	est := modelcatalog.DefaultSizeEstimator{}
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("x")},
		}},
		Tools: []aipapi.ToolDef{
			{Name: "fn", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
	}
	got := est.Estimate(context.Background(), call)
	if !got.Available {
		t.Fatalf("got %+v", got)
	}
	if got.Input <= 1 {
		t.Fatalf("expected tools to add bytes, got %d", got.Input)
	}
	if got.Basis != modelcatalog.EstimateBasisCanonicalUTF8AndTools {
		t.Fatalf("Basis: got %q want tools composite", got.Basis)
	}
}

func TestDefaultSizeEstimator_sessionHints_noContribution_unavailable(t *testing.T) {
	t.Parallel()
	est := modelcatalog.DefaultSizeEstimator{}
	call := aipapi.Call{
		Session: aipapi.SessionRef{
			ResumeToken: "token",
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	got := est.Estimate(context.Background(), call)
	if got.Available {
		t.Fatalf("expected unavailable when session contribution missing, got %+v", got)
	}
	if got.Basis != modelcatalog.EstimateBasisSessionContributionUnavailable {
		t.Fatalf("Basis: got %q", got.Basis)
	}
}

func TestDefaultSizeEstimator_sessionHints_withContribution_available(t *testing.T) {
	t.Parallel()
	est := modelcatalog.DefaultSizeEstimator{}
	call := aipapi.Call{
		Session: aipapi.SessionRef{
			ResumeToken: "token",
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("ab")},
		}},
	}
	ctx := modelcatalog.WithSessionSizeContribution(context.Background(), 100)
	got := est.Estimate(ctx, call)
	if !got.Available {
		t.Fatalf("got %+v", got)
	}
	if got.Input != 2+100 {
		t.Fatalf("Input: got %d want 102", got.Input)
	}
	if got.Basis != modelcatalog.EstimateBasisCanonicalUTF8AndSession {
		t.Fatalf("Basis: got %q", got.Basis)
	}
}

func TestDefaultSizeEstimator_jsonPart_bytes(t *testing.T) {
	t.Parallel()
	est := modelcatalog.DefaultSizeEstimator{}
	raw := json.RawMessage(`{"k":1}`)
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind:    aipapi.PartJSON,
				Content: raw,
			}},
		}},
	}
	got := est.Estimate(context.Background(), call)
	if !got.Available || got.Input != int64(len(raw)) {
		t.Fatalf("got %+v", got)
	}
}

func TestDefaultSizeEstimator_countsReasoningParts(t *testing.T) {
	t.Parallel()
	est := modelcatalog.DefaultSizeEstimator{}
	opaque := json.RawMessage(`{"data":"x"}`)
	if !json.Valid(opaque) {
		t.Fatal("opaque must be valid JSON")
	}
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleAssistant,
			Parts: []aipapi.Part{
				{
					Kind: aipapi.PartReasoning,
					Reasoning: &aipapi.ReasoningPart{
						Dialect:   aipapi.ReasoningDialectOpenAIChatTextV1,
						Text:      "abcd",
						Signature: "sig",
						Opaque:    opaque,
					},
				},
				aipapi.TextPart("xy"),
			},
		}},
	}
	got := est.Estimate(context.Background(), call)
	if !got.Available {
		t.Fatalf("expected available estimate, got %+v", got)
	}
	want := int64(len("abcd") + len("sig") + len(opaque) + len("xy"))
	if got.Input != want {
		t.Fatalf("Input=%d want %d (reasoning text+signature+opaque must participate in request sizing)", got.Input, want)
	}
}

func TestDefaultSizeEstimator_countsToolResultText(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("x", 4096)
	content := json.RawMessage(`{"ok":true}`)
	call := aipapi.Call{Messages: []aipapi.Message{{
		Role: aipapi.RoleTool,
		Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: "call_1",
			ToolName:   "lookup",
			Text:       text,
			Content:    content,
		}},
	}}}
	got := (modelcatalog.DefaultSizeEstimator{}).Estimate(t.Context(), call)
	want := int64(len(text) + len(content) + len("call_1") + len("lookup"))
	if !got.Available || got.Input != want {
		t.Fatalf("tool-result estimate=%+v, want %d bytes including literal text", got, want)
	}
}

func TestDefaultSizeEstimator_itemAuthorityCountsToolResult(t *testing.T) {
	t.Parallel()
	output := strings.Repeat("x", 4096)
	call := aipapi.Call{Items: []aipapi.Item{
		{
			Kind: aipapi.ItemKindMessage, ID: "msg-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "find the widget"}},
		},
		{
			Kind: aipapi.ItemKindToolCall, ID: "tc-1", Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{CallID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"q":"widget"}`)},
		},
		{
			Kind: aipapi.ItemKindToolResult, ID: "tr-1", Status: aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{CallID: "call-1", Name: "lookup", Output: output},
		},
	}}
	if err := call.Validate(); err != nil {
		t.Fatalf("item-authority fixture is invalid: %v", err)
	}
	got := (modelcatalog.DefaultSizeEstimator{}).Estimate(context.Background(), call)
	if !got.Available || got.Input < int64(len(output)) {
		t.Fatalf("item-authority estimate=%+v, want at least the %d-byte tool result", got, len(output))
	}
}

func TestDefaultSizeEstimator_manySmallItemAuthorityItemsStaySmall(t *testing.T) {
	t.Parallel()
	items := make([]aipapi.Item, 600)
	for i := range items {
		items[i] = aipapi.Item{
			Kind: aipapi.ItemKindMessage, ID: "m-" + strconv.Itoa(i), Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "x"}},
		}
	}
	call := aipapi.Call{Items: items}
	if err := call.Validate(); err != nil {
		t.Fatalf("small-item fixture is invalid: %v", err)
	}
	got := (modelcatalog.DefaultSizeEstimator{}).Estimate(context.Background(), call)
	if !got.Available || got.Input < 600 || got.Input >= 131072 {
		t.Fatalf("600 one-character items estimated as %+v; structural Go memory must not dominate prompt size", got)
	}
}
