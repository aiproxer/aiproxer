package backendplugin_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestApplyItemAuthorityMetadata_usesFirstClassFields(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Items: []aipapi.Item{{
			Kind:   aipapi.ItemKindMessage,
			ID:     "msg-1",
			Status: aipapi.ItemStatusCompleted,
			Role:   aipapi.RoleUser,
			Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "hello"},
			},
		}},
	}
	inv := backendplugin.Invocation{
		RequestID: "req-1", AttemptID: "att-1", ALegID: "a", BLegID: "b", CanonicalModelID: "model",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: new("hello")}}}},
	}
	if err := backendplugin.ApplyCallWireMetadata(&inv, call, nil); err != nil {
		t.Fatal(err)
	}
	if !backendplugin.HasItemAuthorityInvocation(inv) {
		t.Fatal("expected item authority invocation")
	}
	req, ok := backendplugin.ProtocolRequirementsFromInvocation(inv)
	if !ok {
		t.Fatal("expected protocol requirements")
	}
	if !containsCap(req.Capabilities, aipapi.CapabilityOrderedItems) {
		t.Fatalf("requirements=%#v", req)
	}
}

func TestProtocolRequirementsFromMetadata_invalidJSON(t *testing.T) {
	t.Parallel()

	_, ok := backendplugin.ProtocolRequirementsFromMetadata(map[string]string{
		backendplugin.MetaProtocolRequirements: "{",
	})
	if ok {
		t.Fatal("expected decode failure")
	}
}

func TestCapabilitySummaryFromAipapi_mapsDialectCapabilities(t *testing.T) {
	t.Parallel()

	summary := backendplugin.CapabilitySummaryFromAipapi(aipapi.NewBackendCaps(
		aipapi.CapabilityTools,
		aipapi.CapabilityReasoningReplay,
	))
	if !summary.Tools || !summary.ReasoningReplay {
		t.Fatalf("summary=%#v", summary)
	}
}

func TestApplyItemAuthorityMetadata_noOpForLegacyAuthority(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
	}
	inv := backendplugin.Invocation{}
	backendplugin.ApplyItemAuthorityMetadata(&inv, call)
	if backendplugin.HasItemAuthorityInvocation(inv) {
		t.Fatal("legacy authority must not set item authority")
	}
	_ = json.RawMessage(`{}`)
}

func containsCap(caps []aipapi.Capability, want aipapi.Capability) bool {
	return slices.Contains(caps, want)
}
