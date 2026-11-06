package service

import (
	"context"
	"slices"
	"testing"

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
