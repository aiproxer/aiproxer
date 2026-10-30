package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestPhase5_MultipleUniqueDedupeKeysAggregate(t *testing.T) {
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
			aLegID: "a-multi-key",
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{BLegID: "b-multi-key", ALegID: "a-multi-key", Seq: 1},
			routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-multi", Model: "model-multi"}},
			authorityLifecycle{},
		),
	}
	bindTestRuntimeOwners(stream, ex)

	sidebandStream := &sidebandEvidenceStream{
		sideband: []aipapi.Event{
			{
				Kind:             aipapi.EventUsageDelta,
				InputTokens:      10,
				OutputTokens:     5,
				CacheReadTokens:  2,
				CacheWriteTokens: 1,
				ReasoningTokens:  3,
				TotalTokens:      15,
				CostNanoUnits:    100000,
				Currency:         "USD",
				CostPresent:      true,
				UsagePresence: aipapi.UsagePresence{
					InputTokens:      true,
					OutputTokens:     true,
					CacheReadTokens:  true,
					CacheWriteTokens: true,
					ReasoningTokens:  true,
					TotalTokens:      true,
				},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "charge-1",
				},
			},
			{
				Kind:             aipapi.EventUsageDelta,
				InputTokens:      20,
				OutputTokens:     10,
				CacheReadTokens:  4,
				CacheWriteTokens: 2,
				ReasoningTokens:  6,
				TotalTokens:      30,
				CostNanoUnits:    200000,
				Currency:         "USD",
				CostPresent:      true,
				UsagePresence: aipapi.UsagePresence{
					InputTokens:      true,
					OutputTokens:     true,
					CacheReadTokens:  true,
					CacheWriteTokens: true,
					ReasoningTokens:  true,
					TotalTokens:      true,
				},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "charge-2",
				},
			},
			{
				Kind:             aipapi.EventUsageDelta,
				InputTokens:      30,
				OutputTokens:     15,
				CacheReadTokens:  6,
				CacheWriteTokens: 3,
				ReasoningTokens:  9,
				TotalTokens:      45,
				CostNanoUnits:    300000,
				Currency:         "USD",
				CostPresent:      true,
				UsagePresence: aipapi.UsagePresence{
					InputTokens:      true,
					OutputTokens:     true,
					CacheReadTokens:  true,
					CacheWriteTokens: true,
					ReasoningTokens:  true,
					TotalTokens:      true,
				},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "charge-3",
				},
			},
		},
	}
	testStoreInner(stream, sidebandStream)

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
	if !got.Evidence.InputTokens.Present || got.Evidence.InputTokens.Value != 60 {
		t.Fatalf("expected aggregated InputTokens=60, got %+v", got.Evidence.InputTokens)
	}
	if !got.Evidence.OutputTokens.Present || got.Evidence.OutputTokens.Value != 30 {
		t.Fatalf("expected aggregated OutputTokens=30, got %+v", got.Evidence.OutputTokens)
	}
	if !got.Evidence.CacheReadTokens.Present || got.Evidence.CacheReadTokens.Value != 12 {
		t.Fatalf("expected aggregated CacheReadTokens=12, got %+v", got.Evidence.CacheReadTokens)
	}
	if !got.Evidence.CacheWriteTokens.Present || got.Evidence.CacheWriteTokens.Value != 6 {
		t.Fatalf("expected aggregated CacheWriteTokens=6, got %+v", got.Evidence.CacheWriteTokens)
	}
	if !got.Evidence.ReasoningTokens.Present || got.Evidence.ReasoningTokens.Value != 18 {
		t.Fatalf("expected aggregated ReasoningTokens=18, got %+v", got.Evidence.ReasoningTokens)
	}
	if !got.Evidence.TotalTokens.Present || got.Evidence.TotalTokens.Value != 90 {
		t.Fatalf("expected aggregated TotalTokens=90, got %+v", got.Evidence.TotalTokens)
	}
	if !got.Evidence.Cost.Present || got.Evidence.Cost.NanoUnits != 600000 {
		t.Fatalf("expected aggregated Cost=600000, got %+v", got.Evidence.Cost)
	}
}

func TestPhase5_DuplicateAcrossNormalAndTerminalDrain(t *testing.T) {
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
			aLegID: "a-dup-drain",
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{BLegID: "b-dup-drain", ALegID: "a-dup-drain", Seq: 1},
			routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-dup", Model: "model-dup"}},
			authorityLifecycle{},
		),
		responsePipeline: newResponsePipeline(),
	}
	bindTestRuntimeOwners(stream, ex)

	// Stage 1: normal drain receives charge 1
	sidebandStream := &sidebandEvidenceStream{
		sideband: []aipapi.Event{
			{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   15,
				CostNanoUnits: 50000,
				Currency:      "USD",
				CostPresent:   true,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "key-1",
				},
			},
		},
	}
	testStoreInner(stream, sidebandStream)

	sess := stream.attempt.snapshot()
	sess.drainSidebandEvidence(context.Background(), stream.facts, stream.responsePipeline)

	// Stage 2: stream now has duplicate key-1 and new key-2
	sidebandStream.mu.Lock()
	sidebandStream.sideband = []aipapi.Event{
		{
			Kind:          aipapi.EventUsageDelta,
			InputTokens:   15,
			CostNanoUnits: 50000,
			Currency:      "USD",
			CostPresent:   true,
			UsagePresence: aipapi.UsagePresence{InputTokens: true},
			Accounting: aipapi.UsageAccountingMetadata{
				Source:    aipapi.UsageSourceProviderReported,
				Authority: aipapi.UsageAuthorityAuthoritative,
				DedupeKey: "key-1", // duplicate!
			},
		},
		{
			Kind:          aipapi.EventUsageDelta,
			InputTokens:   25,
			CostNanoUnits: 75000,
			Currency:      "USD",
			CostPresent:   true,
			UsagePresence: aipapi.UsagePresence{InputTokens: true},
			Accounting: aipapi.UsageAccountingMetadata{
				Source:    aipapi.UsageSourceProviderReported,
				Authority: aipapi.UsageAuthorityAuthoritative,
				DedupeKey: "key-2", // new!
			},
		},
	}
	sidebandStream.mu.Unlock()

	// Terminalize attempt
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
	// 15 (key-1) + 25 (key-2) = 40
	if !got.Evidence.InputTokens.Present || got.Evidence.InputTokens.Value != 40 {
		t.Fatalf("expected deduped InputTokens=40, got %+v", got.Evidence.InputTokens)
	}
	if !got.Evidence.Cost.Present || got.Evidence.Cost.NanoUnits != 125000 {
		t.Fatalf("expected deduped Cost=125000, got %+v", got.Evidence.Cost)
	}
}

type stagedEvidenceStream struct {
	mu           sync.Mutex
	beforeCancel []aipapi.Event
	duringCancel []aipapi.Event
	afterClose   []aipapi.Event
	cancelCalled atomic.Int32
	closeCalled  atomic.Int32
}

func (s *stagedEvidenceStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *stagedEvidenceStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCalled.Add(1)
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

func (s *stagedEvidenceStream) Close() error {
	s.closeCalled.Add(1)
	return nil
}

func (s *stagedEvidenceStream) DrainUsageEvidence() []aipapi.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeCalled.Load() > 0 && len(s.afterClose) > 0 {
		out := s.afterClose
		s.afterClose = nil
		return out
	}
	if s.cancelCalled.Load() > 0 && len(s.duringCancel) > 0 {
		out := s.duringCancel
		s.duringCancel = nil
		return out
	}
	if len(s.beforeCancel) > 0 {
		out := s.beforeCancel
		s.beforeCancel = nil
		return out
	}
	return nil
}

func TestPhase5_DrainBeforeDuringAfterCancel(t *testing.T) {
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
			aLegID: "a-staged-drain",
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{BLegID: "b-staged-drain", ALegID: "a-staged-drain", Seq: 1},
			routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-staged", Model: "model-staged"}},
			authorityLifecycle{},
		),
	}
	bindTestRuntimeOwners(stream, ex)

	stagedStream := &stagedEvidenceStream{
		beforeCancel: []aipapi.Event{{
			Kind:          aipapi.EventUsageDelta,
			InputTokens:   10,
			CostNanoUnits: 10000,
			CostPresent:   true,
			UsagePresence: aipapi.UsagePresence{InputTokens: true},
			Accounting: aipapi.UsageAccountingMetadata{
				Source:    aipapi.UsageSourceProviderReported,
				Authority: aipapi.UsageAuthorityAuthoritative,
				DedupeKey: "stage-1-before-cancel",
			},
		}},
		duringCancel: []aipapi.Event{{
			Kind:          aipapi.EventUsageDelta,
			InputTokens:   20,
			CostNanoUnits: 20000,
			CostPresent:   true,
			UsagePresence: aipapi.UsagePresence{InputTokens: true},
			Accounting: aipapi.UsageAccountingMetadata{
				Source:    aipapi.UsageSourceProviderReported,
				Authority: aipapi.UsageAuthorityAuthoritative,
				DedupeKey: "stage-2-during-cancel",
			},
		}},
		afterClose: []aipapi.Event{{
			Kind:          aipapi.EventUsageDelta,
			InputTokens:   30,
			CostNanoUnits: 30000,
			CostPresent:   true,
			UsagePresence: aipapi.UsagePresence{InputTokens: true},
			Accounting: aipapi.UsageAccountingMetadata{
				Source:    aipapi.UsageSourceProviderReported,
				Authority: aipapi.UsageAuthorityAuthoritative,
				DedupeKey: "stage-3-after-close",
			},
		}},
	}
	testStoreInner(stream, stagedStream)

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
	// 10 + 20 + 30 = 60
	if !got.Evidence.InputTokens.Present || got.Evidence.InputTokens.Value != 60 {
		t.Fatalf("expected all stages drained InputTokens=60, got %+v", got.Evidence.InputTokens)
	}
	if !got.Evidence.Cost.Present || got.Evidence.Cost.NanoUnits != 60000 {
		t.Fatalf("expected all stages drained Cost=60000, got %+v", got.Evidence.Cost)
	}
	if stagedStream.cancelCalled.Load() != 1 {
		t.Fatalf("expected cancelCalled=1, got %d", stagedStream.cancelCalled.Load())
	}
	if stagedStream.closeCalled.Load() != 1 {
		t.Fatalf("expected closeCalled=1, got %d", stagedStream.closeCalled.Load())
	}
}

type panickingDrainStream struct {
	cancelCount atomic.Int32
	closeCount  atomic.Int32
}

func (p *panickingDrainStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (p *panickingDrainStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	p.cancelCount.Add(1)
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

func (p *panickingDrainStream) Close() error {
	p.closeCount.Add(1)
	return nil
}

func (p *panickingDrainStream) DrainUsageEvidence() []aipapi.Event {
	panic("malicious drain panic")
}

func TestPhase5_DrainFailureBestEffort(t *testing.T) {
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
			aLegID: "a-panic-drain",
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{BLegID: "b-panic-drain", ALegID: "a-panic-drain", Seq: 1},
			routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-panic", Model: "model-panic"}},
			authorityLifecycle{},
		),
	}
	bindTestRuntimeOwners(stream, ex)

	panicStream := &panickingDrainStream{}
	testStoreInner(stream, panicStream)

	sess := stream.attempt.snapshot()
	res := sess.TerminalizeAttempt(context.Background(), IntentCancellation, attemptEvidence{
		Command:     sdkterminal.CommandCancel,
		LegOutcome:  usageevidence.LegOutcomeCanceled,
		CancelCause: &aipapi.CancelCause{Kind: aipapi.CancelExplicit},
	})

	if res.Result.Err != nil {
		t.Fatalf("unexpected terminalize error: %v", res.Result.Err)
	}
	if panicStream.cancelCount.Load() != 1 {
		t.Fatalf("cancelCount = %d, want 1", panicStream.cancelCount.Load())
	}
	if panicStream.closeCount.Load() != 1 {
		t.Fatalf("closeCount = %d, want 1", panicStream.closeCount.Load())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(recordedLegs) != 1 {
		t.Fatalf("recorded legs = %d, want 1", len(recordedLegs))
	}
	got := recordedLegs[0]
	if got.BLegID != "b-panic-drain" || got.AttemptSeq != 1 {
		t.Fatalf("record lineage = %+v", got)
	}
}

func TestPhase5_FinalizerCostAugmentation(t *testing.T) {
	t.Parallel()

	var recordedLegs []usageevidence.CallLegUsageRecord
	var mu sync.Mutex

	ex := TestExecutor()
	ex.BillingLegObserver = BillingLegObserverFunc(func(_ context.Context, record usageevidence.CallLegUsageRecord) {
		mu.Lock()
		recordedLegs = append(recordedLegs, record)
		mu.Unlock()
	})

	callID := usageevidence.BillingCallID("bc_22222222222222222222222222222222")
	callState := newBillingCallState(callID)

	stream := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:           "a-augment",
			billingCallID:    callID,
			billingCallState: callState,
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{BLegID: "b-augment", ALegID: "a-augment", Seq: 1},
			routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-augment", Model: "model-augment"}},
			authorityLifecycle{},
		),
	}
	bindTestRuntimeOwners(stream, ex)

	sess := stream.attempt.snapshot()
	sess.billingCallID = callID
	sess.billingCallState = callState
	// Finalizer returns tokens only, no cost
	sess.finalizeBilling = func(ctx context.Context, in execbackend.BillingFinalizationInput) (aipapi.Event, error) {
		return aipapi.Event{
			Kind:          aipapi.EventUsageDelta,
			InputTokens:   200,
			OutputTokens:  100,
			UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
			Accounting: aipapi.UsageAccountingMetadata{
				Source:    aipapi.UsageSourceProviderReported,
				Authority: aipapi.UsageAuthorityAuthoritative,
				DedupeKey: "finalizer-tokens-only",
			},
		}, nil
	}

	// Sideband returns cost only
	sidebandStream := &sidebandEvidenceStream{
		sideband: []aipapi.Event{{
			Kind:          aipapi.EventUsageDelta,
			CostNanoUnits: 450000,
			Currency:      "USD",
			CostPresent:   true,
			Accounting: aipapi.UsageAccountingMetadata{
				Source:    aipapi.UsageSourceProviderReported,
				Authority: aipapi.UsageAuthorityAuthoritative,
				DedupeKey: "sideband-cost-only",
			},
		}},
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
	// Authoritative tokens from finalizer
	if !got.Evidence.InputTokens.Present || got.Evidence.InputTokens.Value != 200 {
		t.Fatalf("expected InputTokens=200, got %+v", got.Evidence.InputTokens)
	}
	if !got.Evidence.OutputTokens.Present || got.Evidence.OutputTokens.Value != 100 {
		t.Fatalf("expected OutputTokens=100, got %+v", got.Evidence.OutputTokens)
	}
	// Cost augmented from sideband
	if !got.Evidence.Cost.Present || got.Evidence.Cost.NanoUnits != 450000 {
		t.Fatalf("expected augmented Cost=450000, got %+v", got.Evidence.Cost)
	}
}

func TestPhase5_LateOpenReadyDisposePreservesEvidence(t *testing.T) {
	t.Parallel()

	var recordedLegs []usageevidence.CallLegUsageRecord
	var mu sync.Mutex

	sess := newAttemptSession(attemptSessionInput{
		inner: &sidebandEvidenceStream{
			sideband: []aipapi.Event{{
				Kind:          aipapi.EventUsageDelta,
				InputTokens:   77,
				CostNanoUnits: 77000,
				Currency:      "USD",
				CostPresent:   true,
				UsagePresence: aipapi.UsagePresence{InputTokens: true},
				Accounting: aipapi.UsageAccountingMetadata{
					Source:    aipapi.UsageSourceProviderReported,
					Authority: aipapi.UsageAuthorityAuthoritative,
					DedupeKey: "late-open-charge",
				},
			}},
		},
		bleg: b2bua.BLegRecord{BLegID: "b-late-open", ALegID: "a-late-open", Seq: 3},
		cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-late", Model: "model-late"}},
		observeBillingLeg: func(_ context.Context, r usageevidence.CallLegUsageRecord) {
			mu.Lock()
			recordedLegs = append(recordedLegs, r)
			mu.Unlock()
		},
	})

	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	// Dispose ready attempt (as happens when Open returns after cancellation)
	ready.Dispose(context.Background(), errors.New("open returned after cancel"))

	mu.Lock()
	defer mu.Unlock()
	if len(recordedLegs) != 1 {
		t.Fatalf("recorded legs = %d, want 1", len(recordedLegs))
	}
	got := recordedLegs[0]
	if got.BLegID != "b-late-open" || got.AttemptSeq != 3 {
		t.Fatalf("lineage = %+v", got)
	}
	if !got.Evidence.InputTokens.Present || got.Evidence.InputTokens.Value != 77 {
		t.Fatalf("expected InputTokens=77 preserved on late open dispose, got %+v", got.Evidence.InputTokens)
	}
}

func TestPhase5_BoundedAccumulatorFailsSafely(t *testing.T) {
	t.Parallel()

	sess := newAttemptSession(attemptSessionInput{})
	// Try adding 2000 unique events
	for i := range 2000 {
		ev := aipapi.Event{
			Kind:        aipapi.EventUsageDelta,
			InputTokens: 1,
			Accounting: aipapi.UsageAccountingMetadata{
				DedupeKey: "key-" + string(rune(i)),
			},
		}
		sess.rememberUsageEvidenceOnce(ev)
	}

	sess.usageMu.Lock()
	count := len(sess.accumulatedUsage)
	sess.usageMu.Unlock()

	if count > maxAttemptAccumulatedUsage {
		t.Fatalf("accumulatedUsage length %d exceeds bound %d", count, maxAttemptAccumulatedUsage)
	}
	if count != maxAttemptAccumulatedUsage {
		t.Fatalf("accumulatedUsage length = %d, want %d", count, maxAttemptAccumulatedUsage)
	}
}

func TestPhase5_ParallelLoserWithSidebandEvidence(t *testing.T) {
	t.Parallel()

	var recordedLegs []usageevidence.CallLegUsageRecord
	var mu sync.Mutex

	ex := TestExecutor()
	ex.BillingLegObserver = BillingLegObserverFunc(func(_ context.Context, record usageevidence.CallLegUsageRecord) {
		mu.Lock()
		recordedLegs = append(recordedLegs, record)
		mu.Unlock()
	})

	callID := usageevidence.BillingCallID("bc_33333333333333333333333333333333")
	callState := newBillingCallState(callID)

	stream := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:           "a-loser-sideband",
			billingCallID:    callID,
			billingCallState: callState,
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{BLegID: "b-loser-sideband", ALegID: "a-loser-sideband", Seq: 2},
			routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-loser", Model: "model-loser"}},
			authorityLifecycle{},
		),
	}
	bindTestRuntimeOwners(stream, ex)

	sess := stream.attempt.snapshot()
	sess.billingCallID = callID
	sess.billingCallState = callState
	sess.finalizeBilling = func(ctx context.Context, in execbackend.BillingFinalizationInput) (aipapi.Event, error) {
		return aipapi.Event{}, errors.New("finalizer unsupported for parallel loser")
	}

	sidebandStream := &sidebandEvidenceStream{
		sideband: []aipapi.Event{{
			Kind:          aipapi.EventUsageDelta,
			InputTokens:   55,
			CostNanoUnits: 123456,
			Currency:      "USD",
			CostPresent:   true,
			UsagePresence: aipapi.UsagePresence{InputTokens: true},
			Accounting: aipapi.UsageAccountingMetadata{
				Source:    aipapi.UsageSourceProviderReported,
				Authority: aipapi.UsageAuthorityAuthoritative,
				DedupeKey: "loser-charge-key",
			},
		}},
	}
	testStoreInner(stream, sidebandStream)

	sess.TerminalizeAttempt(context.Background(), IntentParallelLoser, attemptEvidence{
		Command:    sdkterminal.CommandParallelLoser,
		LegOutcome: usageevidence.LegOutcomeLoser,
	})

	mu.Lock()
	defer mu.Unlock()
	if len(recordedLegs) != 1 {
		t.Fatalf("recorded legs = %d, want 1", len(recordedLegs))
	}
	got := recordedLegs[0]
	if got.BLegID != "b-loser-sideband" || got.AttemptSeq != 2 {
		t.Fatalf("lineage mismatch: got %+v", got)
	}
	if got.Outcome != usageevidence.LegOutcomeLoser {
		t.Fatalf("outcome = %v, want %v", got.Outcome, usageevidence.LegOutcomeLoser)
	}
	if !got.Evidence.InputTokens.Present || got.Evidence.InputTokens.Value != 55 {
		t.Fatalf("expected InputTokens=55, got %+v", got.Evidence.InputTokens)
	}
	if !got.Evidence.Cost.Present || got.Evidence.Cost.NanoUnits != 123456 {
		t.Fatalf("expected Cost=123456, got %+v", got.Evidence.Cost)
	}
}
