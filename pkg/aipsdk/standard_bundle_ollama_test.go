package aipsdk_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestStandardDistributionRequirements_excludesOllamaBackends(t *testing.T) {
	t.Parallel()
	for _, r := range aipsdk.StandardDistributionRequirements() {
		if r.Kind == aipsdk.PluginKindBackend && (r.ID == "ollama" || r.ID == "ollama-cloud") {
			t.Fatalf("%s must not be mandatory after Phase 7 externalization", r.ID)
		}
	}
}
