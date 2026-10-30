package economics_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase2EconomicsRepair_V2ReferencesRejectInvalidUTF8BeforeCanonicalization(t *testing.T) {
	t.Parallel()

	invalid := string([]byte{0xff})
	v := validValuation(economics.BasisProviderReported)
	v.ID = invalid
	if err := v.Validate(); err == nil {
		t.Fatal("valuation V2 identity accepted invalid UTF-8")
	}
	if _, err := v.CanonicalJSON(); err == nil {
		t.Fatal("valuation canonicalization accepted invalid UTF-8")
	}

	input := validRatingInput()
	input.Scope = invalid
	if err := input.Validate(); err == nil {
		t.Fatal("rating V2 scope accepted invalid UTF-8")
	}

	quote := validQuoteInput()
	quote.Scope = invalid
	if err := quote.Validate(); err == nil {
		t.Fatal("quote V2 scope accepted invalid UTF-8")
	}
}

func TestPhase2EconomicsRepair_LineItemRejectsFractionalTokenAndCountQuantity(t *testing.T) {
	t.Parallel()

	for _, unit := range []string{metering.UnitToken, metering.UnitCount} {
		t.Run(unit, func(t *testing.T) {
			t.Parallel()
			v := validValuation(economics.BasisProviderReported)
			component := &metering.ComponentKey{
				Direction: metering.DirectionInput,
				Component: "custom:" + unit,
				Unit:      unit,
				SchemaID:  "custom:economics-repair:v1",
			}
			v.Lines[0].Component = component
			v.Lines[0].Unit = unit
			v.Lines[0].Quantity = decimalPtr("1.5")
			if err := v.Validate(); err == nil {
				t.Fatalf("fractional %s quantity accepted", unit)
			}
		})
	}
}

func TestPhase2EconomicsRepair_UnitBoundRejectsFractionalTokenAndCount(t *testing.T) {
	t.Parallel()

	for _, unit := range []string{metering.UnitToken, metering.UnitCount} {
		t.Run(unit, func(t *testing.T) {
			t.Parallel()
			bound := economics.UnitBound{Unit: unit, Amount: decimalPtr("1.5"), Present: true}
			if err := bound.Validate(); err == nil {
				t.Fatalf("fractional %s unit bound accepted", unit)
			}
		})
	}

	seconds := economics.UnitBound{Unit: metering.UnitSecond, Amount: decimalPtr("1.5"), Present: true}
	if err := seconds.Validate(); err != nil {
		t.Fatalf("fractional native seconds bound must remain supported: %v", err)
	}
}

func TestPhase2EconomicsRepair_ObservationRefsRejectConflictingHashesAndDuplicates(t *testing.T) {
	t.Parallel()

	conflicting := metering.ObservationRef{StoreID: "store-1", ObservationID: "obs-1", Revision: 1, PayloadHash: "different"}
	duplicate := metering.ObservationRef{StoreID: "store-1", ObservationID: "obs-1", Revision: 1, PayloadHash: "hash-1"}

	for _, tc := range []struct {
		name string
		make func([]metering.ObservationRef) error
	}{
		{
			name: "valuation input observations",
			make: func(refs []metering.ObservationRef) error {
				v := validValuation(economics.BasisProviderReported)
				v.InputObservations = refs
				return v.Validate()
			},
		},
		{
			name: "valuation line source observations",
			make: func(refs []metering.ObservationRef) error {
				v := validValuation(economics.BasisProviderReported)
				v.Lines[0].SourceObservationRefs = refs
				return v.Validate()
			},
		},
		{
			name: "rating input observations",
			make: func(refs []metering.ObservationRef) error {
				in := validRatingInput()
				in.ObservationRefs = refs
				in.Observations = nil
				return in.Validate()
			},
		},
		{
			name: "quote input observations",
			make: func(refs []metering.ObservationRef) error {
				in := validQuoteInput()
				in.ObservationRefs = refs
				return in.Validate()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, invalid := range [][]metering.ObservationRef{
				{{StoreID: "store-1", ObservationID: "obs-1", Revision: 1, PayloadHash: "hash-1"}, conflicting},
				{{StoreID: "store-1", ObservationID: "obs-1", Revision: 1, PayloadHash: "hash-1"}, duplicate},
			} {
				if err := tc.make(invalid); err == nil {
					t.Fatalf("observation-ref collection accepted %v", invalid)
				}
			}
		})
	}
}

func TestPhase2EconomicsRepair_ValuationRejectsCrossStoreAndDuplicateAdjustmentRefs(t *testing.T) {
	t.Parallel()

	base := economics.AdjustmentRef{StoreID: "store-1", ValuationID: "valuation-1", Revision: 1, OperationID: "op-1"}
	for _, tc := range []struct {
		name string
		refs []economics.AdjustmentRef
	}{
		{name: "cross store", refs: []economics.AdjustmentRef{{StoreID: "other-store", ValuationID: base.ValuationID, Revision: base.Revision, OperationID: base.OperationID}}},
		{name: "duplicate", refs: []economics.AdjustmentRef{base, base}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := validValuation(economics.BasisProviderReported)
			v.Lines[0].AdjustmentRefs = tc.refs
			if err := v.Validate(); err == nil {
				t.Fatalf("valuation accepted %s adjustment refs", tc.name)
			}
		})
	}
}

func TestPhase2EconomicsRepair_ValuationCoverageRefsRejectDuplicateOrContradictoryEdges(t *testing.T) {
	t.Parallel()

	ref := metering.ChargeRef{StoreID: "store-1", ObservationID: "unresolved-charge-observation", Revision: 1, ChargeItemID: "charge-1"}
	for _, relation := range []metering.CoverageRelation{metering.CoverageInclusive, metering.CoverageAdditive} {
		t.Run(string(relation), func(t *testing.T) {
			t.Parallel()
			v := validValuation(economics.BasisProviderReported)
			v.CoverageRefs = []metering.ChargeCoverageRef{{Ref: ref, Relation: relation}, {Ref: ref, Relation: relation}}
			if err := v.Validate(); err == nil {
				t.Fatalf("duplicate %s coverage edge accepted", relation)
			}
		})
	}

	contradictory := validValuation(economics.BasisProviderReported)
	contradictory.CoverageRefs = []metering.ChargeCoverageRef{
		{Ref: ref, Relation: metering.CoverageInclusive},
		{Ref: ref, Relation: metering.CoverageAdditive},
	}
	if err := contradictory.Validate(); err == nil {
		t.Fatal("contradictory coverage edges accepted")
	}
}

func TestPhase2EconomicsRepair_ValuationAllowsUnresolvedCoverageRefsForResolver(t *testing.T) {
	t.Parallel()

	v := validValuation(economics.BasisProviderReported)
	v.CoverageRefs = []metering.ChargeCoverageRef{{
		Ref: metering.ChargeRef{
			StoreID:       "store-1",
			ObservationID: "observation-that-arrives-later",
			Revision:      1,
			ChargeItemID:  "charge-1",
		},
		Relation: metering.CoverageInclusive,
	}}
	if err := v.Validate(); err != nil {
		t.Fatalf("refs-only valuation must defer external graph resolution: %v", err)
	}
}

func TestPhase2EconomicsRepair_V2AbsentMoneyCannotCarryMetadata(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		check func() error
	}{
		{
			name: "line rounded amount",
			check: func() error {
				v := validValuation(economics.BasisProviderReported)
				v.Lines[0].RoundedAmount = &economics.Money{NanoUnits: 1, Currency: "USD"}
				return v.Validate()
			},
		},
		{
			name: "total rounded amount",
			check: func() error {
				v := validValuation(economics.BasisProviderReported)
				v.Totals = []economics.CurrencyTotal{{Currency: "USD", Amount: decimalPtr("1"), RoundedAmount: economics.Money{NanoUnits: 1, Currency: "USD"}}}
				return v.Validate()
			},
		},
		{
			name: "quote minimum",
			check: func() error {
				q := validExposureQuote()
				q.Minimum = economics.Money{NanoUnits: 1, Currency: "USD"}
				return q.Validate()
			},
		},
		{
			name: "quote maximum",
			check: func() error {
				q := validExposureQuote()
				q.Maximum = economics.Money{NanoUnits: 1, Currency: "USD"}
				return q.Validate()
			},
		},
		{
			name: "quote credit bound",
			check: func() error {
				q := validExposureQuote()
				q.CreditBound = economics.Money{NanoUnits: 1, Currency: "USD"}
				return q.Validate()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.check(); err == nil {
				t.Fatalf("absent V2 money with metadata accepted for %s", tc.name)
			}
		})
	}
}

func TestPhase2EconomicsRepair_ExposureQuoteRequiresKnownCompleteness(t *testing.T) {
	t.Parallel()

	q := validExposureQuote()
	q.Completeness = ""
	if err := q.Validate(); err == nil {
		t.Fatal("exposure quote accepted unknown completeness")
	}

	q.Completeness = economics.CompletenessUnknown
	if err := q.Validate(); err != nil {
		t.Fatalf("known unknown completeness should validate: %v", err)
	}
}

func TestPhase2EconomicsRepair_QuoteInputObservationRefsAreBounded(t *testing.T) {
	t.Parallel()

	in := validQuoteInput()
	in.ObservationRefs = make([]metering.ObservationRef, economics.MaxQuoteObservationRefs+1)
	for i := range in.ObservationRefs {
		in.ObservationRefs[i] = metering.ObservationRef{
			StoreID:       "store-1",
			ObservationID: "observation-" + strings.Repeat("x", i%3) + string(rune('a'+i%26)),
			Revision:      1,
			PayloadHash:   "hash-" + string(rune('a'+i%26)),
		}
	}
	if err := in.Validate(); err == nil {
		t.Fatal("quote input accepted observation refs above its bound")
	}
}

func validRatingInput() economics.RatingInput {
	return economics.RatingInput{
		Version: 1, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject:         validSubject(),
		ObservationRefs: []metering.ObservationRef{{StoreID: "store-1", ObservationID: "obs-1", Revision: 1, PayloadHash: "hash-1"}},
		Rater:           economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater", Version: "v1"}, RaterID: "reference"},
		Tariff:          economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff", Version: "v1"}, RaterID: "provider"},
		Policy:          economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy", Version: "v1"}, PolicyID: "operator"},
	}
}

func validQuoteInput() economics.QuoteInput {
	return economics.QuoteInput{
		Version: 1, Perspective: metering.PerspectiveCustomer, Basis: economics.BasisCustomerPolicy,
		Subject:              validSubject(),
		ObservationRefs:      []metering.ObservationRef{{StoreID: "store-1", ObservationID: "obs-1", Revision: 1, PayloadHash: "hash-1"}},
		InputSetHash:         phase2Hash('1'),
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "store://snapshots/qualifiers/v1", ContentHash: phase2Hash('3')},
		CandidateLimits:      []economics.Limit{{Name: "output_tokens", Unit: metering.UnitToken, Value: 10}},
		Tariff:               economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff", Version: "v1"}}, TariffContent: &economics.SnapshotContentRef{ContentRef: "store://snapshots/tariff/v1", ContentHash: phase2Hash('4')},
		Policy: economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy", Version: "v1"}, PolicyID: "customer"}, PolicyContent: &economics.SnapshotContentRef{ContentRef: "store://snapshots/policy/v1", ContentHash: phase2Hash('2')},
	}
}

func validExposureQuote() economics.ExposureQuote {
	return economics.ExposureQuote{
		Version: 1, Subject: validSubject(),
		Policy:       economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy", Version: "v1"}, PolicyID: "customer"},
		Completeness: economics.CompletenessUnknown,
	}
}
