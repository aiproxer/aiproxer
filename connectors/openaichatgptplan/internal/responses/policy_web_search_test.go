package responses_test

// Hosted web-search declaration carriage proofs for task 4.3.
//
// The OpenAI Responses frontend preserves the closed declaration table under a
// bounded opaque extension. Only this connector consumes it: the declaration
// is omitted in interoperable posture with one operator-visible Omission per
// entry, or refused before projection/network in strict posture. The carrier is
// validated as unsupported capability, never as invalid user context, and no
// omission ever applies to history: an unrepresentable model-authored search
// item is refused instead of dropped.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const webSearchDeclarationsExtensionKey = "openairesponses.web_search_declarations"

var documentedWebSearchDeclarations = []string{
	"web_search",
	"web_search_preview",
	"web_search_2025_08_26",
	"web_search_preview_2025_03_11",
}

func webSearchDeclarationField(index int, declarationType string) string {
	return fmt.Sprintf("Extensions[%q][%d] (type %q)", webSearchDeclarationsExtensionKey, index, declarationType)
}

func policyCallWithWebSearchCarrier(t *testing.T, carrier string) aipapi.Call {
	t.Helper()
	call := policyMinimalCall()
	call.Extensions = map[string]json.RawMessage{
		webSearchDeclarationsExtensionKey: json.RawMessage(carrier),
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("the carrier fixture must be a valid canonical call: %v", err)
	}
	return call
}

func policyMarshalWebSearchCarrier(t *testing.T, declarations []string) string {
	t.Helper()
	raw, err := json.Marshal(declarations)
	if err != nil {
		t.Fatalf("marshal carrier: %v", err)
	}
	return string(raw)
}

// TestPreviewPolicyOmitsHostedWebSearchDeclarations proves the interoperable
// path: the closed table is consumed, removed from the clone, and every entry
// is recorded with the carrier path, declared type, wire field and reason. A
// caller-executed function named web_search stays canonical.
func TestPreviewPolicyOmitsHostedWebSearchDeclarations(t *testing.T) {
	t.Parallel()

	call := policyCallWithWebSearchCarrier(t, policyMarshalWebSearchCarrier(t, documentedWebSearchDeclarations))
	call.Tools = []aipapi.ToolDef{
		{Name: "lookup", Description: "Look up data", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "web_search", Description: "Caller-executed search", Parameters: json.RawMessage(`{"type":"object"}`)},
	}

	admission := policyAdmit(t, responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeInteroperable)
	want := make([]responses.Omission, 0, len(documentedWebSearchDeclarations))
	for i, declarationType := range documentedWebSearchDeclarations {
		want = append(want, responses.Omission{
			Field:  webSearchDeclarationField(i, declarationType),
			Wire:   "tools",
			Reason: responses.PolicyReasonWebSearchDeclaration,
		})
	}
	policyRequireOmissions(t, admission, want)

	// The carrier is consumed before projection: the key and the declaration
	// types must not reach the provider request. The function tool named
	// web_search is ordinary canonical content and must remain.
	if bytes.Contains(admission.Request.Body, []byte(webSearchDeclarationsExtensionKey)) {
		t.Fatalf("the admitted request must not carry the declaration carrier: %s", admission.Request.Body)
	}
	var body struct {
		Input []struct {
			Type  string `json:"type"`
			Tools []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"input"`
	}
	if err := json.Unmarshal(admission.Request.Body, &body); err != nil {
		t.Fatalf("admitted body: %v", err)
	}
	var admittedTools []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	for _, item := range body.Input {
		if item.Type == "additional_tools" {
			admittedTools = item.Tools
		}
	}
	if len(admittedTools) != 2 || admittedTools[0].Name != "lookup" || admittedTools[1].Name != "web_search" {
		t.Fatalf("the admitted tools = %+v, want the two caller-executed functions", admittedTools)
	}
	for _, tool := range admittedTools {
		if tool.Type != "function" {
			t.Fatalf("the admitted request must not carry a hosted declaration, got type %q", tool.Type)
		}
	}

	// The policy reads a clone and never mutates the canonical call.
	if len(call.Extensions) != 1 {
		t.Fatalf("the policy mutated the caller's extensions: %v", call.Extensions)
	}
	if _, ok := call.Extensions[webSearchDeclarationsExtensionKey]; !ok {
		t.Fatalf("the policy removed the carrier from the caller's call: %v", call.Extensions)
	}

	// Strict posture must refuse the same request before projection/network.
	strictAdmission, err := responses.AdmitCall(responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeStrict)
	policyRequireRefusal(t, err, responses.PolicyReasonWebSearchDeclaration, webSearchDeclarationField(0, documentedWebSearchDeclarations[0]))
	policyRequireZeroAdmission(t, strictAdmission, "strict hosted web-search declaration")
}

// TestPreviewPolicyRefusesMalformedWebSearchCarrier proves every malformed
// carrier is a deterministic unsupported-capability refusal in both postures,
// never invalid user context and never a silent drop.
func TestPreviewPolicyRefusesMalformedWebSearchCarrier(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		carrier string
	}{
		{name: "null", carrier: `null`},
		{name: "empty value", carrier: ``},
		{name: "object", carrier: `{}`},
		{name: "empty array", carrier: `[]`},
		{name: "string", carrier: `"web_search"`},
		{name: "number entry", carrier: `[1]`},
		{name: "object entry", carrier: `["web_search",{"type":"web_search"}]`},
		{name: "null entry", carrier: `["web_search",null]`},
		{name: "unknown variant", carrier: `["web_search_future"]`},
		{name: "namespace is not a declaration", carrier: `["namespace"]`},
		{name: "empty type", carrier: `[""]`},
		{name: "whitespace type", carrier: `[" web_search"]`},
	}
	for _, tc := range cases {
		for _, mode := range []responses.PolicyMode{responses.PolicyModeInteroperable, responses.PolicyModeStrict} {
			t.Run(tc.name+"/"+string(mode), func(t *testing.T) {
				t.Parallel()
				call := policyCallWithWebSearchCarrier(t, tc.carrier)
				admission, err := responses.AdmitCall(responses.ProjectInput{Call: call, Model: fixtureModel}, mode)
				requireWebSearchCarrierRefusal(t, err)
				policyRequireZeroAdmission(t, admission, tc.name)
			})
		}
	}

	t.Run("entry count over aipapi.MaxTools", func(t *testing.T) {
		t.Parallel()
		declarations := make([]string, 0, aipapi.MaxTools+1)
		for range aipapi.MaxTools + 1 {
			declarations = append(declarations, "web_search")
		}
		call := policyCallWithWebSearchCarrier(t, policyMarshalWebSearchCarrier(t, declarations))
		admission, err := responses.AdmitCall(responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeInteroperable)
		requireWebSearchCarrierRefusal(t, err)
		policyRequireZeroAdmission(t, admission, "over-bound declaration count")
	})

	t.Run("carrier value over 64 KiB", func(t *testing.T) {
		t.Parallel()
		// 2048 copies of the longest documented type is 65537 bytes of compact
		// JSON: inside the entry-count bound, outside the carrier byte bound.
		declarations := make([]string, 0, aipapi.MaxTools)
		for range aipapi.MaxTools {
			declarations = append(declarations, "web_search_preview_2025_03_11")
		}
		call := policyCallWithWebSearchCarrier(t, policyMarshalWebSearchCarrier(t, declarations))
		admission, err := responses.AdmitCall(responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeInteroperable)
		requireWebSearchCarrierRefusal(t, err)
		policyRequireZeroAdmission(t, admission, "over-bound carrier value")
	})
}

func requireWebSearchCarrierRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("the malformed hosted web-search carrier must be refused")
	}
	if !errors.Is(err, responses.ErrRequestNotAdmissible) {
		t.Fatalf("the malformed carrier must be a policy refusal, got %v", err)
	}
	var refusal *responses.PolicyError
	if !errors.As(err, &refusal) {
		t.Fatalf("the refusal must carry a PolicyError, got %T: %v", err, err)
	}
	if refusal.Reason != responses.PolicyReasonWebSearchDeclaration {
		t.Fatalf("the refusal reason = %q, want %q", refusal.Reason, responses.PolicyReasonWebSearchDeclaration)
	}
	if !strings.HasPrefix(refusal.Field, fmt.Sprintf("Extensions[%q]", webSearchDeclarationsExtensionKey)) {
		t.Fatalf("the refusal must name the carrier path, got %q", refusal.Field)
	}
	if refusal.Detail == "" {
		t.Fatal("the refusal must explain itself")
	}
	_ = requireClassified(t, err, responses.ErrorClassUnsupportedCapability)
}

// TestPreviewPolicyRefusesRawHostedWebSearchOnWire is the wire-side defense:
// a hosted web-search declaration that bypasses the carrier is refused on the
// exact projected document that would reach the network.
func TestPreviewPolicyRefusesRawHostedWebSearchOnWire(t *testing.T) {
	t.Parallel()
	for _, declarationType := range documentedWebSearchDeclarations {
		t.Run("top-level/"+declarationType, func(t *testing.T) {
			t.Parallel()
			body := policyDocumentWithTopLevelTools(t, []any{
				map[string]any{"type": "function", "name": "lawful", "parameters": map[string]any{"type": "object"}},
				map[string]any{"type": declarationType},
			})
			admission, err := responses.AdmitProjectedRequest(policyRequestFromBody(t, body))
			policyRequireRefusal(t, err, responses.PolicyReasonUnsupportedTool, "tools[1].type")
			policyRequireZeroAdmission(t, admission, "top-level raw hosted web-search declaration")
			if !strings.Contains(err.Error(), declarationType) {
				t.Fatalf("the refusal must name the hosted type %q: %v", declarationType, err)
			}
		})
		t.Run("additional_tools/"+declarationType, func(t *testing.T) {
			t.Parallel()
			body := policyDocumentWithTools(t, []any{
				map[string]any{"type": "function", "name": "lawful", "parameters": map[string]any{"type": "object"}},
				map[string]any{"type": declarationType},
			})
			admission, err := responses.AdmitProjectedRequest(policyRequestFromBody(t, body))
			policyRequireRefusal(t, err, responses.PolicyReasonUnsupportedTool, "input[0].tools[1].type")
			policyRequireZeroAdmission(t, admission, "additional_tools raw hosted web-search declaration")
		})
	}
}

// TestPreviewPolicyDoesNotClassifyHostedWebSearchByName proves the classification
// is declaration-table and type driven, never name driven: a caller-executed
// function tool named web_search remains admissible with zero omissions.
func TestPreviewPolicyDoesNotClassifyHostedWebSearchByName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"web_search", "namespace"} {
		for _, mode := range []responses.PolicyMode{responses.PolicyModeInteroperable, responses.PolicyModeStrict} {
			t.Run(name+"/"+string(mode), func(t *testing.T) {
				t.Parallel()
				call := policyMinimalCall()
				call.Tools = []aipapi.ToolDef{{
					Name:        name,
					Description: "Caller-executed tool",
					Parameters:  json.RawMessage(`{"type":"object"}`),
				}}
				admission := policyAdmit(t, responses.ProjectInput{Call: call, Model: fixtureModel}, mode)
				policyRequireOmissions(t, admission, nil)
				if !bytes.Contains(admission.Request.Body, []byte(`"`+name+`"`)) {
					t.Fatalf("the caller-executed tool %q must remain canonical: %s", name, admission.Request.Body)
				}
			})
		}
	}
}

// TestPreviewPolicyRefusesHostedWebSearchHistory proves no omission ever
// applies to history. The canonical model has no equivalent for a hosted
// web_search_call output item, so an input history item carrying it is refused
// instead of being dropped, reordered or synthesized away.
func TestPreviewPolicyRefusesHostedWebSearchHistory(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "search for the weather"}},
			},
			{
				Kind: aipapi.ItemKindExtension,
				Extension: &aipapi.OpaqueExtension{
					Namespace: "openai",
					Type:      "web_search_call",
					Direction: "response",
					Data:      json.RawMessage(`{"action":{"type":"search","query":"weather"}}`),
				},
			},
		},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("the history fixture must be a valid canonical call: %v", err)
	}
	for _, mode := range []responses.PolicyMode{responses.PolicyModeInteroperable, responses.PolicyModeStrict} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			admission, err := responses.AdmitCall(responses.ProjectInput{Call: call, Model: fixtureModel}, mode)
			policyRequireProjectionRefusal(t, err, responses.ProjectionReasonItemKind, "Items[1]")
			policyRequireZeroAdmission(t, admission, "hosted web-search history")
		})
	}

	// The refusal is not name based: an ordinary caller-executed tool call and
	// result named web_search are supported trajectory content.
	callerExecuted := aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "look it up"}},
			},
			{
				Kind:     aipapi.ItemKindToolCall,
				ToolCall: &aipapi.ToolCallItem{CallID: "call_1", Name: "web_search", Arguments: json.RawMessage(`{"query":"weather"}`)},
			},
			{
				Kind:       aipapi.ItemKindToolResult,
				ToolResult: &aipapi.ToolResultItem{CallID: "call_1", Name: "web_search", Output: "sunny"},
			},
		},
	}
	if err := callerExecuted.Validate(); err != nil {
		t.Fatalf("the caller-executed trajectory fixture must be valid: %v", err)
	}
	admission := policyAdmit(t, responses.ProjectInput{Call: callerExecuted, Model: fixtureModel}, responses.PolicyModeStrict)
	policyRequireOmissions(t, admission, nil)
	var body struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(admission.Request.Body, &body); err != nil {
		t.Fatalf("admitted body: %v", err)
	}
	foundCall, foundResult := false, false
	for _, item := range body.Input {
		var itemType string
		_ = json.Unmarshal(item["type"], &itemType)
		switch itemType {
		case "function_call":
			foundCall = true
		case "function_call_output":
			foundResult = true
		}
	}
	if !foundCall || !foundResult {
		t.Fatalf("the caller-executed web_search trajectory must stay projected, got %+v", body.Input)
	}
}
