package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type sidebandEvidenceStream struct {
	events   []aipapi.Event
	sideband []aipapi.Event
	mu       sync.Mutex
}

func (s *sidebandEvidenceStream) Recv(context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[0]
	s.events = s.events[1:]
	return ev, nil
}

func (s *sidebandEvidenceStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

func (s *sidebandEvidenceStream) Close() error {
	return nil
}

func (s *sidebandEvidenceStream) DrainUsageEvidence() []aipapi.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]aipapi.Event(nil), s.sideband...)
	s.sideband = nil
	return out
}

func TestRED_TerminalSideband_FinalizerUnsupportedLosesBufferedSideband(t *testing.T) {
	t.Parallel()

	var recordedLegs []usageevidence.CallLegUsageRecord
	var mu sync.Mutex

	ex := TestExecutor()
	ex.BillingLegObserver = BillingLegObserverFunc(func(_ context.Context, record usageevidence.CallLegUsageRecord) {
		mu.Lock()
		recordedLegs = append(recordedLegs, record)
		mu.Unlock()
	})

	stream := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: "a-sideband-1",
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{BLegID: "b-sideband-1", ALegID: "a-sideband-1", Seq: 1},
			routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-sideband", Model: "model-1"}},
			authorityLifecycle{},
		),
	}
	bindTestRuntimeOwners(stream, ex)

	// Stream with buffered sideband evidence (e.g. input tokens = 42, cost = 100000)
	sidebandStream := &sidebandEvidenceStream{
		sideband: []aipapi.Event{{
			Kind:          aipapi.EventUsageDelta,
			InputTokens:   42,
			OutputTokens:  0,
			CostNanoUnits: 100000,
			Currency:      "USD",
			CostPresent:   true,
			UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
			Accounting: aipapi.UsageAccountingMetadata{
				Source:    aipapi.UsageSourceProviderReported,
				Authority: aipapi.UsageAuthorityAuthoritative,
				DedupeKey: "sideband-charge-1",
			},
		}},
	}
	testStoreInner(stream, sidebandStream)

	// Terminalize attempt as canceled (never surfaced output)
	sess := stream.attempt.snapshot()
	sess.TerminalizeAttempt(context.Background(), IntentCancellation, attemptEvidence{
		Command:     sdkterminal.CommandCancel,
		LegOutcome:  usageevidence.LegOutcomeCanceled,
		CancelCause: &aipapi.CancelCause{Kind: aipapi.CancelExplicit},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(recordedLegs) != 1 {
		t.Fatalf("recorded legs = %d, want 1", len(recordedLegs))
	}
	got := recordedLegs[0]
	if got.BLegID != "b-sideband-1" || got.AttemptSeq != 1 {
		t.Fatalf("record lineage = %+v", got)
	}
	// Assert provider-sideband evidence was preserved in terminal record
	if !got.Evidence.InputTokens.Present || got.Evidence.InputTokens.Value != 42 {
		t.Fatalf("expected InputTokens=42 preserved from sideband, got %+v", got.Evidence.InputTokens)
	}
}

func TestRED_TerminalSideband_FinalizerFailureLosesBufferedSideband(t *testing.T) {
	t.Parallel()

	var recordedLegs []usageevidence.CallLegUsageRecord
	var mu sync.Mutex

	ex := TestExecutor()
	ex.BillingLegObserver = BillingLegObserverFunc(func(_ context.Context, record usageevidence.CallLegUsageRecord) {
		mu.Lock()
		recordedLegs = append(recordedLegs, record)
		mu.Unlock()
	})

	stream := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: "a-sideband-2",
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{BLegID: "b-sideband-2", ALegID: "a-sideband-2", Seq: 2},
			routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-sideband", Model: "model-2"}},
			authorityLifecycle{},
		),
	}
	bindTestRuntimeOwners(stream, ex)

	// Mock finalize billing failure
	sess := stream.attempt.snapshot()
	sess.finalizeBilling = func(ctx context.Context, in execbackend.BillingFinalizationInput) (aipapi.Event, error) {
		return aipapi.Event{}, errors.New("provider finalize API error")
	}

	sidebandStream := &sidebandEvidenceStream{
		sideband: []aipapi.Event{{
			Kind:          aipapi.EventUsageDelta,
			InputTokens:   84,
			OutputTokens:  0,
			CostNanoUnits: 200000,
			Currency:      "USD",
			CostPresent:   true,
			UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
			Accounting: aipapi.UsageAccountingMetadata{
				Source:    aipapi.UsageSourceProviderReported,
				Authority: aipapi.UsageAuthorityAuthoritative,
				DedupeKey: "sideband-charge-2",
			},
		}},
	}
	testStoreInner(stream, sidebandStream)

	sess.TerminalizeAttempt(context.Background(), IntentParallelLoser, attemptEvidence{
		Command:    sdkterminal.CommandParallelLoser,
		LegOutcome: usageevidence.LegOutcomeFailed,
	})

	mu.Lock()
	defer mu.Unlock()
	if len(recordedLegs) != 1 {
		t.Fatalf("recorded legs = %d, want 1", len(recordedLegs))
	}
	got := recordedLegs[0]
	if !got.Evidence.InputTokens.Present || got.Evidence.InputTokens.Value != 84 {
		t.Fatalf("expected InputTokens=84 preserved from sideband after finalizer failure, got %+v", got.Evidence.InputTokens)
	}
}

func TestRED_TerminalSideband_DuplicateDedupeKeysDeduped(t *testing.T) {
	t.Parallel()

	var recordedLegs []usageevidence.CallLegUsageRecord
	var mu sync.Mutex

	ex := TestExecutor()
	ex.BillingLegObserver = BillingLegObserverFunc(func(_ context.Context, record usageevidence.CallLegUsageRecord) {
		mu.Lock()
		recordedLegs = append(recordedLegs, record)
		mu.Unlock()
	})

	stream := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: "a-sideband-3",
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{BLegID: "b-sideband-3", ALegID: "a-sideband-3", Seq: 1},
			routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-sideband", Model: "model-3"}},
			authorityLifecycle{},
		),
	}
	bindTestRuntimeOwners(stream, ex)

	sess := stream.attempt.snapshot()
	sidebandStream := &sidebandEvidenceStream{
		sideband: []aipapi.Event{
			{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   10,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "dup-key-1",
				},
			},
			{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   10,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "dup-key-1",
				},
			},
		},
	}
	testStoreInner(stream, sidebandStream)

	sess.TerminalizeAttempt(context.Background(), IntentCancellation, attemptEvidence{
		Command:     sdkterminal.CommandCancel,
		LegOutcome:  usageevidence.LegOutcomeCanceled,
		CancelCause: &aipapi.CancelCause{Kind: aipapi.CancelExplicit},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(recordedLegs) != 1 {
		t.Fatalf("recorded legs = %d, want 1", len(recordedLegs))
	}
	got := recordedLegs[0]
	// If deduped, total input tokens should be 10 (not 20)
	if got.Evidence.InputTokens.Value != 10 {
		t.Fatalf("expected deduped InputTokens=10, got %d", got.Evidence.InputTokens.Value)
	}
}

func TestCharacterization_TerminalSideband_FinalizerSuccessTakesPrecedence(t *testing.T) {
	t.Parallel()

	var recordedLegs []usageevidence.CallLegUsageRecord
	var mu sync.Mutex

	ex := TestExecutor()
	ex.BillingLegObserver = BillingLegObserverFunc(func(_ context.Context, record usageevidence.CallLegUsageRecord) {
		mu.Lock()
		recordedLegs = append(recordedLegs, record)
		mu.Unlock()
	})

	callID := usageevidence.BillingCallID("bc_11111111111111111111111111111111")
	callState := newBillingCallState(callID)

	stream := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:           "a-sideband-4",
			billingCallID:    callID,
			billingCallState: callState,
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{BLegID: "b-sideband-4", ALegID: "a-sideband-4", Seq: 1},
			routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-sideband", Model: "model-4"}},
			authorityLifecycle{},
		),
	}
	bindTestRuntimeOwners(stream, ex)

	sess := stream.attempt.snapshot()
	sess.billingCallID = callID
	sess.billingCallState = callState
	sess.finalizeBilling = func(ctx context.Context, in execbackend.BillingFinalizationInput) (aipapi.Event, error) {
		return aipapi.Event{
			Kind:          aipapi.EventUsageDelta,
			InputTokens:   100,
			OutputTokens:  50,
			CostNanoUnits: 500000,
			Currency:      "USD",
			CostPresent:   true,
			UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
			Accounting: aipapi.UsageAccountingMetadata{
				Source:    aipapi.UsageSourceProviderReported,
				Authority: aipapi.UsageAuthorityAuthoritative,
				DedupeKey: "finalizer-charge",
			},
		}, nil
	}

	sess.TerminalizeAttempt(context.Background(), IntentCancellation, attemptEvidence{
		Command:     sdkterminal.CommandCancel,
		LegOutcome:  usageevidence.LegOutcomeCanceled,
		CancelCause: &aipapi.CancelCause{Kind: aipapi.CancelExplicit},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(recordedLegs) != 1 {
		t.Fatalf("recorded legs = %d, want 1", len(recordedLegs))
	}
	got := recordedLegs[0]
	if !got.Evidence.InputTokens.Present || got.Evidence.InputTokens.Value != 100 {
		t.Fatalf("expected authoritative finalizer InputTokens=100, got %+v", got.Evidence.InputTokens)
	}
	if !got.Evidence.OutputTokens.Present || got.Evidence.OutputTokens.Value != 50 {
		t.Fatalf("expected authoritative finalizer OutputTokens=50, got %+v", got.Evidence.OutputTokens)
	}
}

func TestCharacterization_TerminalSideband_NoEvidenceLeavesUnavailable(t *testing.T) {
	t.Parallel()

	var recordedLegs []usageevidence.CallLegUsageRecord
	var mu sync.Mutex

	ex := TestExecutor()
	ex.BillingLegObserver = BillingLegObserverFunc(func(_ context.Context, record usageevidence.CallLegUsageRecord) {
		mu.Lock()
		recordedLegs = append(recordedLegs, record)
		mu.Unlock()
	})

	stream := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: "a-sideband-5",
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{BLegID: "b-sideband-5", ALegID: "a-sideband-5", Seq: 1},
			routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-sideband", Model: "model-5"}},
			authorityLifecycle{},
		),
	}
	bindTestRuntimeOwners(stream, ex)

	sess := stream.attempt.snapshot()
	sess.TerminalizeAttempt(context.Background(), IntentCancellation, attemptEvidence{
		Command:     sdkterminal.CommandCancel,
		LegOutcome:  usageevidence.LegOutcomeCanceled,
		CancelCause: &aipapi.CancelCause{Kind: aipapi.CancelExplicit},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(recordedLegs) != 1 {
		t.Fatalf("recorded legs = %d, want 1", len(recordedLegs))
	}
	got := recordedLegs[0]
	// When no usage evidence exists, usage should not be present (not fabricated zero)
	if got.Evidence.InputTokens.Present || got.Evidence.OutputTokens.Present {
		t.Fatalf("expected usage evidence to be unavailable, got %+v", got.Evidence)
	}
}
