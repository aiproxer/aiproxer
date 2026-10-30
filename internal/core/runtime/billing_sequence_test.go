package runtime

import (
	"context"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// TestAppendIndependentCallLegRejectsMissingAttemptSequence proves the
// independent call-leg seam refuses to persist a leg whose B2BUA sequence was
// lost (Seq <= 0). No new record may be created with an unknown sequence under
// the v2 contract; legacy NULL-sequence rows remain store-side v1 reads.
func TestAppendIndependentCallLegRejectsMissingAttemptSequence(t *testing.T) {
	var mu sync.Mutex
	appended := 0
	executor := &Executor{BillingRuntime: BillingRuntime{
		TerminalUsageSink: testTerminalSink{appendLeg: func(_ context.Context, record usageevidence.CallLegUsageRecord) error {
			mu.Lock()
			appended++
			mu.Unlock()
			return nil
		}},
	}}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := usageevidence.CallLegUsageRecord{
		ALegID: "a-1", BLegID: "b-no-seq", AttemptSeq: 0,
		BackendID: "backend", ProviderID: "provider", ModelID: "model",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: usageevidence.LegOutcomeFailed, Surfaced: usageevidence.SurfacedNo,
	}
	executor.appendIndependentCallLeg(context.Background(), callID, leg)
	mu.Lock()
	got := appended
	mu.Unlock()
	if got != 0 {
		t.Fatalf("appends with missing attempt sequence = %d, want 0 (fail closed)", got)
	}

	leg.AttemptSeq = 7
	executor.appendIndependentCallLeg(context.Background(), callID, leg)
	mu.Lock()
	got = appended
	mu.Unlock()
	if got != 1 {
		t.Fatalf("appends with known sequence = %d, want 1", got)
	}
}

// TestExecutorBillingLegProducersCarryExactB2BUASequence drives every terminal
// leg producer seam and proves the exact b2bua.BLegRecord.Seq reaches the
// durable CallLegUsageRecord.AttemptSeq untouched: opened winner, never-started,
// failed-open/canceled, parallel loser, and swallowed producers all funnel
// through the independent append seam.
func TestExecutorBillingLegProducersCarryExactB2BUASequence(t *testing.T) {
	var mu sync.Mutex
	seqByBLeg := map[string]int{}
	executor := &Executor{BillingRuntime: BillingRuntime{
		TerminalUsageSink: testTerminalSink{appendLeg: func(_ context.Context, record usageevidence.CallLegUsageRecord) error {
			sealed, err := record.Seal()
			if err != nil {
				return err
			}
			if sealed.AttemptSeq <= 0 {
				t.Errorf("producer %q appended non-positive AttemptSeq %d", sealed.BLegID, sealed.AttemptSeq)
			}
			mu.Lock()
			seqByBLeg[sealed.BLegID] = sealed.AttemptSeq
			mu.Unlock()
			return nil
		}},
	}}
	now := time.Unix(200, 0).UTC()
	primary := func(backend, model string) routing.Primary {
		return routing.Primary{Backend: backend, Model: model}
	}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state := newBillingCallState(callID)

	// 1. Never-started producer (open never began; terminal record still uses
	// the allocated sequence).
	executor.appendIndependentTerminalLeg(ctx, state, "a-1",
		b2bua.BLegRecord{BLegID: "b_b3e5f2c1", ALegID: "a-1", Seq: 3},
		primary("backend", "model"), now, now, usageevidence.LegOutcomeNeverStarted)

	// 2. Open failure / canceled producer.
	executor.appendPostOpenTerminalLeg(ctx, state, "a-1",
		b2bua.BLegRecord{BLegID: "b_9a2f1e8d", ALegID: "a-1", Seq: 4},
		primary("backend", "model"), now, now)

	// 3. Opened winner producer (recordBillingLeg path).
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:           "a-1",
			billingCallID:    callID,
			billingCallState: state,
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b_7d6c5b4a", ALegID: "a-1", Seq: 5}, routing.AttemptCandidate{Primary: primary("backend", "model")}, authorityLifecycle{}),
	}
	bindTestRuntimeOwners(stream, executor)
	stream.terminal.recordBillingLegForAttempt(ctx, stream.facts.terminalFacts(), stream.attempt.snapshot(), stream.attempt.require().terminalEvidence(), sdkterminal.CommandNormalFinish, aipapi.Event{}, true, stream.facts.billingCallState)

	// 4. Parallel loser producer (also covers the parallel winner path: the
	// same reporting seam runs for both with distinct allocated sequences).
	parallel := &parallelLeg{
		billingCallState: state,
		bleg:             b2bua.BLegRecord{BLegID: "b_5c4b3a2e", ALegID: "a-1", Seq: 6},
		cand:             routing.AttemptCandidate{Primary: primary("backend", "model")},
		startedAt:        now,
	}
	executor.recordParallelBillingLeg(ctx, parallel, aipapi.Event{}, sdkterminal.CommandParallelLoser, false)

	// 5. Swallowed producer (recordBillingLeg with the swallowed command uses
	// the attempt's own sequence).
	swallowed := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:           "a-1",
			billingCallID:    callID,
			billingCallState: state,
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b_2a1f0e9d", ALegID: "a-1", Seq: 7}, routing.AttemptCandidate{Primary: primary("backend", "model")}, authorityLifecycle{}),
	}
	bindTestRuntimeOwners(swallowed, executor)
	swallowed.terminal.recordBillingLegForAttempt(ctx, swallowed.facts.terminalFacts(), swallowed.attempt.snapshot(), swallowed.attempt.require().terminalEvidence(), sdkterminal.CommandSwallowedAttempt, aipapi.Event{}, false, swallowed.facts.billingCallState)

	mu.Lock()
	got := maps.Clone(seqByBLeg)
	mu.Unlock()

	want := map[string]int{
		"b_b3e5f2c1": 3,
		"b_9a2f1e8d": 4,
		"b_7d6c5b4a": 5,
		"b_5c4b3a2e": 6,
		"b_2a1f0e9d": 7,
	}
	for bLegID, wantSeq := range want {
		if got[bLegID] != wantSeq {
			t.Errorf("B-leg %q AttemptSeq = %d, want exact b2bua sequence %d (all=%v)", bLegID, got[bLegID], wantSeq, got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("producer legs = %v, want %v", got, want)
	}
}
