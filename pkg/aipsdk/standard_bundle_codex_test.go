package aipsdk_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestStandardDistributionRequirements_excludesCodexBackends(t *testing.T) {
	t.Parallel()
	for _, r := range aipsdk.StandardDistributionRequirements() {
		if r.Kind == aipsdk.PluginKindBackend && (r.ID == "openai-codex" || r.ID == "openai-codex-app-server") {
			t.Fatalf("%s must not be mandatory after Phase 8.2 externalization", r.ID)
		}
	}
}
