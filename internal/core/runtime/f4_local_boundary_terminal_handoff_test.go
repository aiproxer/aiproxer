package runtime

// F4 adversarial repair: the pre-terminal economic checkpoint queue and the
// local boundary head map are separate owners. A valid local boundary head can
// already be durable while every ordinary pending/deferred queue slot is full.
// When the terminal drain then observes a changed local measurement, queue
// admission rejects it. The old code republished the stale prior head as if it
// were the final measurement and recorded no capture loss, so the sealed
// record silently carried a stale local value even though a later unrelated
// flush succeeded.
//
// The production direct terminal branch (TerminalizeAttempt without
// BillingLegFn) is the entrypoint under test: it drains local observations into
// the terminal record, flushes the checkpoint queue, then builds the record.
//
// Scope: the in-memory observation-sink vectors and the file-backed
// checkpoint-journal restart/count vectors stay here and assert the runtime
// record facts (final local measurement, no stale republish, sticky
// checkpoint-capacity loss marker) and the durable journal ownership (exact
// accepted-prefix rows, prior local head durable, final local head without a
// durable owner). The durable billing-store read-back and the
// marker-vs-control retail valuation are Enterprise (5.2 preservation:
// TestEnterprisePreservationRuntimeDurableRestartRetainsFinalLocalMeasurement
// and ...DurableSinkFailureRetainsMarker).

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

const f4CallID = usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef")

// f4LocalBoundaryAttempt builds an attempt whose direct TerminalizeAttempt
// branch owns the terminal billing record (no BillingLegFn), with a live local
// boundary accumulator and a toggleable durable observation sink.
func f4LocalBoundaryAttempt(sink metering.ObservationSink) (*attemptSession, *coremetering.BoundaryAccumulator) {
	boundary := coremetering.NewBoundaryAccumulator()
	boundary.PrepareCall(aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("input")}}}})
	boundary.MarkAttempted()
	boundary.MarkAccepted(true)
	session := &attemptSession{
		terminal:        newStreamTerminal(sdkterminal.ScopeAttempt),
		boundary:        boundary,
		observationSink: sink,
		// The file-backed journal vectors reopen the durable store and assert the
		// final local measurement survived, so the terminal flush must actually
		// complete. A real file-backed SQLite store under -race on a loaded host
		// exceeds the production 2s bound through no defect of this test, which
		// would drop observations those tests then verify. Raise the budget for
		// this attempt only; production requests keep economicCheckpointFlushTimeout
		// unchanged.
		terminalFlushBudget: 60 * time.Second,
		bleg:                b2bua.BLegRecord{ALegID: "a-f4", BLegID: "b-f4", Seq: 1},
		cand:                routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-f4", Model: "model-f4"}},
		billingCallID:       f4CallID,
		billingStoreID:      "store-f4",
		requestID:           "request-f4",
		now:                 func() time.Time { return time.Unix(1_700_000_100, 0).UTC() },
		billingEnabled:      func() bool { return true },
	}
	return session, boundary
}

// f4RecordingTerminal is the money-free recording terminal port used by the
// file-backed journal vectors. It seals and records exactly the leg the runtime
// hands to TerminalUsageSink through appendBillingLegStrict and never touches a
// store, journal or monetary path. Durable billing-store persistence of the
// same leg is Enterprise-owned (see the file header).
type f4RecordingTerminal struct {
	mu   sync.Mutex
	legs []usageevidence.CallLegUsageRecord
}

func (p *f4RecordingTerminal) append(_ context.Context, _ usageevidence.BillingCallID, record usageevidence.CallLegUsageRecord) error {
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.legs = append(p.legs, sealed)
	return nil
}

func (p *f4RecordingTerminal) snapshot() []usageevidence.CallLegUsageRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]usageevidence.CallLegUsageRecord(nil), p.legs...)
}

// f4ObserveProviderOutput appends a bounded text delta so the local provider
// output plane accumulates byteCount text bytes.
func f4ObserveProviderOutput(boundary *coremetering.BoundaryAccumulator, byteCount int) {
	boundary.ObserveProviderEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: strings.Repeat("x", byteCount)})
}

// f4SaturateOrdinaryCheckpointQueue fills every pending and deferred slot with
// a unique, non-coalescing accepted delta observation scoped to the attempt's
// durable store/call identity. The identity scoping is required by any REAL
// observation journal (and by the terminal leg validator): an unscoped
// observation would be rejected as out-of-scope rather than saturating the
// queue. Distinct revisions keep every delta observation a distinct economic
// identity, so no two coalesce.
func f4SaturateOrdinaryCheckpointQueue(t *testing.T, session *attemptSession) {
	t.Helper()
	total := maxPreTerminalEconomicCheckpointPending + maxPreTerminalEconomicCheckpointDeferred
	for i := uint64(1); i <= uint64(total); i++ {
		observation := f4OrdinaryCheckpointObservation(session, i)
		if admission := session.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{Observation: observation}); admission.disposition == economicEvidenceAdmissionRejected {
			t.Fatalf("ordinary checkpoint %d rejected before saturation: %+v", i, admission)
		}
	}
	session.checkpointMu.Lock()
	queued := len(session.checkpointPending) + len(session.checkpointDeferred)
	session.checkpointMu.Unlock()
	if queued != total {
		t.Fatalf("saturated checkpoint queue entries=%d, want %d", queued, total)
	}
}

// f4OrdinaryCheckpointObservation builds one distinct non-coalescing ordinary
// delta observation in the attempt's own durable scope.
func f4OrdinaryCheckpointObservation(session *attemptSession, revision uint64) metering.Observation {
	observation := refinement41Observation(revision, "1")
	observation.Semantics = metering.SemanticsDelta
	observation.ID = fmt.Sprintf("f4-ordinary-%d", revision)
	observation.SourceEventKey = "f4-ordinary-checkpoint"
	observation.StreamID = "f4-ordinary-stream"
	storeID := session.billingStoreID
	callID := session.billingCallID.String()
	observation.Subject = metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID,
		CallID: callID, BillingCallID: callID,
		ALegID: session.bleg.ALegID, BLegID: session.bleg.BLegID,
		AttemptID: session.bleg.BLegID, AttemptSeq: uint64(session.bleg.Seq),
	}
	observation.Correlation = metering.CorrelationV2{
		StoreID: storeID, CallID: callID, BillingCallID: callID,
		ALegID: session.bleg.ALegID, BLegID: session.bleg.BLegID,
		AttemptID: session.bleg.BLegID, AttemptSeq: uint64(session.bleg.Seq),
	}
	return observation
}

// f4LocalProviderOutputTokens returns the coefficient of the local
// provider-output text-token measure in the terminal record.
func f4LocalProviderOutputTokens(t *testing.T, record usageevidence.CallLegUsageRecord) string {
	t.Helper()
	if tokens, ok := f4ObservationsLocalProviderOutputTokens(record.Observations); ok {
		return tokens
	}
	t.Fatalf("terminal record has no local provider-output token observation: %+v", record.Observations)
	return ""
}

// f4ObservationsLocalProviderOutputTokens extracts the local provider-output
// text-token coefficient from a set of observations.
func f4ObservationsLocalProviderOutputTokens(observations []metering.Observation) (string, bool) {
	for _, observation := range observations {
		if observation.Origin != metering.OriginLocal || observation.Boundary != metering.BoundaryBackendIngress {
			continue
		}
		for _, measure := range observation.Measures {
			if measure.Key.Direction == metering.DirectionOutput && measure.Key.Component == metering.ComponentTextToken && measure.Value != nil {
				return measure.Value.Coefficient, true
			}
		}
	}
	return "", false
}

// f4IsCheckpointCapacityLossConflict reports whether one conflict is the
// runtime-owned reserved capture-loss marker for checkpoint-capacity loss.
func f4IsCheckpointCapacityLossConflict(conflict usageevidence.EvidenceConflict) bool {
	wantReason := evidenceCaptureLossConflictReasonPrefix + evidenceCaptureLossCheckpointCapacity.reason()
	return conflict.IncomingCoverage == usageevidence.EconomicEvidenceCoverageUnsupported && conflict.IncomingCoverageReason == wantReason
}

// f4HasCheckpointCapacityLossMarker reports whether the record carries the
// runtime-owned reserved capture-loss marker for checkpoint-capacity loss.
func f4HasCheckpointCapacityLossMarker(record usageevidence.CallLegUsageRecord) bool {
	return slices.ContainsFunc(record.EvidenceConflicts, f4IsCheckpointCapacityLossConflict)
}

func f4Terminalize(t *testing.T, session *attemptSession, intent attemptTerminalIntent, cmd sdkterminal.Command, outcome usageevidence.LegOutcome) (usageevidence.CallLegUsageRecord, error) {
	t.Helper()
	var captured *usageevidence.CallLegUsageRecord
	session.observeBillingLeg = func(_ context.Context, record usageevidence.CallLegUsageRecord) {
		cloned := record.Clone()
		captured = &cloned
	}
	result := session.TerminalizeAttempt(context.Background(), intent, attemptEvidence{Command: cmd, LegOutcome: outcome})
	if captured == nil {
		t.Fatal("direct terminal branch did not hand off a billing leg record")
	}
	return *captured, result.Result.Err
}

// TestF4TerminalHandoffRetainsFinalLocalMeasurementWhenQueueSaturated is the
// primary RED/GREEN regression. The prior local head (output tokens = 10) is
// durably checkpointed and flushed, the ordinary queue is saturated, and then
// the final local measurement (output tokens = 20) cannot be queued. The
// terminal record must carry the immutable final measurement, and the durable
// checkpoint truncation must stay visible as sticky capture loss that a later
// successful terminal flush does not clear.
func TestF4TerminalHandoffRetainsFinalLocalMeasurementWhenQueueSaturated(t *testing.T) {
	t.Parallel()
	sink := &refinement41ObservationSink{}
	session, boundary := f4LocalBoundaryAttempt(sink)

	// Step 1: valid local head output tokens = 10, durably checkpointed.
	f4ObserveProviderOutput(boundary, 40)
	session.checkpointLocalBoundaryObservations(session.economicCheckpointNow())
	if err := session.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("initial local checkpoint flush: %v", err)
	}
	const durableHeads = 3
	durable := sink.observationsSnapshot()
	if len(durable) != durableHeads {
		t.Fatalf("durable pre-terminal local observations=%d, want %d", len(durable), durableHeads)
	}
	if prior, ok := f4ObservationsLocalProviderOutputTokens(durable); !ok || prior != "10" {
		t.Fatalf("durable local prior head tokens=%q ok=%v, want 10", prior, ok)
	}

	// Step 2: saturate every ordinary pending+deferred slot.
	f4SaturateOrdinaryCheckpointQueue(t, session)

	// Step 3: the actual final local measurement is output tokens = 20.
	f4ObserveProviderOutput(boundary, 40)

	record, terminalErr := f4Terminalize(t, session, IntentSuccess, sdkterminal.CommandNormalFinish, usageevidence.LegOutcomeWinner)
	if terminalErr != nil {
		t.Fatalf("terminal flush unexpectedly failed: %v", terminalErr)
	}

	if got := f4LocalProviderOutputTokens(t, record); got != "20" {
		t.Fatalf("terminal local provider-output tokens=%q, want final 20 (stale prior labeled as final)", got)
	}
	if !f4HasCheckpointCapacityLossMarker(record) {
		t.Fatalf("terminal record missing sticky checkpoint-capacity capture loss: %+v", record.EvidenceConflicts)
	}
	if loss := session.evidenceCaptureLossSnapshot(); !loss.present || !loss.causes.has(evidenceCaptureLossCheckpointCapacity) {
		t.Fatalf("capture loss not retained after successful terminal flush: %+v", loss)
	}
}

// TestF4TerminalHandoffRetainsMeasurementWhenTerminalFlushAlsoFails covers the
// continuing sink failure: the final local measurement must still reach the
// terminal record and the loss must still be recorded even though the terminal
// checkpoint flush returns an error.
func TestF4TerminalHandoffRetainsMeasurementWhenTerminalFlushAlsoFails(t *testing.T) {
	t.Parallel()
	sink := &refinement41ObservationSink{}
	session, boundary := f4LocalBoundaryAttempt(sink)

	f4ObserveProviderOutput(boundary, 40)
	session.checkpointLocalBoundaryObservations(session.economicCheckpointNow())
	if err := session.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("initial local checkpoint flush: %v", err)
	}

	f4SaturateOrdinaryCheckpointQueue(t, session)
	f4ObserveProviderOutput(boundary, 40)

	sink.mu.Lock()
	sink.err = errors.New("journal unavailable")
	sink.mu.Unlock()

	record, terminalErr := f4Terminalize(t, session, IntentSuccess, sdkterminal.CommandNormalFinish, usageevidence.LegOutcomeWinner)
	if terminalErr == nil || !strings.Contains(terminalErr.Error(), "journal unavailable") {
		t.Fatalf("terminal flush error=%v, want journal unavailable", terminalErr)
	}
	if got := f4LocalProviderOutputTokens(t, record); got != "20" {
		t.Fatalf("terminal local provider-output tokens=%q, want final 20 despite flush failure", got)
	}
	if !f4HasCheckpointCapacityLossMarker(record) {
		t.Fatalf("terminal record missing sticky capture loss: %+v", record.EvidenceConflicts)
	}
}

// TestF4CancellationTerminalHandoffRetainsFinalLocalMeasurement covers the
// cancellation terminal intent through the same direct record branch.
func TestF4CancellationTerminalHandoffRetainsFinalLocalMeasurement(t *testing.T) {
	t.Parallel()
	sink := &refinement41ObservationSink{}
	session, boundary := f4LocalBoundaryAttempt(sink)

	f4ObserveProviderOutput(boundary, 40)
	session.checkpointLocalBoundaryObservations(session.economicCheckpointNow())
	if err := session.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("initial local checkpoint flush: %v", err)
	}

	f4SaturateOrdinaryCheckpointQueue(t, session)
	f4ObserveProviderOutput(boundary, 40)

	record, terminalErr := f4Terminalize(t, session, IntentCancellation, sdkterminal.CommandCancel, usageevidence.LegOutcomeCanceled)
	if terminalErr != nil {
		t.Fatalf("cancellation terminal flush error: %v", terminalErr)
	}
	if got := f4LocalProviderOutputTokens(t, record); got != "20" {
		t.Fatalf("cancellation terminal local provider-output tokens=%q, want final 20", got)
	}
	if !f4HasCheckpointCapacityLossMarker(record) {
		t.Fatalf("cancellation terminal record missing sticky capture loss: %+v", record.EvidenceConflicts)
	}
}

// TestF4TerminalHandoffRetainsFinalLocalMeasurementAcrossDurableRestart is the
// durable counterpart to the in-memory handoff regressions. It drives the same
// real direct attemptSession.TerminalizeAttempt branch (no BillingLegFn) with a
// REAL file-backed observation journal and a money-free recording terminal
// port, then closes and reopens the journal file and reads the persisted
// observations back. The durable billing-store read-back and the
// marker-vs-control retail valuation are Enterprise (see the file header).
//
// The prior local head (output tokens = 10) is durably checkpointed, all 128
// ordinary checkpoint slots are saturated, and the final local measurement
// (output tokens = 20) cannot be queued. The sealed leg handed to the terminal
// port must carry the immutable final 20 (never the stale 10 republished as
// final) with the sticky checkpoint-capacity capture loss intact, and the
// observation journal must still show only the accepted 10 prefix: the final 20
// has no durable checkpoint owner, so the loss is genuine rather than a stale
// republish.
func TestF4TerminalHandoffRetainsFinalLocalMeasurementAcrossDurableRestart(t *testing.T) {
	// This durable proof drives the production terminal callback against real
	// file-backed SQLite. The 20s cleanupTimeout below only widens the OUTER
	// terminal cleanup budget; it does not relax the production inner checkpoint
	// flush budget (economicCheckpointFlushTimeout, a fixed 2s, applied under
	// context.WithoutCancel). The 128-slot flush must therefore fit that inner
	// budget, which is why the file-backed journal uses the WAL/synchronous=NORMAL
	// reopen DSN instead of paying a full fsync per commit. Production callers
	// keep every budget unchanged.
	ctx := context.Background()

	const storeID = "store-f4"
	dir := t.TempDir()
	journalPath := filepath.Join(dir, "f4-journal.sqlite")

	journal := r5c2bOpenFileStore(t, journalPath, storeID)

	session, boundary := f4LocalBoundaryAttempt(journalstore.NewObservationSink(journal.store))
	session.terminal.cleanupTimeout = 20 * time.Second
	session.submissionID = "submission-f4"
	persisted := &f4RecordingTerminal{}
	session.appendBillingLegStrict = persisted.append

	// Step 1: valid local head output tokens = 10, durably checkpointed.
	f4ObserveProviderOutput(boundary, 40)
	session.checkpointLocalBoundaryObservations(session.economicCheckpointNow())
	if err := session.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("initial durable local checkpoint flush: %v", err)
	}

	// Step 2: saturate every ordinary pending+deferred slot.
	f4SaturateOrdinaryCheckpointQueue(t, session)

	// Step 3: the actual final local measurement is output tokens = 20.
	f4ObserveProviderOutput(boundary, 40)

	record, terminalErr := f4Terminalize(t, session, IntentSuccess, sdkterminal.CommandNormalFinish, usageevidence.LegOutcomeWinner)
	// This test asserts durability across a simulated restart, so the flush must
	// actually complete. Exceeding the bounded terminal budget is classified by
	// ErrEconomicCheckpointFlushTimeout, but a timed-out flush drops the ordinary
	// observations this test then reopens and verifies, so it is a real failure
	// here rather than an acceptable outcome. Report the classification explicitly
	// so the cause is not mistaken for a generic store fault.
	if terminalErr != nil {
		if errors.Is(terminalErr, ErrEconomicCheckpointFlushTimeout) {
			t.Fatalf("terminal flush exceeded its bounded budget and dropped durable observations: %v", terminalErr)
		}
		t.Fatalf("terminal drain/flush unexpectedly failed: %v", terminalErr)
	}

	// The sealed leg handed to the terminal port must carry the immutable final
	// 20 with the sticky loss disposition.
	handed := persisted.snapshot()
	if len(handed) != 1 {
		t.Fatalf("terminal leg handoffs = %d, want exactly one", len(handed))
	}
	if got := f4LocalProviderOutputTokens(t, handed[0]); got != "20" {
		t.Fatalf("handed leg local provider-output tokens=%q, want final 20 (stale prior would be 10)", got)
	}
	if !f4HasCheckpointCapacityLossMarker(handed[0]) {
		t.Fatalf("handed leg lost the checkpoint-capacity capture loss disposition: %+v", handed[0].EvidenceConflicts)
	}

	// Reopen the durable journal from its file to model a process restart.
	journal.close(t)
	restartedJournal := r5c2bOpenFileStore(t, journalPath, storeID)
	defer restartedJournal.close(t)

	// The durable observation journal holds exactly the accepted checkpoint
	// prefix: the local 10 is durable while the final local 20 never received a
	// durable checkpoint owner.
	const ordinaryCheckpoints = maxPreTerminalEconomicCheckpointPending + maxPreTerminalEconomicCheckpointDeferred
	const localPrefixObservations = 3
	page, err := restartedJournal.store.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: record.BLegID, Limit: 500,
	})
	if err != nil {
		t.Fatalf("list durable observations after restart: %v", err)
	}
	if want := localPrefixObservations + ordinaryCheckpoints; len(page.Observations) != want {
		t.Fatalf("durable observations=%d, want local prefix %d + ordinary %d", len(page.Observations), localPrefixObservations, ordinaryCheckpoints)
	}
	durablePrior := false
	for _, observation := range page.Observations {
		if observation.Origin != metering.OriginLocal || observation.Boundary != metering.BoundaryBackendIngress {
			continue
		}
		for _, measure := range observation.Measures {
			if measure.Key.Direction != metering.DirectionOutput || measure.Key.Component != metering.ComponentTextToken || measure.Value == nil {
				continue
			}
			switch measure.Value.Coefficient {
			case "10":
				durablePrior = true
			case "20":
				t.Fatalf("final local 20 unexpectedly has a durable checkpoint owner: %+v", observation)
			}
		}
	}
	if !durablePrior {
		t.Fatal("durable journal lost the pre-terminal local provider-output 10 prefix observation")
	}
}

// TestF4TerminalHandoffRetainsFinalLocalMeasurementWhenDurableSinkFails is the
// continuing sink-failure durable variant: the file-backed observation journal
// becomes unavailable before terminalization, so the terminal checkpoint flush
// fails and neither the saturated ordinary queue nor the rejected final local
// measurement can become durable. The final local 20 and the sticky
// capture-loss disposition must still reach the money-free recording terminal
// port, so the same restart still observes the immutable final measurement and
// the fail-closed disposition despite the checkpoint outage. The durable
// billing-store read-back and the marker-vs-control retail valuation are
// Enterprise (see the file header).
//
// Keep this private, saturated durable fixture serial with package peers
// so race instrumentation competes less for its default terminal budget.
// It has no shared store or competing actor; all production budgets and
// durability assertions remain unchanged.
func TestF4TerminalHandoffRetainsFinalLocalMeasurementWhenDurableSinkFails(t *testing.T) {
	ctx := context.Background()

	const storeID = "store-f4"
	dir := t.TempDir()
	journalPath := filepath.Join(dir, "f4-journal-failing.sqlite")

	journal := r5c2bOpenFileStore(t, journalPath, storeID)

	realSink, ok := journalstore.NewObservationSink(journal.store).(metering.AtomicObservationSink)
	if !ok {
		t.Fatal("file-backed observation sink is not atomic")
	}
	sink := &r5c2bFlakySink{delegate: realSink, available: true}
	session, boundary := f4LocalBoundaryAttempt(sink)
	// Real file-backed durability uses the same bounded test budget as the
	// successful-restart fixture; production cleanup deadlines stay unchanged.
	session.terminal.cleanupTimeout = 20 * time.Second
	session.submissionID = "submission-f4"
	persisted := &f4RecordingTerminal{}
	session.appendBillingLegStrict = persisted.append

	f4ObserveProviderOutput(boundary, 40)
	session.checkpointLocalBoundaryObservations(session.economicCheckpointNow())
	if err := session.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("initial durable local checkpoint flush: %v", err)
	}

	f4SaturateOrdinaryCheckpointQueue(t, session)
	f4ObserveProviderOutput(boundary, 40)

	// The journal is unavailable for the terminal flush: neither the saturated
	// ordinary queue nor the rejected final local measurement is durable.
	sink.setAvailable(false)

	record, terminalErr := f4Terminalize(t, session, IntentSuccess, sdkterminal.CommandNormalFinish, usageevidence.LegOutcomeWinner)
	if terminalErr == nil || !strings.Contains(terminalErr.Error(), "r5c2b journal unavailable") {
		t.Fatalf("terminal flush error=%v, want r5c2b journal unavailable", terminalErr)
	}

	handed := persisted.snapshot()
	if len(handed) != 1 {
		t.Fatalf("terminal leg handoffs = %d, want exactly one", len(handed))
	}
	if got := f4LocalProviderOutputTokens(t, handed[0]); got != "20" {
		t.Fatalf("handed leg local provider-output tokens=%q, want final 20 despite sink failure", got)
	}
	if !f4HasCheckpointCapacityLossMarker(handed[0]) {
		t.Fatalf("handed leg lost the checkpoint-capacity capture loss disposition: %+v", handed[0].EvidenceConflicts)
	}

	// The unavailable journal holds only the initial local prefix: none of the
	// saturated ordinary observations nor the final local 20 could be persisted.
	journal.close(t)
	restartedJournal := r5c2bOpenFileStore(t, journalPath, storeID)
	defer restartedJournal.close(t)
	page, err := restartedJournal.store.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: record.BLegID, Limit: 500,
	})
	if err != nil {
		t.Fatalf("list durable observations after sink failure: %v", err)
	}
	if len(page.Observations) != 3 {
		t.Fatalf("durable observations=%d, want only the 3-observation local prefix", len(page.Observations))
	}
}
