package openairesponses_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// webSearchDeclarationsCarrierKey is the documented cross-module wire contract:
// the OpenAI Responses frontend preserves the hosted web-search declaration
// types under this opaque extension key, and the SIWC connector consumes the
// same literal. Both modules define it privately; the staged end-to-end proof
// (task 5.4) pins the two definitions together.
const webSearchDeclarationsCarrierKey = "openairesponses.web_search_declarations"

func decodeToolsRequest(t *testing.T, toolsJSON string) (*openairesponses.DecodedCreate, error) {
	t.Helper()
	body := fmt.Sprintf(`{"model":"gpt-4o-mini","stream":true,"tools":%s,"input":"search please"}`, toolsJSON)
	return openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
}

func requireCarrierTypes(t *testing.T, call *aipapi.Call, want []string) {
	t.Helper()
	raw, ok := call.Extensions[webSearchDeclarationsCarrierKey]
	if !ok {
		t.Fatalf("the call must carry the hosted web-search declarations extension, got %v", call.Extensions)
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the carrier must be a JSON array of declaration type strings: %v (%s)", err, raw)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("carrier declarations = %v, want %v", got, want)
	}
	// Type strings only: re-encoding the carrier must be the exact compact
	// array, so no declaration attribute, filter, location or body survived.
	canonical, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal carrier: %v", err)
	}
	if string(raw) != string(canonical) {
		t.Fatalf("the carrier must hold type strings only:\n got: %s\nwant: %s", raw, canonical)
	}
}

// TestDecodeCreate_HostedWebSearchDeclarationCarriage pins the closed table and
// the type-only carrier. A function tool named web_search is deliberately in the
// same catalogue: it stays canonical and is never classified by its name.
func TestDecodeCreate_HostedWebSearchDeclarationCarriage(t *testing.T) {
	t.Parallel()
	d, err := decodeToolsRequest(t, `[
		{"type":"function","name":"lookup","description":"Look up data","parameters":{"type":"object"}},
		{"type":"web_search_preview","user_location":{"type":"approximate","city":"San Francisco"},"search_context_size":"high"},
		{"type":"web_search"},
		{"type":"web_search_2025_08_26","filters":{"allowed_domains":["example.com"]}},
		{"type":"web_search_preview_2025_03_11"},
		{"type":"function","name":"web_search","parameters":{"type":"object"}}
	]`)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantCarrier := []string{
		"web_search_preview",
		"web_search",
		"web_search_2025_08_26",
		"web_search_preview_2025_03_11",
	}
	requireCarrierTypes(t, d.Call, wantCarrier)
	if len(d.Call.Tools) != 2 {
		t.Fatalf("function tools = %d, want 2 (hosted declarations are not function tools)", len(d.Call.Tools))
	}
	if d.Call.Tools[0].Name != "lookup" || d.Call.Tools[1].Name != "web_search" {
		t.Fatalf("function tool names = %q, %q", d.Call.Tools[0].Name, d.Call.Tools[1].Name)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatalf("the decoded call is not canonical: %v", err)
	}
}

// TestDecodeCreate_HostedWebSearchDeclarationRequiresExactExtension pins the
// admission consequence: the carrier is not round-trip metadata, so a backend
// must advertise opaque extensions AND the exact call extension.
func TestDecodeCreate_HostedWebSearchDeclarationRequiresExactExtension(t *testing.T) {
	t.Parallel()
	d, err := decodeToolsRequest(t, `[{"type":"web_search_preview"}]`)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	required := aipapi.DeriveProtocolRequirements(*d.Call)
	if !slices.Contains(required.Capabilities, aipapi.CapabilityOpaqueExtensions) {
		t.Fatalf("the carrier must require opaque_extensions, got %v", required.Capabilities)
	}
	exact := aipapi.ExtensionRequirement{Namespace: "call", Type: webSearchDeclarationsCarrierKey}
	if !slices.Contains(required.ExtensionTypes, exact) {
		t.Fatalf("the carrier must require the exact call extension %+v, got %+v", exact, required.ExtensionTypes)
	}

	// A generic opaque-extension backend without the exact type is rejected.
	without := aipapi.MatchRequirements(required, aipapi.ProtocolRequirements{
		Capabilities: []aipapi.Capability{aipapi.CapabilityOpaqueExtensions},
	}, aipapi.ReasoningReplaySupport{})
	if without.Kind != aipapi.NegotiationReject {
		t.Fatalf("a backend without the exact extension must be rejected, got %v", without.Kind)
	}
	if !slices.Contains(without.MissingExtensions, exact) {
		t.Fatalf("the rejection must name the missing exact extension %+v, got %+v", exact, without.MissingExtensions)
	}

	with := aipapi.MatchRequirements(required, aipapi.ProtocolRequirements{
		Capabilities:   []aipapi.Capability{aipapi.CapabilityOpaqueExtensions},
		ExtensionTypes: required.ExtensionTypes,
	}, aipapi.ReasoningReplaySupport{})
	if with.Kind != aipapi.NegotiationLossless {
		t.Fatalf("a backend with the exact extension must be admitted, got %v (%+v)", with.Kind, with)
	}
}

// TestDecodeCreate_WebSearchNameIsNotADeclaration proves the classification is
// by the declaration type, never by a tool/custom name and never by a
// namespace declaration.
func TestDecodeCreate_WebSearchNameIsNotADeclaration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		tools string
	}{
		{name: "typed function named web_search", tools: `[{"type":"function","name":"web_search","parameters":{"type":"object"}}]`},
		{name: "flat function named web_search", tools: `[{"name":"web_search","parameters":{"type":"object"}}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := decodeToolsRequest(t, tc.tools)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if _, ok := d.Call.Extensions[webSearchDeclarationsCarrierKey]; ok {
				t.Fatalf("a caller-executed function named web_search must not be carried as a hosted declaration")
			}
			if len(d.Call.Tools) != 1 || d.Call.Tools[0].Name != "web_search" {
				t.Fatalf("the function named web_search must stay canonical, got %+v", d.Call.Tools)
			}
		})
	}

	// A namespace declaration groups function/custom tools. It is not a
	// web-search synonym and the closed table must not admit it; namespace
	// projection is a separate frontend slice and must not enter this carrier.
	d, err := decodeToolsRequest(t, `[{"type":"namespace","name":"web_search","tools":[]}]`)
	if err != nil {
		t.Fatalf("an empty namespace declaration must decode: %v", err)
	}
	if _, ok := d.Call.Extensions[webSearchDeclarationsCarrierKey]; ok {
		t.Fatal("a namespace declaration must not be carried as a hosted web-search declaration")
	}
	if len(d.Call.Tools) != 0 {
		t.Fatalf("an empty namespace must not add function tools, got %+v", d.Call.Tools)
	}
}

// TestDecodeCreate_OtherHostedToolTypesStillFail pins the unchanged
// fail-closed set after the carriage widens decode for the closed web-search
// table only. Unknown/new web-search variants are not silently admitted.
func TestDecodeCreate_OtherHostedToolTypesStillFail(t *testing.T) {
	t.Parallel()
	hosted := []string{
		"file_search",
		"image_generation",
		"code_interpreter",
		"computer_use_preview",
		"mcp",
		"tool_search",
		"web_search_2099_01_01",
	}
	for _, typ := range hosted {
		t.Run(typ, func(t *testing.T) {
			t.Parallel()
			_, err := decodeToolsRequest(t, fmt.Sprintf(`[{"type":%q}]`, typ))
			if err == nil {
				t.Fatalf("the hosted declaration type %q must fail decode", typ)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf(`unsupported type %q`, typ)) {
				t.Fatalf("the refusal must name the unsupported type %q: %v", typ, err)
			}
		})
	}
}

// TestDecodeCreate_HostedWebSearchDeclarationBounds pins both limits: at most
// aipapi.MaxTools entries and a carrier value of at most 64 KiB.
func TestDecodeCreate_HostedWebSearchDeclarationBounds(t *testing.T) {
	t.Parallel()

	t.Run("entry count over aipapi.MaxTools", func(t *testing.T) {
		t.Parallel()
		items := make([]string, 0, aipapi.MaxTools+1)
		for range aipapi.MaxTools + 1 {
			items = append(items, `{"type":"web_search"}`)
		}
		_, err := decodeToolsRequest(t, "["+strings.Join(items, ",")+"]")
		if err == nil {
			t.Fatalf("more than %d tool declarations must fail decode", aipapi.MaxTools)
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("maximum is %d", aipapi.MaxTools)) {
			t.Fatalf("the refusal must name the entry bound: %v", err)
		}
	})

	t.Run("carrier value over 64 KiB", func(t *testing.T) {
		t.Parallel()
		// 2048 copies of the longest documented type is 65537 bytes of
		// compact JSON: inside the entry-count bound, outside the carrier bound.
		items := make([]string, 0, aipapi.MaxTools)
		for range aipapi.MaxTools {
			items = append(items, `{"type":"web_search_preview_2025_03_11"}`)
		}
		_, err := decodeToolsRequest(t, "["+strings.Join(items, ",")+"]")
		if err == nil {
			t.Fatal("a carrier value larger than 64 KiB must fail decode")
		}
		if !strings.Contains(err.Error(), "maximum is 65536") {
			t.Fatalf("the refusal must name the carrier byte bound: %v", err)
		}
	})

	t.Run("entry count and carrier value at their bounds", func(t *testing.T) {
		t.Parallel()
		items := make([]string, 0, aipapi.MaxTools)
		for range aipapi.MaxTools {
			items = append(items, `{"type":"web_search"}`)
		}
		d, err := decodeToolsRequest(t, "["+strings.Join(items, ",")+"]")
		if err != nil {
			t.Fatalf("a carrier at both documented bounds must decode: %v", err)
		}
		raw := d.Call.Extensions[webSearchDeclarationsCarrierKey]
		if len(raw) > 64<<10 {
			t.Fatalf("the retained carrier is %d bytes, want <= %d", len(raw), 64<<10)
		}
		var got []string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("carrier json: %v", err)
		}
		if len(got) != aipapi.MaxTools {
			t.Fatalf("carrier declarations = %d, want %d", len(got), aipapi.MaxTools)
		}
	})
}

// TestDecodeCreate_WebSearchHistoryIsRefused pins the no-fabrication rule on
// the input side: a model-authored web-search history item has no canonical
// equivalent, so the frontend refuses it before any backend sees the call. The
// omission path applies to declarations only; it must never drop history.
func TestDecodeCreate_WebSearchHistoryIsRefused(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
	}{
		{
			name:  "web_search_call",
			input: `[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"weather"}}]`,
		},
		{
			name:  "web_search_call_output",
			input: `[{"type":"web_search_call_output","call_id":"ws_1","output":"result"}]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := fmt.Sprintf(`{"model":"gpt-4o-mini","input":%s}`, tc.input)
			_, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
				RouteSelector: "stub:gpt-4o-mini",
			})
			if err == nil {
				t.Fatalf("model-authored web-search history item %q must be refused", tc.name)
			}
			if !strings.Contains(err.Error(), "unsupported input item type") {
				t.Fatalf("the refusal must identify the unsupported history item type: %v", err)
			}
		})
	}
}
