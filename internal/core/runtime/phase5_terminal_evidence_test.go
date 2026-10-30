package runtime

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	_ "modernc.org/sqlite"
)

func phase5EvidenceEvent(key string, source aipapi.UsageSource, authority aipapi.UsageAuthority, input, output, cost int64) aipapi.Event {
	return aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: int(input), OutputTokens: int(output),
		CostNanoUnits: cost, Currency: "USD", CostPresent: true,
		UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
		Accounting:    aipapi.UsageAccountingMetadata{Source: source, Authority: authority, DedupeKey: key},
	}
}

func phase5EvidenceDraft(t *testing.T, callID usageevidence.BillingCallID) billingLegDraft {
	t.Helper()
	return billingLegDraft{
		callID: callID, aLegID: "a-phase5", storeID: "phase5-store", bLegID: "b-phase5", seq: 1,
		primary:   routing.Primary{Backend: "backend", Model: "model"},
		startedAt: time.Unix(100, 0).UTC(), finishedAt: time.Unix(101, 0).UTC(),
		command: sdkterminal.CommandNormalFinish,
	}
}

func TestPhase5TrustedStoreIDScopesV2ObservationToJournalStore(t *testing.T) {
	t.Parallel()
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	executor := &Executor{BillingRuntime: BillingRuntime{BillingIdentity: BillingIdentity{
		StoreID: func(context.Context) string { return " store-a " },
	}}}
	prep := &preparedRequest{}
	if err := executor.stampBillingStoreID(context.Background(), prep); err != nil {
		t.Fatal(err)
	}
	request := prep.terminalFacts()
	draft := phase5EvidenceDraft(t, callID)
	draft.storeID = request.storeID
	draft.stream = phase5EvidenceEvent("trusted-store-source", aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative, 2, 1, 3)
	record := billingLegRecord(draft)
	if len(record.Observations) != 1 {
		t.Fatalf("trusted-store observations = %d, want one", len(record.Observations))
	}
	observation := record.Observations[0]
	if observation.Subject.StoreID != "store-a" || observation.Correlation.StoreID != "store-a" {
		t.Fatalf("observation store lineage = subject=%q correlation=%q, want store-a", observation.Subject.StoreID, observation.Correlation.StoreID)
	}

	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	storeA, err := journalstore.NewDurableStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "store-a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.Close() })
	storeB, err := journalstore.OpenStore(ctx, storeA.DB(), journalstore.DurableConfig{StoreID: "store-b"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeB.Close() })
	if err := storeA.AppendObservation(ctx, observation); err != nil {
		t.Fatalf("store-a rejected trusted observation: %v", err)
	}
	if err := storeB.AppendObservation(ctx, observation); !errors.Is(err, journalstore.ErrQueryOutOfScope) {
		t.Fatalf("store-b append = %v, want ErrQueryOutOfScope", err)
	}
}

func TestPhase5MissingTrustedStoreIDKeepsV1Only(t *testing.T) {
	t.Parallel()
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	draft := phase5EvidenceDraft(t, callID)
	draft.storeID = ""
	draft.stream = phase5EvidenceEvent("no-trusted-store", aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative, 1, 1, 2)
	record := billingLegRecord(draft)
	if record.HasV2Evidence() || len(record.Observations) != 0 {
		t.Fatalf("missing trusted store emitted V2 evidence: version=%d observations=%#v", record.EvidenceVersion, record.Observations)
	}
	if !record.Evidence.Cost.Present || record.Evidence.Cost.NanoUnits != 2 {
		t.Fatalf("missing trusted store lost V1 compatibility evidence: %+v", record.Evidence)
	}
}

func TestPhase5TerminalEvidenceRetainsStreamAndFinalizerSources(t *testing.T) {
	t.Parallel()
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	draft := phase5EvidenceDraft(t, callID)
	draft.finalize = phase5EvidenceEvent("finalizer-source", aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative, 10, 2, 7)
	draft.stream = phase5EvidenceEvent("stream-source", aipapi.UsageSourceLocalEstimator, aipapi.UsageAuthorityEstimated, 4, 1, 42)
	record := billingLegRecord(draft)
	if !record.HasV2Evidence() || len(record.Observations) != 2 {
		t.Fatalf("terminal evidence = version %d observations=%d, want two V2 source observations", record.EvidenceVersion, len(record.Observations))
	}
	seen := make(map[string]bool, len(record.Observations))
	for _, observation := range record.Observations {
		if observation.Subject.Kind != "b_leg" || observation.Subject.BLegID != "b-phase5" {
			t.Fatalf("observation subject = %+v, want concrete B-leg root", observation.Subject)
		}
		seen[observation.SourceEventKey] = true
	}
	if !seen["finalizer-source"] || !seen["stream-source"] {
		t.Fatalf("source observations = %v, want finalizer and stream", seen)
	}
	// The scalar field is only a labelled V1 projection. It may select the
	// finalizer, but it cannot erase the stream observation above.
	if !record.Evidence.Cost.Present || record.Evidence.Cost.NanoUnits != 7 {
		t.Fatalf("V1 projection = %+v, want finalizer cost 7", record.Evidence)
	}
}

func TestPhase5TerminalEvidenceReplayAndConflictAreVisible(t *testing.T) {
	t.Parallel()
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	attempt := &attemptSession{}
	first := phase5EvidenceEvent("sideband-source", aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative, 1, 2, 3)
	changed := first
	changed.OutputTokens = 9
	if !attempt.rememberUsageEvidenceOnceAs(first, billingEvidenceRoleSideband) {
		t.Fatal("first sideband evidence was not captured")
	}
	if attempt.rememberUsageEvidenceOnceAs(first, billingEvidenceRoleSideband) {
		t.Fatal("identical sideband replay should be deduplicated")
	}
	if attempt.rememberUsageEvidenceOnceAs(changed, billingEvidenceRoleSideband) {
		t.Fatal("changed source revision should be retained as a conflict, not a second observation")
	}
	events, conflicts := attempt.billingEvidenceDrain()
	if len(events) != 1 || len(conflicts) != 1 {
		t.Fatalf("drained evidence events=%d conflicts=%d, want one and one", len(events), len(conflicts))
	}
	record := billingLegRecord(func() billingLegDraft {
		draft := phase5EvidenceDraft(t, callID)
		draft.evidenceEvents = events
		draft.evidenceConflicts = conflicts
		return draft
	}())
	if len(record.Observations) != 1 || len(record.EvidenceConflicts) != 1 {
		t.Fatalf("record evidence observations=%d conflicts=%d, want one and one", len(record.Observations), len(record.EvidenceConflicts))
	}
	if record.EvidenceConflicts[0].Identity == "" || record.EvidenceConflicts[0].ExistingHash == record.EvidenceConflicts[0].IncomingHash {
		t.Fatalf("conflict = %+v, want changed payload hashes", record.EvidenceConflicts[0])
	}
}

func TestPhase5AttemptEvidenceDrainIsIndependentOfLaterBLeg(t *testing.T) {
	t.Parallel()
	attempt := &attemptSession{}
	if !attempt.rememberUsageEvidenceOnceAs(phase5EvidenceEvent("old-b-leg", aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative, 2, 0, 1), billingEvidenceRoleSideband) {
		t.Fatal("old B-leg evidence was not captured")
	}
	first, _ := attempt.billingEvidenceDrain()
	second, _ := attempt.billingEvidenceDrain()
	if len(first) != 1 || len(second) != 0 {
		t.Fatalf("drain lengths = %d/%d, want 1/0", len(first), len(second))
	}
	// A second terminal callback has no stale accumulator to attach to a new
	// B-leg; only a newly captured event can be attributed to that owner.
	if got := attempt.billingEvidenceSnapshot(); len(got) != 0 {
		t.Fatalf("stale evidence leaked to replacement B-leg: %#v", got)
	}
}

func TestPhase5DuplicateTerminalExitDrainsLateEvidence(t *testing.T) {
	t.Parallel()
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	attempt := &attemptSession{
		terminal:      newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:          b2bua.BLegRecord{ALegID: "a-phase5", BLegID: "b-phase5", Seq: 1},
		cand:          routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
		billingCallID: callID,
		now:           func() time.Time { return time.Unix(101, 0).UTC() },
	}
	first := attempt.TerminalizeAttempt(context.Background(), IntentSuccess, attemptEvidence{
		Command: sdkterminal.CommandNormalFinish, LegOutcome: usageevidence.LegOutcomeWinner,
		ALegID: "a-phase5", BillingCallID: callID,
	})
	if first.Result.Err != nil {
		t.Fatalf("first terminalization failed: %v", first.Result.Err)
	}
	if !attempt.rememberUsageEvidenceOnceAs(phase5EvidenceEvent("late-terminal", aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative, 1, 1, 1), billingEvidenceRoleSideband) {
		t.Fatal("late evidence was not captured")
	}
	second := attempt.TerminalizeAttempt(context.Background(), IntentSuccess, attemptEvidence{Command: sdkterminal.CommandNormalFinish})
	if second.Result.Err != nil {
		t.Fatalf("duplicate terminalization failed: %v", second.Result.Err)
	}
	if got := attempt.billingEvidenceSnapshot(); len(got) != 0 {
		t.Fatalf("late evidence survived duplicate terminal exit: %#v", got)
	}
}

type phase5DurabilitySink struct {
	calls       atomic.Int32
	ctxCanceled atomic.Bool
	err         error
}

func (s *phase5DurabilitySink) AppendCall(context.Context, usageevidence.CallUsageRecord) error {
	return nil
}

func (s *phase5DurabilitySink) AppendLeg(ctx context.Context, _ usageevidence.CallLegUsageRecord) error {
	s.calls.Add(1)
	if ctx.Err() != nil {
		s.ctxCanceled.Store(true)
	}
	return s.err
}

func TestPhase5StrictTerminalDurabilityFailureDoesNotRetryProviderInference(t *testing.T) {
	t.Parallel()
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	sinkErr := errors.New("phase5 sink unavailable")
	sink := &phase5DurabilitySink{err: sinkErr}
	executor := &Executor{BillingRuntime: BillingRuntime{TerminalUsageSink: sink}}
	var finalizeCalls atomic.Int32
	session := &attemptSession{
		terminal:         newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:             b2bua.BLegRecord{ALegID: "a-phase5", BLegID: "b-phase5", Seq: 1},
		cand:             routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
		billingCallID:    callID,
		billingCallState: newBillingCallState(callID),
		now:              func() time.Time { return time.Unix(101, 0).UTC() },
		finalizeBilling: func(context.Context, execbackend.BillingFinalizationInput) (aipapi.Event, error) {
			finalizeCalls.Add(1)
			return phase5EvidenceEvent("strict-finalizer", aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative, 1, 1, 3), nil
		},
	}
	session.appendBillingLegStrict = executor.appendIndependentCallLegStrict
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	first := session.TerminalizeAttempt(ctx, IntentSuccess, attemptEvidence{
		Command: sdkterminal.CommandNormalFinish, LegOutcome: usageevidence.LegOutcomeWinner,
		ALegID: "a-phase5", BillingCallID: callID, BillingState: session.billingCallState,
		Usage: phase5EvidenceEvent("strict-stream", aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative, 1, 1, 2),
	})
	if !errors.Is(first.Result.Err, sinkErr) {
		t.Fatalf("terminal result error = %v, want sink durability failure", first.Result.Err)
	}
	if sink.calls.Load() != 1 || finalizeCalls.Load() != 1 {
		t.Fatalf("sink/finalizer calls = %d/%d, want one terminal append and one inference finalizer", sink.calls.Load(), finalizeCalls.Load())
	}
	if sink.ctxCanceled.Load() {
		t.Fatal("strict sink received canceled request context; closure identity must survive request cancellation")
	}
	second := session.TerminalizeAttempt(context.Background(), IntentSuccess, attemptEvidence{Command: sdkterminal.CommandNormalFinish, LegOutcome: usageevidence.LegOutcomeWinner})
	if sink.calls.Load() != 1 || finalizeCalls.Load() != 1 {
		t.Fatalf("repeated terminalization retried effects: sink/finalizer calls = %d/%d", sink.calls.Load(), finalizeCalls.Load())
	}
	if !errors.Is(second.Result.Err, sinkErr) {
		t.Fatalf("replayed terminal result error = %v, want original sink failure", second.Result.Err)
	}
}
