package runtime

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestHeartbeat_StopJoinsSynchronously proves lease heartbeat exit is joined by
// Stop (used by stopLeaseHeartbeat) without needing a redundant generation pin.
func TestHeartbeat_StopJoinsSynchronously(t *testing.T) {
	t.Parallel()
	h := newLeaseHeartbeat()
	var running atomic.Bool
	go func() {
		running.Store(true)
		defer func() {
			running.Store(false)
			close(h.doneCh)
		}()
		<-h.stopCh
		time.Sleep(30 * time.Millisecond)
	}()
	// Wait until loop is live.
	deadline := time.Now().Add(time.Second)
	for !running.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	started := time.Now()
	h.Stop()
	elapsed := time.Since(started)
	if running.Load() {
		t.Fatal("Stop returned while heartbeat still running")
	}
	if elapsed < 25*time.Millisecond {
		t.Fatalf("Stop returned too quickly (%v); expected synchronous join", elapsed)
	}
}
