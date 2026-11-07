package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// declaredWebSearchExtension is the exact hosted web-search declaration
// carrier this connector consumes. The literal is also defined privately by the
// OpenAI Responses frontend; the staged host integration test pins the two
// definitions together and proves the admission consequence.
const declaredWebSearchExtension = "openairesponses.web_search_declarations"

func configuredCapabilityInstance(t *testing.T) *instance {
	t.Helper()
	configured, err := New().Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID:  "capability-instance",
		FactoryKind: FactoryKind,
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	inst, ok := configured.(*instance)
	if !ok {
		t.Fatalf("Configure returned %T, want the direct factory instance", configured)
	}
	return inst
}

// TestResolve_AdvertisesOpaqueExtensionsAndTheExactHostedDeclarationExtension
// pins the runtime admission authority of requirement 1.7: the resolved
// profile declares opaque-extension support AND the exact call extension the
// connector consumes. The staged host integration test proves that generic core
// admits the carrier only against these runtime facts.
func TestResolve_AdvertisesOpaqueExtensionsAndTheExactHostedDeclarationExtension(t *testing.T) {
	t.Parallel()
	profile, err := configuredCapabilityInstance(t).Resolve(context.Background(), nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !profile.Capabilities.OpaqueExtensions {
		t.Fatal("the resolved profile must declare opaque-extension capability")
	}
	wantExtension := backendplugin.ExtensionRequirementDTO{
		Namespace: "call",
		Type:      declaredWebSearchExtension,
	}
	if !slices.Contains(profile.DialectSupport.ExtensionTypes, wantExtension) {
		t.Fatalf("the resolved profile must declare the exact hosted-declaration extension %+v, got %+v",
			wantExtension, profile.DialectSupport.ExtensionTypes)
	}
}

// TestDescribe_SemanticCarrierAdvertisementCarriesThePromptCacheAliasAndFailsClosed
// pins the F1 ABI boundary at the connector's own descriptor:
//
//   - the descriptor advertises semantic_extensions_v1, so a host that offers
//     the bounded residual carrier negotiates it and the legacy
//     Call.PromptCacheKey alias reaches the preview policy as one carrier;
//   - the descriptor does NOT advertise exact_openresponses_fields, because
//     the SIWC preview route does not accept exact Responses fields;
//   - a host that does not offer the carrier still negotiates (the feature is
//     additive) but the alias remains a loud ABI refusal, never a silent drop.
func TestDescribe_SemanticCarrierAdvertisementCarriesThePromptCacheAliasAndFailsClosed(t *testing.T) {
	t.Parallel()
	descriptor, err := New().Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !slices.ContainsFunc(descriptor.Features, func(f backendplugin.Feature) bool {
		return f.Name == backendplugin.FeatureSemanticExtensions
	}) {
		t.Fatalf("descriptor must advertise %q, got %+v", backendplugin.FeatureSemanticExtensions, descriptor.Features)
	}
	if slices.ContainsFunc(descriptor.Features, func(f backendplugin.Feature) bool {
		return f.Name == backendplugin.FeatureExactOpenResponsesFields
	}) {
		t.Fatalf("descriptor must not advertise %q, got %+v", backendplugin.FeatureExactOpenResponsesFields, descriptor.Features)
	}

	plugin := backendplugin.ProtocolOffer{
		Major:                   descriptor.ProtocolMajor,
		Minor:                   descriptor.ProtocolMinor,
		DisableTransportRetries: true,
		Features:                descriptor.Features,
	}
	hostWithCarrier := backendplugin.ProtocolOffer{
		Major:                   backendplugin.ProtocolMajorV1,
		Minor:                   descriptor.ProtocolMinor,
		DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureSemanticExtensions},
		},
	}
	negotiated, err := backendplugin.Negotiate(hostWithCarrier, plugin)
	if err != nil {
		t.Fatalf("a host that offers the carrier must negotiate the connector: %v", err)
	}
	if !backendplugin.ProxyOwnedSemanticExtensionsSupported(negotiated) {
		t.Fatalf("the negotiated feature set must enable %q: %+v",
			backendplugin.FeatureSemanticExtensions, negotiated)
	}

	call := promptCacheAliasCall()
	inv := promptCacheAliasInvocation()
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, negotiated); err != nil {
		t.Fatalf("the negotiated carrier must accept the prompt-cache alias: %v", err)
	}
	if inv.PromptCacheKey != "" {
		t.Fatalf("the legacy alias must not remain the typed ABI authority: %q", inv.PromptCacheKey)
	}
	if len(inv.SemanticExtensions) != 1 {
		t.Fatalf("the legacy alias must bridge to exactly one semantic carrier, got %+v", inv.SemanticExtensions)
	}
	if carrier := inv.SemanticExtensions[0]; carrier.Namespace != "aip" || carrier.Type != "prompt_cache_key" ||
		carrier.Implementor != "proxy" || carrier.Direction != "request" {
		t.Fatalf("unexpected prompt-cache carrier identity: %+v", carrier)
	} else if got := string(carrier.Data.Bytes()); got != `"cache-1"` {
		t.Fatalf("prompt-cache carrier data = %s, want %q", got, `"cache-1"`)
	}

	// Additive feature: an older host still negotiates, but without the carrier
	// it must reject the alias rather than quietly dropping the hint.
	hostWithoutCarrier := backendplugin.ProtocolOffer{
		Major:                   backendplugin.ProtocolMajorV1,
		Minor:                   descriptor.ProtocolMinor,
		DisableTransportRetries: true,
	}
	oldNegotiated, err := backendplugin.Negotiate(hostWithoutCarrier, plugin)
	if err != nil {
		t.Fatalf("the additive carrier must not break an older host: %v", err)
	}
	if backendplugin.ProxyOwnedSemanticExtensionsSupported(oldNegotiated) {
		t.Fatalf("an older host must not gain the carrier: %+v", oldNegotiated)
	}
	oldInv := promptCacheAliasInvocation()
	err = backendplugin.ApplyCallWireMetadataWithNegotiation(&oldInv, call, nil, oldNegotiated)
	if !errors.Is(err, backendplugin.ErrExactOpenResponsesUnsupported) {
		t.Fatalf("without the carrier the alias must remain an ABI refusal, got %v", err)
	}
}

func promptCacheAliasCall() aipapi.Call {
	return aipapi.Call{
		PromptCacheKey: "cache-1",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello"}},
		}},
	}
}

func promptCacheAliasInvocation() backendplugin.Invocation {
	text := "hello"
	return backendplugin.Invocation{
		RequestID: "req-f1", AttemptID: "attempt-f1", ALegID: "aleg", BLegID: "bleg",
		CanonicalModelID: "gpt-5",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
	}
}

// TestDescribe_StaticOpaqueCapabilityMirrorsTheRuntimeBit pins requirement
// 14.12's static half: the descriptor mirrors the runtime capability, but it is
// not admission authority. The staged host integration test proves that static
// capability alone cannot admit the preserved declaration.
func TestDescribe_StaticOpaqueCapabilityMirrorsTheRuntimeBit(t *testing.T) {
	t.Parallel()
	descriptor, err := New().Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(descriptor.Factories) != 1 {
		t.Fatalf("Describe reports %d factories, want the one direct export", len(descriptor.Factories))
	}
	if !descriptor.Factories[0].StaticCapabilities.OpaqueExtensions {
		t.Fatal("the static descriptor must mirror OpaqueExtensions")
	}
}
