package featurehost

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

// effortValuesConnectorService is the executable peer of the composed proof.
// It publishes the same graded-effort facts a real connector would, so the
// production host session, executable adapter, core executor, and feature
// guard all run as they do in a live generation.
type effortValuesConnectorService struct {
	instance *effortValuesConnectorInstance
}

func (s effortValuesConnectorService) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		ProtocolMinor: backendplugin.ProtocolMinorReasoningEffortValues,
		PluginID:      "effort-executor-proof", Version: "v1",
		Features:  []backendplugin.Feature{{Name: backendplugin.FeatureReasoningEffortValues}},
		Factories: []backendplugin.FactoryDescriptor{{Kind: "fake", StaticCapabilities: backendplugin.CapabilitySummary{Streaming: true, Reasoning: true}}},
	}, nil
}

func (s effortValuesConnectorService) Configure(context.Context, backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	return s.instance, nil
}

type effortValuesConnectorInstance struct {
	executes    atomic.Int64
	factQueries atomic.Int64
	mu          sync.Mutex
	resolvedFor []string
	openModel   string
	openEffort  string
}

func (i *effortValuesConnectorInstance) Resolve(_ context.Context, modelID *string) (backendplugin.ResolvedProfile, error) {
	profile := backendplugin.ResolvedProfile{Capabilities: backendplugin.CapabilitySummary{Streaming: true, Reasoning: true}}
	if modelID == nil {
		return profile, nil
	}
	requested := *modelID
	i.mu.Lock()
	i.resolvedFor = append(i.resolvedFor, requested)
	i.mu.Unlock()
	// Only the exact native model has provider-proven graded values. The
	// upstream list is deliberately non-exhaustive: an omission is Unknown.
	if requested == "gpt-6-native-xyz" {
		i.factQueries.Add(1)
		profile.ReasoningEffortValues = &backendplugin.ReasoningEffortValuesV1{
			ModelID: requested, Supported: []string{"high"}, Exhaustive: true,
		}
	}
	return profile, nil
}

func (i *effortValuesConnectorInstance) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}

func (i *effortValuesConnectorInstance) Execute(stream backendplugin.ExecuteStream) error {
	i.executes.Add(1)
	start, err := stream.Recv()
	if err != nil {
		return err
	}
	i.mu.Lock()
	if start.Invocation != nil {
		i.openModel = start.Invocation.NativeModelID
		if start.Invocation.Options.ReasoningEffort != nil {
			i.openEffort = *start.Invocation.Options.ReasoningEffort
		}
	}
	i.mu.Unlock()
	if err := stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		return err
	}
	for sequence, kind := range []backendplugin.EventKind{backendplugin.EventResponseStarted, backendplugin.EventResponseFinished} {
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: uint64(sequence + 1),
			Event: &backendplugin.CanonicalEvent{Kind: kind},
		}); err != nil {
			return err
		}
	}
	return stream.Send(backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameTerminal, Sequence: 3,
		Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
	})
}

func (i *effortValuesConnectorInstance) Close(context.Context) error { return nil }

func (i *effortValuesConnectorInstance) resolvedModels() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.resolvedFor...)
}

func (i *effortValuesConnectorInstance) opened() (model, effort string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.openModel, i.openEffort
}

// effortValuesStaticInventory proves the model is installed for the backend.
type effortValuesStaticInventory struct{ models []modelinventory.Model }

func (p effortValuesStaticInventory) LoadModels(context.Context) (modelinventory.Snapshot, error) {
	return modelinventory.Snapshot{
		Source: modelinventory.SourceRemote, LoadedAt: time.Unix(1_700_000_000, 0).UTC(),
		Models: append([]modelinventory.Model(nil), p.models...),
	}, nil
}

func effortValuesBoundRegistry(t *testing.T) modelregistry.BoundView {
	t.Helper()
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "fake", Kind: "fake", BackendPrefixes: []string{"fake"},
			Provider: effortValuesStaticInventory{models: []modelinventory.Model{
				{CanonicalID: "fake/gpt-6-native-xyz", NativeID: "gpt-6-native-xyz"},
				{CanonicalID: "fake/gpt-6-other", NativeID: "gpt-6-other-native"},
			}},
		}},
		Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start model registry: %v", err)
	}
	return rt.BoundView()
}

func effortValuesGuardConfig(strict bool) proxymemory.Config {
	cfg := proxymemory.DefaultConfig()
	cfg.ModelsDeclared = true
	cfg.Models.Strict = strict
	policy := proxymemory.PhasePolicy{
		Defaults: proxymemory.PhaseTarget{Backend: "fake", Model: "fake/gpt-6-native-xyz", ReasoningEffort: "high"},
		Inventory: proxymemory.PhaseInventory{
			Backends: []string{"fake"},
			Models:   []string{"fake/gpt-6-native-xyz", "fake/gpt-6-other"},
			Targets: []proxymemory.TargetPolicy{
				{Backend: "fake", Model: "fake/gpt-6-native-xyz", ReasoningEfforts: []string{"high", "none"}},
				{Backend: "fake", Model: "fake/gpt-6-other", ReasoningEfforts: []string{"high"}},
			},
		},
	}
	cfg.Models.Extraction = policy
	cfg.Models.Consolidation = policy
	return cfg
}

// effortValuesExecutor assembles the production executor with the real
// feature guard adapter, the real executable adapter, and a real host session
// to the fake connector.
func effortValuesExecutor(t *testing.T, strict bool) (*runtime.Executor, *effortValuesConnectorInstance, *proxymemory.Metrics) {
	t.Helper()
	instance := &effortValuesConnectorInstance{}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(
		backendplugin.ProtocolOffer{
			Major: 1, Minor: backendplugin.ProtocolMinorReasoningEffortValues,
			Features:                []backendplugin.Feature{{Name: backendplugin.FeatureReasoningEffortValues}},
			DisableTransportRetries: true,
		},
		effortValuesConnectorService{instance: instance},
	))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() { gs.Stop(); _ = lis.Close() })

	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	sess, profile, err := host.DialConfiguredSession(context.Background(), conn, "effort", "fake", nil,
		backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatalf("dial connector: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	br := adapter.Build(sess, profile, adapter.Options{InstanceID: "effort", Negotiation: sess.Negotiation()})

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{"fake": br.Backend}
	ex.Rand = routing.NewSeededRng(1)
	ex.BoundRegistry = effortValuesBoundRegistry(t)
	fingerprintKey := make([]byte, 32)
	for i := range fingerprintKey {
		fingerprintKey[i] = byte(i + 1)
	}
	mgr, err := app.NewManager(memory.New(memory.Options{SimulateDurable: true}), app.NewRandGenerator(fingerprintKey), b2bualineage.New(st), app.ManagerConfig{
		FingerprintKey: fingerprintKey,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatalf("secure session manager: %v", err)
	}
	ex.SecureSession = mgr
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.SyntheticLocalPrincipal = true
	metrics := proxymemory.NewPrometheusCollector()
	ex.AttemptGuard = proxyMemoryAttemptGuard{guard: proxymemory.NewAttemptGuard(effortValuesGuardConfig(strict), metrics, nil)}
	return ex, instance, metrics
}

func effortValuesContext(t *testing.T) context.Context {
	t.Helper()
	bound := effortValuesBoundRegistry(t)
	ctx := modelregistry.WithBoundView(context.Background(), bound)
	ctx = routing.WithNativeModelResolver(ctx, bound)
	return execctx.WithAuxiliaryRole(ctx, "proxy_memory_phase1")
}

func effortValuesCall(selector, effort string) *aipapi.Call {
	return &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: selector},
		Options: aipapi.GenerationOptions{ReasoningEffort: effort},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
}

func effortUnverifiedCount(metrics *proxymemory.Metrics) int {
	return testutil.CollectAndCount(metrics, "aip_proxy_memory_events_total")
}

// TestProxyMemoryExecutorEffortValuesPreOpenGuard is the composed proof: the
// real executor drives candidate admission, the feature guard adapter, the
// executable adapter and the host session, and only then opens the provider.
func TestProxyMemoryExecutorEffortValuesPreOpenGuard(t *testing.T) {
	t.Parallel()

	t.Run("supported reaches provider open without an unverified effort", func(t *testing.T) {
		t.Parallel()
		ex, instance, metrics := effortValuesExecutor(t, false)
		stream, err := ex.Execute(effortValuesContext(t), effortValuesCall("fake:fake/gpt-6-native-xyz", "high"))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if _, err := aipapi.Collect(context.Background(), stream); err != nil {
			t.Fatalf("collect: %v", err)
		}
		if got := instance.executes.Load(); got != 1 {
			t.Fatalf("provider opens = %d, want 1", got)
		}
		model, effort := instance.opened()
		if model != "gpt-6-native-xyz" || effort != "high" {
			t.Fatalf("provider open = %q/%q, want the exact native model and effort (resolved=%v)", model, effort, instance.resolvedModels())
		}
		if got := effortUnverifiedCount(metrics); got != 0 {
			t.Fatalf("unverified effort events = %d, want 0 for a provider-verified value", got)
		}
		if got := instance.factQueries.Load(); got == 0 {
			t.Fatalf("the model-specific effort declaration was never queried: %v", instance.resolvedModels())
		}
		last := instance.resolvedModels()
		if len(last) == 0 || last[len(last)-1] != "gpt-6-native-xyz" {
			t.Fatalf("the pre-open effort query used %v, want the exact native model", last)
		}
	})

	t.Run("unsupported is refused before provider open", func(t *testing.T) {
		t.Parallel()
		ex, instance, _ := effortValuesExecutor(t, false)
		_, err := ex.Execute(effortValuesContext(t), effortValuesCall("fake:fake/gpt-6-native-xyz", "none"))
		if err == nil {
			t.Fatal("Execute: want a pre-open refusal for an unsupported value")
		}
		if got := instance.executes.Load(); got != 0 {
			t.Fatalf("provider opens = %d, want 0 (never started)", got)
		}
	})

	t.Run("unknown non-strict serves and counts one unverified effort", func(t *testing.T) {
		t.Parallel()
		ex, instance, metrics := effortValuesExecutor(t, false)
		stream, err := ex.Execute(effortValuesContext(t), effortValuesCall("fake:fake/gpt-6-other", "high"))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if _, err := aipapi.Collect(context.Background(), stream); err != nil {
			t.Fatalf("collect: %v", err)
		}
		if got := instance.executes.Load(); got != 1 {
			t.Fatalf("provider opens = %d, want 1 under the non-strict policy", got)
		}
		if got := effortUnverifiedCount(metrics); got != 1 {
			t.Fatalf("unverified effort events = %d, want exactly 1", got)
		}
	})

	t.Run("unknown strict is refused before provider open", func(t *testing.T) {
		t.Parallel()
		ex, instance, metrics := effortValuesExecutor(t, true)
		_, err := ex.Execute(effortValuesContext(t), effortValuesCall("fake:fake/gpt-6-other", "high"))
		if err == nil {
			t.Fatal("Execute: want a pre-open refusal under strict policy")
		}
		if got := instance.executes.Load(); got != 0 {
			t.Fatalf("provider opens = %d, want 0 (never started)", got)
		}
		if got := effortUnverifiedCount(metrics); got != 0 {
			t.Fatalf("unverified effort events = %d, want 0 for a strict refusal", got)
		}
	})
}
