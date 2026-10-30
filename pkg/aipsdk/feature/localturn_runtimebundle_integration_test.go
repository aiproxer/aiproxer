package feature_test

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
)

type integrationTraceRecorder struct {
	mu  sync.Mutex
	ids []string
}

func (r *integrationTraceRecorder) Record(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
}

func (r *integrationTraceRecorder) Trace() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.ids))
	copy(out, r.ids)
	return out
}

type integrationLTHandler struct {
	id          string
	order       int
	claim       bool
	matchCount  atomic.Int64
	handleCount atomic.Int64
	recorder    *integrationTraceRecorder
}

func (h *integrationLTHandler) ID() string                     { return h.id }
func (h *integrationLTHandler) Order() int                     { return h.order }
func (h *integrationLTHandler) FailureMode() hooks.FailureMode { return hooks.FailClosed }

func (h *integrationLTHandler) Match(ctx context.Context, call aipapi.Call, meta localturn.Meta) (localturn.MatchResult, error) {
	if h.recorder != nil {
		h.recorder.Record(h.id)
	}
	h.matchCount.Add(1)
	if !h.claim {
		return localturn.MatchResult{Claimed: false}, nil
	}
	return localturn.MatchResult{
		Claimed: true,
		Indexes: []int{0},
		Reason:  "matched",
	}, nil
}

func (h *integrationLTHandler) Handle(ctx context.Context, input localturn.HandleInput) (localturn.Reply, error) {
	h.handleCount.Add(1)
	return localturn.Reply{Text: "handled"}, nil
}

func (h *integrationLTHandler) MatchCount() int64 {
	return h.matchCount.Load()
}

func (h *integrationLTHandler) HandleCount() int64 {
	return h.handleCount.Load()
}

func TestLocalTurnMapBackedCandidate_CompileGenerationOrderAndIsolation(t *testing.T) {
	t.Parallel()

	recorder := &integrationTraceRecorder{}
	candA := &integrationLTHandler{id: "cand-a", order: 10, claim: false, recorder: recorder}
	featMid := &integrationLTHandler{id: "feature-mid", order: 15, claim: false, recorder: recorder}
	candZ := &integrationLTHandler{id: "cand-z", order: 20, claim: true, recorder: recorder}

	candidateSlice := []localturn.Handler{candZ, candA}
	candCs := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(candCs, feature.PlaneLocalTurnHandlers, "cand-plugin", candidateSlice))
	candidateFrozen := candCs.Freeze()

	// Immediately mutate original source slice to prove defensive copy
	candidateSlice[0] = &integrationLTHandler{id: "mutated-z", order: 99}
	candidateSlice[1] = &integrationLTHandler{id: "mutated-a", order: 99}

	reg := pluginreg.NewRegistry()
	require.NoError(t, reg.RegisterFeature("feature-lt", func(n yaml.Node) (feature.FeatureBundle, error) {
		cs := feature.NewContributionSet()
		_ = feature.Contribute(cs, feature.PlaneLocalTurnHandlers, "feature-lt", []localturn.Handler{featMid})
		return feature.BundleFromPlanes(cs.Freeze(), nil), nil
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

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  slog.Default(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	generation, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		CandidateOpts: &runtimebundle.BuildOptions{
			FeaturePlanes: candidateFrozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, generation)
	t.Cleanup(func() { _ = generation.Close() })

	assert.Equal(t, int64(0), candA.MatchCount())
	assert.Equal(t, int64(0), candA.HandleCount())
	assert.Equal(t, int64(0), featMid.MatchCount())
	assert.Equal(t, int64(0), featMid.HandleCount())
	assert.Equal(t, int64(0), candZ.MatchCount())
	assert.Equal(t, int64(0), candZ.HandleCount())

	call := &aipapi.Call{
		Messages: []aipapi.Message{
			{
				Role: aipapi.RoleUser,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "valid user text message"},
				},
			},
		},
	}

	execView := generation.ExecutorView()
	require.NotNil(t, execView)

	stream, err := execView.Execute(context.Background(), call)
	require.NoError(t, err)
	require.NotNil(t, stream)
	defer func() { _ = stream.Close() }()

	events, err := aipapi.Collect(context.Background(), stream)
	require.NoError(t, err)
	assert.NotEmpty(t, events.Text.String())

	assert.Equal(t, []string{"cand-a", "feature-mid", "cand-z"}, recorder.Trace())
	assert.Equal(t, int64(1), candA.MatchCount())
	assert.Equal(t, int64(0), candA.HandleCount())
	assert.Equal(t, int64(1), featMid.MatchCount())
	assert.Equal(t, int64(0), featMid.HandleCount())
	assert.Equal(t, int64(1), candZ.MatchCount())
	assert.Equal(t, int64(1), candZ.HandleCount())
}

func TestLocalTurnMalformedMapCandidate_CompileGenerationAttributedAndPriorGenerationUsable(t *testing.T) {
	t.Parallel()

	recorder := &integrationTraceRecorder{}
	base := &integrationLTHandler{id: "base", order: 10, claim: true, recorder: recorder}

	reg := pluginreg.NewRegistry()
	require.NoError(t, reg.RegisterFeature("feature-lt-base", func(n yaml.Node) (feature.FeatureBundle, error) {
		cs := feature.NewContributionSet()
		_ = feature.Contribute(cs, feature.PlaneLocalTurnHandlers, "feature-lt-base", []localturn.Handler{base})
		return feature.BundleFromPlanes(cs.Freeze(), nil), nil
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

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  slog.Default(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	genBaseline, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, genBaseline)
	t.Cleanup(func() { _ = genBaseline.Close() })

	assert.Equal(t, int64(0), base.MatchCount())
	assert.Equal(t, int64(0), base.HandleCount())

	malformedFrozen := feature.NewMalformedGeneratedFrozenLocalTurnCandidateForTest([]localturn.Handler{nil}, nil)

	_, err = runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		CandidateOpts: &runtimebundle.BuildOptions{
			FeaturePlanes: malformedFrozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})

	require.Error(t, err)
	var attributed *feature.AttributedError
	require.ErrorAs(t, err, &attributed)
	assert.Equal(t, "candidate", attributed.PluginID)
	assert.Equal(t, feature.PlaneLocalTurnHandlers.ID, attributed.PlaneID)
	assert.ErrorIs(t, err, feature.ErrInvalidContribution)

	assert.Equal(t, int64(0), base.MatchCount())
	assert.Equal(t, int64(0), base.HandleCount())

	call := &aipapi.Call{
		Messages: []aipapi.Message{
			{
				Role: aipapi.RoleUser,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "valid user message"},
				},
			},
		},
	}

	execView := genBaseline.ExecutorView()
	require.NotNil(t, execView)

	stream, err := execView.Execute(context.Background(), call)
	require.NoError(t, err)
	require.NotNil(t, stream)
	defer func() { _ = stream.Close() }()

	events, err := aipapi.Collect(context.Background(), stream)
	require.NoError(t, err)
	assert.NotEmpty(t, events.Text.String())

	assert.Equal(t, []string{"base"}, recorder.Trace())
	assert.Equal(t, int64(1), base.MatchCount())
	assert.Equal(t, int64(1), base.HandleCount())
}
