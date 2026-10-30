package backendplugin_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestSemanticExtension_ABIRoundTripPreservesValueAndNull(t *testing.T) {
	t.Parallel()
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "al", BLegID: "bl", CanonicalModelID: "m",
		SemanticExtensions: []backendplugin.SemanticExtension{
			{Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: backendplugin.SemanticExtensionValue, Data: backendplugin.RawJSONFromBytes([]byte(`"cache-1"`))},
			{Namespace: "aip", Type: "nullable_hint", Implementor: "proxy", Direction: "request", Presence: backendplugin.SemanticExtensionNull},
		},
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: strPtr("hello")}}}},
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
		t.Fatalf("semantic extension round-trip mismatch: want=%#v got=%#v", inv, back)
	}
}

func TestSemanticExtension_RejectsUnboundedIdentityAndEnvelopeData(t *testing.T) {
	t.Parallel()
	base := backendplugin.Invocation{RequestID: "r", AttemptID: "a", ALegID: "al", BLegID: "bl", CanonicalModelID: "m", Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: strPtr("hello")}}}}}
	for _, ext := range []backendplugin.SemanticExtension{
		{Namespace: "aip", Type: "x", Implementor: "proxy", Direction: "other", Presence: backendplugin.SemanticExtensionNull},
		{Namespace: "aip", Type: "x", Implementor: "proxy", Direction: "request", Presence: backendplugin.SemanticExtensionValue, Data: backendplugin.RawJSONFromBytes([]byte(`{"request":{"messages":[]}}`))},
	} {
		inv := base
		inv.SemanticExtensions = []backendplugin.SemanticExtension{ext}
		if _, err := backendplugin.InvocationToProto(inv); err == nil {
			t.Fatalf("expected bounded semantic carrier validation failure for %#v", ext)
		}
	}
}

func TestSemanticExtension_LegacyPromptCacheAliasBridgesToOneCarrier(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		SemanticExtensions: []aipapi.SemanticExtension{{Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: aipapi.SemanticExtensionValue, Data: json.RawMessage(`"cache-1"`)}},
		Messages:           []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello"}}}},
	}
	inv := backendplugin.Invocation{RequestID: "r", AttemptID: "a", ALegID: "al", BLegID: "bl", CanonicalModelID: "m", Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: strPtr("hello")}}}}}
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, semanticNegotiation()); err != nil {
		t.Fatal(err)
	}
	if inv.PromptCacheKey != "" || len(inv.SemanticExtensions) != 1 || string(inv.SemanticExtensions[0].Data.Bytes()) != `"cache-1"` {
		t.Fatalf("legacy alias was not bridged to one semantic authority: %#v", inv)
	}
	back, err := backendplugin.CallFromInvocation(inv)
	if err != nil {
		t.Fatal(err)
	}
	if back.PromptCacheKey != "cache-1" {
		t.Fatalf("source-compatible PromptCacheKey alias not restored: %q", back.PromptCacheKey)
	}
}

func TestSemanticExtension_Minor6AliasDoesNotRequireLegacyExactFeature(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		PromptCacheKey: "cache-1",
		Messages:       []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello"}}}},
	}
	neg := backendplugin.Negotiation{Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorSemanticExtensions, EnabledFeatures: []string{backendplugin.FeatureSemanticExtensions}}
	if err := backendplugin.RequireExactOpenResponsesABISupport(neg, call); err != nil {
		t.Fatalf("minor-6 semantic alias should not require legacy exact feature: %v", err)
	}
	inv := backendplugin.Invocation{RequestID: "r", AttemptID: "a", ALegID: "al", BLegID: "bl", CanonicalModelID: "m", Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: strPtr("hello")}}}}}
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, neg); err != nil || inv.PromptCacheKey != "" || len(inv.SemanticExtensions) != 1 {
		t.Fatalf("minor-6 alias bridge failed: err=%v inv=%#v", err, inv)
	}
}

func TestSemanticExtension_RejectsConflictingPromptCacheAuthorities(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		PromptCacheKey:     "legacy",
		SemanticExtensions: []aipapi.SemanticExtension{{Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: aipapi.SemanticExtensionValue, Data: json.RawMessage(`"carrier"`)}},
		Messages:           []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello"}}}},
	}
	inv := backendplugin.Invocation{RequestID: "r", AttemptID: "a", ALegID: "al", BLegID: "bl", CanonicalModelID: "m", Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: strPtr("hello")}}}}}
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, semanticNegotiation()); err == nil {
		t.Fatal("expected conflicting prompt cache authorities to fail closed")
	}
}

func TestSemanticExtension_UnknownRequiredFeatureAndOldABI(t *testing.T) {
	t.Parallel()
	_, err := backendplugin.Negotiate(
		backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorSemanticExtensions, DisableTransportRetries: true, Features: []backendplugin.Feature{{Name: backendplugin.FeatureSemanticExtensions, Required: true}}},
		backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorExactOpenResponsesFields, DisableTransportRetries: true, Features: []backendplugin.Feature{{Name: backendplugin.FeatureExactOpenResponsesFields}}},
	)
	if !errors.Is(err, backendplugin.ErrUnknownRequiredFeature) {
		t.Fatalf("expected unknown required semantic feature rejection, got %v", err)
	}
	neg, err := backendplugin.Negotiate(
		backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorSemanticExtensions, DisableTransportRetries: true, Features: []backendplugin.Feature{{Name: backendplugin.FeatureExactOpenResponsesFields}}},
		backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorExactOpenResponsesFields, DisableTransportRetries: true, Features: []backendplugin.Feature{{Name: backendplugin.FeatureExactOpenResponsesFields}}},
	)
	if err != nil || !neg.Compatible || neg.NegotiatedMinor != backendplugin.ProtocolMinorExactOpenResponsesFields {
		t.Fatalf("v1.3 compatibility changed: neg=%+v err=%v", neg, err)
	}
}

func TestSemanticExtension_NegotiationMinor6IsIndependentFromV1_5(t *testing.T) {
	t.Parallel()
	base := backendplugin.ProtocolOffer{Major: 1, DisableTransportRetries: true}
	old := base
	old.Minor = backendplugin.ProtocolMinorAccountingEvidence
	old.Features = []backendplugin.Feature{{Name: backendplugin.FeatureSemanticExtensions, Required: true}}
	peer := base
	peer.Minor = backendplugin.ProtocolMinorAccountingEvidence
	if _, err := backendplugin.Negotiate(old, peer); !errors.Is(err, backendplugin.ErrUnknownRequiredFeature) && !errors.Is(err, backendplugin.ErrIncompatibleMinor) {
		t.Fatalf("v1.5 must not silently enable semantic carrier: %v", err)
	}
	modern := base
	modern.Minor = backendplugin.ProtocolMinorSemanticExtensions
	modern.Features = []backendplugin.Feature{{Name: backendplugin.FeatureSemanticExtensions}}
	neg, err := backendplugin.Negotiate(modern, modern)
	if err != nil || neg.NegotiatedMinor != backendplugin.ProtocolMinorSemanticExtensions || !backendplugin.ProxyOwnedSemanticExtensionsSupported(neg) {
		t.Fatalf("v1.6 carrier negotiation failed: %+v err=%v", neg, err)
	}
}

func TestSemanticExtension_RequiresExactNegotiatedCarrier(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		SemanticExtensions: []aipapi.SemanticExtension{{Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: aipapi.SemanticExtensionValue, Data: json.RawMessage(`"cache-1"`)}},
		Messages:           []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello"}}}},
	}
	if err := backendplugin.RequireSemanticExtensionsABISupport(oldSemanticNegotiation(), call); err == nil {
		t.Fatal("expected old ABI to reject required semantic carrier")
	}
	if err := backendplugin.RequireSemanticExtensionsABISupport(semanticNegotiation(), call); err != nil {
		t.Fatal(err)
	}
}

func TestSemanticExtension_ABIRejectsOversizeValue(t *testing.T) {
	t.Parallel()
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "al", BLegID: "bl", CanonicalModelID: "m",
		SemanticExtensions: []backendplugin.SemanticExtension{{
			Namespace: "aip", Type: "oversize", Presence: backendplugin.SemanticExtensionValue,
			Data: backendplugin.RawJSONFromBytes([]byte(strings.Repeat("x", aipapi.MaxSemanticExtensionDataBytes+1))),
		}},
	}
	if _, err := backendplugin.InvocationToProto(inv); err == nil {
		t.Fatal("expected oversized semantic extension rejection")
	}
}

func semanticNegotiation() backendplugin.Negotiation {
	return backendplugin.Negotiation{Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorSemanticExtensions, EnabledFeatures: []string{backendplugin.FeatureExactOpenResponsesFields, backendplugin.FeatureSemanticExtensions}}
}

func oldSemanticNegotiation() backendplugin.Negotiation {
	return backendplugin.Negotiation{Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorExactOpenResponsesFields, EnabledFeatures: []string{backendplugin.FeatureExactOpenResponsesFields}}
}

var _ = json.RawMessage{}
