package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

const r3CallID usageevidence.BillingCallID = "bc_0123456789abcdef0123456789abcdef"

// TestR3EconomicReceiptReplayProducesOneObservationNoConflict proves that a
// source event redelivered with only a different transport receipt time is the
// same semantic observation: one effective economic event and no terminal
// conflict, so the retained audit receipt stays the original delivery. Retail
// selection over the replay-equal record is Enterprise (5.2 preservation).
func TestR3EconomicReceiptReplayProducesOneObservationNoConflict(t *testing.T) {
	attempt, pipeline := r3EconomicAttempt()
	first := r3EconomicObservation("obs-r3-receipt")
	second := first.Clone()
	second.ReceivedAt = second.ReceivedAt.Add(2 * time.Hour)
	if first.Fingerprint() == second.Fingerprint() {
		t.Fatal("test fixture must differ by receipt time only")
	}

	stream := &phase7EconomicRuntimeStream{observations: []metering.Observation{first, second}}
	pipeline.consumeBackendUsageEvidenceForAttempt(context.Background(), recvTurnFacts{}, attempt, stream)

	economic, conflicts := attempt.economicEvidenceDrain()
	if len(economic) != 1 || len(conflicts) != 0 {
		t.Fatalf("receipt-only replay drain observations=%d conflicts=%d, want one and none", len(economic), len(conflicts))
	}
	record := r3SealedRecord(t, economic, nil)
	if len(record.Observations) != 1 || len(record.EvidenceConflicts) != 0 {
		t.Fatalf("receipt-only replay record observations=%d conflicts=%d, want one and none", len(record.Observations), len(record.EvidenceConflicts))
	}
	if !record.Observations[0].ReceivedAt.Equal(first.ReceivedAt) {
		t.Fatalf("retained audit receipt = %v, want original delivery %v", record.Observations[0].ReceivedAt, first.ReceivedAt)
	}
}

// TestR3EconomicLineageCarrierPlacementReplayProducesOneObservationNoConflict
// proves that the approved Subject/Correlation lineage split is a placement
// detail: equivalent carriers produce one effective observation and no
// conflict.
func TestR3EconomicLineageCarrierPlacementReplayProducesOneObservationNoConflict(t *testing.T) {
	attempt, pipeline := r3EconomicAttempt()
	correlationCarried := r3EconomicObservation("obs-r3-carrier")
	subjectCarried := correlationCarried.Clone()
	subjectCarried.Subject.ProviderRequestID = subjectCarried.Correlation.ProviderRequestID
	subjectCarried.Correlation.ProviderRequestID = ""
	if subjectCarried.IdentityKey() != correlationCarried.IdentityKey() {
		t.Fatal("carrier placement must not change the source identity")
	}
	if subjectCarried.Fingerprint() == correlationCarried.Fingerprint() {
		t.Fatal("test fixture must differ by carrier placement only")
	}

	stream := &phase7EconomicRuntimeStream{observations: []metering.Observation{correlationCarried, subjectCarried}}
	pipeline.consumeBackendUsageEvidenceForAttempt(context.Background(), recvTurnFacts{}, attempt, stream)

	economic, conflicts := attempt.economicEvidenceDrain()
	if len(economic) != 1 || len(conflicts) != 0 {
		t.Fatalf("carrier-placement replay drain observations=%d conflicts=%d, want one and none", len(economic), len(conflicts))
	}
	record := r3SealedRecord(t, economic, nil)
	if len(record.Observations) != 1 || len(record.EvidenceConflicts) != 0 {
		t.Fatalf("carrier-placement replay record observations=%d conflicts=%d, want one and none", len(record.Observations), len(record.EvidenceConflicts))
	}
}

// TestR3EconomicChangedPayloadStillConflicts proves the fix does not hide a
// genuine semantic change: a changed quantity under the same source identity
// remains a visible bounded conflict and the sealed record keeps it. The
// downstream retail refusal over the runtime-produced record is Enterprise
// (5.2 preservation: TestEnterprisePreservationRuntimeChangedEvidenceRefusesRetail).
func TestR3EconomicChangedPayloadStillConflicts(t *testing.T) {
	attempt, pipeline := r3EconomicAttempt()
	original := r3EconomicObservation("obs-r3-changed")
	changed := original.Clone()
	changed.Measures[0].Value = &metering.Decimal{Coefficient: "9", Scale: 0}

	stream := &phase7EconomicRuntimeStream{observations: []metering.Observation{original, changed}}
	pipeline.consumeBackendUsageEvidenceForAttempt(context.Background(), recvTurnFacts{}, attempt, stream)

	economic, conflicts := attempt.economicEvidenceDrain()
	if len(economic) != 1 || len(conflicts) != 1 {
		t.Fatalf("changed payload drain observations=%d conflicts=%d, want one and one", len(economic), len(conflicts))
	}
	record := r3SealedRecord(t, economic, conflicts)
	if len(record.Observations) != 1 || len(record.EvidenceConflicts) != 1 {
		t.Fatalf("changed payload record observations=%d conflicts=%d, want one and one", len(record.Observations), len(record.EvidenceConflicts))
	}
}

// TestR3EconomicChangedChargeStillConflicts proves a changed reported charge
// under the same identity is not normalized away by receipt-insensitive replay.
func TestR3EconomicChangedChargeStillConflicts(t *testing.T) {
	attempt, pipeline := r3EconomicAttempt()
	original := r3EconomicObservation("obs-r3-charge")
	changed := original.Clone()
	changed.Charges[0].Amount = &metering.Decimal{Coefficient: "11", Scale: 2}

	stream := &phase7EconomicRuntimeStream{observations: []metering.Observation{original, changed}}
	pipeline.consumeBackendUsageEvidenceForAttempt(context.Background(), recvTurnFacts{}, attempt, stream)

	economic, conflicts := attempt.economicEvidenceDrain()
	if len(economic) != 1 || len(conflicts) != 1 {
		t.Fatalf("changed charge drain observations=%d conflicts=%d, want one and one", len(economic), len(conflicts))
	}
}

// TestR3EconomicChangedCoverageStillConflicts proves a genuine disposition
// change stays visible even though the observation payload is replay-equal.
func TestR3EconomicChangedCoverageStillConflicts(t *testing.T) {
	attempt := &attemptSession{}
	observation := r3EconomicObservation("obs-r3-coverage")
	attempt.rememberEconomicEvidenceOnce(r3Evidence(observation, "complete", ""))
	attempt.rememberEconomicEvidenceOnce(r3Evidence(observation, "partial", "legacy V1 token-only evidence"))

	economic, conflicts := attempt.economicEvidenceDrain()
	if len(economic) != 1 || len(conflicts) != 1 {
		t.Fatalf("changed coverage drain observations=%d conflicts=%d, want one and one", len(economic), len(conflicts))
	}
	conflict := conflicts[0]
	if conflict.ExistingCoverage != usageevidence.EconomicEvidenceCoverageComplete ||
		conflict.IncomingCoverage != usageevidence.EconomicEvidenceCoveragePartial ||
		conflict.IncomingCoverageReason != "legacy V1 token-only evidence" {
		t.Fatalf("coverage conflict = %+v, want visible complete->partial disposition change", conflict)
	}
}

// TestR3AppendCanonicalObservationsUsesSemanticReplayIdentity tests the
// terminal local-observation seam directly: receipt-only and carrier-placement
// replays collapse to one observation with no conflict, while a changed
// measure under the same identity remains a bounded conflict.
func TestR3AppendCanonicalObservationsUsesSemanticReplayIdentity(t *testing.T) {
	base := r3EconomicObservation("obs-r3-append")
	receipt := base.Clone()
	receipt.ReceivedAt = receipt.ReceivedAt.Add(90 * time.Minute)
	carrier := base.Clone()
	carrier.Subject.ProviderRequestID = carrier.Correlation.ProviderRequestID
	carrier.Correlation.ProviderRequestID = ""

	observations, conflicts, _ := appendCanonicalObservations(nil, nil, []metering.Observation{base, receipt, carrier}, usageevidence.MaxCallLegEvidenceObservations)
	if len(observations) != 1 || len(conflicts) != 0 {
		t.Fatalf("semantic replay observations=%d conflicts=%d, want one and none", len(observations), len(conflicts))
	}

	changed := base.Clone()
	changed.Measures[0].Value = &metering.Decimal{Coefficient: "5", Scale: 0}
	observations, conflicts, _ = appendCanonicalObservations(observations, conflicts, []metering.Observation{changed}, usageevidence.MaxCallLegEvidenceObservations)
	if len(observations) != 1 || len(conflicts) != 1 {
		t.Fatalf("changed measure observations=%d conflicts=%d, want one and one", len(observations), len(conflicts))
	}
}

// TestR3ChangedScopeFailsRecordValidation proves an observation whose B-leg
// lineage does not match the record is rejected rather than silently deduped
// against a same-identity observation from another scope.
func TestR3ChangedScopeFailsRecordValidation(t *testing.T) {
	observation := r3EconomicObservation("obs-r3-scope")
	observation.Subject.BLegID = "b-leg-other"
	observation.Correlation.BLegID = "b-leg-other"

	record, err := r3Record([]execbackend.EconomicEvidence{r3Evidence(observation, "complete", "")}, nil).Seal()
	if err == nil {
		t.Fatalf("mismatched observation scope sealed without error: %+v", record)
	}
}

func r3EconomicAttempt() (*attemptSession, *responsePipeline) {
	return &attemptSession{}, newResponsePipeline()
}

func r3Evidence(observation metering.Observation, coverage, reason string) execbackend.EconomicEvidence {
	return execbackend.EconomicEvidence{Observation: observation, Coverage: coverage, CoverageReason: reason}
}

func r3EconomicObservation(id string) metering.Observation {
	now := time.Unix(1700000000, 0).UTC()
	value := metering.Decimal{Coefficient: "2", Scale: 0}
	amount := metering.Decimal{Coefficient: "7", Scale: 2}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: "source-r3", Revision: 1,
		StreamID: "stream-r3", Sequence: 1, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: "store-r3", BillingCallID: r3CallID.String(),
			ALegID: "a-leg-r3", BLegID: "b-leg-r3", AttemptSeq: 1,
		},
		Correlation: metering.CorrelationV2{
			StoreID: "store-r3", BillingCallID: r3CallID.String(), ALegID: "a-leg-r3", BLegID: "b-leg-r3",
			AttemptSeq: 1, ProviderRequestID: "provider-r3",
		},
		Semantics: metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "provider.r3",
		Measures: []metering.Measure{{
			Key:   metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "provider.media.v2"},
			Value: &value, Quality: metering.QualityObserved,
		}},
		Charges: []metering.ReportedCharge{{ChargeItemID: "charge-r3", Amount: &amount, Currency: "USD", Kind: metering.ChargeKindAggregate}},
	}
}

func r3Record(economic []execbackend.EconomicEvidence, conflicts []usageevidence.EvidenceConflict) usageevidence.CallLegUsageRecord {
	return billingLegRecord(billingLegDraft{
		callID: r3CallID, submissionID: "submission-r3", aLegID: "a-leg-r3", storeID: "store-r3",
		bLegID: "b-leg-r3", seq: 1,
		primary:   routing.Primary{Backend: "backend-r3", Model: "model-r3"},
		startedAt: time.Unix(100, 0).UTC(), finishedAt: time.Unix(101, 0).UTC(),
		command: sdkterminal.CommandNormalFinish, outcome: usageevidence.LegOutcomeWinner, surfaced: usageevidence.SurfacedYes,
		economicObservations: economic, economicConflicts: conflicts,
	})
}

func r3SealedRecord(t *testing.T, economic []execbackend.EconomicEvidence, conflicts []usageevidence.EvidenceConflict) usageevidence.CallLegUsageRecord {
	t.Helper()
	sealed, err := r3Record(economic, conflicts).Seal()
	if err != nil {
		t.Fatalf("seal r3 record: %v", err)
	}
	return sealed
}
