package runtimehost_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

func TestShutdown_LatePublishRejectedDuringSourceRead(t *testing.T) {
	t.Parallel()
	gate := newStageGate()
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
		atomic: configsource.AtomicEligible,
		gate:   gate,
	}
	c := newTestCoordinator(t, nil, src, nil, nil, nil)
	activeBefore := c.Status().ActiveGeneration

	done := make(chan sdkreload.Result, 1)
	go func() {
		done <- c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	}()
	gate.WaitEnter(t)
	c.BeginShutdown()
	gate.Release()
	res := <-done
	if res.Category == sdkreload.ResultPublished {
		t.Fatalf("published after BeginShutdown is never acceptable; category=%q gen=%d", res.Category, res.ActiveGeneration)
	}
	if res.Category != sdkreload.ResultCanceled {
		t.Fatalf("category=%q want canceled", res.Category)
	}
	if c.Status().ActiveGeneration != activeBefore {
		t.Fatalf("active gen changed after shutdown cancel: before=%d after=%d", activeBefore, c.Status().ActiveGeneration)
	}
	idleCtx, idleCancel := context.WithTimeout(context.Background(), time.Second)
	defer idleCancel()
	if err := c.WaitForIdle(idleCtx); err != nil {
		t.Fatalf("WaitForIdle: %v", err)
	}
	late := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if late.Category != sdkreload.ResultCanceled {
		t.Fatalf("late reload=%q", late.Category)
	}
}

func TestShutdown_SourceReadReceivesContextCancellation(t *testing.T) {
	t.Parallel()
	gate := newStageGate()
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
		atomic: configsource.AtomicEligible,
		gate:   gate,
	}
	c := newTestCoordinator(t, nil, src, nil, nil, nil)

	done := make(chan sdkreload.Result, 1)
	go func() {
		done <- c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	}()
	gate.WaitEnter(t)
	c.BeginShutdown()
	// Do not Release the gate — cancellation must unblock ReadStable via ctx.
	res := <-done
	if res.Category != sdkreload.ResultCanceled {
		t.Fatalf("category=%q want canceled (source must observe ctx cancel)", res.Category)
	}
	idleCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.WaitForIdle(idleCtx); err != nil {
		t.Fatalf("WaitForIdle: %v", err)
	}
}

func TestShutdown_CompileReceivesContextCancellationAndRollsBack(t *testing.T) {
	t.Parallel()
	gate := newStageGate()
	plane := newFakePlane(map[string]int{"local-stub": 1})
	compile := runtimehost.FuncCompiler(func(ctx context.Context, _ *config.Config, _ map[string]int) (runtimehost.PublishedRequestPlane, error) {
		if err := gate.Hold(ctx); err != nil {
			_ = plane.Close()
			return nil, err
		}
		return plane, nil
	})
	var loads atomic.Int64
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		n := loads.Add(1)
		return baseEffective("fp-"+string(rune('a'+n)), byte(n+1)), nil
	})
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1"), PrivateDigest: [32]byte{9}},
		atomic: configsource.AtomicEligible,
	}
	c := newTestCoordinator(t, nil, src, loader, compile, nil)
	activeBefore := c.Status().ActiveGeneration

	done := make(chan sdkreload.Result, 1)
	go func() {
		done <- c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	}()
	gate.WaitEnter(t)
	c.BeginShutdown()
	res := <-done
	if res.Category != sdkreload.ResultCanceled {
		t.Fatalf("category=%q want canceled", res.Category)
	}
	if c.Status().ActiveGeneration != activeBefore {
		t.Fatalf("late publication: before=%d after=%d", activeBefore, c.Status().ActiveGeneration)
	}
	if !plane.closed.Load() {
		t.Fatal("candidate plane must be rolled back (closed)")
	}
	idleCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.WaitForIdle(idleCtx); err != nil {
		t.Fatalf("WaitForIdle: %v", err)
	}
}

func TestShutdown_PendingSignalDoesNotPublish(t *testing.T) {
	t.Parallel()
	gate := newStageGate()
	compile := &controllableCompiler{gate: gate, kinds: map[string]int{"local-stub": 1}}
	var loads atomic.Int64
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		n := loads.Add(1)
		return baseEffective("fp-"+string(rune('a'+n)), byte(n+1)), nil
	})
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1"), PrivateDigest: [32]byte{9}},
		atomic: configsource.AtomicEligible,
	}
	c := newTestCoordinator(t, nil, src, loader, compile, nil)

	done := make(chan sdkreload.Result, 1)
	go func() {
		done <- c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	}()
	gate.WaitEnter(t)
	busy := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if busy.Category != sdkreload.ResultBusy {
		t.Fatalf("coalesce busy=%q", busy.Category)
	}
	c.BeginShutdown()
	res := <-done
	if res.Category == sdkreload.ResultPublished {
		t.Fatalf("published after BeginShutdown during compile: %q", res.Category)
	}
	idleCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.WaitForIdle(idleCtx); err != nil {
		t.Fatalf("WaitForIdle: %v", err)
	}
	st := c.Status()
	if st.PendingSignal {
		t.Fatal("pending signal must clear on BeginShutdown")
	}
	if st.Busy {
		t.Fatal("must not remain busy after idle")
	}
	late := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if late.Category != sdkreload.ResultCanceled {
		t.Fatalf("late sighup=%q", late.Category)
	}
}

func TestShutdown_BeginShutdownArmRaceCancelsAttempt(t *testing.T) {
	t.Parallel()
	// High-count race: BeginShutdown after attempt enters source read must cancel
	// and never publish; WaitForIdle must always succeed.
	for i := range 200 {
		gate := newStageGate()
		src := &fakeSource{
			path:   "/fixed/startup/config.yaml",
			snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
			atomic: configsource.AtomicEligible,
			gate:   gate,
		}
		c := newTestCoordinator(t, nil, src, nil, nil, nil)
		done := make(chan sdkreload.Result, 1)
		go func() {
			done <- c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
		}()
		gate.WaitEnter(t)
		c.BeginShutdown()
		res := <-done
		if res.Category == sdkreload.ResultPublished {
			t.Fatalf("iter %d published after BeginShutdown", i)
		}
		if res.Category != sdkreload.ResultCanceled {
			t.Fatalf("iter %d category=%q want canceled", i, res.Category)
		}
		idleCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := c.WaitForIdle(idleCtx); err != nil {
			cancel()
			t.Fatalf("iter %d WaitForIdle: %v", i, err)
		}
		cancel()
	}
}

func TestShutdown_ActivePinnedStreamSurvivesBeginShutdown(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	closer := &countingCloser{}
	g := m.PrepareOwned("pinned", closer)
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	pin, ok := lease.TransferPin(runtimehost.PinSSE)
	if !ok {
		t.Fatal("pin")
	}
	lease.Release()

	m.BeginShutdown()
	if closer.closes.Load() != 0 {
		t.Fatal("must not close beneath pin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := m.ShutdownDetached(ctx)
	if err == nil {
		t.Fatal("expected timeout while pinned")
	}
	if closer.closes.Load() != 0 {
		t.Fatal("pinned generation must remain open")
	}
	pin.Release()
}

func TestShutdown_CleanupErrorAggregated(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(2, nil)
	boom := errors.New("cleanup boom")
	g := m.PrepareOwned("cleanup", errCloser{err: boom})
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	m.BeginShutdown()
	err := m.ShutdownDetached(context.Background())
	if err == nil {
		t.Fatal("expected cleanup error")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
}

type errCloser struct{ err error }

func (c errCloser) Close() error { return c.err }
