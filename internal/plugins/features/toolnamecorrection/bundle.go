package toolnamecorrection

import (
	"fmt"

	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// FeatureBundle constructs the opt-in feature bundle from validated
// configuration: exactly one tool-call finalizer on PlaneToolCallFinalizers.
func FeatureBundle(cfg Config) (aipfeature.FeatureBundle, error) {
	fin, err := newFinalizer(cfg)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, ID, []toolcall.Finalizer{fin}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", ID, err)
	}
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := b.Validate(); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", ID, err)
	}
	return b, nil
}
