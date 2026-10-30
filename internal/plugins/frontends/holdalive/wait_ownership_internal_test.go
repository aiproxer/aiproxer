package holdalive

// Task 1 (large-payload-lifecycle-correctness): late disposal after the grace
// bound needs no canceled caller and no additional cleanup goroutine
// (Requirement 2.2).

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type graceOwnable struct {
	closed atomic.Int32
}

func (o *graceOwnable) Close() {
	o.closed.Add(1)
}

//nolint:paralleltest // stubs package-level cancelDrainGrace
func TestLargePayloadLifecycle_HoldaliveLateDisposalAfterGraceNeedsNoCaller(t *testing.T) {
	orig := cancelDrainGrace
	cancelDrainGrace = 20 * time.Millisecond
	t.Cleanup(func() { cancelDrainGrace = orig })

	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	res := &graceOwnable{}
	disposed := make(chan struct{}, 8)
	discard := func(v *graceOwnable) {
		if v != nil {
			v.Close()
		}
		select {
		case disposed <- struct{}{}:
		default:
		}
	}
	fnStarted := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		_, err := Wait(ctx, &recordingWriter{}, Config{Enabled: true, Interval: time.Hour},
			func(context.Context) (*graceOwnable, error) {
				close(fnStarted)
				<-release // ignores ctx; outlives the grace bound
				return res, nil
			}, discard)
		returned <- err
	}()

	select {
	case <-fnStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("producer did not start")
	}
	cancel()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after the drain grace")
	}
	// The caller is gone while execution is still blocked: nothing may have
	// been disposed yet, and no receiver remains for the eventual result.
	select {
	case <-disposed:
		t.Fatal("result disposed before execution returned")
	default:
	}
	// Late completion is still disposed exactly once with no caller waiting.
	close(release)
	select {
	case <-disposed:
	case <-time.After(10 * time.Second):
		t.Fatal("late result was not disposed after the caller left")
	}
	if got := res.closed.Load(); got != 1 {
		t.Fatalf("late result closes=%d want exactly 1", got)
	}
}
