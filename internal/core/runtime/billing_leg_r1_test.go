package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestR1TerminalSubmissionLinkagePreservesProviderSupersessionGraph(t *testing.T) {
	first, second := r1ProviderRevisions(t)

	record := billingLegRecord(billingLegDraft{
		callID: usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef"), submissionID: "submission-r1",
		aLegID: "a-leg-r1", storeID: "store-r1", bLegID: "b-leg-r1", seq: 1,
		primary:   routing.Primary{Backend: "backend-r1", Model: "model-r1"},
		startedAt: time.Unix(100, 0).UTC(), finishedAt: time.Unix(101, 0).UTC(),
		command: sdkterminal.CommandNormalFinish, surfaced: usageevidence.SurfacedYes,
		economicObservations: []execbackend.EconomicEvidence{
			{Observation: first}, {Observation: second},
		},
	})
	if len(record.Observations) != 2 {
		t.Fatalf("terminal provider observations = %d, want two revisions", len(record.Observations))
	}
	if record.SubmissionID != "submission-r1" {
		t.Fatalf("terminal record submission attribution = %q, want trusted submission-r1", record.SubmissionID)
	}
	if got := record.Observations[0].Subject.SubmissionID; got != "" {
		t.Fatalf("provider observation was rewritten at terminal with submission %q", got)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatalf("terminal provider record failed sealing: %v", err)
	}
	if sealed.SubmissionID != "submission-r1" || sealed.Fingerprint == "" {
		t.Fatalf("sealed terminal provider record = %+v, want trusted linkage and fingerprint", sealed)
	}
	if err := sdkmetering.ValidateSupersessionGraph(record.Observations); err != nil {
		t.Fatalf("terminal provider supersession graph invalid: %v", err)
	}
	if _, err := aggregate.ApplyObservations(record.Observations); err != nil {
		t.Fatalf("terminal provider observations failed reduction: %v", err)
	}
}

func TestR1MixedAcceptedAndRejectedRevisionCannotRateAsComplete(t *testing.T) {
	first, second := r1ProviderRevisions(t)
	spoofed := second.Clone()
	spoofed.Subject.SubmissionID = "adapter-spoofed"
	spoofed.Correlation.SubmissionID = "adapter-spoofed"
	before := spoofed.Fingerprint()

	record := billingLegRecord(billingLegDraft{
		callID: usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef"), submissionID: "submission-r1",
		aLegID: "a-leg-r1", storeID: "store-r1", bLegID: "b-leg-r1", seq: 1,
		primary:   routing.Primary{Backend: "backend-r1", Model: "model-r1"},
		startedAt: time.Unix(100, 0).UTC(), finishedAt: time.Unix(101, 0).UTC(),
		command: sdkterminal.CommandNormalFinish, surfaced: usageevidence.SurfacedYes,
		economicObservations: []execbackend.EconomicEvidence{{Observation: first}, {Observation: spoofed}},
	})
	if len(record.Observations) != 1 || record.Observations[0].Revision != first.Revision {
		t.Fatalf("mixed terminal observations = %+v, want only accepted revision 1", record.Observations)
	}
	if len(record.EvidenceConflicts) == 0 {
		t.Fatal("rejected revision disappeared without a bounded conflict")
	}
	if got := record.EvidenceConflicts[0].IncomingCoverage; got != usageevidence.EconomicEvidenceCoverageUnsupported || record.EvidenceConflicts[0].IncomingCoverageReason != "trusted_submission_linkage_mismatch" {
		t.Fatalf("rejected revision conflict coverage = %q/%q, want unsupported/trusted_submission_linkage_mismatch", got, record.EvidenceConflicts[0].IncomingCoverageReason)
	}
	if got := spoofed.Fingerprint(); got != before {
		t.Fatalf("rejected adapter observation fingerprint changed: %q -> %q", before, got)
	}
}

func TestR1PreTerminalCheckpointRestartKeepsSubmissionAsSeparateLinkage(t *testing.T) {
	first, second := r1ProviderRevisions(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "r1-restart.db")
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open first sqlite process: %v", err)
	}
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatalf("first sqlite bun DB: %v", err)
	}
	store, err := journalstore.NewDurableStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "store-r1"})
	if err != nil {
		_ = sqlDB.Close()
		t.Fatalf("open first durable store: %v", err)
	}
	sink := journalstore.NewObservationSink(store)
	attempt := &attemptSession{observationSink: sink}
	for _, observation := range []sdkmetering.Observation{first, second} {
		attempt.rememberEconomicObservationOnce(observation)
		if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
			t.Fatalf("pre-terminal checkpoint flush: %v", err)
		}
	}

	// Snapshot the durable originals and their immutable references before the
	// process boundary. From here the pre-close slice is never replayed into the
	// restarted store: only these captured expectations, including the original
	// Ref payload hashes and supersession edge, are compared against recovery.
	originals, err := store.ListObservations(ctx, journalstore.ObservationQuery{StreamID: first.StreamID, Limit: 10})
	if err != nil {
		_ = store.Close()
		_ = sqlDB.Close()
		t.Fatalf("list pre-terminal durable observations: %v", err)
	}
	if len(originals.Observations) != 2 {
		_ = store.Close()
		_ = sqlDB.Close()
		t.Fatalf("pre-terminal durable observations = %d, want two revisions", len(originals.Observations))
	}
	originalGraph := r1ObservationGraph(t, originals.Observations)
	if err := sdkmetering.ValidateSupersessionGraph(originals.Observations); err != nil {
		_ = store.Close()
		_ = sqlDB.Close()
		t.Fatalf("pre-terminal provider supersession graph invalid: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first durable store: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close first sqlite process: %v", err)
	}

	// Reopen the file through fresh sql/bun/store handles and read the persisted
	// records BEFORE any replay. Recovery must stand on the durable rows alone;
	// the source envelope, including revision-2's predecessor Ref, is the only
	// lineage carried into terminal assembly after this process boundary.
	reopenedSQL, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open restarted sqlite process: %v", err)
	}
	reopenedBun, err := db.NewBunDB(reopenedSQL, db.DialectSQLite)
	if err != nil {
		_ = reopenedSQL.Close()
		t.Fatalf("restarted sqlite bun DB: %v", err)
	}
	reopened, err := journalstore.OpenStore(ctx, reopenedBun, journalstore.DurableConfig{StoreID: "store-r1"})
	if err != nil {
		_ = reopenedSQL.Close()
		t.Fatalf("open restarted durable store: %v", err)
	}
	restartedPage, err := reopened.ListObservations(ctx, journalstore.ObservationQuery{StreamID: first.StreamID, Limit: 10})
	if err != nil {
		_ = reopened.Close()
		_ = reopenedSQL.Close()
		t.Fatalf("list restarted durable observations before replay: %v", err)
	}
	restartedDurable := restartedPage.Observations
	if len(restartedDurable) != 2 {
		_ = reopened.Close()
		_ = reopenedSQL.Close()
		t.Fatalf("restarted durable observations = %d, want exactly two recovered revisions", len(restartedDurable))
	}
	r1AssertRecoveredOriginals(t, originalGraph, restartedDurable)

	// Hydrate a fresh attempt exclusively from the post-reopen query. No
	// pre-close in-memory observation participates in terminal assembly.
	restartedSink := journalstore.NewObservationSink(reopened)
	restarted := &attemptSession{observationSink: restartedSink}
	for _, observation := range restartedDurable {
		restarted.rememberEconomicObservationOnce(observation)
	}
	recoveredEvidence, recoveredConflicts := restarted.economicEvidenceDrain()
	if len(recoveredConflicts) != 0 {
		t.Fatalf("recovered observations produced %d terminal conflicts, want none", len(recoveredConflicts))
	}
	if len(recoveredEvidence) != 2 {
		t.Fatalf("recovered terminal evidence = %d, want two revisions", len(recoveredEvidence))
	}

	for _, submissionID := range []string{"", "submission-r1"} {
		record := billingLegRecord(billingLegDraft{
			callID: usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef"), submissionID: submissionID,
			aLegID: "a-leg-r1", storeID: "store-r1", bLegID: "b-leg-r1", seq: 1,
			primary:   routing.Primary{Backend: "backend-r1", Model: "model-r1"},
			startedAt: time.Unix(100, 0).UTC(), finishedAt: time.Unix(101, 0).UTC(),
			command: sdkterminal.CommandNormalFinish, surfaced: usageevidence.SurfacedYes,
			economicObservations: recoveredEvidence,
		})
		if record.SubmissionID != submissionID {
			t.Fatalf("record submission = %q, want %q", record.SubmissionID, submissionID)
		}
		if len(record.Observations) != 2 {
			t.Fatalf("submission %q terminal observations = %d, want two recovered revisions", submissionID, len(record.Observations))
		}
		for _, observation := range record.Observations {
			if observation.Subject.SubmissionID != "" {
				t.Fatalf("recovered provider observation was rewritten with submission %q", observation.Subject.SubmissionID)
			}
		}
		if err := sdkmetering.ValidateSupersessionGraph(record.Observations); err != nil {
			t.Fatalf("submission %q invalid after restart: %v", submissionID, err)
		}
	}

	// Terminal reduction runs over the recovered revisions with the trusted
	// record-level submission, never a provider-carried claim. Provider rating
	// over the recovered journal is Enterprise (5.2 preservation:
	// TestEnterprisePreservationRuntimeRecoveredProviderRatingAcrossJournalRestart).
	rated := billingLegRecord(billingLegDraft{
		callID: usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef"), submissionID: "submission-r1",
		aLegID: "a-leg-r1", storeID: "store-r1", bLegID: "b-leg-r1", seq: 1,
		primary:   routing.Primary{Backend: "backend-r1", Model: "model-r1"},
		startedAt: time.Unix(100, 0).UTC(), finishedAt: time.Unix(101, 0).UTC(),
		command: sdkterminal.CommandNormalFinish, surfaced: usageevidence.SurfacedYes,
		economicObservations: recoveredEvidence,
	})
	if rated.SubmissionID != "submission-r1" {
		t.Fatalf("recovered terminal record submission = %q, want trusted submission-r1", rated.SubmissionID)
	}
	if _, err := aggregate.ApplyObservations(rated.Observations); err != nil {
		t.Fatalf("recovered revisions failed reduction: %v", err)
	}

	spoofed := restartedDurable[1].Clone()
	spoofed.Subject.SubmissionID = "adapter-spoofed"
	spoofed.Correlation.SubmissionID = "adapter-spoofed"
	before := spoofed.Fingerprint()
	rejected := billingLegRecord(billingLegDraft{
		callID: usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef"), submissionID: "submission-r1",
		aLegID: "a-leg-r1", storeID: "store-r1", bLegID: "b-leg-r1", seq: 1,
		primary:   routing.Primary{Backend: "backend-r1", Model: "model-r1"},
		startedAt: time.Unix(100, 0).UTC(), finishedAt: time.Unix(101, 0).UTC(),
		command:              sdkterminal.CommandNormalFinish,
		economicObservations: []execbackend.EconomicEvidence{{Observation: spoofed}},
	})
	if len(rejected.Observations) != 0 {
		t.Fatalf("untrusted adapter-carried observation was retained: %+v", rejected.Observations)
	}
	if len(rejected.EvidenceConflicts) != 1 {
		t.Fatalf("untrusted adapter-carried observation conflicts = %d, want one quarantine diagnostic", len(rejected.EvidenceConflicts))
	}
	if got := spoofed.Fingerprint(); got != before {
		t.Fatalf("adapter observation fingerprint changed during rejection: %q -> %q", before, got)
	}

	trustedCarried := restartedDurable[0].Clone()
	trustedCarried.Subject.SubmissionID = "submission-r1"
	trustedCarried.Correlation.SubmissionID = "submission-r1"
	trustedBefore := trustedCarried.Fingerprint()
	trusted := billingLegRecord(billingLegDraft{
		callID: usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef"), submissionID: "submission-r1",
		aLegID: "a-leg-r1", storeID: "store-r1", bLegID: "b-leg-r1", seq: 1,
		primary:   routing.Primary{Backend: "backend-r1", Model: "model-r1"},
		startedAt: time.Unix(100, 0).UTC(), finishedAt: time.Unix(101, 0).UTC(),
		command:              sdkterminal.CommandNormalFinish,
		economicObservations: []execbackend.EconomicEvidence{{Observation: trustedCarried}},
	})
	if len(trusted.Observations) != 1 || trusted.Observations[0].Subject.SubmissionID != "submission-r1" {
		t.Fatalf("matching adapter submission claim was not retained: %+v", trusted.Observations)
	}
	if got := trustedCarried.Fingerprint(); got != trustedBefore {
		t.Fatalf("matching adapter observation fingerprint changed: %q -> %q", trustedBefore, got)
	}

	// Idempotent replay is exercised only after the recovery assertions: the
	// same recovered revisions are re-delivered and must not duplicate rows.
	for _, observation := range restartedDurable {
		restarted.rememberEconomicObservationOnce(observation)
	}
	if err := restarted.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("idempotent replay flush: %v", err)
	}
	replayPage, err := reopened.ListObservations(ctx, journalstore.ObservationQuery{StreamID: first.StreamID, Limit: 10})
	if err != nil {
		_ = reopened.Close()
		_ = reopenedSQL.Close()
		t.Fatalf("list after idempotent replay: %v", err)
	}
	if len(replayPage.Observations) != 2 {
		_ = reopened.Close()
		_ = reopenedSQL.Close()
		t.Fatalf("idempotent replay produced %d durable observations, want 2", len(replayPage.Observations))
	}
	r1AssertRecoveredOriginals(t, originalGraph, replayPage.Observations)

	if err := reopened.Close(); err != nil {
		t.Fatalf("close restarted durable store: %v", err)
	}
	if err := reopenedSQL.Close(); err != nil {
		t.Fatalf("close restarted sqlite process: %v", err)
	}
}

// r1OriginalObservation captures one persisted revision and its immutable,
// replay-stable reference so recovery can be compared against the exact source
// state that crossed the process boundary.
type r1OriginalObservation struct {
	observation sdkmetering.Observation
	ref         sdkmetering.ObservationRef
	fingerprint string
}

func r1ObservationKey(observation sdkmetering.Observation) string {
	return fmt.Sprintf("%s@%d", observation.ID, observation.Revision)
}

func r1ObservationGraph(t *testing.T, observations []sdkmetering.Observation) map[string]r1OriginalObservation {
	t.Helper()
	graph := make(map[string]r1OriginalObservation, len(observations))
	for _, observation := range observations {
		ref, err := observation.Ref("store-r1")
		if err != nil {
			t.Fatalf("original observation %q ref: %v", r1ObservationKey(observation), err)
		}
		graph[r1ObservationKey(observation)] = r1OriginalObservation{
			observation: observation,
			ref:         ref,
			fingerprint: observation.Fingerprint(),
		}
	}
	return graph
}

// r1AssertRecoveredOriginals verifies that the records read back after restart
// are byte-for-byte the original revisions: same count, identity, payload
// fingerprint, immutable Ref hash, and supersession edge to the original
// predecessor hash. A missing or regenerated observation fails here.
func r1AssertRecoveredOriginals(t *testing.T, original map[string]r1OriginalObservation, recovered []sdkmetering.Observation) {
	t.Helper()
	if len(recovered) != len(original) || len(recovered) != 2 {
		t.Fatalf("recovered observations = %d, want %d persisted originals", len(recovered), len(original))
	}
	if recovered[0].Revision == recovered[1].Revision {
		t.Fatalf("recovered source revisions are not distinct: %d/%d", recovered[0].Revision, recovered[1].Revision)
	}
	refsByRevision := make(map[uint64]sdkmetering.ObservationRef, len(recovered))
	for _, observation := range recovered {
		key := r1ObservationKey(observation)
		expected, ok := original[key]
		if !ok {
			t.Fatalf("recovered observation %q was not one of the persisted originals", key)
		}
		if got := observation.Fingerprint(); got != expected.fingerprint {
			t.Fatalf("recovered observation %q fingerprint = %q, want original %q", key, got, expected.fingerprint)
		}
		recoveredRef, err := observation.Ref("store-r1")
		if err != nil {
			t.Fatalf("recovered observation %q ref: %v", key, err)
		}
		if !recoveredRef.Equal(expected.ref) {
			t.Fatalf("recovered observation %q ref = %+v, want original %+v", key, recoveredRef, expected.ref)
		}
		refsByRevision[observation.Revision] = expected.ref
	}
	var predecessor, successor sdkmetering.Observation
	for _, observation := range recovered {
		if len(observation.Supersedes) == 0 {
			predecessor = observation
		} else {
			successor = observation
		}
	}
	if predecessor.ID == "" || successor.ID == "" {
		t.Fatalf("recovered observations do not form a predecessor/successor revision pair: %+v", recovered)
	}
	if len(successor.Supersedes) != 1 {
		t.Fatalf("recovered successor supersedes = %d refs, want exactly one predecessor", len(successor.Supersedes))
	}
	if expected := refsByRevision[predecessor.Revision]; !successor.Supersedes[0].Equal(expected) {
		t.Fatalf("recovered successor predecessor ref = %+v, want original %+v", successor.Supersedes[0], expected)
	}
}

func r1ProviderRevisions(t *testing.T) (sdkmetering.Observation, sdkmetering.Observation) {
	t.Helper()
	provider := coremetering.NewProviderEvidenceBuffer()
	provider.BindEconomicEvidence(coremetering.ObservationIdentity{
		StoreID: "store-r1", RequestID: "request-r1", CallID: "bc_0123456789abcdef0123456789abcdef", BillingCallID: "bc_0123456789abcdef0123456789abcdef",
		ALegID: "a-leg-r1", BLegID: "b-leg-r1", AttemptID: "b-leg-r1", AttemptSeq: 1,
		ObservedAt: time.Unix(100, 0).UTC(), ReceivedAt: time.Unix(100, 0).UTC(),
	})
	provider.AddUsageEvent(r1ProviderUsageEvent(3, 2), "provider.r1.v2")
	first := provider.DrainEconomicObservations()
	if len(first) != 1 {
		t.Fatalf("first provider drain = %d observations, want one", len(first))
	}
	provider.AddUsageEvent(r1ProviderUsageEvent(4, 2), "provider.r1.v2")
	second := provider.DrainEconomicObservations()
	if len(second) != 1 {
		t.Fatalf("second provider drain = %d observations, want one", len(second))
	}
	return first[0], second[0]
}

func r1ProviderUsageEvent(input, output int) aipapi.Event {
	return aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: input, OutputTokens: output,
		CostNanoUnits: int64(input + output), Currency: "USD", CostPresent: true,
		UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
		Accounting: aipapi.UsageAccountingMetadata{
			Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
			DedupeKey: "provider.r1.v2:stream", ProviderAccountKey: "acct-r1", ProviderRequestID: "req-r1",
		},
	}
}
