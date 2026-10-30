package usageevidence

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// These pure fixtures are also used by the billing package's monetary tests.
func phase5CostLeg(t *testing.T, callID BillingCallID, id string, seq int, outcome LegOutcome, surfaced SurfacedState, amount *int64) CallLegUsageRecord {
	t.Helper()
	leg := testCallLegUsageRecord(callID, id)
	leg.AttemptSeq = seq
	leg.Outcome = outcome
	leg.Surfaced = surfaced
	if amount == nil {
		leg.Evidence = FinalBillingEvidence{Source: EvidenceSourceUnavailable, Authority: EvidenceAuthorityUnavailable}
	} else {
		leg.Evidence.Cost = MoneyEvidence{NanoUnits: *amount, Currency: "USD", Present: true}
		leg.Evidence.Source = EvidenceSourceProviderReported
		leg.Evidence.Authority = EvidenceAuthorityAuthoritative
	}
	return leg
}

func phase5ChargeObservation(t *testing.T, callID BillingCallID, bLegID, id, item string, amount *string, payer metering.PaymentParty, covers ...metering.ChargeCoverageRef) metering.Observation {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	charge := metering.ReportedCharge{ChargeItemID: item, Kind: metering.ChargeKindAggregate, Payer: payer, Covers: covers}
	if amount != nil {
		value, err := metering.ParseDecimal(*amount)
		if err != nil {
			t.Fatal(err)
		}
		charge.Amount = &value
		charge.Currency = "USD"
	}
	return metering.Observation{
		Version: 2, ID: id, SourceEventKey: id + "-source", Revision: 1, StreamID: id + "-stream", Sequence: 1,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-1", ALegID: "a-1", BillingCallID: callID.String(), BLegID: bLegID},
		Correlation: metering.CorrelationV2{StoreID: "store-1", CallID: callID.String(), BillingCallID: callID.String(), ALegID: "a-1", BLegID: bLegID},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "phase5:test:v1", Charges: []metering.ReportedCharge{charge},
	}
}

func phase5V2Leg(t *testing.T, callID BillingCallID, id string, observation metering.Observation) CallLegUsageRecord {
	t.Helper()
	leg := phase5CostLeg(t, callID, id, 1, LegOutcomeWinner, SurfacedYes, nil)
	leg.ALegID = "a-1"
	leg.EvidenceVersion = EvidenceFormatVersionV2
	leg.EvidenceProjection = EvidenceProjectionV1
	leg.Observations = []metering.Observation{observation}
	return leg
}
