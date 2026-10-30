package runtimehost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// ErrNoActiveExecutor is returned when Execute cannot acquire an active generation
// executor (host shutting down or empty publication).
var ErrNoActiveExecutor = errors.New("runtimehost: no active generation executor")

// ExecutorProvider is optionally implemented by PublishedRequestPlane values so
// the stable GenerationExecutor can dispatch without importing runtimebundle.
type ExecutorProvider interface {
	ExecutorView() aipsdk.ExecutorView
}

// GenerationExecutor is the stable, process-owned ExecutorView facade.
// Each Execute acquires the current generation, delegates to that generation's
// executor, and pins the returned stream until terminal/error/EOF/Close
// (req 16.12). CancelALeg reaches process-owned cross-generation A-leg state
// via the active generation executor's shared lifecycle (req 16.13).
type GenerationExecutor struct {
	manager *Manager
	clock   func() time.Time
}

// NewGenerationExecutor returns a stable delegating ExecutorView backed by m.
func NewGenerationExecutor(m *Manager) *GenerationExecutor {
	return &GenerationExecutor{manager: m}
}

// SetWallClock installs an optional wall-clock callback for response metadata.
func (e *GenerationExecutor) SetWallClock(clock func() time.Time) {
	if e == nil {
		return
	}
	e.clock = clock
}

var _ aipsdk.ExecutorView = (*GenerationExecutor)(nil)

// Execute acquires the active generation, runs the generation executor, and
// transfers a provider pin onto the returned stream until completion/close.
func (e *GenerationExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	if e == nil || e.manager == nil {
		return nil, ErrNoActiveExecutor
	}
	lease, ok := e.manager.Acquire()
	if !ok {
		return nil, ErrNoActiveExecutor
	}
	view, err := executorFromLease(lease)
	if err != nil {
		lease.Release()
		return nil, err
	}
	stream, execErr := view.Execute(ctx, call)
	if execErr != nil {
		lease.Release()
		return nil, execErr
	}
	if stream == nil {
		lease.Release()
		return nil, nil
	}
	pin, ok := lease.TransferPin(PinProvider)
	if !ok {
		_ = stream.Close()
		lease.Release()
		return nil, fmt.Errorf("%w: pin transfer failed", ErrNoActiveExecutor)
	}
	return &pinnedEventStream{inner: stream, pin: pin}, nil
}

// CancelALeg acquires the current generation and delegates cancellation so the
// process-owned A-leg lifecycle (shared across generations) is reached.
func (e *GenerationExecutor) CancelALeg(ctx context.Context, req aipapi.ALegCancelRequest) error {
	if e == nil || e.manager == nil {
		return ErrNoActiveExecutor
	}
	lease, ok := e.manager.Acquire()
	if !ok {
		return ErrNoActiveExecutor
	}
	defer lease.Release()
	view, err := executorFromLease(lease)
	if err != nil {
		return err
	}
	return view.CancelALeg(ctx, req)
}

// WallClock returns the optional wall-clock callback.
func (e *GenerationExecutor) WallClock() func() time.Time {
	if e == nil {
		return nil
	}
	return e.clock
}

func executorFromLease(lease *Lease) (aipsdk.ExecutorView, error) {
	if lease == nil {
		return nil, ErrNoActiveExecutor
	}
	plane := lease.RequestPlane()
	provider, ok := plane.(ExecutorProvider)
	if !ok || provider == nil {
		return nil, ErrNoActiveExecutor
	}
	view := provider.ExecutorView()
	if view == nil {
		return nil, ErrNoActiveExecutor
	}
	return view, nil
}

// pinnedEventStream retains a generation pin until Recv hits EOF/error or Close.
type pinnedEventStream struct {
	inner aipapi.EventStream
	pin   *Pin
	once  sync.Once
}

func (s *pinnedEventStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s == nil || s.inner == nil {
		s.release()
		return aipapi.Event{}, io.EOF
	}
	ev, err := s.inner.Recv(ctx)
	if err != nil {
		s.release()
	}
	return ev, err
}

func (s *pinnedEventStream) Close() error {
	var err error
	if s != nil && s.inner != nil {
		err = s.inner.Close()
	}
	s.release()
	return err
}

func (s *pinnedEventStream) release() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.pin != nil {
			s.pin.Release()
			s.pin = nil
		}
	})
}
