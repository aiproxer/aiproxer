package backendplugin_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestRequireOrderedItemABISupport_rejectsOldMinor(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{Items: []aipapi.Item{{
		Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}},
	}}}
	err := backendplugin.RequireOrderedItemABISupport(backendplugin.Negotiation{
		Compatible: true, NegotiatedMinor: 0, EnabledFeatures: []string{},
	}, call)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRequireOrderedItemABISupport_acceptsMinor2(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{Items: []aipapi.Item{{
		Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}},
	}}}
	err := backendplugin.RequireOrderedItemABISupport(backendplugin.Negotiation{
		Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
		EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
	}, call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInvocationToProto_roundTripsSpecializedItems(t *testing.T) {
	t.Parallel()

	dialect := "compact.v1"
	text := "thought"
	refID := "msg-prev"
	extData := json.RawMessage(`{"k":1}`)
	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		ItemAuthority: true,
		Items: []backendplugin.InvocationItem{
			{Kind: "item_reference", ID: "ref-1", Status: "completed", ItemReference: &backendplugin.InvocationItemReference{ID: refID}},
			{Kind: "reasoning", ID: "rs-1", Status: "completed", Reasoning: &backendplugin.InvocationReasoningItem{Dialect: &dialect, Text: &text}},
			{Kind: "compaction", ID: "cmp-1", Status: "completed", Compaction: &backendplugin.InvocationCompactionItem{Dialect: dialect, Opaque: backendplugin.RawJSONFromBytes(json.RawMessage(`{"ok":true}`))}},
			{Kind: "extension", ID: "ext-1", Status: "completed", Extension: &backendplugin.InvocationExtensionItem{Namespace: "ns", Type: "beta", Opaque: backendplugin.RawJSONFromBytes(extData)}},
		},
		ProtocolRequirements: backendplugin.ProtocolRequirements{
			Capabilities: []string{string(aipapi.CapabilityOrderedItems)},
		},
	}
	p, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatal(err)
	}
	back, err := backendplugin.InvocationFromProto(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Items) != 4 {
		t.Fatalf("items=%#v", back.Items)
	}
	if back.Items[0].ItemReference == nil || back.Items[0].ItemReference.ID != refID {
		t.Fatalf("reference=%#v", back.Items[0].ItemReference)
	}
	if back.Items[1].Reasoning == nil || back.Items[1].Reasoning.Dialect == nil {
		t.Fatalf("reasoning=%#v", back.Items[1].Reasoning)
	}
	if back.Items[2].Compaction == nil || back.Items[2].Compaction.Dialect != dialect {
		t.Fatalf("compaction=%#v", back.Items[2].Compaction)
	}
	if back.Items[3].Extension == nil || back.Items[3].Extension.Type != "beta" {
		t.Fatalf("extension=%#v", back.Items[3].Extension)
	}
}
