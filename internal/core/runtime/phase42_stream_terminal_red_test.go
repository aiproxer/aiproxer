package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Phase 4.2 RED/GREEN integration: Recv/Close and major exits share one terminal
// owner per request/attempt (requirements 7.1–7.8, 13.4, 13.7; D8, D13, D17).

type blockUntilCancelStream struct {
	entered chan struct{}
	mu      sync.Mutex
	cancel  context.CancelFunc
}

func (s *blockUntilCancelStream) Recv(ctx context.Context) (aipapi.Event, error) {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	return aipapi.Event{}, ctx.Err()
}

func (s *blockUntilCancelStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return aipapi.CancelResult{}
}

func (s *blockUntilCancelStream) Close() error {
	s.Cancel(context.Background(), aipapi.CancelCause{})
	return nil
}

func TestPhase42_RecvCloseRace_SingleSettlement(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-recv-close",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	state := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}
	cand := authorityCandidate()
	entered := make(chan struct{}, 1)
	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{ID: "req-recv-close", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-recv-close",
			aLegID:   aLegID,
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-recv-close", Seq: 1}, cand, testAuthorityLifecycle(ex, state, cand), newAttemptAccountingTracker(time.Unix(1, 0))),
		responsePipeline: &responsePipeline{seenEvents: []aipapi.Event{{
			Kind: aipapi.EventUsageDelta, InputTokens: 2, OutputTokens: 3, TotalTokens: 5,
		}}},
	}
	testStoreInner(rs, &blockUntilCancelStream{entered: entered})
	rs.terminal.markCommitted(rs.attempt.snapshot())
	installTestTurnTerminal(rs)

	ctx := t.Context()

	var recvErr error
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		_, recvErr = rs.Recv(ctx)
	})
	wg.Go(func() {
		<-start
		<-entered
		_ = rs.Close()
	})
	close(start)
	wg.Wait()

	if !rs.terminal.finished() {
		t.Fatal("stream must be finished after Close/Recv race")
	}
	if rs.terminal == nil || rs.terminal.requestTerminal() == nil || rs.terminal.requestTerminal().Owner() == nil {
		t.Fatal("request terminal owner must be installed")
	}
	if !rs.terminal.requestTerminal().Owner().State().IsTerminal() {
		t.Fatalf("request terminal state=%q", rs.terminal.requestTerminal().Owner().State())
	}
	// One settle (cancellation/final) — never two competing terminal settlements.
	settles := auth.settleCalls.Load()
	if settles != 1 {
		t.Fatalf("settleCalls=%d want 1 (recv/close must not double-settle); recvErr=%v", settles, recvErr)
	}
	if out, ok := rs.terminal.requestTerminal().Owner().Outcome(); !ok || out.Snapshot.Bytes() == nil {
		t.Fatal("winner must publish accumulator snapshot")
	}
}

func TestPhase42_MajorExitCommands_ClaimOnce(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		cmd   sdk.Command
		scope sdk.Scope
	}{
		{"normal_finish", sdk.CommandNormalFinish, sdk.ScopeRequest},
		{"partial_error", sdk.CommandPartialError, sdk.ScopeRequest},
		{"cancel", sdk.CommandCancel, sdk.ScopeRequest},
		{"close", sdk.CommandClose, sdk.ScopeRequest},
		{"timeout", sdk.CommandTimeout, sdk.ScopeRequest},
		{"eof", sdk.CommandEOF, sdk.ScopeRequest},
		{"panic", sdk.CommandPanic, sdk.ScopeRequest},
		{"gate_replacement", sdk.CommandGateReplacement, sdk.ScopeRequest},
		{"frontend_encoder", sdk.CommandFrontendEncoderFailure, sdk.ScopeRequest},
		{"parallel_loser", sdk.CommandParallelLoser, sdk.ScopeAttempt},
		{"swallowed", sdk.CommandSwallowedAttempt, sdk.ScopeAttempt},
		{"pre_backend", sdk.CommandPreBackendDenial, sdk.ScopeAttempt},
		{"backend_open", sdk.CommandBackendOpenFailure, sdk.ScopeAttempt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var effects atomic.Int32
			term := newStreamTerminal(tc.scope)
			snap := coretermSnapshot(tc.cmd == sdk.CommandGateReplacement)
			r1 := term.Terminalize(context.Background(), tc.cmd, func() coreterm.AccumulatorSnapshot {
				return snap
			}, func(context.Context, coreterm.Outcome) error {
				effects.Add(1)
				return nil
			})
			if tc.cmd == sdk.CommandGateReplacement {
				if r1.Won || !errors.Is(r1.Err, sdk.ErrOutputCommitted) {
					t.Fatalf("gate with committed output: %+v", r1)
				}
				return
			}
			if !r1.Won {
				t.Fatalf("first claim: %+v", r1)
			}
			r2 := term.Terminalize(context.Background(), sdk.CommandClose, func() coreterm.AccumulatorSnapshot {
				return coreterm.NewAccumulatorSnapshot([]byte("x"), false)
			}, func(context.Context, coreterm.Outcome) error {
				effects.Add(1)
				return nil
			})
			if r2.Won || effects.Load() != 1 {
				t.Fatalf("second Won=%v effects=%d r2=%+v", r2.Won, effects.Load(), r2)
			}
		})
	}
}

func coretermSnapshot(committed bool) coreterm.AccumulatorSnapshot {
	return coreterm.NewAccumulatorSnapshot([]byte("snap"), committed)
}
