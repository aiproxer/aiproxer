package backendplugin_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestInvocationItemsValidate_rejectsUnknownKind(t *testing.T) {
	t.Parallel()
	inv := backendplugin.Invocation{
		ItemAuthority: true,
		Items: []backendplugin.InvocationItem{{
			Kind: "evil", ID: "x", Status: "completed",
		}},
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestInvocationItemsValidate_rejectsConflictingContentPayload(t *testing.T) {
	t.Parallel()
	text := "hi"
	ref := "img://1"
	inv := backendplugin.Invocation{
		ItemAuthority: true,
		Items: []backendplugin.InvocationItem{{
			Kind: "message", ID: "m1", Status: "completed", Role: backendplugin.RoleUser,
			Content: []backendplugin.InvocationContentPart{{
				Kind: backendplugin.PartKindText, Text: &text, ImageRef: &ref,
			}},
		}},
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestInvocationItemsValidate_rejectsInvalidRawJSON(t *testing.T) {
	t.Parallel()
	inv := backendplugin.Invocation{
		ItemAuthority: true,
		Items: []backendplugin.InvocationItem{{
			Kind: "tool_call", ID: "tc1", Status: "completed",
			ToolCall: &backendplugin.InvocationToolCall{
				CallID: "c1", Name: "fn",
				Arguments: backendplugin.RawJSONFromBytes([]byte("{")),
			},
		}},
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestInvocationItemsValidate_rejectsUnknownCapability(t *testing.T) {
	t.Parallel()
	inv := backendplugin.Invocation{
		ItemAuthority: true,
		Items: []backendplugin.InvocationItem{{
			Kind: "message", ID: "m1", Status: "completed", Role: backendplugin.RoleUser,
			Content: []backendplugin.InvocationContentPart{{Kind: backendplugin.PartKindText, Text: strPtr("hi")}},
		}},
		ProtocolRequirements: backendplugin.ProtocolRequirements{
			Capabilities: []string{"not_a_real_capability"},
		},
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestInvocationItemsValidate_rejectsDuplicateItemIDs(t *testing.T) {
	t.Parallel()
	inv := backendplugin.Invocation{
		ItemAuthority: true,
		Items: []backendplugin.InvocationItem{
			{Kind: "message", ID: "dup", Status: "completed", Role: backendplugin.RoleUser, Content: []backendplugin.InvocationContentPart{{Kind: backendplugin.PartKindText, Text: strPtr("a")}}},
			{Kind: "message", ID: "dup", Status: "completed", Role: backendplugin.RoleUser, Content: []backendplugin.InvocationContentPart{{Kind: backendplugin.PartKindText, Text: strPtr("b")}}},
		},
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestInvocationToProto_roundTripsSpecializedItemFields(t *testing.T) {
	t.Parallel()

	dialect := string(aipapi.ReasoningDialectOpenAIChatTextV1)
	text := "thought"
	sig := "sig-1"
	mime := "image/png"
	fname := "doc.pdf"
	dir := "input"
	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		ItemAuthority: true,
		Items: []backendplugin.InvocationItem{
			{
				Kind: "message", ID: "dev-1", Status: "completed", Role: backendplugin.RoleDeveloper,
				Content: []backendplugin.InvocationContentPart{{
					Kind: backendplugin.PartKindImageRef, ImageRef: strPtr("img://1"), ImageMIME: &mime,
				}},
			},
			{
				Kind: "reasoning", ID: "rs-1", Status: "completed",
				Reasoning: &backendplugin.InvocationReasoningItem{Dialect: &dialect, Text: &text, Signature: &sig},
			},
			{
				Kind: "compaction", ID: "cmp-1", Status: "completed",
				Compaction: &backendplugin.InvocationCompactionItem{
					EncapsulatedID: "enc-1", Dialect: "compact.v1", Opaque: backendplugin.RawJSONFromBytes([]byte(`{"ok":true}`)),
				},
			},
			{
				Kind: "extension", ID: "ext-1", Status: "completed",
				Extension: &backendplugin.InvocationExtensionItem{
					Namespace: "ns", Type: "beta", Direction: dir,
					Opaque: backendplugin.RawJSONFromBytes([]byte(`{"k":1}`)),
				},
			},
			{
				Kind: "message", ID: "file-1", Status: "completed", Role: backendplugin.RoleUser,
				Content: []backendplugin.InvocationContentPart{{
					Kind: backendplugin.PartKindFileRef, FileRef: strPtr("file://1"), FileMIME: &mime, FileName: &fname,
				}},
			},
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
	if back.Items[0].Role != backendplugin.RoleDeveloper {
		t.Fatalf("developer role=%q", back.Items[0].Role)
	}
	if back.Items[1].Reasoning == nil || back.Items[1].Reasoning.Signature == nil || *back.Items[1].Reasoning.Signature != sig {
		t.Fatalf("reasoning=%#v", back.Items[1].Reasoning)
	}
	if back.Items[2].Compaction == nil || back.Items[2].Compaction.EncapsulatedID != "enc-1" {
		t.Fatalf("compaction=%#v", back.Items[2].Compaction)
	}
	if back.Items[3].Extension == nil || back.Items[3].Extension.Direction != dir {
		t.Fatalf("extension=%#v", back.Items[3].Extension)
	}
	if back.Items[4].Content[0].FileName == nil || *back.Items[4].Content[0].FileName != fname {
		t.Fatalf("file part=%#v", back.Items[4].Content[0])
	}
}

func TestInvocationItemsValidate_rejectsDeepJSON(t *testing.T) {
	t.Parallel()
	deep := strings.Repeat(`{"a":`, 70) + "1" + strings.Repeat("}", 70)
	inv := backendplugin.Invocation{
		ItemAuthority: true,
		Items: []backendplugin.InvocationItem{{
			Kind: "tool_call", ID: "tc1", Status: "completed",
			ToolCall: &backendplugin.InvocationToolCall{
				CallID: "c1", Name: "fn",
				Arguments: backendplugin.RawJSONFromBytes([]byte(deep)),
			},
		}},
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected error")
	}
}
