package runtimebundle

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	coreworkspace "github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Stubs for Session Openers and Workspace Resolvers ---

type stubSessionOpener struct {
	id     string
	labels map[string]string
	err    error
	events *[]string
	mu     *sync.Mutex
}

func (o stubSessionOpener) ID() string { return o.id }

func (o stubSessionOpener) Open(ctx context.Context, in session.OpenInput) (session.OpenResult, error) {
	if o.mu != nil && o.events != nil {
		o.mu.Lock()
		*o.events = append(*o.events, o.id)
		o.mu.Unlock()
	}
	if o.err != nil {
		return session.OpenResult{}, o.err
	}
	return session.OpenResult{SessionLabelUpserts: o.labels}, nil
}

type stubWorkspaceResolver struct {
	id     string
	view   workspace.WorkspaceView
	err    error
	events *[]string
	mu     *sync.Mutex
}

func (r stubWorkspaceResolver) Resolve(ctx context.Context) (workspace.WorkspaceView, error) {
	if r.mu != nil && r.events != nil {
		r.mu.Lock()
		*r.events = append(*r.events, r.id)
		r.mu.Unlock()
	}
	if r.err != nil {
		return workspace.WorkspaceView{}, r.err
	}
	return r.view, nil
}

// --- Parity and Snapshot Projection Tests ---

func TestSessionAndWorkspaceProjection_ParityWithFrozenAndRegistrationOrder(t *testing.T) {
	t.Parallel()

	var events []string
	var mu sync.Mutex

	b1 := testkit.FeatureBundle(t, "b1", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "b1", []session.Opener{
			stubSessionOpener{id: "so-1", labels: map[string]string{"k1": "v1"}, events: &events, mu: &mu},
			stubSessionOpener{id: "so-2", labels: map[string]string{"k2": "v2"}, events: &events, mu: &mu},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "b1", []workspace.Resolver{
			stubWorkspaceResolver{id: "wr-1", view: workspace.WorkspaceView{ID: "ws-1", ProjectRoot: "/project/1"}, events: &events, mu: &mu},
		})
	}, nil)

	b2 := testkit.FeatureBundle(t, "b2", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "b2", []session.Opener{
			stubSessionOpener{id: "so-3", labels: map[string]string{"k2": "v2-override", "k3": "v3"}, events: &events, mu: &mu},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "b2", []workspace.Resolver{
			stubWorkspaceResolver{id: "wr-2", view: workspace.WorkspaceView{ID: "ws-2", ProjectRoot: "/project/2", Markers: []string{"git"}}, events: &events, mu: &mu},
		})
	}, nil)

	gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)

	// 1. Verify Frozen plane contents preserve registration order
	frozenSO := aipfeature.Get(gen.Frozen, aipfeature.PlaneSessionOpeners)
	require.Len(t, frozenSO, 3)
	assert.Equal(t, "so-1", frozenSO[0].ID())
	assert.Equal(t, "so-2", frozenSO[1].ID())
	assert.Equal(t, "so-3", frozenSO[2].ID())

	frozenWR := aipfeature.Get(gen.Frozen, aipfeature.PlaneWorkspaceResolvers)
	require.Len(t, frozenWR, 2)

	// 2. Build runtime snapshot from FeaturePlanes directly
	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{
		FeaturePlanes: gen.Frozen,
	}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

	// 3. Snapshot SessionOpeners returns defensive copies preserving registration order
	snapSO := snap.SessionOpeners()
	require.Len(t, snapSO, 3)
	assert.Equal(t, "so-1", snapSO[0].ID())
	assert.Equal(t, "so-2", snapSO[1].ID())
	assert.Equal(t, "so-3", snapSO[2].ID())

	// 4. SessionOpen stage runs openers in registration order, merging label upserts
	openRes := extensions.RunSessionOpenStage(context.Background(), nil, nil, snapSO, session.OpenInput{})
	assert.Equal(t, "v1", openRes.SessionLabelUpserts["k1"])
	assert.Equal(t, "v2-override", openRes.SessionLabelUpserts["k2"])
	assert.Equal(t, "v3", openRes.SessionLabelUpserts["k3"])

	// 5. Workspace resolver resolves in registration order (last writer wins on ID/ProjectRoot)
	wsView, wsErr := snap.Workspace().Resolve(context.Background())
	require.NoError(t, wsErr)
	assert.Equal(t, "ws-2", wsView.ID)
	assert.Equal(t, "/project/2", wsView.ProjectRoot)
	assert.Contains(t, wsView.Markers, "git")
}

func TestSessionAndWorkspaceProjection_NilVsEmptySemantics(t *testing.T) {
	t.Parallel()

	t.Run("zero_value_bundles_produce_nil_planes", func(t *testing.T) {
		t.Parallel()
		gen, err := featurebundle.MergeBundlesGenerated(aipfeature.FeatureBundle{}, aipfeature.FeatureBundle{})
		require.NoError(t, err)

		assert.Nil(t, aipfeature.Get(gen.Frozen, aipfeature.PlaneSessionOpeners))
		assert.Nil(t, aipfeature.Get(gen.Frozen, aipfeature.PlaneWorkspaceResolvers))

		bus := hooks.New(hooks.Config{})
		opts := &BuildOptions{FeaturePlanes: gen.Frozen}
		snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

		assert.Nil(t, snap.SessionOpeners())

		// Workspace resolver defaults to DisabledResolver
		wsView, err := snap.Workspace().Resolve(context.Background())
		require.Error(t, err)
		assert.True(t, errors.Is(err, workspace.ErrResolverNotConfigured))
		assert.Equal(t, workspace.WorkspaceView{}, wsView)
	})

	t.Run("explicit_empty_slices_preserve_non_nil_empty", func(t *testing.T) {
		t.Parallel()
		b := testkit.FeatureBundle(t, "empty", func(cs *aipfeature.ContributionSet) error {
			if err := aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "empty", []session.Opener{}); err != nil {
				return err
			}
			return aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "empty", []workspace.Resolver{})
		}, nil)
		gen, err := featurebundle.MergeBundlesGenerated(b)
		require.NoError(t, err)

		gotSO := aipfeature.Get(gen.Frozen, aipfeature.PlaneSessionOpeners)
		assert.NotNil(t, gotSO, "PlaneSessionOpeners must preserve explicit non-nil empty slice")
		assert.Empty(t, gotSO)

		gotWR := aipfeature.Get(gen.Frozen, aipfeature.PlaneWorkspaceResolvers)
		assert.NotNil(t, gotWR, "PlaneWorkspaceResolvers must preserve explicit non-nil empty slice")
		assert.Empty(t, gotWR)

		bus := hooks.New(hooks.Config{})
		opts := &BuildOptions{FeaturePlanes: gen.Frozen}
		snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

		snapSO := snap.SessionOpeners()
		assert.Nil(t, snapSO, "Snapshot SessionOpeners must normalize explicit empty slice to nil")
	})
}

func TestSessionAndWorkspaceProjection_BackingArrayIsolation(t *testing.T) {
	t.Parallel()

	origSO := []session.Opener{stubSessionOpener{id: "so-orig"}}
	origWR := []workspace.Resolver{stubWorkspaceResolver{id: "wr-orig"}}

	b := testkit.FeatureBundle(t, "orig", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "orig", origSO); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "orig", origWR)
	}, nil)

	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)

	// Mutate original slice backing array
	origSO[0] = stubSessionOpener{id: "so-mutated"}
	origWR[0] = stubWorkspaceResolver{id: "wr-mutated"}

	// Frozen plane must retain original value
	assert.Equal(t, "so-orig", aipfeature.Get(gen.Frozen, aipfeature.PlaneSessionOpeners)[0].ID())

	// Snapshot defensive copy isolation
	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{FeaturePlanes: gen.Frozen}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

	snapSO1 := snap.SessionOpeners()
	snapSO1[0] = stubSessionOpener{id: "so-snap-mutated"}
	snapSO2 := snap.SessionOpeners()
	assert.Equal(t, "so-orig", snapSO2[0].ID())
}

// --- End-to-End CompileGeneration and Execution Tests ---

func TestCompileGeneration_SessionAndWorkspaceExecution(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	var executedOpeners []string
	var executedResolvers []string
	var mu sync.Mutex

	// Register feature 1: SessionOpener
	require.NoError(t, reg.RegisterFeature("test-session-1", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "test-session-1", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "test-session-1", []session.Opener{
				stubSessionOpener{
					id:     "so-feature-1",
					labels: map[string]string{"env": "test"},
					events: &executedOpeners,
					mu:     &mu,
				},
			})
		}, nil), nil
	}))

	// Register feature 2: WorkspaceResolver
	require.NoError(t, reg.RegisterFeature("test-workspace-1", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "test-workspace-1", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "test-workspace-1", []workspace.Resolver{
				stubWorkspaceResolver{
					id:     "wr-feature-1",
					view:   workspace.WorkspaceView{ID: "ws-feature-1", ProjectRoot: "/app"},
					events: &executedResolvers,
					mu:     &mu,
				},
			})
		}, nil), nil
	}))

	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand := obsTestCandidateConfig(t, "test-session-1", "test-workspace-1")

	genRuntime, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok, "genRuntime must be *GenerationBundle")
	snap := bundle.execution.executor.RuntimeSnapshot
	require.NotNil(t, snap)

	// Verify SessionOpeners
	openers := snap.SessionOpeners()
	require.Len(t, openers, 1)
	assert.Equal(t, "so-feature-1", openers[0].ID())

	// Run session open stage
	openRes := extensions.RunSessionOpenStage(context.Background(), nil, nil, openers, session.OpenInput{})
	assert.Equal(t, "test", openRes.SessionLabelUpserts["env"])

	mu.Lock()
	assert.Contains(t, executedOpeners, "so-feature-1")
	mu.Unlock()

	// Verify WorkspaceResolver
	wsView, err := snap.Workspace().Resolve(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ws-feature-1", wsView.ID)
	assert.Equal(t, "/app", wsView.ProjectRoot)

	mu.Lock()
	assert.Contains(t, executedResolvers, "wr-feature-1")
	mu.Unlock()
}

func TestCompileGeneration_CandidateFeaturePlanesOverlaySessionAndWorkspace(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)
	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	var executedOpeners []string
	var executedResolvers []string
	var mu sync.Mutex

	candBundle := testkit.FeatureBundle(t, "cand", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "cand", []session.Opener{
			stubSessionOpener{
				id:     "cand-so-1",
				labels: map[string]string{"cand-key": "cand-val"},
				events: &executedOpeners,
				mu:     &mu,
			},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "cand", []workspace.Resolver{
			stubWorkspaceResolver{
				id:     "cand-wr-1",
				view:   workspace.WorkspaceView{ID: "cand-ws-1", ProjectRoot: "/cand-root"},
				events: &executedResolvers,
				mu:     &mu,
			},
		})
	}, nil)
	candGen, err := featurebundle.MergeBundlesGenerated(candBundle)
	require.NoError(t, err)

	cand := obsTestCandidateConfig(t)

	genRuntime, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: candGen.Frozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok)
	snap := bundle.execution.executor.RuntimeSnapshot
	require.NotNil(t, snap)

	// Verify candidate SessionOpeners overlaid
	openers := snap.SessionOpeners()
	require.Len(t, openers, 1)
	assert.Equal(t, "cand-so-1", openers[0].ID())

	openRes := extensions.RunSessionOpenStage(context.Background(), nil, nil, openers, session.OpenInput{})
	assert.Equal(t, "cand-val", openRes.SessionLabelUpserts["cand-key"])

	// Verify candidate WorkspaceResolvers overlaid
	wsView, err := snap.Workspace().Resolve(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "cand-ws-1", wsView.ID)
	assert.Equal(t, "/cand-root", wsView.ProjectRoot)
}

func TestCompileGeneration_CandidateFeaturePlanes_SessionAndWorkspaceCandidateEmptyPreservation(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)
	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	candBundle := testkit.FeatureBundle(t, "empty", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "empty", []session.Opener{}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "empty", []workspace.Resolver{})
	}, nil)
	candGen, err := featurebundle.MergeBundlesGenerated(candBundle)
	require.NoError(t, err)

	cand := obsTestCandidateConfig(t)

	genRuntime, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: candGen.Frozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok)
	snap := bundle.execution.executor.RuntimeSnapshot
	require.NotNil(t, snap)

	snapSO := snap.SessionOpeners()
	assert.Nil(t, snapSO, "candidate empty SessionOpeners must normalize to nil in snapshot")
}

func TestCompileGeneration_CandidateFeaturePlanes_UnrelatedPlanesIgnoredWithSessionAndWorkspace(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)
	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	candBundle := testkit.FeatureBundle(t, "cand", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "cand", []session.Opener{
			stubSessionOpener{id: "cand-so-valid"},
		}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "cand", []workspace.Resolver{
			stubWorkspaceResolver{id: "cand-wr-valid", view: workspace.WorkspaceView{ID: "ws-valid"}},
		}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "cand", []traffic.Observer{
			stubTrafficObs{id: "unrelated-traffic-obs"},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "cand", []usage.Observer{
			stubUsageObs{id: "unrelated-usage-obs"},
		})
	}, nil)
	candGen, err := featurebundle.MergeBundlesGenerated(candBundle)
	require.NoError(t, err)

	cand := obsTestCandidateConfig(t)

	genRuntime, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: candGen.Frozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok)
	snap := bundle.execution.executor.RuntimeSnapshot
	require.NotNil(t, snap)

	openers := snap.SessionOpeners()
	require.Len(t, openers, 1)
	assert.Equal(t, "cand-so-valid", openers[0].ID())

	wsView, err := snap.Workspace().Resolve(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ws-valid", wsView.ID)

	// Unrelated planes in candidate must NOT be present on snapshot
	assert.Equal(t, traffic.NoopObserver{}, snap.TrafficObserver())
	assert.Equal(t, usage.NoopObserver{}, snap.UsageObserver())
}

type typedNilSessionOpener struct {
	id string
}

func (o *typedNilSessionOpener) ID() string {
	if o == nil {
		return "so-nil"
	}
	return o.id
}

func (o *typedNilSessionOpener) Open(ctx context.Context, in session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{SessionLabelUpserts: map[string]string{"typed-nil": "ok"}}, nil
}

type typedNilWorkspaceResolver struct {
	id string
}

func (r *typedNilWorkspaceResolver) Resolve(ctx context.Context) (workspace.WorkspaceView, error) {
	return workspace.WorkspaceView{ID: "ws-typed-nil"}, nil
}

func TestSessionAndWorkspace_LiteralNilAndBoxedTypedNilLegacyCharacterization(t *testing.T) {
	t.Parallel()

	var typedNilSO *typedNilSessionOpener
	var literalNilSO session.Opener
	validSO := stubSessionOpener{id: "so-valid"}

	var typedNilWR *typedNilWorkspaceResolver
	var literalNilWR workspace.Resolver
	validWR := stubWorkspaceResolver{id: "wr-valid", view: workspace.WorkspaceView{ID: "ws-valid"}}

	// 1. Bundle merging preserves literal nil and boxed typed-nil without error
	b := testkit.FeatureBundle(t, "test", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "test", []session.Opener{literalNilSO, typedNilSO, validSO}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "test", []workspace.Resolver{literalNilWR, typedNilWR, validWR})
	}, nil)

	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err, "contribute/merge must not reject literal nil or boxed typed nil for session openers and workspace resolvers")

	gotSO := aipfeature.Get(gen.Frozen, aipfeature.PlaneSessionOpeners)
	require.Len(t, gotSO, 3)
	assert.Nil(t, gotSO[0])
	assert.True(t, gotSO[1] != nil, "boxed typed-nil interface is non-nil interface holding nil concrete pointer")
	assert.Equal(t, "so-nil", gotSO[1].ID())
	assert.Equal(t, "so-valid", gotSO[2].ID())

	gotWR := aipfeature.Get(gen.Frozen, aipfeature.PlaneWorkspaceResolvers)
	require.Len(t, gotWR, 3)
	assert.Nil(t, gotWR[0])
	assert.True(t, gotWR[1] != nil, "boxed typed-nil interface is non-nil interface holding nil concrete pointer")

	// 2. Snapshot accessors return defensive copies with all 3 elements
	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{FeaturePlanes: gen.Frozen}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

	snapSO := snap.SessionOpeners()
	require.Len(t, snapSO, 3)
	assert.Nil(t, snapSO[0])

	// 3. Diagnostics materializers safely skip literal nil entries
	diagSO := aipfeature.PlaneSessionOpeners.Diagnostics.Materialize(gotSO)
	require.Len(t, diagSO, 2) // skips literal nil, retains typed nil and valid
	assert.Equal(t, "opener:so-nil", diagSO[0].Label)
	assert.Equal(t, "opener:so-valid", diagSO[1].Label)

	diagWR := aipfeature.PlaneWorkspaceResolvers.Diagnostics.Materialize(gotWR)
	require.Len(t, diagWR, 2) // skips literal nil (index 0), retains index 1 and 2
	assert.Equal(t, "workspace_resolver:1", diagWR[0].Label)
	assert.Equal(t, "workspace_resolver:2", diagWR[1].Label)

	// 4. SessionOpen stage skips literal nil without panic
	openRes := extensions.RunSessionOpenStage(context.Background(), nil, nil, snapSO, session.OpenInput{})
	assert.Equal(t, "ok", openRes.SessionLabelUpserts["typed-nil"])

	// 5. Workspace resolver chain skips literal nil without panic
	wsView, wsErr := snap.Workspace().Resolve(context.Background())
	require.NoError(t, wsErr)
	assert.Equal(t, "ws-valid", wsView.ID)
}

func TestSessionAndWorkspace_ErrorAndFailOpenBehavior(t *testing.T) {
	t.Parallel()

	// 1. SessionOpener returning error continues to next opener (fail-open)
	openers := []session.Opener{
		stubSessionOpener{id: "so-err", err: errors.New("opener failed")},
		stubSessionOpener{id: "so-ok", labels: map[string]string{"k": "v"}},
	}
	openRes := extensions.RunSessionOpenStage(context.Background(), nil, nil, openers, session.OpenInput{})
	assert.Equal(t, "v", openRes.SessionLabelUpserts["k"])

	// 2. WorkspaceResolver error in fail-open mode (default) ignores error and continues
	resChain := coreworkspace.NewResolverChain([]workspace.Resolver{
		stubWorkspaceResolver{id: "wr-err", err: errors.New("resolver failed")},
		stubWorkspaceResolver{id: "wr-ok", view: workspace.WorkspaceView{ID: "ws-ok"}},
	})
	view, err := resChain.Resolve(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ws-ok", view.ID)

	// 3. WorkspaceResolver error in strict fail-closed mode stops and returns error
	strictChain := coreworkspace.NewStrictChain([]workspace.Resolver{
		stubWorkspaceResolver{id: "wr-err", err: errors.New("strict resolver failed")},
		stubWorkspaceResolver{id: "wr-ok", view: workspace.WorkspaceView{ID: "ws-ok"}},
	})
	_, strictErr := strictChain.Resolve(context.Background())
	require.Error(t, strictErr)
	assert.Equal(t, "strict resolver failed", strictErr.Error())
}

func TestSessionAndWorkspace_DiagnosticsCoalescedStageByteEquivalence(t *testing.T) {
	t.Parallel()

	b := testkit.FeatureBundle(t, "diag", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "diag", []session.Opener{
			stubSessionOpener{id: "user-session"},
			stubSessionOpener{id: "tenant-session"},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "diag", []workspace.Resolver{
			stubWorkspaceResolver{id: "local-ws"},
			stubWorkspaceResolver{id: "remote-ws"},
		})
	}, nil)

	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)

	so := aipfeature.Get(gen.Frozen, aipfeature.PlaneSessionOpeners)
	wr := aipfeature.Get(gen.Frozen, aipfeature.PlaneWorkspaceResolvers)

	diagSO := aipfeature.PlaneSessionOpeners.Diagnostics.Materialize(so)
	diagWR := aipfeature.PlaneWorkspaceResolvers.Diagnostics.Materialize(wr)

	// Combine into coalesced session_open stage: openers first, workspace second
	var handlerIDs []string
	for _, d := range diagSO {
		handlerIDs = append(handlerIDs, d.Label)
	}
	for _, d := range diagWR {
		handlerIDs = append(handlerIDs, d.Label)
	}

	type stageOccupancyJSON struct {
		StageID    string   `json:"stage_id"`
		Count      int      `json:"count"`
		HandlerIDs []string `json:"handler_ids"`
	}

	occupancy := stageOccupancyJSON{
		StageID:    "session_open",
		Count:      len(handlerIDs),
		HandlerIDs: handlerIDs,
	}

	gotBytes, err := json.Marshal(occupancy)
	require.NoError(t, err)

	const expectedJSON = `{"stage_id":"session_open","count":4,"handler_ids":["opener:user-session","opener:tenant-session","workspace_resolver:0","workspace_resolver:1"]}`
	assert.Equal(t, expectedJSON, string(gotBytes), "exact serialized JSON bytes for coalesced session_open stage must match")
}
