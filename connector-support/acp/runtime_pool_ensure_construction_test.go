package acp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// TestEnsureProcess_CanceledLeaderStaysAttachedToSpawn proves that cancelling
// the caller that owns an in-flight construction does not detach that
// construction: the leader returns only after Spawn has finished, and the
// capacity reservation it held is released once it is done.
func TestEnsureProcess_CanceledLeaderStaysAttachedToSpawn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{MaxRuntimes: 1, ClaimWait: time.Second})
		t.Cleanup(func() { _ = pool.Close() })

		key := RuntimeKey{ClientSession: "canceled-leader"}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		spawnEntered := make(chan struct{})
		releaseSpawn := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(releaseSpawn) })
		defer unblock()

		leaderProc := newFakeProcess(t)
		leaderTransport := &closeTrackingTransport{}
		leaderDone := make(chan error, 1)
		go func() {
			_, err := pool.EnsureProcess(ctx, key, SpawnHandshakeStrategy{
				Spawn: func() ([]string, Process, Transport, error) {
					close(spawnEntered)
					<-releaseSpawn
					return []string{"agent-bin"}, leaderProc, leaderTransport, nil
				},
				Handshake: func(context.Context, Transport) (string, error) {
					t.Error("cancelled leader must not run the handshake")
					return "", errors.New("unexpected handshake")
				},
				LogPrefix: "test-canceled-leader",
			})
			leaderDone <- err
		}()

		<-spawnEntered
		cancel()
		synctest.Wait()

		select {
		case err := <-leaderDone:
			t.Fatalf("cancelled leader returned while Spawn was still blocked: %v", err)
		default:
		}

		unblock()
		if err := <-leaderDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("leader err = %v, want context.Canceled", err)
		}

		// The construction reservation must be released: with one runtime
		// authorized, a different key can spawn after the cancelled leader is
		// done, waiting only for that leader's refused child to retire.
		other := RuntimeKey{ClientSession: "after-canceled-leader"}
		otherProc := newFakeProcess(t)
		otherTransport := &closeTrackingTransport{}
		res, err := pool.EnsureProcess(context.Background(), other, SpawnHandshakeStrategy{
			Spawn: func() ([]string, Process, Transport, error) {
				return []string{"agent-bin"}, otherProc, otherTransport, nil
			},
			Handshake: func(context.Context, Transport) (string, error) {
				return "session-after-cancel", nil
			},
			LogPrefix: "test-after-canceled-leader",
		})
		if err != nil {
			t.Fatalf("second key EnsureProcess after cancelled leader: %v", err)
		}
		if res.SessionID != "session-after-cancel" {
			t.Fatalf("second key SessionID = %q, want session-after-cancel", res.SessionID)
		}
	})
}

// TestEnsureProcess_CloseJoinsBlockedConstructionAndCleanup proves shutdown
// does not linearize while an elected leader is still constructing or while
// the cleanup of its refused child is still running.
func TestEnsureProcess_CloseJoinsBlockedConstructionAndCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{})
		t.Cleanup(func() { _ = pool.Close() })

		key := RuntimeKey{ClientSession: "close-during-construction"}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		spawnEntered := make(chan struct{})
		releaseSpawn := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(releaseSpawn) })
		defer unblock()

		proc := newFakeProcess(t)
		transport := newBlockedTransport()
		unblockTransport := sync.OnceFunc(transport.unblock)
		defer unblockTransport()

		leaderDone := make(chan error, 1)
		go func() {
			_, err := pool.EnsureProcess(ctx, key, SpawnHandshakeStrategy{
				Spawn: func() ([]string, Process, Transport, error) {
					close(spawnEntered)
					<-releaseSpawn
					return []string{"agent-bin"}, proc, transport, nil
				},
				Handshake: func(context.Context, Transport) (string, error) {
					t.Error("cancelled leader must not run the handshake")
					return "", errors.New("unexpected handshake")
				},
				LogPrefix: "test-close-construction",
			})
			leaderDone <- err
		}()

		<-spawnEntered
		cancel()
		synctest.Wait()

		closeDone := make(chan error, 1)
		go func() { closeDone <- pool.Close() }()
		synctest.Wait()
		select {
		case err := <-closeDone:
			t.Fatalf("Close returned while a construction was still blocked: %v", err)
		default:
		}

		unblock()
		<-transport.entered
		synctest.Wait()
		select {
		case err := <-closeDone:
			t.Fatalf("Close returned before the refused child's cleanup finished: %v", err)
		default:
		}

		unblockTransport()
		if err := <-closeDone; err != nil {
			t.Fatalf("Close after construction cleanup: %v", err)
		}
		if err := <-leaderDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("leader err = %v, want context.Canceled", err)
		}
	})
}
