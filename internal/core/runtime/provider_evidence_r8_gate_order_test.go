package runtime

import (
	"testing"
	"time"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// R8 gate-order terminal proof. An unsupported-ordering draft that also omits
// its source key must not vanish: the loss marker produced by
// ProviderEvidenceBuffer.Add has to survive the real runtime evidence drain and
// terminal record assembly, and the canonical reduction over that terminal
// record must stay incomplete. This is the runtime half of the boundary; the
// metering tests prove the buffer-level marker and cause, and
// retail_rating.go:948-1000 keeps a non-quantity B-leg observation as an
// incomplete retail reference (ErrQuantityIncomplete), never a complete payable
// valuation.
func TestProviderEvidenceR8GateOrderMarkerReachesTerminalRecord(t *testing.T) {
	t.Parallel()

	const bLegID = "b-r8-gate"
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_800_000, 0).UTC()
	identity := coremetering.ObservationIdentity{
		StoreID: "store-r8-gate", RequestID: "req-r8-gate", CallID: callID.String(), BillingCallID: callID.String(),
		ALegID: "a-r8-gate", BLegID: bLegID, AttemptID: bLegID, AttemptSeq: 1,
		ObservedAt: now, ReceivedAt: now,
	}

	buffer := coremetering.NewProviderEvidenceBuffer()
	buffer.BindEconomicEvidence(identity)
	buffer.Add(r5bProviderMediaMoneyDraft("provider.r8.gate.prefix", 1))
	malformed := r5bProviderMediaMoneyDraft("provider.r8.gate.malformed", 3)
	malformed.SourceEventKey = ""
	malformed.Revision = 1
	buffer.Add(malformed)
	buffer.Add(r5bProviderMediaMoneyDraft("provider.r8.gate.update", 2))

	attempt := newAttemptSession(attemptSessionInput{})
	stream := &phase7EconomicRuntimeStream{observations: buffer.DrainEconomicObservations()}
	attempt.drainStreamUsageEvidence(stream)
	economic, conflicts := attempt.economicEvidenceDrain()

	record := billingLegRecord(billingLegDraft{
		callID: callID, submissionID: "submission-r8-gate", aLegID: "a-r8-gate",
		storeID: "store-r8-gate", bLegID: bLegID, seq: 1,
		primary:   routing.Primary{Backend: "backend-r8-gate", Model: "model-r8-gate"},
		startedAt: now, finishedAt: now.Add(time.Second),
		command: sdkterminal.CommandNormalFinish, outcome: usageevidence.LegOutcomeWinner, surfaced: usageevidence.SurfacedYes,
		economicObservations: economic, economicConflicts: conflicts,
	})
	if _, err := record.Seal(); err != nil {
		t.Fatalf("terminal record with loss marker failed sealing: %v", err)
	}

	markerVisible := false
	for _, observation := range record.Observations {
		if observation.Authority != metering.AuthorityUnavailableClaim {
			continue
		}
		for _, measure := range observation.Measures {
			if measure.Quality == metering.QualityUnavailable && measure.Reason == "unsupported_ordering_identity" {
				markerVisible = true
			}
		}
	}
	if !markerVisible {
		t.Fatalf("unsupported-ordering loss marker did not reach the terminal record: %+v", record.Observations)
	}

	snapshot, err := aggregate.ApplyObservations(record.Observations)
	if err != nil {
		t.Fatalf("reduce terminal record observations: %v", err)
	}
	if snapshot.Complete {
		t.Fatal("terminal record with an unsupported-ordering marker reduced complete; prefix could be billed")
	}
}
