package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestStreamTerminal_EffectError_AdvancesFailedAndWaitersObserve(t *testing.T) {
	t.Parallel()
	term := newStreamTerminal(sdk.ScopeRequest)
	effectErr := errors.New("settle failed")
	entered := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]coreterm.Result, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0] = term.Terminalize(context.Background(), sdk.CommandClose, func() coreterm.AccumulatorSnapshot {
			return coreterm.NewAccumulatorSnapshot([]byte("w"), true)
		}, func(context.Context, coreterm.Outcome) error {
			close(entered)
			<-release
			return effectErr
		})
	}()
	<-entered
	go func() {
		defer wg.Done()
		results[1] = term.Terminalize(context.Background(), sdk.CommandCancel, func() coreterm.AccumulatorSnapshot {
			return coreterm.NewAccumulatorSnapshot([]byte("l"), false)
		}, func(context.Context, coreterm.Outcome) error {
			t.Error("loser must not run effects")
			return nil
		})
	}()
	close(release)
	wg.Wait()

	if term.Owner().State() != sdk.StateFailed {
		t.Fatalf("state=%q want failed", term.Owner().State())
	}
	for i, r := range results {
		if !errors.Is(r.Err, effectErr) {
			t.Fatalf("result[%d] err=%v want %v", i, r.Err, effectErr)
		}
		if r.State != sdk.StateFailed {
			t.Fatalf("result[%d] state=%q", i, r.State)
		}
	}
	if !results[0].Won || results[1].Won {
		t.Fatalf("winner/loser flags: %+v %+v", results[0], results[1])
	}
}

func TestStreamTerminal_EffectPanic_AdvancesFailed(t *testing.T) {
	t.Parallel()
	term := newStreamTerminal(sdk.ScopeRequest)
	r := term.Terminalize(context.Background(), sdk.CommandPartialError, func() coreterm.AccumulatorSnapshot {
		return coreterm.NewAccumulatorSnapshot(nil, false)
	}, func(context.Context, coreterm.Outcome) error {
		panic("effect boom")
	})
	if !r.Won {
		t.Fatalf("%+v", r)
	}
	if term.Owner().State() != sdk.StateFailed {
		t.Fatalf("state=%q want failed", term.Owner().State())
	}
}

func TestStreamTerminal_NestedAttemptSkipped_RequestOnlyCommand(t *testing.T) {
	t.Parallel()
	rs := &retryRecvStream{}
	testAttemptSession(rs)
	installTestTurnTerminal(rs)
	var requestEffects atomic.Int32
	r := testTerminalizeRequest(context.Background(), rs, sdk.CommandFrontendEncoderFailure, func(context.Context) error {
		requestEffects.Add(1)
		return nil
	})
	if !r.Won {
		t.Fatalf("request claim: %+v", r)
	}
	if requestEffects.Load() != 1 {
		t.Fatalf("request effects=%d", requestEffects.Load())
	}
	if testAttemptSession(rs).terminal.Owner().State() != sdk.StateOpen {
		t.Fatalf("attempt must stay open when nested skipped, state=%q", testAttemptSession(rs).terminal.Owner().State())
	}
}

func TestStreamTerminal_NestedAttemptEffectError_PropagatesToRequest(t *testing.T) {
	t.Parallel()
	rs := &retryRecvStream{}
	testAttemptSession(rs)
	installTestTurnTerminal(rs)
	effectErr := errors.New("attempt settle failed")
	r := testTerminalizeRequest(context.Background(), rs, sdk.CommandClose, func(context.Context) error {
		return effectErr
	})
	if !r.Won || !errors.Is(r.Err, effectErr) {
		t.Fatalf("got %+v", r)
	}
	if rs.terminal.requestTerminal().Owner().State() != sdk.StateFailed {
		t.Fatalf("request state=%q", rs.terminal.requestTerminal().Owner().State())
	}
	if testAttemptSession(rs).terminal.Owner().State() != sdk.StateFailed {
		t.Fatalf("attempt state=%q", testAttemptSession(rs).terminal.Owner().State())
	}
}

func TestStreamTerminal_OutputCommittedAfterClaim_AwaitsWinner(t *testing.T) {
	t.Parallel()
	term := newStreamTerminal(sdk.ScopeRequest)
	entered := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	var closeRes, gateRes coreterm.Result

	wg.Go(func() {
		closeRes = term.Terminalize(context.Background(), sdk.CommandClose, func() coreterm.AccumulatorSnapshot {
			return coreterm.NewAccumulatorSnapshot([]byte("close"), true)
		}, func(context.Context, coreterm.Outcome) error {
			close(entered)
			<-release
			return nil
		})
	})
	<-entered
	done := make(chan struct{})
	go func() {
		gateRes = term.Terminalize(context.Background(), sdk.CommandGateReplacement, func() coreterm.AccumulatorSnapshot {
			return coreterm.NewAccumulatorSnapshot([]byte("gate"), true)
		}, func(context.Context, coreterm.Outcome) error {
			t.Error("gate effects must not run")
			return nil
		})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("gate replacement must await winner when another claim exists")
	default:
	}
	close(release)
	wg.Wait()
	<-done
	if closeRes.Won && closeRes.State != sdk.StateReleased {
		t.Fatalf("close result: %+v", closeRes)
	}
	if gateRes.Won {
		t.Fatalf("gate must lose: %+v", gateRes)
	}
	if gateRes.Outcome.Command != sdk.CommandClose {
		t.Fatalf("gate must observe close winner, got %q", gateRes.Outcome.Command)
	}
	if !errors.Is(gateRes.Err, sdk.ErrOutputCommitted) {
		t.Fatalf("gate err=%v want OutputCommitted", gateRes.Err)
	}
}

func TestTerminalOwner_concurrentEffectsOnce(t *testing.T) {
	t.Parallel()
	rs := &retryRecvStream{}
	testAttemptSession(rs)
	var effects atomic.Int32
	var winners atomic.Int32
	const n = 32
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			r := testTerminalizeRequest(context.Background(), rs, sdk.CommandClose, func(context.Context) error {
				effects.Add(1)
				return nil
			})
			if r.Won {
				winners.Add(1)
			}
		})
	}
	wg.Wait()

	if effects.Load() != 1 {
		t.Fatalf("effects=%d want 1", effects.Load())
	}
	if winners.Load() != 1 {
		t.Fatalf("winners=%d want 1", winners.Load())
	}
	req, att := testTerminalOwners(rs)
	req2, att2 := testTerminalOwners(rs)
	if req != req2 || att != att2 {
		t.Fatal("terminal owners must remain stable after init")
	}
	if !req.Owner().State().IsTerminal() || !att.Owner().State().IsTerminal() {
		t.Fatalf("request=%q attempt=%q", req.Owner().State(), att.Owner().State())
	}
}

func TestRetryRecvStream_installAttempt_concurrentWithClose_noDeadlockOnceRequest(t *testing.T) {
	t.Parallel()
	rs := &retryRecvStream{}
	testAttemptSession(rs)
	installTestTurnTerminal(rs)
	var effects atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			<-start
			testInstallSlot(&rs.attempt, newAttemptSession(attemptSessionInput{}))
		})
	}
	for range 16 {
		wg.Go(func() {
			<-start
			_ = testTerminalizeRequest(context.Background(), rs, sdk.CommandClose, func(context.Context) error {
				effects.Add(1)
				return nil
			})
		})
	}
	close(start)
	wg.Wait()
	if effects.Load() != 1 {
		t.Fatalf("request-plane effects=%d want 1", effects.Load())
	}
	req, _ := testTerminalOwners(rs)
	if !req.Owner().State().IsTerminal() {
		t.Fatalf("request state=%q", req.Owner().State())
	}
}
