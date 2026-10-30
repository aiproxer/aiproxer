package aggregate_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestApplyObservations_ReducesByFullKeyAndIndependentSourceScope(t *testing.T) {
	t.Parallel()

	localAudio := observation("local-audio", "audio-stream", 1, metering.SemanticsDelta, metering.OriginLocal, metering.AcquisitionLocalTokenizer, blegSubject("b-1"), measure(metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentAudio, Unit: metering.UnitSecond,
		SchemaID: "media.v1",
	}, "100"))
	providerAudioInput := observation("provider-audio-input", "audio-stream", 2, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-1"), measure(metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentAudio, Unit: metering.UnitSecond,
		SchemaID: "media.v1",
	}, "110"))
	providerAudioOutput := observation("provider-audio-output", "audio-stream", 3, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-1"), measure(metering.ComponentKey{
		Direction: metering.DirectionOutput, Component: metering.ComponentAudio, Unit: metering.UnitSecond,
		SchemaID: "media.v1",
	}, "8"))
	shortCache := observation("cache-short", "cache-stream", 1, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-1"), measure(metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentCacheWriteInputToken, Unit: metering.UnitToken,
		SchemaID: metering.DefaultInclusionSchemaID, Dimensions: []metering.Dimension{{Name: "cache_lifetime", Value: "5m"}},
	}, "2"))
	longCache := observation("cache-long", "cache-stream", 2, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-1"), measure(metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentCacheWriteInputToken, Unit: metering.UnitToken,
		SchemaID: metering.DefaultInclusionSchemaID, Dimensions: []metering.Dimension{{Name: "cache_lifetime", Value: "1h"}},
	}, "3"))

	snapshot, err := aggregate.ApplyObservations([]metering.Observation{longCache, providerAudioOutput, localAudio, shortCache, providerAudioInput})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Measures) != 5 {
		t.Fatalf("reduced measures=%d want 5: %+v", len(snapshot.Measures), snapshot.Measures)
	}
	if got := snapshot.ValueFor(localAudio, localAudio.Measures[0].Key); got != "100/0" {
		t.Fatalf("local audio=%q want 100/0", got)
	}
	if got := snapshot.ValueFor(providerAudioInput, providerAudioInput.Measures[0].Key); got != "110/0" {
		t.Fatalf("provider audio input=%q want 110/0", got)
	}
	if got := snapshot.ValueFor(providerAudioOutput, providerAudioOutput.Measures[0].Key); got != "8/0" {
		t.Fatalf("provider audio output=%q want 8/0", got)
	}
	if got := snapshot.ValueFor(shortCache, shortCache.Measures[0].Key); got != "2/0" {
		t.Fatalf("short cache=%q want 2/0", got)
	}
	if got := snapshot.ValueFor(longCache, longCache.Measures[0].Key); got != "3/0" {
		t.Fatalf("long cache=%q want 3/0", got)
	}
}

func TestApplyObservations_DeltaCumulativeGaugeAndResourceSemantics(t *testing.T) {
	t.Parallel()

	inputKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	stream := blegSubject("b-reduce")
	delta := observation("delta", "reduce-stream", 1, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, stream, measure(inputKey, "2"))
	deltaNext := observation("delta-next", "reduce-stream", 2, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, stream, measure(inputKey, "3"))
	cumulative := observation("cumulative", "reduce-stream", 2, metering.SemanticsCumulative, metering.OriginProvider, metering.AcquisitionProviderResponse, stream,
		measure(metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}, "4"))
	absent := observation("absent", "reduce-stream", 3, metering.SemanticsCumulative, metering.OriginProvider, metering.AcquisitionProviderResponse, stream,
		metering.Measure{Key: inputKey, Quality: metering.QualityUnknown})

	baseGauge := accountGauge("gauge-1", 1, "12.5")
	nextGauge := accountGauge("gauge-2", 2, "13.0")
	resourceKey := metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentStorage, Unit: metering.UnitByteSecond, SchemaID: "resource.v1"}
	resource := resourceObservation("resource", 1, metering.SemanticsDelta, resourceKey, "1.250")

	snapshot, err := aggregate.ApplyObservations([]metering.Observation{nextGauge, resource, absent, cumulative, deltaNext, delta, baseGauge})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.ValueFor(delta, inputKey); got != "5/0" {
		t.Fatalf("delta input=%q want 5/0", got)
	}
	if got := snapshot.ValueFor(absent, inputKey); got != "5/0" {
		t.Fatalf("absent cumulative field erased known input=%q want 5/0", got)
	}
	if got := snapshot.ValueFor(cumulative, cumulative.Measures[0].Key); got != "4/0" {
		t.Fatalf("cumulative output=%q want 4/0", got)
	}
	if got := snapshot.ValueFor(baseGauge, baseGauge.Measures[0].Key); got != "13/0" {
		t.Fatalf("gauge latest=%q want 13/0", got)
	}
	if got := snapshot.ValueFor(resource, resourceKey); got != "125/2" {
		t.Fatalf("resource exact value=%q want 125/2", got)
	}
}

func TestApplyObservations_RejectsAmbiguousCumulativeAndPreservesPendingCoverage(t *testing.T) {
	t.Parallel()

	key := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	a := observation("cum-a", "ambiguous", 7, metering.SemanticsCumulative, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-amb"), measure(key, "10"))
	b := observation("cum-b", "ambiguous", 7, metering.SemanticsCumulative, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-amb"), measure(key, "11"))
	a.Revision, b.Revision = 1, 1
	if _, err := aggregate.ApplyObservations([]metering.Observation{b, a}); !errors.Is(err, aggregate.ErrAmbiguousCumulative) {
		t.Fatalf("ambiguous cumulative error=%v want ErrAmbiguousCumulative", err)
	}

	pending := observation("pending-charge", "pending", 1, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-pending"), measure(key, "1"))
	pending.Charges = []metering.ReportedCharge{{
		ChargeItemID: "aggregate", Kind: metering.ChargeKindAggregate,
		Amount: decimal("12"), Currency: "USD",
		Covers: []metering.ChargeCoverageRef{{Ref: metering.ChargeRef{StoreID: "store-1", ObservationID: "future", Revision: 1, ChargeItemID: "component"}, Relation: metering.CoverageInclusive}},
	}}
	snapshot, err := aggregate.ApplyObservations([]metering.Observation{pending})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Complete || snapshot.Payable || len(snapshot.PendingCoverage) != 1 {
		t.Fatalf("pending coverage was treated as payable/complete: %+v", snapshot)
	}
	if len(snapshot.Charges) != 1 {
		t.Fatalf("reported charge was recomputed/dropped: %+v", snapshot.Charges)
	}

	pendingCorrection := observation("pending-correction", "pending", 2, metering.SemanticsCorrection, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-pending"), measure(key, "-1"))
	pendingCorrection.Supersedes = []metering.ObservationRef{{
		StoreID: "store-1", ObservationID: "future-observation", Revision: 1, PayloadHash: "future-payload-hash",
	}}
	snapshot, err = aggregate.ApplyObservations([]metering.Observation{pendingCorrection})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Complete || snapshot.Payable || len(snapshot.PendingSupersedes) != 1 {
		t.Fatalf("unresolved supersession was treated as payable/complete: %+v", snapshot)
	}
}

func TestApplyObservations_DeduplicatesExactReplayBeforeGraphValidation(t *testing.T) {
	t.Parallel()

	key := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	base := observation("same", "replay-stream", 1, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-replay"), measure(key, "5"))
	base.Charges = []metering.ReportedCharge{{ChargeItemID: "aggregate", Kind: metering.ChargeKindAggregate, Amount: decimal("1"), Currency: "USD"}}
	late := base
	late.ReceivedAt = late.ReceivedAt.Add(10 * time.Minute)
	snapshot, err := aggregate.ApplyObservations([]metering.Observation{late, base})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Replayed != 1 || len(snapshot.Observations) != 1 {
		t.Fatalf("exact replay not deduplicated before graph checks: %+v", snapshot)
	}
	if got := snapshot.ValueFor(base, key); got != "5/0" {
		t.Fatalf("replayed measure=%q want 5/0", got)
	}

	conflict := base
	conflict.Measures = []metering.Measure{measure(key, "6")}
	if _, err := aggregate.ApplyObservations([]metering.Observation{base, conflict}); !errors.Is(err, aggregate.ErrIdentityConflict) {
		t.Fatalf("changed replay error=%v want ErrIdentityConflict", err)
	}
}

func TestApplyObservations_CorrectionReplacesEffectiveChargeAndRetainsPriorEvidence(t *testing.T) {
	t.Parallel()

	key := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	base := observation("charge-base", "charge-stream", 1, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-charge"), measure(key, "1"))
	base.Charges = []metering.ReportedCharge{{ChargeItemID: "provider-total", Kind: metering.ChargeKindAggregate, Amount: decimal("1.00"), Currency: "USD"}}
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatal(err)
	}
	correction := observation("charge-correction", "charge-stream", 2, metering.SemanticsCorrection, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-charge"))
	correction.Supersedes = []metering.ObservationRef{baseRef}
	correction.Charges = []metering.ReportedCharge{{ChargeItemID: "provider-total", Kind: metering.ChargeKindAggregate, Amount: decimal("1.25"), Currency: "USD"}}

	snapshot, err := aggregate.ApplyObservations([]metering.Observation{correction, base})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Observations) != 2 {
		t.Fatalf("prior immutable observations lost: %d", len(snapshot.Observations))
	}
	if len(snapshot.Charges) != 1 {
		t.Fatalf("correction duplicated effective charge: %+v", snapshot.Charges)
	}
	if snapshot.Charges[0].ObservationID != correction.ID || snapshot.Charges[0].Charge.Amount == nil || snapshot.Charges[0].Charge.Amount.CanonicalString() != "125/2" {
		t.Fatalf("effective correction charge=%+v", snapshot.Charges[0])
	}
	if snapshot.Observations[0].Charges[0].Amount == nil || snapshot.Observations[0].Charges[0].Amount.CanonicalString() != "1/0" {
		t.Fatalf("prior source evidence was mutated: %+v", snapshot.Observations[0].Charges[0])
	}
}

func TestApplyObservations_ReplacementAndCorrectionKeepUnrelatedComponents(t *testing.T) {
	t.Parallel()

	inputKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	outputKey := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	base := observation("replacement-base", "replacement-stream", 1, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-replacement"), measure(inputKey, "10"), measure(outputKey, "5"))
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatal(err)
	}
	replacement := observation("replacement", "replacement-stream", 2, metering.SemanticsReplacement, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-replacement"), measure(outputKey, "7"))
	replacement.Supersedes = []metering.ObservationRef{baseRef}
	snapshot, err := aggregate.ApplyObservations([]metering.Observation{replacement, base})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.ValueFor(replacement, inputKey); got != "10/0" {
		t.Fatalf("replacement erased unrelated input=%q want 10/0", got)
	}
	if got := snapshot.ValueFor(replacement, outputKey); got != "7/0" {
		t.Fatalf("replacement output=%q want 7/0", got)
	}

	correction := observation("correction", "replacement-stream", 3, metering.SemanticsCorrection, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-replacement"), measure(outputKey, "-2"))
	replacementRef, err := replacement.Ref(replacement.Subject.StoreID)
	if err != nil {
		t.Fatal(err)
	}
	correction.Supersedes = []metering.ObservationRef{replacementRef}
	snapshot, err = aggregate.ApplyObservations([]metering.Observation{correction, replacement, base})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.ValueFor(correction, inputKey); got != "10/0" {
		t.Fatalf("correction erased unrelated input=%q want 10/0", got)
	}
	if got := snapshot.ValueFor(correction, outputKey); got != "5/0" {
		t.Fatalf("signed correction output=%q want 5/0", got)
	}
}

func TestApplyFacts_UsesV2ReductionWithoutChangingV1FactIdentity(t *testing.T) {
	t.Parallel()
	fact := metering.Fact{
		FactID: "v1-fact", StreamID: "v1-stream", Sequence: 1, Kind: metering.FactKindDelta,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle: metering.LifecycleBackendAttempt, Source: metering.SourceObserved,
		Authority: metering.AuthorityAuthoritative, Presence: metering.PresencePresent,
		Correlation: metering.Correlation{BLegID: "b-v1", AttemptID: "a-v1"},
		Quantities:  []metering.Quantity{{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 7, Present: true}},
	}
	legacyKey := fact.SourceEventKey()
	snapshot, err := aggregate.ApplyFacts([]metering.Fact{fact})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.ValueForObservationID("v1-fact", metering.ComponentOutputToken); got != "7/0" {
		t.Fatalf("V1 bridge value=%q want 7/0", got)
	}
	if fact.SourceEventKey() != legacyKey {
		t.Fatalf("V1 bridge changed source event key: %q vs %q", fact.SourceEventKey(), legacyKey)
	}
}

func TestApplyFacts_PreservesLegacySignedMoneyCorrectionSemantics(t *testing.T) {
	t.Parallel()
	base := metering.Fact{
		FactID: "v1-money-base", StreamID: "v1-money-stream", Sequence: 1, Kind: metering.FactKindDelta,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Source: metering.SourceObserved, Authority: metering.AuthorityAuthoritative, Presence: metering.PresencePresent,
		Correlation: metering.Correlation{BLegID: "b-v1-money", AttemptID: "a-v1-money"},
		Money:       &metering.MoneyObservation{NanoUnits: 5, Currency: "USD", Present: true},
	}
	correction := base
	correction.FactID = "v1-money-correction"
	correction.Sequence = 2
	correction.Kind = metering.FactKindCorrection
	correction.Supersedes = []string{base.FactID}
	correction.Money = &metering.MoneyObservation{NanoUnits: -2, Currency: "USD", Present: true}

	snapshot, err := aggregate.ApplyFacts([]metering.Fact{correction, base})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Charges) != 2 {
		t.Fatalf("legacy correction was treated as replacement rather than signed adjustment: %+v", snapshot.Charges)
	}
	if snapshot.Charges[0].Charge.Amount == nil || snapshot.Charges[1].Charge.Amount == nil {
		t.Fatalf("legacy charge amounts lost: %+v", snapshot.Charges)
	}
	if snapshot.Charges[0].Charge.Amount.CanonicalString() != "5/9" || snapshot.Charges[1].Charge.Amount.CanonicalString() != "-2/9" {
		t.Fatalf("legacy charge amounts=%q,%q want 5/9,-2/9", snapshot.Charges[0].Charge.Amount.CanonicalString(), snapshot.Charges[1].Charge.Amount.CanonicalString())
	}
}

func observation(id, stream string, seq uint64, semantics, origin, acquisition string, subject metering.SubjectRef, measures ...metering.Measure) metering.Observation {
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event", Revision: 1,
		StreamID: stream, Sequence: seq, Origin: origin, Acquisition: acquisition,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: subject, Correlation: metering.CorrelationV2{StoreID: subject.StoreID, BLegID: subject.BLegID, AttemptID: subject.AttemptID, ProviderAccountKey: "acct-1"},
		Semantics: semantics, ObservedAt: time.Unix(10, 0).UTC(), ReceivedAt: time.Unix(10, 0).UTC(),
		MappingRef: "family.v1", Measures: measures,
	}
}

func blegSubject(id string) metering.SubjectRef {
	return metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-1", BLegID: id, AttemptID: "attempt-" + id}
}

func accountGauge(id string, seq uint64, value string) metering.Observation {
	subject := metering.SubjectRef{Kind: metering.SubjectAccountWindow, StoreID: "store-1", ProviderAccountKey: "acct-gauge", PoolID: "pool-1", WindowID: "window-1", ResetAt: time.Unix(1000, 0).UTC()}
	key := metering.ComponentKey{Direction: metering.DirectionNone, Component: "account_utilization", Unit: metering.UnitPercent, SchemaID: "account-window.v1"}
	obs := metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event", Revision: 1,
		StreamID: "account-window", Sequence: seq, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle: metering.LifecycleAuxiliaryRequest, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: "store-1", ProviderAccountKey: "acct-gauge"}, Semantics: metering.SemanticsGauge,
		ObservedAt: time.Unix(10, 0).UTC(), ReceivedAt: time.Unix(10, 0).UTC(), MappingRef: "account.v1",
		Measures: []metering.Measure{measure(key, value)},
	}
	return obs
}

func resourceObservation(id string, seq uint64, semantics string, key metering.ComponentKey, value string) metering.Observation {
	subject := metering.SubjectRef{Kind: metering.SubjectResource, StoreID: "store-1", ResourceID: "cache-1", PeriodID: "period-1"}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event", Revision: 1, StreamID: "resource-stream", Sequence: seq,
		Origin: metering.OriginLocal, Acquisition: metering.AcquisitionLocalEstimator, Authority: metering.AuthorityEstimatedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleAuxiliaryRequest,
		Subject: subject, Correlation: metering.CorrelationV2{StoreID: "store-1", ResourceID: "cache-1", PeriodID: "period-1"}, Semantics: semantics,
		ObservedAt: time.Unix(10, 0).UTC(), ReceivedAt: time.Unix(10, 0).UTC(), MappingRef: "resource.v1", Measures: []metering.Measure{measure(key, value)},
	}
}

func measure(key metering.ComponentKey, value string) metering.Measure {
	d := decimal(value)
	return metering.Measure{Key: key, Value: d, Quality: metering.QualityObserved, MethodRef: "fixture.v1"}
}

func decimal(value string) *metering.Decimal {
	d, err := metering.ParseDecimal(value)
	if err != nil {
		panic(err)
	}
	return &d
}
