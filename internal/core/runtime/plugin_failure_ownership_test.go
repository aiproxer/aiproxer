package runtime_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// pluginFailStream emits optional committed text then a terminal recv error.
type pluginFailStream struct {
	committed bool
	err       error
	sent      bool
}

func (s *pluginFailStream) Recv(context.Context) (aipapi.Event, error) {
	if !s.sent && s.committed {
		s.sent = true
		return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, nil
	}
	if s.err != nil {
		err := s.err
		s.err = nil
		return aipapi.Event{}, err
	}
	return aipapi.Event{}, io.EOF
}
func (s *pluginFailStream) Close() error { return nil }
func (s *pluginFailStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeTransport}
}

func TestPreOutput_ClassifiedErrorFailsoverOnce(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 3
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return nil, aipapi.RecoverablePreOutputError(errors.New("plugin crash pre-output"))
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "ok"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	col, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "ok" {
		t.Fatalf("text=%q", col.Text.String())
	}
	if opens.Load() != 2 {
		t.Fatalf("opens=%d want 2 (one swallowed pre-output + success)", opens.Load())
	}
}

func TestPostOutput_NoReplayAfterCommit(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 3
	ex.Backends = map[string]execbackend.Backend{
		"one": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return &pluginFailStream{
					committed: true,
					err:       errors.New("provider fail after commit"),
				}, nil
			},
		},
		"two": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventTextDelta, Delta: "replay"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "one:m|two:m"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	ev, err := stream.Recv(context.Background())
	if err != nil || !aipapi.OutputCommitted(ev) {
		t.Fatalf("want committed text: %+v %v", ev, err)
	}
	_, err = stream.Recv(context.Background())
	if err == nil {
		t.Fatal("expected post-output failure")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatal("post-output must not be recoverable for host/core replay")
	}
	if opens.Load() != 1 {
		t.Fatalf("host/core must not open second attempt after commit: opens=%d", opens.Load())
	}
}

func TestCrash_PreOutputInvalidatesOnlyThatAttempt(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.MaxAttempts = 3
	ex.Backends = map[string]execbackend.Backend{
		"crash": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return &pluginFailStream{err: aipapi.RecoverablePreOutputError(errors.New("crash"))}, nil
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "alive"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "crash:m|ok:m"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	col, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "alive" {
		t.Fatalf("%q", col.Text.String())
	}
	if opens.Load() < 2 {
		t.Fatalf("expected crash attempt then later core-selected open, opens=%d", opens.Load())
	}
}
