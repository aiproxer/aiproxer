package extensions_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

// openGateWaitBound is the liveness bound of these regression tests. A regressing
// build blocks forever instead of returning, so any finite bound fails; a passing
// build returns as soon as the gate admits or the caller is canceled.
const openGateWaitBound = 10 * time.Second

// panickingModeFactory panics from a descriptor method. The session calls
// descriptor methods outside the isolated provider boundary, so the panic reaches
// the Open caller exactly as an unguarded provider panic would.
type panickingModeFactory struct{}

func (panickingModeFactory) ID() string { return "panicky-mode" }
func (panickingModeFactory) Order() int { return 0 }
func (panickingModeFactory) FailureMode() sdkhooks.FailureMode {
	panic("final stream factory failure mode exploded")
}

func (panickingModeFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return &fsObs{}, nil
}

// gateBlockingFactory holds one Open inside its provider call until release is
// closed, so a test controls exactly who owns the session's opening gate.
type gateBlockingFactory struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *gateBlockingFactory) ID() string                        { return "gate-blocking-open" }
func (f *gateBlockingFactory) Order() int                        { return 0 }
func (f *gateBlockingFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (f *gateBlockingFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	f.once.Do(func() { close(f.entered) })
	<-f.release
	return &fsObs{}, nil
}

// TestFinalStreamObservationOpen_ReleasesGateAfterFactoryPanic pins that an
// unexpected unwind inside Open cannot latch the session's opening gate. The
// panic still reaches the caller (panic isolation belongs to the caller's
// boundary), but every later Open on the same session must complete: a latched
// gate waits on a cond with no owner and no cancellation, so the request path
// would hang forever.
func TestFinalStreamObservationOpen_ReleasesGateAfterFactoryPanic(t *testing.T) {
	t.Parallel()
	sess := &extensions.FinalStreamObservationSession{}

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("Open did not propagate the factory panic")
			}
		}()
		_ = sess.Open(t.Context(), []response.StreamObserverFactory{
			panickingModeFactory{},
			fsFactory{id: "good", ord: 1, mode: sdkhooks.FailOpen, obs: &fsObs{}},
		}, response.StreamMeta{BLegID: "b1"}, response.Services{})
	}()

	opened := &fsObs{}
	done := make(chan error, 1)
	go func() {
		done <- sess.Open(t.Context(), []response.StreamObserverFactory{
			fsFactory{id: "good", mode: sdkhooks.FailOpen, obs: opened},
		}, response.StreamMeta{BLegID: "b1"}, response.Services{})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Open after the panicking Open failed: %v", err)
		}
	case <-time.After(openGateWaitBound):
		t.Fatal("Open never returned: the panicking Open left the opening gate latched with no owner")
	}

	// The recovered session must still serve the stage, not merely return.
	if err := extensions.RunFinalStreamObservationStage(t.Context(), nil, nil, sess, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, false); err != nil {
		t.Fatalf("observe after the panicking Open failed: %v", err)
	}
	if got := opened.n.Load(); got != 1 {
		t.Fatalf("observer saw %d events, want 1", got)
	}
}

// TestFinalStreamObservationOpen_WaitHonorsCallerCancellation pins that a caller
// waiting behind an in-flight Open can withdraw. The wait is admission, not
// execution: the caller's context owns it, so a canceled or expired caller must
// be told so instead of waiting for the in-flight Open to finish.
func TestFinalStreamObservationOpen_WaitHonorsCallerCancellation(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	sess := &extensions.FinalStreamObservationSession{}

	inFlight := make(chan error, 1)
	go func() {
		inFlight <- sess.Open(t.Context(), []response.StreamObserverFactory{
			&gateBlockingFactory{entered: entered, release: release},
		}, response.StreamMeta{BLegID: "b1"}, response.Services{})
	}()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	contending := make(chan error, 1)
	go func() {
		contending <- sess.Open(ctx, []response.StreamObserverFactory{
			fsFactory{id: "contending", mode: sdkhooks.FailOpen, obs: &fsObs{}},
		}, response.StreamMeta{BLegID: "b2"}, response.Services{})
	}()

	select {
	case err := <-contending:
		if !errors.Is(err, context.Canceled) {
			close(release)
			t.Fatalf("contending Open returned %v, want context.Canceled", err)
		}
	case <-time.After(openGateWaitBound):
		close(release)
		t.Fatal("contending Open ignored caller cancellation and waited for the in-flight Open")
	}
	close(release)
	if err := <-inFlight; err != nil {
		t.Fatalf("in-flight Open failed: %v", err)
	}
}
