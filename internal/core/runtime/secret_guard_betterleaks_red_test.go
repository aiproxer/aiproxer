package runtime_test

import (
	"context"
	"reflect"
	"strings"
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
	featuresecretguard "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard"
	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

const syntheticBetterLeaksRuntimeGitHubToken = "ghp_Z9yX7wV5uT3sR1qP8nM6kJ4hG2fD0cB8aF6Q" // #nosec G101 -- synthetic fixture only.

type betterLeaksRuntimeHarness struct {
	exec         *runtime.Executor
	store        *memory.Store
	mgr          *app.Manager
	call         *aipapi.Call
	ownerID      string
	backendOpens atomic.Int32
	sawSecret    atomic.Bool
}

func newBetterLeaksRuntimeHarness(t *testing.T, action string) *betterLeaksRuntimeHarness {
	t.Helper()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal("create B2BUA store")
	}
	store := memory.New(memory.Options{SimulateDurable: true})
	key := secretGuardFingerprintKey(t)
	mgr, err := app.NewManager(store, app.NewRandGenerator(key), b2bualineage.New(b2), app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal("create secure-session manager")
	}

	services, err := featuresecretguard.BuildGenerationServices(featuresecretguard.DetectorPolicy{
		BetterLeaks: featuresecretguard.BetterLeaksPolicy{
			Enabled:           true,
			MinimumConfidence: featuresecretguard.DefaultBetterLeaksConfidence,
			MaxDecodeDepth:    featuresecretguard.DefaultBetterLeaksDecodeDepth,
			Workers:           1,
			MaxFindings:       featuresecretguard.DefaultBetterLeaksMaxFindings,
		},
	}, engine.NewDisabledSource())
	if err != nil {
		t.Fatal("compose BetterLeaks generation services")
	}

	bus := hooks.New(hooks.Config{})
	guard := featuresecretguard.NewGuard(featuresecretguard.Config{
		Action: action,
	})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspaceResolverForBetterLeaksRuntime{},
		SecretGuardPlane: extensions.SecretGuardPlane{
			MatcherResolver:    engine.NewStaticMatcherResolver(nil, engine.MatcherOptions{}),
			Capability:         services,
			AuditFailurePolicy: sdk.AuditFailClosed,
		},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			SecretGuards: []sdk.Guard{guard},
		}),
	})

	h := &betterLeaksRuntimeHarness{store: store, mgr: mgr, ownerID: "betterleaks-runtime-user"}
	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(2600, 0).UTC() }
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				h.backendOpens.Add(1)
				for _, message := range call.Messages {
					for _, part := range message.Parts {
						if strings.Contains(part.Text, syntheticBetterLeaksRuntimeGitHubToken) {
							h.sawSecret.Store(true)
						}
					}
				}
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	h.exec = ex
	h.call = &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("GITHUB_TOKEN=" + syntheticBetterLeaksRuntimeGitHubToken)},
		}},
	}
	return h
}

type workspaceResolverForBetterLeaksRuntime struct{}

func (workspaceResolverForBetterLeaksRuntime) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{}, nil
}

func TestExecutor_BetterLeaksBlockPreDispatchQuarantinesComposedGeneration(t *testing.T) {
	h := newBetterLeaksRuntimeHarness(t, featuresecretguard.ActionBlock)
	h.call.ID = "betterleaks-synthetic-call-id"
	before := aipapi.CloneCall(*h.call)
	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: h.ownerID})

	stream, execErr := h.exec.Execute(ctx, h.call)
	if stream != nil {
		_, _ = aipapi.Collect(t.Context(), stream)
	}
	if execErr == nil || !aipapi.IsPolicyDenied(execErr) {
		t.Fatal("BetterLeaks block did not produce a policy denial")
	}
	if h.backendOpens.Load() != 0 {
		t.Fatal("BetterLeaks block dispatched to the backend")
	}
	if !reflect.DeepEqual(*h.call, before) {
		t.Fatal("BetterLeaks block mutated the canonical call")
	}
	sessionID := latestStoredSessionID(ctx, t, h.store, h.ownerID)
	assertStoredSessionQuarantined(ctx, t, h.store, h.mgr, sessionID)
}

func TestExecutor_BetterLeaksRedactReachesBackendWithoutSecret(t *testing.T) {
	h := newBetterLeaksRuntimeHarness(t, featuresecretguard.ActionRedact)
	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: h.ownerID})

	stream, execErr := h.exec.Execute(ctx, h.call)
	if execErr != nil {
		t.Fatal("BetterLeaks literal redaction denied the rewritable request")
	}
	if stream == nil {
		t.Fatal("redacted request did not reach the backend")
	}
	_, _ = aipapi.Collect(t.Context(), stream)
	if h.backendOpens.Load() != 1 {
		t.Fatalf("backend opens = %d", h.backendOpens.Load())
	}
	if h.sawSecret.Load() {
		t.Fatal("backend observed the BetterLeaks token")
	}
}
