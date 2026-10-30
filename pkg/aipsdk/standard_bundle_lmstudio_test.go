package aipsdk_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestStandardDistributionRequirements_excludesLmstudioBackend(t *testing.T) {
	t.Parallel()
	for _, r := range aipsdk.StandardDistributionRequirements() {
		if r.Kind == aipsdk.PluginKindBackend && r.ID == "lmstudio" {
			t.Fatal("lmstudio must not be mandatory after Phase 7 externalization")
		}
	}
}
