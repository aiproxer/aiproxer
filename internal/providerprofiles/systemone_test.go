package providerprofiles_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestSystemOneProfiles_CompileDecisionOnlyStaticInventory(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"typesafe", "openrouter-systemone", "commandcode-systemone", "subquadratic-systemone"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			p, err := providerprofiles.EmbeddedProfile(id)
			if err != nil {
				t.Fatal(err)
			}
			compiled, err := providerprofiles.CompileProfile(p)
			if err != nil {
				t.Fatal(err)
			}
			_, decision := compiled.Capabilities[aipapi.CapabilityDecisions]
			if !decision || len(compiled.Capabilities) != 1 || compiled.Binding.FactoryKind != "custom-systemone-compatible" || len(p.Models.Static) == 0 {
				t.Fatalf("compiled decision profile = %+v", compiled)
			}
			p.Models.Policy = providerprofiles.DiscoveryFamilyDefault
			if err := providerprofiles.Validate(p); err == nil {
				t.Fatal("decision profile accepted non-static discovery")
			}
		})
	}
}
