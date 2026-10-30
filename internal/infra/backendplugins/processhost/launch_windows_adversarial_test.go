//go:build windows

package processhost_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"golang.org/x/sys/windows"
)

// TestWindowsProc_ConcurrentSignalKillClose proves concurrent termination and
// teardown never use a disposed handle.
//
// SignalKill holds the process mutex across the job and process termination,
// and Close disposes the handles under the same mutex after the reap. A
// SignalKill that snapshotted the handles and released the mutex before the
// native call could terminate an invalid or a reused handle once Close
// disposed them; that use-after-dispose surfaces as an invalid-handle error.
//
// The test overlaps termination, teardown and reap, rejects any escaping
// invalid-handle error, and then pins the close contract: the forced
// termination ends the connector with exit code 1, so the close that performs
// the reap reports it and the idempotent second close reports nil. That
// matches the Linux launcher, whose first close reports the kill and later
// closes are nil.
func TestWindowsProc_ConcurrentSignalKillClose(t *testing.T) {
	artifact := stageFixture(t, "flooder")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	proc, err := processhost.NewPlatformLauncher().Launch(ctx, processhost.LaunchSpec{
		Artifact:   artifact,
		Generation: 1,
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}

	// Overlap termination with teardown and reap. The kills hammer the same
	// mutex Close uses for disposal, so the F3 interleaving — snapshot,
	// dispose, use — is exercised continuously, not sequentially. A
	// use-after-dispose would surface as an invalid handle; it is recorded
	// in an atomic flag rather than a bounded channel, so the assertion
	// observes every occurrence instead of the errors the channel fits.
	stop := make(chan struct{})
	var invalidHandle atomic.Bool
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					if err := proc.SignalKill(); err != nil {
						if errors.Is(err, windows.ERROR_INVALID_HANDLE) {
							invalidHandle.Store(true)
						}
					}
				}
			}
		})
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- proc.Wait() }()

	// Close runs while the kills are in flight.
	time.Sleep(200 * time.Millisecond)
	closeErr := make(chan error, 2)
	go func() { closeErr <- proc.Close() }()
	go func() { closeErr <- proc.Close() }()
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()

	select {
	case werr := <-waitDone:
		if werr == nil || !strings.Contains(werr.Error(), "exit 1") {
			t.Fatalf("wait after forced termination: %v, want exit 1", werr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent termination did not settle")
	}
	first := <-closeErr
	second := <-closeErr
	// Exactly one close performs the reap and reports the forced exit; the
	// other is the idempotent second close and reports nil.
	reaped, idle := first, second
	if idle != nil {
		reaped, idle = second, first
	}
	if idle != nil {
		t.Fatalf("second close: %v, want nil", idle)
	}
	if reaped == nil || !strings.Contains(reaped.Error(), "exit 1") {
		t.Fatalf("reaping close: %v, want exit 1", reaped)
	}

	// No termination used a handle after disposal: that interleaving
	// surfaces as an invalid-handle error, and the flag records every
	// occurrence.
	if invalidHandle.Load() {
		t.Fatal("a termination used a disposed handle")
	}

	// Past the teardown the handles are gone: further calls settle without
	// touching anything.
	if err := proc.SignalKill(); err != nil {
		t.Fatalf("signal after close: %v, want nil", err)
	}
	if err := proc.Wait(); err == nil || !strings.Contains(err.Error(), "exit 1") {
		t.Fatalf("wait after close: %v, want the cached exit 1", err)
	}
}
