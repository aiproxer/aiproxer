package runtimehost_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

// Task 4.6: blocked SSE/stream pin exhausts retained budget → later publish rejects
// without terminating the old stream (req 10.8-10.11).
func TestBlockedStream_RetentionCapRejectsWithoutTerminating(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(1, nil)
	g1 := m.Prepare("stream-gen")
	mustPublish(t, m, g1)

	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	pin, ok := lease.TransferPin(runtimehost.PinSSE)
	if !ok {
		t.Fatal("transfer sse pin")
	}

	mustPublish(t, m, m.Prepare("g2"))
	active := m.Active()
	pressure := m.RetentionPressure()
	if pressure.BlockingCategory != runtimehost.RetentionCategoryBudget {
		t.Fatalf("pressure category=%q want %q", pressure.BlockingCategory, runtimehost.RetentionCategoryBudget)
	}
	if pressure.Retained < 1 || pressure.MaxRetained != 1 {
		t.Fatalf("pressure=%+v", pressure)
	}

	candCloses := atomic.Int32{}
	cand := m.PrepareOwned("g3", &stubOwned{closeFn: func() error {
		candCloses.Add(1)
		return nil
	}})
	err := m.Publish(cand)
	if !errors.Is(err, runtimehost.ErrRetentionBlocked) {
		t.Fatalf("want retention blocked, got %v", err)
	}
	if candCloses.Load() != 1 {
		t.Fatalf("candidate rollback closes=%d want 1", candCloses.Load())
	}
	if m.Active() != active {
		t.Fatal("retention block must not swap active")
	}
	select {
	case <-g1.Drained():
		t.Fatal("blocked stream must not be terminated for retention pressure")
	default:
	}
	if g1.Refs() != 1 {
		t.Fatalf("stream pin refs=%d", g1.Refs())
	}

	pin.Release()
	<-g1.Drained()
	if _, err := m.RetireGeneration(context.Background(), g1); err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatal(err)
	}
	m.SweepClosed()
	mustPublish(t, m, m.Prepare("g3-after-drain"))
}

// TestGeneration_CloseFailure_StaysClosingForRetry drives Generation.Close
// directly without a replacing Publish, so Manager's automatic post-publish
// retirement scheduling (task 7.3) never races this manual drive.
func TestGeneration_CloseFailure_StaysClosingForRetry(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("close-temp")
	var closes atomic.Int32
	owned := &stubOwned{closeFn: func() error {
		n := closes.Add(1)
		if n == 1 {
			return closeErr
		}
		return nil
	}}
	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g)
	m.BeginShutdown()
	m.DetachActive()
	<-g.Drained()
	if err := g.BeginClose(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("first close: %v", err)
	}
	if g.Lifecycle() != runtimehost.GenClosing {
		t.Fatalf("lifecycle=%v want closing after failed cleanup", g.Lifecycle())
	}
	if err := g.Close(); err != nil {
		t.Fatalf("retry close: %v", err)
	}
	if g.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v want closed", g.Lifecycle())
	}
	if closes.Load() != 2 {
		t.Fatalf("closes=%d want 2 attempts", closes.Load())
	}
	if err := g.Close(); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("post-success close: %v", err)
	}
}

// TestManagerRetire_QuiesceErrorStatusReporting and the panic-isolation test
// below use BeginShutdown+DetachActive (not a replacing Publish) so the
// explicit synchronous Manager.RetireGeneration call under test is the only
// retirement attempt — Manager's automatic post-publish scheduling (task 7.3)
// is covered separately in manager_retire_test.go.
func TestManagerRetire_QuiesceErrorStatusReporting(t *testing.T) {
	t.Parallel()
	quiesceErr := errors.New("quiesce-failed")
	owned := &ledgerOwned{
		quiesceFn: func(context.Context) error { return quiesceErr },
	}
	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	m.BeginShutdown()
	m.DetachActive()

	status, err := m.RetireGeneration(context.Background(), g1)
	if !errors.Is(err, quiesceErr) {
		t.Fatalf("want quiesce error, got %v", err)
	}
	if status.Outcome != runtimehost.LifecycleOutcomeQuiesceFailed {
		t.Fatalf("status=%+v", status)
	}
	if status.GenerationID != g1.ID() {
		t.Fatalf("status gen=%d want %d", status.GenerationID, g1.ID())
	}
}

func TestManagerRetire_QuiescePanicIsolatedContinuesDrain(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	owned := &ledgerOwned{
		quiesceFn: func(context.Context) error {
			close(entered)
			panic("quiesce boom")
		},
	}
	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	m.BeginShutdown()
	m.DetachActive()

	errCh := make(chan error, 1)
	statusCh := make(chan runtimehost.RetirementStatus, 1)
	go func() {
		st, err := m.RetireGeneration(context.Background(), g1)
		statusCh <- st
		errCh <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("quiesce panic path did not start")
	}
	lease.Release()

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "quiesce boom") {
			t.Fatalf("want quiesce panic error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retire timeout")
	}
	st := <-statusCh
	if st.Outcome != runtimehost.LifecycleOutcomeQuiesceFailed {
		t.Fatalf("status=%+v", st)
	}
	if owned.closes.Load() != 1 {
		t.Fatalf("close should still run after quiesce panic isolation, closes=%d", owned.closes.Load())
	}
}
