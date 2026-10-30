package aipapi_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestContentPartExtension_DerivesExactExtensionRequirement(t *testing.T) {
	t.Parallel()
	call := callWith(itemWith(extContentPart("acme:input_file", `{"type":"acme:input_file","file_url":"https://x/f"}`)))
	req := aipapi.DeriveProtocolRequirements(call)
	want := aipapi.ExtensionRequirement{Namespace: "acme", Type: "acme:input_file"}
	if !slices.Contains(req.ExtensionTypes, want) {
		t.Fatalf("expected extension requirement %+v in %+v", want, req.ExtensionTypes)
	}
	if !slices.Contains(req.Capabilities, aipapi.CapabilityOpaqueExtensions) {
		t.Fatalf("expected opaque_extensions capability, got %v", req.Capabilities)
	}
}

func TestContentPartExtension_DerivesRequirementFromStructuredToolResultPart(t *testing.T) {
	t.Parallel()
	call := callWith(aipapi.Item{
		Kind:   aipapi.ItemKindToolResult,
		ID:     "tr-1",
		Status: aipapi.ItemStatusCompleted,
		ToolResult: &aipapi.ToolResultItem{
			CallID: "call-1",
			Name:   "lookup",
			Parts:  []aipapi.ContentPart{extContentPart("acme.com/part", `{"type":"acme.com/part","payload":{"k":1}}`)},
		},
	})
	req := aipapi.DeriveProtocolRequirements(call)
	want := aipapi.ExtensionRequirement{Namespace: "acme.com", Type: "acme.com/part"}
	if !slices.Contains(req.ExtensionTypes, want) {
		t.Fatalf("expected tool-result extension requirement %+v in %+v", want, req.ExtensionTypes)
	}
}

func TestContentPartExtension_CanonicalMetadataIsUsedForNonWireSources(t *testing.T) {
	t.Parallel()
	part := aipapi.ContentPart{
		Kind: aipapi.ContentPartExtension,
		Extension: &aipapi.ExtensionContentPart{
			Namespace:   "custom",
			Type:        "acme:widget",
			Implementor: "acme-vendor",
			Data:        json.RawMessage(`{"type":"acme:widget"}`),
		},
	}
	req := aipapi.DeriveProtocolRequirements(callWith(itemWith(part)))
	want := aipapi.ExtensionRequirement{Namespace: "custom", Type: "acme:widget", Implementor: "acme-vendor"}
	if !slices.Contains(req.ExtensionTypes, want) {
		t.Fatalf("expected carried extension requirement %+v in %+v", want, req.ExtensionTypes)
	}
}

func TestContentPartExtension_DeriveExtensionNamespace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		typ  string
		want string
	}{
		{typ: "acme:input_file", want: "acme"},
		{typ: "acme.com/part", want: "acme.com"},
		{typ: "a:b/c", want: "a"},
		{typ: "", want: ""},
		{typ: "unprefixed", want: "unprefixed"},
	}
	for _, tt := range tests {
		if got := aipapi.DeriveExtensionNamespace(tt.typ); got != tt.want {
			t.Fatalf("DeriveExtensionNamespace(%q) = %q, want %q", tt.typ, got, tt.want)
		}
	}
}

func TestContentPartExtension_ValidationNamespaceAndImplementor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ext     *aipapi.ExtensionContentPart
		wantErr string
	}{
		{
			name: "valid namespace and implementor",
			ext: &aipapi.ExtensionContentPart{
				Namespace:   "acme",
				Type:        "acme:part",
				Implementor: "acme-vendor",
				Data:        json.RawMessage(`{"type":"acme:part"}`),
			},
		},
		{
			name: "namespace with whitespace rejected",
			ext: &aipapi.ExtensionContentPart{
				Namespace: "ac me",
				Type:      "acme:part",
				Data:      json.RawMessage(`{"type":"acme:part"}`),
			},
			wantErr: "namespace must not contain whitespace",
		},
		{
			name: "oversized namespace rejected",
			ext: &aipapi.ExtensionContentPart{
				Namespace: strings.Repeat("a", aipapi.MaxExtensionNamespaceBytes+1),
				Type:      "acme:part",
				Data:      json.RawMessage(`{"type":"acme:part"}`),
			},
			wantErr: "exceeds",
		},
		{
			name: "oversized implementor rejected",
			ext: &aipapi.ExtensionContentPart{
				Implementor: strings.Repeat("b", aipapi.MaxExtensionImplementorBytes+1),
				Type:        "acme:part",
				Data:        json.RawMessage(`{"type":"acme:part"}`),
			},
			wantErr: "exceeds",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := callWith(itemWith(aipapi.ContentPart{Kind: aipapi.ContentPartExtension, Extension: tt.ext})).Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected validation error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestContentPartExtension_JSONRoundTripPreservesNamespaceAndImplementor(t *testing.T) {
	t.Parallel()
	part := aipapi.ContentPart{
		Kind: aipapi.ContentPartExtension,
		Extension: &aipapi.ExtensionContentPart{
			Namespace:   "acme",
			Type:        "acme:part",
			Implementor: "acme-vendor",
			Data:        json.RawMessage(`{"type":"acme:part","payload":{"k":1}}`),
		},
	}
	call := callWith(itemWith(part))
	raw, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	var back aipapi.Call
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got := back.Items[0].Content[0].Extension
	if got == nil || got.Namespace != "acme" || got.Type != "acme:part" || got.Implementor != "acme-vendor" {
		t.Fatalf("json round-trip mismatch: %+v", got)
	}
	if string(got.Data) != `{"type":"acme:part","payload":{"k":1}}` {
		t.Fatalf("json round-trip data mismatch: %s", string(got.Data))
	}
}

func TestAdmitCandidate_ContentPartExtensionRequiresExactDeclaredType(t *testing.T) {
	t.Parallel()
	call := callWith(itemWith(extContentPart("acme:part", `{"type":"acme:part"}`)))
	call.Invocation = aipapi.Invocation{
		Operation:     aipapi.OperationOpenResponsesCreate,
		TransportMode: aipapi.TransportModeNonStreaming,
	}
	caps := aipapi.NewBackendCaps(defaultOrderedCaps()...)
	base := func() aipapi.CandidateAdmissionInput {
		return aipapi.CandidateAdmissionInput{
			Call:           call,
			Invocation:     call.Invocation,
			BackendCaps:    caps,
			TransportCaps:  aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{Operation: aipapi.OperationOpenResponsesCreate, Modes: []aipapi.TransportMode{aipapi.TransportModeNonStreaming}}),
			ReplaySupport:  aipapi.ReasoningReplaySupport{},
			DialectSupport: aipapi.DialectSupport{},
			ProjectionTarget: aipapi.DefaultLegacyProjectionTarget(
				caps, aipapi.ReasoningReplaySupport{},
			),
		}
	}

	// Capability is declared but no exact extension type: admission must reject
	// before any upstream work.
	without := base()
	res := aipapi.AdmitCandidate(without)
	if res.Kind != aipapi.NegotiationReject {
		t.Fatalf("expected reject without declared extension type, got %s", res.Kind)
	}
	if res.Requirements.Kind != aipapi.NegotiationReject {
		t.Fatalf("expected requirements reject, got %s", res.Requirements.Kind)
	}
	if len(res.Requirements.MissingExtensions) == 0 {
		t.Fatalf("expected a missing extension requirement, got %+v", res.Requirements)
	}

	// Declaring the exact namespace/type satisfies admission losslessly.
	with := base()
	with.DialectSupport = aipapi.DialectSupport{
		ExtensionTypes: []aipapi.ExtensionRequirement{{Namespace: "acme", Type: "acme:part"}},
	}
	res = aipapi.AdmitCandidate(with)
	if res.Kind != aipapi.NegotiationLossless {
		t.Fatalf("expected lossless with declared extension type, got %s err=%v", res.Kind, res.Err())
	}

	// A mismatched declared type still rejects.
	mismatch := base()
	mismatch.DialectSupport = aipapi.DialectSupport{
		ExtensionTypes: []aipapi.ExtensionRequirement{{Namespace: "other", Type: "other:part"}},
	}
	res = aipapi.AdmitCandidate(mismatch)
	if res.Kind != aipapi.NegotiationReject {
		t.Fatalf("expected reject with mismatched declared extension type, got %s", res.Kind)
	}
}

func defaultOrderedCaps() []aipapi.Capability {
	return []aipapi.Capability{
		aipapi.CapabilityStreaming,
		aipapi.CapabilityTools,
		aipapi.CapabilityVision,
		aipapi.CapabilityDocuments,
		aipapi.CapabilityReasoning,
		aipapi.CapabilityParallelToolCalls,
		aipapi.CapabilityOrderedItems,
		aipapi.CapabilityAssistantPhase,
		aipapi.CapabilityItemReferences,
		aipapi.CapabilityCompaction,
		aipapi.CapabilityOpaqueExtensions,
	}
}
