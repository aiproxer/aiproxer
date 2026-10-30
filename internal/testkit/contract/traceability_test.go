package contract

import "testing"

func TestReleaseCriticalFeaturesHaveNonMatrixOwners(t *testing.T) {
	if err := ValidateFeatureOwnership(); err != nil {
		t.Fatal(err)
	}
	for _, owner := range ReleaseCriticalFeatureOwners() {
		if len(owner.Frontend)+len(owner.Core)+len(owner.Backend)+len(owner.Profile)+len(owner.Protocol)+len(owner.Sentinel) == 0 {
			t.Fatalf("feature %q has no owner", owner.Feature)
		}
	}
}
