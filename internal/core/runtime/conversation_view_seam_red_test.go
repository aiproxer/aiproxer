package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// TestPrepareRequest_SeamOrdering_CharacterizesCurrentFlow is RED for 3.1.
// It proves the seam exists after authoritative A-leg/secret/submit/CTP
// and before inference-specific PreRequest/billing/route work, and that
// ingress view is deep-cloned isolated from backend working call.
// Before the fix, ingress and backend are same pointer or share underlying
// slices, and PreRequest mutation leaks into the preserved ingress view.
func TestPrepareRequest_SeamOrdering_CharacterizesCurrentFlow(t *testing.T) {
	ctx := context.Background()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("b2b store: %v", err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(testFingerprintKey32(t)), b2bualineage.New(b2), app.ManagerConfig{
		FingerprintKey: testFingerprintKey32(t),
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatalf("secure manager: %v", err)
	}
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = true

	// PreRequest handler that mutates backend call only (inference-specific).
	// If seam is correctly before this transform, ingress must NOT contain the injection.
	type preReqInjector struct{}
	// implement prerequest.Handler interface minimally via test helper? Use a wrapper.
	// We use a simple hook via extensions snapshot: register a handler that appends a message.
	injector := &preRequestInjectingHandler{id: "test_injector"}

	snap := extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{
			PreRequestHandlers: []prerequest.Handler{injector},
		}),
	})
	ex.RuntimeSnapshot = snap
	ex.Bus = hooks.New(hooks.Config{
		SubmitHooks: []sdkhooks.SubmitHook{spySubmitHookNoop{}},
	})

	call := &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: "c-seam-order"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}

	pr, prepCtx, cleanup, err := ex.prepareRequest(ctx, call)
	if err != nil {
		t.Fatalf("prepareRequest: %v", err)
	}
	defer cleanup()
	_ = prepCtx

	// 1. Seam must have preserved a separate ingress view (via identity.ingressCall).
	if pr == nil {
		t.Fatal("nil preparedRequest")
	}
	if pr.identity == nil || pr.identity.ingressCall == nil {
		t.Fatalf("RED: ingressCall not preserved (seam missing)")
	}
	ingress := pr.identity.ingressCall
	if pr.call == nil {
		t.Fatal("backend call nil")
	}
	if ingress == pr.call {
		t.Fatalf("ingress and backend share same pointer (no clone isolation)")
	}
	// Deep clone isolation: mutating backend must not affect ingress.
	origIngressLen := len(ingress.Messages)
	pr.call.Messages = append(pr.call.Messages, aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("mutated")}})
	if len(ingress.Messages) != origIngressLen {
		t.Fatalf("ingress mutated when backend mutated: ingress len %d want %d", len(ingress.Messages), origIngressLen)
	}
	// Also mutate nested part text
	if len(pr.call.Messages) > 0 && len(pr.call.Messages[0].Parts) > 0 {
		pr.call.Messages[0].Parts[0].Text = "backend-mutated"
		if ingress.Messages[0].Parts[0].Text == "backend-mutated" {
			t.Fatalf("ingress shares underlying Part storage with backend (shallow clone)")
		}
	}

	// 2. Ordering: ingress must NOT contain PreRequest injection, backend MUST.
	hasInjected := func(c *aipapi.Call) bool {
		for _, m := range c.Messages {
			for _, p := range m.Parts {
				if p.Text == "injected-by-prerequest" {
					return true
				}
			}
		}
		return false
	}
	if hasInjected(ingress) {
		t.Fatalf("ingress contains PreRequest injection (seam not before transforms)")
	}
	if !hasInjected(pr.call) {
		t.Fatalf("backend missing PreRequest injection (transform not applied to backend)")
	}

	// 3. No-op preservation: with no tags/steering, backend after transforms is still valid and
	// ingress remains valid canonical call (Validate passes).
	if err := ingress.Validate(); err != nil {
		t.Fatalf("ingress Validate: %v", err)
	}
	if err := pr.call.Validate(); err != nil {
		t.Fatalf("backend Validate: %v", err)
	}

	// 4. Seam must occur after secure A-leg authority: ingressCall ALegID must match identity.
	if ingress.Session.ALegID == "" {
		t.Fatalf("ingress ALegID empty (seam before A-leg)")
	}
	if ingress.Session.ALegID != pr.identity.aLeg.ALegID {
		t.Fatalf("ingress ALegID mismatch: %q vs %q", ingress.Session.ALegID, pr.identity.aLeg.ALegID)
	}
	// Secure turn and A-leg must be populated before seam – verified via identity already set.
	if pr.identity == nil || pr.identity.aLeg.ALegID == "" {
		t.Fatal("identity not set before seam")
	}
}

type spySubmitHookNoop struct{}

func (spySubmitHookNoop) ID() string                        { return "spy_submit_noop" }
func (spySubmitHookNoop) Order() int                        { return 0 }
func (spySubmitHookNoop) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (spySubmitHookNoop) Handle(_ context.Context, _ *aipapi.Call, _ *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type preRequestInjectingHandler struct {
	id string
}

func (h *preRequestInjectingHandler) ID() string                        { return h.id }
func (h *preRequestInjectingHandler) Order() int                        { return 0 }
func (h *preRequestInjectingHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h *preRequestInjectingHandler) Handle(_ context.Context, call *aipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	call.Messages = append(call.Messages, aipapi.Message{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart("injected-by-prerequest")},
	})
	return prerequest.Decision{}, nil
}

// Ensure the test compiles against the expected seam surface: identity must expose
// ingressCall and preparedRequest must expose call/identity. This will fail RED until seam added.
var _ = func(pr *preparedRequest) {
	_ = pr.identity.ingressCall
	_ = pr.call
	_ = pr.identity
	_ = pr.aScope
	_ = execctx.SessionModeFromContext // ensure imports used
}
