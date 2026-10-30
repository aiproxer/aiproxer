package backendplugin_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func exactNegotiation() backendplugin.Negotiation {
	return backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorExactOpenResponsesFields,
		EnabledFeatures: []string{backendplugin.FeatureOrderedItems, backendplugin.FeatureExactOpenResponsesFields},
	}
}

func oldMinorNegotiation() backendplugin.Negotiation {
	return backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
		EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
	}
}

func TestRequireExactOpenResponsesABISupport_plainCallBackwardCompatible(t *testing.T) {
	t.Parallel()
	for _, neg := range []backendplugin.Negotiation{
		{Compatible: true, NegotiatedMinor: 0},
		oldMinorNegotiation(),
		exactNegotiation(),
	} {
		call := aipapi.Call{
			PromptCacheKey: "",
			Items: []aipapi.Item{{
				Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}},
			}},
		}
		if err := backendplugin.RequireExactOpenResponsesABISupport(neg, call); err != nil {
			t.Fatalf("neg=%#v plain call rejected: %v", neg, err)
		}
	}
}

func TestRequireExactOpenResponsesABISupport_rejectsAtOldMinor(t *testing.T) {
	t.Parallel()
	calls := []struct {
		name string
		call aipapi.Call
	}{
		{
			name: "prompt_cache_key",
			call: aipapi.Call{PromptCacheKey: "k1"},
		},
		{
			name: "inline_file_data",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartFileRef, FileData: "aGVsbG8="}},
			}}},
		},
		{
			name: "extension_content_part",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{{
					Kind:      aipapi.ContentPartExtension,
					Extension: &aipapi.ExtensionContentPart{Type: "acme:input_file", Data: json.RawMessage(`{"type":"acme:input_file"}`)},
				}},
			}}},
		},
		{
			name: "reasoning_exact_fields",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind: aipapi.ItemKindReasoning, ID: "rs-1", Status: aipapi.ItemStatusCompleted,
				Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{
					Dialect:        aipapi.ReasoningDialectOpenAIResponsesItemV1,
					Summary:        json.RawMessage(`[]`),
					SummaryPresent: true,
				}},
			}}},
		},
		{
			name: "compaction_encrypted_content",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind: aipapi.ItemKindCompaction, ID: "cmp-1", Status: aipapi.ItemStatusCompleted,
				Compaction: &aipapi.CompactionItem{Dialect: "compact.v1", EncryptedContent: "gAAAAABpayload"},
			}}},
		},
		{
			name: "legacy_part_exact_reasoning",
			call: aipapi.Call{Messages: []aipapi.Message{{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
					Dialect:                 aipapi.ReasoningDialectOpenAIResponsesItemV1,
					EncryptedContent:        json.RawMessage("null"),
					EncryptedContentPresent: true,
				}}},
			}}},
		},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := backendplugin.RequireExactOpenResponsesABISupport(oldMinorNegotiation(), tc.call)
			if !errors.Is(err, backendplugin.ErrExactOpenResponsesUnsupported) {
				t.Fatalf("err=%v want ErrExactOpenResponsesUnsupported", err)
			}
		})
	}
}

func TestRequireExactOpenResponsesABISupport_rejectsMissingFeature(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{PromptCacheKey: "k1"}
	neg := backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorExactOpenResponsesFields,
		EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
	}
	if err := backendplugin.RequireExactOpenResponsesABISupport(neg, call); !errors.Is(err, backendplugin.ErrExactOpenResponsesUnsupported) {
		t.Fatalf("err=%v want ErrExactOpenResponsesUnsupported", err)
	}
}

func TestRequireExactOpenResponsesABISupport_acceptsAtMinor3(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		PromptCacheKey: "k1",
		Items: []aipapi.Item{{
			Kind: aipapi.ItemKindCompaction, ID: "cmp-1", Status: aipapi.ItemStatusCompleted,
			Compaction: &aipapi.CompactionItem{Dialect: "compact.v1", EncryptedContent: "gAAAAABpayload"},
		}},
	}
	if err := backendplugin.RequireExactOpenResponsesABISupport(exactNegotiation(), call); err != nil {
		t.Fatalf("minor-3 call rejected: %v", err)
	}
}

func TestRequireExactOpenResponsesEventABISupport(t *testing.T) {
	t.Parallel()
	summary := backendplugin.RawJSONFromBytes([]byte(`[]`))
	tests := []struct {
		name    string
		neg     backendplugin.Negotiation
		ev      *backendplugin.CanonicalEvent
		wantErr bool
	}{
		{
			name: "plain_event_old_minor",
			neg:  oldMinorNegotiation(),
			ev:   &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta, Delta: new("x")},
		},
		{
			name:    "exact_fields_old_minor",
			neg:     oldMinorNegotiation(),
			ev:      &backendplugin.CanonicalEvent{Kind: backendplugin.EventReasoningPart, ReasoningSummary: summary},
			wantErr: true,
		},
		{
			name: "exact_fields_minor3",
			neg:  exactNegotiation(),
			ev: &backendplugin.CanonicalEvent{
				Kind:                      backendplugin.EventReasoningPart,
				ReasoningSummary:          summary,
				ReasoningEncryptedContent: backendplugin.RawJSONNullValue(),
			},
		},
		{
			name: "nil_event",
			neg:  oldMinorNegotiation(),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := backendplugin.RequireExactOpenResponsesEventABISupport(tc.neg, tc.ev)
			if tc.wantErr {
				if !errors.Is(err, backendplugin.ErrExactOpenResponsesUnsupported) {
					t.Fatalf("err=%v want ErrExactOpenResponsesUnsupported", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
		})
	}
}

func TestRequireExactOpenResponsesABISupport_incompatibleNegotiation(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{PromptCacheKey: "k1"}
	if err := backendplugin.RequireExactOpenResponsesABISupport(backendplugin.Negotiation{Compatible: false}, call); !errors.Is(err, backendplugin.ErrExactOpenResponsesUnsupported) {
		t.Fatalf("err=%v want ErrExactOpenResponsesUnsupported", err)
	}
}

func TestCanonicalEventConversionRejectsInvalidExactShape(t *testing.T) {
	t.Parallel()
	_, err := backendplugin.CanonicalEventToProto(&backendplugin.CanonicalEvent{
		Kind:             backendplugin.EventReasoningPart,
		ReasoningSummary: backendplugin.RawJSONFromBytes([]byte(`{"not":"an array"}`)),
	})
	if err == nil {
		t.Fatal("expected invalid summary shape rejection")
	}
}
