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
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type mutateSubmitHook struct{}

func (mutateSubmitHook) ID() string                        { return "mutate_submit" }
func (mutateSubmitHook) Order() int                        { return 0 }
func (mutateSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (mutateSubmitHook) Handle(_ context.Context, call *aipapi.Call, _ *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	if call != nil && len(call.Messages) > 0 && len(call.Messages[0].Parts) > 0 {
		call.Messages[0].Parts[0].Text = "after-submit"
	}
	return sdkhooks.SubmitDecision{}, nil
}

func TestPrepareSubmitAndALeg_capturesFrontendIngressBeforeSubmit(t *testing.T) {
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
	ex.Bus = hooks.New(hooks.Config{SubmitHooks: []sdkhooks.SubmitHook{mutateSubmitHook{}}})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1700, 0) }

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-z"})
	call := &aipapi.Call{
		ID: "req-fe-ingress",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("before-submit")},
		}},
	}
	_, baseline, _, _, outCtx, err := ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
	if err != nil {
		t.Fatal(err)
	}
	holder := meteringHolderFrom(outCtx)
	if holder == nil || holder.FrontendIngress == nil {
		t.Fatal("expected frontend ingress checkpoint on context")
	}
	fe := holder.FrontendIngress
	if fe.Public.Boundary != metering.BoundaryFrontendIngress {
		t.Fatalf("boundary=%q", fe.Public.Boundary)
	}
	if fe.Call.Messages[0].Parts[0].Text != "before-submit" {
		t.Fatalf("FE ingress call=%q want before-submit", fe.Call.Messages[0].Parts[0].Text)
	}
	if baseline.Messages[0].Parts[0].Text != "after-submit" {
		t.Fatalf("post-submit baseline=%q want after-submit", baseline.Messages[0].Parts[0].Text)
	}
}
