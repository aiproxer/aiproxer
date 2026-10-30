package runtime

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// R5-RUNTIME-A adversarial repair: drainStreamUsageEvidence destructively
// consumes source drafts before admission. Once an observation has been
// consumed it has no replay owner, so a rejected admission is an irreversible
// capture loss. The attempt must retain explicit, bounded, sticky loss state
// that survives later successful flushes and terminal accumulator resets, and
// any known-truncated prefix must stay fail-closed for terminal rating.

const r5CheckpointCapacity = maxPreTerminalEconomicCheckpointPending + maxPreTerminalEconomicCheckpointDeferred

// r5ScenarioObservation builds a distinct, non-coalescible native V2 delta
// observation carrying both a media component and provider-reported money.
func r5ScenarioObservation(sequence int) metering.Observation {
	observation := r3EconomicObservation(fmt.Sprintf("obs-r5-%d", sequence))
	observation.SourceEventKey = fmt.Sprintf("source-r5-%d", sequence)
	observation.StreamID = "stream-r5"
	observation.Revision = 1
	observation.Sequence = uint64(sequence + 1)
	observation.Semantics = metering.SemanticsDelta
	observation.Measures[0].Value = &metering.Decimal{Coefficient: strconv.Itoa(sequence + 1), Scale: 0}
	observation.Charges[0].ChargeItemID = fmt.Sprintf("charge-r5-%d", sequence)
	observation.Charges[0].Amount = &metering.Decimal{Coefficient: strconv.Itoa(sequence + 1), Scale: 2}
	return observation
}

func r5ScenarioObservations(count int) []metering.Observation {
	out := make([]metering.Observation, 0, count)
	for i := range count {
		out = append(out, r5ScenarioObservation(i))
	}
	return out
}

// TestR5EconomicCheckpointCapacityRejectionRetainsStickyCaptureLoss is the
// primary RED/GREEN regression. A durable sink is bound but unavailable, so the
// bounded pending/deferred queues saturate at r5CheckpointCapacity. Every
// further destructively drained observation is rejected with no replay owner.
// The attempt must retain bounded sticky loss state, the accepted queue must
// keep its retry ownership, and a later successful flush must clear the
// transient checkpoint error without erasing the loss. The terminal record must
// stay fail-closed because the accepted prefix is known-truncated.
func TestR5EconomicCheckpointCapacityRejectionRetainsStickyCaptureLoss(t *testing.T) {
	t.Parallel()

	const fed = r5CheckpointCapacity + 32
	sink := &refinement41ObservationSink{err: errors.New("journal unavailable")}
	attempt := newAttemptSession(attemptSessionInput{observationSink: sink})
	source := &phase7EconomicRuntimeStream{observations: r5ScenarioObservations(fed)}

	attempt.drainStreamUsageEvidence(source)

	loss := attempt.evidenceCaptureLossSnapshot()
	if !loss.present {
		t.Fatal("destructively drained checkpoint-capacity rejection left no capture-loss state")
	}
	if loss.count != fed-r5CheckpointCapacity {
		t.Fatalf("capture-loss count=%d, want %d", loss.count, fed-r5CheckpointCapacity)
	}
	if !loss.causes.has(evidenceCaptureLossCheckpointCapacity) {
		t.Fatalf("capture-loss causes=%08b, want checkpoint-capacity bit", loss.causes)
	}
	if loss.firstCause != evidenceCaptureLossCheckpointCapacity {
		t.Fatalf("capture-loss first cause=%v, want checkpoint capacity", loss.firstCause)
	}
	if loss.firstIdentity == "" {
		t.Fatal("capture-loss first identity was not preserved")
	}
	if loss.bytes == 0 {
		t.Fatal("capture-loss byte total was not recorded")
	}

	// Accepted queue retry ownership is preserved: exactly the bounded queue
	// capacity was admitted and no rejected observation acquired a dedupe marker.
	attempt.checkpointMu.Lock()
	accepted := len(attempt.checkpointPending) + len(attempt.checkpointDeferred)
	attempt.checkpointMu.Unlock()
	if accepted != r5CheckpointCapacity {
		t.Fatalf("accepted queue entries=%d, want %d", accepted, r5CheckpointCapacity)
	}
	attempt.economicMu.Lock()
	identities := len(attempt.economicIdentities)
	_, rejectedHasDedupe := attempt.economicIdentities[r5ScenarioObservation(fed-1).IdentityKey()]
	attempt.economicMu.Unlock()
	if identities != r5CheckpointCapacity {
		t.Fatalf("retained identity records=%d, want %d", identities, r5CheckpointCapacity)
	}
	if rejectedHasDedupe {
		t.Fatal("rejected observation acquired a dedupe marker and lost retry ownership")
	}

	attempt.checkpointMu.Lock()
	transientErr := attempt.checkpointErr
	attempt.checkpointMu.Unlock()
	if transientErr == nil {
		t.Fatal("capacity rejection did not surface a transient checkpoint error")
	}

	// A later successful flush clears the transient error; the loss is sticky.
	sink.err = nil
	if err := attempt.flushEconomicCheckpointsAtTerminal(context.Background()); err != nil {
		t.Fatalf("recovery flush: %v", err)
	}
	attempt.checkpointMu.Lock()
	transientErr = attempt.checkpointErr
	attempt.checkpointMu.Unlock()
	if transientErr != nil {
		t.Fatalf("transient checkpoint error survived successful flush: %v", transientErr)
	}
	if recovered := attempt.evidenceCaptureLossSnapshot(); !recovered.present || recovered.count != loss.count {
		t.Fatalf("successful flush cleared or changed capture loss: %+v", recovered)
	}

	_, _, retention := attempt.economicEvidenceDrainPartitioned()
	if cleared := attempt.evidenceCaptureLossSnapshot(); !cleared.present || cleared.count != loss.count {
		t.Fatalf("terminal accumulator reset cleared capture loss: %+v", cleared)
	}
	_, _ = attempt.billingEvidenceDrain()
	if cleared := attempt.evidenceCaptureLossSnapshot(); !cleared.present || cleared.count != loss.count {
		t.Fatalf("usage-evidence drain cleared capture loss: %+v", cleared)
	}

	wantReason := evidenceCaptureLossConflictReasonPrefix + evidenceCaptureLossCheckpointCapacity.reason()
	markerVisible := false
	for _, conflict := range retention {
		if conflict.IncomingCoverage == usageevidence.EconomicEvidenceCoverageUnsupported &&
			conflict.IncomingCoverageReason == wantReason {
			markerVisible = true
		}
	}
	if !markerVisible {
		t.Fatalf("capture-loss reserved marker missing from terminal drain: %+v", retention)
	}
}

// TestR5EconomicObservationCapRejectionRecordsStickyCaptureLoss covers the
// nil-sink terminal-only path: with no durable checkpoint consumer the queue is
// ignored and the ordinary observation cap is the rejection boundary. Overflow
// must be retained as explicit capture loss rather than silently dropped.
func TestR5EconomicObservationCapRejectionRecordsStickyCaptureLoss(t *testing.T) {
	t.Parallel()

	const overflow = 5
	fed := usageevidence.MaxCallLegEvidenceObservations + overflow
	attempt := newAttemptSession(attemptSessionInput{})
	source := &phase7EconomicRuntimeStream{observations: r5ScenarioObservations(fed)}

	attempt.drainStreamUsageEvidence(source)

	loss := attempt.evidenceCaptureLossSnapshot()
	if !loss.present {
		t.Fatal("destructively drained observation-cap rejection left no capture-loss state")
	}
	if loss.count != overflow {
		t.Fatalf("capture-loss count=%d, want %d", loss.count, overflow)
	}
	if loss.firstCause != evidenceCaptureLossObservationCap || !loss.causes.has(evidenceCaptureLossObservationCap) {
		t.Fatalf("capture-loss cause=%v causes=%08b, want observation cap", loss.firstCause, loss.causes)
	}
	if loss.firstIdentity == "" {
		t.Fatal("capture-loss first identity was not preserved")
	}

	// The explicit admission disposition names the rejection and cause.
	admission := attempt.rememberEconomicEvidenceOnce(r3Evidence(r5ScenarioObservation(fed), "complete", ""))
	if admission.disposition != economicEvidenceAdmissionRejected {
		t.Fatalf("admission disposition=%v, want rejected", admission.disposition)
	}
	if admission.cause != evidenceCaptureLossObservationCap {
		t.Fatalf("admission cause=%v, want observation cap", admission.cause)
	}
}
