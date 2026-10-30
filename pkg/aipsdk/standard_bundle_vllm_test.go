package aipsdk_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestStandardDistributionRequirements_excludesVllmBackend(t *testing.T) {
	t.Parallel()
	for _, r := range aipsdk.StandardDistributionRequirements() {
		if r.Kind == aipsdk.PluginKindBackend && r.ID == "vllm" {
			t.Fatal("vllm must not be mandatory after Phase 7 externalization")
		}
	}
}
