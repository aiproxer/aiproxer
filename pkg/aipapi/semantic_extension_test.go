package aipapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestSemanticExtension_PreservesPresenceAndCloneOwnership(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello"}}}},
		SemanticExtensions: []aipapi.SemanticExtension{
			{Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: aipapi.SemanticExtensionValue, Data: json.RawMessage(`"cache-1"`)},
			{Namespace: "aip", Type: "nullable_hint", Implementor: "proxy", Direction: "request", Presence: aipapi.SemanticExtensionNull},
		},
	}
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := aipapi.CloneCall(call)
	clone.SemanticExtensions[0].Data[0] = 'X'
	if string(call.SemanticExtensions[0].Data) != `"cache-1"` {
		t.Fatalf("semantic extension data was aliased: %s", call.SemanticExtensions[0].Data)
	}
	if clone.SemanticExtensions[1].Presence != aipapi.SemanticExtensionNull || len(clone.SemanticExtensions[1].Data) != 0 {
		t.Fatalf("null presence was not preserved: %#v", clone.SemanticExtensions[1])
	}
}

func TestSemanticExtension_ExactRequirementsAndAdmission(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages:           []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello"}}}},
		SemanticExtensions: []aipapi.SemanticExtension{{Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: aipapi.SemanticExtensionValue, Data: json.RawMessage(`"cache-1"`)}},
	}
	required := aipapi.DeriveProtocolRequirements(call)
	if len(required.ExtensionTypes) != 1 || required.ExtensionTypes[0].Type != "prompt_cache_key" {
		t.Fatalf("requirements=%+v", required)
	}
	if result := aipapi.MatchRequirements(required, aipapi.ProtocolRequirements{}, aipapi.ReasoningReplaySupport{}); result.Kind == aipapi.NegotiationLossless {
		t.Fatal("expected unsupported semantic carrier identity to fail admission")
	}
	if result := aipapi.MatchRequirements(required, aipapi.ProtocolRequirements{
		Capabilities:   []aipapi.Capability{aipapi.CapabilityOpaqueExtensions},
		ExtensionTypes: required.ExtensionTypes,
	}, aipapi.ReasoningReplaySupport{}); result.Kind != aipapi.NegotiationLossless {
		t.Fatalf("expected exact identity admission to remain lossless: %+v", result)
	}
}

func TestSemanticExtension_RejectsOversizeAndInvalidPresence(t *testing.T) {
	t.Parallel()
	base := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello"}}}}}
	for name, ext := range map[string]aipapi.SemanticExtension{
		"missing identity": {Presence: aipapi.SemanticExtensionValue, Data: json.RawMessage(`true`)},
		"invalid json":     {Namespace: "aip", Type: "x", Presence: aipapi.SemanticExtensionValue, Data: json.RawMessage("{")},
		"oversize":         {Namespace: "aip", Type: "x", Presence: aipapi.SemanticExtensionValue, Data: json.RawMessage(strings.Repeat("x", aipapi.MaxSemanticExtensionDataBytes+1))},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.CloneCall(base)
			call.SemanticExtensions = []aipapi.SemanticExtension{ext}
			if err := call.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestSemanticExtension_RejectsUnboundedIdentityDuplicateAndEnvelope(t *testing.T) {
	t.Parallel()
	base := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello"}}}}}
	for name, ext := range map[string]aipapi.SemanticExtension{
		"closed direction": {Namespace: "aip", Type: "x", Implementor: "proxy", Direction: "other", Presence: aipapi.SemanticExtensionNull},
		"envelope":         {Namespace: "aip", Type: "x", Implementor: "proxy", Direction: "request", Presence: aipapi.SemanticExtensionValue, Data: json.RawMessage(`{"request":{"messages":[]}}`)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.CloneCall(base)
			call.SemanticExtensions = []aipapi.SemanticExtension{ext}
			if err := call.Validate(); err == nil {
				t.Fatal("expected semantic carrier validation failure")
			}
		})
	}
	duplicate := aipapi.CloneCall(base)
	duplicate.SemanticExtensions = []aipapi.SemanticExtension{
		{Namespace: "aip", Type: "x", Implementor: "proxy", Direction: "request", Presence: aipapi.SemanticExtensionNull},
		{Namespace: "aip", Type: "x", Implementor: "proxy", Direction: "request", Presence: aipapi.SemanticExtensionNull},
	}
	if err := duplicate.Validate(); err == nil {
		t.Fatal("expected duplicate semantic carrier identity rejection")
	}
}

func TestSemanticExtension_DoesNotBecomeLegacyProjectionTunnel(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Items:              []aipapi.Item{{Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hello"}}}},
		SemanticExtensions: []aipapi.SemanticExtension{{Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: aipapi.SemanticExtensionValue, Data: json.RawMessage(`"cache-1"`)}},
	}
	_, err := aipapi.ProjectItemsToLegacyView(call, aipapi.DefaultLegacyProjectionTarget(aipapi.NewBackendCaps(aipapi.CapabilityOpaqueExtensions), aipapi.ReasoningReplaySupport{}))
	if err == nil || !aipapi.IsProjectionError(err) {
		t.Fatalf("expected semantic residual to be rejected, got %v", err)
	}
}
