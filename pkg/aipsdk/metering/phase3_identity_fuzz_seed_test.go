package metering_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Phase 3.1 property/fuzz seeds for ordinary non-negative quantities
// (requirement 13.8). No generated corpus committed.

func FuzzPhase3_FactValidate_OrdinaryNonNegative(f *testing.F) {
	f.Add(int64(0))
	f.Add(int64(1))
	f.Add(int64(-1))
	f.Add(int64(-42))
	f.Fuzz(func(t *testing.T, value int64) {
		fact := phase3CustomerIngressFact("fuzz-req", "fuzz-fe", 1)
		fact.Kind = metering.FactKindDelta
		fact.Quantities = []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     value,
			Present:   true,
		}}
		err := fact.Validate()
		if value < 0 && err == nil {
			t.Fatalf("negative ordinary quantity %d must fail validation (req 6.4)", value)
		}
	})
}
