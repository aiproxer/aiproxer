package runtime_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestExecutor_CancelALegCancelsActiveBLeg(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	inner := newExplicitCancelBlockingStream()
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.ALegLifecycle = leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
	ex.Backends = map[string]execbackend.Backend{
		"managed": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return inner, nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "explicit-cancel"},
		Route:   aipapi.RouteIntent{Selector: "managed:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := stream.Recv(context.Background())
		done <- err
	}()
	<-inner.ready

	if err := ex.CancelALeg(context.Background(), aipapi.ALegCancelRequest{ALegID: call.Session.ALegID}); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, leglifecycle.ErrALegCanceled) {
			t.Fatalf("Recv after explicit cancel = %v want ErrALegCanceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Recv remained blocked after explicit A-leg cancellation")
	}
	if got, want := inner.calls(), []string{"cancel:explicit", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("managed B-leg calls = %v want %v", got, want)
	}
	if _, err := stream.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv after stream finished = %v want EOF", err)
	}
}

func TestExecutor_CancelALegCancelsActiveBLegWithDefaultLifecycle(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	inner := newExplicitCancelBlockingStream()
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"managed": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return inner, nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "default-lifecycle-cancel"},
		Route:   aipapi.RouteIntent{Selector: "managed:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if call.Session.ALegID == "" {
		t.Fatal("executor did not assign A-leg id")
	}

	if err := ex.CancelALeg(context.Background(), aipapi.ALegCancelRequest{ALegID: call.Session.ALegID}); err != nil {
		t.Fatalf("CancelALeg with default lifecycle: %v", err)
	}

	if got, want := inner.calls(), []string{"cancel:explicit", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("managed B-leg calls = %v want %v", got, want)
	}
}

type explicitCancelBlockingStream struct {
	ready  chan struct{}
	closed chan struct{}
	readyO sync.Once
	closeO sync.Once
	mu     sync.Mutex
	log    []string
}

func newExplicitCancelBlockingStream() *explicitCancelBlockingStream {
	return &explicitCancelBlockingStream{ready: make(chan struct{}), closed: make(chan struct{})}
}

func (s *explicitCancelBlockingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	s.readyO.Do(func() { close(s.ready) })
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-s.closed:
		return aipapi.Event{}, leglifecycle.ErrALegCanceled
	}
}

func (s *explicitCancelBlockingStream) Cancel(_ context.Context, cause leglifecycle.CancelCause) leglifecycle.CancelResult {
	s.mu.Lock()
	s.log = append(s.log, "cancel:"+string(cause.Kind))
	s.mu.Unlock()
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeProvider}
}

func (s *explicitCancelBlockingStream) Close() error {
	s.closeO.Do(func() { close(s.closed) })
	s.mu.Lock()
	s.log = append(s.log, "close")
	s.mu.Unlock()
	return nil
}

func (s *explicitCancelBlockingStream) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}
