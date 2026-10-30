package standardplugins

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

func TestEssentialCompatibleBackendsCarryBuiltinCompatibleOrigin(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := InstallEssentialBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{
		CustomOpenAILegacyCompatibleID,
		CustomOpenAIResponsesCompatibleID,
		CustomAnthropicCompatibleID,
	} {
		if !reg.HasBackend(kind) {
			t.Fatalf("compatible kind %q missing from essential bundle", kind)
		}
		source, ok := reg.BackendRegistrationSource(kind)
		if !ok {
			t.Fatalf("compatible kind %q missing registration provenance", kind)
		}
		if source != pluginreg.BackendSourceBuiltinCompatible {
			t.Fatalf("compatible kind %q source=%q want %q", kind, source, pluginreg.BackendSourceBuiltinCompatible)
		}
		if reg.IsDiscoveredBackend(kind) {
			t.Fatalf("compatible kind %q must not require executable discovery", kind)
		}
	}
}

func TestNativeEssentialBackendsRetainBuiltinOrigin(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := InstallEssentialBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range EssentialBackendKinds()[:5] {
		source, ok := reg.BackendRegistrationSource(kind)
		if !ok || source != pluginreg.BackendSourceBuiltin {
			t.Fatalf("native essential kind %q source=%q ok=%v", kind, source, ok)
		}
	}
}
