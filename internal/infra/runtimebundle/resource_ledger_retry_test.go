package runtimebundle_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
)

// Task 7.2 focused ResourceLedger / GenerationBundle / CandidateRuntime contracts.

func TestResourceLedger_Close_RetryOnlyFailedEntries(t *testing.T) {
	t.Parallel()
	var okCalls, flakyCalls atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("ok", runtimebundle.PhaseClose, func() error {
		okCalls.Add(1)
		return nil
	})
	ledger.AddClose("flaky", runtimebundle.PhaseClose, func() error {
		if flakyCalls.Add(1) == 1 {
			return errors.New("temp")
		}
		return nil
	})
	if err := ledger.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(context.Background()); err == nil {
		t.Fatal("first Close must fail")
	}
	if okCalls.Load() != 1 || flakyCalls.Load() != 1 {
		t.Fatalf("after fail ok=%d flaky=%d", okCalls.Load(), flakyCalls.Load())
	}
	if err := ledger.Close(context.Background()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if okCalls.Load() != 1 {
		t.Fatalf("successful sibling re-ran: ok=%d", okCalls.Load())
	}
	if flakyCalls.Load() != 2 {
		t.Fatalf("flaky retries=%d want 2", flakyCalls.Load())
	}
	if err := ledger.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if okCalls.Load() != 1 || flakyCalls.Load() != 2 {
		t.Fatalf("post-success close re-ran ok=%d flaky=%d", okCalls.Load(), flakyCalls.Load())
	}
}

func TestResourceLedger_Close_PanicThenSuccessRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("boom", runtimebundle.PhaseClose, func() error {
		if calls.Add(1) == 1 {
			panic("cleanup boom")
		}
		return nil
	})
	err1 := ledger.Close(context.Background())
	if err1 == nil {
		t.Fatal("panic must surface as error")
	}
	if err := ledger.Close(context.Background()); err != nil {
		t.Fatalf("retry after panic: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d want 2", calls.Load())
	}
}

func TestResourceLedger_Rollback_ExactOnceTerminal(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	fail := errors.New("rollback-fail")
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("a", runtimebundle.PhaseClose, func() error {
		calls.Add(1)
		return fail
	})
	err1 := ledger.Rollback(context.Background())
	if !errors.Is(err1, fail) {
		t.Fatalf("rollback: %v", err1)
	}
	err2 := ledger.Rollback(context.Background())
	if !errors.Is(err2, fail) {
		t.Fatalf("cached rollback: %v", err2)
	}
	if calls.Load() != 1 {
		t.Fatalf("terminal rollback re-ran calls=%d", calls.Load())
	}
}

func TestResourceLedger_QuiesceClose_ConcurrentShareAttempt(t *testing.T) {
	t.Parallel()
	var quiesces, closes atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error {
		quiesces.Add(1)
		enterOnce.Do(func() { close(entered) })
		<-release
		return nil
	})
	ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		closes.Add(1)
		return nil
	})

	errCh := make(chan error, 2)
	go func() { errCh <- ledger.Quiesce(context.Background()) }()
	<-entered
	go func() { errCh <- ledger.Close(context.Background()) }()
	// Second Quiesce must share/wait rather than overlapping PhaseQuiesce.
	go func() { errCh <- ledger.Quiesce(context.Background()) }()
	close(release)
	for range 3 {
		if err := <-errCh; err != nil {
			t.Fatalf("lifecycle: %v", err)
		}
	}
	if quiesces.Load() != 1 || closes.Load() != 1 {
		t.Fatalf("quiesce=%d close=%d want 1/1", quiesces.Load(), closes.Load())
	}
}

func TestResourceLedger_LateRegistration_AfterCloseImmediateOnce(t *testing.T) {
	t.Parallel()
	var early, late atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("early", runtimebundle.PhaseClose, func() error {
		early.Add(1)
		return nil
	})
	if err := ledger.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	ledger.AddClose("late", runtimebundle.PhaseClose, func() error {
		late.Add(1)
		return nil
	})
	if late.Load() != 1 {
		t.Fatalf("late registration must clean immediately, late=%d", late.Load())
	}
	if late.Load() != 1 || early.Load() != 1 {
		t.Fatalf("double clean early=%d late=%d", early.Load(), late.Load())
	}
	if ledger.Len() != 1 {
		t.Fatalf("late must not reopen sealed ledger len=%d", ledger.Len())
	}
}

func TestResourceLedger_LateQuiesceRegistration_AfterQuiesceImmediate(t *testing.T) {
	t.Parallel()
	var late atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error { return nil })
	if err := ledger.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	ledger.AddClose("late-worker", runtimebundle.PhaseQuiesce, func() error {
		late.Add(1)
		return nil
	})
	if late.Load() != 1 {
		t.Fatalf("PhaseQuiesce after quiesce must run immediately, late=%d", late.Load())
	}
	if err := ledger.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if late.Load() != 1 {
		t.Fatalf("late quiesce re-ran on close late=%d", late.Load())
	}
}

func TestCandidateRuntime_CloseWins_TransferNilNoBundle(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("be", runtimebundle.PhaseClose, func() error {
		closes.Add(1)
		enterOnce.Do(func() { close(entered) })
		<-release
		return nil
	})
	cand := runtimebundle.NewCandidateRuntimeForTest(ledger)

	errCh := make(chan error, 1)
	go func() { errCh <- cand.Close() }()
	<-entered
	// Close holds lifecycle claim; transfer must be denied (nil), not receive a cleaned ledger.
	transferred := runtimebundle.TransferLedgerOwnershipForTest(cand)
	close(release)
	if err := <-errCh; err != nil {
		t.Fatalf("close: %v", err)
	}
	if transferred != nil {
		t.Fatal("Close-won transfer must return nil ledger")
	}
	if closes.Load() != 1 {
		t.Fatalf("closes=%d want 1", closes.Load())
	}
	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("candidate re-cleaned closes=%d", closes.Load())
	}
}

func TestCandidateRuntime_TransferWins_CandidateNoopGenerationCleans(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("be", runtimebundle.PhaseClose, func() error {
		closes.Add(1)
		return nil
	})
	cand := runtimebundle.NewCandidateRuntimeForTest(ledger)
	transferred := runtimebundle.TransferLedgerOwnershipForTest(cand)
	if transferred == nil {
		t.Fatal("transfer")
	}
	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cand.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 0 {
		t.Fatalf("candidate closed transferred resources closes=%d", closes.Load())
	}
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(transferred)
	if err := bundle.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("generation closes=%d", closes.Load())
	}
}

func TestCandidateRuntime_QuiesceWins_TransferDeniedCandidateCloseFinishes(t *testing.T) {
	t.Parallel()
	var quiesces, closes atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error {
		quiesces.Add(1)
		enterOnce.Do(func() { close(entered) })
		<-release
		return nil
	})
	ledger.AddClose("be", runtimebundle.PhaseClose, func() error {
		closes.Add(1)
		return nil
	})
	cand := runtimebundle.NewCandidateRuntimeForTest(ledger)

	errCh := make(chan error, 1)
	go func() { errCh <- cand.Quiesce(context.Background()) }()
	<-entered
	transferred := runtimebundle.TransferLedgerOwnershipForTest(cand)
	close(release)
	if err := <-errCh; err != nil {
		t.Fatalf("quiesce: %v", err)
	}
	if transferred != nil {
		t.Fatal("Quiesce-won transfer must return nil")
	}
	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}
	if quiesces.Load() != 1 || closes.Load() != 1 {
		t.Fatalf("quiesce=%d close=%d want 1/1", quiesces.Load(), closes.Load())
	}
}

func TestCandidateRuntime_TransferVsLifecycleRace_NeverReturnsCleanedLedger(t *testing.T) {
	t.Parallel()
	for range 128 {
		var closes atomic.Int32
		ledger := runtimebundle.NewResourceLedger()
		ledger.AddClose("be", runtimebundle.PhaseClose, func() error {
			closes.Add(1)
			return nil
		})
		cand := runtimebundle.NewCandidateRuntimeForTest(ledger)

		start := make(chan struct{})
		errCh := make(chan error, 1)
		xferCh := make(chan *runtimebundle.ResourceLedger, 1)
		go func() {
			<-start
			errCh <- cand.Close()
		}()
		go func() {
			<-start
			xferCh <- runtimebundle.TransferLedgerOwnershipForTest(cand)
		}()
		close(start)
		err := <-errCh
		transferred := <-xferCh
		if err != nil {
			t.Fatalf("close: %v", err)
		}

		switch {
		case transferred == nil:
			// Lifecycle claimed: cleanup exactly once on the candidate path.
			if closes.Load() != 1 {
				t.Fatalf("lifecycle-won closes=%d want 1", closes.Load())
			}
		case closes.Load() == 0:
			// Transfer claimed: generation must clean once; ledger is not cleaned yet.
			bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(transferred)
			if err := bundle.Close(); err != nil {
				t.Fatal(err)
			}
			if closes.Load() != 1 {
				t.Fatalf("generation closes=%d", closes.Load())
			}
		default:
			t.Fatalf("transfer returned non-nil already-cleaned ledger closes=%d", closes.Load())
		}

		if err := cand.Close(); err != nil {
			t.Fatal(err)
		}
		if err := cand.Quiesce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if closes.Load() != 1 {
			t.Fatalf("post-race re-clean closes=%d", closes.Load())
		}
	}
}

func TestCandidateRuntime_TransferWinsOverLaterClose(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("be", runtimebundle.PhaseClose, func() error {
		closes.Add(1)
		return nil
	})
	cand := runtimebundle.NewCandidateRuntimeForTest(ledger)
	transferred := runtimebundle.TransferLedgerOwnershipForTest(cand)
	if transferred == nil {
		t.Fatal("transfer")
	}
	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 0 {
		t.Fatalf("candidate closed transferred resources closes=%d", closes.Load())
	}
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(transferred)
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("generation closes=%d", closes.Load())
	}
}

func TestResourceLedger_RollbackThenPrepare_NoRestart(t *testing.T) {
	t.Parallel()
	var starts, stops atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddAction(
		"life", runtimebundle.PhasePrepare,
		func(context.Context) error { starts.Add(1); return nil },
		func(context.Context) error { stops.Add(1); return nil },
	)
	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Prepare(context.Background()); err != nil {
		t.Fatalf("prepare after rollback: %v", err)
	}
	if starts.Load() != 0 {
		t.Fatalf("Prepare restarted after rollback starts=%d", starts.Load())
	}
	if stops.Load() != 0 {
		t.Fatalf("unstarted stop ran stops=%d", stops.Load())
	}
}

func TestResourceLedger_CloseThenActivate_NoRestart(t *testing.T) {
	t.Parallel()
	var starts atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddAction(
		"commit", runtimebundle.PhaseActivate,
		func(context.Context) error { starts.Add(1); return nil },
		nil,
	)
	if err := ledger.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Activate(context.Background()); err != nil {
		t.Fatalf("activate after close: %v", err)
	}
	if starts.Load() != 0 {
		t.Fatalf("Activate restarted after close starts=%d", starts.Load())
	}
}

func TestResourceLedger_QuiesceThenPrepare_NoRestart(t *testing.T) {
	t.Parallel()
	var starts, quiesces atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddAction(
		"life", runtimebundle.PhasePrepare,
		func(context.Context) error { starts.Add(1); return nil },
		func(context.Context) error { return nil },
	)
	ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error {
		quiesces.Add(1)
		return nil
	})
	if err := ledger.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Prepare(context.Background()); err != nil {
		t.Fatalf("prepare after quiesce: %v", err)
	}
	if starts.Load() != 0 {
		t.Fatalf("Prepare restarted after quiesce starts=%d", starts.Load())
	}
	if quiesces.Load() != 1 {
		t.Fatalf("quiesces=%d", quiesces.Load())
	}
	if err := ledger.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestResourceLedger_PrepareActivateVsTerminalRace_NoStartAfterTerminal(t *testing.T) {
	t.Parallel()
	for range 64 {
		var starts, stops atomic.Int32
		entered := make(chan struct{})
		release := make(chan struct{})
		var enterOnce sync.Once
		ledger := runtimebundle.NewResourceLedger()
		ledger.AddAction(
			"life", runtimebundle.PhasePrepare,
			func(context.Context) error { starts.Add(1); return nil },
			func(context.Context) error { stops.Add(1); return nil },
		)
		ledger.AddClose("be", runtimebundle.PhaseClose, func() error {
			enterOnce.Do(func() { close(entered) })
			<-release
			return nil
		})

		errCh := make(chan error, 3)
		go func() { errCh <- ledger.Close(context.Background()) }()
		<-entered
		go func() { errCh <- ledger.Prepare(context.Background()) }()
		go func() { errCh <- ledger.Activate(context.Background()) }()
		close(release)
		for range 3 {
			if err := <-errCh; err != nil {
				t.Fatalf("lifecycle: %v", err)
			}
		}
		if starts.Load() != 0 {
			t.Fatalf("starts after terminal ownership starts=%d", starts.Load())
		}
		if stops.Load() != 0 {
			t.Fatalf("unstarted stop ran stops=%d", stops.Load())
		}
	}
}

func TestResourceLedger_NormalPrepareActivateQuiesceClose(t *testing.T) {
	t.Parallel()
	var starts, activates, quiesces, closes atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddAction(
		"life", runtimebundle.PhasePrepare,
		func(context.Context) error { starts.Add(1); return nil },
		func(context.Context) error { closes.Add(1); return nil },
	)
	ledger.AddAction(
		"commit", runtimebundle.PhaseActivate,
		func(context.Context) error { activates.Add(1); return nil },
		nil,
	)
	ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error {
		quiesces.Add(1)
		return nil
	})
	if err := ledger.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 || activates.Load() != 1 || quiesces.Load() != 1 || closes.Load() != 1 {
		t.Fatalf("starts=%d act=%d q=%d close=%d", starts.Load(), activates.Load(), quiesces.Load(), closes.Load())
	}
}

func TestBackendInstance_Close_IdleBeforeStopOrdering(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var order []string
	track := func(name string) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}
	inst := runtimebundle.WrapBackendInstance(execbackend.Backend{
		Close: func() error { track("close"); return nil },
	}, runtimebundle.OptionalBackendHooks{
		CleanupIdleTransports: func(context.Context) error { track("idle"); return nil },
		Stop:                  func(context.Context) error { track("stop"); return nil },
		Start:                 func(context.Context) error { return nil },
	})
	if err := inst.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	want := []string{"idle", "stop", "close"}
	if len(got) != len(want) {
		t.Fatalf("order=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order=%v want %v", got, want)
		}
	}
}
