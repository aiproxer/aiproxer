package runtimebundle_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

func TestCompileGeneration_NilHandlerRollsBackCandidate(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "nilh", "x", "nilh:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: func(context.Context, *config.Config, *slog.Logger, stdhttp.StandardHTTPInput) (http.Handler, error) {
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("expected nil handler rejection")
	}
	if !strings.Contains(err.Error(), "nil handler") {
		t.Fatalf("err=%v", err)
	}
	if ps.Closed() {
		t.Fatal("process closed on nil handler")
	}
	ok, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "nilh2", "y", "nilh2:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("recompile after nil-handler rollback: %v", err)
	}
	t.Cleanup(func() { _ = ok.Close() })
}

// TestCompileGeneration_ComposerReceivesDefensiveConfigClone proves the
// HandlerComposer is never lent the caller candidate or the canonical frozen
// generation source: it receives a deep defensive clone (including nested YAML
// nodes/slices). Mutations during and after composition must not poison the
// caller candidate or the published immutable generation views.
func TestCompileGeneration_ComposerReceivesDefensiveConfigClone(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	cand := stubCandidateConfig(t, "own", "own-text", "own:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	origRoute := cand.Routing.DefaultRoute
	origFEID := cand.Plugins.Frontends[0].ID
	origBackendID := cand.Plugins.Backends[0].ID
	origBackendText := nestedYAMLValue(t, cand.Plugins.Backends[0].Config, "text")
	if origBackendText != "own-text" {
		t.Fatalf("fixture backend yaml: %q", origBackendText)
	}
	// Capture nested Content element pointers from the caller candidate so we
	// can prove the composer clone does not share YAML node identity.
	var candBEContent0 *yaml.Node
	if len(cand.Plugins.Backends[0].Config.Content) > 0 {
		candBEContent0 = cand.Plugins.Backends[0].Config.Content[0]
	}
	candFESlice := cand.Plugins.Frontends

	var received *config.Config
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in stdhttp.StandardHTTPInput) (http.Handler, error) {
			if cfg == nil {
				t.Fatal("composer cfg is nil")
			}
			received = cfg
			// Distinct root pointer from the caller candidate.
			if cfg == cand {
				t.Fatal("composer received caller candidate pointer")
			}
			// Nested plugin slices must not alias the caller candidate.
			if len(cfg.Plugins.Frontends) > 0 && len(candFESlice) > 0 {
				if &cfg.Plugins.Frontends[0] == &candFESlice[0] {
					t.Fatal("composer frontends slice aliases caller candidate")
				}
			}
			// Nested YAML node identity must differ from the caller candidate
			// (deep clone, not shallow struct/slice copy).
			if candBEContent0 != nil && len(cfg.Plugins.Backends) > 0 && len(cfg.Plugins.Backends[0].Config.Content) > 0 {
				if cfg.Plugins.Backends[0].Config.Content[0] == candBEContent0 {
					t.Fatal("composer backend YAML Content aliases caller candidate")
				}
			}
			// Mutate nested frontend/config/routing during composition after
			// building a valid handler but before return — if the composer was
			// lent the canonical frozen source, publication (which reads frozen
			// after compose) would observe these values.
			handler, err := stdhttp.ComposeStandardHTTP(ctx, cfg, log, in)
			if err != nil {
				return nil, err
			}
			cfg.Routing.DefaultRoute = "compose-mutated:gone"
			cfg.Routing.MaxAttempts = 99
			if len(cfg.Plugins.Frontends) > 0 {
				cfg.Plugins.Frontends[0].ID = "compose-mutated-fe"
				cfg.Plugins.Frontends[0].Enabled = false
			}
			if len(cfg.Plugins.Backends) > 0 {
				cfg.Plugins.Backends[0].ID = "compose-mutated-be"
				mutateNestedYAMLValue(t, &cfg.Plugins.Backends[0].Config, "text", "compose-mutated-text")
			}
			return handler, nil
		},
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	if received == nil {
		t.Fatal("composer did not capture cfg")
	}
	gb, ok := bundle.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}

	// Caller candidate must remain the original source of truth.
	if cand.Routing.DefaultRoute != origRoute {
		t.Fatalf("caller routing mutated: %q", cand.Routing.DefaultRoute)
	}
	if cand.Routing.MaxAttempts != 3 {
		t.Fatalf("caller max_attempts mutated: %d", cand.Routing.MaxAttempts)
	}
	if cand.Plugins.Frontends[0].ID != origFEID || !cand.Plugins.Frontends[0].Enabled {
		t.Fatalf("caller frontend mutated: id=%q enabled=%v", cand.Plugins.Frontends[0].ID, cand.Plugins.Frontends[0].Enabled)
	}
	if cand.Plugins.Backends[0].ID != origBackendID {
		t.Fatalf("caller backend id mutated: %q", cand.Plugins.Backends[0].ID)
	}
	if got := nestedYAMLValue(t, cand.Plugins.Backends[0].Config, "text"); got != origBackendText {
		t.Fatalf("caller backend yaml mutated: %q", got)
	}

	// Published immutable generation state must reflect the original candidate,
	// not the composer-time mutations (proves composer was not lent canonical
	// frozen — publication reads frozen.Plugins.Frontends after compose).
	if gb.Routing().DefaultRoute != origRoute {
		t.Fatalf("published routing poisoned during compose: %q", gb.Routing().DefaultRoute)
	}
	frontends := gb.FrozenFrontends()
	if len(frontends) == 0 {
		t.Fatal("expected published frontends")
	}
	if frontends[0].ID != origFEID {
		t.Fatalf("published frontend id poisoned during compose: %q", frontends[0].ID)
	}
	if !frontends[0].Enabled {
		t.Fatal("published frontend enabled poisoned during compose")
	}
	regs := gb.Registrations()
	if len(regs) == 0 {
		t.Fatal("expected published registrations")
	}
	for _, r := range regs {
		if strings.Contains(r.ID, "compose-mutated") {
			t.Fatalf("published registration poisoned during compose: %q", r.ID)
		}
	}
	kinds := bundle.BackendFactoryKindCounts()
	if kinds == nil || kinds["local-stub"] < 1 {
		t.Fatalf("published capabilities poisoned: %#v", kinds)
	}

	// Post-return retained-pointer mutation must not affect caller or published
	// immutable views either.
	received.Routing.DefaultRoute = "post-mutated:gone"
	received.Routing.MaxAttempts = 1
	if len(received.Plugins.Frontends) > 0 {
		received.Plugins.Frontends[0].ID = "post-mutated-fe"
		received.Plugins.Frontends[0].Enabled = false
	}
	if len(received.Plugins.Backends) > 0 {
		received.Plugins.Backends[0].ID = "post-mutated-be"
		mutateNestedYAMLValue(t, &received.Plugins.Backends[0].Config, "text", "post-mutated-text")
	}

	if cand.Routing.DefaultRoute != origRoute || cand.Plugins.Frontends[0].ID != origFEID {
		t.Fatal("caller candidate mutated via retained composer pointer")
	}
	if got := nestedYAMLValue(t, cand.Plugins.Backends[0].Config, "text"); got != origBackendText {
		t.Fatalf("caller backend yaml mutated post-compose: %q", got)
	}
	if gb.Routing().DefaultRoute != origRoute {
		t.Fatalf("published routing poisoned post-compose: %q", gb.Routing().DefaultRoute)
	}
	if got := gb.FrozenFrontends()[0].ID; got != origFEID {
		t.Fatalf("published frontend id poisoned post-compose: %q", got)
	}
	if !gb.FrozenFrontends()[0].Enabled {
		t.Fatal("published frontend enabled poisoned post-compose")
	}
	body := postResponses(t, bundle.Handler(), "stub-default")
	if !strings.Contains(body, "own-text") {
		t.Fatalf("handler followed mutated backend yaml: %s", body)
	}
	if strings.Contains(body, "compose-mutated") || strings.Contains(body, "post-mutated") {
		t.Fatalf("handler observed composer mutations: %s", body)
	}
}

func TestCompileGeneration_DeepFreezeRegistrationsAndYAML(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	cand := stubCandidateConfig(t, "freeze", "freeze-text", "freeze:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	origBackendNode := cand.Plugins.Backends[0].Config

	var planeRegs []aipsdk.Registration
	var stackCfg *config.Config
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in stdhttp.StandardHTTPInput) (http.Handler, error) {
			planeRegs = in.Operations.Registrations
			stackCfg = cfg
			if stackCfg == nil {
				t.Fatal("cfg param is nil")
			}
			return stdhttp.ComposeStandardHTTP(ctx, cfg, log, in)
		},
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	gb, ok := bundle.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}

	cand.Routing.DefaultRoute = "mutated:gone"
	cand.Plugins.Backends[0].Config = genYAMLNode(t, `text: "mutated-backend"`)
	if len(origBackendNode.Content) > 0 {
		origBackendNode.Content[0].Value = "poison"
	}
	if len(planeRegs) > 0 {
		planeRegs[0].ID = "mutated-reg"
		planeRegs[0].Config.Node = genYAMLNode(t, `{"poison":true}`)
	}
	returned := gb.Registrations()
	if len(returned) == 0 {
		t.Fatal("expected registrations")
	}
	returned[0].ID = "mutated-returned"
	returned[0].Config.Node = genYAMLNode(t, `{"poison":"returned"}`)
	frontends := gb.FrozenFrontends()
	frontends[0].ID = "mutated-fe"
	frontends[0].Config = genYAMLNode(t, `{"label":"mutated"}`)
	if stackCfg != nil {
		stackCfg.Routing.DefaultRoute = "stack-mutated"
		if len(stackCfg.Plugins.Frontends) > 0 {
			stackCfg.Plugins.Frontends[0].ID = "stack-fe"
		}
	}

	body := postResponses(t, bundle.Handler(), "stub-default")
	if !strings.Contains(body, "freeze-text") {
		t.Fatalf("handler followed mutated YAML: %s", body)
	}
	if gb.Routing().DefaultRoute != "freeze:stub-default" {
		t.Fatalf("routing mutated: %q", gb.Routing().DefaultRoute)
	}
	if got := gb.FrozenFrontends()[0].ID; got != "openai-responses" {
		t.Fatalf("frontend id mutated: %q", got)
	}
	if got := gb.Registrations()[0].ID; strings.Contains(got, "mutated") {
		t.Fatalf("registration id mutated: %q", got)
	}
	rr := httptest.NewRecorder()
	bundle.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz after mutation: %d", rr.Code)
	}
}

func TestCompileGeneration_FeatureSurfaceNoStartupLeakOrDuplicate(t *testing.T) {
	t.Parallel()
	startupLife := &overlapLife{}
	candLife := &overlapLife{}
	var startupObs, prodObs, candObs atomic.Int32
	var startupHook, candHook atomic.Int32

	cfg := processBaseConfig()
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	cat := stdFactoryCatalog(t)
	err := cat.RegisterFeature("cand-obs-feature", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "cand-obs-feature", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "cand-obs-feature", []traffic.Observer{countTrafficObs{n: &candObs}})
		}, nil), nil
	})
	require.NoError(t, err)

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{
			PluginRegistry:    cat,
			FeatureLifecycles: []aipplugin.Lifecycle{startupLife},
			FeaturePlanes: frozenRequestTransform(countTransform{
				id: "startup-hook",
				n:  &startupHook,
			}),
			Production: runtimebundle.ProductionOptions{
				TrafficObservers: []traffic.Observer{countTrafficObs{n: &prodObs}},
			},
		},
		Tracing: runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	// probeTraffic checks traffic-observer isolation through the focused HTTP
	// composition input (task 3.4): TrafficPorts.Obs is the same generation
	// extension snapshot observer projected without RequestPlane. Request-
	// transform isolation (not part of the HTTP composition boundary) is
	// covered separately by TestCompileCandidate_RequestTransformNoLeak.
	probeTraffic := func(in stdhttp.StandardHTTPInput) {
		t.Helper()
		obs := in.Frontends.TrafficPorts.Obs
		if obs == nil {
			t.Fatal("expected traffic observer")
		}
		startupObs.Store(0)
		prodObs.Store(0)
		candObs.Store(0)
		_ = obs.OnObservation(context.Background(), traffic.Observation{Leg: traffic.LegCTP})
		if got := startupObs.Load(); got != 0 {
			t.Fatalf("startup observer leaked calls=%d", got)
		}
		if got := prodObs.Load(); got != 1 {
			t.Fatalf("production observer calls=%d want 1", got)
		}
		if got := candObs.Load(); got != 1 {
			t.Fatalf("candidate observer calls=%d want 1", got)
		}
	}

	candCfgA := stubCandidateConfig(t, "feat-a", "a", "feat-a:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	candCfgA.Plugins.Features = []config.PluginConfig{
		{ID: "cand-obs-feature", Enabled: true},
	}

	a, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: candCfgA,
		CandidateOpts: &runtimebundle.BuildOptions{
			FeatureLifecycles: []aipplugin.Lifecycle{candLife},
			FeaturePlanes:     frozenRequestTransform(countTransform{id: "cand-hook", n: &candHook}),
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in stdhttp.StandardHTTPInput) (http.Handler, error) {
			probeTraffic(in)
			return stdhttp.ComposeStandardHTTP(ctx, cfg, log, in)
		},
	})
	if err != nil {
		t.Fatalf("compile A: %v", err)
	}
	if startupLife.starts.Load() != 0 {
		t.Fatalf("startup lifecycle started=%d", startupLife.starts.Load())
	}
	if candLife.starts.Load() != 1 {
		t.Fatalf("candidate lifecycle starts=%d", candLife.starts.Load())
	}

	b, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "feat-b", "b", "feat-b:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		// Intentionally empty candidate surface: must not reuse startup lifecycles/hooks.
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in stdhttp.StandardHTTPInput) (http.Handler, error) {
			obs := in.Frontends.TrafficPorts.Obs
			startupObs.Store(0)
			prodObs.Store(0)
			candObs.Store(0)
			if obs != nil {
				_ = obs.OnObservation(context.Background(), traffic.Observation{Leg: traffic.LegCTP})
			}
			if startupObs.Load() != 0 {
				t.Fatal("empty candidate retained startup traffic observer")
			}
			if prodObs.Load() != 1 {
				t.Fatalf("production observer calls=%d", prodObs.Load())
			}
			if candObs.Load() != 0 {
				t.Fatal("empty candidate retained prior candidate observer")
			}
			return stdhttp.ComposeStandardHTTP(ctx, cfg, log, in)
		},
	})
	if err != nil {
		t.Fatalf("compile B: %v", err)
	}
	if startupLife.starts.Load() != 0 {
		t.Fatalf("startup lifecycle started on empty candidate=%d", startupLife.starts.Load())
	}
	if candLife.starts.Load() != 1 {
		t.Fatalf("candidate A lifecycle starts mutated=%d", candLife.starts.Load())
	}
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if candLife.stops.Load() != 1 {
		t.Fatalf("candidate stops=%d", candLife.stops.Load())
	}
	if startupLife.stops.Load() != 0 {
		t.Fatalf("startup lifecycle stopped=%d", startupLife.stops.Load())
	}
}

// TestCompileGeneration_RequestTransformNoStartupLeakOrDuplicate proves
// candidate-scoped request transforms neither leak startup-merged transforms
// into a candidate nor persist across an unrelated candidate compile
// (companion to the traffic-observer probe in
// TestCompileGeneration_FeatureSurfaceNoStartupLeakOrDuplicate). Request
// transforms run inside the executor's real request pipeline (below the HTTP
// composition boundary), so isolation is proven end-to-end through the
// composed handler rather than by introspecting a retained snapshot.
func TestCompileGeneration_RequestTransformNoStartupLeakOrDuplicate(t *testing.T) {
	t.Parallel()
	var startupHook, candHook atomic.Int32
	cfg := processBaseConfig()
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{
			PluginRegistry: stdFactoryCatalog(t),
			FeaturePlanes:  frozenRequestTransform(countTransform{id: "startup-hook", n: &startupHook}),
		},
		Tracing: runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	a, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "rt-a", "a", "rt-a:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		CandidateOpts: &runtimebundle.BuildOptions{
			FeaturePlanes: frozenRequestTransform(countTransform{id: "cand-hook", n: &candHook}),
		},
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile A: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	startupHook.Store(0)
	candHook.Store(0)
	_ = postResponses(t, a.Handler(), "stub-default")
	if got := startupHook.Load(); got != 0 {
		t.Fatalf("startup request transform leaked calls=%d", got)
	}
	if got := candHook.Load(); got != 1 {
		t.Fatalf("candidate request transform calls=%d want 1", got)
	}

	b, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "rt-b", "b", "rt-b:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		// Intentionally empty candidate surface: must not reuse candidate A's transform.
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile B: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	startupHook.Store(0)
	candHook.Store(0)
	_ = postResponses(t, b.Handler(), "stub-default")
	if got := startupHook.Load(); got != 0 {
		t.Fatalf("empty candidate retained startup request transform, calls=%d", got)
	}
	if got := candHook.Load(); got != 0 {
		t.Fatalf("empty candidate retained candidate A request transform, calls=%d", got)
	}
}

func TestCompileGeneration_BundleQuiesceBeforeClose_LifecycleStopOnce(t *testing.T) {
	t.Parallel()
	life := &overlapLife{}
	ps := newProcessForGeneration(t)
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "retire", "retire-text", "retire:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		CandidateOpts: &runtimebundle.BuildOptions{FeatureLifecycles: []aipplugin.Lifecycle{life}},
		Compose:       stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	gb, ok := bundle.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}
	if life.starts.Load() != 1 {
		t.Fatalf("starts=%d", life.starts.Load())
	}
	before := gb.ResourceCount()
	if before == 0 {
		t.Fatal("expected resources")
	}
	if err := bundle.Quiesce(context.Background()); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	afterQuiesce := gb.ResourceCount()
	if afterQuiesce > before {
		t.Fatalf("quiesce increased resources %d -> %d", before, afterQuiesce)
	}
	if life.stops.Load() != 0 {
		t.Fatal("lifecycle Stop must not run during Quiesce")
	}

	m := runtimehost.NewManager(2, nil)
	g := m.PrepareRequestPlane("retire", bundle)
	mustPublishGen(t, m, g)
	mustPublishGen(t, m, m.Prepare("next"))

	if _, err := m.RetireGeneration(context.Background(), g); err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("Retire: %v", err)
	}
	if life.stops.Load() != 1 {
		t.Fatalf("lifecycle stops=%d want 1", life.stops.Load())
	}
	if g.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v", g.Lifecycle())
	}
	if _, err := m.RetireGeneration(context.Background(), g); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("second retire: %v", err)
	}
	if life.stops.Load() != 1 {
		t.Fatalf("doubled stops=%d", life.stops.Load())
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if life.stops.Load() != 1 {
		t.Fatalf("bundle close doubled stops=%d", life.stops.Load())
	}
}

func TestCompileGeneration_RequestPlaneBoundOnPublishAcquire(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "bind", "bind-text", "bind:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}

	m := runtimehost.NewManager(2, nil)
	g := m.BeginPrepareRequestPlane("bind", bundle)
	if g.RequestPlane() != bundle {
		t.Fatal("begin-prepare must bind request plane")
	}
	if err := g.AttachRequestPlane(bundle); !errors.Is(err, runtimehost.ErrRequestPlaneAlreadyBound) {
		t.Fatalf("rebind: %v", err)
	}
	if err := g.MarkPrepared(); err != nil {
		t.Fatal(err)
	}
	mustPublishGen(t, m, g)
	if err := g.AttachRequestPlane(bundle); !errors.Is(err, runtimehost.ErrIllegalTransition) {
		t.Fatalf("post-publish rebind: %v", err)
	}

	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	defer lease.Release()
	if lease.RequestPlane() != bundle {
		t.Fatal("lease must identify exact request-plane bundle")
	}
	if lease.Handler() == nil {
		t.Fatal("lease handler is nil")
	}
	body := postResponses(t, lease.Handler(), "stub-default")
	if !strings.Contains(body, "bind-text") {
		t.Fatalf("body=%s", body)
	}

	// Retention rejection closes attached bundle exactly once.
	m2 := runtimehost.NewManager(1, nil)
	g1 := m2.Prepare("keep")
	mustPublishGen(t, m2, g1)
	lease2, ok := m2.Acquire()
	if !ok {
		t.Fatal("acquire keep")
	}
	pin, ok := lease2.TransferPin(runtimehost.PinAsync)
	if !ok {
		t.Fatal("pin")
	}
	mustPublishGen(t, m2, m2.Prepare("fill"))
	blocked, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "blocked", "z", "blocked:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	cand := m2.PrepareRequestPlane("blocked", blocked)
	pubErr := m2.Publish(cand)
	if !errors.Is(pubErr, runtimehost.ErrRetentionBlocked) {
		t.Fatalf("want retention blocked, got %v", pubErr)
	}
	if cand.CloseCount() != 1 {
		t.Fatalf("close count=%d", cand.CloseCount())
	}
	if err := blocked.Close(); err != nil {
		t.Fatal(err)
	}
	pin.Release()
	_ = bundle.Close()
}

func TestCompileCandidate_DoesNotPruneProcessPools(t *testing.T) {
	t.Parallel()
	cfg := processBaseConfig()
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{
			PluginRegistry: stdFactoryCatalog(t),
			Testing: runtimebundle.TestingOptions{
				PostgresPoolOpener: func(context.Context, string, db.PoolSettings) (*bun.DB, error) {
					sqlDB, err := sql.Open("sqlite", ":memory:")
					if err != nil {
						return nil, err
					}
					opened, err := db.NewBunDB(sqlDB, db.DialectSQLite)
					if err != nil {
						return nil, err
					}
					return opened, nil
				},
			},
		},
		Tracing: runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	unclaimed, err := ps.DatabasePools.Open(context.Background(), "postgres://candidate-compile/unclaimed", db.PoolSettings{})
	if err != nil {
		t.Fatalf("open unclaimed: %v", err)
	}
	before := ps.DatabasePools.Len()
	if before < 1 {
		t.Fatal("expected unclaimed pool registered")
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("pool-%d", i)
			candCfg := stubCandidateConfig(t, id, "p", id+":stub-default", []config.PluginConfig{
				{ID: "openai-responses", Enabled: true},
			})
			c, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
				Process:   ps,
				Bus:       hooks.New(hooks.Config{}),
				Candidate: candCfg,
			})
			if err != nil {
				errCh <- err
				return
			}
			if err := c.Close(); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("compile: %v", err)
	}
	if ps.DatabasePools.Len() != before {
		t.Fatalf("pool len=%d want %d (candidate compile must not prune)", ps.DatabasePools.Len(), before)
	}
	if err := unclaimed.PingContext(context.Background()); err != nil {
		t.Fatalf("unclaimed pool closed by candidate compile: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatal(err)
	}
	if !ps.Closed() {
		t.Fatal("process close must own pools")
	}
}

func mustPublishGen(t *testing.T, m *runtimehost.Manager, g *runtimehost.Generation) {
	t.Helper()
	if err := m.Publish(g); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

type countTrafficObs struct{ n *atomic.Int32 }

func (c countTrafficObs) OnObservation(context.Context, traffic.Observation) error {
	c.n.Add(1)
	return nil
}

type countTransform struct {
	id string
	n  *atomic.Int32
}

func (c countTransform) ID() string                        { return c.id }
func (c countTransform) Order() int                        { return 0 }
func (c countTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (c countTransform) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	c.n.Add(1)
	return nil
}

func frozenRequestTransform(t request.Transform) aipfeature.FrozenPlaneSet {
	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "test-plugin", []request.Transform{t})
	return cs.Freeze()
}
