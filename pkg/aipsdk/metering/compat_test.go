package metering_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestUnknownEnum_preservesWireUnknowns(t *testing.T) {
	t.Parallel()

	if metering.UnknownEnum("", false) {
		t.Fatal("empty raw is absent, not an unknown enum")
	}
	if metering.UnknownEnum(string(metering.PerspectiveCustomer), metering.PerspectiveCustomer.IsKnown()) {
		t.Fatal("known perspective must not be reported as unknown")
	}
	raw := metering.EconomicPerspective("enterprise_future")
	if !metering.UnknownEnum(string(raw), raw.IsKnown()) {
		t.Fatal("non-empty unknown perspective must be preservable on wire decode")
	}
	if err := raw.Validate(); err == nil {
		t.Fatal("Validate must still reject unknowns for local strict construction")
	}
}

func TestCompatibilityPolicyConstant(t *testing.T) {
	t.Parallel()
	if metering.CompatibilityPolicy != "additive_v1" {
		t.Fatalf("CompatibilityPolicy=%q", metering.CompatibilityPolicy)
	}
}
