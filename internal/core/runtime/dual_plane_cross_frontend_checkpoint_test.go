package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// TestSharedCheckpointAcrossFrontendOperations proves that
// supported frontend call shapes share the same executor checkpoint contract
// (frontend_ingress boundary) rather than pairwise protocol translators
// (Phase 12.3 / requirements 17.1, 17.3). Anthropic/Gemini currently omit
// Invocation.Operation; empty operation covers that wire path.
func TestSharedCheckpointAcrossFrontendOperations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		op   aipapi.Operation
	}{
		{name: "openai_chat", op: aipapi.OperationOpenAIChatCompletions},
		{name: "openai_responses", op: aipapi.OperationOpenAIResponses},
		{name: "anthropic_or_gemini_empty_op", op: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			memSS := memory.New(memory.Options{SimulateDurable: true})
			mgr := testSecureManager(t, memSS, b2)
			snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
				Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
			})
			ex := setSecureSessionDenialMapper(TestExecutor())
			ex.Store = b2
			ex.Bus = hooks.New(hooks.Config{})
			ex.RuntimeSnapshot = snap
			ex.SecureSession = mgr
			ex.Now = func() time.Time { return time.Unix(1800, 0) }

			ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-cross-fe"})
			call := &aipapi.Call{
				ID: "req-" + tc.name,
				Invocation: aipapi.Invocation{
					Operation: tc.op,
				},
				Messages: []aipapi.Message{{
					Role:  aipapi.RoleUser,
					Parts: []aipapi.Part{aipapi.TextPart("cross-fe")},
				}},
			}
			_, _, _, _, outCtx, err := ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
			if err != nil {
				t.Fatal(err)
			}
			holder := meteringHolderFrom(outCtx)
			if holder == nil || holder.FrontendIngress == nil {
				t.Fatal("expected frontend ingress checkpoint on context")
			}
			if holder.FrontendIngress.Public.Boundary != metering.BoundaryFrontendIngress {
				t.Fatalf("boundary=%q want %q", holder.FrontendIngress.Public.Boundary, metering.BoundaryFrontendIngress)
			}
			if holder.FrontendIngress.Public.Lifecycle != metering.LifecycleLogicalRequest {
				t.Fatalf("lifecycle=%q", holder.FrontendIngress.Public.Lifecycle)
			}
		})
	}
}
