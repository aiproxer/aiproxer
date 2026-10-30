package backendplugin_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func exactExactReasoningPart() *aipapi.ReasoningPart {
	return &aipapi.ReasoningPart{
		Dialect:                 aipapi.ReasoningDialectOpenAIResponsesItemV1,
		Summary:                 json.RawMessage(`[{"type":"summary_text","text":"s"}]`),
		SummaryPresent:          true,
		Content:                 json.RawMessage(`[{"type":"output_text","text":"t"}]`),
		ContentPresent:          true,
		EncryptedContent:        json.RawMessage("null"),
		EncryptedContentPresent: true,
	}
}

func TestConvertCharacterization_ExactOpenResponsesInvocationRoundTrip(t *testing.T) {
	t.Parallel()

	fileData := "aGVsbG8="
	extType := "acme:input_file"
	extData := json.RawMessage(`{"type":"acme:input_file","file_url":"https://x/f"}`)
	dialect := string(aipapi.ReasoningDialectOpenAIResponsesItemV1)

	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		PromptCacheKey: "conv-1",
		ItemAuthority:  true,
		Items: []backendplugin.InvocationItem{
			{
				Kind: "message", ID: "file-1", Status: "completed", Role: backendplugin.RoleUser,
				Content: []backendplugin.InvocationContentPart{{
					Kind: backendplugin.PartKindFileRef, FileRef: strPtr("https://x/report.pdf"),
					FileName: strPtr("report.pdf"), FileMIME: strPtr("application/pdf"), FileData: &fileData,
				}},
			},
			{
				Kind: "message", ID: "ext-1", Status: "completed", Role: backendplugin.RoleUser,
				Content: []backendplugin.InvocationContentPart{{
					Kind: backendplugin.PartKindExtension, ExtensionType: &extType,
					ExtensionData: backendplugin.RawJSONFromBytes(extData),
				}},
			},
			{
				Kind: "message", ID: "rs-part-1", Status: "completed", Role: backendplugin.RoleAssistant,
				Content: []backendplugin.InvocationContentPart{{
					Kind: backendplugin.PartKindReasoning,
					Reasoning: &backendplugin.InvocationReasoningPart{
						Dialect:          &dialect,
						Summary:          backendplugin.RawJSONFromBytes([]byte(`[{"type":"summary_text","text":"s"}]`)),
						Content:          backendplugin.RawJSONFromBytes([]byte(`[{"type":"output_text","text":"t"}]`)),
						EncryptedContent: backendplugin.RawJSONNullValue(),
					},
				}},
			},
			{
				Kind: "reasoning", ID: "rs-item-1", Status: "completed",
				Reasoning: &backendplugin.InvocationReasoningItem{
					Dialect:          &dialect,
					Summary:          backendplugin.RawJSONFromBytes([]byte(`[]`)),
					EncryptedContent: backendplugin.RawJSONNullValue(),
				},
			},
			{
				Kind: "compaction", ID: "cmp-1", Status: "completed",
				Compaction: &backendplugin.InvocationCompactionItem{
					Dialect: "compact.v1", EncryptedContent: "gAAAAABpayload",
				},
			},
		},
	}
	if err := inv.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	p, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatal(err)
	}
	back, err := backendplugin.InvocationFromProto(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inv, back) {
		t.Fatalf("exact OpenResponses invocation round-trip mismatch:\nwant=%#v\ngot=%#v", inv, back)
	}
}

func TestConvertCharacterization_ExactOpenResponsesEventRoundTrip(t *testing.T) {
	t.Parallel()
	dialect := string(aipapi.ReasoningDialectOpenAIResponsesItemV1)
	ev := &backendplugin.CanonicalEvent{
		Kind:                      backendplugin.EventReasoningPart,
		ReasoningDialect:          &dialect,
		ReasoningOpaque:           []byte(`{"id":"rs_1","type":"reasoning","summary":[]}`),
		ReasoningSummary:          backendplugin.RawJSONFromBytes([]byte(`[]`)),
		ReasoningContent:          backendplugin.RawJSONFromBytes([]byte(`[{"type":"output_text","text":"t"}]`)),
		ReasoningEncryptedContent: backendplugin.RawJSONNullValue(),
	}
	wire, err := backendplugin.CanonicalEventToProto(ev)
	if err != nil {
		t.Fatal(err)
	}
	back, err := backendplugin.CanonicalEventFromProto(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ev, back) {
		t.Fatalf("exact OpenResponses event round-trip mismatch:\nwant=%#v\ngot=%#v", ev, back)
	}
}

func TestConvertCharacterization_LegacyPartExactReasoningRoundTrip(t *testing.T) {
	t.Parallel()
	dialect := string(aipapi.ReasoningDialectOpenAIResponsesItemV1)
	part := backendplugin.Part{
		Kind:                      backendplugin.PartKindReasoning,
		ReasoningText:             strPtr("text"),
		ReasoningDialect:          &dialect,
		ReasoningOpaque:           backendplugin.RawJSONFromBytes([]byte(`{"id":"rs_1"}`)),
		ReasoningSummary:          backendplugin.RawJSONFromBytes([]byte(`[]`)),
		ReasoningContent:          backendplugin.RawJSONFromBytes([]byte(`[{"type":"output_text","text":"t"}]`)),
		ReasoningEncryptedContent: backendplugin.RawJSONNullValue(),
	}
	msg := backendplugin.Message{Role: backendplugin.RoleAssistant, Parts: []backendplugin.Part{part}}
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
		Messages: []backendplugin.Message{msg},
		Options:  backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	if err := inv.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	wire, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatal(err)
	}
	back, err := backendplugin.InvocationFromProto(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inv, back) {
		t.Fatalf("legacy part round-trip mismatch:\nwant=%#v\ngot=%#v", inv, back)
	}
}

func TestConvertCharacterization_LegacyPartExactReasoningPresenceAndEmptyValues(t *testing.T) {
	t.Parallel()
	dialect := string(aipapi.ReasoningDialectOpenAIResponsesItemV1)
	for _, tc := range []struct {
		name                        string
		summary, content, encrypted backendplugin.RawJSON
	}{
		{name: "absent", summary: backendplugin.RawJSONAbsentValue(), content: backendplugin.RawJSONAbsentValue(), encrypted: backendplugin.RawJSONAbsentValue()},
		{name: "null encrypted", summary: backendplugin.RawJSONFromBytes([]byte(`[]`)), content: backendplugin.RawJSONFromBytes([]byte(`[]`)), encrypted: backendplugin.RawJSONNullValue()},
		{name: "empty arrays", summary: backendplugin.RawJSONFromBytes([]byte(`[]`)), content: backendplugin.RawJSONFromBytes([]byte(`[]`)), encrypted: backendplugin.RawJSONFromBytes([]byte(`""`))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			part := backendplugin.Part{
				Kind: backendplugin.PartKindReasoning, ReasoningDialect: &dialect,
				ReasoningText: strPtr(" exact "), ReasoningSummary: tc.summary, ReasoningContent: tc.content, ReasoningEncryptedContent: tc.encrypted,
			}
			inv := backendplugin.Invocation{
				RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
				Messages: []backendplugin.Message{{Role: backendplugin.RoleAssistant, Parts: []backendplugin.Part{part}}},
			}
			wire, err := backendplugin.InvocationToProto(inv)
			if err != nil {
				t.Fatal(err)
			}
			back, err := backendplugin.InvocationFromProto(wire)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(inv, back) {
				t.Fatalf("presence/value mismatch: want %#v got %#v", inv, back)
			}
		})
	}
}

func TestApplyOrderedItemWire_preservesExactOpenResponsesFields(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		PromptCacheKey: "conv-1",
		Items: []aipapi.Item{
			{
				Kind: aipapi.ItemKindMessage, ID: "file-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{{
					Kind: aipapi.ContentPartFileRef, FileRef: "https://x/report.pdf", FileData: "aGVsbG8=",
					FileName: "report.pdf", FileMIME: "application/pdf",
				}},
			},
			{
				Kind: aipapi.ItemKindMessage, ID: "ext-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{{
					Kind:      aipapi.ContentPartExtension,
					Extension: &aipapi.ExtensionContentPart{Type: "acme:input_file", Data: json.RawMessage(`{"type":"acme:input_file"}`)},
				}},
			},
			{
				Kind: aipapi.ItemKindMessage, ID: "rs-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleAssistant,
				Content: []aipapi.ContentPart{{
					Kind:      aipapi.ContentPartReasoning,
					Reasoning: exactExactReasoningPart(),
				}},
			},
			{
				Kind: aipapi.ItemKindReasoning, ID: "rs-item-1", Status: aipapi.ItemStatusCompleted,
				Reasoning: &aipapi.ReasoningItem{Reasoning: exactExactReasoningPart()},
			},
			{
				Kind: aipapi.ItemKindCompaction, ID: "cmp-1", Status: aipapi.ItemStatusCompleted,
				Compaction: &aipapi.CompactionItem{Dialect: "compact.v1", EncryptedContent: "gAAAAABpayload"},
			},
		},
	}
	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: strPtr("x")}}}},
	}
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, exactNegotiation()); err != nil {
		t.Fatal(err)
	}
	if inv.PromptCacheKey != "conv-1" {
		t.Fatalf("prompt_cache_key=%q", inv.PromptCacheKey)
	}
	if !backendplugin.HasItemAuthorityInvocation(inv) {
		t.Fatal("expected item authority")
	}
	filePart := inv.Items[0].Content[0]
	if filePart.FileData == nil || *filePart.FileData != "aGVsbG8=" {
		t.Fatalf("file_data not preserved: %#v", filePart)
	}
	extPart := inv.Items[1].Content[0]
	if extPart.ExtensionType == nil || *extPart.ExtensionType != "acme:input_file" || extPart.ExtensionData.State() != backendplugin.RawJSONValue {
		t.Fatalf("extension part not preserved: %#v", extPart)
	}
	rsPart := inv.Items[2].Content[0]
	if rsPart.Reasoning == nil || rsPart.Reasoning.Summary.State() != backendplugin.RawJSONValue ||
		rsPart.Reasoning.Content.State() != backendplugin.RawJSONValue ||
		rsPart.Reasoning.EncryptedContent.State() != backendplugin.RawJSONNull {
		t.Fatalf("reasoning exact fields not preserved: %#v", rsPart.Reasoning)
	}
	rsItem := inv.Items[3].Reasoning
	if rsItem == nil || rsItem.Summary.State() != backendplugin.RawJSONValue ||
		rsItem.EncryptedContent.State() != backendplugin.RawJSONNull {
		t.Fatalf("reasoning item exact fields not preserved: %#v", rsItem)
	}
	cmp := inv.Items[4].Compaction
	if cmp == nil || cmp.EncryptedContent != "gAAAAABpayload" {
		t.Fatalf("compaction encrypted_content not preserved: %#v", cmp)
	}
}

func TestCallFromInvocation_preservesExactOpenResponsesFields(t *testing.T) {
	t.Parallel()

	dialect := string(aipapi.ReasoningDialectOpenAIResponsesItemV1)
	fileData := "aGVsbG8="
	extType := "acme:input_file"
	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		PromptCacheKey: "conv-1",
		ItemAuthority:  true,
		Items: []backendplugin.InvocationItem{
			{
				Kind: "message", ID: "file-1", Status: "completed", Role: backendplugin.RoleUser,
				Content: []backendplugin.InvocationContentPart{{
					Kind: backendplugin.PartKindFileRef, FileRef: strPtr("https://x/f"), FileData: &fileData,
				}},
			},
			{
				Kind: "message", ID: "ext-1", Status: "completed", Role: backendplugin.RoleUser,
				Content: []backendplugin.InvocationContentPart{{
					Kind: backendplugin.PartKindExtension, ExtensionType: &extType,
					ExtensionData: backendplugin.RawJSONFromBytes([]byte(`{"type":"acme:input_file"}`)),
				}},
			},
			{
				Kind: "reasoning", ID: "rs-1", Status: "completed",
				Reasoning: &backendplugin.InvocationReasoningItem{
					Dialect:          &dialect,
					Summary:          backendplugin.RawJSONFromBytes([]byte(`[]`)),
					EncryptedContent: backendplugin.RawJSONNullValue(),
				},
			},
			{
				Kind: "compaction", ID: "cmp-1", Status: "completed",
				Compaction: &backendplugin.InvocationCompactionItem{Dialect: "compact.v1", EncryptedContent: "gAAAAABpayload"},
			},
		},
	}
	call, err := backendplugin.CallFromInvocation(inv)
	if err != nil {
		t.Fatal(err)
	}
	if call.PromptCacheKey != "conv-1" {
		t.Fatalf("prompt_cache_key=%q", call.PromptCacheKey)
	}
	if call.Items[0].Content[0].FileData != "aGVsbG8=" {
		t.Fatalf("file_data=%q", call.Items[0].Content[0].FileData)
	}
	ext := call.Items[1].Content[0].Extension
	if ext == nil || ext.Type != "acme:input_file" || string(ext.Data) != `{"type":"acme:input_file"}` {
		t.Fatalf("extension=%#v", ext)
	}
	rp := call.Items[2].Reasoning.Reasoning
	if rp.SummaryPresent == false || rp.EncryptedContentPresent == false || string(rp.EncryptedContent) != "null" {
		t.Fatalf("reasoning exact fields=%#v", rp)
	}
	if call.Items[3].Compaction == nil || call.Items[3].Compaction.EncryptedContent != "gAAAAABpayload" {
		t.Fatalf("compaction=%#v", call.Items[3].Compaction)
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("canonical validate: %v", err)
	}
}
