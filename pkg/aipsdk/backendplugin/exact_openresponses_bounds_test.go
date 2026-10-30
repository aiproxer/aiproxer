package backendplugin_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func exactItemInvocation(t *testing.T) backendplugin.Invocation {
	t.Helper()
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
		ItemAuthority: true,
		Items: []backendplugin.InvocationItem{{
			Kind: "message", ID: "m1", Status: "completed", Role: backendplugin.RoleUser,
			Content: []backendplugin.InvocationContentPart{{Kind: backendplugin.PartKindText, Text: strPtr("hi")}},
		}},
	}
	return inv
}

func TestExactOpenResponsesValidate_rejectsOversizedInlineFileData(t *testing.T) {
	t.Parallel()
	inv := exactItemInvocation(t)
	inv.Items[0].Content[0] = backendplugin.InvocationContentPart{
		Kind:     backendplugin.PartKindFileRef,
		FileRef:  strPtr("https://x/f"),
		FileData: strPtr(strings.Repeat("a", aipapi.MaxFileDataBytes+1)),
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected oversized file_data rejection")
	}
}

func TestExactOpenResponsesValidate_rejectsOversizedCompactionEncryptedContent(t *testing.T) {
	t.Parallel()
	inv := exactItemInvocation(t)
	inv.Items[0] = backendplugin.InvocationItem{
		Kind: "compaction", ID: "cmp-1", Status: "completed",
		Compaction: &backendplugin.InvocationCompactionItem{
			Dialect: "compact.v1", EncryptedContent: strings.Repeat("x", aipapi.MaxCompactionEncryptedContentBytes+1),
		},
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected oversized encrypted_content rejection")
	}
}

func TestExactOpenResponsesValidate_rejectsNullReasoningSummary(t *testing.T) {
	t.Parallel()
	inv := exactItemInvocation(t)
	dialect := string(aipapi.ReasoningDialectOpenAIResponsesItemV1)
	inv.Items[0] = backendplugin.InvocationItem{
		Kind: "reasoning", ID: "rs-1", Status: "completed",
		Reasoning: &backendplugin.InvocationReasoningItem{
			Dialect: &dialect,
			Summary: backendplugin.RawJSONNullValue(),
		},
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected null summary rejection")
	}
}

func TestExactOpenResponsesValidate_rejectsNonArrayReasoningFields(t *testing.T) {
	t.Parallel()
	for _, raw := range []backendplugin.RawJSON{
		backendplugin.RawJSONFromBytes([]byte(`{"type":"summary_text"}`)),
		backendplugin.RawJSONFromBytes([]byte(`"content"`)),
	} {
		inv := exactItemInvocation(t)
		dialect := string(aipapi.ReasoningDialectOpenAIResponsesItemV1)
		inv.Items[0] = backendplugin.InvocationItem{
			Kind: "reasoning", ID: "rs-1", Status: "completed",
			Reasoning: &backendplugin.InvocationReasoningItem{Dialect: &dialect, Summary: raw},
		}
		if err := inv.Validate(); err == nil {
			t.Fatalf("expected non-array reasoning field rejection for %#v", raw)
		}
	}
}

func TestExactOpenResponsesValidate_rejectsOversizedReasoningSummary(t *testing.T) {
	t.Parallel()
	inv := exactItemInvocation(t)
	dialect := string(aipapi.ReasoningDialectOpenAIResponsesItemV1)
	big := `["` + strings.Repeat("x", int(backendplugin.DefaultMaxRawJSONBytes)) + `"]`
	inv.Items[0] = backendplugin.InvocationItem{
		Kind: "reasoning", ID: "rs-1", Status: "completed",
		Reasoning: &backendplugin.InvocationReasoningItem{
			Dialect: &dialect,
			Summary: backendplugin.RawJSONFromBytes([]byte(big)),
		},
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected oversized reasoning summary rejection")
	}
}

func TestExactOpenResponsesValidate_extensionPartRequiresTypeAndData(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		part backendplugin.InvocationContentPart
	}{
		{name: "missing type", part: backendplugin.InvocationContentPart{Kind: backendplugin.PartKindExtension, ExtensionData: backendplugin.RawJSONFromBytes([]byte(`{}`))}},
		{name: "missing data", part: backendplugin.InvocationContentPart{Kind: backendplugin.PartKindExtension, ExtensionType: strPtr("acme:part")}},
		{name: "conflicting payload", part: backendplugin.InvocationContentPart{
			Kind: backendplugin.PartKindExtension, ExtensionType: strPtr("acme:part"),
			ExtensionData: backendplugin.RawJSONFromBytes([]byte(`{}`)), Text: strPtr("x"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inv := exactItemInvocation(t)
			inv.Items[0].Content[0] = tc.part
			if err := inv.Validate(); err == nil {
				t.Fatal("expected extension part rejection")
			}
		})
	}
}

func TestExactOpenResponsesValidate_fileRefRequiresRefOrData(t *testing.T) {
	t.Parallel()
	inv := exactItemInvocation(t)
	inv.Items[0].Content[0] = backendplugin.InvocationContentPart{Kind: backendplugin.PartKindFileRef}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected file_ref requirement rejection")
	}
}

func TestExactOpenResponsesValidate_acceptsFileDataOnly(t *testing.T) {
	t.Parallel()
	inv := exactItemInvocation(t)
	inv.Items[0].Content[0] = backendplugin.InvocationContentPart{
		Kind: backendplugin.PartKindFileRef, FileData: strPtr("aGVsbG8="), FileName: strPtr("minimal.pdf"),
	}
	if err := inv.Validate(); err != nil {
		t.Fatalf("file_data-only part rejected: %v", err)
	}
}
