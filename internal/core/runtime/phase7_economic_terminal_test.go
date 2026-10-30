package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type phase7EconomicRuntimeStream struct {
	observations []metering.Observation
}

func (*phase7EconomicRuntimeStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, nil
}

func (*phase7EconomicRuntimeStream) Send(aipapi.Event) error { return nil }
func (*phase7EconomicRuntimeStream) Close() error            { return nil }
func (*phase7EconomicRuntimeStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (s *phase7EconomicRuntimeStream) DrainEconomicObservations() []metering.Observation {
	out := make([]metering.Observation, len(s.observations))
	for i, observation := range s.observations {
		out[i] = observation.Clone()
	}
	s.observations = nil
	return out
}

func TestPhase7EconomicTerminalBridgeRetainsCanonicalObservationAndConflict(t *testing.T) {
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	observation := phase7EconomicRuntimeObservation()
	changed := observation
	changed.Measures = append([]metering.Measure(nil), observation.Measures...)
	changed.Measures[0].Value = &metering.Decimal{Coefficient: "3", Scale: 0}
	stream := &phase7EconomicRuntimeStream{observations: []metering.Observation{observation, observation, changed}}
	attempt := &attemptSession{}
	pipeline := newResponsePipeline()
	pipeline.consumeBackendUsageEvidenceForAttempt(context.Background(), recvTurnFacts{}, attempt, stream)

	observations, conflicts := attempt.economicEvidenceDrain()
	if len(observations) != 1 || len(conflicts) != 1 {
		t.Fatalf("economic drain observations=%d conflicts=%d, want one and one", len(observations), len(conflicts))
	}
	record := billingLegRecord(billingLegDraft{
		callID: callID, aLegID: "a-phase7", storeID: "store-phase7", bLegID: "b-phase7", seq: 1,
		primary:   routing.Primary{Backend: "backend", Model: "model"},
		startedAt: time.Unix(100, 0).UTC(), finishedAt: time.Unix(101, 0).UTC(),
		command: sdkterminal.CommandNormalFinish, economicObservations: observations, economicConflicts: conflicts,
	})
	if len(record.Observations) != 1 || len(record.EvidenceConflicts) != 1 {
		t.Fatalf("terminal record observations=%d conflicts=%d, want one and one", len(record.Observations), len(record.EvidenceConflicts))
	}
	if record.Observations[0].Charges[0].Amount == nil || record.Observations[0].Charges[0].Amount.CanonicalString() != "7/2" {
		t.Fatalf("terminal observation lost exact charge: %+v", record.Observations[0].Charges)
	}
	if record.Observations[0].Subject.BLegID != "b-phase7" || record.Observations[0].Correlation.ProviderRequestID != "provider-phase7" {
		t.Fatalf("terminal observation lost subject/provider identity: %+v", record.Observations[0])
	}
}

func TestPhase7EconomicFinalizerResultIsCachedWithV2Evidence(t *testing.T) {
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	observation := phase7EconomicRuntimeObservation()
	var calls int
	attempt := &attemptSession{finalizeBillingV2: func(context.Context, execbackend.BillingFinalizationInput) (execbackend.BillingFinalizationResult, error) {
		calls++
		return execbackend.BillingFinalizationResult{
			Usage:            aipapi.Event{Kind: aipapi.EventUsageDelta, UsagePresence: aipapi.UsagePresence{TotalTokens: true}, TotalTokens: 2},
			EconomicEvidence: []execbackend.EconomicEvidence{{Observation: observation, Coverage: string(backendplugin.EvidenceCoverageComplete)}},
		}, nil
	}}
	state := newBillingCallState(callID)
	in := execbackend.BillingFinalizationInput{TraceID: "trace-phase7", ALegID: "a-phase7", BLegID: "b-phase7", Backend: "backend", Model: "model"}
	result, ok := attempt.finalizeBillingResult(context.Background(), state, in)
	if !ok || len(result.EconomicEvidence) != 1 {
		t.Fatalf("first finalizer result=%+v ok=%v", result, ok)
	}
	result, ok = attempt.finalizeBillingResult(context.Background(), state, in)
	if !ok || len(result.EconomicEvidence) != 1 || calls != 1 {
		t.Fatalf("cached finalizer result=%+v ok=%v calls=%d, want one call/evidence", result, ok, calls)
	}
	for _, economic := range result.EconomicEvidence {
		attempt.rememberEconomicObservationOnce(economic.Observation)
	}
	observations, conflicts := attempt.economicEvidenceDrain()
	if len(observations) != 1 || len(conflicts) != 0 {
		t.Fatalf("captured finalizer observations=%d conflicts=%d, want one and none", len(observations), len(conflicts))
	}
}

func TestPhase7EconomicRuntimeTerminalRetainsV1PartialDisposition(t *testing.T) {
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	input, output := int64(2), int64(3)
	lifted, err := backendplugin.LiftAccountingEvidenceV1(backendplugin.AccountingEvidence{
		InputTokens: &input, OutputTokens: &output,
		Presence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
		Source:   backendplugin.AccountingSourceProviderReported, Authority: backendplugin.AccountingAuthorityAuthoritative,
		Plane: backendplugin.AccountingPlaneProviderBillable, DedupeKey: "legacy-phase7-terminal",
	}, backendplugin.V1EvidenceIdentity{
		StoreID: "store-phase7", ObservationID: "legacy-phase7-observation", SourceEventKey: "legacy-phase7-terminal",
		Revision: 1, StreamID: "stream-phase7", Subject: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-phase7", BillingCallID: callID.String(), BLegID: "b-v1"},
		Correlation: metering.CorrelationV2{StoreID: "store-phase7", BillingCallID: callID.String(), BLegID: "b-v1"}, ObservedAt: time.Unix(100, 0).UTC(), ReceivedAt: time.Unix(100, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("V1 lift: %v", err)
	}
	attempt := &attemptSession{}
	attempt.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{
		Observation: lifted.Observation, Coverage: string(lifted.Coverage), CoverageReason: lifted.CoverageReason,
	})
	economic, conflicts := attempt.economicEvidenceDrain()
	record := billingLegRecord(billingLegDraft{
		callID: callID, aLegID: "a-v1", storeID: "store-phase7", bLegID: "b-v1", seq: 1,
		primary: routing.Primary{Backend: "backend", Model: "model"}, startedAt: time.Unix(100, 0).UTC(), finishedAt: time.Unix(101, 0).UTC(),
		command: sdkterminal.CommandNormalFinish, economicObservations: economic, economicConflicts: conflicts,
	})
	if len(record.Observations) != 1 || len(record.EconomicDispositions) != 1 {
		t.Fatalf("terminal record observations=%d dispositions=%d", len(record.Observations), len(record.EconomicDispositions))
	}
	disposition := record.EconomicDispositions[0]
	if disposition.Coverage != usageevidence.EconomicEvidenceCoveragePartial || disposition.CoverageReason != "legacy V1 token-only evidence" {
		t.Fatalf("V1 coverage disposition = %+v", disposition)
	}
	if record.Observations[0].Fingerprint() == "" {
		t.Fatal("terminal provider observation fingerprint was lost")
	}
}

func TestPhase7EconomicRuntimeTerminalDispositionConflictIsVisible(t *testing.T) {
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	observation := phase7EconomicRuntimeObservation()
	attempt := &attemptSession{}
	attempt.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{Observation: observation, Coverage: string(backendplugin.EvidenceCoverageComplete)})
	attempt.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{
		Observation: observation, Coverage: string(backendplugin.EvidenceCoveragePartial), CoverageReason: "legacy V1 token-only evidence",
	})
	economic, conflicts := attempt.economicEvidenceDrain()
	if len(economic) != 1 || len(conflicts) != 1 {
		t.Fatalf("economic drain observations=%d conflicts=%d, want one and one", len(economic), len(conflicts))
	}
	record := billingLegRecord(billingLegDraft{
		callID: callID, aLegID: "a-phase7", storeID: "store-phase7", bLegID: "b-phase7", seq: 1,
		primary: routing.Primary{Backend: "backend", Model: "model"}, startedAt: time.Unix(100, 0).UTC(), finishedAt: time.Unix(101, 0).UTC(),
		command: sdkterminal.CommandNormalFinish, outcome: usageevidence.LegOutcomeWinner, surfaced: usageevidence.SurfacedYes,
		economicObservations: economic, economicConflicts: conflicts,
	})
	if len(record.EconomicDispositions) != 1 || len(record.EvidenceConflicts) != 1 {
		t.Fatalf("terminal disposition/conflicts = %d/%d, want one/one", len(record.EconomicDispositions), len(record.EvidenceConflicts))
	}
	conflict := record.EvidenceConflicts[0]
	if conflict.ExistingCoverage != usageevidence.EconomicEvidenceCoverageComplete || conflict.IncomingCoverage != usageevidence.EconomicEvidenceCoveragePartial || conflict.IncomingCoverageReason != "legacy V1 token-only evidence" {
		t.Fatalf("coverage conflict metadata = %+v", conflict)
	}
	if _, err := record.Seal(); err != nil {
		t.Fatalf("visible coverage conflict must remain safely durable: %v", err)
	}
}

func phase7EconomicRuntimeObservation() metering.Observation {
	now := time.Unix(1700000000, 0).UTC()
	value := metering.Decimal{Coefficient: "2", Scale: 0}
	amount := metering.Decimal{Coefficient: "7", Scale: 2}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: "obs-phase7", SourceEventKey: "source-phase7", Revision: 1,
		StreamID: "stream-phase7", Sequence: 1, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-phase7", ALegID: "a-phase7", BLegID: "b-phase7", AttemptSeq: 1},
		Correlation: metering.CorrelationV2{StoreID: "store-phase7", ALegID: "a-phase7", BLegID: "b-phase7", AttemptSeq: 1, ProviderRequestID: "provider-phase7"},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "provider.phase7",
		Measures: []metering.Measure{{Key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "provider.media.v2"}, Value: &value, Quality: metering.QualityObserved}},
		Charges:  []metering.ReportedCharge{{ChargeItemID: "charge-phase7", Amount: &amount, Currency: "USD", Kind: metering.ChargeKindAggregate}},
	}
}
