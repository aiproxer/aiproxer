package acp

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestClaimForTurn_KillsAndResetsOnConfigMismatch verifies that claiming a turn
// on an idle child spawned with a different process-scoped config kills the
// child and clears transcript state so the next spawn receives a full replay,
// then marks the runtime in use.
func TestClaimForTurn_KillsAndResetsOnConfigMismatch(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)
	oldProc := newFakeProcess(t)
	oldTransport := &closeTrackingTransport{}
	pool.SetProcess(key, oldProc, oldTransport, "session-low", "low", "agent-bin")
	if rt := pool.Get(key); rt != nil {
		rt.SetHistoryState(historyState{messageCount: 3, prefixHash: "abc"})
	}

	rt, busy := pool.ClaimForTurn(t.Context(), key, "high")
	if busy {
		t.Fatal("expected busy=false on idle runtime with config mismatch")
	}
	if rt == nil {
		t.Fatal("expected non-nil runtime")
	}
	// The retirement of the mismatched child runs in a pool-owned goroutine:
	// the claim must not block on a process-tree cleanup.
	waitFor(t, 0, 0, "the mismatched child is retired", func() bool {
		return oldTransport.closeCount() == 1
	})
	if got := pool.Get(key); got.HasProcess() {
		t.Fatal("mismatched process must be killed by ClaimForTurn")
	}
	if state := rt.HistoryState(); state.messageCount != 0 || state.prefixHash != "" {
		t.Fatalf("history state = %+v, want zero after reset", state)
	}
	if !rt.IsInUse() {
		t.Fatal("ClaimForTurn must mark the runtime in use on the claiming path")
	}
}

func TestClaimForTurn_KeepsMatchingProcess(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)
	proc := newFakeProcess(t)
	transport := &closeTrackingTransport{}
	pool.SetProcess(key, proc, transport, "session-low", "low", "agent-bin")

	rt, busy := pool.ClaimForTurn(t.Context(), key, "low")
	if busy {
		t.Fatal("expected busy=false when config matches")
	}
	if transport.closeCount() != 0 {
		t.Fatalf("transport close count = %d, want 0", transport.closeCount())
	}
	if rt == nil || !rt.HasProcess() {
		t.Fatal("matching process must be kept")
	}
	if !rt.IsInUse() {
		t.Fatal("matching-config claim must still mark the runtime in use")
	}
}

func TestClaimForTurn_NoProcessClaimsIdle(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)

	rt, busy := pool.ClaimForTurn(t.Context(), key, "high")
	if busy {
		t.Fatal("expected busy=false when no process exists")
	}
	if rt == nil {
		t.Fatal("expected non-nil runtime")
	}
	if rt.HasProcess() {
		t.Fatal("runtime must not have a process")
	}
	if !rt.IsInUse() {
		t.Fatal("claim must mark the runtime in use even without a process")
	}
}

func TestClaimForTurn_CreatesEntryWhenAbsent(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}

	rt, busy := pool.ClaimForTurn(t.Context(), key, "high")
	if busy {
		t.Fatal("expected busy=false for absent runtime")
	}
	if rt == nil {
		t.Fatal("expected ClaimForTurn to create a runtime entry")
	}
	if pool.Get(key) == nil {
		t.Fatal("ClaimForTurn must register the created entry in the pool")
	}
}

// TestClaimForTurn_RefusesInUseRuntime is the core regression for the
// high-severity finding: a turn that wants a different process-scoped config
// must NOT kill a child another turn is still streaming on. The claim is
// refused and the live child is left untouched.
func TestClaimForTurn_RefusesInUseRuntime(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)
	proc := newFakeProcess(t)
	transport := &closeTrackingTransport{}
	pool.SetProcess(key, proc, transport, "session-low", "low", "agent-bin")
	pool.MarkInUse(key) // simulate an in-flight turn on the child

	rt, busy := pool.ClaimForTurn(t.Context(), key, "high")
	if !busy {
		t.Fatal("expected busy=true when a turn is in flight on a mismatched child")
	}
	if transport.closeCount() != 0 {
		t.Fatalf("in-use transport must not be closed; close count = %d", transport.closeCount())
	}
	proc.mu.Lock()
	killed := proc.killed
	proc.mu.Unlock()
	if killed {
		t.Fatal("in-use process must not be killed")
	}
	if rt == nil || !rt.HasProcess() {
		t.Fatal("in-use runtime must keep its process when claim is refused")
	}
	if got := pool.Get(key).ProcessConfig(); got != "low" {
		t.Fatalf("process config = %q, want low (unchanged)", got)
	}
}

// TestClaimForTurn_RefusesInUseRuntimeEvenWhenConfigMatches documents the
// race-free guarantee: a single stdio subprocess cannot carry two concurrent
// turns, so a second concurrent turn is rejected even when its process config
// matches the live child. This is what lets the config-reset kill run only on
// the claiming (idle) path without a use-after-kill race.
func TestClaimForTurn_RefusesInUseRuntimeEvenWhenConfigMatches(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)
	proc := newFakeProcess(t)
	transport := &closeTrackingTransport{}
	pool.SetProcess(key, proc, transport, "session-low", "low", "agent-bin")
	pool.MarkInUse(key)

	_, busy := pool.ClaimForTurn(t.Context(), key, "low")
	if !busy {
		t.Fatal("expected busy=true for a concurrent turn on an in-use runtime")
	}
	if transport.closeCount() != 0 {
		t.Fatalf("transport must not be closed; close count = %d", transport.closeCount())
	}
}

func TestClaimForTurn_EmptyConfigKeepsAcpProcess(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	// ACP-session protocols use an empty process config for every turn.
	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)
	proc := newFakeProcess(t)
	transport := &closeTrackingTransport{}
	pool.SetProcess(key, proc, transport, "session", "", "agent-bin")

	rt, busy := pool.ClaimForTurn(t.Context(), key, "")
	if busy {
		t.Fatal("expected busy=false for empty process config on idle runtime")
	}
	if transport.closeCount() != 0 {
		t.Fatalf("ACP transport must not be closed; close count = %d", transport.closeCount())
	}
	if rt == nil || !rt.HasProcess() {
		t.Fatal("ACP process must be kept")
	}
}

// TestClaimForTurn_ClaimSurvivesConcurrentClaimer verifies the full turn
// serialization invariant: once a turn claims the runtime, a peer claim is
// refused and the claimed child is not killed; after the first turn releases,
// a peer can claim and reset a mismatched child.
func TestClaimForTurn_ClaimSurvivesConcurrentClaimer(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	proc := newFakeProcess(t)
	transport := &closeTrackingTransport{}

	first, busy := pool.ClaimForTurn(t.Context(), key, "low")
	if busy {
		t.Fatal("first claim must succeed on an idle runtime")
	}
	// Simulate the first turn's ensure-process having spawned the child.
	pool.SetProcess(key, proc, transport, "session-low", "low", "agent-bin")

	if _, busy2 := pool.ClaimForTurn(t.Context(), key, "high"); !busy2 {
		t.Fatal("second concurrent claim must be refused while the first holds the runtime")
	}
	if transport.closeCount() != 0 {
		t.Fatalf("first turn's transport must not be closed by the refused peer; close count = %d", transport.closeCount())
	}
	proc.mu.Lock()
	killed := proc.killed
	proc.mu.Unlock()
	if killed {
		t.Fatal("first turn's process must not be killed by the refused peer")
	}
	_ = first

	// Once the first turn releases, a peer can claim and reset a mismatched child.
	pool.Release(key)
	rt, busy3 := pool.ClaimForTurn(t.Context(), key, "high")
	if busy3 {
		t.Fatal("claim must succeed after the first turn releases")
	}
	if rt == nil || rt.HasProcess() {
		t.Fatal("post-release config-mismatch claim must clear the old process")
	}
}

// TestClaimForTurn_OnlyOneConcurrentClaimant drives many goroutines to claim the
// same runtime at once and verifies the atomic-claim invariant: exactly one
// claimant wins and the rest are rejected (busy), so the live child is killed
// at most once. This is the concurrency guarantee that makes the config-reset
// kill safe without a per-runtime lock.
func TestClaimForTurn_OnlyOneConcurrentClaimant(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	proc := newFakeProcess(t)
	transport := &closeTrackingTransport{}
	pool.SetProcess(key, proc, transport, "session-low", "low", "agent-bin")

	const n = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	var success, busy atomic.Int64
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cfg := "low"
			if i%2 == 0 {
				cfg = "high" // half the claimants want a different process config
			}
			<-start
			_, isBusy := pool.ClaimForTurn(t.Context(), key, cfg)
			if isBusy {
				busy.Add(1)
				return
			}
			success.Add(1)
		}(i)
	}
	close(start)
	wg.Wait()

	if got := success.Load(); got != 1 {
		t.Fatalf("expected exactly 1 successful claim, got %d", got)
	}
	if got := busy.Load(); got != n-1 {
		t.Fatalf("expected %d busy rejections, got %d", n-1, got)
	}
	if got := transport.closeCount(); got > 1 {
		t.Fatalf("live child must be killed at most once; transport close count = %d", got)
	}
}
