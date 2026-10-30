package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Task 3.2 RED/GREEN: concurrent response-finished terminal paths compete
// through the real finalization entry point. Exactly one request terminal
// winner performs the finalization/effects; the loser observes terminal loss.
func TestTask32TerminalEconomics_ConcurrentFinalizationHasOneWinner(t *testing.T) {
	var finalizerCalls atomic.Int32
	var legCalls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	executor := &Executor{
		CoreRuntime: CoreRuntime{Backends: map[string]execbackend.Backend{
			"backend": {
				FinalizeBilling: func(context.Context, execbackend.BillingFinalizationInput) (aipapi.Event, error) {
					finalizerCalls.Add(1)
					enteredOnce.Do(func() { close(entered) })
					<-release
					return aipapi.Event{Kind: aipapi.EventUsageDelta, OutputTokens: 3}, nil
				},
			},
		}},
		BillingRuntime: BillingRuntime{BillingLegObserver: BillingLegObserverFunc(func(context.Context, usageevidence.CallLegUsageRecord) {
			legCalls.Add(1)
		})},
	}
	stream := &retryRecvStream{
		responsePipeline: newResponsePipelineForExecutor(executor),
		terminal:         newTurnTerminal(),
		facts:            testRecvTurnFacts(recvTurnFacts{aLegID: "a-finalize"}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{ALegID: "a-finalize", BLegID: "b-finalize", Seq: 1},
			routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
			authorityLifecycle{},
		),
	}
	bindTestRuntimeOwners(stream, executor)
	finish := aipapi.Event{Kind: aipapi.EventResponseFinished}
	results := make(chan error, 2)
	var calls atomic.Int32
	go func() {
		_, _, err := stream.terminal.finalizeResponseFinishedAuthority(context.Background(), finish, stream.facts.terminalFacts(), stream.attempt.snapshot(), stream.responsePipeline)
		calls.Add(1)
		results <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("winner did not reach FinalizeBilling")
	}
	loserDone := make(chan error, 1)
	go func() {
		_, _, err := stream.terminal.finalizeResponseFinishedAuthority(context.Background(), finish, stream.facts.terminalFacts(), stream.attempt.snapshot(), stream.responsePipeline)
		calls.Add(1)
		loserDone <- err
	}()
	select {
	case err := <-loserDone:
		t.Fatalf("terminal loser returned before winner effects completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	results <- <-loserDone
	var nils, losses int
	for range 2 {
		if err := <-results; err == nil {
			nils++
		} else {
			losses++
		}
	}
	if calls.Load() != 2 || nils != 1 || losses != 1 {
		t.Fatalf("concurrent finalization calls=%d nils=%d losses=%d", calls.Load(), nils, losses)
	}
	if finalizerCalls.Load() != 1 || legCalls.Load() != 1 {
		t.Fatalf("economic effects finalizer_calls=%d leg_calls=%d, want one each", finalizerCalls.Load(), legCalls.Load())
	}
}

func TestTask32TerminalEconomics_OldAttemptCallbackRecordsOnlyOldBLeg(t *testing.T) {
	var mu sync.Mutex
	var records []usageevidence.CallLegUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		BillingLegObserver: BillingLegObserverFunc(func(_ context.Context, record usageevidence.CallLegUsageRecord) {
			mu.Lock()
			records = append(records, record)
			mu.Unlock()
		}),
	}}
	stream := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts:    testRecvTurnFacts(recvTurnFacts{aLegID: "a-old"}),
	}
	bindTestRuntimeOwners(stream, executor)
	old := newAttemptSession(attemptSessionInput{
		bleg: b2bua.BLegRecord{ALegID: "a-old", BLegID: "b-old", Seq: 1},
		cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "old-backend", Model: "old-model"}},
	})
	next := newAttemptSession(attemptSessionInput{
		bleg: b2bua.BLegRecord{ALegID: "a-old", BLegID: "b-next", Seq: 2},
		cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "next-backend", Model: "next-model"}},
	})
	testInstallSlot(&stream.attempt, old)
	ready := &readyAttempt{session: next, state: readyStatePrepared}
	if detached, published := stream.attempt.swapIfOpen(ready); !published || detached != old {
		t.Fatalf("attempt swap detached=%p published=%v, want old=%p", detached, published, old)
	}
	callback := func(ctx context.Context) {
		stream.terminal.recordBillingLegForAttempt(ctx, stream.facts.terminalFacts(), old, old.terminalEvidence(), sdkterminal.CommandSwallowedAttempt, aipapi.Event{}, false, stream.facts.billingCallState)
	}
	callback(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(records) != 1 || records[0].BLegID != "b-old" || records[0].BackendID != "old-backend" {
		t.Fatalf("old terminal callback attributed records = %+v", records)
	}
}

func TestTask32TerminalEconomics_OldAttemptCallbackMetersOnlyOldBLeg(t *testing.T) {
	recorder := &recordingMeter{}
	holder := &checkpoint.RequestHolder{}
	for _, blegID := range []string{"b-old-meter", "b-next-meter"} {
		if _, err := holder.StoreBackendIngress(checkpoint.BackendIngressInput{
			Call: aipapi.Call{
				ID:       "request-meter",
				Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
			},
			AttemptID:    blegID,
			BLegID:       blegID,
			CheckpointID: "be-" + blegID,
			StreamID:     "stream-" + blegID,
			Now:          time.Unix(2, 0).UTC(),
		}); err != nil {
			t.Fatalf("StoreBackendIngress(%s): %v", blegID, err)
		}
	}
	executor := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: recorder}}
	stream := &retryRecvStream{
		responsePipeline: newResponsePipelineForExecutor(executor),
		terminal:         newTurnTerminal(),
		facts:            testRecvTurnFacts(recvTurnFacts{traceID: "request-meter"}),
		attempt:          attemptSlot{},
	}
	bindTestRuntimeOwners(stream, executor)
	testInstallSlot(&stream.attempt, newAttemptSession(attemptSessionInput{
		bleg: b2bua.BLegRecord{BLegID: "b-old-meter", Seq: 1},
	}))
	old := stream.attempt.snapshot()
	callback := func(ctx context.Context) error {
		result := testTerminalizeRequestForAttempt(ctx, stream, sdkterminal.CommandClose, old, func(cctx context.Context) error {
			stream.terminal.settleCancellationAuthorityForAttempt(cctx, old, stream.responsePipeline)
			return nil
		})
		return result.Err
	}
	ready := &readyAttempt{
		session: newAttemptSession(attemptSessionInput{
			bleg: b2bua.BLegRecord{BLegID: "b-next-meter", Seq: 2},
		}),
		state: readyStatePrepared,
	}
	stream.attempt.swapIfOpen(ready)
	if old == nil {
		t.Fatal("old attempt snapshot is nil")
	}
	if err := callback(withMeteringHolder(context.Background(), holder)); err != nil {
		t.Fatalf("old terminal callback: %v", err)
	}
	facts := recorder.Facts()
	if len(facts) != 1 {
		t.Fatalf("metering facts=%d want 1", len(facts))
	}
	if got := facts[0].Correlation.BLegID; got != "b-old-meter" {
		t.Fatalf("old terminal callback metered B-leg %q, want b-old-meter", got)
	}
}
