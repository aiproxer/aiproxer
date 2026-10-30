package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// F6 runtime proof. The F6 counterexample is a stock ProviderEvidenceBuffer
// draft whose provider-owned identifier is one byte past the SDK contract bound.
// Add admits it (it is under the metadata byte cap), but the pre-fix drain
// silently dropped it, so the runtime only ever saw the accepted prefix and
// could seal/rate it as complete. These tests drive the real runtime sideband
// and terminal drains and require the bounded provider-neutral loss marker to
// survive, the earlier valid prefix to be retained, and no invalid amount to be
// fabricated.

// f6RuntimeInvalidDraft is an otherwise valid provider draft carrying a
// ProviderRequestID of MaxSchemaIDBytes+1.
func f6RuntimeInvalidDraft(key string, count uint64) coremetering.ProviderEvidenceDraft {
	draft := r5bProviderMediaMoneyDraft(key, count)
	draft.ProviderRequestID = strings.Repeat("r", metering.MaxSchemaIDBytes+1)
	return draft
}

// f6AssertInvalidDraftRecord proves the terminal record kept the valid prefix,
// excluded the invalid draft and its amount, surfaced the invalid-draft loss
// marker, and reduced incomplete with only the prefix's commercial value.
func f6AssertInvalidDraftRecord(t *testing.T, observations []metering.Observation, prefixKey, invalidKey, wantQuantity, wantMoney string) {
	t.Helper()
	prefixVisible := false
	markerVisible := false
	for _, observation := range observations {
		if observation.SourceEventKey == prefixKey {
			prefixVisible = true
		}
		if observation.SourceEventKey == invalidKey {
			t.Fatalf("invalid draft reached accepted terminal evidence: %+v", observation)
		}
		for _, charge := range observation.Charges {
			if charge.ChargeItemID == "charge:"+invalidKey {
				t.Fatalf("invalid draft's charge reached terminal evidence: %+v", charge)
			}
		}
		if observation.Authority != metering.AuthorityUnavailableClaim {
			continue
		}
		for _, measure := range observation.Measures {
			if measure.Quality == metering.QualityUnavailable && measure.Reason == "invalid_draft" {
				markerVisible = true
			}
		}
	}
	if !prefixVisible {
		t.Fatalf("earlier valid prefix was not retained: %+v", observations)
	}
	if !markerVisible {
		t.Fatalf("invalid-draft loss marker did not reach the terminal record: %+v", observations)
	}
	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		t.Fatalf("reduce terminal record observations: %v", err)
	}
	if snapshot.Complete {
		t.Fatal("terminal record with an invalid-draft marker reduced complete; prefix could be billed")
	}
	quantity, money := r5cReducedCommercialState(t, observations, r5bMediaKey())
	if quantity != wantQuantity || money != wantMoney {
		t.Fatalf("invalid draft fabricated commercial value: quantity=%q money=%q, want prefix-only %s/%s", quantity, money, wantQuantity, wantMoney)
	}
}

func f6TerminalRecord(t *testing.T, callID usageevidence.BillingCallID, storageID, bLegID, submissionID, aLegID string, now time.Time, economic []execbackend.EconomicEvidence, conflicts []usageevidence.EvidenceConflict) usageevidence.CallLegUsageRecord {
	t.Helper()
	record := billingLegRecord(billingLegDraft{
		callID: callID, submissionID: submissionID, aLegID: aLegID,
		storeID: storageID, bLegID: bLegID, seq: 1,
		primary:   routing.Primary{Backend: "backend-f6", Model: "model-f6"},
		startedAt: now, finishedAt: now.Add(time.Second),
		command: sdkterminal.CommandNormalFinish, outcome: usageevidence.LegOutcomeWinner, surfaced: usageevidence.SurfacedYes,
		economicObservations: economic, economicConflicts: conflicts,
	})
	if _, err := record.Seal(); err != nil {
		t.Fatalf("terminal record with F6 loss marker failed sealing: %v", err)
	}
	return record
}

// F6 terminal drain: prefix and invalid draft drained together at terminal time.
func TestProviderEvidenceF6InvalidDraftReachesTerminalRecord(t *testing.T) {
	t.Parallel()
	const (
		bLegID     = "b-f6-terminal"
		prefixKey  = "provider.f6.terminal.prefix"
		invalidKey = "provider.f6.terminal.invalid"
	)
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_900_000, 0).UTC()
	identity := coremetering.ObservationIdentity{
		StoreID: "store-f6-terminal", RequestID: "req-f6-terminal", CallID: callID.String(), BillingCallID: callID.String(),
		ALegID: "a-f6-terminal", BLegID: bLegID, AttemptID: bLegID, AttemptSeq: 1,
		ObservedAt: now, ReceivedAt: now,
	}
	buffer := coremetering.NewProviderEvidenceBuffer()
	buffer.BindEconomicEvidence(identity)
	buffer.Add(r5bProviderMediaMoneyDraft(prefixKey, 1))
	buffer.Add(f6RuntimeInvalidDraft(invalidKey, 3))

	attempt := newAttemptSession(attemptSessionInput{})
	stream := &phase7EconomicRuntimeStream{observations: buffer.DrainEconomicObservations()}
	attempt.drainStreamUsageEvidence(stream)
	economic, conflicts := attempt.economicEvidenceDrain()

	record := f6TerminalRecord(t, callID, identity.StoreID, bLegID, "submission-f6-terminal", identity.ALegID, now, economic, conflicts)
	f6AssertInvalidDraftRecord(t, record.Observations, prefixKey, invalidKey, "1", "100")
}

// F6 live sideband: the valid prefix is drained live, then the invalid draft
// arrives in a later live drain. The prefix must survive and the invalid drain
// must surface the loss marker so the eventual terminal record stays incomplete.
func TestProviderEvidenceF6InvalidDraftReachesLiveSidebandDrain(t *testing.T) {
	t.Parallel()
	const (
		bLegID     = "b-f6-live"
		prefixKey  = "provider.f6.live.prefix"
		invalidKey = "provider.f6.live.invalid"
	)
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_910_000, 0).UTC()
	identity := coremetering.ObservationIdentity{
		StoreID: "store-f6-live", RequestID: "req-f6-live", CallID: callID.String(), BillingCallID: callID.String(),
		ALegID: "a-f6-live", BLegID: bLegID, AttemptID: bLegID, AttemptSeq: 1,
		ObservedAt: now, ReceivedAt: now,
	}
	buffer := coremetering.NewProviderEvidenceBuffer()
	buffer.BindEconomicEvidence(identity)
	source := &r5cProviderStream{buffer: buffer}
	attempt := newAttemptSession(attemptSessionInput{})

	buffer.Add(r5bProviderMediaMoneyDraft(prefixKey, 2))
	attempt.drainStreamUsageEvidence(source)

	buffer.Add(f6RuntimeInvalidDraft(invalidKey, 5))
	attempt.drainStreamUsageEvidence(source)

	economic, conflicts := attempt.economicEvidenceDrain()
	record := f6TerminalRecord(t, callID, identity.StoreID, bLegID, "submission-f6-live", identity.ALegID, now, economic, conflicts)
	f6AssertInvalidDraftRecord(t, record.Observations, prefixKey, invalidKey, "2", "200")
}
