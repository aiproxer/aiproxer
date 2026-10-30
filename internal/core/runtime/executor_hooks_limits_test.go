package runtime_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// submitRouteRewrite implements sdkhooks.SubmitHook and replaces Route.Selector for routing tests.
type submitRouteRewrite struct {
	to string
}

func (submitRouteRewrite) ID() string                        { return "test-route-rewrite" }
func (submitRouteRewrite) Order() int                        { return 0 }
func (submitRouteRewrite) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (s submitRouteRewrite) Handle(ctx context.Context, call *aipapi.Call, meta *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	_ = ctx
	_ = meta
	call.Route.Selector = s.to
	return sdkhooks.SubmitDecision{}, nil
}

// submitInflateUserText grows the first text part beyond canonical limits (DoS probe).
type submitInflateUserText struct{}

func (submitInflateUserText) ID() string                        { return "test-inflate" }
func (submitInflateUserText) Order() int                        { return 0 }
func (submitInflateUserText) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (submitInflateUserText) Handle(ctx context.Context, call *aipapi.Call, meta *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	_ = ctx
	_ = meta
	if len(call.Messages) > 0 && len(call.Messages[0].Parts) > 0 {
		call.Messages[0].Parts[0].Text = strings.Repeat("z", aipapi.MaxPartTextBytes+1)
	}
	return sdkhooks.SubmitDecision{}, nil
}

// executorSubmitHooksBus returns a Bus with only submit hooks (no request-part hooks).
func executorSubmitHooksBus(submit ...sdkhooks.SubmitHook) *hooks.Bus {
	return hooks.New(hooks.Config{SubmitHooks: submit})
}

func TestExecutor_submitHook_routeSelector_usedForPlanning(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var backendOpened atomic.Value
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = executorSubmitHooksBus(submitRouteRewrite{
		to: "backendB:model-x",
	})
	ex.Backends = map[string]execbackend.Backend{
		"backendA": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				backendOpened.Store("A")
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
		"backendB": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				backendOpened.Store("B")
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "backendA:old"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, err = aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	v := backendOpened.Load()
	if v != "B" {
		t.Fatalf("routing must use post-submit Route.Selector (want backend B opened), got %v", v)
	}
}

func TestExecutor_selectorAlias_opensMappedBackend(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^incoming$`, Replacement: "backendB:model-x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var backendOpened atomic.Value
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.SelectorAliases = ar
	ex.Backends = map[string]execbackend.Backend{
		"backendA": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				backendOpened.Store("A")
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
		"backendB": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				backendOpened.Store("B")
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "incoming"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, err = aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if v := backendOpened.Load(); v != "B" {
		t.Fatalf("want backend B, got %v", v)
	}
}

func TestExecutor_submitHook_oversizedCall_rejectedBeforeBackendOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = executorSubmitHooksBus(submitInflateUserText{})
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
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
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected validation error after submit hook inflated message text")
	}
	if opens != 0 {
		t.Fatalf("backend must not open (opens=%d)", opens)
	}
}
