package acp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestEnsureProcess_WaiterContextCancelUnblocks guards the singleflight waiter
// path: a waiter whose context expires while a peer flight is blocked must stop
// waiting instead of blocking until the peer flight completes.
func TestEnsureProcess_WaiterContextCancelUnblocks(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/cancel-waiter", Model: "agent", ClientSession: "s1"}
	if _, err := pool.Acquire(key); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	handshakeStarted := make(chan struct{})
	unblockHandshake := make(chan struct{})
	unblockOnce := sync.OnceFunc(func() { close(unblockHandshake) })
	defer unblockOnce()

	leaderProc := newFakeProcess(t)
	leaderTransport := &closeTrackingTransport{}

	type leaderOut struct {
		res EnsureProcessResult
		err error
	}
	leaderDone := make(chan leaderOut, 1)
	go func() {
		res, err := pool.EnsureProcess(context.Background(), key, SpawnHandshakeStrategy{
			ProcessConfigKey: "cfg",
			Spawn: func() ([]string, Process, Transport, error) {
				return []string{"agent-bin"}, leaderProc, leaderTransport, nil
			},
			Handshake: func(ctx context.Context, tr Transport) (string, error) {
				close(handshakeStarted)
				select {
				case <-unblockHandshake:
					return "session-leader", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			},
			LogPrefix: "test-waiter-cancel-leader",
		})
		leaderDone <- leaderOut{res: res, err: err}
	}()

	select {
	case <-handshakeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("leader handshake did not start")
	}

	waiterCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	waiterDone := make(chan error, 1)
	go func() {
		_, err := pool.EnsureProcess(waiterCtx, key, SpawnHandshakeStrategy{
			ProcessConfigKey: "cfg",
			Spawn: func() ([]string, Process, Transport, error) {
				t.Error("waiter Spawn must not run while sharing the leader flight")
				return nil, nil, nil, errors.New("waiter spawn must not run")
			},
			Handshake: func(context.Context, Transport) (string, error) {
				t.Error("waiter Handshake must not run while sharing the leader flight")
				return "", errors.New("waiter handshake must not run")
			},
			LogPrefix: "test-waiter-cancel-waiter",
		})
		waiterDone <- err
	}()

	select {
	case err := <-waiterDone:
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter err = %v, want context deadline/canceled", err)
		}
		select {
		case out := <-leaderDone:
			t.Fatalf("leader completed before waiter cancellation (out=%+v); test did not isolate waiter blocking", out)
		default:
		}
	case <-time.After(time.Second):
		t.Fatal("waiter EnsureProcess did not return on context timeout while leader flight blocked (singleflight waiter ignores cancellation)")
	}

	unblockOnce()
	select {
	case out := <-leaderDone:
		if out.err != nil {
			t.Fatalf("leader EnsureProcess: %v", out.err)
		}
		if out.res.SessionID != "session-leader" {
			t.Fatalf("leader SessionID = %q, want session-leader", out.res.SessionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("leader did not complete after unblock")
	}
}
