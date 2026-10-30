package runtimebundle_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"gopkg.in/yaml.v3"
)

// terminalDecisionGenerationProjection is the narrow immutable generation
// surface required by Task 5.1. The projection is intentionally discovered
// from the published request plane rather than through a generic dependency
// getter or process registry.
type terminalDecisionGenerationProjection interface {
	TerminalDecisionProvider() terminaldecision.Provider
}

type terminalDecisionTestProvider struct {
	id     string
	calls  atomic.Int32
	inputs chan terminaldecision.Input
}

func (p *terminalDecisionTestProvider) ID() string { return p.id }

func (p *terminalDecisionTestProvider) Decide(_ context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	p.calls.Add(1)
	if p.inputs != nil {
		select {
		case p.inputs <- in:
		default:
		}
	}
	return terminaldecision.Decision{
		Kind:       terminaldecision.DecisionAllowStop,
		ReasonCode: "test-allow",
	}, nil
}

type terminalDecisionFeatureFixture struct {
	process       *runtimebundle.ProcessServices
	firstFactory  string
	secondFactory string
	first         *terminalDecisionTestProvider
	second        *terminalDecisionTestProvider
	invalid       *terminalDecisionTestProvider
}

func newTerminalDecisionFeatureFixture(t *testing.T) *terminalDecisionFeatureFixture {
	t.Helper()
	registry := stdFactoryCatalog(t)
	fixture := &terminalDecisionFeatureFixture{
		firstFactory:  "terminal-decision-red-first",
		secondFactory: "terminal-decision-red-second",
		first:         &terminalDecisionTestProvider{id: "terminal-red.first"},
		second:        &terminalDecisionTestProvider{id: "terminal-red.second"},
		invalid:       &terminalDecisionTestProvider{id: ""},
	}
	register := func(factoryID string, provider terminaldecision.Provider) {
		t.Helper()
		err := registry.RegisterFeature(factoryID, func(yaml.Node) (aipfeature.FeatureBundle, error) {
			cs := aipfeature.NewContributionSet()
			if err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, factoryID, provider); err != nil {
				return aipfeature.FeatureBundle{}, err
			}
			return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
		})
		if err != nil {
			t.Fatalf("RegisterFeature(%q): %v", factoryID, err)
		}
	}
	register(fixture.firstFactory, fixture.first)
	register(fixture.secondFactory, fixture.second)
	register("terminal-decision-red-invalid", fixture.invalid)

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  processBaseConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: registry},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	fixture.process = ps
	return fixture
}

func terminalDecisionCandidate(t *testing.T, factoryID string, enabled bool) *config.Config {
	t.Helper()
	cfg := stubCandidateConfig(t, "terminal-decision-backend", "terminal-decision-text", "terminal-decision-backend:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	if factoryID != "" {
		cfg.Plugins.Features = []config.PluginConfig{{
			Kind:    factoryID,
			ID:      "terminal-decision-feature",
			Enabled: enabled,
			Config:  genYAMLNode(t, "{}"),
		}}
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate candidate: %v", err)
	}
	return cfg
}

func compileTerminalDecisionGeneration(t *testing.T, ps *runtimebundle.ProcessServices, candidate *config.Config) runtimebundle.GenerationRuntime {
	t.Helper()
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: candidate,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	return bundle
}

func publishTerminalDecisionBundle(t *testing.T, m *runtimehost.Manager, label string, bundle runtimebundle.GenerationRuntime) *runtimehost.Generation {
	t.Helper()
	g := m.PrepareRequestPlane(label, bundle)
	if err := m.Publish(g); err != nil {
		t.Fatalf("Publish(%s): %v", label, err)
	}
	return g
}

func projectedTerminalDecisionProvider(t *testing.T, lease *runtimehost.Lease) terminaldecision.Provider {
	t.Helper()
	if lease == nil || lease.RequestPlane() == nil {
		t.Fatal("admitted request has no immutable request plane")
	}
	projection, ok := lease.RequestPlane().(terminalDecisionGenerationProjection)
	if !ok {
		t.Fatal("published generation is missing TerminalDecisionProvider() projection")
	}
	return projection.TerminalDecisionProvider()
}

func TestTerminalDecisionLifecycle_InvalidCandidateLeavesLastGoodGenerationServing(t *testing.T) {
	t.Parallel()
	fixture := newTerminalDecisionFeatureFixture(t)
	m := runtimehost.NewManager(4, nil)
	good := compileTerminalDecisionGeneration(t, fixture.process, terminalDecisionCandidate(t, fixture.firstFactory, true))
	g1 := publishTerminalDecisionBundle(t, m, "terminal-good", good)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("Acquire good generation")
	}
	t.Cleanup(lease.Release)

	_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   fixture.process,
		Candidate: terminalDecisionCandidate(t, "terminal-decision-red-invalid", true),
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		t.Fatal("invalid provider candidate unexpectedly compiled")
	}
	if m.Active() != g1 {
		t.Fatal("failed candidate replaced last-good generation")
	}
	if got := projectedTerminalDecisionProvider(t, lease); got != fixture.first {
		t.Fatalf("admitted request provider=%p want first=%p", got, fixture.first)
	}
	body := postResponses(t, lease.Handler(), "stub-default")
	if body == "" {
		t.Fatal("last-good generation returned an empty response")
	}

	lease.Release()
	if err := m.ShutdownDetached(context.Background()); err != nil {
		t.Fatalf("ShutdownDetached: %v", err)
	}
}

func TestTerminalDecisionLifecycle_ReloadAndDisableKeepAdmittedProviderImmutable(t *testing.T) {
	t.Parallel()
	fixture := newTerminalDecisionFeatureFixture(t)
	m := runtimehost.NewManager(4, nil)
	first := compileTerminalDecisionGeneration(t, fixture.process, terminalDecisionCandidate(t, fixture.firstFactory, true))
	g1 := publishTerminalDecisionBundle(t, m, "terminal-first", first)
	oldLease, ok := m.Acquire()
	if !ok {
		t.Fatal("Acquire first generation")
	}
	t.Cleanup(oldLease.Release)
	if got := projectedTerminalDecisionProvider(t, oldLease); got != fixture.first {
		t.Fatalf("first admitted provider=%p want %p", got, fixture.first)
	}

	second := compileTerminalDecisionGeneration(t, fixture.process, terminalDecisionCandidate(t, fixture.secondFactory, true))
	g2 := publishTerminalDecisionBundle(t, m, "terminal-second", second)
	newLease, ok := m.Acquire()
	if !ok {
		t.Fatal("Acquire second generation")
	}
	t.Cleanup(newLease.Release)
	if got := projectedTerminalDecisionProvider(t, oldLease); got != fixture.first {
		t.Fatalf("reload mutated admitted provider=%p want first=%p", got, fixture.first)
	}
	if got := projectedTerminalDecisionProvider(t, newLease); got != fixture.second {
		t.Fatalf("new admitted provider=%p want second=%p", got, fixture.second)
	}

	disabled := compileTerminalDecisionGeneration(t, fixture.process, terminalDecisionCandidate(t, "", false))
	g3 := publishTerminalDecisionBundle(t, m, "terminal-disabled", disabled)
	disabledLease, ok := m.Acquire()
	if !ok {
		t.Fatal("Acquire disabled generation")
	}
	t.Cleanup(disabledLease.Release)
	if got := projectedTerminalDecisionProvider(t, disabledLease); got != nil {
		t.Fatalf("disabled generation provider=%p want nil", got)
	}
	if got := projectedTerminalDecisionProvider(t, oldLease); got != fixture.first {
		t.Fatalf("old admitted provider changed after disable=%p want first=%p", got, fixture.first)
	}
	if m.Active() != g3 || g1 == g2 || g2 == g3 {
		t.Fatal("reload did not publish distinct immutable generations")
	}

	oldLease.Release()
	newLease.Release()
	disabledLease.Release()
	if err := m.ShutdownDetached(context.Background()); err != nil {
		t.Fatalf("ShutdownDetached: %v", err)
	}
}

func TestTerminalDecisionLifecycle_CandidateActivationFailureUnwindsResourcesInReverseOrder(t *testing.T) {
	t.Parallel()
	fixture := newTerminalDecisionFeatureFixture(t)
	var mu sync.Mutex
	var order []string
	lifecycle := func(name string) aipplugin.Lifecycle {
		return &terminalDecisionLifecycleResource{name: name, mu: &mu, order: &order}
	}

	_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   fixture.process,
		Candidate: terminalDecisionCandidate(t, fixture.firstFactory, true),
		CandidateOpts: &runtimebundle.BuildOptions{
			FeatureLifecycles: []aipplugin.Lifecycle{lifecycle("first"), lifecycle("second")},
		},
		Compose: stdhttp.ComposeStandardHTTP,
		FaultInject: runtimebundle.CandidateFaultInject{
			After: "activate",
		},
	})
	if err == nil {
		t.Fatal("activation fault unexpectedly compiled")
	}
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	want := []string{"start:first", "start:second", "stop:second", "stop:first"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("provider-bearing candidate cleanup order=%v want %v", got, want)
	}
}

type terminalDecisionLifecycleResource struct {
	name  string
	mu    *sync.Mutex
	order *[]string
}

func (r *terminalDecisionLifecycleResource) Start(context.Context) error {
	r.mu.Lock()
	*r.order = append(*r.order, "start:"+r.name)
	r.mu.Unlock()
	return nil
}

func (r *terminalDecisionLifecycleResource) Stop(context.Context) error {
	r.mu.Lock()
	*r.order = append(*r.order, "stop:"+r.name)
	r.mu.Unlock()
	return nil
}

func (*terminalDecisionLifecycleResource) SafeUnderCandidateOverlap() bool { return true }
