package runtime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestExecutor_backendSeamRegression locks introduce-hexagonal-architecture task 3.3:
// executor policy (capability gate, pre-output recovery, no post-output failover) with
// backends supplied only as [execbackend.Backend] test doubles — no concrete backend plugins.
func TestExecutor_backendSeamRegression(t *testing.T) {
	t.Parallel()

	t.Run("capability reject before backend open", func(t *testing.T) {
		t.Parallel()
		st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var opens int32
		ex := runtime.TestExecutor()
		ex.Store = st
		ex.Bus = hooks.New(hooks.Config{})
		ex.Rand = routing.NewSeededRng(1)
		ex.Backends = map[string]execbackend.Backend{
			"nope": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					atomic.AddInt32(&opens, 1)
					return nil, errors.New("must not open")
				},
			},
		}
		call := &aipapi.Call{
			Route: aipapi.RouteIntent{Selector: "nope:g"},
			Messages: []aipapi.Message{{
				Role: aipapi.RoleUser,
				Parts: []aipapi.Part{{
					Kind:      aipapi.PartImageRef,
					ImageRef:  "https://example.com/x.png",
					ImageMIME: "image/png",
				}},
			}},
		}
		_, err = ex.Execute(context.Background(), call)
		if err == nil {
			t.Fatal("expected error")
		}
		if !aipapi.IsReject(err) {
			t.Fatalf("expected capability reject, got %T %v", err, err)
		}
		if atomic.LoadInt32(&opens) != 0 {
			t.Fatalf("backend must not open, opens=%d", opens)
		}
	})

	t.Run("pre-output recovery uses second backend", func(t *testing.T) {
		t.Parallel()
		st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ex := runtime.TestExecutor()
		ex.Store = st
		ex.Bus = hooks.New(hooks.Config{})
		ex.Rand = routing.NewSeededRng(1)
		ex.Backends = map[string]execbackend.Backend{
			"bad": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
				},
			},
			"ok": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					return aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventResponseFinished},
					}), nil
				},
			},
		}
		call := &aipapi.Call{
			Route: aipapi.RouteIntent{Selector: "bad:m|ok:m"},
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("hi")},
			}},
		}
		s, err := ex.Execute(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := aipapi.Collect(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("no second backend open after output started", func(t *testing.T) {
		t.Parallel()
		st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var opens int32
		ex := runtime.TestExecutor()
		ex.Store = st
		ex.Bus = hooks.New(hooks.Config{})
		ex.Rand = routing.NewSeededRng(1)
		ex.Backends = map[string]execbackend.Backend{
			"one": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					atomic.AddInt32(&opens, 1)
					return &deltaThenErrStream{n: 0}, nil
				},
			},
			"two": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					atomic.AddInt32(&opens, 1)
					return aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventResponseFinished},
					}), nil
				},
			},
		}
		call := &aipapi.Call{
			Route: aipapi.RouteIntent{Selector: "one:m|two:m"},
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("hi")},
			}},
		}
		stream, err := ex.Execute(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		for range 3 {
			if _, err := stream.Recv(ctx); err != nil {
				t.Fatalf("unexpected recv error: %v", err)
			}
		}
		if _, err := stream.Recv(ctx); err == nil {
			t.Fatal("expected error after committed output")
		}
		if aipapi.IsRecoverablePreOutput(err) {
			t.Fatal("post-output failure must not classify as recoverable pre-output for retry")
		}
		if atomic.LoadInt32(&opens) != 1 {
			t.Fatalf("expected no failover backend open, opens=%d", opens)
		}
		_ = stream.Close()
	})
}
