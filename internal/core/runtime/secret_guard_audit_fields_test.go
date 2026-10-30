package runtime_test

import (
	"context"
	"strings"
	"sync"
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

func TestExecutor_secretGuardBlock_decisionEventRequiredFields(t *testing.T) {
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

	var mu sync.Mutex
	var got secretguard.DecisionEvent
	obs := secretguard.ObserverFunc(func(_ context.Context, ev secretguard.DecisionEvent) error {
		mu.Lock()
		got = ev
		mu.Unlock()
		return nil
	})

	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS2{}}),
		SecretGuardPlane: extensions.SecretGuardPlane{
			DecisionObserver:   obs,
			AccessMode:         "single_user",
			ConfigVersion:      "cfg-audit-fields-v1",
			AuditFailurePolicy: secretguard.AuditFailClosed,
		},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			SecretGuards: []secretguard.Guard{&blockingSecretGuard{}},
		}),
	})
	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1910, 0).UTC() }
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				t.Fatal("backend must not open on block")
				return nil, nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(7)

	ctx := secretguard.WithIngressAttribution(
		execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "user-audit-fields"}),
		secretguard.IngressAttribution{
			PeerIP:              "203.0.113.10",
			FrontendID:          "openai-responses",
			Operation:           "responses.create",
			AgentIdentityDigest: "agent-digest-abc",
		},
	)
	call := &aipapi.Call{
		ID:    "trace-audit-fields",
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("leak=" + testkit.SyntheticOpenAIAPIKey)},
		}},
	}
	stream, execErr := ex.Execute(ctx, call)
	if stream != nil {
		_, _ = aipapi.Collect(t.Context(), stream)
	}
	if !aipapi.IsPolicyDenied(execErr) {
		t.Fatalf("want policy denied, got %v", execErr)
	}

	mu.Lock()
	ev := got
	mu.Unlock()

	requireNonEmpty := func(name, v string) {
		t.Helper()
		if strings.TrimSpace(v) == "" {
			t.Fatalf("%s must be non-empty", name)
		}
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("Timestamp must be set")
	}
	requireNonEmpty("EventID", ev.EventID)
	requireNonEmpty("TraceID", ev.TraceID)
	requireNonEmpty("SessionID", ev.SessionID)
	requireNonEmpty("ALegID", ev.ALegID)
	requireNonEmpty("TurnID", ev.TurnID)
	requireNonEmpty("PrincipalID", ev.PrincipalID)
	requireNonEmpty("PeerIP", ev.PeerIP)
	requireNonEmpty("Source", ev.Source)
	requireNonEmpty("FrontendID", ev.FrontendID)
	requireNonEmpty("Operation", ev.Operation)
	requireNonEmpty("AgentIdentityDigest", ev.AgentIdentityDigest)
	requireNonEmpty("RequestedRoute", ev.RequestedRoute)
	requireNonEmpty("RequestedModel", ev.RequestedModel)
	requireNonEmpty("Action", ev.Action)
	requireNonEmpty("AccessMode", ev.AccessMode)
	requireNonEmpty("ConfigVersion", ev.ConfigVersion)
	requireNonEmpty("QuarantineResult", ev.QuarantineResult)
	requireNonEmpty("GuardID", ev.GuardID)
	if ev.Outcome != secretguard.OutcomeBlock {
		t.Fatalf("Outcome=%q want block", ev.Outcome)
	}
	if ev.QuarantineResult != secretguard.QuarantineResultCommitted {
		t.Fatalf("QuarantineResult=%q want committed", ev.QuarantineResult)
	}
	if ev.BackendDispatched {
		t.Fatal("BackendDispatched must be false for block")
	}
	if len(ev.Findings) == 0 {
		t.Fatal("Findings must be present")
	}
	requireNonEmpty("Findings[0].SecretRefName", ev.Findings[0].SecretRefName)
	requireNonEmpty("Findings[0].Location", ev.Findings[0].Location)
	if ev.Findings[0].OccurrenceCount < 1 {
		t.Fatal("OccurrenceCount must be >= 1")
	}
	for _, needle := range testkit.AllSyntheticSecretGuardNeedles() {
		dump := ev.EventID + ev.TraceID + ev.RequestedRoute + ev.Findings[0].SecretRefName + ev.Findings[0].Location
		if strings.Contains(dump, needle) {
			t.Fatalf("audit fields must not contain secret needle %q", needle)
		}
	}
}
