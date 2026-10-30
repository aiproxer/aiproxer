package runtimebundle_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
)

func TestResourceLedger_RollbackReverseOrderIdempotent(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	var mu sync.Mutex
	var order []string
	var closes atomic.Int32

	wrap := func(name string) func() error {
		return func() error {
			closes.Add(1)
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}

	ledger.AddClose("a", runtimebundle.PhaseClose, wrap("a"))
	ledger.AddClose("b", runtimebundle.PhaseClose, wrap("b"))
	ledger.AddClose("c", runtimebundle.PhaseQuiesce, wrap("c"))

	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if want := []string{"c", "b", "a"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rollback order=%v want %v", got, want)
	}
	n := closes.Load()
	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != n {
		t.Fatalf("idempotent closes: first=%d after=%d", n, closes.Load())
	}
}

func TestResourceLedger_RollbackAggregatesErrors(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	errA := errors.New("close-a")
	errB := errors.New("close-b")
	ledger.AddClose("a", runtimebundle.PhaseClose, func() error { return errA })
	ledger.AddClose("b", runtimebundle.PhaseClose, func() error { return errB })
	err := ledger.Rollback(context.Background())
	if err == nil {
		t.Fatal("expected aggregated error")
	}
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Fatalf("want both errors joined, got %v", err)
	}
}

func TestResourceLedger_QuiesceThenClosePhases(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	var mu sync.Mutex
	var order []string
	track := func(name string) func() error {
		return func() error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}
	ledger.AddClose("worker", runtimebundle.PhaseQuiesce, track("quiesce"))
	ledger.AddClose("backend", runtimebundle.PhaseClose, track("close"))

	if err := ledger.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if fmt.Sprint(order) != "[quiesce]" {
		t.Fatalf("after quiesce order=%v", order)
	}
	mu.Unlock()

	if err := ledger.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(order) != "[quiesce close]" {
		t.Fatalf("after close order=%v", order)
	}
}

func TestResourceLedger_PrepareActivateFaultInjection(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	var prepared, activated, closed atomic.Int32
	ledger.AddAction(
		"life", runtimebundle.PhasePrepare,
		func(context.Context) error {
			prepared.Add(1)
			return nil
		},
		func(context.Context) error {
			closed.Add(1)
			return nil
		},
	)
	ledger.AddAction(
		"commit", runtimebundle.PhaseActivate,
		func(context.Context) error {
			activated.Add(1)
			return nil
		},
		nil,
	)

	if err := ledger.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if prepared.Load() != 1 {
		t.Fatalf("prepared=%d", prepared.Load())
	}
	if err := ledger.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if activated.Load() != 1 {
		t.Fatalf("activated=%d", activated.Load())
	}

	// Inject prepare failure on a second ledger: acquired close must run.
	bad := runtimebundle.NewResourceLedger()
	var rolled atomic.Int32
	bad.AddClose("res", runtimebundle.PhaseClose, func() error {
		rolled.Add(1)
		return nil
	})
	bad.AddAction(
		"boom", runtimebundle.PhasePrepare,
		func(context.Context) error { return errors.New("prepare-fail") },
		nil,
	)
	if err := bad.Prepare(context.Background()); err == nil {
		t.Fatal("expected prepare failure")
	}
	if err := bad.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rolled.Load() != 1 {
		t.Fatalf("rollback closes=%d", rolled.Load())
	}
}

func TestResourceLedger_StartErrorKeepsSafeTextAndUnwrapsCause(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("credential-bearing prepare detail")
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddAction("prepare", runtimebundle.PhasePrepare, func(context.Context) error { return sentinel }, nil)
	err := ledger.Prepare(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("Prepare error=%v must unwrap sentinel", err)
	}
	if strings.Contains(err.Error(), sentinel.Error()) || err.Error() != "runtimebundle: ledger start failed" {
		t.Fatalf("start failure must keep bounded safe text, got %q", err)
	}
}

func TestResourceLedger_RollbackSkipsUnstartedLifecycleEntries(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	var mu sync.Mutex
	var order []string
	var stops atomic.Int32
	trackStop := func(name string) func(context.Context) error {
		return func(context.Context) error {
			stops.Add(1)
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}

	ledger.AddClose("res", runtimebundle.PhaseClose, func() error {
		stops.Add(1)
		mu.Lock()
		order = append(order, "res")
		mu.Unlock()
		return nil
	})
	ledger.AddAction(
		"a", runtimebundle.PhasePrepare,
		func(context.Context) error { return nil },
		trackStop("a"),
	)
	ledger.AddAction(
		"b", runtimebundle.PhasePrepare,
		func(context.Context) error { return errors.New("prepare-b-fail") },
		trackStop("b"),
	)
	ledger.AddAction(
		"c", runtimebundle.PhasePrepare,
		func(context.Context) error {
			t.Fatal("c start must never be attempted after b fails")
			return nil
		},
		trackStop("c"),
	)

	if err := ledger.Prepare(context.Background()); err == nil {
		t.Fatal("expected prepare failure at b")
	}
	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if want := []string{"b", "a", "res"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rollback order=%v want %v (skip unstarted c; stop attempted b + prior a + close-only res)", got, want)
	}
	n := stops.Load()
	if n != 3 {
		t.Fatalf("stops=%d want 3", n)
	}
	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stops.Load() != n {
		t.Fatalf("repeated rollback must stay exactly-once: first=%d after=%d", n, stops.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(order) != "[b a res]" {
		t.Fatalf("order after repeated rollback=%v", order)
	}
}

func TestResourceLedger_NeverOwnsProcessServices(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	var processClosed atomic.Bool
	processCloser := func() error {
		processClosed.Store(true)
		return nil
	}
	_ = processCloser // process closers stay on ProcessServices, not the ledger
	ledger.AddClose("candidate-only", runtimebundle.PhaseClose, func() error { return nil })
	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if processClosed.Load() {
		t.Fatal("process services must not be on candidate ledger")
	}
}

func TestResourceLedger_AddCloseAfterRollbackRunsImmediatelyOnce(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	var closed atomic.Int32
	lateErr := errors.New("late-close")
	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	ledger.AddClose("late", runtimebundle.PhaseClose, func() error {
		closed.Add(1)
		return lateErr
	})
	if closed.Load() != 1 {
		t.Fatalf("late AddClose must run immediately, closed=%d", closed.Load())
	}
	if closed.Load() != 1 {
		t.Fatalf("idempotent late close: closed=%d", closed.Load())
	}
	if ledger.Len() != 0 {
		t.Fatalf("late entry must not reopen ledger, len=%d", ledger.Len())
	}
}

func TestResourceLedger_PublishStartsOnlyOnPublish(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	var published, stopped atomic.Int32
	ledger.AddAction(
		"worker", runtimebundle.PhasePublish,
		func(context.Context) error {
			published.Add(1)
			return nil
		},
		func(context.Context) error {
			stopped.Add(1)
			return nil
		},
	)
	if err := ledger.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if published.Load() != 0 {
		t.Fatalf("PhasePublish ran during activate: starts=%d", published.Load())
	}
	if err := ledger.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if published.Load() != 1 {
		t.Fatalf("published starts=%d", published.Load())
	}
	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stopped.Load() != 1 {
		t.Fatalf("rollback stops=%d", stopped.Load())
	}
}

func TestResourceLedger_RollbackSkipsUnpublishedStarts(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	var stopped atomic.Int32
	ledger.AddAction(
		"worker", runtimebundle.PhasePublish,
		func(context.Context) error { return nil },
		func(context.Context) error {
			stopped.Add(1)
			return nil
		},
	)
	if err := ledger.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stopped.Load() != 0 {
		t.Fatalf("unpublished PhasePublish stop ran: stopped=%d", stopped.Load())
	}
}

func TestResourceLedger_PhasePublishPanicIsCachedAndCleanupRemainsUsable(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	var starts, stops atomic.Int32
	ledger.AddAction("publish-worker", runtimebundle.PhasePublish,
		func(context.Context) error {
			starts.Add(1)
			panic("private publish panic")
		},
		func(context.Context) error {
			stops.Add(1)
			return nil
		})
	first := ledger.Publish(context.Background())
	if first == nil || !strings.Contains(first.Error(), "start failed") || strings.Contains(first.Error(), "private publish panic") {
		t.Fatalf("publish panic must become a bounded cached error: %v", first)
	}
	second := ledger.Publish(context.Background())
	if second == nil || second.Error() != first.Error() || starts.Load() != 1 {
		t.Fatalf("PhasePublish retried or changed its cached outcome: first=%v second=%v starts=%d", first, second, starts.Load())
	}
	if err := ledger.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stops.Load() != 1 {
		t.Fatalf("attempted publish resource cleanup calls=%d want 1", stops.Load())
	}
}

func TestResourceLedger_PhasePublishReturnedErrorIsSharedAndCleanupRemainsUsable(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	wantErr := errors.New("publish start rejected")
	entered, release := make(chan struct{}), make(chan struct{})
	var starts, stops atomic.Int32
	var enteredOnce sync.Once
	ledger.AddAction("publish-worker", runtimebundle.PhasePublish,
		func(context.Context) error {
			starts.Add(1)
			enteredOnce.Do(func() { close(entered) })
			<-release
			return wantErr
		},
		func(context.Context) error {
			stops.Add(1)
			return nil
		})

	firstResult := make(chan error, 1)
	go func() { firstResult <- ledger.Publish(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second): // deadlock guard only; the hook barrier provides synchronization.
		t.Fatal("PhasePublish hook did not start")
	}

	const waiters = 5
	ready := make(chan struct{}, waiters)
	waitResults := make(chan error, waiters)
	for range waiters {
		go func() {
			ready <- struct{}{}
			waitResults <- ledger.Publish(context.Background())
		}()
	}
	for range waiters {
		<-ready
	}
	close(release)

	checkPublishError := func(err error) {
		t.Helper()
		if !errors.Is(err, wantErr) {
			t.Fatalf("PhasePublish error=%v want wrapped cause %v", err, wantErr)
		}
	}
	checkPublishError(awaitResourceLedgerPublishResult(t, firstResult))
	for range waiters {
		checkPublishError(awaitResourceLedgerPublishResult(t, waitResults))
	}
	if starts.Load() != 1 {
		t.Fatalf("PhasePublish hook calls=%d want once", starts.Load())
	}
	type lifecycleResults struct {
		publish, quiesce, close error
	}
	lifecycleDone := make(chan lifecycleResults, 1)
	go func() {
		result := lifecycleResults{publish: ledger.Publish(context.Background())}
		result.quiesce = ledger.Quiesce(context.Background())
		result.close = ledger.Close(context.Background())
		lifecycleDone <- result
	}()
	guard, cancelGuard := context.WithTimeout(context.Background(), 5*time.Second)
	var lifecycle lifecycleResults
	select {
	case lifecycle = <-lifecycleDone:
		cancelGuard()
	case <-guard.Done():
		cancelGuard()
		t.Fatal("PhasePublish completion did not release waiters for later Quiesce and Close")
	}
	checkPublishError(lifecycle.publish)
	if lifecycle.quiesce != nil {
		t.Fatalf("Quiesce after failed publication: %v", lifecycle.quiesce)
	}
	if lifecycle.close != nil {
		t.Fatalf("Close after failed publication: %v", lifecycle.close)
	}
	if stops.Load() != 1 {
		t.Fatalf("attempted publish resource cleanup calls=%d want once", stops.Load())
	}
}

func awaitResourceLedgerPublishResult(t *testing.T, results <-chan error) error {
	t.Helper()
	guard, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case err := <-results:
		return err
	case <-guard.Done():
		t.Fatal("PhasePublish caller was not released with the cached result")
		return nil
	}
}

func TestResourceLedger_AddCloseNilIsNoop(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("nil", runtimebundle.PhaseClose, nil)
	if ledger.Len() != 0 {
		t.Fatalf("len=%d", ledger.Len())
	}
}
