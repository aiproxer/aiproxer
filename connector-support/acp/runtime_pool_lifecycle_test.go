package acp

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// blockedTransport is a Transport whose Close blocks until it is released. It
// models a supervised process whose extinction cannot be confirmed promptly.
type blockedTransport struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockedTransport() *blockedTransport {
	return &blockedTransport{entered: make(chan struct{}), release: make(chan struct{})}
}

func (t *blockedTransport) CallUnary(context.Context, []byte, int) ([]byte, error) {
	return nil, nil
}

func (t *blockedTransport) CallPromptStream(context.Context, []byte) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}

func (t *blockedTransport) SendJSONRPC(context.Context, []byte) error { return nil }

func (t *blockedTransport) Close() error {
	t.once.Do(func() { close(t.entered) })
	<-t.release
	return nil
}

func (t *blockedTransport) unblock() { close(t.release) }

// TestEviction_ClaimedRuntimeIsNeverRetired proves the eviction reservation
// is atomic with its selection: a runtime that a turn claimed is never
// retired by the capacity eviction, and its pool entry stays owned.
func TestEviction_ClaimedRuntimeIsNeverRetired(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{MaxRuntimes: 2, ClaimWait: 5 * time.Second})
	t.Cleanup(func() { _ = pool.Close() })

	claimed := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "claimed"}
	idle := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "idle"}
	claimedTransport := &closeTrackingTransport{}
	pool.SetProcess(claimed, newFakeProcess(t), claimedTransport, "session", "", "agent-bin")
	pool.SetProcess(idle, newFakeProcess(t), &closeTrackingTransport{}, "session", "", "agent-bin")
	pool.Release(claimed)
	pool.Release(idle)

	// The turn claims the runtime before the eviction runs.
	if _, busy := pool.ClaimForTurn(t.Context(), claimed, ""); busy {
		t.Fatal("the first claim must succeed")
	}
	other := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "other"}
	if _, err := pool.Acquire(other); err != nil {
		t.Fatal(err)
	}
	if err := pool.reserveCapacity(t.Context(), other); err != nil {
		t.Fatalf("reserve capacity: %v", err)
	}

	// The claimed runtime keeps its process and its pool entry.
	if claimedTransport.closeCount() != 0 {
		t.Fatalf("the eviction retired a claimed runtime: close count = %d", claimedTransport.closeCount())
	}
	rt := pool.Get(claimed)
	if rt == nil {
		t.Fatal("the claimed runtime lost its pool entry")
	}
	if !rt.HasProcess() {
		t.Fatal("the eviction took the claimed runtime's process")
	}
	if rt.IsInUse() != true {
		t.Fatal("the claimed runtime lost its claim")
	}
}

// TestEviction_ReservedRuntimeIsNotReused proves the reservation the eviction
// takes is visible to a concurrent claim: the reserved process is retired, and
// the claim never reuses it.
func TestEviction_ReservedRuntimeIsNotReused(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{MaxRuntimes: 1, ClaimWait: 5 * time.Second})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "reserved"}
	transport := &closeTrackingTransport{}
	pool.SetProcess(key, newFakeProcess(t), transport, "session", "", "agent-bin")
	pool.Release(key)

	// The eviction reserves the victim under the runtime lock.
	victim, ok := pool.reserveIdleVictim(RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "other"})
	if !ok {
		t.Fatal("expected an idle victim")
	}
	pool.retireAsync(victim.key, victim.rt, victim.detached, victim.done, true)

	// A concurrent claim must not reuse the reserved process.
	claimed, busy := pool.ClaimForTurn(t.Context(), key, "")
	if busy {
		t.Fatal("the claim must succeed after the reserved process was retired")
	}
	if claimed.HasProcess() {
		t.Fatal("the claim reused the reserved process")
	}
	waitFor(t, 0, 0, "the reserved process is retired", func() bool {
		return transport.closeCount() == 1
	})
}

// TestEviction_FailedCleanupKeepsTheSlot proves the fail-closed contract: a
// retirement that does not confirm process extinction keeps the slot, its
// evidence and its ownership, and the slot is never replaced.
func TestEviction_FailedCleanupKeepsTheSlot(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{MaxRuntimes: 1, ClaimWait: 5 * time.Second})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "quarantined"}
	proc := newFakeProcess(t)
	proc.waitErr = ErrCleanupIncomplete
	pool.SetProcess(key, proc, nil, "session", "", "agent-bin")
	pool.Release(key)

	other := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "other"}
	if _, err := pool.Acquire(other); err != nil {
		t.Fatal(err)
	}
	if err := pool.reserveCapacity(t.Context(), other); err == nil {
		t.Fatal("the capacity reservation ignored a runtime whose cleanup was not confirmed")
	}
	waitFor(t, 0, 0, "the failed retirement is recorded", func() bool {
		rt := pool.Get(key)
		if rt == nil {
			return false
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		return rt.quarantined
	})
	rt := pool.Get(key)
	if rt == nil {
		t.Fatal("the quarantined runtime lost its pool entry")
	}
	if !errors.Is(rt.cleanupOutcome(), ErrCleanupIncomplete) {
		t.Fatal("the quarantined runtime lost its cleanup evidence")
	}
}

// TestWithdrawal_SuccessorProcessSurvives proves a departing turn can never
// retire the process its successor published: the withdrawal detaches the
// incarnation it owned, the successor cannot publish before that retirement
// completes, and the departing turn's join only ever targets its own
// incarnation.
func TestWithdrawal_SuccessorProcessSurvives(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{ClaimWait: 30 * time.Second})
		t.Cleanup(func() { _ = pool.Close() })

		key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
		first := newBlockedTransport()
		pool.SetProcess(key, newFakeProcess(t), first, "session", "", "agent-bin")
		if _, busy := pool.ClaimForTurn(t.Context(), key, ""); busy {
			t.Fatal("the first claim must succeed")
		}

		// The turn withdraws: the incarnation is detached and the claim is
		// released, so a successor can claim the slot.
		pool.MarkWithdrawn(key)
		detached, ok := pool.DetachRuntime(key)
		if !ok {
			t.Fatal("expected a detached incarnation")
		}
		pool.Release(key)
		<-first.entered

		// The successor claims the slot while the retirement is still in
		// flight: it must wait for the retirement instead of reusing or
		// replacing the withdrawn process.
		claimed := make(chan bool, 1)
		go func() {
			_, busy := pool.ClaimForTurn(t.Context(), key, "")
			claimed <- busy
		}()
		synctest.Wait()
		select {
		case busy := <-claimed:
			t.Fatalf("the successor claim did not wait for the withdrawal: busy=%v", busy)
		default:
		}

		first.unblock()
		synctest.Wait()
		if busy := <-claimed; busy {
			t.Fatal("the successor claim was refused after the withdrawal completed")
		}

		// The successor publishes its own process; the departing turn's join
		// must not have retired it.
		successorTransport := &closeTrackingTransport{}
		pool.SetProcess(key, newFakeProcess(t), successorTransport, "session-2", "", "agent-bin")
		<-detached.done
		if successorTransport.closeCount() != 0 {
			t.Fatalf("the departing turn retired the successor's process: close count = %d", successorTransport.closeCount())
		}
		rt := pool.Get(key)
		if rt == nil || !rt.HasProcess() {
			t.Fatal("the successor lost its process")
		}
	})
}

// TestClaimWait_BlockedRetirementDoesNotBlockTheClaim proves a retirement that
// never completes cannot block the request path: the claim itself never waits
// for the retirement, and the spawn path observes the caller's deadline
// instead of hanging on the process tree.
func TestClaimWait_BlockedRetirementDoesNotBlockTheClaim(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{ClaimWait: 30 * time.Second})
		t.Cleanup(func() { _ = pool.Close() })

		key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
		transport := newBlockedTransport()
		pool.SetProcess(key, newFakeProcess(t), transport, "session", "", "agent-bin")
		pool.MarkWithdrawn(key)

		claimed := make(chan bool, 1)
		go func() {
			_, busy := pool.ClaimForTurn(t.Context(), key, "")
			claimed <- busy
		}()
		synctest.Wait()
		<-transport.entered

		select {
		case busy := <-claimed:
			if busy {
				t.Fatal("the claim was refused although the retirement runs in the background")
			}
		default:
			t.Fatal("the claim waited for the blocked retirement")
		}

		// The spawn path must observe the caller's deadline.
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		spawned := make(chan error, 1)
		go func() {
			_, err := pool.EnsureProcess(ctx, key, SpawnHandshakeStrategy{
				Spawn: func() ([]string, Process, Transport, error) {
					t.Error("a process was spawned although the previous incarnation is still retiring")
					return nil, nil, nil, errors.New("unexpected spawn")
				},
				Handshake: func(context.Context, Transport) (string, error) {
					return "", errors.New("unexpected handshake")
				},
			})
			spawned <- err
		}()
		synctest.Wait()
		if err := <-spawned; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the spawn path error = %v, want the caller's deadline", err)
		}
		transport.unblock()
	})
}

// TestClaimWait_CancellationStopsTheWait proves a cancelled request never
// acquires a claim: the wait observes the caller's context.
func TestClaimWait_CancellationStopsTheWait(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{ClaimWait: 30 * time.Second})
		t.Cleanup(func() { _ = pool.Close() })

		key := RuntimeKey{Workspace: "/tmp/p", Model: "agent", ClientSession: "s1"}
		if _, busy := pool.ClaimForTurn(t.Context(), key, ""); busy {
			t.Fatal("the first claim must succeed")
		}

		ctx, cancel := context.WithCancel(t.Context())
		claimed := make(chan bool, 1)
		go func() {
			_, busy := pool.ClaimForTurn(ctx, key, "")
			claimed <- busy
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if busy := <-claimed; !busy {
			t.Fatal("the cancelled request acquired a claim")
		}
	})
}

// TestToolSink_ConcurrentTurnsAreIsolated proves the tool-event state of one
// turn never leaks into another: two concurrent streams that use the same tool
// call id keep their own summaries, and one stream's end never flushes the
// other's open tool call.
func TestToolSink_ConcurrentTurnsAreIsolated(t *testing.T) {
	t.Parallel()
	first := selectToolSink(&legacyOptionsSpec{})
	second := selectToolSink(&legacyOptionsSpec{})

	update := map[string]any{"toolCallId": "tool-1", "title": "read_file", "status": "pending"}
	if _, err := first.HandleToolUpdate(context.Background(), acpToolCall, update); err != nil {
		t.Fatal(err)
	}
	if _, err := second.HandleToolUpdate(context.Background(), acpToolCall, update); err != nil {
		t.Fatal(err)
	}

	// The second stream ends with an open tool call: it flushes its own
	// summary and must not flush the first stream's state.
	flushed := second.FlushIncomplete()
	if len(flushed) != 1 {
		t.Fatalf("flushed events = %d, want 1", len(flushed))
	}
	if !strings.Contains(flushed[0].Delta, "read_file") {
		t.Fatalf("flushed summary lost the tool name: %q", flushed[0].Delta)
	}
	if again := second.FlushIncomplete(); len(again) != 0 {
		t.Fatalf("the second flush repeated the summary: %v", again)
	}

	// The first stream still owns its open tool call.
	events, err := first.HandleToolUpdate(context.Background(), acpToolCallUpdate, map[string]any{
		"toolCallId": "tool-1", "status": "completed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("the first stream lost its completion summary: %v", events)
	}
	if !strings.Contains(events[0].Delta, "read_file") {
		t.Fatalf("the first stream's summary lost the tool name: %q", events[0].Delta)
	}
}

// TestRuntimeIdentity_SameOpeningNeverSharesRuntimeState proves two requests
// that open with the same text but carry different request identities never
// share one agent runtime.
func TestRuntimeIdentity_SameOpeningNeverSharesRuntimeState(t *testing.T) {
	t.Parallel()
	first := &aipapi.Call{ID: "req-1", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
	second := &aipapi.Call{ID: "req-2", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
	if CallRuntimeIdentity(first) == CallRuntimeIdentity(second) {
		t.Fatal("two requests with the same opening resolved to one runtime identity")
	}
}
