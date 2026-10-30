package runtime_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	coreworkspace "github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type labelOpener struct{}

func (labelOpener) ID() string { return "label-opener" }
func (labelOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{SessionLabelUpserts: map[string]string{"boot": "1"}}, nil
}

type memWS struct{}

func (memWS) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{ID: "ws-1", ProjectRoot: "/proj"}, nil
}

func TestExecutor_backendOpenContext_hasSessionLabelsAndWorkspace(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: coreworkspace.NewResolverChain([]aipworkspace.Resolver{memWS{}}),
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			SessionOpeners: []session.Opener{labelOpener{}},
		}),
	})
	var openCtx context.Context
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				openCtx = ctx
				_ = call
				_ = cand
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
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
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
	v, ok := execctx.FromContext(openCtx)
	if !ok {
		t.Fatal("expected views")
	}
	if v.Session.Labels["boot"] != "1" {
		t.Fatalf("session labels %+v", v.Session.Labels)
	}
	if v.Workspace.ProjectRoot != "/proj" {
		t.Fatalf("workspace root %q", v.Workspace.ProjectRoot)
	}
	if v.Session.WorkspaceID != "ws-1" {
		t.Fatalf("session workspace id: got %q", v.Session.WorkspaceID)
	}
}
