package runtime_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestExecutorStreamRecovery_postOutputEOFEmitsWarningFinishNoRetry(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.StreamRecovery = streamrecovery.Config{Enabled: true, EmitWarning: true}
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "partial"},
				}), nil
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	var kinds []aipapi.EventKind
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		kinds = append(kinds, ev.Kind)
	}
	if opens.Load() != 1 {
		t.Fatalf("opens=%d want 1", opens.Load())
	}
	if len(kinds) < 2 || kinds[len(kinds)-2] != aipapi.EventWarning || kinds[len(kinds)-1] != aipapi.EventResponseFinished {
		t.Fatalf("tail kinds=%v", kinds)
	}
}

func TestExecutorStreamRecovery_preOutputIdleCancelsAndFailsOver(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	slow := &idleBlockingStream{}
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.StreamRecovery = streamrecovery.Config{Enabled: true, IdleTimeout: 5 * time.Millisecond}
	ex.Backends = map[string]execbackend.Backend{
		"slow": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return slow, nil
			},
		},
		"fast": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "slow:gpt-4|fast:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if opens.Load() != 2 {
		t.Fatalf("opens=%d want 2", opens.Load())
	}
	if !slow.cancelled.Load() || !slow.closed.Load() {
		t.Fatalf("slow stream cancelled=%v closed=%v, want both true", slow.cancelled.Load(), slow.closed.Load())
	}
}

func TestExecutorStreamRecovery_postOutputIdleEmitsWarningFinishNoRetry(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	idle := &idleAfterEventsStream{events: []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "partial"},
	}}
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.StreamRecovery = streamrecovery.Config{Enabled: true, IdleTimeout: 5 * time.Millisecond, EmitWarning: true}
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return idle, nil
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	var kinds []aipapi.EventKind
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		kinds = append(kinds, ev.Kind)
	}
	if opens.Load() != 1 {
		t.Fatalf("opens=%d want 1", opens.Load())
	}
	if len(kinds) < 2 || kinds[len(kinds)-2] != aipapi.EventWarning || kinds[len(kinds)-1] != aipapi.EventResponseFinished {
		t.Fatalf("tail kinds=%v", kinds)
	}
	if !idle.cancelled.Load() || !idle.closed.Load() {
		t.Fatalf("idle stream cancelled=%v closed=%v, want both true", idle.cancelled.Load(), idle.closed.Load())
	}
}

type idleBlockingStream struct {
	cancelled atomic.Bool
	closed    atomic.Bool
}

func (s *idleBlockingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	<-ctx.Done()
	return aipapi.Event{}, ctx.Err()
}

func (s *idleBlockingStream) Close() error {
	s.closed.Store(true)
	return nil
}

func (s *idleBlockingStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.cancelled.Store(true)
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

type idleAfterEventsStream struct {
	events    []aipapi.Event
	i         int
	cancelled atomic.Bool
	closed    atomic.Bool
}

func (s *idleAfterEventsStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.i < len(s.events) {
		ev := s.events[s.i]
		s.i++
		return ev, nil
	}
	<-ctx.Done()
	return aipapi.Event{}, ctx.Err()
}

func (s *idleAfterEventsStream) Close() error {
	s.closed.Store(true)
	return nil
}

func (s *idleAfterEventsStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.cancelled.Store(true)
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}
