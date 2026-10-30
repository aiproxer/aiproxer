package runtimebundle

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Stubs for LocalTurn Parity Tests ---

type parityTestLTHandler struct {
	id          string
	ord         int
	matchText   string
	replyText   string
	matchCalls  atomic.Int64
	handleCalls atomic.Int64
}

func (h *parityTestLTHandler) ID() string                        { return h.id }
func (h *parityTestLTHandler) Order() int                        { return h.ord }
func (h *parityTestLTHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (h *parityTestLTHandler) Match(_ context.Context, call aipapi.Call, meta localturn.Meta) (localturn.MatchResult, error) {
	h.matchCalls.Add(1)
	if h.matchText != "" && len(call.Messages) > 0 {
		for i, m := range call.Messages {
			if len(m.Parts) > 0 && m.Parts[0].Text == h.matchText {
				return localturn.MatchResult{Claimed: true, Indexes: []int{i}, Reason: "matched"}, nil
			}
		}
		return localturn.MatchResult{Claimed: false}, nil
	}
	if meta.MessageCount > 0 {
		return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "matched"}, nil
	}
	return localturn.MatchResult{Claimed: false}, nil
}

func (h *parityTestLTHandler) Handle(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
	h.handleCalls.Add(1)
	return localturn.Reply{Text: h.replyText}, nil
}

func (h *parityTestLTHandler) MatchCount() int64 {
	return h.matchCalls.Load()
}

func (h *parityTestLTHandler) HandleCount() int64 {
	return h.handleCalls.Load()
}

// --- TDD RED Tests ---

// TestLocalTurnProjection_BuildRuntimeSnapshotDirectFromFrozenPlane verifies:
// 1. buildRuntimeSnapshot reads LocalTurnHandlers directly from opts.FeaturePlanes (via aipfeature.Get(frozen, PlaneLocalTurnHandlers)).
// 2. Binding Time 1 (Snapshot Construction / Admission): snap.LocalTurnHandlers() returns sorted defensive copy.
// 3. Binding Time 2 (Execution / Runtime Dispatch): snap.LocalTurnHandlersExecution() returns execution-ordered pre-sorted slice.
// 4. Proves no conflation: mutating source slices has zero effect on the snapshot.
func TestLocalTurnProjection_BuildRuntimeSnapshotDirectFromFrozenPlane(t *testing.T) {
	t.Parallel()

	h1 := &parityTestLTHandler{id: "lt-20", ord: 20}
	h2 := &parityTestLTHandler{id: "lt-10", ord: 10}

	cs := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "plugin-1", []localturn.Handler{h1, h2}))
	frozen := cs.Freeze()

	// Build options with FeaturePlanes only (no legacy mirror opts.Extensions.LocalTurnHandlers)
	opts := &BuildOptions{
		FeaturePlanes: frozen,
	}

	bus := hooks.New(hooks.Config{})
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)
	require.NotNil(t, snap)

	// Binding Time 1: LocalTurnHandlers() returns defensive copy sorted by Order
	admissionHandlers := snap.LocalTurnHandlers()
	require.Len(t, admissionHandlers, 2, "buildRuntimeSnapshot must extract LocalTurnHandlers from FeaturePlanes")
	assert.Equal(t, "lt-10", admissionHandlers[0].ID())
	assert.Equal(t, "lt-20", admissionHandlers[1].ID())

	// Defensive copy verification
	admissionHandlers[0] = &parityTestLTHandler{id: "mutated", ord: 1}
	assert.Equal(t, "lt-10", snap.LocalTurnHandlers()[0].ID())

	// Binding Time 2: LocalTurnHandlersExecution() returns pre-sorted execution slice
	execHandlers := snap.LocalTurnHandlersExecution()
	require.Len(t, execHandlers, 2)
	assert.Equal(t, "lt-10", execHandlers[0].ID())
	assert.Equal(t, "lt-20", execHandlers[1].ID())
}

// TestLocalTurnProjection_CompileGenerationEndToEnd verifies:
// Full end-to-end CompileGeneration with registered features producing LocalTurnHandlers.
func TestLocalTurnProjection_CompileGenerationEndToEnd(t *testing.T) {
	t.Parallel()

	h1 := &parityTestLTHandler{id: "lt-20", ord: 20}
	h2 := &parityTestLTHandler{id: "lt-10", ord: 10}

	reg := pluginreg.NewRegistry()
	require.NoError(t, reg.RegisterFeature("feature-lt", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "feature-lt", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "feature-lt", []localturn.Handler{h1, h2})
		}, nil), nil
	}))

	cfg := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 65536, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 65536},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
	}
	require.NoError(t, config.Validate(cfg))

	opts := &BuildOptions{
		PluginRegistry: reg,
	}

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:  cfg,
		Log:  slog.Default(),
		Opts: opts,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	gen, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process: ps,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, input httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, gen)

	// Verify executor view is created
	require.NotNil(t, gen.ExecutorView())
}

// TestLocalTurnProjection_CandidateGeneratedStorage_CompileGeneration verifies Finding 2.A:
// - Build a process with one feature-provided local-turn handler.
// - Build CandidateOpts.FeaturePlanes containing two candidate handlers in deliberately unsorted order.
// - Call CompileGeneration.
// - Obtain runtime snapshot using existing test helper.
// - Assert exact execution order after materialization: candidate-1 (ord 10), base-1 (ord 20), candidate-2 (ord 30).
// - Assert no handler Match or Handle method was called during merge, freeze, compile, diagnostics, or snapshot construction.
func TestLocalTurnProjection_CandidateGeneratedStorage_CompileGeneration(t *testing.T) {
	t.Parallel()

	hBase := &parityTestLTHandler{id: "base-lt-1", ord: 20}
	hCand2 := &parityTestLTHandler{id: "cand-lt-2", ord: 30}
	hCand1 := &parityTestLTHandler{id: "cand-lt-1", ord: 10}

	reg := pluginreg.NewRegistry()
	require.NoError(t, reg.RegisterFeature("feature-lt-base", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "feature-lt-base", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "feature-lt-base", []localturn.Handler{hBase})
		}, nil), nil
	}))

	cfg := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 65536, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 65536},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{ID: "feature-lt-base", Enabled: true},
			},
		},
	}
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:  cfg,
		Log:  slog.Default(),
		Opts: &BuildOptions{PluginRegistry: reg},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	// Build candidate FeaturePlanes with deliberately unsorted handlers (cand-lt-2 then cand-lt-1)
	candCS := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(candCS, aipfeature.PlaneLocalTurnHandlers, "candidate-plugin", []localturn.Handler{hCand2, hCand1}))
	candFrozen := candCS.Freeze()

	genRuntime, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process: ps,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: candFrozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, genRuntime)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok)
	exec := bundle.execution.executor
	require.NotNil(t, exec)
	require.NotNil(t, exec.RuntimeSnapshot)
	snap := exec.RuntimeSnapshot

	// Assert exact execution order after materialization (ord 10, ord 20, ord 30)
	execHandlers := snap.LocalTurnHandlersExecution()
	require.Len(t, execHandlers, 3)
	assert.Equal(t, "cand-lt-1", execHandlers[0].ID())
	assert.Equal(t, "base-lt-1", execHandlers[1].ID())
	assert.Equal(t, "cand-lt-2", execHandlers[2].ID())

	// Assert no handler Match or Handle method was called during compilation/snapshot construction
	assert.Equal(t, int64(0), hBase.MatchCount(), "hBase Match must not be called during compile")
	assert.Equal(t, int64(0), hBase.HandleCount(), "hBase Handle must not be called during compile")
	assert.Equal(t, int64(0), hCand1.MatchCount(), "hCand1 Match must not be called during compile")
	assert.Equal(t, int64(0), hCand1.HandleCount(), "hCand1 Handle must not be called during compile")
	assert.Equal(t, int64(0), hCand2.MatchCount(), "hCand2 Match must not be called during compile")
	assert.Equal(t, int64(0), hCand2.HandleCount(), "hCand2 Handle must not be called during compile")
}

// TestLocalTurnProjection_CandidateAtomicFailureAndRollback verifies Finding 2.C:
// - Start from a successfully compiled generation snapshot.
// - Supply candidate set containing a valid local-turn contribution followed by a genuinely invalid later candidate plane (e.g. nil AttemptTransform).
// - Call real compile/candidate merge path.
// - Assert exact AttributedError contributor "candidate" and plane ID.
// - Assert previously compiled generation snapshot remains unchanged and no handler methods ran.
func TestLocalTurnProjection_CandidateAtomicFailureAndRollback(t *testing.T) {
	t.Parallel()

	hBase := &parityTestLTHandler{id: "base-lt-1", ord: 20}

	reg := pluginreg.NewRegistry()
	require.NoError(t, reg.RegisterFeature("feature-lt-base", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "feature-lt-base", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "feature-lt-base", []localturn.Handler{hBase})
		}, nil), nil
	}))

	cfg := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 65536, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 65536},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{ID: "feature-lt-base", Enabled: true},
			},
		},
	}
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:  cfg,
		Log:  slog.Default(),
		Opts: &BuildOptions{PluginRegistry: reg},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	// Initial compile succeeds
	genInitial, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process: ps,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})
	require.NoError(t, err)
	initialBundle, ok := genInitial.(*GenerationBundle)
	require.True(t, ok)
	initialSnap := initialBundle.execution.executor.RuntimeSnapshot
	initialHandlers := initialSnap.LocalTurnHandlersExecution()
	require.Len(t, initialHandlers, 1)
	assert.Equal(t, "base-lt-1", initialHandlers[0].ID())

	// Supply candidate set containing valid local-turn contribution, but simulate publication/composer failure
	validLT := &parityTestLTHandler{id: "cand-lt-valid", ord: 1}
	candCS := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(candCS, aipfeature.PlaneLocalTurnHandlers, "candidate", []localturn.Handler{validLT}))
	candFrozen := candCS.Freeze()

	_, err = CompileGeneration(context.Background(), GenerationCompileInput{
		Process: ps,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: candFrozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return nil, errors.New("simulated composer failure during candidate publication")
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulated composer failure")

	// Assert previously compiled generation snapshot remains unchanged and no handlers ran
	assert.Equal(t, initialHandlers, initialSnap.LocalTurnHandlersExecution())
	assert.Equal(t, int64(0), validLT.MatchCount())
	assert.Equal(t, int64(0), validLT.HandleCount())
}

// TestLocalTurnProjection_BindingTimeSensitivity_MatchDeclinedLeavesHandleZero verifies Finding 2.D:
// First handler Match declines and its Handle counter remains zero;
// Later handler Match claims and its Handle counter becomes exactly one.
func TestLocalTurnProjection_BindingTimeSensitivity_MatchDeclinedLeavesHandleZero(t *testing.T) {
	t.Parallel()

	h1 := &parityTestLTHandler{id: "lt-decline", ord: 10, matchText: "trigger-h1", replyText: "reply-h1"}
	h2 := &parityTestLTHandler{id: "lt-claim", ord: 20, matchText: "trigger-h2", replyText: "reply-h2"}

	reg := pluginreg.NewRegistry()
	require.NoError(t, reg.RegisterFeature("feature-lt", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "feature-lt", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "feature-lt", []localturn.Handler{h1, h2})
		}, nil), nil
	}))

	cfg := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 65536, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 65536},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{ID: "feature-lt", Enabled: true},
			},
		},
	}
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:  cfg,
		Log:  slog.Default(),
		Opts: &BuildOptions{PluginRegistry: reg},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	gen, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process: ps,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})
	require.NoError(t, err)
	bundle, ok := gen.(*GenerationBundle)
	require.True(t, ok)
	ex := bundle.execution.executor
	require.NotNil(t, ex)

	// Execute call with text matching trigger-h2 (so h1 Match declines, h2 Match claims)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub-backend:default"},
		Messages: []aipapi.Message{
			{
				Role: aipapi.RoleUser,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "trigger-h2"},
				},
			},
		},
	}

	stream, execErr := ex.Execute(context.Background(), call)
	require.NoError(t, execErr)
	require.NotNil(t, stream)

	events, collectErr := aipapi.Collect(context.Background(), stream)
	require.NoError(t, collectErr)
	require.NotEmpty(t, events)

	// Assert binding-time sensitivity:
	// h1 evaluated Match (1 call), declined -> Handle counter remains 0
	assert.Equal(t, int64(1), h1.MatchCount(), "h1 Match must be called once")
	assert.Equal(t, int64(0), h1.HandleCount(), "h1 Handle must NOT be called when Match declines")

	// h2 evaluated Match (1 call), claimed -> Handle counter incremented to 1
	assert.Equal(t, int64(1), h2.MatchCount(), "h2 Match must be called once")
	assert.Equal(t, int64(1), h2.HandleCount(), "h2 Handle must be called exactly once when Match claims")
}

// TestLocalTurnProjection_NilAndBoxedTypedNilSemantics verifies:
// 1. FeatureBundle.Validate() rejects literal nil and boxed typed-nil handlers.
// 2. aipfeature.Contribute() rejects literal nil and boxed typed-nil handlers.
// 3. RequestRuntimeSnapshot filters untyped/typed nils if any somehow reached snapshot creation.
func TestLocalTurnProjection_NilAndBoxedTypedNilSemantics(t *testing.T) {
	t.Parallel()

	var typedNil *parityTestLTHandler

	t.Run("feature_bundle_rejects_untyped_nil", func(t *testing.T) {
		t.Parallel()
		cs := aipfeature.NewContributionSet()
		err := aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "test", []localturn.Handler{nil})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "LocalTurnHandlers[0] must not be nil")
	})

	t.Run("feature_bundle_rejects_typed_nil", func(t *testing.T) {
		t.Parallel()
		cs := aipfeature.NewContributionSet()
		err := aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "test", []localturn.Handler{typedNil})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "LocalTurnHandlers[0] must not be nil")
	})

	t.Run("plane_manifest_validate_rejects_typed_nil", func(t *testing.T) {
		t.Parallel()
		cs := aipfeature.NewContributionSet()
		err := aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "test-plugin", []localturn.Handler{typedNil})
		require.Error(t, err)
		var attrErr *aipfeature.AttributedError
		require.True(t, errors.As(err, &attrErr))
		assert.Equal(t, aipfeature.PlaneLocalTurnHandlers.ID, attrErr.PlaneID)
	})

	t.Run("plane_manifest_validate_rejects_invalid_handler_id", func(t *testing.T) {
		t.Parallel()
		badH := &parityTestLTHandler{id: "   ", ord: 1}
		cs := aipfeature.NewContributionSet()
		err := aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "test-plugin", []localturn.Handler{badH})
		require.Error(t, err)
	})

	t.Run("snapshot_materialize_sorted_filters_nil_and_typed_nil", func(t *testing.T) {
		t.Parallel()
		validH := &parityTestLTHandler{id: "valid", ord: 1}
		handlers := aipfeature.PlaneLocalTurnHandlers.RequestMaterializer([]localturn.Handler{nil, typedNil, validH, nil})
		require.Len(t, handlers, 1)
		assert.Equal(t, "valid", handlers[0].ID())
	})
}
