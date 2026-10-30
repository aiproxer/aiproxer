package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	corecp "github.com/aiproxer/aiproxer/internal/core/controlplane"
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
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestReadinessReport_secretGuardQuarantineFaultWithoutSecretMaterial(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	fake := &testkit.FakeSecureSessionStore{
		Delegate:      memSS,
		QuarantineErr: errors.New("disk full"),
	}
	key := secretGuardFingerprintKey(t)
	mgr, err := app.NewManager(fake, app.NewRandGenerator(key), b2bualineage.New(b2), app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS2{}}),
		SecretGuardPlane: extensions.SecretGuardPlane{
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
	ex.Now = func() time.Time { return time.Unix(1902, 0) }
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("unreachable")
			},
		},
	}
	ex.Rand = routing.NewSeededRng(3)

	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "user-rd"})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(testkit.SyntheticOpenAIAPIKey)},
		}},
	}
	_, _ = ex.Execute(ctx, call)

	svc := corecp.NewReadinessReportService(corecp.ReadinessReportSources{
		SecretGuardQuarantine: func(context.Context) (controlplane.ReadinessComponentStatus, error) {
			state := controlplane.CapabilityReady
			reason := controlplane.ReasonNone
			if ex.QuarantinePersistenceFaulted() {
				state = controlplane.CapabilityUnavailable
				reason = controlplane.ReasonBackingUnavailable
			}
			return controlplane.ReadinessComponentStatus{
				Component:        controlplane.ReadinessComponentSecretGuardQuarantine,
				State:            state,
				Reason:           reason,
				EnforcementScope: controlplane.EnforcementScopeAdvisorySingleProcess,
			}, nil
		},
	})
	report, err := svc.Report(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var row *controlplane.ReadinessComponentStatus
	for i := range report.Components {
		if report.Components[i].Component == controlplane.ReadinessComponentSecretGuardQuarantine {
			row = &report.Components[i]
			break
		}
	}
	if row == nil {
		t.Fatal("missing secret_guard_quarantine component")
	}
	if row.State != controlplane.CapabilityUnavailable {
		t.Fatalf("state=%q want unavailable", row.State)
	}
	if row.Reason != controlplane.ReasonBackingUnavailable {
		t.Fatalf("reason=%q", row.Reason)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, secret := range testkit.AllSyntheticSecretGuardValues() {
		if secret != "" && strings.Contains(body, secret) {
			t.Fatal("readiness report must not contain synthetic secret material")
		}
	}
	for _, needle := range testkit.AllSyntheticSecretGuardNeedles() {
		if needle != "" && strings.Contains(body, needle) {
			t.Fatal("readiness report must not contain synthetic secret substrings")
		}
	}
	if strings.Contains(body, "disk full") {
		t.Fatal("readiness report must not expose internal quarantine error detail")
	}
}
