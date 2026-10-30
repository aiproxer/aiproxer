package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// F6 durable restart proof. The existing F6 runtime tests prove the bounded
// invalid-draft loss marker survives the runtime drain and an in-memory
// billingLegRecord + Seal. They make no durable-write or restart claim: the
// record is never handed to a production store, so a process restart would
// find no evidence at all. These tests close that gap by driving the real
// terminal drain, persisting the immutable prefix and the sanitized marker into
// a real file-backed checkpoint journal, then closing and reopening the file
// and reading the records back. The durable billing-store leg read-back is
// Enterprise (5.2 preservation:
// TestEnterprisePreservationRuntimeProviderEvidenceDurableReadBack), so this
// file keeps the journal half plus the in-memory leg assertions.
//
// The F6 counterexample is unchanged: a draft admitted by Add (under the
// metadata byte cap) whose ProviderRequestID is MaxSchemaIDBytes+1. The drain
// must retain the valid prefix, surface one bounded provider-neutral
// invalid_draft marker, persist neither the raw over-bound identifier nor its
// charge, and keep the aggregate incomplete and the prefix amount unchanged.
// This slice asserts no monetary/valuation API is invoked: evidence is derived
// only by the runtime drain and pure aggregate reduction; it never builds a V2
// valuation or invents posting state.

// f6CaptureDrainStream forwards the real provider evidence buffer drain to the
// runtime while independently cloning every drained observation before any
// append or persistence. The clone is the test-owned expected observation set;
// it is never derived from what a store reads back.
type f6CaptureDrainStream struct {
	*r5cProviderStream
	captured []metering.Observation
}

func (s *f6CaptureDrainStream) DrainEconomicObservations() []metering.Observation {
	drained := s.r5cProviderStream.DrainEconomicObservations()
	for i := range drained {
		s.captured = append(s.captured, drained[i].Clone())
	}
	return drained
}

// f6ExpectedPrefix freezes the one accepted-prefix observation observed at drain
// time and fails rather than guess if the fixture cannot identify it uniquely.
func f6ExpectedPrefix(t *testing.T, captured []metering.Observation, prefixKey string) metering.Observation {
	t.Helper()
	var matches []metering.Observation
	for _, observation := range captured {
		if observation.SourceEventKey == prefixKey {
			matches = append(matches, observation.Clone())
		}
	}
	if len(matches) != 1 {
		t.Fatalf("fixture cannot identify exactly one accepted prefix %q from the real drain: found %d", prefixKey, len(matches))
	}
	if matches[0].Authority == metering.AuthorityUnavailableClaim {
		t.Fatalf("accepted prefix %q unexpectedly carries unavailable authority", prefixKey)
	}
	if matches[0].Fingerprint() == "" {
		t.Fatalf("accepted prefix %q observed at drain time does not canonicalize", prefixKey)
	}
	return matches[0]
}

// f6IsInvalidDraftMarker reports whether one observation is the provider-neutral
// unavailable invalid_draft loss marker.
func f6IsInvalidDraftMarker(observation metering.Observation) bool {
	if observation.Authority != metering.AuthorityUnavailableClaim {
		return false
	}
	for _, measure := range observation.Measures {
		if measure.Quality == metering.QualityUnavailable && measure.Reason == "invalid_draft" {
			return true
		}
	}
	return false
}

// f6AssertInvalidDraftMarkerSanitized proves one recovered marker carries no
// charge and none of the rejected raw draft's over-bound provider identifier.
func f6AssertInvalidDraftMarkerSanitized(t *testing.T, label string, marker metering.Observation, overBound string) {
	t.Helper()
	if len(marker.Charges) != 0 {
		t.Fatalf("%s: invalid_draft marker carried charges: %+v", label, marker.Charges)
	}
	if marker.Subject.ProviderRequestID != "" || marker.Correlation.ProviderRequestID != "" {
		t.Fatalf("%s: invalid_draft marker retained a provider request id: subject=%q correlation=%q", label, marker.Subject.ProviderRequestID, marker.Correlation.ProviderRequestID)
	}
	payload, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatalf("%s: invalid_draft marker did not canonicalize: %v", label, err)
	}
	if strings.Contains(string(payload), overBound) {
		t.Fatalf("%s: invalid_draft marker retained the raw over-bound provider identifier", label)
	}
}

// f6DurableAssertRecoveredEvidence compares the recovered accepted prefix to the
// drain-time expected observation by its full canonical fingerprint (identity,
// provenance, measures, charges and evidence) and requires exactly one
// sanitized invalid_draft marker with no charge and no raw provider identifier.
// Fingerprint covers the immutable canonical payload; DeepEqual on the raw
// struct would falsely flag the store's canonicalization.
func f6DurableAssertRecoveredEvidence(t *testing.T, label string, observations []metering.Observation, wantPrefix metering.Observation, prefixKey, invalidKey string) {
	t.Helper()
	overBound := strings.Repeat("r", metering.MaxSchemaIDBytes+1)
	wantFingerprint := wantPrefix.Fingerprint()
	prefixCount := 0
	markerCount := 0
	for _, observation := range observations {
		if observation.SourceEventKey == invalidKey {
			t.Fatalf("%s: invalid raw draft reached recovered evidence: %+v", label, observation)
		}
		if observation.Subject.ProviderRequestID == overBound || observation.Correlation.ProviderRequestID == overBound {
			t.Fatalf("%s: raw over-bound provider request id survived into recovered evidence: %+v", label, observation)
		}
		for _, charge := range observation.Charges {
			if charge.ChargeItemID == "charge:"+invalidKey {
				t.Fatalf("%s: invalid draft's charge reached recovered evidence: %+v", label, charge)
			}
		}
		if observation.SourceEventKey == prefixKey {
			prefixCount++
			if got := observation.Fingerprint(); got != wantFingerprint {
				t.Fatalf("%s: recovered accepted prefix is not the immutable drain-time payload: got fingerprint %s want %s", label, got, wantFingerprint)
			}
		}
		if f6IsInvalidDraftMarker(observation) {
			markerCount++
			f6AssertInvalidDraftMarkerSanitized(t, label, observation, overBound)
		}
	}
	if prefixCount != 1 {
		t.Fatalf("%s: expected exactly one recovered accepted prefix %q, found %d", label, prefixKey, prefixCount)
	}
	if markerCount != 1 {
		t.Fatalf("%s: expected exactly one recovered invalid_draft marker, found %d", label, markerCount)
	}
}

// f6DurableIdentity binds the provider evidence buffer to the exact
// store/call/B-leg scope of the terminal record that will be sealed into the
// durable billing store, so drained observations pass the record's
// submission/lineage checks. SubmissionID is carried by the trusted call-leg
// record (the buffer identity contract has no submission field); the buffer's
// observations carry no competing submission claim, so the record-level
// authority is the only submission scope in play.
func f6DurableIdentity(storeID string, callID usageevidence.BillingCallID, aLegID, bLegID, attemptID string, seq uint64, now time.Time) coremetering.ObservationIdentity {
	return coremetering.ObservationIdentity{
		StoreID: storeID, RequestID: "req-" + bLegID, CallID: callID.String(), BillingCallID: callID.String(),
		ALegID: aLegID, BLegID: bLegID, AttemptID: attemptID, AttemptSeq: seq,
		ObservedAt: now, ReceivedAt: now,
	}
}

// f6DurableAssertLeg applies the F6 in-memory invariants to the sealed leg and
// adds the drain-time immutability guarantee that the recovered accepted prefix
// is the exact clone captured before any drain or persistence, and that exactly
// one sanitized invalid_draft marker survived with no raw over-bound identifier
// and no fabricated V1 cost projection.
func f6DurableAssertLeg(t *testing.T, wantPrefix metering.Observation, durable usageevidence.CallLegUsageRecord, prefixKey, invalidKey, wantQuantity, wantMoney string) {
	t.Helper()
	f6AssertInvalidDraftRecord(t, durable.Observations, prefixKey, invalidKey, wantQuantity, wantMoney)
	f6DurableAssertRecoveredEvidence(t, "sealed leg", durable.Observations, wantPrefix, prefixKey, invalidKey)
	// No monetary/valuation API was invoked to build this leg: the V1 cost
	// compatibility projection must stay absent rather than fabricating money,
	// and no posting state may be invented for the rejected draft.
	if durable.Evidence.Cost.Present {
		t.Fatalf("durable evidence fabricated a V1 cost projection: %+v", durable.Evidence.Cost)
	}
}

// f6DurableAssertJournal proves the checkpoint journal itself, after restart,
// still holds the immutable accepted prefix and the sanitized loss marker while
// excluding the invalid raw draft and its charge. The prefix's reduced
// commercial value must be unchanged and the snapshot must remain incomplete.
func f6DurableAssertJournal(ctx context.Context, t *testing.T, store *journalstore.DurableStore, wantPrefix metering.Observation, storeID, bLegID, prefixKey, invalidKey, wantQuantity, wantMoney string) {
	t.Helper()
	page, err := store.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: bLegID, Limit: 500,
	})
	if err != nil {
		t.Fatalf("list durable journal after restart: %v", err)
	}
	f6DurableAssertRecoveredEvidence(t, "durable journal", page.Observations, wantPrefix, prefixKey, invalidKey)
	prefixVisible := false
	markerVisible := false
	overBound := strings.Repeat("r", metering.MaxSchemaIDBytes+1)
	for _, observation := range page.Observations {
		if observation.SourceEventKey == invalidKey {
			t.Fatalf("invalid raw draft reached the durable journal: %+v", observation)
		}
		if observation.Subject.ProviderRequestID == overBound || observation.Correlation.ProviderRequestID == overBound {
			t.Fatalf("raw over-bound provider request id reached the durable journal: %+v", observation)
		}
		for _, charge := range observation.Charges {
			if charge.ChargeItemID == "charge:"+invalidKey {
				t.Fatalf("invalid draft's charge reached the durable journal: %+v", charge)
			}
		}
		if observation.SourceEventKey == prefixKey {
			prefixVisible = true
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
		t.Fatalf("durable journal lost the accepted prefix: %+v", page.Observations)
	}
	if !markerVisible {
		t.Fatalf("durable journal lost the sanitized invalid-draft marker: %+v", page.Observations)
	}
	snapshot, err := aggregate.ApplyObservations(page.Observations)
	if err != nil {
		t.Fatalf("reduce durable journal: %v", err)
	}
	if snapshot.Complete {
		t.Fatal("durable journal reduced complete despite the invalid-draft marker")
	}
	quantity, money := r5cReducedCommercialState(t, page.Observations, r5bMediaKey())
	if quantity != wantQuantity || money != wantMoney {
		t.Fatalf("durable journal fabricated commercial value: quantity=%q money=%q, want prefix-only %s/%s", quantity, money, wantQuantity, wantMoney)
	}
}

// TestProviderEvidenceF6DurableRestartTerminalDrain: the valid prefix and the
// invalid draft are drained together at terminal time and the sealed leg keeps
// the prefix and the sanitized marker while the checkpoint journal is persisted.
// After a close/reopen restart the durable journal still carries the prefix and
// the sanitized marker, the invalid draft and its charge are absent, and the
// reduction stays incomplete with the unchanged prefix amount.
func TestProviderEvidenceF6DurableRestartTerminalDrain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const (
		storeID      = "store-f6-durable-terminal"
		aLegID       = "a-f6-durable-terminal"
		bLegID       = "b-f6-durable-terminal"
		submissionID = "submission-f6-durable-terminal"
		prefixKey    = "provider.f6.durable.terminal.prefix"
		invalidKey   = "provider.f6.durable.terminal.invalid"
	)
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_930_000, 0).UTC()
	dir := t.TempDir()
	journalPath := filepath.Join(dir, "f6-durable-terminal-journal.sqlite")

	journal := r5c2bOpenFileStore(t, journalPath, storeID)

	buffer := coremetering.NewProviderEvidenceBuffer()
	buffer.BindEconomicEvidence(f6DurableIdentity(storeID, callID, aLegID, bLegID, bLegID, 1, now))
	buffer.Add(r5bProviderMediaMoneyDraft(prefixKey, 1))
	buffer.Add(f6RuntimeInvalidDraft(invalidKey, 3))

	stream := &f6CaptureDrainStream{r5cProviderStream: &r5cProviderStream{buffer: buffer}}
	attempt := newAttemptSession(attemptSessionInput{observationSink: journalstore.NewObservationSink(journal.store)})
	attempt.drainStreamUsageEvidence(stream)
	expectedPrefix := f6ExpectedPrefix(t, stream.captured, prefixKey)
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("durable checkpoint flush: %v", err)
	}
	economic, conflicts := attempt.economicEvidenceDrain()
	record := f6TerminalRecord(t, callID, storeID, bLegID, submissionID, aLegID, now, economic, conflicts)
	f6DurableAssertLeg(t, expectedPrefix, record, prefixKey, invalidKey, "1", "100")

	// Restart: close and reopen the production checkpoint journal from its file.
	journal.close(t)
	restartedJournal := r5c2bOpenFileStore(t, journalPath, storeID)
	defer restartedJournal.close(t)

	f6DurableAssertJournal(ctx, t, restartedJournal.store, expectedPrefix, storeID, bLegID, prefixKey, invalidKey, "1", "100")
}

// TestProviderEvidenceF6DurableRestartLiveDrain: the valid prefix is drained
// live, then the invalid draft arrives in a later live drain. The valid prefix
// live observation and the sanitized marker must survive the journal restart and
// reduce to the unchanged prefix-only commercial value while staying
// incomplete.
func TestProviderEvidenceF6DurableRestartLiveDrain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const (
		storeID      = "store-f6-durable-live"
		aLegID       = "a-f6-durable-live"
		bLegID       = "b-f6-durable-live"
		submissionID = "submission-f6-durable-live"
		prefixKey    = "provider.f6.durable.live.prefix"
		invalidKey   = "provider.f6.durable.live.invalid"
	)
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_940_000, 0).UTC()
	dir := t.TempDir()
	journalPath := filepath.Join(dir, "f6-durable-live-journal.sqlite")

	journal := r5c2bOpenFileStore(t, journalPath, storeID)

	buffer := coremetering.NewProviderEvidenceBuffer()
	buffer.BindEconomicEvidence(f6DurableIdentity(storeID, callID, aLegID, bLegID, bLegID, 1, now))
	stream := &f6CaptureDrainStream{r5cProviderStream: &r5cProviderStream{buffer: buffer}}
	attempt := newAttemptSession(attemptSessionInput{observationSink: journalstore.NewObservationSink(journal.store)})

	buffer.Add(r5bProviderMediaMoneyDraft(prefixKey, 2))
	attempt.drainStreamUsageEvidence(stream)

	buffer.Add(f6RuntimeInvalidDraft(invalidKey, 5))
	attempt.drainStreamUsageEvidence(stream)
	expectedPrefix := f6ExpectedPrefix(t, stream.captured, prefixKey)

	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("durable checkpoint flush: %v", err)
	}
	economic, conflicts := attempt.economicEvidenceDrain()
	record := f6TerminalRecord(t, callID, storeID, bLegID, submissionID, aLegID, now, economic, conflicts)
	f6DurableAssertLeg(t, expectedPrefix, record, prefixKey, invalidKey, "2", "200")

	// Restart: close and reopen the production checkpoint journal from its file.
	journal.close(t)
	restartedJournal := r5c2bOpenFileStore(t, journalPath, storeID)
	defer restartedJournal.close(t)

	f6DurableAssertJournal(ctx, t, restartedJournal.store, expectedPrefix, storeID, bLegID, prefixKey, invalidKey, "2", "200")
}
