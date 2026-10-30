package aipsdk_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestStandardDistributionRequirements_excludesOpenCodeBackends(t *testing.T) {
	t.Parallel()
	for _, r := range aipsdk.StandardDistributionRequirements() {
		if r.Kind == aipsdk.PluginKindBackend && (r.ID == "opencode-go" || r.ID == "opencode-zen") {
			t.Fatalf("%s must not be mandatory after Phase 8.1 externalization", r.ID)
		}
	}
}
