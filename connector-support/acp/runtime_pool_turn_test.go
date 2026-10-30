package acp

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// TestClaimForTurn_WaitsForTheRunningTurnToRelease proves the bounded wait: a
// turn that arrives while the previous turn of the same conversation still
// holds the runtime waits for the release instead of failing immediately.
func TestClaimForTurn_WaitsForTheRunningTurnToRelease(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{ClaimWait: 30 * time.Second})
		key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
		if _, busy := pool.ClaimForTurn(t.Context(), key, ""); busy {
			t.Fatal("the first claim must succeed")
		}

		claimed := make(chan bool, 1)
		go func() {
			_, busy := pool.ClaimForTurn(t.Context(), key, "")
			claimed <- busy
		}()
		synctest.Wait()

		select {
		case busy := <-claimed:
			t.Fatalf("the claim was refused while the turn was in flight: busy=%v", busy)
		default:
		}

		pool.Release(key)
		synctest.Wait()
		if busy := <-claimed; busy {
			t.Fatal("the claim was refused after the turn released the runtime")
		}
	})
}

// TestClaimForTurn_ClaimWaitIsBounded proves the wait is bounded: a turn that
// never releases is refused instead of waiting forever.
func TestClaimForTurn_ClaimWaitIsBounded(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{ClaimWait: 30 * time.Second})
		key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
		if _, busy := pool.ClaimForTurn(t.Context(), key, ""); busy {
			t.Fatal("the first claim must succeed")
		}

		claimed := make(chan bool, 1)
		go func() {
			_, busy := pool.ClaimForTurn(t.Context(), key, "")
			claimed <- busy
		}()
		synctest.Wait()

		time.Sleep(31 * time.Second)
		synctest.Wait()
		if busy := <-claimed; !busy {
			t.Fatal("the claim succeeded although the running turn never released")
		}
	})
}

// TestClaimForTurn_UnusableRuntimeIsNotWaitable proves the honest distinction:
// a runtime whose cleanup was not confirmed can never serve a turn, so the
// claim is refused at once instead of waiting for a release that never comes.
func TestClaimForTurn_UnusableRuntimeIsNotWaitable(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{ClaimWait: 30 * time.Second})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	proc := newFakeProcess(t)
	proc.waitErr = ErrCleanupIncomplete
	pool.SetProcess(key, proc, newStdioTransport(proc, nil), "session", "", "agent-bin")
	if err := pool.KillRuntime(key); err == nil {
		t.Fatal("the incomplete cleanup was not reported")
	}

	rt, outcome := pool.claimForTurn(t.Context(), key, "")
	if outcome != claimUnusable {
		t.Fatalf("claim outcome = %v, want claimUnusable", outcome)
	}
	if rt == nil {
		t.Fatal("expected a runtime for the refused claim")
	}
}

// blockingTransport is a Transport whose Close blocks until it is released.
type blockingTransport struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingTransport() *blockingTransport {
	return &blockingTransport{entered: make(chan struct{}), release: make(chan struct{})}
}

func (t *blockingTransport) CallUnary(context.Context, []byte, int) ([]byte, error) {
	return nil, nil
}

func (t *blockingTransport) CallPromptStream(context.Context, []byte) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}

func (t *blockingTransport) SendJSONRPC(context.Context, []byte) error { return nil }

func (t *blockingTransport) Close() error {
	t.once.Do(func() { close(t.entered) })
	<-t.release
	return nil
}

func (t *blockingTransport) unblock() { close(t.release) }

// TestPoolManagedStream_CloseReleasesTheClaimWhileRetirementBlocks is the
// regression for the poisoned runtime: a retirement that blocks must not keep
// the runtime claimed, because the next turn of the same conversation would
// then be refused for the lifetime of the instance.
func TestPoolManagedStream_CloseReleasesTheClaimWhileRetirementBlocks(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{})
		key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
		if _, err := pool.Acquire(key); err != nil {
			t.Fatal(err)
		}
		transport := newBlockingTransport()
		pool.SetProcess(key, newFakeProcess(t), transport, "session", "", "agent-bin")
		if _, busy := pool.ClaimForTurn(t.Context(), key, ""); busy {
			t.Fatal("the first claim must succeed")
		}

		inner := newPromptNDJSONStream(
			context.Background(),
			io.NopCloser(strings.NewReader("")),
			&client{},
			"sess",
			1,
			"msg1",
			SessionUpdateMapperOptions{},
			nil,
			CancelProfile{},
			0,
		)
		stream := &poolManagedStream{inner: inner, pool: pool, key: key, admission: true}

		closed := make(chan struct{})
		go func() {
			defer close(closed)
			_ = stream.Close()
		}()
		synctest.Wait()

		<-transport.entered
		if rt := pool.Get(key); rt == nil || rt.IsInUse() {
			t.Fatal("the blocked retirement kept the runtime claimed")
		}

		transport.unblock()
		synctest.Wait()
		<-closed
	})
}

// TestClaimForTurn_LegacyInUseMarkerIsNotWaited proves the wait never spins on
// a claim that cannot signal its release: the legacy in-use marker carries no
// release signal, so the claim is refused at once.
func TestClaimForTurn_LegacyInUseMarkerIsNotWaited(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{ClaimWait: 30 * time.Second})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
	if _, err := pool.Acquire(key); err != nil {
		t.Fatal(err)
	}
	pool.MarkInUse(key)
	claimed := make(chan bool, 1)
	go func() {
		_, busy := pool.ClaimForTurn(t.Context(), key, "")
		claimed <- busy
	}()
	select {
	case busy := <-claimed:
		if !busy {
			t.Fatal("the claim succeeded although the runtime was marked in use")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the claim waited for a release that can never arrive")
	}
}

// TestRuntimePool_MaxRuntimesRetiresTheOldestIdleRuntime proves the pool
// bound: a per-request key must not grow the pool without limit, and the
// least recently active idle runtime is the one that is retired.
func TestRuntimePool_MaxRuntimesRetiresTheOldestIdleRuntime(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{MaxRuntimes: 2, ClaimWait: 5 * time.Second})
	t.Cleanup(func() { _ = pool.Close() })

	oldest := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "oldest"}
	middle := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "middle"}
	newest := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "newest"}

	oldestTransport := &closeTrackingTransport{}
	pool.SetProcess(oldest, newFakeProcess(t), oldestTransport, "session", "", "agent-bin")
	pool.Release(oldest)
	pool.SetProcess(middle, newFakeProcess(t), &closeTrackingTransport{}, "session", "", "agent-bin")
	pool.Release(middle)

	if _, err := pool.Acquire(newest); err != nil {
		t.Fatal(err)
	}
	if err := pool.reserveCapacity(t.Context(), newest); err != nil {
		t.Fatalf("reserve capacity: %v", err)
	}
	// The eviction retires the victim in a pool-owned goroutine: the bound is
	// never enforced by blocking the request on a process-tree cleanup.
	waitFor(t, 0, 0, "the oldest idle runtime is retired", func() bool {
		return oldestTransport.closeCount() == 1
	})
	if pool.Get(oldest) != nil {
		t.Fatal("the evicted runtime is still owned by the pool")
	}
	if pool.Get(middle) == nil || pool.Get(newest) == nil {
		t.Fatal("the bound retired a runtime that was not the oldest idle one")
	}
}
