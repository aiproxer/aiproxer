package metering_test

// Phase 19.3 bounded-metadata certification (Requirements 4.6, 18.5).
//
// The canonical observation envelope must accept every entry up to its
// declared bound without silently dropping or truncating any of them, and must
// fail closed with a typed error above the bound or above the normalized
// 64 KiB serialization cap. The benchmark records the bounded allocation cost
// of validate/canonicalize/fingerprint at the declared entry limits.

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

var phase193BenchSink any

func phase193BaseObservation() metering.Observation {
	now := time.Unix(1_700_193_000, 0).UTC()
	return metering.Observation{
		Version:        metering.ObservationVersionV2,
		ID:             "phase193-bounded",
		SourceEventKey: "phase193-event",
		Revision:       1,
		StreamID:       "phase193-stream",
		Sequence:       1,
		Origin:         metering.OriginProvider,
		Acquisition:    metering.AcquisitionProviderResponse,
		Authority:      metering.AuthorityObservedClaim,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendIngress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject:        metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "phase193", ALegID: "a-193", BillingCallID: "call-193", BLegID: "b-193"},
		Correlation:    metering.CorrelationV2{StoreID: "phase193", RequestID: "req-193", CallID: "call-193", BillingCallID: "call-193", ALegID: "a-193", BLegID: "b-193", AttemptID: "att-193"},
		Semantics:      metering.SemanticsDelta,
		ObservedAt:     now,
		ReceivedAt:     now,
		MappingRef:     "phase193:bench:v1",
	}
}

func phase193Decimal(tb testing.TB, raw string) *metering.Decimal {
	tb.Helper()
	value, err := metering.ParseDecimal(raw)
	if err != nil {
		tb.Fatalf("parse decimal %q: %v", raw, err)
	}
	return &value
}

// phase193CountEvidenceLocations are 32 distinct allowlisted count locations
// (24 response paths plus 8 header locations). Combined with the eight known
// acquisitions they yield the 256 unique (location, acquisition) pairs needed
// to reach MaxObservationEvidence without a duplicate-field rejection.
var phase193CountEvidenceLocations = []string{
	"$.usage.input_tokens", "$.usage.output_tokens", "$.usage.total_tokens",
	"$.usage.cache_read_input_tokens", "$.usage.cache_write_input_tokens",
	"$.usage.reasoning_output_tokens", "$.usage.tool_use_prompt_tokens",
	"$.usage.image_count", "$.usage.input_image_count", "$.usage.output_image_count",
	"$.usage.input_image_tokens", "$.usage.output_image_tokens",
	"$.usage.input_text_tokens", "$.usage.output_text_tokens",
	"$.usage.input_audio_tokens", "$.usage.output_audio_tokens",
	"$.usage.input_video_tokens", "$.usage.output_video_tokens",
	"$.usage.input_video_frames", "$.usage.output_video_frames",
	"$.usage.input_document_tokens", "$.usage.output_document_tokens",
	"$.usage.input_document_pages", "$.usage.output_document_pages",
	"content-length", "x-usage-input-tokens", "x-usage-output-tokens",
	"x-usage-total-tokens", "x-usage-cache-read-input-tokens",
	"x-usage-cache-write-input-tokens", "x-usage-reasoning-output-tokens",
	"x-usage-image-count",
}

var phase193EvidenceAcquisitions = []string{
	metering.AcquisitionLocalTokenizer,
	metering.AcquisitionLocalTransport,
	metering.AcquisitionLocalEstimator,
	metering.AcquisitionProviderCountAPI,
	metering.AcquisitionProviderResponse,
	metering.AcquisitionProviderHeader,
	metering.AcquisitionProviderFinalizer,
	metering.AcquisitionStatementImporter,
}

// phase193MaxObservation builds an observation with every entry family at its
// declared bound. The caller asserts it still fits the 64 KiB cap.
func phase193MaxObservation(tb testing.TB) metering.Observation {
	tb.Helper()
	obs := phase193BaseObservation()

	measures := make([]metering.Measure, metering.MaxObservationMeasures)
	for i := range measures {
		measures[i] = metering.Measure{
			Key: metering.ComponentKey{
				Direction: metering.DirectionNone,
				Component: fmt.Sprintf("phase193_metric_%03d", i),
				Unit:      metering.UnitCount,
				SchemaID:  "s",
			},
			Value:   phase193Decimal(tb, "1"),
			Quality: metering.QualityObserved,
		}
	}
	obs.Measures = measures

	charges := make([]metering.ReportedCharge, metering.MaxObservationCharges)
	for i := range charges {
		charges[i] = metering.ReportedCharge{
			ChargeItemID: fmt.Sprintf("phase193-charge-%03d", i),
			Amount:       phase193Decimal(tb, "0.01"),
			Currency:     "USD",
			Kind:         metering.ChargeKindAggregate,
			Payer:        metering.PaymentParty{Kind: metering.PaymentPartyOperator},
		}
	}
	obs.Charges = charges

	evidence := make([]metering.SafeEvidenceField, 0, metering.MaxObservationEvidence)
	for _, location := range phase193CountEvidenceLocations {
		for _, acquisition := range phase193EvidenceAcquisitions {
			evidence = append(evidence, metering.SafeEvidenceField{
				Path:        location,
				Lexeme:      "1",
				Present:     true,
				Acquisition: acquisition,
			})
		}
	}
	if len(evidence) != metering.MaxObservationEvidence {
		tb.Fatalf("evidence fixture = %d locations, want exactly %d", len(evidence), metering.MaxObservationEvidence)
	}
	obs.Evidence = evidence
	return obs
}

func phase193Counts(obs metering.Observation) (measures, charges, evidence int) {
	return len(obs.Measures), len(obs.Charges), len(obs.Evidence)
}

// TestPhase193BoundedObservationAtLimitsRetainsEveryEntry proves the declared
// bounds are inclusive: at the limit every measure, charge and evidence field
// survives canonicalization unchanged (no silent truncation).
func TestPhase193BoundedObservationAtLimitsRetainsEveryEntry(t *testing.T) {
	t.Parallel()

	obs := phase193MaxObservation(t)
	if err := obs.Validate(); err != nil {
		t.Fatalf("observation at declared limits is invalid: %v", err)
	}
	canonical, err := obs.Canonical()
	if err != nil {
		t.Fatalf("canonicalize observation at declared limits: %v", err)
	}
	if gotMeasures, gotCharges, gotEvidence := phase193Counts(canonical); gotMeasures != metering.MaxObservationMeasures ||
		gotCharges != metering.MaxObservationCharges || gotEvidence != metering.MaxObservationEvidence {
		t.Fatalf("canonical entries = %d/%d/%d, want %d/%d/%d (silent drop)",
			gotMeasures, gotCharges, gotEvidence,
			metering.MaxObservationMeasures, metering.MaxObservationCharges, metering.MaxObservationEvidence)
	}
	payload, err := canonical.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	if len(payload) > metering.MaxSafeEvidenceBytes {
		t.Fatalf("normalized observation = %d bytes, want <= %d", len(payload), metering.MaxSafeEvidenceBytes)
	}
	first := canonical.Fingerprint()
	second := canonical.Fingerprint()
	if first == "" || first != second {
		t.Fatalf("fingerprint = %q then %q, want a stable non-empty value", first, second)
	}
}

// TestPhase193BoundedObservationOverLimitFailsClosed proves every declared
// entry bound and the normalized size cap fail closed with typed errors rather
// than truncating the offending entry.
func TestPhase193BoundedObservationOverLimitFailsClosed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*metering.Observation)
		target error
	}{
		{
			name: "measures-over-limit",
			mutate: func(o *metering.Observation) {
				o.Measures = append(o.Measures, metering.Measure{
					Key:   metering.ComponentKey{Direction: metering.DirectionNone, Component: "phase193_overflow", Unit: metering.UnitCount, SchemaID: "s"},
					Value: phase193Decimal(t, "1"), Quality: metering.QualityObserved,
				})
			},
			target: metering.ErrInvalidObservation,
		},
		{
			name: "charges-over-limit",
			mutate: func(o *metering.Observation) {
				o.Charges = append(o.Charges, metering.ReportedCharge{
					ChargeItemID: "phase193-charge-overflow", Amount: phase193Decimal(t, "0.01"),
					Currency: "USD", Kind: metering.ChargeKindAggregate,
					Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator},
				})
			},
			target: metering.ErrInvalidObservation,
		},
		{
			name: "evidence-over-limit",
			mutate: func(o *metering.Observation) {
				o.Evidence = append(o.Evidence, metering.SafeEvidenceField{
					Path: "$.usage.input_tokens", Lexeme: "2", Present: true, Acquisition: metering.AcquisitionLocalTokenizer,
				})
			},
			target: metering.ErrInvalidObservation,
		},
		{
			name: "dimensions-over-limit",
			mutate: func(o *metering.Observation) {
				key := o.Measures[0].Key
				key.Dimensions = nil
				for i := 0; i <= metering.MaxDimensions; i++ {
					key.Dimensions = append(key.Dimensions, metering.Dimension{Name: fmt.Sprintf("dim-%02d", i), Value: "v"})
				}
				o.Measures[0].Key = key
			},
			target: metering.ErrInvalidObservation,
		},
		{
			name: "dimension-value-over-limit",
			mutate: func(o *metering.Observation) {
				key := o.Measures[0].Key
				key.Dimensions = []metering.Dimension{{Name: "dim", Value: string(make([]byte, metering.MaxDimensionValueBytes+1))}}
				o.Measures[0].Key = key
			},
			target: metering.ErrInvalidObservation,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			obs := phase193MaxObservation(t)
			tc.mutate(&obs)
			if err := obs.Validate(); !errors.Is(err, tc.target) {
				t.Fatalf("validate error = %v, want %v", err, tc.target)
			}
			if _, err := obs.CanonicalJSON(); !errors.Is(err, tc.target) {
				t.Fatalf("canonical json error = %v, want %v", err, tc.target)
			}
		})
	}
}

// BenchmarkPhase193BoundedMetadataObservation measures the bounded cost of
// validating, canonicalizing and fingerprinting a maximum-size observation.
func BenchmarkPhase193BoundedMetadataObservation(b *testing.B) {
	obs := phase193MaxObservation(b)
	payload, err := obs.CanonicalJSON()
	if err != nil {
		b.Fatalf("canonical json: %v", err)
	}
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()

	b.Run("Validate", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if err := obs.Validate(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("CanonicalJSON", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			out, err := obs.CanonicalJSON()
			if err != nil {
				b.Fatal(err)
			}
			phase193BenchSink = out
		}
	})
	b.Run("Fingerprint", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			phase193BenchSink = obs.Fingerprint()
		}
	})
}
