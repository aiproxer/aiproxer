package metering_test

// Phase 19 review-blocker-4 metadata proof (Requirements 16.1, 18.5).
//
// The pre-existing TestPhase193BoundedObservationOverLimitFailsClosed covers
// five entry/dimension bound families (measures, charges, evidence,
// dimensions count, dimension value) but never exceeds the normalized 64 KiB
// serialization cap independently of those counts, and never exercises the
// supersedes bound. This file proves both gaps at the owning SDK package:
//   - supersedes-over-limit fails closed with ErrInvalidObservation;
//   - an observation within every entry/dimension count bound whose canonical
//     serialization exceeds MaxSafeEvidenceBytes fails closed with
//     ErrInvalidObservation on both Validate and CanonicalJSON.
//
// Honest category count (see evidence correction): the envelope enforces seven
// distinct bound families — measures, charges, evidence, supersedes,
// dimensions-per-key, dimension value bytes, and normalized serialization
// bytes — not the six claimed in the 19.3 evidence draft.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// TestPhase19R4ObservationSupersedesOverLimitFailsClosed proves the supersedes
// entry bound (MaxObservationSupersedes) fails closed with the specified
// typed error rather than truncating the offending reference.
func TestPhase19R4ObservationSupersedesOverLimitFailsClosed(t *testing.T) {
	t.Parallel()

	obs := phase193MaxObservation(t)
	supersedes := make([]metering.ObservationRef, 0, metering.MaxObservationSupersedes+1)
	for i := 0; i <= metering.MaxObservationSupersedes; i++ {
		supersedes = append(supersedes, metering.ObservationRef{
			StoreID:       "phase193",
			ObservationID: fmt.Sprintf("phase193-superseded-%03d", i),
			Revision:      1,
			PayloadHash:   fmt.Sprintf("phase193-hash-%03d", i),
		})
	}
	obs.Supersedes = supersedes
	obs.Semantics = metering.SemanticsCorrection
	if err := obs.Validate(); !errors.Is(err, metering.ErrInvalidObservation) {
		t.Fatalf("validate error = %v, want %v", err, metering.ErrInvalidObservation)
	}
	if _, err := obs.CanonicalJSON(); !errors.Is(err, metering.ErrInvalidObservation) {
		t.Fatalf("canonical json error = %v, want %v", err, metering.ErrInvalidObservation)
	}
}

// TestPhase19R4NormalizedSerializationCapFailsClosedIndependentOfEntryBounds
// proves the normalized 64 KiB cap is enforced independently: every entry and
// dimension count stays within its declared bound, yet canonical serialization
// exceeds MaxSafeEvidenceBytes and fails closed with ErrInvalidObservation on
// both Validate and CanonicalJSON.
func TestPhase19R4NormalizedSerializationCapFailsClosedIndependentOfEntryBounds(t *testing.T) {
	t.Parallel()

	obs := phase193BaseObservation()
	measures := make([]metering.Measure, metering.MaxObservationMeasures)
	for i := range measures {
		dimensions := make([]metering.Dimension, metering.MaxDimensions)
		for j := range dimensions {
			dimensions[j] = metering.Dimension{
				Name:  fmt.Sprintf("dim-%02d", j),
				Value: strings.Repeat("v", metering.MaxDimensionValueBytes),
			}
		}
		measures[i] = metering.Measure{
			Key: metering.ComponentKey{
				Direction:  metering.DirectionNone,
				Component:  fmt.Sprintf("phase19_r4_metric_%03d", i),
				Unit:       metering.UnitCount,
				SchemaID:   "s",
				Dimensions: dimensions,
			},
			Value:   phase193Decimal(t, "1"),
			Quality: metering.QualityObserved,
		}
	}
	obs.Measures = measures

	if len(obs.Measures) != metering.MaxObservationMeasures {
		t.Fatalf("measures = %d, want exactly %d (must stay within the entry bound)", len(obs.Measures), metering.MaxObservationMeasures)
	}
	if len(obs.Charges) != 0 || len(obs.Evidence) != 0 || len(obs.Supersedes) != 0 {
		t.Fatalf("fixture must carry measures only, got %d charges/%d evidence/%d supersedes",
			len(obs.Charges), len(obs.Evidence), len(obs.Supersedes))
	}
	for i, measure := range obs.Measures {
		if len(measure.Key.Dimensions) != metering.MaxDimensions {
			t.Fatalf("measures[%d] dimensions = %d, want exactly %d (within bound)", i, len(measure.Key.Dimensions), metering.MaxDimensions)
		}
	}

	validateErr := obs.Validate()
	if !errors.Is(validateErr, metering.ErrInvalidObservation) {
		t.Fatalf("validate error = %v, want %v", validateErr, metering.ErrInvalidObservation)
	}
	if !strings.Contains(validateErr.Error(), "exceeds") {
		t.Fatalf("validate error = %q, want the serialization-cap exceeds diagnostic (not an entry-bound diagnostic)", validateErr.Error())
	}
	if _, err := obs.CanonicalJSON(); !errors.Is(err, metering.ErrInvalidObservation) {
		t.Fatalf("canonical json error = %v, want %v", err, metering.ErrInvalidObservation)
	}
	if fingerprint := obs.Fingerprint(); fingerprint != "" {
		t.Fatalf("fingerprint = %q, want empty (an over-cap observation has no canonical hash)", fingerprint)
	}

	// Control: the at-limits fixture with minimal dimension payload still fits
	// the cap, proving the failure above is the serialization size and not the
	// entry counts.
	control := phase193MaxObservation(t)
	payload, err := control.CanonicalJSON()
	if err != nil {
		t.Fatalf("control canonical json: %v", err)
	}
	if len(payload) > metering.MaxSafeEvidenceBytes {
		t.Fatalf("control normalized observation = %d bytes, want <= %d", len(payload), metering.MaxSafeEvidenceBytes)
	}
}
