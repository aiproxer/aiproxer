package standardplugins_test

import (
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestInstallEssentialBackendsOn_ExactEssentialKindsOnly(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range standardplugins.EssentialBackendKinds() {
		if !reg.HasBackend(id) {
			t.Fatalf("essential kind %q missing from InstallEssentialBackendsOn registry", id)
		}
	}
	ids := reg.BackendFactoryIDs()
	want := append([]string(nil), standardplugins.EssentialBackendKinds()...)
	slices.Sort(ids)
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("InstallEssentialBackendsOn=%v want %v", ids, want)
	}
	if err := reg.MountFrontend("openai-responses", nil, aipsdk.FrontendMountOptions{}); err == nil {
		t.Fatal("InstallEssentialBackendsOn must not register frontends")
	}
}

func TestEssentialOnly_StandardInstallMatchesEssential(t *testing.T) {
	t.Parallel()
	keys := standardplugins.UpstreamAPIKeys{}
	union := standardplugins.StandardBackendBundle(keys)
	essential := standardplugins.EssentialBackendBundle(keys)
	if len(union.Backends) != len(essential.Backends) {
		t.Fatalf("StandardBackendBundle len=%d want essential=%d", len(union.Backends), len(essential.Backends))
	}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, keys); err != nil {
		t.Fatal(err)
	}
	for _, id := range standardplugins.EssentialBackendKinds() {
		if !reg.HasBackend(id) {
			t.Fatalf("InstallStandardBundleOn missing essential kind %q", id)
		}
	}
	ids := reg.BackendFactoryIDs()
	want := append([]string(nil), standardplugins.EssentialBackendKinds()...)
	slices.Sort(ids)
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("standard install backends=%v want essential-only %v", ids, want)
	}
}

func TestEssentialOnly_OptionalKindsNeverInAllowlist(t *testing.T) {
	t.Parallel()
	for _, id := range []string{
		"openrouter", "opencode-go", "opencode-zen", "openai-codex", "openai-codex-app-server",
		"ollama", "cursorcliacp", "huggingface", "llamacpp", "lmstudio", "nvidia", "vllm",
	} {
		if standardplugins.IsEssentialBackendKind(id) {
			t.Fatalf("optional kind %q must not be essential", id)
		}
	}
}
