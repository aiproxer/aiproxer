package standardplugins

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

func TestInstallStandardBackendsOn_declaresExplicitNonUnknownPosture(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range StandardBackendBundle(UpstreamAPIKeys{}).Backends {
		id := entry.ID
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			p, ok := reg.BackendSecurityProfile(id)
			if !ok {
				t.Fatalf("missing security profile for bundled backend factory %q", id)
			}
			if p.CredentialMode == pluginreg.CredentialUnknown || p.CredentialMode == "" {
				t.Fatalf("bundled backend %q must declare explicit non-unknown posture, got %q", id, p.CredentialMode)
			}
		})
	}
}

func TestEssentialOnly_acpFamilyAbsentFromBuiltinBundles(t *testing.T) {
	t.Parallel()
	forbidden := map[string]struct{}{
		"acp": {}, "cursorcliacp": {}, "geminicliacp": {}, "agycliacp": {},
		"openai-codex": {}, "openai-codex-app-server": {},
		"openrouter": {}, "nvidia": {}, "huggingface": {},
		"opencode-go": {}, "opencode-zen": {}, "local-stub": {},
	}
	for _, e := range EssentialBackendBundle(UpstreamAPIKeys{}).Backends {
		if _, bad := forbidden[e.ID]; bad {
			t.Fatalf("essential bundle registers %q", e.ID)
		}
	}
	for _, e := range StandardBackendBundle(UpstreamAPIKeys{}).Backends {
		if _, bad := forbidden[e.ID]; bad {
			t.Fatalf("standard backend bundle registers %q", e.ID)
		}
	}
}
