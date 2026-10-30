package runtimebundle

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguardhost"
	"gopkg.in/yaml.v3"
)

func frozenSecretGuards(guards ...sdk.Guard) aipfeature.FrozenPlaneSet {
	cs := aipfeature.NewContributionSet()
	if len(guards) > 0 {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, "test-plugin", guards)
	}
	return cs.Freeze()
}

type panicSGEnv struct{ calls int }

func (p *panicSGEnv) Lookup(string) (string, bool) {
	p.calls++
	panic("secret guard disabled/multi_user must not call Environment")
}

func (p *panicSGEnv) Snapshot() []string {
	p.calls++
	panic("secret guard disabled/multi_user must not call Environment")
}

type mapSGEnv struct {
	vals map[string]string
	n    int
}

func (m *mapSGEnv) Lookup(name string) (string, bool) {
	m.n++
	v, ok := m.vals[name]
	return v, ok
}

func (m *mapSGEnv) Snapshot() []string {
	m.n++
	out := make([]string, 0, len(m.vals))
	for k, v := range m.vals {
		out = append(out, k+"="+v)
	}
	return out
}

type stubSecretGuard struct {
	id  string
	ord int
}

func (g stubSecretGuard) ID() string                 { return g.id }
func (g stubSecretGuard) Order() int                 { return g.ord }
func (stubSecretGuard) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (stubSecretGuard) Evaluate(context.Context, *aipapi.Call, sdk.Meta, sdk.Services) (sdk.Decision, error) {
	return sdk.Decision{Outcome: sdk.OutcomePass}, nil
}

func TestBuildSecretGuardRuntime_doesNotMutateBuildOptions(t *testing.T) {
	t.Parallel()
	env := &panicSGEnv{}
	binding := &secretguardhost.Binding{Environment: env}
	opts := &BuildOptions{
		FeaturePlanes: frozenSecretGuards(stubSecretGuard{id: "b", ord: 1}, stubSecretGuard{id: "a", ord: 1}),
		Production: ProductionOptions{
			FeatureHostRegistrations: []featurehost.Registration{binding.Registration()},
		},
	}
	before := opts.Production

	res, err := testBuildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opts.Production, before) {
		t.Fatalf("buildSecretGuardRuntime mutated BuildOptions.Production:\nbefore=%#v\nafter=%#v", before, opts.Production)
	}
	if env.calls != 0 {
		t.Fatalf("env calls=%d want 0", env.calls)
	}
	if res == nil || res.Plane.DecisionObserver == nil {
		t.Fatal("injected guards must still wire audit")
	}
}

func TestBuildSecretGuardRuntime_injectedGuardsSkipEnvironmentButWireAudit(t *testing.T) {
	t.Parallel()
	env := &panicSGEnv{}
	opts := &BuildOptions{
		FeaturePlanes: frozenSecretGuards(stubSecretGuard{id: "injected-without-feature"}),
		Production: ProductionOptions{
			FeatureHostRegistrations: []featurehost.Registration{
				(&secretguardhost.Binding{Environment: env}).Registration(),
			},
		},
	}

	res, err := testBuildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if env.calls != 0 {
		t.Fatalf("injected guards must not consult Environment; calls=%d", env.calls)
	}
	if res.Plane.DecisionObserver == nil {
		t.Fatal("injected guards must wire a secret-decision observer")
	}
	if res.Inventory == nil {
		t.Fatal("injected guards must still produce inventory metadata")
	}
}

func TestBuildSecretGuardRuntime_configuredGuardLoadsCatalogAndFreezesPlane(t *testing.T) {
	t.Parallel()
	env := &mapSGEnv{vals: map[string]string{
		"OPENAI_API_KEY": testkit.SyntheticOpenAIAPIKey,
	}}
	guards := []sdk.Guard{
		stubSecretGuard{id: "z", ord: 2},
		stubSecretGuard{id: "a", ord: 1},
		stubSecretGuard{id: "b", ord: 1},
	}
	opts := &BuildOptions{
		FeaturePlanes: frozenSecretGuards(guards...),
		Production: ProductionOptions{
			FeatureHostRegistrations: []featurehost.Registration{
				(&secretguardhost.Binding{Environment: env}).Registration(),
			},
		},
	}
	regs := []aipsdk.Registration{{
		Kind:        aipsdk.PluginKindFeature,
		ID:          "sg-main",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: mustNodeForRuntimebundle(t, "action: redact\naudit_failure_policy: best_effort\n")},
	}}

	res, err := testBuildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, regs)
	if err != nil {
		t.Fatal(err)
	}
	if env.n == 0 {
		t.Fatal("configured secrets-guard must consult Environment")
	}
	if res.Plane.MatcherResolver == nil {
		t.Fatal("configured secrets-guard must bind a matcher resolver")
	}
	m, err := res.Plane.MatcherResolver.Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("configured secrets-guard must resolve a matcher")
	}
	findings, err := m.ScanString(t.Context(), "x="+testkit.SyntheticOpenAIAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("configured secrets-guard matcher must find loaded secret")
	}
	guards[0] = stubSecretGuard{id: "mutated", ord: 0}
	if got := len(res.Plane.Guards); got != 0 {
		t.Fatalf("engine plane must not duplicate guards (they travel on planes); got %d", got)
	}
	frozenGuards := aipfeature.Get(opts.FeaturePlanes, aipfeature.PlaneSecretGuards)
	if got := frozenGuards[0].ID(); got != "z" {
		t.Fatalf("planes mutated via caller slice; got %q want z (unsorted clone)", got)
	}
	snap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
		SecretGuardPlane: res.Plane,
		FeaturePlanes:    opts.FeaturePlanes,
	})
	if got := snap.SecretGuardExecutionPlane().Guards[0].ID(); got != "a" {
		t.Fatalf("snapshot must sort guards; got %q want a", got)
	}
	if res.Inventory == nil || res.Inventory.CatalogEntryCount == 0 {
		t.Fatalf("expected inventory metadata from returned composition result: %#v", res.Inventory)
	}
}

func TestBuildSecretGuardRuntime_multiUserEnabledSkipsEnvironment(t *testing.T) {
	t.Parallel()
	env := &panicSGEnv{}
	opts := &BuildOptions{
		Production: ProductionOptions{
			FeatureHostRegistrations: []featurehost.Registration{
				(&secretguardhost.Binding{Environment: env}).Registration(),
			},
		},
	}
	regs := []aipsdk.Registration{{
		Kind:        aipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: mustNodeForRuntimebundle(t, "action: block\n")},
	}}
	cfg := &config.Config{Access: config.AccessConfig{Mode: "multi_user"}}
	res, err := testBuildSecretGuardRuntime(cfg, slog.Default(), opts, regs)
	if err != nil {
		t.Fatal(err)
	}
	if env.calls != 0 {
		t.Fatalf("multi_user must not consult Environment; calls=%d", env.calls)
	}
	if res == nil || res.Plane.AccessMode != "multi_user" {
		t.Fatalf("AccessMode=%q want multi_user", res.Plane.AccessMode)
	}
	if res.Inventory == nil || res.Inventory.AccessMode != "multi_user" {
		t.Fatalf("inventory access mode=%#v", res.Inventory)
	}
}

func TestBuildSecretGuardRuntime_rejectsMultipleEnabledBeforeEnv(t *testing.T) {
	t.Parallel()
	env := &panicSGEnv{}
	opts := &BuildOptions{
		Production: ProductionOptions{
			FeatureHostRegistrations: []featurehost.Registration{
				(&secretguardhost.Binding{Environment: env}).Registration(),
			},
		},
	}
	regs := []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "sg-a", FactoryKind: "secrets-guard", Enabled: true, Config: aipsdk.ConfigPayload{Node: mustNodeForRuntimebundle(t, "action: log\n")}},
		{Kind: aipsdk.PluginKindFeature, ID: "sg-b", FactoryKind: "secrets-guard", Enabled: true, Config: aipsdk.ConfigPayload{Node: mustNodeForRuntimebundle(t, "action: redact\n")}},
	}
	_, err := testBuildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, regs)
	if err == nil {
		t.Fatal("expected duplicate enabled secrets-guard registrations to fail")
	}
	if env.calls != 0 {
		t.Fatalf("env calls=%d", env.calls)
	}
	for _, bad := range []string{"action", "log", "redact"} {
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("error leaked %q: %v", bad, err)
		}
	}
}

type typedNilDecisionObserver struct{}

func (*typedNilDecisionObserver) OnSecretDecision(context.Context, sdk.DecisionEvent) error {
	return nil
}

func TestBuildSecretGuardRuntime_typedNilObserverFallsBackToSlog(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	var typedNil *typedNilDecisionObserver
	fh, err := featurehost.NewProcess(context.Background(), featurehost.ProcessInput{Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	// The observer enters composition through the pinned GenerationInput port;
	// the composed plane is read back purely via plane access.
	out, err := fh.CompileGeneration(context.Background(), featurehost.GenerationInput{
		Planes:           frozenSecretGuards(stubSecretGuard{id: "guard", ord: 1}),
		AccessMode:       "single_user",
		DecisionObserver: typedNil,
	})
	if err != nil {
		t.Fatal(err)
	}
	plane, _ := secretGuardFromPlanes(out.Planes)
	if sdk.IsNilObserver(plane.DecisionObserver) {
		t.Fatal("typed-nil observer must be replaced with a usable runtime observer")
	}
	if err := plane.DecisionObserver.OnSecretDecision(t.Context(), sdk.DecisionEvent{EventID: "evt-typed-nil"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "aip.secret_guard.decision") {
		t.Fatalf("expected slog fallback decision log, got %q", buf.String())
	}
}

func mustNodeForRuntimebundle(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}
