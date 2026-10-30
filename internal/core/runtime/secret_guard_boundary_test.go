package runtime_test

import (
	"context"
	"errors"
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
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestLegalPipeline_secretGuardAfterSessionOpenBeforeSubmit(t *testing.T) {
	t.Parallel()
	ids := feature.LegalPipelineStageIDs()
	openIdx := feature.LegalStageDescriptorIndex(feature.StageIDSessionOpen)
	guardIdx := feature.LegalStageDescriptorIndex(feature.StageIDSecretGuard)
	submitIdx := feature.LegalStageDescriptorIndex(feature.StageIDSubmit)
	if openIdx < 0 || guardIdx < 0 || submitIdx < 0 {
		t.Fatalf("missing stages open=%d guard=%d submit=%d in %#v", openIdx, guardIdx, submitIdx, ids)
	}
	if openIdx >= guardIdx || guardIdx >= submitIdx {
		t.Fatalf("want session_open < secret_guard < submit_request; got %d < %d < %d", openIdx, guardIdx, submitIdx)
	}
}

func TestExecutor_secretGuardDisabled_noninterference(t *testing.T) {
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

	var backendOpens atomic.Int32
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS2{}}),
	})
	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1801, 0) }
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				backendOpens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)

	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "user-sg2"})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("token=" + testkit.SyntheticOpenAIAPIKey)},
		}},
	}
	stream, execErr := ex.Execute(ctx, call)
	if execErr != nil {
		t.Fatal(execErr)
	}
	if _, err := aipapi.Collect(t.Context(), stream); err != nil && !errors.Is(err, context.Canceled) {
		_ = err
	}
	if backendOpens.Load() == 0 {
		t.Fatal("disabled secret-guard must not block backend dispatch")
	}
}

type redactingSecretGuard struct {
	evals *atomic.Int32
}

func (g *redactingSecretGuard) ID() string                           { return "redact-openai" }
func (g *redactingSecretGuard) Order() int                           { return 0 }
func (g *redactingSecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }

func (g *redactingSecretGuard) Evaluate(_ context.Context, call *aipapi.Call, _ secretguard.Meta, _ secretguard.Services) (secretguard.Decision, error) {
	if g.evals != nil {
		g.evals.Add(1)
	}
	needle := testkit.SyntheticOpenAIAPIKey
	mask := strings.Repeat("*", len(needle))
	mutated := 0
	for i := range call.Messages {
		for j := range call.Messages[i].Parts {
			if strings.Contains(call.Messages[i].Parts[j].Text, needle) {
				call.Messages[i].Parts[j].Text = strings.ReplaceAll(call.Messages[i].Parts[j].Text, needle, mask)
				mutated++
			}
		}
	}
	if mutated == 0 {
		return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
	}
	return secretguard.Decision{
		Outcome:       secretguard.OutcomeRedacted,
		MutationCount: mutated,
		Findings: []secretguard.Finding{{
			SecretRefName:   "OPENAI_API_KEY",
			SourceCategory:  secretguard.SourceCategoryProxyEnv,
			Location:        "messages[].parts[].text",
			OccurrenceCount: mutated,
		}},
	}, nil
}

type scanLimitLoggingSecretGuard struct {
	evals *atomic.Int32
}

func (g *scanLimitLoggingSecretGuard) ID() string { return "log-scan-limit" }
func (g *scanLimitLoggingSecretGuard) Order() int { return 0 }
func (g *scanLimitLoggingSecretGuard) FailureMode() secretguard.FailureMode {
	return secretguard.FailClosed
}

func (g *scanLimitLoggingSecretGuard) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	if g.evals != nil {
		g.evals.Add(1)
	}
	return secretguard.Decision{
		Outcome:       secretguard.OutcomeLog,
		ScanLimitHit:  true,
		FailureKind:   "scan_limit",
		FailureReason: "scan_max_bytes exceeded",
		Findings: []secretguard.Finding{{
			SecretRefName:   "OPENAI_API_KEY",
			SourceCategory:  secretguard.SourceCategoryProxyEnv,
			Location:        "messages[0].parts[0].text",
			OccurrenceCount: 1,
		}},
	}, nil
}

func TestExecutor_secretGuardRedact_checkpointAndBackendSanitized(t *testing.T) {
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
		sawSecret    atomic.Bool
	)
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS2{}}),
		SecretGuardPlane: extensions.SecretGuardPlane{
			AuditFailurePolicy: secretguard.AuditFailClosed,
		},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			SecretGuards: []secretguard.Guard{&redactingSecretGuard{evals: &guardEvals}},
		}),
	})
	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1802, 0) }
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				backendOpens.Add(1)
				for _, msg := range call.Messages {
					for _, p := range msg.Parts {
						if strings.Contains(p.Text, testkit.SyntheticOpenAIAPIKey) {
							sawSecret.Store(true)
						}
					}
				}
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)

	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "user-sg-redact"})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("token=" + testkit.SyntheticOpenAIAPIKey)},
		}},
	}
	stream, execErr := ex.Execute(ctx, call)
	if execErr != nil {
		t.Fatal(execErr)
	}
	if stream != nil {
		_, _ = aipapi.Collect(t.Context(), stream)
	}
	if guardEvals.Load() == 0 {
		t.Fatal("secret guard Evaluate was never invoked")
	}
	if backendOpens.Load() == 0 {
		t.Fatal("redact path must continue to backend")
	}
	if sawSecret.Load() {
		t.Fatal("backend/checkpoint path must not see pre-redaction secret")
	}
}

func TestExecutor_secretGuardLog_scanLimitContinuesToBackend(t *testing.T) {
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
			SecretGuards: []secretguard.Guard{&scanLimitLoggingSecretGuard{evals: &guardEvals}},
		}),
	})
	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1803, 0) }
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				backendOpens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)

	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "user-sg-log-limit"})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("token=" + testkit.SyntheticOpenAIAPIKey)},
		}},
	}
	stream, execErr := ex.Execute(ctx, call)
	if execErr != nil {
		t.Fatal(execErr)
	}
	if stream != nil {
		_, _ = aipapi.Collect(t.Context(), stream)
	}
	if guardEvals.Load() == 0 {
		t.Fatal("secret guard Evaluate was never invoked")
	}
	if backendOpens.Load() == 0 {
		t.Fatal("log scan-limit decision must continue to backend dispatch")
	}
}
