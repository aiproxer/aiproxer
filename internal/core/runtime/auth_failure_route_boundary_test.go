package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// A recoverable authentication failure can reach only explicitly configured
// candidates. Provider classification belongs to connector tests (including
// TestClaudeSpec_AuthenticationRemediationAndOutputBoundary); this test proves
// the host half of that contract without importing an optional connector.
func TestPreOutputAuthFailure_OnlyExplicitRouteCanReachAlternate(t *testing.T) {
	for _, tc := range []struct {
		name, selector string
		wantAlternate  int64
	}{
		{"single_candidate", "primary:m", 0},
		{"explicit_alternate", "primary:m|alternate:m", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var primaryOpens, alternateOpens atomic.Int64
			executor := coreruntime.TestExecutor()
			executor.Store = store
			executor.Bus = hooks.New(hooks.Config{})
			executor.Rand = routing.NewSeededRng(1)
			executor.MaxAttempts = 2
			executor.Backends = map[string]execbackend.Backend{
				"primary": {
					Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
					Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						primaryOpens.Add(1)
						return nil, aipapi.RecoverablePreOutputError(aipapi.NewStreamError("authentication_required", "selected credential unavailable"))
					},
				},
				"alternate": {
					Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
					Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						alternateOpens.Add(1)
						return aipapi.NewFixedEventStream([]aipapi.Event{
							{Kind: aipapi.EventResponseStarted},
							{Kind: aipapi.EventMessageStarted},
							{Kind: aipapi.EventTextDelta, Delta: "explicit alternate"},
							{Kind: aipapi.EventResponseFinished},
						}), nil
					},
				},
			}
			stream, err := executor.Execute(t.Context(), &aipapi.Call{
				Route:    aipapi.RouteIntent{Selector: tc.selector},
				Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
			})
			var text string
			if err == nil {
				t.Cleanup(func() { _ = stream.Close() })
				collected, collectErr := aipapi.Collect(t.Context(), stream)
				err = collectErr
				text = collected.Text.String()
			}
			if tc.wantAlternate == 0 {
				if err == nil || text != "" {
					t.Fatalf("single-candidate failure must surface without content: err=%v text=%q", err, text)
				}
			} else if err != nil || text != "explicit alternate" {
				t.Fatalf("explicit alternate must serve the request: err=%v text=%q", err, text)
			}
			if primaryOpens.Load() != 1 || alternateOpens.Load() != tc.wantAlternate {
				t.Fatalf("opens primary=%d alternate=%d; want 1/%d", primaryOpens.Load(), alternateOpens.Load(), tc.wantAlternate)
			}
		})
	}
}
