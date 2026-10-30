package economics

import (
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func preAdvisoryValuationFixture() Valuation {
	return Valuation{
		ID: "legacy-valuation", Version: ValuationVersionV2,
		Perspective: metering.PerspectiveOperator, Basis: BasisProviderReported,
		Subject: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "legacy-store", BLegID: "legacy-b-leg"},
		Scope:   "legacy-scope",
		InputObservations: []metering.ObservationRef{{
			StoreID: "legacy-store", ObservationID: "legacy-observation", Revision: 1,
			PayloadHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
		Payer:        metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "legacy-customer"},
		Completeness: CompletenessPartial,
		CreatedAt:    time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC),
	}
}
