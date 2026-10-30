package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestExecutor_execute_routePreferenceDrivesPlanner locks the planning-side effect of
// execctx.RouteCandidatePreferences at the Execute boundary (Phase 4 Task 4.1 gap).
// When the caller injects a preferred candidate key via context, the executor's planner
// must prefer that backend when expanding failover groups.
func TestExecutor_execute_routePreferenceDrivesPlanner(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var preferredOpens atomic.Int32
	var otherOpens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"preferred": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				preferredOpens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
		"other": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				otherOpens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "preferred:m|other:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	// Inject route preference for "preferred" backend.
	ctx := execctx.WithRouteCandidatePreferences(context.Background(), []string{"preferred"})
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if preferredOpens.Load() == 0 {
		t.Fatal("expected preferred backend to be opened; route preference was not honored by the planner")
	}
	if otherOpens.Load() > 0 {
		t.Fatal("expected other backend to NOT be opened when preference is honored on first attempt")
	}
}
