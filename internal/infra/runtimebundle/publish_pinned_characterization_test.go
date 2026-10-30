package runtimebundle_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"gopkg.in/yaml.v3"
)

// --- Extension Stubs for RuntimeBundle Pinned Concurrency Characterization ---

type charBundleSessionOpener struct{ id string }

func (s charBundleSessionOpener) ID() string { return s.id }
func (charBundleSessionOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type charBundleToolCatalogFilter struct{ id string }

func (c charBundleToolCatalogFilter) ID() string                      { return c.id }
func (charBundleToolCatalogFilter) Order() int                        { return 0 }
func (charBundleToolCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charBundleToolCatalogFilter) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type charBundleToolCallPolicy struct {
	id  string
	ord int
}

func (p charBundleToolCallPolicy) ID() string                      { return p.id }
func (p charBundleToolCallPolicy) Order() int                      { return p.ord }
func (charBundleToolCallPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charBundleToolCallPolicy) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type charBundleToolCallFinalizer struct {
	id  string
	ord int
}

func (f charBundleToolCallFinalizer) ID() string { return f.id }
func (f charBundleToolCallFinalizer) Order() int { return f.ord }
func (charBundleToolCallFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type charBundleRequestTransform struct{ id string }

func (r charBundleRequestTransform) ID() string                      { return r.id }
func (charBundleRequestTransform) Order() int                        { return 0 }
func (charBundleRequestTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charBundleRequestTransform) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type charBundlePreRequestHandler struct{ id string }

func (p charBundlePreRequestHandler) ID() string                      { return p.id }
func (charBundlePreRequestHandler) Order() int                        { return 0 }
func (charBundlePreRequestHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charBundlePreRequestHandler) Handle(context.Context, *aipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}

type charBundleRouteHintProvider struct{ id string }

func (rh charBundleRouteHintProvider) ID() string                     { return rh.id }
func (charBundleRouteHintProvider) Order() int                        { return 0 }
func (charBundleRouteHintProvider) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charBundleRouteHintProvider) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type charBundleCompletionGate struct{ id string }

func (g charBundleCompletionGate) ID() string                      { return g.id }
func (charBundleCompletionGate) Order() int                        { return 0 }
func (charBundleCompletionGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charBundleCompletionGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type charBundleAttemptTransform struct{ id string }

func (a charBundleAttemptTransform) ID() string                      { return a.id }
func (charBundleAttemptTransform) Order() int                        { return 0 }
func (charBundleAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (charBundleAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type charBundleStreamObserverFactory struct{ id string }

func (s charBundleStreamObserverFactory) ID() string                      { return s.id }
func (charBundleStreamObserverFactory) Order() int                        { return 0 }
func (charBundleStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (charBundleStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type charBundleTrafficRedactor struct{ id string }

func (tr charBundleTrafficRedactor) ID() string { return tr.id }
func (charBundleTrafficRedactor) Redact(context.Context, sdktraffic.Leg, sdktraffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

type charBundleSecretGuard struct {
	id  string
	ord int
}

func (sg charBundleSecretGuard) ID() string { return sg.id }
func (sg charBundleSecretGuard) Order() int { return sg.ord }
func (charBundleSecretGuard) FailureMode() secretguard.FailureMode {
	return secretguard.FailClosed
}

func (charBundleSecretGuard) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type charBundleLocalTurnHandler struct {
	id  string
	ord int
}

func (lt charBundleLocalTurnHandler) ID() string { return lt.id }
func (lt charBundleLocalTurnHandler) Order() int { return lt.ord }
func (charBundleLocalTurnHandler) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

func (charBundleLocalTurnHandler) Match(context.Context, aipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{Claimed: false}, nil
}

func (charBundleLocalTurnHandler) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{Text: "char-bundle-local-turn"}, nil
}

type charBundleTerminalProvider struct{ id string }

func (p *charBundleTerminalProvider) ID() string {
	if p == nil {
		return ""
	}
	return p.id
}

func (p *charBundleTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "char-done"}, nil
}

func newProcessForPinnedGeneration(t *testing.T) *runtimebundle.ProcessServices {
	t.Helper()
	cat := stdFactoryCatalog(t)
	err := cat.RegisterFeature("char-bundle-feature", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		var cfg struct {
			Label string `yaml:"label"`
		}
		_ = n.Decode(&cfg)
		label := cfg.Label
		return testkit.FeatureBundle(t, "char-bundle-feature", func(cs *aipfeature.ContributionSet) error {
			if err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, label+"-terminal", terminaldecision.Provider(&charBundleTerminalProvider{id: label + "-terminal"})); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "char-bundle-feature", []localturn.Handler{
				charBundleLocalTurnHandler{id: label + "-localturn", ord: 0},
			}); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "char-bundle-feature", []session.Opener{
				charBundleSessionOpener{id: label + "-session"},
			}); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCatalogFilters, "char-bundle-feature", []toolcatalog.Filter{
				charBundleToolCatalogFilter{id: label + "-catalog"},
			}); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCallPolicies, "char-bundle-feature", []toolpolicy.Policy{
				charBundleToolCallPolicy{id: label + "-policy"},
			}); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, "char-bundle-feature", []toolcall.Finalizer{
				charBundleToolCallFinalizer{id: label + "-finalizer"},
			}); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "char-bundle-feature", []request.Transform{
				charBundleRequestTransform{id: label + "-reqxform"},
			}); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "char-bundle-feature", []prerequest.Handler{
				charBundlePreRequestHandler{id: label + "-prereq"},
			}); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlaneRouteHintProviders, "char-bundle-feature", []routehint.Provider{
				charBundleRouteHintProvider{id: label + "-routehint"},
			}); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlaneCompletionGates, "char-bundle-feature", []completion.Gate{
				charBundleCompletionGate{id: label + "-gate"},
			}); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "char-bundle-feature", []request.AttemptTransform{
				charBundleAttemptTransform{id: label + "-attxform"},
			}); err != nil {
				return err
			}
			return aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, "char-bundle-feature", []secretguard.Guard{
				charBundleSecretGuard{id: label + "-sg", ord: 0},
			})
		}, nil), nil
	})
	require.NoError(t, err)
	cfg := processBaseConfig()
	require.NoError(t, config.Validate(cfg))
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: cat},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

func pinnedCandidateConfig(t *testing.T, backendID, text, defaultRoute, label string) *config.Config {
	t.Helper()
	cfg := stubCandidateConfig(t, backendID, text, defaultRoute, []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	cfg.Plugins.Features = []config.PluginConfig{
		{
			Kind:    "char-bundle-feature",
			ID:      "char-bundle-feature",
			Enabled: true,
			Config:  genYAMLNode(t, fmt.Sprintf("label: %q\n", label)),
		},
	}
	require.NoError(t, config.Validate(cfg))
	return cfg
}

// assertGenerationPlanesMatch verifies that all planes exposed by a GenerationBundle
// and its RuntimeSnapshot match expectedLabel.
func assertGenerationPlanesMatch(t *testing.T, bundle runtimebundle.GenerationRuntime, expectedLabel string) bool {
	t.Helper()
	if !assert.NotNil(t, bundle) {
		return false
	}

	// 1. GenerationBundle-level terminal decision provider if concrete bundle
	if gb, ok := bundle.(*runtimebundle.GenerationBundle); ok {
		td := gb.TerminalDecisionProvider()
		if assert.NotNil(t, td) {
			assert.Equal(t, expectedLabel+"-terminal", td.ID())
		}
	}

	// 2. Extract runtime snapshot from the executor view
	exProvider, ok := bundle.(runtimehost.ExecutorProvider)
	if !assert.True(t, ok, "bundle must implement ExecutorProvider") {
		return false
	}
	exView := exProvider.ExecutorView()
	if !assert.NotNil(t, exView) {
		return false
	}
	executor, ok := exView.(*coreruntime.Executor)
	if !assert.True(t, ok, "executor view must be *coreruntime.Executor") {
		return false
	}
	snap := executor.RuntimeSnapshot
	if !assert.NotNil(t, snap) {
		return false
	}

	sess := snap.SessionOpeners()
	if assert.Len(t, sess, 1) {
		assert.Equal(t, expectedLabel+"-session", sess[0].ID())
	}

	cat := snap.ToolCatalogFilters()
	if assert.Len(t, cat, 1) {
		assert.Equal(t, expectedLabel+"-catalog", cat[0].ID())
	}

	pols := snap.ToolCallPolicies()
	if assert.Len(t, pols, 1) {
		assert.Equal(t, expectedLabel+"-policy", pols[0].ID())
	}

	polsExec := snap.ToolCallPoliciesExecution()
	if assert.Len(t, polsExec, 1) {
		assert.Equal(t, expectedLabel+"-policy", polsExec[0].ID())
	}

	fins := snap.ToolCallFinalizers()
	if assert.Len(t, fins, 1) {
		assert.Equal(t, expectedLabel+"-finalizer", fins[0].ID())
	}

	rx := snap.RequestTransforms()
	if assert.Len(t, rx, 1) {
		assert.Equal(t, expectedLabel+"-reqxform", rx[0].ID())
	}

	pr := snap.PreRequestHandlers()
	if assert.Len(t, pr, 1) {
		assert.Equal(t, expectedLabel+"-prereq", pr[0].ID())
	}

	rh := snap.RouteHintProviders()
	if assert.Len(t, rh, 1) {
		assert.Equal(t, expectedLabel+"-routehint", rh[0].ID())
	}

	cg := snap.CompletionGates()
	if assert.Len(t, cg, 1) {
		assert.Equal(t, expectedLabel+"-gate", cg[0].ID())
	}

	at := snap.AttemptTransforms()
	if assert.Len(t, at, 1) {
		assert.Equal(t, expectedLabel+"-attxform", at[0].ID())
	}

	sg := snap.SecretGuardPlane()
	if assert.Len(t, sg.Guards, 1) {
		assert.Equal(t, expectedLabel+"-sg", sg.Guards[0].ID())
	}

	lt := snap.LocalTurnHandlers()
	if assert.Len(t, lt, 1) {
		assert.Equal(t, expectedLabel+"-localturn", lt[0].ID())
	}

	snapTD := snap.TerminalDecisionProvider()
	if assert.NotNil(t, snapTD) {
		assert.Equal(t, expectedLabel+"-terminal", snapTD.ID())
	}
	return !t.Failed()
}

// TestGenerationPublish_PinnedRequestRetainsFrozenExtensionSurface pins Requirement 1.5:
//   - Proves that while a request holds a lease on Generation 1, publishing candidate Generation 2
//     leaves Generation 1's frozen extension surface completely intact.
//   - The pinned in-flight request continues executing against Generation 1's planes end-to-end,
//     while newly arriving requests immediately observe Generation 2.
func TestGenerationPublish_PinnedRequestRetainsFrozenExtensionSurface(t *testing.T) {
	t.Parallel()

	// 1. Establish ProcessServices
	ps := newProcessForPinnedGeneration(t)

	// 2. Compile Generation 1
	gen1Cfg := pinnedCandidateConfig(t, "gen-1-backend", "serving-gen1", "gen-1-backend:stub-default", "gen1")
	gen1Bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: gen1Cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	require.NoError(t, err)

	mgr := runtimehost.NewManager(8, nil)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.ShutdownDetached(shutdownCtx)
	})

	publishedGen1 := publishTestGeneration(t, mgr, gen1Bundle, "gen1")
	require.Equal(t, int64(1), publishedGen1.ID())
	require.Equal(t, int64(1), mgr.Active().ID())

	// 3. Coordinate concurrent in-flight request and publisher using channel gates (no sleeps)
	req1Started := make(chan struct{})
	req1Stage1Done := make(chan struct{})
	gen2Published := make(chan struct{})
	req1Completed := make(chan struct{})
	req2Completed := make(chan struct{})

	// 4. In-flight Request 1 goroutine (pinned to Gen 1)
	go func() {
		defer func() {
			close(req1Completed)
		}()

		// Acquire lease on Generation 1
		lease1, ok := mgr.Acquire()
		if !assert.True(t, ok, "must acquire lease on active generation 1") {
			return
		}
		defer lease1.Release()

		close(req1Started)

		// Assert Generation 1 planes on lease
		bundle1, ok := lease1.RequestPlane().(runtimebundle.GenerationRuntime)
		if !assert.True(t, ok) {
			return
		}
		assertGenerationPlanesMatch(t, bundle1, "gen1")

		// Verify initial HTTP handler output on Gen 1
		res1 := postResponses(t, lease1.Handler(), "gen-1-backend:stub-default")
		assert.Contains(t, res1, "serving-gen1")

		close(req1Stage1Done)

		// Wait until Generation 2 has completed publication
		<-gen2Published

		// Invariant: In-flight request STILL observes Generation 1's frozen extension surface
		assertGenerationPlanesMatch(t, bundle1, "gen1")

		// In-flight request continues serving through Gen 1 handler seamlessly
		res1Again := postResponses(t, lease1.Handler(), "gen-1-backend:stub-default")
		assert.Contains(t, res1Again, "serving-gen1")
	}()

	// 5. Publisher goroutine: compiles and publishes Generation 2 concurrently
	go func() {
		defer func() {
			close(gen2Published)
		}()

		<-req1Stage1Done

		gen2Cfg := pinnedCandidateConfig(t, "gen-2-backend", "serving-gen2", "gen-2-backend:stub-default", "gen2")
		gen2Bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
			Process:   ps,
			Candidate: gen2Cfg,
			Compose:   stdhttp.ComposeStandardHTTP,
		})
		if !assert.NoError(t, err) {
			return
		}

		publishedGen2 := publishTestGeneration(t, mgr, gen2Bundle, "gen2")
		assert.Equal(t, int64(2), publishedGen2.ID())
		assert.Equal(t, int64(2), mgr.Active().ID())
	}()

	// 6. Request 2 goroutine: arrives AFTER Generation 2 publication and observes Gen 2
	go func() {
		defer func() {
			close(req2Completed)
		}()

		<-gen2Published

		lease2, ok := mgr.Acquire()
		if !assert.True(t, ok, "must acquire lease on newly published generation 2") {
			return
		}
		defer lease2.Release()

		assert.Equal(t, int64(2), lease2.Generation().ID())

		bundle2, ok := lease2.RequestPlane().(runtimebundle.GenerationRuntime)
		if !assert.True(t, ok) {
			return
		}
		assertGenerationPlanesMatch(t, bundle2, "gen2")

		res2 := postResponses(t, lease2.Handler(), "gen-2-backend:stub-default")
		assert.Contains(t, res2, "serving-gen2")
	}()

	// Wait for all participants to finish cleanly
	<-req1Started
	<-req1Completed
	<-req2Completed
}

// TestGenerationPublish_PinnedRequest_RepeatedPublishAcrossInterleavings pins Requirement 1.5:
//   - Proves that repeated candidate publications (Gen 2 .. Gen 6) never mutate or rebind an
//     already-pinned snapshot held via TransferPin/RetainPin.
func TestGenerationPublish_PinnedRequest_RepeatedPublishAcrossInterleavings(t *testing.T) {
	t.Parallel()

	ps := newProcessForPinnedGeneration(t)
	mgr := runtimehost.NewManager(16, nil)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.ShutdownDetached(shutdownCtx)
	})

	// Publish Generation 1
	gen1Cfg := pinnedCandidateConfig(t, "gen-1-backend", "serving-gen1", "gen-1-backend:stub-default", "gen1")
	gen1Bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: gen1Cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	require.NoError(t, err)

	publishTestGeneration(t, mgr, gen1Bundle, "gen1")

	// Pinned in-flight lease converted to async pin
	lease, ok := mgr.Acquire()
	require.True(t, ok)
	pin, ok := lease.TransferPin(runtimehost.PinAsync)
	require.True(t, ok)
	defer pin.Release()

	pinnedBundle, ok := pin.Generation().RequestPlane().(runtimebundle.GenerationRuntime)
	require.True(t, ok)

	// Repeatedly compile and publish successive candidate generations
	const numPublishes = 5
	for i := 2; i <= numPublishes+1; i++ {
		genLabel := fmt.Sprintf("gen%d", i)
		backendID := fmt.Sprintf("gen-%d-backend", i)
		route := fmt.Sprintf("%s:stub-default", backendID)

		candCfg := pinnedCandidateConfig(t, backendID, "serving-"+genLabel, route, genLabel)
		candBundle, compileErr := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
			Process:   ps,
			Candidate: candCfg,
			Compose:   stdhttp.ComposeStandardHTTP,
		})
		require.NoError(t, compileErr)

		published := publishTestGeneration(t, mgr, candBundle, genLabel)
		assert.Equal(t, int64(i), published.ID())
		assert.Equal(t, int64(i), mgr.Active().ID())

		// Verify that pinned Gen 1 bundle remains 100% frozen with zero drift
		assertGenerationPlanesMatch(t, pinnedBundle, "gen1")
	}

	// Final verification of pinned Gen 1 bundle
	assertGenerationPlanesMatch(t, pinnedBundle, "gen1")
}

// TestGenerationPublish_FrozenGenerationLeakCheck verifies Requirement 1.5:
//   - Rigorously validates goroutine and service leak safety under concurrent publish and pinning.
//   - Ensures all managers, generations, and process services close cleanly with zero goroutine leaks.
func TestGenerationPublish_FrozenGenerationLeakCheck(t *testing.T) {
	// Goroutine leak detector ensuring zero background worker leaks across publication cycles
	// IgnoreCurrent scopes the check to goroutines this test starts: idle HTTP
	// client connections left by earlier tests in the package are not leaks of
	// this test, and their runtime function names vary by Go release (Go 1.27
	// moved the HTTP/2 transport to net/http/internal/http2).
	defer goleak.VerifyNone(
		t,
		goleak.IgnoreCurrent(),
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreAnyFunction("net/http.(*http2ClientConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*http2ClientConn).writeLoop"),
		goleak.IgnoreAnyFunction("net/http.setRequestCancel"),
		goleak.IgnoreTopFunction("net/http.setRequestCancel.func4"),
	)

	ps := newProcessForPinnedGeneration(t)
	mgr := runtimehost.NewManager(16, nil)

	// Publish Generation 1
	gen1Cfg := pinnedCandidateConfig(t, "gen-1-backend", "serving-gen1", "gen-1-backend:stub-default", "gen1")
	gen1Bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: gen1Cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	require.NoError(t, err)

	g1 := publishTestGeneration(t, mgr, gen1Bundle, "gen1")

	// Concurrently hold leases and pins across multiple goroutines
	var wg sync.WaitGroup
	const concurrentPins = 8
	acquired := make(chan bool, concurrentPins)
	ready := make(chan struct{})

	for i := range concurrentPins {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			lease, ok := mgr.Acquire()
			acquired <- ok
			if !ok {
				return
			}
			defer lease.Release()

			<-ready

			// Exercise pin conversions
			pin, ok := lease.RetainPin(runtimehost.PinSSE)
			if ok {
				defer pin.Release()
				bundle, ok := pin.Generation().RequestPlane().(runtimebundle.GenerationRuntime)
				if ok {
					assertGenerationPlanesMatch(t, bundle, "gen1")
				}
			}
		}(i)
	}
	for range concurrentPins {
		require.True(t, <-acquired, "generation 1 must remain acquirable until every worker holds a lease")
	}

	// Publish Generation 2 concurrently while leases are being acquired and pinned
	gen2Cfg := pinnedCandidateConfig(t, "gen-2-backend", "serving-gen2", "gen-2-backend:stub-default", "gen2")
	gen2Bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: gen2Cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	require.NoError(t, err)

	close(ready)
	g2 := publishTestGeneration(t, mgr, gen2Bundle, "gen2")
	require.Equal(t, int64(2), g2.ID())

	wg.Wait()

	// Shutdown manager and sweep all closed generations
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, mgr.ShutdownDetached(shutdownCtx))
	<-g1.Drained()
	<-g2.Drained()

	// Verify manager reports no open generations
	mgr.SweepClosed()
	assert.False(t, mgr.HasOpenGenerations(), "manager must report no open generations after shutdown and sweep")

	// Close ProcessServices
	require.NoError(t, ps.Close())
	assert.True(t, ps.Closed(), "process services must report closed")
}
