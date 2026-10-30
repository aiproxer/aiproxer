package usageevidence

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestCallLegEconomicDispositionSurvivesJSONAndReplay(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	observation := phase5ChargeObservation(t, callID, "b-disposition", "obs-disposition", "provider", func() *string {
		value := "3.5"
		return &value
	}(), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	leg := phase5V2Leg(t, callID, "b-disposition", observation)
	leg.EconomicEvidenceVersion = EconomicEvidenceDispositionVersionV1
	disposition, err := NewEconomicEvidenceDisposition(observation, EconomicEvidenceCoveragePartial, "legacy V1 token-only evidence")
	if err != nil {
		t.Fatal(err)
	}
	leg.EconomicDispositions = []EconomicEvidenceDisposition{disposition}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded CallLegUsageRecord
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := CheckCallLegUsageReplay(sealed, decoded); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(decoded.EconomicDispositions) != 1 || decoded.EconomicDispositions[0].Coverage != EconomicEvidenceCoveragePartial {
		t.Fatalf("decoded dispositions = %+v", decoded.EconomicDispositions)
	}
}

func TestCallLegEconomicDispositionConflictIsVisibleAndNonReplayable(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	observation := phase5ChargeObservation(t, callID, "b-disposition-conflict", "obs-disposition-conflict", "provider", nil, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	complete := phase5V2Leg(t, callID, "b-disposition-conflict", observation)
	complete.EconomicEvidenceVersion = EconomicEvidenceDispositionVersionV1
	disposition, err := NewEconomicEvidenceDisposition(observation, EconomicEvidenceCoverageComplete, "")
	if err != nil {
		t.Fatal(err)
	}
	complete.EconomicDispositions = []EconomicEvidenceDisposition{disposition}
	partial := complete
	partialDisposition := disposition
	partialDisposition.Coverage = EconomicEvidenceCoveragePartial
	partialDisposition.CoverageReason = "legacy V1 token-only evidence"
	partial.EconomicDispositions = []EconomicEvidenceDisposition{partialDisposition}
	complete, err = complete.Seal()
	if err != nil {
		t.Fatal(err)
	}
	partial, err = partial.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if complete.Fingerprint == partial.Fingerprint {
		t.Fatal("coverage disposition must participate in durable replay identity")
	}
	if !errors.Is(CheckCallLegUsageReplay(complete, partial), ErrReplayConflict) {
		t.Fatal("mismatched coverage disposition must remain a replay conflict")
	}
}
