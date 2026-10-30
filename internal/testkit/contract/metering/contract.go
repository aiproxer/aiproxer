// Package metering contains a bounded family-level contract test for provider
// normalizers and the shared V2 reducer. It intentionally does not enumerate
// frontend-by-backend combinations.
package metering

import (
	"math/big"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/internal/core/metering/normalize"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Env supplies the actual family normalizer and the shared reducer under test.
// A provider-family adapter can reuse Run by adapting its local decoded fields
// into normalize.Input; no provider SDK type enters this contract package.
type Env struct {
	Normalize func(normalize.Input) (normalize.Result, error)
	Reduce    func([]sdkmetering.Observation) (aggregate.SnapshotV2, error)
}

// Run exercises exact partitioning, unknown/presence retention, native
// multimodal direction/unit identity, namespaced resources and aggregate
// coverage. The fixtures are synthetic; only a test-side exact oracle checks
// the documented 11.6 partition and production code does not calculate price.
func Run(t *testing.T, env Env) {
	t.Helper()
	if env.Normalize == nil {
		t.Fatal("metering contract: Normalize is required")
	}
	if env.Reduce == nil {
		t.Fatal("metering contract: Reduce is required")
	}

	t.Run("inclusive partition and source evidence", func(t *testing.T) {
		mapping := tokenMapping()
		result, err := env.Normalize(normalize.Input{
			Mapping: mapping,
			Fields: []normalize.Field{
				{Name: "total", Lexeme: "1000", Present: true},
				{Name: "read", Lexeme: "600", Present: true},
				{Name: "write", Lexeme: "100", Present: true},
				{Name: "output", Lexeme: "200", Present: true},
				{Name: "reasoning", Lexeme: "50", Present: true},
				{Name: "provider.custom.credits", Lexeme: "0.125", Present: true},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != normalize.StatusComplete {
			t.Fatalf("status=%q diagnostics=%v", result.Status, result.Diagnostics)
		}
		if result.MappingRef != "family.tokens@v1" {
			t.Fatalf("mapping ref=%q", result.MappingRef)
		}
		if valueFor(result.Measures, sdkmetering.ComponentInputToken) != "300/0" {
			t.Fatalf("uncached partition=%q", valueFor(result.Measures, sdkmetering.ComponentInputToken))
		}
		if valueFor(result.Measures, sdkmetering.ComponentInputTokenTotal) != "" {
			t.Fatal("inclusive aggregate entered chargeable measures")
		}
		if valueFor(result.Measures, sdkmetering.ComponentOutputToken) != "200/0" {
			t.Fatalf("output partition=%q", valueFor(result.Measures, sdkmetering.ComponentOutputToken))
		}
		if valueFor(result.Informational, sdkmetering.ComponentReasoningOutputToken) != "50/0" {
			t.Fatalf("reasoning informational=%q", valueFor(result.Informational, sdkmetering.ComponentReasoningOutputToken))
		}
		if originalValue(result.OriginalFields, "provider.custom.credits") != "0.125" {
			t.Fatalf("unknown exact evidence was not retained: %+v", result.OriginalFields)
		}
		if got := syntheticChargeOracle(result.Measures); got.Cmp(new(big.Rat).SetFrac64(116, 10)) != 0 {
			t.Fatalf("synthetic exact charge oracle=%s want 58/5", got.RatString())
		}
		observation, err := result.Attach(baseObservation("tck-inclusive", "tck-stream", 1))
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := env.Reduce([]sdkmetering.Observation{observation})
		if err != nil {
			t.Fatal(err)
		}
		key := sdkmetering.ComponentKey{Direction: sdkmetering.DirectionInput, Component: sdkmetering.ComponentInputToken, Unit: sdkmetering.UnitToken, SchemaID: sdkmetering.DefaultInclusionSchemaID}
		if got := snapshot.ValueFor(observation, key); got != "300/0" {
			t.Fatalf("reduced uncached=%q", got)
		}

		separate := mapping
		separate.InputMode = normalize.InputSeparate
		separateResult, err := env.Normalize(normalize.Input{
			Mapping: separate,
			Fields: []normalize.Field{
				{Name: "input", Lexeme: "300", Present: true},
				{Name: "read", Lexeme: "600", Present: true},
				{Name: "write", Lexeme: "100", Present: true},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if separateResult.Status != normalize.StatusComplete || valueFor(separateResult.Measures, sdkmetering.ComponentInputToken) != "300/0" {
			t.Fatalf("separate cache partition status=%q input=%q", separateResult.Status, valueFor(separateResult.Measures, sdkmetering.ComponentInputToken))
		}
	})

	t.Run("multimodal native directions and units", func(t *testing.T) {
		imageInput := sdkmetering.ComponentKey{Direction: sdkmetering.DirectionInput, Component: sdkmetering.ComponentImage, Unit: sdkmetering.UnitImage, SchemaID: "media.v1", Dimensions: []sdkmetering.Dimension{{Name: "quality", Value: "high"}}}
		audioOutput := sdkmetering.ComponentKey{Direction: sdkmetering.DirectionOutput, Component: sdkmetering.ComponentAudio, Unit: sdkmetering.UnitSecond, SchemaID: "media.v1", Dimensions: []sdkmetering.Dimension{{Name: "channels", Value: "1"}}}
		videoOutput := sdkmetering.ComponentKey{Direction: sdkmetering.DirectionOutput, Component: sdkmetering.ComponentVideo, Unit: sdkmetering.UnitFrame, SchemaID: "media.v1", Dimensions: []sdkmetering.Dimension{{Name: "fps", Value: "24"}}}
		observation := baseObservation("tck-media", "tck-media-stream", 1)
		observation.Measures = []sdkmetering.Measure{tckMeasure(imageInput, "1"), tckMeasure(audioOutput, "8.0"), tckMeasure(videoOutput, "240")}
		snapshot, err := env.Reduce([]sdkmetering.Observation{observation})
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Measures) != 3 {
			t.Fatalf("native media measures=%d want 3", len(snapshot.Measures))
		}
		if got := snapshot.ValueFor(observation, audioOutput); got != "8/0" {
			t.Fatalf("audio output=%q", got)
		}
		if got := snapshot.ValueFor(observation, videoOutput); got != "240/0" {
			t.Fatalf("video output=%q", got)
		}
	})

	t.Run("resource presence and aggregate coverage", func(t *testing.T) {
		resource := sdkmetering.Observation{
			Version: sdkmetering.ObservationVersionV2, ID: "tck-resource", SourceEventKey: "resource-event", Revision: 1,
			StreamID: "resource-stream", Sequence: 1, Origin: sdkmetering.OriginLocal, Acquisition: sdkmetering.AcquisitionLocalEstimator,
			Authority: sdkmetering.AuthorityEstimatedClaim, Perspective: sdkmetering.PerspectiveOperator, Boundary: sdkmetering.BoundaryBackendEgress,
			Lifecycle:   sdkmetering.LifecycleAuxiliaryRequest,
			Subject:     sdkmetering.SubjectRef{Kind: sdkmetering.SubjectResource, StoreID: "tck-store", ResourceID: "cache", PeriodID: "period"},
			Correlation: sdkmetering.CorrelationV2{StoreID: "tck-store", ResourceID: "cache", PeriodID: "period"},
			Semantics:   sdkmetering.SemanticsDelta, ObservedAt: time.Unix(1, 0).UTC(), ReceivedAt: time.Unix(1, 0).UTC(), MappingRef: "resource.v1",
			Measures: []sdkmetering.Measure{{Key: sdkmetering.ComponentKey{Direction: sdkmetering.DirectionNone, Component: "provider.storage", Unit: sdkmetering.UnitByteSecond, SchemaID: "resource.v1"}, Quality: sdkmetering.QualityUnknown}},
			Charges: []sdkmetering.ReportedCharge{{
				ChargeItemID: "aggregate", Kind: sdkmetering.ChargeKindAggregate, Amount: tckDecimal("12"), Currency: "USD",
				Covers: []sdkmetering.ChargeCoverageRef{{Ref: sdkmetering.ChargeRef{StoreID: "tck-store", ObservationID: "late-component", Revision: 1, ChargeItemID: "component"}, Relation: sdkmetering.CoverageInclusive}},
			}},
		}
		snapshot, err := env.Reduce([]sdkmetering.Observation{resource})
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Complete || snapshot.Payable || len(snapshot.PendingCoverage) != 1 {
			t.Fatalf("unknown/pending resource was payable: %+v", snapshot)
		}
		if len(snapshot.Charges) != 1 || snapshot.Charges[0].Charge.Amount == nil {
			t.Fatalf("aggregate charge was recomputed or dropped: %+v", snapshot.Charges)
		}
	})
}

func tokenMapping() normalize.Mapping {
	key := func(direction sdkmetering.FlowDirection, component string) sdkmetering.ComponentKey {
		return sdkmetering.ComponentKey{Direction: direction, Component: component, Unit: sdkmetering.UnitToken, SchemaID: sdkmetering.DefaultInclusionSchemaID}
	}
	return normalize.Mapping{
		ID: "family.tokens", Family: "family", Version: "v1", InputMode: normalize.InputInclusive,
		InputTotal:    normalize.FieldSpec{Name: "total", EvidencePath: "$.usage.total_tokens", Key: key(sdkmetering.DirectionInput, sdkmetering.ComponentInputTokenTotal)},
		InputUncached: normalize.FieldSpec{Name: "input", EvidencePath: "$.usage.input_tokens", Key: key(sdkmetering.DirectionInput, sdkmetering.ComponentInputToken)},
		CacheRead:     []normalize.FieldSpec{{Name: "read", EvidencePath: "$.usage.cache_read_input_tokens", Key: key(sdkmetering.DirectionInput, sdkmetering.ComponentCacheReadInputToken)}},
		CacheWrite:    []normalize.FieldSpec{{Name: "write", EvidencePath: "$.usage.cache_write_input_tokens", Key: key(sdkmetering.DirectionInput, sdkmetering.ComponentCacheWriteInputToken)}},
		Output:        normalize.FieldSpec{Name: "output", EvidencePath: "$.usage.output_tokens", Key: key(sdkmetering.DirectionOutput, sdkmetering.ComponentOutputToken)},
		Reasoning:     normalize.FieldSpec{Name: "reasoning", EvidencePath: "$.usage.reasoning_output_tokens", Key: key(sdkmetering.DirectionOutput, sdkmetering.ComponentReasoningOutputToken)},
		ReasoningMode: normalize.ReasoningIncluded,
	}
}

func baseObservation(id, stream string, sequence uint64) sdkmetering.Observation {
	subject := sdkmetering.SubjectRef{Kind: sdkmetering.SubjectBLeg, StoreID: "tck-store", BLegID: "tck-b-leg", AttemptID: "tck-attempt"}
	return sdkmetering.Observation{
		Version: sdkmetering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event", Revision: 1,
		StreamID: stream, Sequence: sequence, Origin: sdkmetering.OriginProvider, Acquisition: sdkmetering.AcquisitionProviderResponse,
		Authority: sdkmetering.AuthorityObservedClaim, Perspective: sdkmetering.PerspectiveOperator,
		Boundary: sdkmetering.BoundaryBackendEgress, Lifecycle: sdkmetering.LifecycleBackendAttempt,
		Subject: subject, Correlation: sdkmetering.CorrelationV2{StoreID: "tck-store", BLegID: "tck-b-leg", AttemptID: "tck-attempt", ProviderAccountKey: "tck-account"},
		Semantics: sdkmetering.SemanticsDelta, ObservedAt: time.Unix(1, 0).UTC(), ReceivedAt: time.Unix(1, 0).UTC(), MappingRef: "family.v1",
	}
}

func tckMeasure(key sdkmetering.ComponentKey, value string) sdkmetering.Measure {
	return sdkmetering.Measure{Key: key, Value: tckDecimal(value), Quality: sdkmetering.QualityObserved, MethodRef: "tck.v1"}
}

func tckDecimal(value string) *sdkmetering.Decimal {
	decimal, err := sdkmetering.ParseDecimal(value)
	if err != nil {
		panic(err)
	}
	return &decimal
}

func valueFor(measures []sdkmetering.Measure, component string) string {
	for _, measure := range measures {
		if measure.Key.Component == component && measure.Value != nil {
			return measure.Value.CanonicalString()
		}
	}
	return ""
}

func originalValue(fields []normalize.OriginalField, name string) string {
	for _, field := range fields {
		if field.Name == name {
			return field.Lexeme
		}
	}
	return ""
}

// syntheticChargeOracle is intentionally test-only. It checks the 11.6
// partition fixture with exact rational arithmetic; production normalization
// and reduction never calculate a price.
func syntheticChargeOracle(measures []sdkmetering.Measure) *big.Rat {
	rates := map[string]*big.Rat{
		sdkmetering.ComponentInputToken:           new(big.Rat).SetFrac64(1, 100),
		sdkmetering.ComponentCacheReadInputToken:  new(big.Rat).SetFrac64(1, 1000),
		sdkmetering.ComponentCacheWriteInputToken: new(big.Rat).SetFrac64(2, 100),
		sdkmetering.ComponentOutputToken:          new(big.Rat).SetFrac64(3, 100),
	}
	total := new(big.Rat)
	for _, measure := range measures {
		rate, ok := rates[measure.Key.Component]
		if !ok || measure.Value == nil {
			continue
		}
		quantity, err := measure.Value.ToRat()
		if err != nil {
			continue
		}
		total.Add(total, new(big.Rat).Mul(quantity, rate))
	}
	return total
}
