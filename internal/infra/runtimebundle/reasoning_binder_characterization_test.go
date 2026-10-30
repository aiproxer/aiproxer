package runtimebundle

import (
	"context"
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/infra/auxiliary"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkauxiliary "github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/reasoninghost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func validateReasoningPreservationCompressionGeneration(ps *ProcessServices, regs []aipsdk.Registration, client sdkauxiliary.BackgroundClient, poller sdkauxiliary.BackgroundPoller) error {
	fh, err := reasoningTestFeatureHost(ps)
	if err != nil {
		return err
	}
	var hostRegs []featurehost.Registration
	if ps != nil && ps.opts != nil {
		if len(ps.opts.Production.FeatureHostRegistrations) > 0 {
			hostRegs = ps.opts.Production.FeatureHostRegistrations
		} else if len(ps.opts.Testing.FeatureHostRegistrations) > 0 {
			hostRegs = ps.opts.Testing.FeatureHostRegistrations
		}
	}
	_, err = fh.CompileGeneration(context.Background(), featurehost.GenerationInput{
		Registrations:     regs,
		HostRegistrations: hostRegs,
		BackgroundClient:  client,
		BackgroundPoller:  poller,
	})
	return err
}

func bindReasoningPreservationCompression(genMerged featurebundle.GeneratedMergeSurface, ps *ProcessServices, regs []aipsdk.Registration, client sdkauxiliary.BackgroundClient, poller sdkauxiliary.BackgroundPoller) (featurebundle.GeneratedMergeSurface, error) {
	fh, err := reasoningTestFeatureHost(ps)
	if err != nil {
		return featurebundle.GeneratedMergeSurface{}, err
	}
	var hostRegs []featurehost.Registration
	if ps != nil && ps.opts != nil {
		if len(ps.opts.Production.FeatureHostRegistrations) > 0 {
			hostRegs = ps.opts.Production.FeatureHostRegistrations
		} else if len(ps.opts.Testing.FeatureHostRegistrations) > 0 {
			hostRegs = ps.opts.Testing.FeatureHostRegistrations
		}
	}
	out, err := fh.CompileGeneration(context.Background(), featurehost.GenerationInput{
		Registrations:     regs,
		HostRegistrations: hostRegs,
		MergeSurface:      genMerged,
		Planes:            genMerged.Frozen,
		Lifecycles:        genMerged.Lifecycles,
		BackgroundClient:  client,
		BackgroundPoller:  poller,
	})
	if err != nil {
		return featurebundle.GeneratedMergeSurface{}, err
	}
	return featurebundle.GeneratedMergeSurface{Frozen: out.Planes, Lifecycles: out.Lifecycles}, nil
}

// reasoningTestFeatureHost returns the process StandardFeatures handle when
// present, or a test-owned facade built through the production NewProcess
// constructor otherwise. There is no test-only facade constructor.
func reasoningTestFeatureHost(ps *ProcessServices) (*featurehost.Runtime, error) {
	if ps != nil && ps.StandardFeatures != nil {
		return ps.StandardFeatures, nil
	}
	return featurehost.NewProcess(context.Background(), featurehost.ProcessInput{
		Logger: slog.Default(),
	})
}

// --- Characterization stubs for reasoning binder ---

type charObserverFactory struct{ id string }

func (f charObserverFactory) ID() string                      { return f.id }
func (charObserverFactory) Order() int                        { return 0 }
func (charObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type charAttemptTransform struct{ id string }

func (t charAttemptTransform) ID() string                      { return t.id }
func (charAttemptTransform) Order() int                        { return 0 }
func (charAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{}, nil
}

type charTerminalProvider struct{ id string }

func (p charTerminalProvider) ID() string { return p.id }
func (charTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop}, nil
}

type charEgressPolicy struct{ version string }

func (p charEgressPolicy) Decide(context.Context, reasoninghost.EgressInput) (reasoninghost.EgressDecision, error) {
	return reasoninghost.EgressDecision{Action: reasoninghost.EgressAllow, PolicyVersion: p.version}, nil
}

type charMatcherResolver struct{}

func (charMatcherResolver) Resolve(context.Context) (sdk.Matcher, error) {
	return charMatcher{}, nil
}

type charMatcher struct{}

func (charMatcher) ScanBytes(context.Context, []byte) ([]sdk.Finding, error)  { return nil, nil }
func (charMatcher) ScanString(context.Context, string) ([]sdk.Finding, error) { return nil, nil }
func (charMatcher) RedactBytes(_ context.Context, b []byte) ([]byte, []sdk.Finding, error) {
	return b, nil, nil
}

func (charMatcher) RedactString(_ context.Context, s string) (string, []sdk.Finding, error) {
	return s, nil, nil
}

func setupReasoningTestServices(t *testing.T, egressRef string) (*ProcessServices, aipsdk.Registration, sdkauxiliary.BackgroundClient, sdkauxiliary.BackgroundPoller) {
	t.Helper()
	reg := pluginreg.NewRegistry()
	require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))

	node := reasoningYAMLForTypedNil(t, egressRef)
	pluginReg := aipsdk.Registration{
		ID:          reasoningpreservation.ID,
		FactoryKind: reasoningpreservation.ID,
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: node},
	}

	scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner {
		return fixedRunnerForTypedNil{id: "char-runner"}
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 10})
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheduler.Close() })

	prod := ProductionOptions{
		FeatureHostRegistrations: []featurehost.Registration{
			(&reasoninghost.Binding{
				EgressPolicies: map[string]reasoninghost.EgressPolicy{
					egressRef: charEgressPolicy{version: "v1"},
				},
				MatcherResolver: charMatcherResolver{},
			}).Registration(),
		},
	}
	opts := &BuildOptions{PluginRegistry: reg, Production: prod}

	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:     config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
		Diagnostics: config.DiagnosticsConfig{
			Enabled:    true,
			HealthPath: "/healthz",
		},
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}},
		},
	}
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:           cfg,
		Log:           slog.Default(),
		Opts:          opts,
		BackgroundAux: scheduler,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	genRunner := auxiliary.NewGenerationExecutorRunner()
	bound := scheduler.BindRunner(genRunner)
	bPoller, ok := bound.(sdkauxiliary.BackgroundPoller)
	require.True(t, ok, "bound client must implement BackgroundPoller")

	return ps, pluginReg, bound, bPoller
}

func TestReasoningCompression_ReplaceByIdentity(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-replace")

	reasoningObsID := reasoningpreservation.ID + "-observer"
	reasoningXformID := reasoningpreservation.ID + "-transform"

	tests := []struct {
		name          string
		initialObs    []response.StreamObserverFactory
		initialXforms []request.AttemptTransform
		wantObsIDs    []string
		wantXformIDs  []string
	}{
		{
			name: "replaces existing reasoning participants in middle preserving surrounding third-party participants",
			initialObs: []response.StreamObserverFactory{
				charObserverFactory{id: "custom-obs-1"},
				charObserverFactory{id: reasoningObsID},
				charObserverFactory{id: "custom-obs-2"},
			},
			initialXforms: []request.AttemptTransform{
				charAttemptTransform{id: "custom-xform-1"},
				charAttemptTransform{id: reasoningXformID},
				charAttemptTransform{id: "custom-xform-2"},
			},
			wantObsIDs:   []string{"custom-obs-1", "custom-obs-2", reasoningObsID},
			wantXformIDs: []string{"custom-xform-1", "custom-xform-2", reasoningXformID},
		},
		{
			name: "no prior reasoning participants appends new participants to end",
			initialObs: []response.StreamObserverFactory{
				charObserverFactory{id: "custom-obs-1"},
				charObserverFactory{id: "custom-obs-2"},
			},
			initialXforms: []request.AttemptTransform{
				charAttemptTransform{id: "custom-xform-1"},
				charAttemptTransform{id: "custom-xform-2"},
			},
			wantObsIDs:   []string{"custom-obs-1", "custom-obs-2", reasoningObsID},
			wantXformIDs: []string{"custom-xform-1", "custom-xform-2", reasoningXformID},
		},
		{
			name: "only reasoning participants replaced with single bound participant",
			initialObs: []response.StreamObserverFactory{
				charObserverFactory{id: reasoningObsID},
			},
			initialXforms: []request.AttemptTransform{
				charAttemptTransform{id: reasoningXformID},
			},
			wantObsIDs:   []string{reasoningObsID},
			wantXformIDs: []string{reasoningXformID},
		},
		{
			name: "multiple duplicate reasoning participants all stripped and replaced by single bound participant at end",
			initialObs: []response.StreamObserverFactory{
				charObserverFactory{id: reasoningObsID},
				charObserverFactory{id: "custom-obs-1"},
				charObserverFactory{id: reasoningObsID},
				charObserverFactory{id: "custom-obs-2"},
				charObserverFactory{id: reasoningObsID},
			},
			initialXforms: []request.AttemptTransform{
				charAttemptTransform{id: reasoningXformID},
				charAttemptTransform{id: "custom-xform-1"},
				charAttemptTransform{id: reasoningXformID},
				charAttemptTransform{id: "custom-xform-2"},
				charAttemptTransform{id: reasoningXformID},
			},
			wantObsIDs:   []string{"custom-obs-1", "custom-obs-2", reasoningObsID},
			wantXformIDs: []string{"custom-xform-1", "custom-xform-2", reasoningXformID},
		},
		{
			name:          "empty surface receives single bound participant",
			initialObs:    nil,
			initialXforms: nil,
			wantObsIDs:    []string{reasoningObsID},
			wantXformIDs:  []string{reasoningXformID},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cs := aipfeature.NewContributionSet()
			if len(tc.initialObs) > 0 {
				require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "char-init", tc.initialObs))
			}
			if len(tc.initialXforms) > 0 {
				require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "char-init", tc.initialXforms))
			}
			genMerged := featurebundle.GeneratedMergeSurface{
				Frozen: cs.Freeze(),
			}
			resGen, err := bindReasoningPreservationCompression(genMerged, ps, []aipsdk.Registration{reg}, client, poller)
			require.NoError(t, err)

			gotObs := aipfeature.Get(resGen.Frozen, aipfeature.PlaneStreamObserverFactories)
			gotObsIDs := make([]string, len(gotObs))
			for i, f := range gotObs {
				require.NotNil(t, f)
				gotObsIDs[i] = f.ID()
			}
			assert.Equal(t, tc.wantObsIDs, gotObsIDs)

			gotXforms := aipfeature.Get(resGen.Frozen, aipfeature.PlaneAttemptTransforms)
			gotXformGenIDs := make([]string, len(gotXforms))
			for i, x := range gotXforms {
				require.NotNil(t, x)
				gotXformGenIDs[i] = x.ID()
			}
			assert.Equal(t, tc.wantXformIDs, gotXformGenIDs)
		})
	}
}

func TestReasoningCompression_Idempotence(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-idempotence")

	reasoningObsID := reasoningpreservation.ID + "-observer"
	reasoningXformID := reasoningpreservation.ID + "-transform"

	cs := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "char-init", []request.AttemptTransform{
		charAttemptTransform{id: "custom-xform-1"},
		charAttemptTransform{id: reasoningXformID},
		charAttemptTransform{id: "custom-xform-2"},
	}))
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "char-init", []response.StreamObserverFactory{
		charObserverFactory{id: "custom-obs-1"},
		charObserverFactory{id: reasoningObsID},
		charObserverFactory{id: "custom-obs-2"},
	}))
	initialGen := featurebundle.GeneratedMergeSurface{
		Frozen: cs.Freeze(),
	}

	resGen1, err := bindReasoningPreservationCompression(initialGen, ps, []aipsdk.Registration{reg}, client, poller)
	require.NoError(t, err)

	resGen2, err := bindReasoningPreservationCompression(resGen1, ps, []aipsdk.Registration{reg}, client, poller)
	require.NoError(t, err)

	resGen3, err := bindReasoningPreservationCompression(resGen2, ps, []aipsdk.Registration{reg}, client, poller)
	require.NoError(t, err)

	genXforms1 := aipfeature.Get(resGen1.Frozen, aipfeature.PlaneAttemptTransforms)
	genXforms2 := aipfeature.Get(resGen2.Frozen, aipfeature.PlaneAttemptTransforms)
	genXforms3 := aipfeature.Get(resGen3.Frozen, aipfeature.PlaneAttemptTransforms)
	require.Len(t, genXforms1, 3)
	require.Len(t, genXforms2, 3)
	require.Len(t, genXforms3, 3)

	obs1 := aipfeature.Get(resGen1.Frozen, aipfeature.PlaneStreamObserverFactories)
	obs2 := aipfeature.Get(resGen2.Frozen, aipfeature.PlaneStreamObserverFactories)
	obs3 := aipfeature.Get(resGen3.Frozen, aipfeature.PlaneStreamObserverFactories)
	require.Len(t, obs1, 3)
	require.Len(t, obs2, 3)
	require.Len(t, obs3, 3)

	for i := range 3 {
		assert.Equal(t, genXforms1[i].ID(), genXforms2[i].ID())
		assert.Equal(t, genXforms1[i].ID(), genXforms3[i].ID())
		assert.Equal(t, obs1[i].ID(), obs2[i].ID())
		assert.Equal(t, obs1[i].ID(), obs3[i].ID())
	}
}

func TestReasoningCompression_FailBeforeMutate_CandidateUnmodified(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-fail-mutate")

	cs := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "char-init", []response.StreamObserverFactory{
		charObserverFactory{id: "custom-obs"},
	}))
	initialGen := featurebundle.GeneratedMergeSurface{
		Frozen: cs.Freeze(),
	}
	snapshotGen := initialGen

	t.Run("BindAttemptTransforms rejects nil elements leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		candGen := initialGen
		resG, err := candGen.BindAttemptTransforms("invalid-provider", []request.AttemptTransform{nil})
		require.Error(t, err)
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, resG, "failed composition must return empty surface")
		assert.Equal(t, snapshotGen.Frozen, candGen.Frozen, "original candidate must remain unmodified")
	})

	t.Run("invalid config yaml returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		candGen := initialGen
		var badNode yaml.Node
		_ = yaml.Unmarshal([]byte("action: [invalid-unbalanced\n"), &badNode)

		badReg := aipsdk.Registration{
			ID:          reasoningpreservation.ID,
			FactoryKind: reasoningpreservation.ID,
			Kind:        aipsdk.PluginKindFeature,
			Enabled:     true,
			Config:      aipsdk.ConfigPayload{Node: badNode},
		}

		resG, err := bindReasoningPreservationCompression(candGen, ps, []aipsdk.Registration{badReg}, client, poller)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reasoningpreservation: config")
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, resG)
		assert.Equal(t, snapshotGen.Frozen, candGen.Frozen, "original generated candidate must remain unmodified")
	})

	t.Run("nil background aux client/poller returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		candGen := initialGen
		resG, err := bindReasoningPreservationCompression(candGen, ps, []aipsdk.Registration{reg}, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BackgroundAux")
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, resG)
		assert.Equal(t, snapshotGen.Frozen, candGen.Frozen, "original generated candidate must remain unmodified")
	})

	t.Run("missing egress policy returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		candGen := initialGen
		nodeMissingEgress := reasoningYAMLForTypedNil(t, "nonexistent-egress-ref")
		missingReg := aipsdk.Registration{
			ID:          reasoningpreservation.ID,
			FactoryKind: reasoningpreservation.ID,
			Kind:        aipsdk.PluginKindFeature,
			Enabled:     true,
			Config:      aipsdk.ConfigPayload{Node: nodeMissingEgress},
		}

		resG, err := bindReasoningPreservationCompression(candGen, ps, []aipsdk.Registration{missingReg}, client, poller)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "EgressPolicy")
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, resG)
		assert.Equal(t, snapshotGen.Frozen, candGen.Frozen, "original generated candidate must remain unmodified")
	})

	t.Run("missing matcher resolver returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		candGen := initialGen
		psNoResolver := &ProcessServices{
			opts: &BuildOptions{
				Production: ProductionOptions{
					FeatureHostRegistrations: []featurehost.Registration{
						(&reasoninghost.Binding{
							EgressPolicies:  map[string]reasoninghost.EgressPolicy{"ref-fail-mutate": charEgressPolicy{}},
							MatcherResolver: nil,
						}).Registration(),
					},
				},
			},
		}
		resG, err := bindReasoningPreservationCompression(candGen, psNoResolver, []aipsdk.Registration{reg}, client, poller)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SecretGuard MatcherResolver")
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, resG)
		assert.Equal(t, snapshotGen.Frozen, candGen.Frozen, "original generated candidate must remain unmodified")
	})
}

func TestReasoningCompression_NilElementHandling(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-nil-elem")

	reasoningObsID := reasoningpreservation.ID + "-observer"
	reasoningXformID := reasoningpreservation.ID + "-transform"

	t.Run("BindAttemptTransforms rejects nil elements under NilReject policy", func(t *testing.T) {
		t.Parallel()
		genMerged := featurebundle.GeneratedMergeSurface{}
		_, err := genMerged.BindAttemptTransforms(reasoningpreservation.ID, []request.AttemptTransform{nil})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be nil")
	})

	t.Run("bindReasoningPreservationCompression replaces reasoning participants in attempt transforms and observer factories safely", func(t *testing.T) {
		t.Parallel()
		cs := aipfeature.NewContributionSet()
		require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "char-init", []request.AttemptTransform{
			charAttemptTransform{id: "custom-xform"},
			charAttemptTransform{id: reasoningXformID},
		}))
		require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "char-init", []response.StreamObserverFactory{
			charObserverFactory{id: "custom-obs"},
			charObserverFactory{id: reasoningObsID},
		}))
		genMerged := featurebundle.GeneratedMergeSurface{
			Frozen: cs.Freeze(),
		}

		resGen, err := bindReasoningPreservationCompression(genMerged, ps, []aipsdk.Registration{reg}, client, poller)
		require.NoError(t, err)

		xforms := aipfeature.Get(resGen.Frozen, aipfeature.PlaneAttemptTransforms)
		require.Len(t, xforms, 2)
		assert.Equal(t, "custom-xform", xforms[0].ID())
		assert.Equal(t, reasoningXformID, xforms[1].ID())

		obs := aipfeature.Get(resGen.Frozen, aipfeature.PlaneStreamObserverFactories)
		require.Len(t, obs, 2)
		assert.Equal(t, "custom-obs", obs[0].ID())
		assert.Equal(t, reasoningObsID, obs[1].ID())
	})

	t.Run("BindStreamObserverFactories rejects nil elements under NilReject policy", func(t *testing.T) {
		t.Parallel()
		genMerged := featurebundle.GeneratedMergeSurface{}
		_, err := genMerged.BindStreamObserverFactories(reasoningpreservation.ID, []response.StreamObserverFactory{nil})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be nil")
	})
}

func TestReasoningCompression_DisabledNoOp(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-disabled")

	cs := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "char-init", []request.AttemptTransform{
		charAttemptTransform{id: "custom-xform"},
	}))
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "char-init", []response.StreamObserverFactory{
		charObserverFactory{id: "custom-obs"},
	}))
	initialGen := featurebundle.GeneratedMergeSurface{
		Frozen: cs.Freeze(),
	}

	t.Run("disabled registration is a no-op", func(t *testing.T) {
		t.Parallel()
		disabledReg := reg
		disabledReg.Enabled = false

		resG, err := bindReasoningPreservationCompression(initialGen, ps, []aipsdk.Registration{disabledReg}, client, poller)
		require.NoError(t, err)
		assert.Equal(t, aipfeature.Get(initialGen.Frozen, aipfeature.PlaneAttemptTransforms), aipfeature.Get(resG.Frozen, aipfeature.PlaneAttemptTransforms))
		assert.Equal(t, aipfeature.Get(initialGen.Frozen, aipfeature.PlaneStreamObserverFactories), aipfeature.Get(resG.Frozen, aipfeature.PlaneStreamObserverFactories))
	})

	t.Run("disabled compression in config is a no-op", func(t *testing.T) {
		t.Parallel()
		var node yaml.Node
		raw := `
action: restore
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 24h
  max_turns_per_session: 10
  max_reasoning_bytes_per_turn: 100000
  max_session_bytes: 1000000
compression:
  enabled: false
`
		require.NoError(t, yaml.Unmarshal([]byte(raw), &node))

		disabledCfgReg := aipsdk.Registration{
			ID:          reasoningpreservation.ID,
			FactoryKind: reasoningpreservation.ID,
			Kind:        aipsdk.PluginKindFeature,
			Enabled:     true,
			Config:      aipsdk.ConfigPayload{Node: node},
		}

		resG, err := bindReasoningPreservationCompression(initialGen, ps, []aipsdk.Registration{disabledCfgReg}, client, poller)
		require.NoError(t, err)
		assert.Equal(t, aipfeature.Get(initialGen.Frozen, aipfeature.PlaneAttemptTransforms), aipfeature.Get(resG.Frozen, aipfeature.PlaneAttemptTransforms))
		assert.Equal(t, aipfeature.Get(initialGen.Frozen, aipfeature.PlaneStreamObserverFactories), aipfeature.Get(resG.Frozen, aipfeature.PlaneStreamObserverFactories))
	})
}

func TestReasoningCompression_LazyOpen(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-lazy-open")

	genMerged := featurebundle.GeneratedMergeSurface{}

	resGen, err := bindReasoningPreservationCompression(genMerged, ps, []aipsdk.Registration{reg}, client, poller)
	require.NoError(t, err)
	require.Len(t, aipfeature.Get(resGen.Frozen, aipfeature.PlaneAttemptTransforms), 1)

	factories := aipfeature.Get(resGen.Frozen, aipfeature.PlaneStreamObserverFactories)
	require.Len(t, factories, 1)
	obsFactory := factories[0]
	require.NotNil(t, obsFactory)
	assert.Equal(t, reasoningpreservation.ID+"-observer", obsFactory.ID())

	// Prove lazy Open succeeds and returns a non-nil StreamObserver
	obs, err := obsFactory.Open(context.Background(), response.StreamMeta{
		TraceID: "t-1",
		ALegID:  "a-1",
		BLegID:  "b-1",
		Model:   "test-model",
	}, response.Services{})
	require.NoError(t, err)
	require.NotNil(t, obs, "bound reasoning stream observer factory must open non-nil StreamObserver")
}
