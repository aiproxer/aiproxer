package runtime_test

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type openTrackingStream struct {
	cancelCalls atomic.Int64
	closeCalls  atomic.Int64
}

func (s *openTrackingStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *openTrackingStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCalls.Add(1)
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

func (s *openTrackingStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func TestRED_CancelLinearization_DelayedParallelArmLaunchesAfterALegCancel(t *testing.T) {
	t.Parallel()

	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
	ex.ALegLifecycle = coord

	primaryOpenEntered := make(chan struct{})
	var hedgedOpenCalls atomic.Int64
	hedgedOpenStarted := make(chan struct{})
	authIDCh, sendAuthID := captureAuthoritativeID()

	ex.Backends = map[string]execbackend.Backend{
		"primary": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				sendAuthID(call)
				close(primaryOpenEntered)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
		"hedged": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				sendAuthID(call)
				hedgedOpenCalls.Add(1)
				select {
				case hedgedOpenStarted <- struct{}{}:
				default:
				}
				return &openTrackingStream{}, nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)

	call := parallelCall("[handicap=1]primary:model!hedged:model")
	call.Session.ALegID = "aleg-lin-1"

	go func() {
		_, _ = ex.Execute(context.Background(), call)
	}()

	select {
	case <-primaryOpenEntered:
	case <-time.After(time.Second):
		t.Fatal("primary Open was not entered")
	}

	targetID := requireAuthoritativeID(t, authIDCh)
	if err := coord.CancelALeg(context.Background(), targetID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if got := hedgedOpenCalls.Load(); got > 0 {
		t.Fatalf("delayed parallel branch invoked Backend.Open %d times after explicit A-leg cancellation", got)
	}
}

func TestRED_CancelLinearization_AuthoritativePreCancelBlocksLaunch(t *testing.T) {
	t.Parallel()

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
	const authID = "auth-lin-2"
	if err := coord.CancelALeg(context.Background(), authID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}
	aScope := coord.StartALeg(authID)
	if _, _, err := aScope.BeginBLegLaunch(context.Background(), "b-test"); err == nil {
		t.Fatal("expected BeginBLegLaunch to fail on pre-canceled authoritative A-leg")
	}
}

func TestRED_CancelLinearization_CancelDuringBlockedOpenDoesNotReachContext(t *testing.T) {
	t.Parallel()

	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
	ex.ALegLifecycle = coord

	openStarted := make(chan struct{})
	var openCtxDone atomic.Bool
	authIDCh, sendAuthID := captureAuthoritativeID()

	ex.Backends = map[string]execbackend.Backend{
		"block-b": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				sendAuthID(call)
				close(openStarted)
				select {
				case <-ctx.Done():
					openCtxDone.Store(true)
					return nil, ctx.Err()
				case <-time.After(200 * time.Millisecond):
					return &openTrackingStream{}, nil
				}
			},
		},
	}

	call := parallelCall("block-b:model")
	call.Session.ALegID = "aleg-lin-3"

	go func() {
		_, _ = ex.Execute(context.Background(), call)
	}()

	select {
	case <-openStarted:
	case <-time.After(time.Second):
		t.Fatal("Backend.Open was not started")
	}

	targetID := requireAuthoritativeID(t, authIDCh)
	if err := coord.CancelALeg(context.Background(), targetID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if !openCtxDone.Load() {
		t.Fatal("openCtx was not canceled during in-flight Backend.Open when A-leg was canceled")
	}
}

func TestCharacterization_CancelLinearization_LateOpenReturnsAfterCancelSettledOnceNotPublished(t *testing.T) {
	t.Parallel()

	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
	ex.ALegLifecycle = coord

	openStarted := make(chan struct{})
	unblockOpen := make(chan struct{})
	stream := &openTrackingStream{}
	authIDCh, sendAuthID := captureAuthoritativeID()

	ex.Backends = map[string]execbackend.Backend{
		"late-b": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				sendAuthID(call)
				close(openStarted)
				<-unblockOpen
				return stream, nil
			},
		},
	}

	call := parallelCall("late-b:model")
	call.Session.ALegID = "aleg-lin-4"

	streamCh := make(chan aipapi.EventStream, 1)
	errCh := make(chan error, 1)
	go func() {
		s, err := ex.Execute(context.Background(), call)
		streamCh <- s
		errCh <- err
	}()

	select {
	case <-openStarted:
	case <-time.After(time.Second):
		t.Fatal("Backend.Open was not started")
	}

	targetID := requireAuthoritativeID(t, authIDCh)
	if err := coord.CancelALeg(context.Background(), targetID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	close(unblockOpen)

	select {
	case s := <-streamCh:
		err := <-errCh
		if err == nil && s != nil {
			_, colErr := aipapi.Collect(context.Background(), s)
			if colErr == nil {
				t.Fatal("late open stream was unexpectedly published and collectible")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("Execute did not complete after late open unblocked")
	}

	if stream.cancelCalls.Load() == 0 && stream.closeCalls.Load() == 0 {
		t.Fatal("late returned stream was never terminalized")
	}
}
