package runtime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type countingPassGuard struct {
	evals *atomic.Int32
}

func (g *countingPassGuard) ID() string                           { return "count-pass" }
func (g *countingPassGuard) Order() int                           { return 0 }
func (g *countingPassGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (g *countingPassGuard) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	if g.evals != nil {
		g.evals.Add(1)
	}
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

func TestExecutor_secretGuardEvaluateOnce_underFailover(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	key := secretGuardFingerprintKey(t)
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(key), b2bualineage.New(b2), app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	var (
		guardEvals   atomic.Int32
		backendOpens atomic.Int32
	)
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS2{}}),
		SecretGuardPlane: extensions.SecretGuardPlane{
			AuditFailurePolicy: secretguard.AuditFailClosed,
		},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			SecretGuards: []secretguard.Guard{&countingPassGuard{evals: &guardEvals}},
		}),
	})
	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.MaxAttempts = 3
	ex.Now = func() time.Time { return time.Unix(1911, 0) }
	ex.Rand = routing.NewSeededRng(11)
	ex.Backends = map[string]execbackend.Backend{
		"a": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				backendOpens.Add(1)
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp-a"))
			},
		},
		"b": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				backendOpens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}

	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "user-eval-once"})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "a:m|b:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
	stream, execErr := ex.Execute(ctx, call)
	if execErr != nil {
		t.Fatal(execErr)
	}
	if stream != nil {
		_, _ = aipapi.Collect(t.Context(), stream)
	}
	if backendOpens.Load() < 2 {
		t.Fatalf("backend opens=%d want >= 2 (failover)", backendOpens.Load())
	}
	if got := guardEvals.Load(); got != 1 {
		t.Fatalf("secret guard Evaluate count=%d want 1 (once per ingress turn, not per backend attempt)", got)
	}
}
