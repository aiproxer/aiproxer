package runtime_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestExecutor_AttemptIdentityIsUniquePerAttempt proves the executor hands each
// actual backend attempt a distinct host-owned identity. The request id is not
// that identity: a failover attempt repeats it, and the accounting fallback
// must not merge two provider charges.
func TestExecutor_AttemptIdentityIsUniquePerAttempt(t *testing.T) {
	t.Parallel()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var metas []execbackend.AttemptMeta
	capture := func(ctx context.Context) {
		meta, ok := execbackend.AttemptMetaFromContext(ctx)
		if !ok {
			return
		}
		mu.Lock()
		metas = append(metas, meta)
		mu.Unlock()
	}
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	first := execbackend.Backend{
		Caps: caps,
		Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			capture(ctx)
			return nil, aipapi.RecoverablePreOutputError(errors.New("temporary"))
		},
	}
	second := execbackend.Backend{
		Caps: caps,
		Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			capture(ctx)
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}

	ex := runtime.TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{"first": first, "second": second}

	call := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "first:m|second:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	mu.Lock()
	got := append([]execbackend.AttemptMeta(nil), metas...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("captured attempts=%d, want 2", len(got))
	}
	if got[0].ID == "" || got[1].ID == "" {
		t.Fatalf("attempt identities must be nonempty: %+v", got)
	}
	if got[0].ID == got[1].ID {
		t.Fatalf("two actual attempts share the host identity %q", got[0].ID)
	}
	if got[0].Seq >= got[1].Seq {
		t.Fatalf("attempt sequence must increase: %+v", got)
	}
}
