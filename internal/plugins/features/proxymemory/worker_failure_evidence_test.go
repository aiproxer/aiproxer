package proxymemory

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// linkedWorkerReceipt is the host operation receipt a composed scheduler would
// hand the worker for one claimed attempt.
func linkedWorkerReceipt(base time.Time) CollectionReceipt {
	return CollectionReceipt{
		StoreID:       "store-failure-evidence",
		BillingCallID: "call-failure-evidence",
		CompletedAt:   base,
	}
}

// TestWorkerPhase1InvalidResultRecordsLinkedFailureEvidence pins the B5
// memory-processing outcome: a completed collection the parser rejects is
// recorded as immutable `invalid_result` evidence linked to the host operation,
// and the same claim is released/backed off through the store's one
// transaction instead of the legacy standalone FailSession.
func TestWorkerPhase1InvalidResultRecordsLinkedFailureEvidence(t *testing.T) {
	cfg := workerTestConfig()
	h := newWorkerTestHarness(t, cfg)
	session := workerClaimedSession(workerTestALeg1, h.base)
	session.ErasureEpoch = 4
	session.LeaseUntilMS = h.base.Add(cfg.Lease).UnixMilli()
	h.store.sessions = []ClaimedSession{session}
	h.store.failureOutcome = FailureRecorded
	h.aux.text = workerInvalidOutput
	h.aux.receipt = linkedWorkerReceipt(h.base)

	h.worker.Tick(context.Background())

	calls := h.store.failureCalls()
	if len(calls) != 1 {
		t.Fatalf("RecordPhaseFailure calls = %d, want 1", len(calls))
	}
	got := calls[0].failure
	if got.Owner != session.Owner {
		t.Fatalf("failure owner = %+v, want %+v", got.Owner, session.Owner)
	}
	if got.Phase != FailurePhaseExtraction {
		t.Fatalf("failure phase = %q, want %q", got.Phase, FailurePhaseExtraction)
	}
	if got.StoreID != "store-failure-evidence" || got.BillingCallID != "call-failure-evidence" {
		t.Fatalf("failure operation = %q/%q, want the host receipt", got.StoreID, got.BillingCallID)
	}
	if got.ClaimRevision != session.Revision || got.ClaimSeq != 0 {
		t.Fatalf("failure claim = revision %d seq %d, want revision %d seq 0",
			got.ClaimRevision, got.ClaimSeq, session.Revision)
	}
	if got.LeaseOwner != session.LeaseOwner || got.LeaseUntilMS != session.LeaseUntilMS {
		t.Fatalf("failure lease = %q/%d, want the claimed lease %q/%d",
			got.LeaseOwner, got.LeaseUntilMS, session.LeaseOwner, session.LeaseUntilMS)
	}
	if got.ErasureEpoch != 4 {
		t.Fatalf("failure epoch = %d, want the captured claim epoch 4", got.ErasureEpoch)
	}
	if got.Kind != FailureKindInvalidResult {
		t.Fatalf("failure kind = %q, want %q", got.Kind, FailureKindInvalidResult)
	}
	if got.CompletedAtMS != h.base.UnixMilli() {
		t.Fatalf("failure completed-at = %d, want the host completion time %d", got.CompletedAtMS, h.base.UnixMilli())
	}
	if want := h.base.Add(cfg.MaxUnusedAge).UnixMilli(); got.ExpiresAtMS != want {
		t.Fatalf("failure expires-at = %d, want the frozen MaxUnusedAge horizon %d", got.ExpiresAtMS, want)
	}
	if got.RecordedAtMS != h.clock.Now().UnixMilli() {
		t.Fatalf("failure recorded-at = %d, want the first-write time %d", got.RecordedAtMS, h.clock.Now().UnixMilli())
	}
	if calls[0].deadline.IsZero() {
		t.Fatal("the failure settlement must run under a bounded lexical deadline")
	}
	if remaining := time.Until(calls[0].deadline); remaining <= 0 || remaining > time.Second {
		t.Fatalf("failure settlement deadline remaining = %v, want a positive bound of at most one second", remaining)
	}
	if fails := h.store.sessionFailCalls(); len(fails) != 0 {
		t.Fatalf("FailSession calls = %d, want 0: the linked fact and release share one transaction", len(fails))
	}
	if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 1 {
		t.Fatalf("phase1_failed = %v, want 1", got)
	}
	records := h.logs.snapshot()
	if len(records) == 0 {
		t.Fatal("a recorded invalid-result failure must be logged")
	}
	for _, record := range records {
		text := captureRecordText(record)
		for _, forbidden := range []string{workerInvalidOutput, workerTestProject, workerTestALeg1, workerTestPrincipal, "call-failure-evidence"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("log record leaks %q: %q", forbidden, text)
			}
		}
	}
}

// TestWorkerPhase1InvalidResultWithoutReceiptKeepsTheUsageOnlyPath pins the
// honest missing-receipt rule: usage-only execution keeps working, the legacy
// claim release still runs, and no linked fact is fabricated.
func TestWorkerPhase1InvalidResultWithoutReceiptKeepsTheUsageOnlyPath(t *testing.T) {
	h := newWorkerTestHarness(t, workerTestConfig())
	h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}
	h.aux.text = workerInvalidOutput

	h.worker.Tick(context.Background())

	if calls := h.store.failureCalls(); len(calls) != 0 {
		t.Fatalf("RecordPhaseFailure calls = %d, want 0 without a host receipt", len(calls))
	}
	if fails := h.store.sessionFailCalls(); len(fails) != 1 {
		t.Fatalf("FailSession calls = %d, want the legacy release exactly once", len(fails))
	}
	if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 1 {
		t.Fatalf("phase1_failed = %v, want 1", got)
	}
	if got := captureCounter(t, h.metrics, EventPhase1InvalidResultUnlinked); got != 1 {
		t.Fatalf("phase1_invalid_result_unlinked = %v, want one explicit diagnostic", got)
	}
	requireLogClassification(t, h.logs.snapshot(), "unlinked")
}

// TestWorkerPhase1InvalidResultWriteFailureRetainsTheLease pins the durability
// boundary: when the evidence transaction fails, the worker does not fall back
// to a second standalone failure write, does not claim durable evidence, and
// leaves the claim to its lease expiry.
func TestWorkerPhase1InvalidResultWriteFailureRetainsTheLease(t *testing.T) {
	h := newWorkerTestHarness(t, workerTestConfig())
	session := workerClaimedSession(workerTestALeg1, h.base)
	session.ErasureEpoch = 1
	session.LeaseUntilMS = h.base.Add(h.worker.cfg.Lease).UnixMilli()
	h.store.sessions = []ClaimedSession{session}
	h.store.failureErr = errors.New("database unavailable")
	h.aux.text = workerInvalidOutput
	h.aux.receipt = linkedWorkerReceipt(h.base)

	h.worker.Tick(context.Background())

	if calls := h.store.failureCalls(); len(calls) != 1 {
		t.Fatalf("RecordPhaseFailure calls = %d, want 1", len(calls))
	}
	if fails := h.store.sessionFailCalls(); len(fails) != 0 {
		t.Fatalf("FailSession calls = %d, want 0 after a failed evidence transaction", len(fails))
	}
	if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 1 {
		t.Fatalf("phase1_failed = %v, want 1", got)
	}
}

// TestWorkerPhase1StaleEvidenceDoesNotCountAsAFailureRelease pins the stale
// claim outcome: the fact may be recorded, but a successor claim is not this
// attempt's to clear, so the worker reports the lease loss instead of a normal
// failure release.
func TestWorkerPhase1StaleEvidenceDoesNotCountAsAFailureRelease(t *testing.T) {
	h := newWorkerTestHarness(t, workerTestConfig())
	session := workerClaimedSession(workerTestALeg1, h.base)
	session.ErasureEpoch = 2
	session.LeaseUntilMS = h.base.Add(h.worker.cfg.Lease).UnixMilli()
	h.store.sessions = []ClaimedSession{session}
	h.store.failureOutcome = FailureRecordedStale
	h.aux.text = workerInvalidOutput
	h.aux.receipt = linkedWorkerReceipt(h.base)

	h.worker.Tick(context.Background())

	if calls := h.store.failureCalls(); len(calls) != 1 {
		t.Fatalf("RecordPhaseFailure calls = %d, want 1", len(calls))
	}
	if got := captureCounter(t, h.metrics, EventPhase1LeaseLost); got != 1 {
		t.Fatalf("phase1_lease_lost = %v, want 1 for stale evidence", got)
	}
	if fails := h.store.sessionFailCalls(); len(fails) != 0 {
		t.Fatalf("FailSession calls = %d, want 0", len(fails))
	}
}

// TestWorkerPhase2InvalidResultRecordsLinkedFailureEvidence pins the phase 2
// half: the fact carries the claimed bank version and dirty sequence, and the
// same transaction conditionally releases the bank claim instead of FailBank.
func TestWorkerPhase2InvalidResultRecordsLinkedFailureEvidence(t *testing.T) {
	cfg := workerTestConfig()
	h := newWorkerTestHarness(t, cfg)
	bank := workerClaimedBank()
	bank.ErasureEpoch = 9
	bank.LeaseUntilMS = h.base.Add(cfg.Lease).UnixMilli()
	h.store.banks = []ClaimedBank{bank}
	h.store.extractions = []Extraction{workerExtraction(h.base)}
	h.store.failureOutcome = FailureRecorded
	h.aux.text = workerInvalidOutput
	h.aux.receipt = linkedWorkerReceipt(h.base)

	h.worker.Tick(context.Background())

	calls := h.store.failureCalls()
	if len(calls) != 1 {
		t.Fatalf("RecordPhaseFailure calls = %d, want 1", len(calls))
	}
	got := calls[0].failure
	if got.Phase != FailurePhaseConsolidation {
		t.Fatalf("failure phase = %q, want %q", got.Phase, FailurePhaseConsolidation)
	}
	if got.ClaimRevision != bank.Version || got.ClaimSeq != bank.DirtySeq {
		t.Fatalf("failure claim = version %d dirty %d, want the claimed bank %d/%d",
			got.ClaimRevision, got.ClaimSeq, bank.Version, bank.DirtySeq)
	}
	if got.ErasureEpoch != 9 {
		t.Fatalf("failure epoch = %d, want the captured claim epoch 9", got.ErasureEpoch)
	}
	if fails := h.store.bankFailCalls(); len(fails) != 0 {
		t.Fatalf("FailBank calls = %d, want 0: the linked fact and release share one transaction", len(fails))
	}
	if got := captureCounter(t, h.metrics, EventPhase2Failed); got != 1 {
		t.Fatalf("phase2_failed = %v, want 1", got)
	}
}

// TestWorkerPhase2InvalidResultWithoutReceiptKeepsTheUsageOnlyPath pins the
// phase 2 missing-receipt fallback.
func TestWorkerPhase2InvalidResultWithoutReceiptKeepsTheUsageOnlyPath(t *testing.T) {
	h := newWorkerTestHarness(t, workerTestConfig())
	h.store.banks = []ClaimedBank{workerClaimedBank()}
	h.store.extractions = []Extraction{workerExtraction(h.base)}
	h.aux.text = workerInvalidOutput

	h.worker.Tick(context.Background())

	if calls := h.store.failureCalls(); len(calls) != 0 {
		t.Fatalf("RecordPhaseFailure calls = %d, want 0 without a host receipt", len(calls))
	}
	if fails := h.store.bankFailCalls(); len(fails) != 1 {
		t.Fatalf("FailBank calls = %d, want the legacy release exactly once", len(fails))
	}
	if got := captureCounter(t, h.metrics, EventPhase2InvalidResultUnlinked); got != 1 {
		t.Fatalf("phase2_invalid_result_unlinked = %v, want one explicit diagnostic", got)
	}
	requireLogClassification(t, h.logs.snapshot(), "unlinked")
}

// TestWorkerInvalidResultSettlementHonoursParentCancellation pins the lexical
// settlement boundary: the evidence write runs under the worker context, so a
// cancelled parent is observed by the store, no detached write is started and
// the claim is left to its lease expiry rather than released by a fallback.
func TestWorkerInvalidResultSettlementHonoursParentCancellation(t *testing.T) {
	h := newWorkerTestHarness(t, workerTestConfig())
	session := workerClaimedSession(workerTestALeg1, h.base)
	session.ErasureEpoch = 3
	session.LeaseUntilMS = h.base.Add(h.worker.cfg.Lease).UnixMilli()
	h.store.failureErr = context.Canceled

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.worker.failSessionInvalidResult(ctx, session, linkedWorkerReceipt(h.base), errors.New("parse rejected"))

	calls := h.store.failureCalls()
	if len(calls) != 1 {
		t.Fatalf("RecordPhaseFailure calls = %d, want 1", len(calls))
	}
	if !errors.Is(calls[0].ctxErr, context.Canceled) {
		t.Fatalf("settlement context error = %v, want the parent cancellation", calls[0].ctxErr)
	}
	if fails := h.store.sessionFailCalls(); len(fails) != 0 {
		t.Fatalf("FailSession calls = %d, want no fallback after a cancelled settlement", len(fails))
	}
	if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 1 {
		t.Fatalf("phase1_failed = %v, want 1", got)
	}
}

// requireLogClassification asserts a bounded diagnostic classification is
// present without accepting owner, project, call, store or content leakage.
func requireLogClassification(t *testing.T, records []slog.Record, classification string) {
	t.Helper()
	for _, record := range records {
		text := captureRecordText(record)
		if strings.Contains(text, classification) {
			for _, forbidden := range []string{workerTestProject, workerTestALeg1, workerTestPrincipal, workerInvalidOutput, "call-failure-evidence", "store-failure-evidence"} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("diagnostic leaks %q: %q", forbidden, text)
				}
			}
			return
		}
	}
	t.Fatalf("no bounded %q diagnostic in logs: %#v", classification, records)
}
