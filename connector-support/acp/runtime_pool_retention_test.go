package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"testing/synctest"
)

// Source: PR #1323 re-review. Mutant: prune at Release but never at completion.
func TestRetirement_PrunesReleasedEmptySlotsButRetainsOwnership(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{MaxRuntimes: 8})
		var blocked []*blockedTransport
		for i := range 32 {
			key := RuntimeKey{ClientSession: fmt.Sprint(i)}
			transport := newBlockedTransport()
			pool.SetProcess(key, newFakeProcess(t), transport, "session", "", "agent")
			if _, busy := pool.ClaimForTurn(t.Context(), key, ""); busy {
				t.Fatal("claim refused")
			}
			pool.DetachRuntime(key)
			<-transport.entered
			pool.Release(key)
			blocked = append(blocked, transport)
		}
		failed := RuntimeKey{ClientSession: "incomplete"}
		proc := newFakeProcess(t)
		proc.waitErr = ErrCleanupIncomplete
		pool.SetProcess(failed, proc, nil, "session", "", "agent")
		pool.DetachRuntime(failed)
		pool.Release(failed)

		held := RuntimeKey{ClientSession: "held"}
		transport := newBlockedTransport()
		pool.SetProcess(held, newFakeProcess(t), transport, "session", "", "agent")
		if _, busy := pool.ClaimForTurn(t.Context(), held, ""); busy {
			t.Fatal("held claim refused")
		}
		pool.DetachRuntime(held)
		<-transport.entered
		for _, transport := range blocked {
			transport.unblock()
		}
		transport.unblock()
		synctest.Wait()
		pool.mu.Lock()
		retained := len(pool.pools)
		pool.mu.Unlock()
		if retained != 2 {
			t.Errorf("retained slots=%d, want only incomplete and held", retained)
		}
		if rt := pool.Get(failed); rt == nil || !errors.Is(rt.cleanupOutcome(), ErrCleanupIncomplete) {
			t.Error("lost incomplete cleanup ownership")
		}
		if rt := pool.Get(held); rt == nil || !rt.IsInUse() {
			t.Error("pruned a claimed slot")
		}
		pool.Release(held)
		if pool.Get(held) != nil {
			t.Error("released empty slot survived")
		}
		if err := pool.Close(); !errors.Is(err, ErrCleanupIncomplete) {
			t.Fatalf("shutdown lost cleanup evidence: %v", err)
		}
	})
}

// Source: canceled-before-claim requests never own a turn that could Release.
func TestOpen_CanceledBeforeClaimPrunesItsEmptySlot(t *testing.T) {
	backend := NewSubprocessBackend(SubprocessBackendConfig{
		Protocol:       &fakeTurnProtocol{model: "agent"},
		Workspace:      WorkspacePolicy{DefaultDir: os.TempDir()},
		Pool:           RuntimePoolConfig{MaxRuntimes: 8},
		ProcessStarter: &fakeStarter{proc: newFakeProcess(t)},
	})
	t.Cleanup(func() { _ = backend.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for i := range 32 {
		if _, err := backend.Open(ctx, newTurnCall(fmt.Sprint(i))); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Open: %v", err)
		}
	}
	concrete, ok := backend.(*subprocessBackend)
	if !ok {
		t.Fatalf("backend type: %T", backend)
	}
	pool := concrete.pool
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if len(pool.pools) != 0 {
		t.Fatalf("canceled requests retained %d empty entries", len(pool.pools))
	}
}
