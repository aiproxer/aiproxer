package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestExecutor_ExecutionCompositionSafety(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	open := func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	}

	backends := map[string]execbackend.Backend{
		"openai":    {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: open},
		"anthropic": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: open},
		"acp":       {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: open},
	}

	classes := map[string]aipsdk.BackendExecutionClass{
		"openai":    aipsdk.BackendExecutionInference,
		"anthropic": aipsdk.BackendExecutionInference,
		"acp":       aipsdk.BackendExecutionAgentRuntime,
	}

	resolver := routing.BackendExecutionResolverFunc(func(id string) (aipsdk.BackendExecutionClass, bool) {
		c, ok := classes[id]
		return c, ok
	})

	newExec := func(policy config.ExecutionCompositionPolicy) *runtime.Executor {
		return runtime.NewExecutor(runtime.ExecutorConfig{
			Core: runtime.CoreRuntime{
				Store:    st,
				Backends: backends,
				Rand:     routing.NewSeededRng(42),
			},
			Extension: runtime.ExtensionRuntime{
				Bus: hooks.New(hooks.Config{}),
			},
			Routing: runtime.RoutingRuntime{
				ExecutionCompositionPolicy: policy,
				BackendExecutionResolver:   resolver,
			},
		})
	}

	t.Run("safe_direct_acp_allowed", func(t *testing.T) {
		ex := newExec(config.ExecutionCompositionSafe)
		call := &aipapi.Call{
			Route:    aipapi.RouteIntent{Selector: "acp:claude-3-7-sonnet"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		}
		stream, err := ex.Execute(context.Background(), call)
		if err != nil {
			t.Fatalf("expected direct ACP to succeed, got %v", err)
		}
		defer func() { _ = stream.Close() }()
	})

	t.Run("safe_composite_acp_and_inference_rejected", func(t *testing.T) {
		ex := newExec(config.ExecutionCompositionSafe)
		tests := []struct {
			name     string
			selector string
		}{
			{name: "failover", selector: "openai:gpt-4o|acp:claude-3-7-sonnet"},
			{name: "first", selector: "[first]acp:claude-3-7-sonnet^[weight=1]openai:gpt-4o"},
			{name: "weighted", selector: "[weight=1]openai:gpt-4o^[weight=1]acp:claude-3-7-sonnet"},
			{name: "parallel", selector: "acp:claude-3-7-sonnet!openai:gpt-4o"},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				call := &aipapi.Call{
					Route:    aipapi.RouteIntent{Selector: tc.selector},
					Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
				}
				stream, err := ex.Execute(context.Background(), call)
				if stream != nil {
					_ = stream.Close()
				}
				if err == nil {
					t.Fatalf("expected error for selector %q, got nil", tc.selector)
				}
				if !errors.Is(err, routing.ErrUnsafeExecutionComposition) {
					t.Fatalf("expected ErrUnsafeExecutionComposition, got: %v", err)
				}
				if _, ok := errors.AsType[*routing.UnsafeExecutionCompositionError](err); !ok {
					t.Fatalf("expected UnsafeExecutionCompositionError, got: %T (%v)", err, err)
				}
			})
		}
	})

	t.Run("safe_composite_pure_inference_allowed", func(t *testing.T) {
		ex := newExec(config.ExecutionCompositionSafe)
		call := &aipapi.Call{
			Route:    aipapi.RouteIntent{Selector: "openai:gpt-4o|anthropic:claude-3-5-sonnet"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		}
		stream, err := ex.Execute(context.Background(), call)
		if err != nil {
			t.Fatalf("expected pure inference failover to succeed, got %v", err)
		}
		defer func() { _ = stream.Close() }()
	})

	t.Run("safe_composite_unconfigured_backend_defers_to_missing_backend", func(t *testing.T) {
		ex := newExec(config.ExecutionCompositionSafe)
		// "unconfigured" is not in backends map or resolver map
		call := &aipapi.Call{
			Route:    aipapi.RouteIntent{Selector: "openai:gpt-4o|unconfigured:model"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		}
		stream, err := ex.Execute(context.Background(), call)
		if stream != nil {
			_ = stream.Close()
		}
		// A5: Must NOT return ErrUnsafeExecutionComposition
		if errors.Is(err, routing.ErrUnsafeExecutionComposition) {
			t.Fatalf("did not expect ErrUnsafeExecutionComposition for unconfigured backend, got: %v", err)
		}
	})

	t.Run("unrestricted_composite_acp_allowed", func(t *testing.T) {
		ex := newExec(config.ExecutionCompositionUnrestricted)
		call := &aipapi.RouteIntent{Selector: "openai:gpt-4o|acp:claude-3-7-sonnet"}
		_ = call
		callMsg := &aipapi.Call{
			Route:    aipapi.RouteIntent{Selector: "openai:gpt-4o|acp:claude-3-7-sonnet"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		}
		stream, err := ex.Execute(context.Background(), callMsg)
		if err != nil {
			t.Fatalf("expected unrestricted composition to succeed, got %v", err)
		}
		defer func() { _ = stream.Close() }()
	})
}
