package acp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// A transport can be closed without the pool driving the retirement (request
// cancellation closes it). The cleanup outcome it observed must still refuse
// reuse and must surface the sticky error to the caller.
func TestRuntimePool_TransportCleanupFailureRefusesReuse(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{})
	key := RuntimeKey{ClientSession: "transport-closed"}
	proc := newFakeProcess(t)
	proc.waitErr = ErrCleanupIncomplete
	transport := newStdioTransport(proc, nil)
	pool.SetProcess(key, proc, transport, "session", "config", "")
	if err := transport.Close(); !errors.Is(err, ErrCleanupIncomplete) {
		t.Fatalf("transport close error = %v, want %v", err, ErrCleanupIncomplete)
	}
	if _, err := pool.Acquire(key); !errors.Is(err, ErrCleanupIncomplete) {
		t.Errorf("Acquire accepted a transport with incomplete cleanup: %v", err)
	}
	if _, err := pool.EnsureProcess(context.Background(), key, SpawnHandshakeStrategy{
		Spawn: func() ([]string, Process, Transport, error) {
			t.Error("a runtime with incomplete cleanup was replaced by a fresh spawn")
			return nil, nil, nil, errors.New("unexpected spawn")
		},
		Handshake: func(context.Context, Transport) (string, error) {
			return "", errors.New("unexpected handshake")
		},
		ProcessConfigKey: "config",
	}); !errors.Is(err, ErrCleanupIncomplete) {
		t.Errorf("EnsureProcess error = %v, want %v", err, ErrCleanupIncomplete)
	}
}

func TestRuntimePool_IncompleteCleanupIsStickyAndPreventsReuse(t *testing.T) {
	for _, withTransport := range []bool{false, true} {
		pool := NewRuntimePool(RuntimePoolConfig{})
		proc := newFakeProcess(t)
		proc.waitErr = ErrCleanupIncomplete
		key := RuntimeKey{ClientSession: "incomplete"}
		var transport Transport
		if withTransport {
			transport = newStdioTransport(proc, nil)
		}
		pool.SetProcess(key, proc, transport, "session", "", "")
		for range 2 {
			if err := pool.KillRuntime(key); !errors.Is(err, ErrCleanupIncomplete) {
				t.Errorf("cleanup error lost: %v", err)
			}
		}
		if _, err := pool.Acquire(key); !errors.Is(err, ErrCleanupIncomplete) {
			t.Errorf("Acquire accepted incomplete cleanup: %v", err)
		}
		for range 2 {
			if err := pool.Close(); !errors.Is(err, ErrCleanupIncomplete) {
				t.Errorf("Close lost cleanup error: %v", err)
			}
		}
	}
}

func TestRuntimePool_HandshakeFailureRetainsIncompleteCleanup(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{})
	key := RuntimeKey{ClientSession: "handshake-cleanup"}
	spawns := 0
	strategy := SpawnHandshakeStrategy{
		Spawn: func() ([]string, Process, Transport, error) {
			spawns++
			proc := newFakeProcess(t)
			proc.waitErr = ErrCleanupIncomplete
			return []string{"peer"}, proc, newStdioTransport(proc, nil), nil
		},
		Handshake: func(context.Context, Transport) (string, error) { return "", errors.New("handshake failed") },
	}
	for range 2 {
		if _, err := pool.EnsureProcess(t.Context(), key, strategy); !errors.Is(err, ErrCleanupIncomplete) {
			t.Errorf("handshake cleanup failure lost: %v", err)
		}
	}
	if spawns != 1 {
		t.Error("spawned into runtime with unconfirmed cleanup")
	}
	if err := pool.Close(); !errors.Is(err, ErrCleanupIncomplete) {
		t.Error("pool lost failed handshake owner", err)
	}
}

func TestRuntimePool_IdentityMismatchStillJoinsCleanupOutcome(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{})
	key := RuntimeKey{ClientSession: "recycled-owner"}
	proc := &cleanupOutcomeProcess{fakeProcess: newFakeProcess(t)}
	proc.waitErr = ErrCleanupIncomplete
	pool.SetProcess(key, proc, nil, "session", "", "")
	rt := pool.Get(key)
	rt.mu.Lock()
	rt.identity.PID++
	rt.mu.Unlock()
	if err := pool.KillRuntime(key); !errors.Is(err, ErrCleanupIncomplete) {
		t.Error("identity mismatch hid unconfirmed cleanup", err)
	}
	if proc.killed {
		t.Error("identity mismatch allowed signalling")
	}
}

type cleanupOutcomeProcess struct{ *fakeProcess }

func (p *cleanupOutcomeProcess) WaitCleanup() error { return p.Wait() }

// A stream retirement and pool Close must join the same Kill/Wait operation.
// Clearing proc early must not let instance cleanup run ahead of that operation.
func TestRuntimePool_CloseJoinsRetirement(t *testing.T) {
	for _, directTransportClose := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			pool := NewRuntimePool(RuntimePoolConfig{})
			proc := &blockedRetirementProcess{Process: newFakeProcess(t), entered: make(chan struct{}), release: make(chan struct{})}
			key := RuntimeKey{ClientSession: "retire"}
			transport := newStdioTransport(proc, nil)
			pool.SetProcess(key, proc, transport, "session", "", "")
			retired := make(chan struct{})
			go func() {
				if directTransportClose {
					_ = transport.Close()
				} else {
					_ = pool.KillRuntime(key)
				}
				close(retired)
			}()
			<-proc.entered
			closed := make(chan struct{})
			go func() { _ = pool.Close(); close(closed) }()
			synctest.Wait()
			select {
			case <-closed:
				t.Error("Close returned before process retirement")
			default:
			}
			close(proc.release)
			<-retired
			<-closed
		})
	}
}

type blockedRetirementProcess struct {
	Process
	entered, release chan struct{}
}

func TestRuntimePool_CloseRetiresAllBeforeJoiningPendingCleanup(t *testing.T) {
	for _, cleanupErr := range []error{nil, ErrCleanupIncomplete} {
		synctest.Test(t, func(t *testing.T) {
			pool := NewRuntimePool(RuntimePoolConfig{})
			stuckKey := RuntimeKey{ClientSession: "stuck"}
			healthyKey := RuntimeKey{ClientSession: "healthy"}
			stuckPeer := newFakeProcess(t)
			stuckPeer.waitErr = cleanupErr
			stuck := &blockedRetirementProcess{Process: stuckPeer, entered: make(chan struct{}), release: make(chan struct{})}
			healthy := &completedRetirementProcess{blockedRetirementProcess: blockedRetirementProcess{Process: newFakeProcess(t), entered: make(chan struct{}), release: make(chan struct{})}, done: make(chan struct{})}
			pool.SetProcess(stuckKey, stuck, nil, "s1", "", "")
			pool.SetProcess(healthyKey, healthy, newStdioTransport(healthy, nil), "s2", "", "")
			closed := make(chan error, 1)
			go func() { closed <- pool.Close() }()
			synctest.Wait()
			// Both peers block at Kill, making this assertion independent of map
			// iteration order: sequential Close can reach at most one peer.
			for _, entered := range []chan struct{}{stuck.entered, healthy.entered} {
				select {
				case <-entered:
				default:
					t.Error("runtime never received retirement request")
				}
			}
			close(healthy.release)
			synctest.Wait()
			select {
			case <-healthy.done:
			default:
				t.Error("healthy runtime did not finish while sibling cleanup remained pending")
			}
			closedEarly := false
			select {
			case err := <-closed:
				closedEarly = true
				t.Errorf("Close did not join pending cleanup: %v", err)
			default:
			}
			if pool.Get(stuckKey) == nil {
				t.Error("pending owner was forgotten")
			}
			close(stuck.release)
			if !closedEarly {
				if err := <-closed; !errors.Is(err, cleanupErr) {
					t.Errorf("Close error = %v, want %v", err, cleanupErr)
				}
			}
			if cleanupErr != nil {
				if _, err := pool.Acquire(stuckKey); !errors.Is(err, ErrCleanupIncomplete) {
					t.Error("failed retirement lost sticky error", err)
				}
			} else if pool.Get(stuckKey) != nil || pool.Get(healthyKey) != nil {
				t.Error("successful shutdown did not clear pool")
			}
		})
	}
}

type completedRetirementProcess struct {
	blockedRetirementProcess
	done chan struct{}
}

func (p *completedRetirementProcess) Wait() error {
	err := p.Process.Wait()
	close(p.done)
	return err
}

func TestRuntimePool_ConcurrentIdleReapingKeepsCanonicalRuntime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{IdleTimeout: time.Second})
		key := RuntimeKey{ClientSession: "idle-retire"}
		proc := &blockedRetirementProcess{Process: newFakeProcess(t), entered: make(chan struct{}), release: make(chan struct{})}
		pool.SetProcess(key, proc, nil, "session", "", "")
		time.Sleep(2 * time.Second)
		results := make(chan *SubprocessRuntime, 2)
		acquire := func() { rt, _ := pool.Acquire(key); results <- rt }
		go acquire()
		<-proc.entered
		go acquire()
		synctest.Wait()
		close(proc.release)
		first, second := <-results, <-results
		if first != second || first != pool.Get(key) {
			t.Fatal("retirement waiter returned a withdrawn runtime")
		}
		_ = pool.Close()
	})
}

func (p *blockedRetirementProcess) Kill() error {
	close(p.entered)
	<-p.release
	return p.Process.Kill()
}

// waitFor polls cond every pollInterval until it returns true or deadline
// elapses. Reports via t.Fatalf with msg if the deadline elapses first.
// Skill golang-testing: tests MUST NOT depend on wall-time sleeps, so the
// pool's stale-kill / idle-reap timing is observed by polling the
// observable outcome instead of fixed-duration sleeps.
func waitFor(t *testing.T, deadline, pollInterval time.Duration, msg string, cond func() bool) {
	t.Helper()
	if deadline <= 0 {
		deadline = 2 * time.Second
	}
	if pollInterval <= 0 {
		pollInterval = 5 * time.Millisecond
	}
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf("waitFor: %s (deadline %s)", msg, deadline)
		case <-ticker.C:
		}
	}
}

func TestRuntimePool_AcquireCreatesRuntime(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	rt, err := pool.Acquire(key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if rt == nil {
		t.Fatal("expected non-nil runtime")
	}
	if rt.HasProcess() {
		t.Fatal("new runtime should not have a process")
	}
	if rt.IsInitialized() {
		t.Fatal("new runtime should not be initialized")
	}
}

func TestRuntimePool_AcquireReturnsSameRuntime(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	rt1, err := pool.Acquire(key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	rt2, err := pool.Acquire(key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if rt1 != rt2 {
		t.Fatal("expected same runtime for same key")
	}
}

func TestRuntimePool_AcquireDifferentKeysDifferentRuntimes(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key1 := RuntimeKey{Workspace: "/tmp/a", Model: "agent", ClientSession: "s1"}
	key2 := RuntimeKey{Workspace: "/tmp/b", Model: "agent", ClientSession: "s1"}
	rt1, err := pool.Acquire(key1)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	rt2, err := pool.Acquire(key2)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if rt1 == rt2 {
		t.Fatal("expected different runtimes for different keys")
	}
}

func TestRuntimePool_SetProcessAndSessionID(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)

	proc := newFakeProcess(t)
	pool.SetProcess(key, proc, nil, "session-123", "", "cursor-agent")

	rt := pool.Get(key)
	if rt == nil {
		t.Fatal("expected non-nil runtime after SetProcess")
	}
	if !rt.HasProcess() {
		t.Fatal("expected runtime to have a process")
	}
	if !rt.IsInitialized() {
		t.Fatal("expected runtime to be initialized")
	}
	if rt.SessionID() != "session-123" {
		t.Fatalf("SessionID = %q, want %q", rt.SessionID(), "session-123")
	}
}

func TestRuntimePool_IdleReaping(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{
		IdleTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	if _, err := pool.Acquire(key); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	proc := newFakeProcess(t)
	pool.SetProcess(key, proc, nil, "session-1", "", "cursor-agent")
	pool.Release(key)

	// Wait until the pool reaps the idle runtime. The reap keeps the slot
	// (its cleanup evidence must survive) and detaches the process, so the
	// observable outcome is a runtime without a process whose process tree was
	// retired. The cond is a pure predicate; assertions live after
	// waitFor so a "not yet" tick does not call t.Fatal in a loop. The
	// last Acquire error is captured so `go test -v` shows the real
	// failure mode if the deadline elapses inside the polling loop; the
	// nil-clear on the no-error branch resets stale state from any prior
	// tick that errored (so a recovered Acquire does not leave a ghost
	// error behind when cond finally succeeds).
	var rt2 *SubprocessRuntime
	var lastAcquireErr error
	waitFor(t, 0, 0, "Acquire returns a runtime without the reaped process", func() bool {
		got, err := pool.Acquire(key)
		if err != nil {
			lastAcquireErr = err
			return false
		}
		lastAcquireErr = nil
		rt2 = got
		if got.HasProcess() {
			return false // not reaped yet
		}
		proc.mu.Lock()
		defer proc.mu.Unlock()
		return proc.killed
	})
	if lastAcquireErr != nil {
		t.Fatalf("Acquire errored during idle reaping wait: %v", lastAcquireErr)
	}
	if rt2.HasProcess() {
		t.Fatal("fresh runtime should not have a process")
	}
}

func TestRuntimePool_NoReapingWhileInUse(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{
		IdleTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	rt, err := pool.Acquire(key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	proc := newFakeProcess(t)
	pool.SetProcess(key, proc, nil, "session-1", "", "cursor-agent")
	pool.MarkInUse(key)

	// While marked in-use, Acquire must return the same runtime — no
	// reaping, no replacement. The existing logic is synchronous under
	// the pool lock, so a single Acquire call suffices.
	rt2, _ := pool.Acquire(key)
	if rt2 != rt {
		t.Fatal("expected same runtime while in use")
	}
}

func TestRuntimePool_KillRuntime(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)
	proc := newFakeProcess(t)
	pool.SetProcess(key, proc, nil, "session-1", "", "cursor-agent")

	if err := pool.KillRuntime(key); err != nil {
		t.Fatalf("KillRuntime: %v", err)
	}

	rt := pool.Get(key)
	if rt == nil {
		t.Fatal("runtime should still exist after kill")
	}
	if rt.HasProcess() {
		t.Fatal("runtime should not have a process after kill")
	}
	if rt.IsInitialized() {
		t.Fatal("runtime should not be initialized after kill")
	}
}

func TestRuntimePool_StaleKillAfterRelease(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{
		StaleKillDelay: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)
	proc := newFakeProcess(t)
	pool.SetProcess(key, proc, nil, "session-1", "", "cursor-agent")
	pool.Release(key)

	// Poll for both observable outcomes: the runtime slot no longer holds
	// a process AND the underlying fake reported itself killed.
	waitFor(t, 0, 0, "stale kill timer fires and clears fake process", func() bool {
		rt := pool.Get(key)
		if rt == nil || rt.HasProcess() {
			return false
		}
		proc.mu.Lock()
		defer proc.mu.Unlock()
		return proc.killed
	})

	if rt := pool.Get(key); rt == nil {
		t.Fatal("runtime should still exist after stale kill")
	}
}

func TestRuntimePool_StaleKillCancelledByAcquire(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{
		StaleKillDelay: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)
	proc := newFakeProcess(t)
	pool.SetProcess(key, proc, nil, "session-1", "", "cursor-agent")
	pool.Release(key)

	// Mark the runtime in-use synchronously so the freshly armed stale-kill
	// timer (50ms) is cancelled before it can fire. Poll that the process
	// survives well past the would-be kill moment — cancellation must work.
	markAt := time.Now()
	pool.MarkInUse(key)
	waitFor(t, 0, 0, "process still alive past StaleKillDelay after MarkInUse cancellation", func() bool {
		if time.Since(markAt) < 100*time.Millisecond {
			return false
		}
		rt := pool.Get(key)
		if rt == nil || !rt.HasProcess() {
			return false
		}
		proc.mu.Lock()
		defer proc.mu.Unlock()
		return !proc.killed
	})
}

func TestRuntimePool_ConcurrentAcquire(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			rt, err := pool.Acquire(key)
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			if rt == nil {
				t.Error("expected non-nil runtime")
			}
		})
	}
	wg.Wait()
}

func TestRuntimePool_CloseKillsAll(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})

	key1 := RuntimeKey{Workspace: "/tmp/a", Model: "agent", ClientSession: "s1"}
	key2 := RuntimeKey{Workspace: "/tmp/b", Model: "agent", ClientSession: "s2"}
	_, _ = pool.Acquire(key1)
	_, _ = pool.Acquire(key2)
	pool.SetProcess(key1, newFakeProcess(t), nil, "s1", "", "agent1")
	pool.SetProcess(key2, newFakeProcess(t), nil, "s2", "", "agent2")

	if err := pool.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Pool should be cleared.
	if rt := pool.Get(key1); rt != nil {
		t.Fatal("expected nil runtime after Close")
	}
}

func TestRuntimePool_HistoryState(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	rt, _ := pool.Acquire(key)

	initial := rt.HistoryState()
	if initial.messageCount != 0 || initial.prefixHash != "" {
		t.Fatalf("initial history state = %+v, want zero", initial)
	}

	rt.SetHistoryState(historyState{messageCount: 5, prefixHash: "abc123"})
	got := rt.HistoryState()
	if got.messageCount != 5 || got.prefixHash != "abc123" {
		t.Fatalf("history state = %+v, want {5, abc123}", got)
	}
}

func TestRuntimeKey_String(t *testing.T) {
	t.Parallel()
	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	s := key.String()
	if s == "" {
		t.Fatal("expected non-empty string representation")
	}
}

func TestRuntimePool_StaleKillPIDReuseHardening(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{
		StaleKillDelay: 30 * time.Millisecond,
	})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)
	proc := newFakeProcess(t)
	pool.SetProcess(key, proc, nil, "session-1", "", "cursor-agent")

	// Manually corrupt the identity to simulate PID reuse (different PID).
	rt := pool.Get(key)
	rt.mu.Lock()
	rt.identity = ProcessIdentity{PID: proc.PID() + 99999}
	rt.mu.Unlock()

	pool.Release(key)

	// Poll for the observable outcome: KillRuntime resets state but skips
	// the kill because identity mismatches. The fake process must remain
	// alive (proc.killed == false) once the timer fires.
	waitFor(t, 0, 0, "KillRuntime clears state without killing the reused-PID process", func() bool {
		rt := pool.Get(key)
		if rt == nil || rt.HasProcess() {
			return false
		}
		proc.mu.Lock()
		defer proc.mu.Unlock()
		// Timer has run and reset state; identity check kept kill off.
		return !proc.killed
	})
}
