package runtime_test

import (
	"context"
	randv2 "math/rand/v2"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestExecutor_concurrentExecute_sharedEmptyHooksBus stresses a shared Executor whose hook bus is
// the explicit empty-bus value from hooks.New — there must be no data race on Executor.Bus.
func TestExecutor_concurrentExecute_sharedEmptyHooksBus(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				_ = ctx
				_ = call
				_ = cand
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.WrapRandV2(randv2.New(randv2.NewPCG(99, 0)))

	const n = 64
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			call := &aipapi.Call{
				Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages: []aipapi.Message{{
					Role:  aipapi.RoleUser,
					Parts: []aipapi.Part{aipapi.TextPart("hi")},
				}},
			}
			_, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Errorf("Execute: %v", err)
				return
			}
		})
	}
	wg.Wait()
}

// TestExecutor_concurrentExecute_sharedRand_weighted exercises a single *rand/v2.Rand
// across concurrent Execute calls while weighted routing calls Intn. Without
// serialization this is a data race (see math/rand/v2.Rand docs). Verified under
// go test -race on platforms where the race runtime can be initialized.
func TestExecutor_concurrentExecute_sharedRand_weighted(t *testing.T) {
	t.Parallel()
	r := randv2.New(randv2.NewPCG(17, 0))
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	open := func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		_ = ctx
		_ = call
		_ = cand
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.WrapRandV2(r)
	ex.Backends = map[string]execbackend.Backend{
		"a": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: open},
		"b": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: open},
	}
	const n = 64
	var wg sync.WaitGroup
	sel := "[weight=1]a:m1^[weight=1]b:m2"
	for range n {
		wg.Go(func() {
			call := &aipapi.Call{
				Route: aipapi.RouteIntent{Selector: sel},
				Messages: []aipapi.Message{{
					Role:  aipapi.RoleUser,
					Parts: []aipapi.Part{aipapi.TextPart("hi")},
				}},
			}
			_, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Errorf("Execute: %v", err)
			}
		})
	}
	wg.Wait()
}
