package leglifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type blockingBLeg struct {
	cancelStarted chan struct{}
	releaseBlock  chan struct{}
}

func (b *blockingBLeg) Cancel(ctx context.Context, cause CancelCause) CancelResult {
	if b.cancelStarted != nil {
		close(b.cancelStarted)
	}
	if b.releaseBlock != nil {
		select {
		case <-b.releaseBlock:
		case <-ctx.Done():
		}
	}
	return CancelResult{Mode: CancelModeProvider}
}

func (b *blockingBLeg) Close() error {
	return nil
}

func (b *blockingBLeg) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, nil
}

type signalingBLeg struct {
	cancelStarted chan struct{}
}

func (s *signalingBLeg) Cancel(ctx context.Context, cause CancelCause) CancelResult {
	if s.cancelStarted != nil {
		close(s.cancelStarted)
	}
	return CancelResult{Mode: CancelModeProvider}
}

func (s *signalingBLeg) Close() error {
	return nil
}

func (s *signalingBLeg) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, nil
}

func TestRED_SiblingFanOut_OneBlockedChildDelaysSiblingSignaling(t *testing.T) {
	t.Parallel()

	c := NewCoordinator(CoordinatorConfig{CancelTimeout: 2 * time.Second})
	a := c.StartALeg("a-fanout-1")

	b1Started := make(chan struct{})
	b1Release := make(chan struct{})
	b1 := &blockingBLeg{
		cancelStarted: b1Started,
		releaseBlock:  b1Release,
	}

	b2Started := make(chan struct{})
	b2 := &signalingBLeg{
		cancelStarted: b2Started,
	}

	if err := a.RegisterBLeg(context.Background(), BLegHandle{ID: "b1", Attempt: b1}); err != nil {
		t.Fatal(err)
	}
	if err := a.RegisterBLeg(context.Background(), BLegHandle{ID: "b2", Attempt: b2}); err != nil {
		t.Fatal(err)
	}

	cancelDone := make(chan error, 1)
	go func() {
		cancelDone <- c.CancelALeg(context.Background(), "a-fanout-1", CancelCause{Kind: CancelExplicit})
	}()

	// Wait for b1 to begin cancellation and enter blocking state.
	select {
	case <-b1Started:
	case <-time.After(1 * time.Second):
		t.Fatal("b1 cancel was not started within timeout")
	}

	// b2 should start cancellation concurrently without waiting for b1 to be released.
	select {
	case <-b2Started:
		// b2 cancel was triggered concurrently with b1.
	case <-time.After(100 * time.Millisecond):
		t.Fatal("sibling b2 was not signaled while b1 was blocked; ALeg.Cancel blocked serially on b1")
	}

	// Release b1 so the background cancel goroutine can complete.
	close(b1Release)
	select {
	case err := <-cancelDone:
		if err != nil {
			t.Fatalf("CancelALeg failed: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("CancelALeg did not finish after b1 release")
	}
}
