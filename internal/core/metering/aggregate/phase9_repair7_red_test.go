package aggregate_test

import (
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase9Repair7_UnresolvedMeasureTaintPropagatesToDeltaAndIsolatedSibling(t *testing.T) {
	t.Parallel()
	affectedKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "repair7.v1"}
	siblingKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "repair7.v1"}
	base := observation("repair7-measure-base", "repair7-measures", 1, metering.SemanticsCumulative, metering.OriginLocal, metering.AcquisitionLocalTokenizer, blegSubject("repair7-main"),
		metering.Measure{Key: affectedKey, Quality: metering.QualityUnavailable}, measure(siblingKey, "4"))
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := observation("repair7-measure-correction", "repair7-measures", 2, metering.SemanticsCorrection, metering.OriginLocal, metering.AcquisitionLocalTokenizer, blegSubject("repair7-main"), measure(affectedKey, "2"))
	correction.Supersedes = []metering.ObservationRef{baseRef}
	delta := observation("repair7-measure-delta", "repair7-measures", 3, metering.SemanticsDelta, metering.OriginLocal, metering.AcquisitionLocalTokenizer, blegSubject("repair7-main"), measure(affectedKey, "3"))
	isolated := observation("repair7-measure-isolated", "repair7-measures", 1, metering.SemanticsDelta, metering.OriginLocal, metering.AcquisitionLocalTokenizer, blegSubject("repair7-isolated"), measure(affectedKey, "7"))

	orders := [][]metering.Observation{
		{delta, correction, base, isolated},
		{base, isolated, delta, correction},
		{isolated, correction, base, delta},
	}
	var want aggregate.SnapshotV2
	for i, observations := range orders {
		snapshot, applyErr := aggregate.ApplyObservations(observations)
		if applyErr != nil {
			t.Fatalf("order %d apply: %v", i, applyErr)
		}
		if i == 0 {
			want = snapshot
		} else if !reflect.DeepEqual(snapshot, want) {
			t.Fatalf("order %d changed deterministic reduction:\n got=%+v\nwant=%+v", i, snapshot, want)
		}
		mainAffected := repair7Measure(t, snapshot, "repair7-main", affectedKey)
		if mainAffected == nil || mainAffected.Complete || mainAffected.Value.CanonicalString() != "5/0" {
			t.Fatalf("order %d affected measure=%+v, want incomplete value 5/0", i, mainAffected)
		}
		mainSibling := repair7Measure(t, snapshot, "repair7-main", siblingKey)
		if mainSibling == nil || !mainSibling.Complete || mainSibling.Value.CanonicalString() != "4/0" {
			t.Fatalf("order %d healthy sibling=%+v, want complete value 4/0", i, mainSibling)
		}
		isolatedMeasure := repair7Measure(t, snapshot, "repair7-isolated", affectedKey)
		if isolatedMeasure == nil || !isolatedMeasure.Complete || isolatedMeasure.Value.CanonicalString() != "7/0" {
			t.Fatalf("order %d isolated measure=%+v, want independent complete value 7/0", i, isolatedMeasure)
		}
		if snapshot.Complete || snapshot.Payable {
			t.Fatalf("order %d unresolved successor was payable/complete: %+v", i, snapshot)
		}
	}
}

func TestPhase9Repair7_UnresolvedMeasureTaintSurvivesPartialReplacement(t *testing.T) {
	t.Parallel()
	affectedKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "repair7.v1"}
	siblingKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "repair7.v1"}
	base := observation("repair7-partial-base", "repair7-partial", 1, metering.SemanticsCumulative, metering.OriginLocal, metering.AcquisitionLocalTokenizer, blegSubject("repair7-partial-main"),
		metering.Measure{Key: affectedKey, Quality: metering.QualityUnknown}, measure(siblingKey, "4"))
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := observation("repair7-partial-correction", "repair7-partial", 2, metering.SemanticsCorrection, metering.OriginLocal, metering.AcquisitionLocalTokenizer, blegSubject("repair7-partial-main"), measure(affectedKey, "2"))
	correction.Supersedes = []metering.ObservationRef{baseRef}
	correctionRef, err := correction.Ref(correction.Subject.StoreID)
	if err != nil {
		t.Fatalf("correction ref: %v", err)
	}
	replacement := observation("repair7-partial-replacement", "repair7-partial", 3, metering.SemanticsReplacement, metering.OriginLocal, metering.AcquisitionLocalTokenizer, blegSubject("repair7-partial-main"), measure(siblingKey, "9"))
	replacement.Supersedes = []metering.ObservationRef{correctionRef}
	isolated := observation("repair7-partial-isolated", "repair7-partial", 1, metering.SemanticsDelta, metering.OriginLocal, metering.AcquisitionLocalTokenizer, blegSubject("repair7-partial-isolated"), measure(affectedKey, "7"))

	orders := [][]metering.Observation{
		{replacement, correction, isolated, base},
		{base, isolated, replacement, correction},
		{correction, replacement, base, isolated},
	}
	var want aggregate.SnapshotV2
	for i, observations := range orders {
		snapshot, applyErr := aggregate.ApplyObservations(observations)
		if applyErr != nil {
			t.Fatalf("order %d apply: %v", i, applyErr)
		}
		if i == 0 {
			want = snapshot
		} else if !reflect.DeepEqual(snapshot, want) {
			t.Fatalf("order %d changed deterministic reduction:\n got=%+v\nwant=%+v", i, snapshot, want)
		}
		mainAffected := repair7Measure(t, snapshot, "repair7-partial-main", affectedKey)
		if mainAffected == nil || mainAffected.Complete || mainAffected.Value.CanonicalString() != "2/0" {
			t.Fatalf("order %d affected partial replacement=%+v, want incomplete retained correction", i, mainAffected)
		}
		mainSibling := repair7Measure(t, snapshot, "repair7-partial-main", siblingKey)
		if mainSibling == nil || !mainSibling.Complete || mainSibling.Value.CanonicalString() != "9/0" {
			t.Fatalf("order %d replacement sibling=%+v, want complete value 9/0", i, mainSibling)
		}
		isolatedMeasure := repair7Measure(t, snapshot, "repair7-partial-isolated", affectedKey)
		if isolatedMeasure == nil || !isolatedMeasure.Complete {
			t.Fatalf("order %d isolated measure=%+v, want complete", i, isolatedMeasure)
		}
		if snapshot.Complete || snapshot.Payable {
			t.Fatalf("order %d unresolved partial replacement was payable/complete: %+v", i, snapshot)
		}
	}
}

func TestPhase9Repair7_UnresolvedProviderChargeTaintPropagatesToDeltaAndIsolatedSibling(t *testing.T) {
	t.Parallel()
	component := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "repair7.v1"}
	base := observation("repair7-charge-base", "repair7-charges", 1, metering.SemanticsCumulative, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("repair7-charge-main"))
	base.Charges = []metering.ReportedCharge{
		repair7Charge("repair7-affected-charge", &component, ""),
		repair7Charge("repair7-sibling-charge", &component, "4"),
	}
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := observation("repair7-charge-correction", "repair7-charges", 2, metering.SemanticsCorrection, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("repair7-charge-main"))
	correction.Supersedes = []metering.ObservationRef{baseRef}
	correction.Charges = []metering.ReportedCharge{repair7Charge("repair7-affected-charge", &component, "2")}
	delta := observation("repair7-charge-delta", "repair7-charges", 3, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("repair7-charge-main"))
	delta.Charges = []metering.ReportedCharge{repair7Charge("repair7-affected-charge", &component, "3")}
	isolated := observation("repair7-charge-isolated", "repair7-charges", 1, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("repair7-charge-isolated"))
	isolated.Charges = []metering.ReportedCharge{repair7Charge("repair7-affected-charge", &component, "7")}

	orders := [][]metering.Observation{
		{delta, correction, base, isolated},
		{base, isolated, delta, correction},
		{isolated, correction, base, delta},
	}
	var want aggregate.SnapshotV2
	for i, observations := range orders {
		snapshot, applyErr := aggregate.ApplyObservations(observations)
		if applyErr != nil {
			t.Fatalf("order %d apply: %v", i, applyErr)
		}
		if i == 0 {
			want = snapshot
		} else if !reflect.DeepEqual(snapshot, want) {
			t.Fatalf("order %d changed deterministic reduction:\n got=%+v\nwant=%+v", i, snapshot, want)
		}
		mainAffected := repair7Charges(t, snapshot, "repair7-charge-main", "repair7-affected-charge")
		if len(mainAffected) != 2 {
			t.Fatalf("order %d affected charge lines=%+v, want correction and delta", i, mainAffected)
		}
		for _, charge := range mainAffected {
			if charge.Complete {
				t.Fatalf("order %d affected provider charge became payable: %+v", i, charge)
			}
		}
		mainSibling := repair7Charges(t, snapshot, "repair7-charge-main", "repair7-sibling-charge")
		if len(mainSibling) != 1 || !mainSibling[0].Complete || mainSibling[0].Charge.Amount == nil || mainSibling[0].Charge.Amount.CanonicalString() != "4/0" {
			t.Fatalf("order %d provider sibling=%+v, want complete amount 4/0", i, mainSibling)
		}
		isolatedCharge := repair7Charges(t, snapshot, "repair7-charge-isolated", "repair7-affected-charge")
		if len(isolatedCharge) != 1 || !isolatedCharge[0].Complete {
			t.Fatalf("order %d isolated provider charge=%+v, want complete", i, isolatedCharge)
		}
		if snapshot.Complete || snapshot.Payable {
			t.Fatalf("order %d unresolved provider successor was payable/complete: %+v", i, snapshot)
		}
	}
}

func TestPhase9Repair7_UnresolvedProviderChargeTaintSurvivesPartialReplacement(t *testing.T) {
	t.Parallel()
	component := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "repair7.v1"}
	base := observation("repair7-charge-partial-base", "repair7-charge-partial", 1, metering.SemanticsCumulative, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("repair7-charge-partial-main"))
	base.Charges = []metering.ReportedCharge{
		repair7Charge("repair7-affected-charge", &component, ""),
		repair7Charge("repair7-sibling-charge", &component, "4"),
	}
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := observation("repair7-charge-partial-correction", "repair7-charge-partial", 2, metering.SemanticsCorrection, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("repair7-charge-partial-main"))
	correction.Supersedes = []metering.ObservationRef{baseRef}
	correction.Charges = []metering.ReportedCharge{repair7Charge("repair7-affected-charge", &component, "2")}
	correctionRef, err := correction.Ref(correction.Subject.StoreID)
	if err != nil {
		t.Fatalf("correction ref: %v", err)
	}
	replacement := observation("repair7-charge-partial-replacement", "repair7-charge-partial", 3, metering.SemanticsReplacement, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("repair7-charge-partial-main"))
	replacement.Supersedes = []metering.ObservationRef{correctionRef}
	replacement.Charges = []metering.ReportedCharge{repair7Charge("repair7-sibling-charge", &component, "9")}
	isolated := observation("repair7-charge-partial-isolated", "repair7-charge-partial", 1, metering.SemanticsDelta, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("repair7-charge-partial-isolated"))
	isolated.Charges = []metering.ReportedCharge{repair7Charge("repair7-affected-charge", &component, "7")}

	orders := [][]metering.Observation{
		{replacement, correction, isolated, base},
		{base, isolated, replacement, correction},
		{correction, replacement, base, isolated},
	}
	var want aggregate.SnapshotV2
	for i, observations := range orders {
		snapshot, applyErr := aggregate.ApplyObservations(observations)
		if applyErr != nil {
			t.Fatalf("order %d apply: %v", i, applyErr)
		}
		if i == 0 {
			want = snapshot
		} else if !reflect.DeepEqual(snapshot, want) {
			t.Fatalf("order %d changed deterministic reduction:\n got=%+v\nwant=%+v", i, snapshot, want)
		}
		mainAffected := repair7Charges(t, snapshot, "repair7-charge-partial-main", "repair7-affected-charge")
		if len(mainAffected) != 1 || mainAffected[0].Complete || mainAffected[0].Charge.Amount == nil || mainAffected[0].Charge.Amount.CanonicalString() != "2/0" {
			t.Fatalf("order %d affected partial replacement=%+v, want incomplete retained correction", i, mainAffected)
		}
		mainSibling := repair7Charges(t, snapshot, "repair7-charge-partial-main", "repair7-sibling-charge")
		if len(mainSibling) != 2 {
			t.Fatalf("order %d sibling charge lines=%+v, want retained base and replacement", i, mainSibling)
		}
		for _, charge := range mainSibling {
			if !charge.Complete || charge.Charge.Amount == nil {
				t.Fatalf("order %d sibling charge=%+v, want complete", i, charge)
			}
		}
		isolatedCharge := repair7Charges(t, snapshot, "repair7-charge-partial-isolated", "repair7-affected-charge")
		if len(isolatedCharge) != 1 || !isolatedCharge[0].Complete {
			t.Fatalf("order %d isolated provider charge=%+v, want complete", i, isolatedCharge)
		}
		if snapshot.Complete || snapshot.Payable {
			t.Fatalf("order %d unresolved partial provider replacement was payable/complete: %+v", i, snapshot)
		}
	}
}

func repair7Measure(t *testing.T, snapshot aggregate.SnapshotV2, blegID string, key metering.ComponentKey) *aggregate.ReducedMeasure {
	t.Helper()
	for i := range snapshot.Measures {
		measure := &snapshot.Measures[i]
		if measure.Scope.Subject.BLegID == blegID && measure.Key.Equal(key) {
			return measure
		}
	}
	return nil
}

func repair7Charges(t *testing.T, snapshot aggregate.SnapshotV2, blegID, itemID string) []aggregate.ReducedCharge {
	t.Helper()
	charges := make([]aggregate.ReducedCharge, 0)
	for _, charge := range snapshot.Charges {
		if charge.Scope.Subject.BLegID == blegID && charge.Charge.ChargeItemID == itemID {
			charges = append(charges, charge)
		}
	}
	return charges
}

func repair7Charge(itemID string, component *metering.ComponentKey, amount string) metering.ReportedCharge {
	charge := metering.ReportedCharge{ChargeItemID: itemID, Component: component, Kind: metering.ChargeKindComponent}
	if amount != "" {
		charge.Currency = "USD"
		charge.Amount = decimal(amount)
	}
	return charge
}
