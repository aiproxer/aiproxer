package acp

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// closeTrackingTransport is a minimal Transport that records Close calls.
type closeTrackingTransport struct {
	mu     sync.Mutex
	closed int
}

func (t *closeTrackingTransport) CallUnary(context.Context, []byte, int) ([]byte, error) {
	return nil, nil
}

func (t *closeTrackingTransport) CallPromptStream(context.Context, []byte) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}
func (t *closeTrackingTransport) SendJSONRPC(context.Context, []byte) error { return nil }
func (t *closeTrackingTransport) Close() error {
	t.mu.Lock()
	t.closed++
	t.mu.Unlock()
	return nil
}

func (t *closeTrackingTransport) closeCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

func TestEnsureProcess_ReusesInitializedRuntime(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)

	proc := newFakeProcess(t)
	transport := &closeTrackingTransport{}
	pool.SetProcess(key, proc, transport, "session-existing", "", "agent-bin")

	spawnCalled := false
	res, err := pool.EnsureProcess(context.Background(), key, SpawnHandshakeStrategy{
		Spawn: func() ([]string, Process, Transport, error) {
			spawnCalled = true
			return nil, nil, nil, errors.New("spawn must not be called on reuse")
		},
		Handshake: func(context.Context, Transport) (string, error) {
			t.Fatal("handshake must not be called on reuse")
			return "", nil
		},
		LogPrefix: "test",
	})
	if err != nil {
		t.Fatalf("EnsureProcess: %v", err)
	}
	if spawnCalled {
		t.Fatal("Spawn must not be called when an initialized runtime exists")
	}
	if res.Transport != transport {
		t.Fatal("expected reused transport")
	}
	if res.SessionID != "session-existing" {
		t.Fatalf("SessionID = %q, want %q", res.SessionID, "session-existing")
	}
}

func TestEnsureProcess_RestartsWhenProcessConfigChanges(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)
	oldProc := newFakeProcess(t)
	oldTransport := &closeTrackingTransport{}
	pool.SetProcess(key, oldProc, oldTransport, "session-low", "low", "agent-bin")

	newProc := newFakeProcess(t)
	newTransport := &closeTrackingTransport{}
	spawned := false
	res, err := pool.EnsureProcess(context.Background(), key, SpawnHandshakeStrategy{
		ProcessConfigKey: "high",
		Spawn: func() ([]string, Process, Transport, error) {
			spawned = true
			return []string{"agent-bin"}, newProc, newTransport, nil
		},
		Handshake: func(context.Context, Transport) (string, error) {
			return "session-high", nil
		},
		LogPrefix: "test-config-change",
	})
	if err != nil {
		t.Fatalf("EnsureProcess: %v", err)
	}
	if !spawned || res.Transport != newTransport || res.SessionID != "session-high" {
		t.Fatalf("expected fresh configured process: spawned=%v result=%+v", spawned, res)
	}
	if oldTransport.closeCount() != 1 {
		t.Fatalf("old transport close count = %d, want 1", oldTransport.closeCount())
	}
	if got := pool.Get(key).ProcessConfig(); got != "high" {
		t.Fatalf("process config = %q, want high", got)
	}
}

func TestEnsureProcess_SpawnsWhenNoProcess(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)

	proc := newFakeProcess(t)
	transport := &closeTrackingTransport{}
	handshakeCalled := false
	res, err := pool.EnsureProcess(context.Background(), key, SpawnHandshakeStrategy{
		Spawn: func() ([]string, Process, Transport, error) {
			return []string{"agent-bin", "--stdio"}, proc, transport, nil
		},
		Handshake: func(ctx context.Context, tr Transport) (string, error) {
			handshakeCalled = true
			if tr != transport {
				t.Fatal("handshake received wrong transport")
			}
			return "session-fresh", nil
		},
		LogPrefix: "test",
	})
	if err != nil {
		t.Fatalf("EnsureProcess: %v", err)
	}
	if !handshakeCalled {
		t.Fatal("Handshake must be called when no process exists")
	}
	if res.Transport != transport {
		t.Fatal("expected spawned transport")
	}
	if res.SessionID != "session-fresh" {
		t.Fatalf("SessionID = %q, want %q", res.SessionID, "session-fresh")
	}

	rt := pool.Get(key)
	if rt == nil || !rt.HasProcess() || !rt.IsInitialized() {
		t.Fatalf("pool must register spawned process; rt=%v", rt)
	}
	if rt.SessionID() != "session-fresh" {
		t.Fatalf("pool SessionID = %q, want %q", rt.SessionID(), "session-fresh")
	}
	if transport.closeCount() != 0 {
		t.Fatalf("transport must not be closed on success; closes=%d", transport.closeCount())
	}
}

func TestEnsureProcess_HandshakeFailureClosesTransportAndErrors(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)

	proc := newFakeProcess(t)
	transport := &closeTrackingTransport{}
	handshakeErr := errors.New("handshake boom")
	res, err := pool.EnsureProcess(context.Background(), key, SpawnHandshakeStrategy{
		Spawn: func() ([]string, Process, Transport, error) {
			return []string{"agent-bin"}, proc, transport, nil
		},
		Handshake: func(context.Context, Transport) (string, error) {
			return "", handshakeErr
		},
		LogPrefix: "test",
	})
	if !errors.Is(err, handshakeErr) {
		t.Fatalf("EnsureProcess err = %v, want %v", err, handshakeErr)
	}
	if res.Transport != nil || res.SessionID != "" {
		t.Fatalf("expected zero result on failure, got %+v", res)
	}
	if transport.closeCount() != 1 {
		t.Fatalf("transport must be closed once on handshake failure; closes=%d", transport.closeCount())
	}
	rt := pool.Get(key)
	if rt != nil && rt.HasProcess() {
		t.Fatal("pool must not register a process on handshake failure")
	}
}

func TestEnsureProcess_SpawnErrorReturned(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)

	spawnErr := errors.New("spawn boom")
	handshakeCalled := false
	_, err := pool.EnsureProcess(context.Background(), key, SpawnHandshakeStrategy{
		Spawn: func() ([]string, Process, Transport, error) {
			return nil, nil, nil, spawnErr
		},
		Handshake: func(context.Context, Transport) (string, error) {
			handshakeCalled = true
			return "", nil
		},
		LogPrefix: "test",
	})
	if !errors.Is(err, spawnErr) {
		t.Fatalf("EnsureProcess err = %v, want %v", err, spawnErr)
	}
	if handshakeCalled {
		t.Fatal("Handshake must not be called when Spawn fails")
	}
}

func TestEnsureProcess_RespawnsAfterKillSameConfig(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)

	var spawnCount atomic.Int64
	ensure := func(ctx context.Context) (EnsureProcessResult, error) {
		proc := newFakeProcess(t)
		transport := &closeTrackingTransport{}
		return pool.EnsureProcess(ctx, key, SpawnHandshakeStrategy{
			ProcessConfigKey: "low",
			Spawn: func() ([]string, Process, Transport, error) {
				spawnCount.Add(1)
				return []string{"agent-bin"}, proc, transport, nil
			},
			Handshake: func(context.Context, Transport) (string, error) {
				return "session-low", nil
			},
			LogPrefix: "test-respawn-after-kill",
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := ensure(ctx); err != nil {
		t.Fatalf("first EnsureProcess: %v", err)
	}
	_ = pool.KillRuntime(key)

	res, err := ensure(ctx)
	if err != nil {
		t.Fatalf("EnsureProcess after KillRuntime must respawn (not hang on stale flight): %v", err)
	}
	if res.SessionID != "session-low" {
		t.Fatalf("SessionID = %q, want session-low", res.SessionID)
	}
	if spawnCount.Load() != 2 {
		t.Fatalf("spawn count = %d, want 2 (fresh spawn after kill)", spawnCount.Load())
	}
}

func TestEnsureProcess_ConcurrentDifferentProcessConfigsConverge(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)

	var spawnCount atomic.Int64
	ensure := func(ctx context.Context, cfg string) (EnsureProcessResult, error) {
		proc := newFakeProcess(t)
		transport := &closeTrackingTransport{}
		return pool.EnsureProcess(ctx, key, SpawnHandshakeStrategy{
			ProcessConfigKey: cfg,
			Spawn: func() ([]string, Process, Transport, error) {
				spawnCount.Add(1)
				return []string{"agent-bin"}, proc, transport, nil
			},
			Handshake: func(context.Context, Transport) (string, error) {
				return "session-" + cfg, nil
			},
			LogPrefix: "test-config-flight",
		})
	}

	// Distinct ProcessConfigKeys must not thrash forever over the shared
	// RuntimeKey slot. Serialize ensures and retry until each caller's config
	// is live (or the deadline fires).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var (
		wg              sync.WaitGroup
		resHigh, resLow EnsureProcessResult
		errHigh, errLow error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		resHigh, errHigh = ensure(ctx, "high")
	}()
	go func() {
		defer wg.Done()
		resLow, errLow = ensure(ctx, "low")
	}()
	wg.Wait()

	if errHigh != nil {
		t.Fatalf("high: %v", errHigh)
	}
	if errLow != nil {
		t.Fatalf("low: %v", errLow)
	}
	if spawnCount.Load() < 2 {
		t.Fatalf("expected at least 2 spawns for distinct process configs, got %d", spawnCount.Load())
	}
	if resHigh.SessionID != "session-high" {
		t.Fatalf("high SessionID = %q, want session-high (shared wrong singleflight result)", resHigh.SessionID)
	}
	if resLow.SessionID != "session-low" {
		t.Fatalf("low SessionID = %q, want session-low (shared wrong singleflight result)", resLow.SessionID)
	}
}

func TestEnsureProcess_ConcurrentSpawnsSerialized(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)

	var spawnCount int64
	var handshakeCount int64
	transport := &closeTrackingTransport{}
	proc := newFakeProcess(t)

	// Block spawns slightly to simulate timing overlap
	spawnBarrier := make(chan struct{})
	var wg sync.WaitGroup

	numConcurrent := 5
	results := make([]EnsureProcessResult, numConcurrent)
	errs := make([]error, numConcurrent)

	for i := range numConcurrent {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			res, err := pool.EnsureProcess(context.Background(), key, SpawnHandshakeStrategy{
				Spawn: func() ([]string, Process, Transport, error) {
					<-spawnBarrier
					importSync := sync.Mutex{}
					importSync.Lock()
					spawnCount++
					importSync.Unlock()
					return []string{"agent-bin"}, proc, transport, nil
				},
				Handshake: func(ctx context.Context, tr Transport) (string, error) {
					importSync := sync.Mutex{}
					importSync.Lock()
					handshakeCount++
					importSync.Unlock()
					return "session-concurrent", nil
				},
				LogPrefix: "test-concurrent",
			})
			results[idx] = res
			errs[idx] = err
		}(i)
	}

	// Unblock all spawns concurrently
	close(spawnBarrier)
	wg.Wait()

	if spawnCount != 1 {
		t.Fatalf("expected exactly 1 spawn, got %d", spawnCount)
	}
	if handshakeCount != 1 {
		t.Fatalf("expected exactly 1 handshake, got %d", handshakeCount)
	}

	for i := range numConcurrent {
		if errs[i] != nil {
			t.Fatalf("goroutine %d failed: %v", i, errs[i])
		}
		if results[i].Transport != transport {
			t.Fatalf("goroutine %d got wrong transport", i)
		}
		if results[i].SessionID != "session-concurrent" {
			t.Fatalf("goroutine %d got wrong session id: %q", i, results[i].SessionID)
		}
	}
}
