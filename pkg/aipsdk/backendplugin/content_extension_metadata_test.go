package backendplugin

import (
	"encoding/json"
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"google.golang.org/protobuf/proto"
)

func TestContentExtensionMetadataPreservedThroughInvocationProto(t *testing.T) {
	t.Parallel()
	ns, impl, typ := "declared.namespace", "vendor-implementor", "derived:part"
	call := aipapi.Call{Items: []aipapi.Item{{
		ID:     "msg_1",
		Kind:   aipapi.ItemKindMessage,
		Status: aipapi.ItemStatusCompleted,
		Role:   aipapi.RoleUser,
		Content: []aipapi.ContentPart{{
			Kind: aipapi.ContentPartExtension,
			Extension: &aipapi.ExtensionContentPart{
				Namespace: ns, Type: typ, Implementor: impl,
				Data: json.RawMessage(`{"type":"derived:part","payload":true}`),
			},
		}},
	}}}

	dto := Invocation{}
	ApplyOrderedItemWire(&dto, call)
	dto.ItemAuthority = true
	if got := dto.Items[0].Content[0].ExtensionNamespace; got == nil || *got != ns {
		t.Fatalf("namespace metadata lost: %#v", got)
	}
	if got := dto.Items[0].Content[0].ExtensionImplementor; got == nil || *got != impl {
		t.Fatalf("implementor metadata lost: %#v", got)
	}

	partWire, err := invocationContentPartToProto(dto.Items[0].Content[0])
	if err != nil {
		t.Fatal(err)
	}
	wire := &backendpluginv1.Invocation{Items: []*backendpluginv1.InvocationItem{{
		Kind: "message", Id: "msg_1", Status: "completed", Role: backendpluginv1.Role_ROLE_USER,
		Content: []*backendpluginv1.InvocationContentPart{partWire},
	}}}
	encoded, err := proto.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var decoded backendpluginv1.Invocation
	if err := proto.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.GetItems()[0].GetContent()[0]
	if got.GetExtensionNamespace() != ns || got.GetExtensionImplementor() != impl {
		t.Fatalf("proto metadata mismatch: namespace=%q implementor=%q", got.GetExtensionNamespace(), got.GetExtensionImplementor())
	}
	if string(got.GetExtensionData().GetJson()) != `{"type":"derived:part","payload":true}` {
		t.Fatalf("extension wire data changed: %s", got.GetExtensionData().GetJson())
	}

	partDTO, err := invocationContentPartFromProto(decoded.GetItems()[0].GetContent()[0])
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := contentPartFromInvocationDTO(partDTO, "test")
	if err != nil {
		t.Fatal(err)
	}
	ext := canonical.Extension
	if ext.Namespace != ns || ext.Implementor != impl || ext.Type != typ {
		t.Fatalf("canonical metadata mismatch: %+v", ext)
	}
}
