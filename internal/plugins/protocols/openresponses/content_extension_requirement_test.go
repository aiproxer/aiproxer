package openresponses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestContentPartExtension_NamespaceDerivedOnDecode proves that decoding a
// vendor-prefixed wire content part materializes the deterministic namespace
// and that re-encoding does not inject a redundant namespace field (the wire
// stays lossless).
func TestContentPartExtension_NamespaceDerivedOnDecode(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"input": []any{contentMessage(
			"user",
			map[string]any{"type": "acme:input_file", "file_url": "https://x/f"},
		)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, call, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	cp := call.Items[0].Content[0]
	if cp.Kind != aipapi.ContentPartExtension || cp.Extension == nil {
		t.Fatalf("expected extension content part, got %+v", cp)
	}
	if cp.Extension.Namespace != "acme" {
		t.Fatalf("derived namespace = %q, want acme", cp.Extension.Namespace)
	}
	encoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if strings.Contains(string(encoded), `"namespace"`) {
		t.Fatalf("re-encoded wire injected a derived namespace field: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"file_url":"https://x/f"`) {
		t.Fatalf("re-encoded wire lost file_url: %s", encoded)
	}
}

// Payload namespace and implementor are malicious opaque fields: they must not
// affect canonical identity, even though the payload remains lossless.
func TestContentPartExtension_WireNamespaceAndImplementorRoundTrip(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"input": []any{contentMessage(
			"user",
			map[string]any{"type": "acme:widget", "namespace": "custom", "implementor": "acme-vendor", "k": 1},
		)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, call, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	cp := call.Items[0].Content[0]
	if cp.Extension == nil || cp.Extension.Namespace != "acme" || cp.Extension.Implementor != "" {
		t.Fatalf("canonical extension metadata mismatch: %+v", cp.Extension)
	}
	encoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if !strings.Contains(string(encoded), `"namespace":"custom"`) || !strings.Contains(string(encoded), `"implementor":"acme-vendor"`) {
		t.Fatalf("opaque payload metadata was not preserved: %s", encoded)
	}
}

// TestContentPartExtension_ExplicitNamespaceMergedOnEncode proves that encoding
// never changes opaque Data, even when canonical metadata is present.
func TestContentPartExtension_ExplicitNamespaceMergedOnEncode(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{Items: []aipapi.Item{{
		Kind: aipapi.ItemKindMessage,
		Role: aipapi.RoleUser,
		Content: []aipapi.ContentPart{{
			Kind: aipapi.ContentPartExtension,
			Extension: &aipapi.ExtensionContentPart{
				Namespace:   "custom",
				Type:        "acme:part",
				Implementor: "acme-vendor",
				Data:        json.RawMessage(`{"type":"acme:part","payload":{"k":1}}`),
			},
		}},
	}}}
	encoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var out struct {
		Input []struct {
			Content []map[string]json.RawMessage `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("re-encoded request: %v", err)
	}
	if got := string(out.Input[0].Content[0]["namespace"]); got != "" {
		t.Fatalf("unexpected top-level namespace injection: %s", got)
	}
	if !strings.Contains(string(encoded), `"payload":{"k":1}`) {
		t.Fatalf("payload = %s, want preserved", encoded)
	}
}

func TestContentPartExtension_EncodePreservesOpaqueDataBytes(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"z":[1,  2], "namespace":"payload-ns", "implementor":"payload-impl"}`)
	part := encodeContentPart(aipapi.ContentPart{
		Kind: aipapi.ContentPartExtension,
		Extension: &aipapi.ExtensionContentPart{
			Type: "acme:part",
			Data: raw,
		},
	}, aipapi.RoleUser)
	if got := part.rawExtension; string(got) != string(raw) {
		t.Fatalf("opaque data bytes changed: got %s, want raw %s", got, raw)
	}
}

// TestContentPartExtension_ToolResultStructuredPartRoundTrip proves extension
// content parts inside structured tool-result output survive decode and encode.
func TestContentPartExtension_ToolResultStructuredPartRoundTrip(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"input": []any{contentMessage("user", map[string]any{"type": "input_text", "text": "hi"})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, call, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	call.Items = append(call.Items, aipapi.Item{
		Kind: aipapi.ItemKindToolResult,
		ID:   "tr-1",
		ToolResult: &aipapi.ToolResultItem{
			CallID: "call-1",
			Name:   "lookup",
			Parts: []aipapi.ContentPart{{
				Kind: aipapi.ContentPartExtension,
				Extension: &aipapi.ExtensionContentPart{
					Type: "acme:result",
					Data: json.RawMessage(`{"type":"acme:result","rows":2}`),
				},
			}},
		},
	})
	encoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var out struct {
		Input []struct {
			Type   string                       `json:"type"`
			Output []map[string]json.RawMessage `json:"output"`
		} `json:"input"`
	}
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("re-encoded request: %v", err)
	}
	var found *map[string]json.RawMessage
	for i := range out.Input {
		if out.Input[i].Type == "function_call_output" {
			for j := range out.Input[i].Output {
				if string(out.Input[i].Output[j]["type"]) == `"acme:result"` {
					found = &out.Input[i].Output[j]
				}
			}
		}
	}
	if found == nil {
		t.Fatalf("re-encoded tool result lost the structured extension part: %s", encoded)
	}
	if got := string((*found)["rows"]); got != "2" {
		t.Fatalf("tool-result extension payload rows = %s, want 2", got)
	}
}
