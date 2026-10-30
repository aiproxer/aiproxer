package runtime

// F4b adversarial repair: the terminal evidence envelope is bounded at
// billing.MaxCallLegEvidenceObservations. billingLegRecord appends provider
// stream/finalizer observations, then independently admitted economic
// observations, then the validated final local origin last. Both bounded
// append helpers stopped at the envelope cap, so a full provider/economic
// population silently displaced the final local measurement even when every
// checkpoint admission had succeeded and left no capture-loss marker. The
// record then sealed without the final local value and without any fail-closed
// disposition.
//
// The repair reserves bounded envelope capacity for the validated final local
// origin before provider/economic evidence can consume the whole envelope, and
// records a trusted reserved fail-closed disposition for any origin that still
// cannot be included. Provider/economic evidence keeps priority for every
// unreserved slot and is never silently dropped.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// f4bUsageEvent builds a distinct provider-reported usage event whose neutral
// V1->V2 projection yields one terminal observation.
func f4bUsageEvent(index int) aipapi.Event {
	return aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   index + 1,
		UsagePresence: aipapi.UsagePresence{InputTokens: true},
		Accounting: aipapi.UsageAccountingMetadata{
			Plane:     aipapi.UsagePlaneProviderBillable,
			Source:    aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative,
			DedupeKey: fmt.Sprintf("provider-usage-%d", index),
		},
	}
}

// f4bEconomicEvidence builds one distinct native economic observation.
func f4bEconomicEvidence(index uint64) execbackend.EconomicEvidence {
	observation := refinement41Observation(index, "1")
	observation.Semantics = metering.SemanticsDelta
	return execbackend.EconomicEvidence{Observation: observation}
}

// f4bHasEnvelopeLossMarker reports whether the record carries the trusted
// reserved fail-closed disposition for terminal evidence envelope truncation.
func f4bHasEnvelopeLossMarker(record usageevidence.CallLegUsageRecord) bool {
	wantReason := evidenceCaptureLossConflictReasonPrefix + evidenceCaptureLossObservationCap.reason()
	for _, conflict := range record.EvidenceConflicts {
		if conflict.IncomingCoverage == usageevidence.EconomicEvidenceCoverageUnsupported && conflict.IncomingCoverageReason == wantReason {
			return true
		}
	}
	return false
}

// TestF4bTerminalEnvelopeReservesFinalLocalOriginForProviderOverflow drives the
// real terminal constructor with a full provider-origin population plus a final
// local measurement. The final local origin must survive inside the reserved
// envelope capacity and the displaced provider tail must stay visible as a
// reserved fail-closed disposition.
func TestF4bTerminalEnvelopeReservesFinalLocalOriginForProviderOverflow(t *testing.T) {
	t.Parallel()
	sink := &refinement41ObservationSink{}
	session, boundary := f4LocalBoundaryAttempt(sink)
	f4ObserveProviderOutput(boundary, 80) // provider-output text tokens = 20
	for i := range usageevidence.MaxCallLegEvidenceObservations {
		if !session.rememberUsageEvidenceOnceAs(f4bUsageEvent(i), billingEvidenceRoleStream) {
			t.Fatalf("provider usage evidence %d was not retained", i)
		}
	}

	record, terminalErr := f4Terminalize(t, session, IntentSuccess, sdkterminal.CommandNormalFinish, usageevidence.LegOutcomeWinner)
	if terminalErr != nil {
		t.Fatalf("terminal flush error: %v", terminalErr)
	}
	if got := f4LocalProviderOutputTokens(t, record); got != "20" {
		t.Fatalf("final local provider-output tokens=%q, want 20 (silently displaced)", got)
	}
	if !f4bHasEnvelopeLossMarker(record) {
		t.Fatalf("provider overflow left no reserved fail-closed disposition: %+v", record.EvidenceConflicts)
	}
	if len(record.Observations) > usageevidence.MaxCallLegEvidenceObservations {
		t.Fatalf("record observations=%d exceeds envelope cap %d", len(record.Observations), usageevidence.MaxCallLegEvidenceObservations)
	}
}

// TestF4bTerminalEnvelopeReservesFinalLocalOriginForEconomicOverflow drives the
// real terminal constructor with a full native economic population plus a final
// local measurement. Checkpoint admission is not exercised (no durable sink),
// so the envelope truncation disposition is the only loss signal.
func TestF4bTerminalEnvelopeReservesFinalLocalOriginForEconomicOverflow(t *testing.T) {
	t.Parallel()
	session, boundary := f4LocalBoundaryAttempt(nil)
	f4ObserveProviderOutput(boundary, 80)
	for i := uint64(1); i <= uint64(usageevidence.MaxCallLegEvidenceObservations); i++ {
		session.rememberEconomicObservationOnce(refinement41Observation(i, "1"))
	}

	record, terminalErr := f4Terminalize(t, session, IntentSuccess, sdkterminal.CommandNormalFinish, usageevidence.LegOutcomeWinner)
	if terminalErr != nil {
		t.Fatalf("terminal flush error: %v", terminalErr)
	}
	if got := f4LocalProviderOutputTokens(t, record); got != "20" {
		t.Fatalf("final local provider-output tokens=%q, want 20 (silently displaced)", got)
	}
	if !f4bHasEnvelopeLossMarker(record) {
		t.Fatalf("economic overflow left no reserved fail-closed disposition: %+v", record.EvidenceConflicts)
	}
	if len(record.Observations) > usageevidence.MaxCallLegEvidenceObservations {
		t.Fatalf("record observations=%d exceeds envelope cap %d", len(record.Observations), usageevidence.MaxCallLegEvidenceObservations)
	}
}

// TestF4bTerminalEnvelopeRecordsLossWhenLocalOriginOverflowsReserve covers the
// opposite origin: a large non-local population plus more local observations
// than the bounded reserve. Both origins are truncated, the local origin keeps
// its reserved capacity, and the record stays fail-closed.
func TestF4bTerminalEnvelopeRecordsLossWhenLocalOriginOverflowsReserve(t *testing.T) {
	t.Parallel()
	economic := make([]execbackend.EconomicEvidence, 0, 1020)
	for i := range uint64(1020) {
		economic = append(economic, f4bEconomicEvidence(i+1))
	}
	local := make([]metering.Observation, 0, 20)
	for i := range 20 {
		observation := refinement41Observation(uint64(i+1), "20")
		observation.SourceEventKey = fmt.Sprintf("local-overflow-%d", i)
		observation.Origin = metering.OriginLocal
		observation.Acquisition = metering.AcquisitionLocalMeasurement
		observation.Boundary = metering.BoundaryBackendIngress
		local = append(local, observation)
	}

	record := billingLegRecord(billingLegDraft{
		callID: f4CallID, aLegID: "a-f4", storeID: "store-f4", bLegID: "b-f4", seq: 1,
		primary:              routing.Primary{Backend: "backend-f4", Model: "model-f4"},
		startedAt:            time.Unix(1_700_000_000, 0).UTC(),
		finishedAt:           time.Unix(1_700_000_001, 0).UTC(),
		economicObservations: economic,
		localObservations:    local,
	})

	localCount := 0
	for _, observation := range record.Observations {
		if strings.HasPrefix(observation.SourceEventKey, "local-overflow-") {
			localCount++
		}
	}
	if localCount == 0 {
		t.Fatal("local origin reserve was not honored: no local observation retained")
	}
	if localCount >= len(local) {
		t.Fatalf("local overflow retained %d observations, want bounded below %d", localCount, len(local))
	}
	if !f4bHasEnvelopeLossMarker(record) {
		t.Fatalf("local and provider truncation left no reserved fail-closed disposition: %+v", record.EvidenceConflicts)
	}
	if len(record.Observations) > usageevidence.MaxCallLegEvidenceObservations {
		t.Fatalf("record observations=%d exceeds envelope cap %d", len(record.Observations), usageevidence.MaxCallLegEvidenceObservations)
	}
}

// TestF4bTerminalEnvelopeMarksDroppedLegacyOriginWithoutLocalInput covers the
// legacy V1 conversion cap that fires before billingLegRecord reserves anything.
// With 1024 distinct captured legacy observations plus a distinct direct
// terminal event and no local origin, observationsFromBillingEvidence drops the
// direct origin itself and the builder previously saw a full 1024 population
// with envelopeDropped=0, so the finalizer disappeared without any trusted
// reserved marker. The conversion drop count must reach the builder.
func TestF4bTerminalEnvelopeMarksDroppedLegacyOriginWithoutLocalInput(t *testing.T) {
	t.Parallel()
	sink := &refinement41ObservationSink{}
	session, _ := f4LocalBoundaryAttempt(sink)
	session.boundary = nil
	for i := range usageevidence.MaxCallLegEvidenceObservations {
		if !session.rememberUsageEvidenceOnceAs(f4bUsageEvent(i), billingEvidenceRoleStream) {
			t.Fatalf("legacy usage evidence %d was not retained", i)
		}
	}
	direct := f4bUsageEvent(0)
	direct.Accounting.DedupeKey = "distinct-direct-terminal"
	direct.InputTokens = 7

	var captured *usageevidence.CallLegUsageRecord
	session.observeBillingLeg = func(_ context.Context, record usageevidence.CallLegUsageRecord) {
		cloned := record.Clone()
		captured = &cloned
	}
	result := session.TerminalizeAttempt(context.Background(), IntentSuccess, attemptEvidence{
		Command: sdkterminal.CommandNormalFinish, LegOutcome: usageevidence.LegOutcomeWinner, StreamFallback: direct,
	})
	if result.Result.Err != nil {
		t.Fatalf("terminal error: %v", result.Result.Err)
	}
	if captured == nil {
		t.Fatal("direct terminal branch did not hand off a billing leg record")
	}
	if len(captured.Observations) > usageevidence.MaxCallLegEvidenceObservations {
		t.Fatalf("record observations=%d exceeds envelope cap %d", len(captured.Observations), usageevidence.MaxCallLegEvidenceObservations)
	}
	if !f4bHasEnvelopeLossMarker(*captured) {
		t.Fatalf("dropped legacy terminal origin left no reserved fail-closed disposition: %+v", captured.EvidenceConflicts)
	}
}
