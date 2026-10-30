package secretguard

import (
	"fmt"

	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// FeatureBundle returns a schema-V1 feature bundle contributing one SecretGuard.
func FeatureBundle(cfg Config) aipfeature.FeatureBundle {
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, ID, []sdk.Guard{NewGuard(cfg)}); err != nil {
		panic(fmt.Sprintf("%s: contribute secret guards: %v", ID, err))
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil)
}
