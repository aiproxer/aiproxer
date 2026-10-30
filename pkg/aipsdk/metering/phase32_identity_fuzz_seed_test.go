package metering_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Phase 3.2 fuzz seeds for identity encoding and presence validation.
// No generated corpora are committed.

func FuzzPhase32_SourceEventKey_DelimiterSafety(f *testing.F) {
	f.Add("a", "b")
	f.Add("a\x00b", "c")
	f.Add("a", "b\x00c")
	f.Add("customer-request:x", "src")
	f.Fuzz(func(t *testing.T, lifecycle, sourceID string) {
		if len(lifecycle) > metering.MaxSourceEventFieldLen || len(sourceID) > metering.MaxSourceEventFieldLen {
			t.Skip()
		}
		left := phase3CustomerIngressFact("req", "fe", 1)
		left.IdentityVersion = metering.IdentityVersionV1
		left.SourceEventKind = "k"
		left.StreamID = lifecycle
		left.SourceID = sourceID

		right := left
		right.StreamID = lifecycle + "\x00shift"
		right.SourceID = "tail"
		// Distinct structured fields must not produce identical keys under
		// adversarial delimiter content (when both validate length bounds).
		if left.StreamID != right.StreamID || left.SourceID != right.SourceID {
			if left.SourceEventKey() == right.SourceEventKey() && left.SourceEventKey() != "" {
				// Only fail when encodings collide despite different component tuples.
				refL := left.SourceEventRef()
				refR := right.SourceEventRef()
				if refL != refR {
					t.Fatalf("collision: lifecycle=%q source=%q key=%q", lifecycle, sourceID, left.SourceEventKey())
				}
			}
		}
	})
}

func FuzzPhase32_MoneyPresentCurrency(f *testing.F) {
	f.Add(true, "", int64(1))
	f.Add(true, "USD", int64(1))
	f.Add(false, "USD", int64(0))
	f.Add(false, "", int64(5))
	f.Fuzz(func(t *testing.T, present bool, currency string, nano int64) {
		fact := phase3OperatorEgressFact("att-fuzz", "be-fuzz", 1)
		fact.Money = &metering.MoneyObservation{
			NanoUnits: nano,
			Currency:  currency,
			Present:   present,
			Source:    metering.SourceProviderReported,
		}
		err := fact.Validate()
		if present && nano >= 0 && currency == "" && err == nil {
			t.Fatal("present money without currency must fail")
		}
		if !present && nano != 0 && err == nil {
			t.Fatal("absent money with non-zero nano must fail")
		}
	})
}
