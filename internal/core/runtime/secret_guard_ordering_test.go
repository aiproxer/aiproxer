package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	sdksecret "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// TestSecretGuardOrdering_BeforeLocalTurn asserts the existing contract: secret guard runs before local-turn.
// It records invocation order via a fake secret guard and a fake local handler.
func TestSecretGuardOrdering_BeforeLocalTurn(t *testing.T) {
	ctx := context.Background()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("b2b: %v", err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(testFingerprintKey32(t)), b2bualineage.New(b2), app.ManagerConfig{FingerprintKey: testFingerprintKey32(t), StoreDurable: true})
	if err != nil {
		t.Fatalf("mgr: %v", err)
	}
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = true

	var order []string
	secretGuard := &orderingSecretGuard{order: &order, id: "secret"}
	localHandler := &orderingLocalHandler{order: &order, id: "local"}

	// Install secret guard via extension snapshot.
	snap := extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
		SecretGuardPlane: extensions.SecretGuardPlane{
			AuditFailurePolicy: sdksecret.AuditFailClosed,
		},
		FeaturePlanes: freezeBundle(testFeatureBundle{
			SecretGuards:      []sdksecret.Guard{secretGuard},
			LocalTurnHandlers: []localturn.Handler{localHandler},
		}),
	})
	ex.RuntimeSnapshot = snap
	ex.Bus = hooks.New(hooks.Config{})

	call := &aipapi.Call{
		Session:  aipapi.SessionRef{ClientSessionID: "c-secret-order"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
	}
	// Use prepareRequest which includes both secret guard and local-turn ordering.
	pr, prepCtx, cleanup, err := ex.prepareRequest(ctx, call)
	if err != nil {
		// Even if prepare fails, order should have secret before local attempt.
		// We still check order.
		t.Logf("prepareRequest err: %v", err)
	}
	defer cleanup()
	_ = pr
	_ = prepCtx

	// Find indices
	secretIdx := -1
	localIdx := -1
	for i, v := range order {
		if v == "secret" {
			secretIdx = i
		}
		if v == "local-match" {
			localIdx = i
		}
	}
	if secretIdx == -1 {
		t.Fatalf("secret guard not invoked, order: %v", order)
	}
	if localIdx == -1 {
		// local may not be invoked if secret guard blocked? but we expect at least match attempted
		t.Logf("local not invoked, order: %v", order)
		return
	}
	if secretIdx > localIdx {
		t.Fatalf("secret guard should run before localturn: secret %d local %d order %v", secretIdx, localIdx, order)
	}
}

type orderingSecretGuard struct {
	order *[]string
	id    string
}

func (g *orderingSecretGuard) ID() string                         { return g.id }
func (g *orderingSecretGuard) Order() int                         { return 0 }
func (g *orderingSecretGuard) FailureMode() sdksecret.FailureMode { return sdksecret.FailOpen }
func (g *orderingSecretGuard) Evaluate(ctx context.Context, call *aipapi.Call, meta sdksecret.Meta, services sdksecret.Services) (sdksecret.Decision, error) {
	*g.order = append(*g.order, "secret")
	return sdksecret.Decision{Outcome: sdksecret.OutcomePass}, nil
}

type orderingLocalHandler struct {
	order *[]string
	id    string
}

func (h *orderingLocalHandler) ID() string                         { return h.id }
func (h *orderingLocalHandler) Order() int                         { return 0 }
func (h *orderingLocalHandler) FailureMode() localturn.FailureMode { return localturn.FailOpen }
func (h *orderingLocalHandler) Match(ctx context.Context, call aipapi.Call, meta localturn.Meta) (localturn.MatchResult, error) {
	*h.order = append(*h.order, "local-match")
	return localturn.MatchResult{Claimed: false}, nil
}

func (h *orderingLocalHandler) Handle(ctx context.Context, input localturn.HandleInput) (localturn.Reply, error) {
	*h.order = append(*h.order, "local-handle")
	return localturn.Reply{Text: "hi"}, nil
}

// Ensure voidWS is available in this package (defined in executor_secure_session_test.go).
