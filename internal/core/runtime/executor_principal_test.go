package runtime_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

func TestExecutor_OpenContext_carriesTransportPrincipalInViews(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var openCtx context.Context
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				openCtx = ctx
				_ = call
				_ = cand
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(3)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "transport-user"})
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)

	v, ok := execctx.FromContext(openCtx)
	if !ok {
		t.Fatal("expected execctx views on backend open context")
	}
	if v.Principal.ID != "transport-user" {
		t.Fatalf("principal id: want transport-user got %q", v.Principal.ID)
	}
}
