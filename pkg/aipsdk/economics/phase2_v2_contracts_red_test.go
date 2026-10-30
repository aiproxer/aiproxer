package economics_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase2V2_ValuationPreservesEQUIPSRLineageAndPresence(t *testing.T) {
	t.Parallel()
	input := metering.ComponentKey{Direction: metering.DirectionInput, Component: "image", Unit: metering.UnitImage, SchemaID: "provider:image:v1", Dimensions: []metering.Dimension{{Name: "quality", Value: "high"}}}
	quantity := decimal("2")
	amount := decimal("0.250")
	line := economics.LineItem{
		ID: "line-image", RuleID: "rule-image-input", ItemID: "obs-1:measure-1", Component: &input,
		Quantity: &quantity, Unit: metering.UnitImage, UnitPrice: decimalPtr("0.125"), Amount: &amount,
		RoundedAmount: &economics.Money{NanoUnits: 250_000_000, Currency: "USD", Present: true},
		RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven,
		IncludedUnit: true, SourceObservationRefs: []metering.ObservationRef{{StoreID: "store-1", ObservationID: "obs-1", Revision: 1, PayloadHash: "hash-1"}},
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	v := economics.Valuation{
		ID: "valuation-1", Version: 1, Perspective: metering.PerspectiveOperator,
		Basis: economics.BasisProviderQuantityLocal, Subject: validSubject(),
		InputObservations: []metering.ObservationRef{{StoreID: "store-1", ObservationID: "obs-1", Revision: 1, PayloadHash: "hash-1"}},
		InputSetHash:      phase2Hash('1'),
		Rater:             economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater", Version: "v1"}, RaterID: "reference"}, RaterContent: &economics.SnapshotContentRef{ContentRef: "store://snapshots/rater/v1", ContentHash: phase2Hash('2')},
		Tariff: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff", Version: "v2"}, RaterID: "provider"}, TariffContent: &economics.SnapshotContentRef{ContentRef: "store://snapshots/tariff/v2", ContentHash: phase2Hash('3')},
		Policy: economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy", Version: "v3"}, PolicyID: "operator"}, PolicyContent: &economics.SnapshotContentRef{ContentRef: "store://snapshots/policy/v3", ContentHash: phase2Hash('4')},
		QualifierSnapshot: phase2Hash('5'), QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "store://snapshots/qualifiers/v1", ContentHash: phase2Hash('5')}, Lines: []economics.LineItem{line},
		Totals:       []economics.CurrencyTotal{{Currency: "USD", Amount: decimalPtr("0.250"), RoundedAmount: economics.Money{NanoUnits: 250_000_000, Currency: "USD", Present: true}}},
		Completeness: economics.CompletenessComplete, CreatedAt: now,
	}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := v.Clone()
	clone.Lines[0].Component.Dimensions[0].Value = "low"
	clone.Lines[0].Quantity.Coefficient = "99"
	clone.InputObservations[0].PayloadHash = "changed"
	if v.Lines[0].Component.Dimensions[0].Value != "high" || v.Lines[0].Quantity.Coefficient != "2" || v.InputObservations[0].PayloadHash != "hash-1" {
		t.Fatal("Valuation.Clone must deep-copy nested values")
	}
	canonical, err := v.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	clone.Lines[0].ID = "other"
	canonicalAgain, err := v.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, canonicalAgain) {
		t.Fatal("valuation canonical JSON must be deterministic")
	}
	if bytes.Contains(canonical, []byte("raw")) || bytes.Contains(canonical, []byte("prompt")) {
		t.Fatal("valuation must contain only safe economic metadata")
	}
	var decoded economics.Valuation
	if err := json.Unmarshal(canonical, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Basis != economics.BasisProviderQuantityLocal || decoded.Lines[0].Unit != metering.UnitImage {
		t.Fatalf("canonical round-trip lost valuation detail: %+v", decoded)
	}
}

func TestPhase2V2_ValuationBasisKeepsProviderMoneySeparateFromDerivedQ(t *testing.T) {
	t.Parallel()
	for _, basis := range []economics.ValuationBasis{
		economics.BasisLocalExpected, economics.BasisProviderQuantityLocal, economics.BasisProviderReported,
		economics.BasisStatementReported, economics.BasisCustomerPolicy,
	} {
		if !basis.IsKnown() {
			t.Fatalf("basis %q must be known", basis)
		}
	}
	q := validValuation(economics.BasisProviderQuantityLocal)
	p := validValuation(economics.BasisProviderReported)
	q.Lines[0].Amount = decimalPtr("1.10")
	p.Lines[0].Amount = decimalPtr("1.32")
	if q.Basis == p.Basis || q.CanonicalKey() == p.CanonicalKey() {
		t.Fatal("Q and P valuations must remain distinct even with related amounts")
	}
	if err := validValuation(economics.BasisProviderReported).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPhase2V2_ValuationReferencesAreBoundedAndCanonical(t *testing.T) {
	t.Parallel()
	withSpace := validValuation(economics.BasisProviderReported)
	withSpace.ID = " valuation-1 "
	if err := withSpace.Validate(); err == nil {
		t.Fatal("valuation identity with surrounding whitespace must be rejected")
	}
	tooLong := validValuation(economics.BasisProviderReported)
	tooLong.ID = strings.Repeat("x", 513)
	if err := tooLong.Validate(); err == nil {
		t.Fatal("valuation identity above the public reference bound must be rejected")
	}
}

func validValuation(basis economics.ValuationBasis) economics.Valuation {
	return economics.Valuation{
		ID: "valuation-" + string(basis), Version: 1, Perspective: metering.PerspectiveOperator, Basis: basis, Subject: validSubject(),
		InputObservations: []metering.ObservationRef{{StoreID: "store-1", ObservationID: "obs-1", Revision: 1, PayloadHash: "hash-1"}},
		InputSetHash:      phase2Hash('1'),
		Rater:             economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater", Version: "v1"}, RaterID: "reference"}, RaterContent: &economics.SnapshotContentRef{ContentRef: "store://snapshots/rater/v1", ContentHash: phase2Hash('2')},
		Tariff: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff", Version: "v1"}, RaterID: "provider"}, TariffContent: &economics.SnapshotContentRef{ContentRef: "store://snapshots/tariff/v1", ContentHash: phase2Hash('3')},
		Policy: economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy", Version: "v1"}, PolicyID: "operator"}, PolicyContent: &economics.SnapshotContentRef{ContentRef: "store://snapshots/policy/v1", ContentHash: phase2Hash('4')},
		QualifierSnapshot: phase2Hash('5'), QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "store://snapshots/qualifiers/v1", ContentHash: phase2Hash('5')},
		Lines:        []economics.LineItem{{ID: "line-1", RuleID: "rule-1", ItemID: "obs-1:measure-1", Component: &metering.ComponentKey{Direction: metering.DirectionOutput, Component: "audio", Unit: metering.UnitSecond, SchemaID: "audio:v1"}, Quantity: decimalPtr("8"), Unit: metering.UnitSecond, Amount: decimalPtr("1.32"), RoundedAmount: &economics.Money{NanoUnits: 1_320_000_000, Currency: "USD", Present: true}, RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven, SourceObservationRefs: []metering.ObservationRef{{StoreID: "store-1", ObservationID: "obs-1", Revision: 1, PayloadHash: "hash-1"}}}},
		Completeness: economics.CompletenessPartial, CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
}

func validSubject() metering.SubjectRef {
	return metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-1", ALegID: "a-1", BillingCallID: "call-1", BLegID: "b-1"}
}

func decimal(raw string) metering.Decimal {
	d, err := metering.ParseDecimal(raw)
	if err != nil {
		panic(err)
	}
	return d
}

func decimalPtr(raw string) *metering.Decimal {
	d := decimal(raw)
	return &d
}
