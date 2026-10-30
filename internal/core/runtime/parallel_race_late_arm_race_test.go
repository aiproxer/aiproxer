package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type lateArmStubStream struct {
	openedCh    chan struct{}
	releaseCh   <-chan struct{}
	cancelCount atomic.Int32
	closeCount  atomic.Int32
}

func (s *lateArmStubStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.openedCh != nil {
		select {
		case s.openedCh <- struct{}{}:
		default:
		}
	}
	select {
	case <-s.releaseCh:
		return aipapi.Event{}, context.Canceled
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}

func (s *lateArmStubStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCount.Add(1)
	return aipapi.CancelResult{}
}

func (s *lateArmStubStream) Close() error {
	s.closeCount.Add(1)
	return nil
}

type normalArmStubStream struct {
	openedCh    chan struct{}
	cancelCount atomic.Int32
	closeCount  atomic.Int32
}

func (s *normalArmStubStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.openedCh != nil {
		select {
		case s.openedCh <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	return aipapi.Event{}, ctx.Err()
}

func (s *normalArmStubStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCount.Add(1)
	return aipapi.CancelResult{}
}

func (s *normalArmStubStream) Close() error {
	s.closeCount.Add(1)
	return nil
}

func TestTryOpenParallelGroup_ContextDoneLateArmStillTerminalized(t *testing.T) {
	t.Parallel()

	auth := reservedAuthorityRecorder("res-late-arm")
	ex, _, _, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	fastOpenedCh := make(chan struct{}, 1)
	lateOpenedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{})
	defer func() {
		select {
		case <-releaseCh:
		default:
			close(releaseCh)
		}
	}()

	fastStream := &normalArmStubStream{openedCh: fastOpenedCh}
	lateStream := &lateArmStubStream{openedCh: lateOpenedCh, releaseCh: releaseCh}

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	tcaps := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
	})

	ex.Backends = map[string]execbackend.Backend{
		"fast-backend": {
			Caps:                    caps,
			TransportCaps:           tcaps,
			EnforcesMaxOutputTokens: true,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return fastStream, nil
			},
		},
		"late-backend": {
			Caps:                    caps,
			TransportCaps:           tcaps,
			EnforcesMaxOutputTokens: true,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return lateStream, nil
			},
		},
	}

	candFast := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fast-backend", Model: "m"},
		Key:     "fast-backend:m",
	}
	candLate := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "late-backend", Model: "m"},
		Key:     "late-backend:m",
	}

	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	req.reqFacts.aScope = aScope

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := ex.tryOpenParallelGroup(ctx, req, []routing.AttemptCandidate{candFast, candLate}, nil, "", false)
		done <- err
	}()

	select {
	case <-fastOpenedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("fast arm never reached receive loop")
	}

	select {
	case <-lateOpenedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("late arm never reached receive loop")
	}

	cancel()

	time.Sleep(150 * time.Millisecond)

	close(releaseCh)

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tryOpenParallelGroup did not return after release")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) &&
		(fastStream.cancelCount.Load() == 0 && fastStream.closeCount.Load() == 0 ||
			lateStream.cancelCount.Load() == 0 && lateStream.closeCount.Load() == 0) {
		time.Sleep(10 * time.Millisecond)
	}
	if fastStream.cancelCount.Load() == 0 && fastStream.closeCount.Load() == 0 {
		t.Errorf("expected fast arm to be terminalized (cancel or close)")
	}
	if lateStream.cancelCount.Load() == 0 && lateStream.closeCount.Load() == 0 {
		t.Errorf("expected late arm to be terminalized (cancel or close), got cancel=%d close=%d", lateStream.cancelCount.Load(), lateStream.closeCount.Load())
	}
}
