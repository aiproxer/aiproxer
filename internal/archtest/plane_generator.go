package archtest

import "github.com/aiproxer/aiproxer/internal/featureplanegen"

// GenerateFeaturePlanesCode parses the feature-plane manifest and returns its generated Go source.
func GenerateFeaturePlanesCode(manifestBytes []byte) ([]byte, error) {
	return featureplanegen.GenerateFeaturePlanesCode(manifestBytes)
}

// GenerateWireEligibilityCode parses the feature-plane manifest and returns
// the core-local wire eligibility catalog source.
func GenerateWireEligibilityCode(manifestBytes []byte) ([]byte, error) {
	return featureplanegen.GenerateWireEligibilityCode(manifestBytes)
}

// WriteGeneratedFileAtomic atomically installs generated feature-plane source.
func WriteGeneratedFileAtomic(path string, data []byte) error {
	return featureplanegen.WriteGeneratedFileAtomic(path, data)
}
