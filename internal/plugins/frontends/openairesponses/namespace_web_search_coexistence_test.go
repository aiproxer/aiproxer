package openairesponses_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
)

// TestDecodeCreate_NamespaceAndHostedWebSearchCoexistWithoutCrossContamination
// pins the coexistence boundary between the namespace-tool projection and the
// hosted web-search carrier:
//
//   - namespace members remain flattened function tools, including a member
//     whose bare name is web_search;
//   - only the closed hosted web-search table enters the opaque carrier;
//   - no namespace declaration or namespace member is reclassified as a hosted
//     web-search declaration, and vice versa.
func TestDecodeCreate_NamespaceAndHostedWebSearchCoexistWithoutCrossContamination(t *testing.T) {
	t.Parallel()
	body := `{
		"model":"gpt-4o",
		"stream":true,
		"input":"delegate and search",
		"tools":[
			{"type":"function","name":"lookup","parameters":{"type":"object"}},
			{"type":"namespace","name":"multi_agent_v1","description":"Delegate control.","tools":[
				{"type":"function","name":"close_agent","parameters":{"type":"object"}},
				{"type":"function","name":"web_search","parameters":{"type":"object"}}
			]},
			{"type":"web_search_preview","user_location":{"type":"approximate","city":"San Francisco"}}
		]
	}`
	decoded, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o",
	})
	if err != nil {
		t.Fatalf("decode the combined namespace + hosted web-search request: %v", err)
	}

	raw, ok := decoded.Call.Extensions["openairesponses.web_search_declarations"]
	if !ok {
		t.Fatalf("the hosted declaration must reach its documented carrier, got %v", decoded.Call.Extensions)
	}
	var declarations []string
	if err := json.Unmarshal(raw, &declarations); err != nil {
		t.Fatalf("the carrier must be a JSON array of type strings: %v (%s)", err, raw)
	}
	if !slices.Equal(declarations, []string{"web_search_preview"}) {
		t.Fatalf("carrier declarations = %v, want only the hosted web-search declaration", declarations)
	}

	names := make([]string, 0, len(decoded.Call.Tools))
	for _, tool := range decoded.Call.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"lookup", "multi_agent_v1__close_agent", "multi_agent_v1__web_search"}
	if !slices.Equal(names, want) {
		t.Fatalf("canonical tool names = %v, want namespace members flattened beside the top-level function %v", names, want)
	}
	if err := decoded.Call.Validate(); err != nil {
		t.Fatalf("the combined canonical call is invalid: %v", err)
	}
}

// TestProfile_NamespaceAndHostedWebSearchBothDeclineBeforeWire pins the wire
// fast path for the combined shape: a raw namespace declaration together with a
// raw hosted web-search declaration must never be raw-forwarded. Each guard is
// also pinned independently by its own suite; this case proves they compose
// without either shape slipping through.
func TestProfile_NamespaceAndHostedWebSearchBothDeclineBeforeWire(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"model":"gpt-4o",
		"stream":true,
		"input":"delegate and search",
		"tools":[
			{"type":"namespace","name":"multi_agent_v1","tools":[
				{"type":"function","name":"close_agent","parameters":{"type":"object"}}
			]},
			{"type":"web_search_preview"}
		]
	}`)
	_, err := openairesponses.NewProfile().CompileProof(t.Context(), defaultProofInput(body, "stub:gpt-4o", nil))
	if err == nil {
		t.Fatal("the combined namespace + hosted web-search request must decline to canonical processing")
	}
	if !errors.Is(err, openairesponses.ErrHostedWebSearchRequiresCanonicalDecode) &&
		!errors.Is(err, openairesponses.ErrNamespaceToolsRequiresCanonicalDecode) {
		t.Fatalf("the combined request must be declined by a tool-shape fast-path guard, got %v", err)
	}
}
