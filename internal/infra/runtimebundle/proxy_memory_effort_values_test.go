package runtimebundle

import (
	"context"
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

type effortValuesFakeService struct{}

func (effortValuesFakeService) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorReasoningEffortValues,
		PluginID: "effort-values-fake", Version: "v1",
		Features:  []backendplugin.Feature{{Name: backendplugin.FeatureReasoningEffortValues}},
		Factories: []backendplugin.FactoryDescriptor{{Kind: "fake", StaticCapabilities: backendplugin.CapabilitySummary{Streaming: true, Reasoning: true}}},
	}, nil
}

func (effortValuesFakeService) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	return effortValuesFakeInstance{}, nil
}

type effortValuesFakeInstance struct{}

func (effortValuesFakeInstance) Resolve(_ context.Context, modelID *string) (backendplugin.ResolvedProfile, error) {
	base := backendplugin.ResolvedProfile{Capabilities: backendplugin.CapabilitySummary{Streaming: true, Reasoning: true}}
	if modelID == nil {
		return base, nil
	}
	if *modelID != "gpt-6-astra" {
		return base, nil
	}
	base.ReasoningEffortValues = &backendplugin.ReasoningEffortValuesV1{
		ModelID: "gpt-6-astra", Supported: []string{"high"}, Exhaustive: true,
	}
	return base, nil
}

func (effortValuesFakeInstance) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}
func (effortValuesFakeInstance) Execute(backendplugin.ExecuteStream) error { return nil }
func (effortValuesFakeInstance) Close(context.Context) error               { return nil }

func effortGuardConfig() proxymemory.Config {
	cfg := proxymemory.DefaultConfig()
	cfg.ModelsDeclared = true
	policy := proxymemory.PhasePolicy{
		Defaults: proxymemory.PhaseTarget{Backend: "fake", Model: "gpt-6-astra", ReasoningEffort: "high"},
		Inventory: proxymemory.PhaseInventory{
			Backends: []string{"fake"}, Models: []string{"gpt-6-astra"},
			Targets: []proxymemory.TargetPolicy{{Backend: "fake", Model: "gpt-6-astra", ReasoningEfforts: []string{"high", "none"}}},
		},
	}
	cfg.Models.Extraction = policy
	cfg.Models.Consolidation = policy
	return cfg
}

func effortGuardInput(support proxymemory.EffortSupportState, effort string) proxymemory.AttemptGuardInput {
	return proxymemory.AttemptGuardInput{
		Role: "proxy_memory_phase1", CandidateKey: "fake:gpt-6-astra",
		Backend: "fake", Model: "gpt-6-astra",
		BaselineRoute:   "fake:gpt-6-astra",
		BaselineOptions: aipapi.GenerationOptions{ReasoningEffort: effort},
		ExpectedOptions: aipapi.GenerationOptions{ReasoningEffort: effort},
		AttemptOptions:  aipapi.GenerationOptions{ReasoningEffort: effort},
		Installation:    proxymemory.InstallationPresent,
		EffortSupport:   support,
	}
}

func toProxymemorySupport(s execbackend.EffortSupportState) proxymemory.EffortSupportState {
	switch s {
	case execbackend.EffortSupportSupported:
		return proxymemory.EffortSupportSupported
	case execbackend.EffortSupportUnsupported:
		return proxymemory.EffortSupportUnsupported
	default:
		return proxymemory.EffortSupportUnknown
	}
}

// TestProxyMemoryEffortValuesComposedPath proves the full public path: a real
// bufconn ResolveProfile through the host session and executable adapter
// reaches EffectiveEffortSupport and the production pre-open guard.
func TestProxyMemoryEffortValuesComposedPath(t *testing.T) {
	t.Parallel()
	lis := bufconn.Listen(1 << 20)
	svc := effortValuesFakeService{}
	desc, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(backendplugin.ProtocolOffer{
		Major: desc.ProtocolMajor, Minor: desc.ProtocolMinor, Features: desc.Features, DisableTransportRetries: true,
	}, svc))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() { gs.Stop(); _ = lis.Close() })

	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	sess, _, err := host.DialConfiguredSession(context.Background(), conn, "effort", "fake", nil,
		backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	if !backendplugin.ReasoningEffortValuesNegotiated(sess.Negotiation()) {
		t.Fatalf("effort values not negotiated: %+v", sess.Negotiation())
	}

	br := adapter.Build(sess, backendplugin.ResolvedProfile{}, adapter.Options{InstanceID: "effort", Negotiation: sess.Negotiation()})
	be := br.Backend
	if be.ResolveEffortSupport == nil {
		t.Fatalf("adapter must bind ResolveEffortSupport")
	}
	opens := 0
	be.Open = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		opens++
		return nil, nil
	}

	call := aipapi.Call{}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "fake", Model: "gpt-6-astra"}, Key: "fake:gpt-6-astra"}

	// Case A: supported value passes the guard and would open upstream.
	if got := execbackend.EffectiveEffortSupport(context.Background(), be, call, cand, "high"); got != execbackend.EffortSupportSupported {
		t.Fatalf("high must be supported, got %d", got)
	}
	guard := proxymemory.NewAttemptGuard(effortGuardConfig(), proxymemory.NewPrometheusCollector(), nil)
	if err := guard.CheckAttempt(context.Background(), effortGuardInput(toProxymemorySupport(execbackend.EffortSupportSupported), "high")); err != nil {
		t.Fatalf("supported effort must pass the guard: %v", err)
	}
	if _, err := be.Open(context.Background(), call, cand); err != nil {
		t.Fatalf("open: %v", err)
	}
	if opens != 1 {
		t.Fatalf("supported effort must invoke provider open once, got %d", opens)
	}

	// Case B: exhaustively excluded value is refused before open.
	if got := execbackend.EffectiveEffortSupport(context.Background(), be, call, cand, "none"); got != execbackend.EffortSupportUnsupported {
		t.Fatalf("none must be unsupported, got %d", got)
	}
	if err := guard.CheckAttempt(context.Background(), effortGuardInput(toProxymemorySupport(execbackend.EffortSupportUnsupported), "none")); err == nil {
		t.Fatalf("unsupported effort must be refused before open")
	}
	if opens != 1 {
		t.Fatalf("refused effort must not invoke open, got %d", opens)
	}

	// Case C: no declaration for this model stays unknown (served, counted).
	other := routing.AttemptCandidate{Primary: routing.Primary{Backend: "fake", Model: "other"}, Key: "fake:other"}
	if got := execbackend.EffectiveEffortSupport(context.Background(), be, call, other, "high"); got != execbackend.EffortSupportUnknown {
		t.Fatalf("undeclared model must be unknown, got %d", got)
	}

	// Case D: never borrow another model's assertion.
	mismatchModel := "gpt-6-astra"
	_ = mismatchModel
	profile, err := sess.Resolve(context.Background(), &cand.Primary.Model)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ReasoningEffortValues == nil || profile.ReasoningEffortValues.ModelID != "gpt-6-astra" {
		t.Fatalf("declaration must name the exact requested model, got %+v", profile.ReasoningEffortValues)
	}
	if _, decided := backendplugin.ClassifyEffortSupport(profile.ReasoningEffortValues, "other", "high"); decided {
		t.Fatalf("mismatched model must not decide")
	}
}
