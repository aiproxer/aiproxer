package runtimebundle

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/reasoninghost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Stubs for Request Transform Families ---

type stubReqTransform struct {
	id     string
	ord    int
	events *[]string
	mu     *sync.Mutex
	mutate func(*aipapi.Call)
}

func (t stubReqTransform) ID() string                        { return t.id }
func (t stubReqTransform) Order() int                        { return t.ord }
func (t stubReqTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (t stubReqTransform) Handle(ctx context.Context, call *aipapi.Call, meta request.RequestMeta, svc request.Services) error {
	if t.mu != nil && t.events != nil {
		t.mu.Lock()
		*t.events = append(*t.events, t.id)
		t.mu.Unlock()
	}
	if t.mutate != nil {
		t.mutate(call)
	}
	return nil
}

type stubPreReqHandler struct {
	id     string
	ord    int
	events *[]string
	mu     *sync.Mutex
	deny   bool
}

func (h stubPreReqHandler) ID() string                        { return h.id }
func (h stubPreReqHandler) Order() int                        { return h.ord }
func (h stubPreReqHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (h stubPreReqHandler) Handle(ctx context.Context, call *aipapi.Call, meta prerequest.Meta, svc prerequest.Services) (prerequest.Decision, error) {
	if h.mu != nil && h.events != nil {
		h.mu.Lock()
		*h.events = append(*h.events, h.id)
		h.mu.Unlock()
	}
	if h.deny {
		return prerequest.Deny("denied by " + h.id), nil
	}
	return prerequest.Allow(), nil
}

type stubAttemptTransform struct {
	id     string
	ord    int
	events *[]string
	mu     *sync.Mutex
	mutate func(*aipapi.Call)
}

func (a stubAttemptTransform) ID() string                        { return a.id }
func (a stubAttemptTransform) Order() int                        { return a.ord }
func (a stubAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (a stubAttemptTransform) HandleAttempt(ctx context.Context, call *aipapi.Call, meta request.AttemptMeta, svc request.Services) (request.AttemptDecision, error) {
	if a.mu != nil && a.events != nil {
		a.mu.Lock()
		*a.events = append(*a.events, a.id)
		a.mu.Unlock()
	}
	if a.mutate != nil {
		a.mutate(call)
	}
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type capturePolicyObserver struct {
	mu      sync.Mutex
	records []policydecision.Record
}

func (c *capturePolicyObserver) OnPolicyDecision(_ context.Context, record policydecision.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, record)
	return nil
}

func (c *capturePolicyObserver) snapshot() []policydecision.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]policydecision.Record, len(c.records))
	copy(out, c.records)
	return out
}

// --- Parity and Snapshot Projection Tests ---

func TestRequestTransformsProjection_ParityWithFrozenAndRegistrationOrder(t *testing.T) {
	t.Parallel()

	var events []string
	var mu sync.Mutex

	b1 := testkit.FeatureBundle(t, "b1", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "b1", []request.Transform{
			stubReqTransform{id: "rt-1", ord: 10, events: &events, mu: &mu},
			stubReqTransform{id: "rt-2", ord: 20, events: &events, mu: &mu},
		}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "b1", []prerequest.Handler{
			stubPreReqHandler{id: "pr-1", ord: 10, events: &events, mu: &mu},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "b1", []request.AttemptTransform{
			stubAttemptTransform{id: "at-1", ord: 10, events: &events, mu: &mu},
		})
	}, nil)

	b2 := testkit.FeatureBundle(t, "b2", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "b2", []request.Transform{
			stubReqTransform{id: "rt-3", ord: 5, events: &events, mu: &mu},
		}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "b2", []prerequest.Handler{
			stubPreReqHandler{id: "pr-2", ord: 5, events: &events, mu: &mu},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "b2", []request.AttemptTransform{
			stubAttemptTransform{id: "at-2", ord: 5, events: &events, mu: &mu},
		})
	}, nil)

	gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)

	// Verify Frozen plane contents preserve registration order
	frozenRT := aipfeature.Get(gen.Frozen, aipfeature.PlaneRequestTransforms)
	require.Len(t, frozenRT, 3)
	assert.Equal(t, "rt-1", frozenRT[0].ID())
	assert.Equal(t, "rt-2", frozenRT[1].ID())
	assert.Equal(t, "rt-3", frozenRT[2].ID())

	frozenPR := aipfeature.Get(gen.Frozen, aipfeature.PlanePreRequestHandlers)
	require.Len(t, frozenPR, 2)
	assert.Equal(t, "pr-1", frozenPR[0].ID())
	assert.Equal(t, "pr-2", frozenPR[1].ID())

	frozenAT := aipfeature.Get(gen.Frozen, aipfeature.PlaneAttemptTransforms)
	require.Len(t, frozenAT, 2)
	assert.Equal(t, "at-1", frozenAT[0].ID())
	assert.Equal(t, "at-2", frozenAT[1].ID())

	// Build runtime snapshot from FeaturePlanes directly
	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{
		FeaturePlanes: gen.Frozen,
	}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

	// Snapshot methods return defensive copies preserving order
	snapRT := snap.RequestTransforms()
	require.Len(t, snapRT, 3)
	assert.Equal(t, "rt-1", snapRT[0].ID())
	assert.Equal(t, "rt-2", snapRT[1].ID())
	assert.Equal(t, "rt-3", snapRT[2].ID())

	// PreRequestHandlers are materialized/sorted in snapshot
	snapPR := snap.PreRequestHandlers()
	require.Len(t, snapPR, 2)
	// MaterializeSorted sorts by Order then ID: pr-2 (ord 5) before pr-1 (ord 10)
	assert.Equal(t, "pr-2", snapPR[0].ID())
	assert.Equal(t, "pr-1", snapPR[1].ID())

	// AttemptTransforms are materialized/sorted in snapshot
	snapAT := snap.AttemptTransforms()
	require.Len(t, snapAT, 2)
	assert.Equal(t, "at-2", snapAT[0].ID())
	assert.Equal(t, "at-1", snapAT[1].ID())
}

func TestRequestTransformsProjection_NilVsEmptySemantics(t *testing.T) {
	t.Parallel()

	t.Run("zero_value_bundles_produce_nil_planes", func(t *testing.T) {
		t.Parallel()
		gen, err := featurebundle.MergeBundlesGenerated(aipfeature.FeatureBundle{}, aipfeature.FeatureBundle{})
		require.NoError(t, err)

		assert.Nil(t, aipfeature.Get(gen.Frozen, aipfeature.PlaneRequestTransforms))
		assert.Nil(t, aipfeature.Get(gen.Frozen, aipfeature.PlanePreRequestHandlers))
		assert.Nil(t, aipfeature.Get(gen.Frozen, aipfeature.PlaneAttemptTransforms))

		bus := hooks.New(hooks.Config{})
		opts := &BuildOptions{FeaturePlanes: gen.Frozen}
		snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

		assert.Nil(t, snap.RequestTransforms())
		assert.Nil(t, snap.PreRequestHandlers())
		assert.Nil(t, snap.AttemptTransforms())
	})

	t.Run("explicitly_empty_slices_normalize_properly", func(t *testing.T) {
		t.Parallel()
		b := testkit.FeatureBundle(t, "empty", func(cs *aipfeature.ContributionSet) error {
			if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "empty", []request.Transform{}); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "empty", []prerequest.Handler{}); err != nil {
				return err
			}
			return aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "empty", []request.AttemptTransform{})
		}, nil)
		gen, err := featurebundle.MergeBundlesGenerated(b)
		require.NoError(t, err)

		gotRT := aipfeature.Get(gen.Frozen, aipfeature.PlaneRequestTransforms)
		assert.NotNil(t, gotRT)
		assert.Empty(t, gotRT)

		gotPR := aipfeature.Get(gen.Frozen, aipfeature.PlanePreRequestHandlers)
		assert.NotNil(t, gotPR)
		assert.Empty(t, gotPR)

		gotAT := aipfeature.Get(gen.Frozen, aipfeature.PlaneAttemptTransforms)
		assert.NotNil(t, gotAT)
		assert.Empty(t, gotAT)

		bus := hooks.New(hooks.Config{})
		opts := &BuildOptions{FeaturePlanes: gen.Frozen}
		snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

		assert.NotNil(t, snap.RequestTransforms())
		assert.Empty(t, snap.RequestTransforms())
		assert.Nil(t, snap.PreRequestHandlers())
		assert.Nil(t, snap.AttemptTransforms())
	})
}

func TestRequestTransformsProjection_BackingArrayIsolation(t *testing.T) {
	t.Parallel()

	origRT := []request.Transform{stubReqTransform{id: "rt-orig"}}
	origPR := []prerequest.Handler{stubPreReqHandler{id: "pr-orig"}}
	origAT := []request.AttemptTransform{stubAttemptTransform{id: "at-orig"}}

	b := testkit.FeatureBundle(t, "orig", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "orig", origRT); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "orig", origPR); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "orig", origAT)
	}, nil)

	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)

	// Mutate original slice backing array
	origRT[0] = stubReqTransform{id: "rt-mutated"}
	origPR[0] = stubPreReqHandler{id: "pr-mutated"}
	origAT[0] = stubAttemptTransform{id: "at-mutated"}

	// Frozen plane must retain original value
	assert.Equal(t, "rt-orig", aipfeature.Get(gen.Frozen, aipfeature.PlaneRequestTransforms)[0].ID())
	assert.Equal(t, "pr-orig", aipfeature.Get(gen.Frozen, aipfeature.PlanePreRequestHandlers)[0].ID())
	assert.Equal(t, "at-orig", aipfeature.Get(gen.Frozen, aipfeature.PlaneAttemptTransforms)[0].ID())

	// Snapshot defensive copy isolation
	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{FeaturePlanes: gen.Frozen}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

	snapRT1 := snap.RequestTransforms()
	snapRT1[0] = stubReqTransform{id: "rt-snap-mutated"}
	snapRT2 := snap.RequestTransforms()
	assert.Equal(t, "rt-orig", snapRT2[0].ID())
}

func TestRequestTransformsProjection_RequestTransformsNilAcceptance(t *testing.T) {
	t.Parallel()

	var typedNilTransform *stubReqTransform

	b := testkit.FeatureBundle(t, "nil-rt", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "nil-rt", []request.Transform{
			nil,
			typedNilTransform,
			stubReqTransform{id: "rt-valid"},
		})
	}, nil)

	require.NoError(t, b.Validate())

	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err, "nil and boxed typed nil RequestTransforms elements must be accepted by MergeBundlesGenerated")

	frozen := aipfeature.Get(gen.Frozen, aipfeature.PlaneRequestTransforms)
	require.Len(t, frozen, 3)
	assert.Nil(t, frozen[0])
	assert.Nil(t, frozen[1])
	assert.Equal(t, "rt-valid", frozen[2].ID())

	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{FeaturePlanes: gen.Frozen}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

	snapRT := snap.RequestTransforms()
	require.Len(t, snapRT, 3)
	assert.Nil(t, snapRT[0])
	assert.Nil(t, snapRT[1])
	assert.Equal(t, "rt-valid", snapRT[2].ID())
}

func TestRequestTransformsProjection_PreRequestHandlersNilAcceptance(t *testing.T) {
	t.Parallel()

	var typedNilHandler *stubPreReqHandler

	b := testkit.FeatureBundle(t, "nil-pr", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "nil-pr", []prerequest.Handler{
			nil,
			typedNilHandler,
			stubPreReqHandler{id: "pr-valid", ord: 1},
		})
	}, nil)

	require.NoError(t, b.Validate())

	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err, "nil and boxed typed nil PreRequestHandlers elements must be accepted by MergeBundlesGenerated")

	frozen := aipfeature.Get(gen.Frozen, aipfeature.PlanePreRequestHandlers)
	require.Len(t, frozen, 3)
	assert.Nil(t, frozen[0])
	assert.Nil(t, frozen[1])
	assert.Equal(t, "pr-valid", frozen[2].ID())

	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{FeaturePlanes: gen.Frozen}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

	snapPR := snap.PreRequestHandlers()
	require.Len(t, snapPR, 3)
}

func TestRequestTransformsProjection_AttemptTransformsTypedNilRejection(t *testing.T) {
	t.Parallel()

	var typedNilAttempt *stubAttemptTransform

	t.Run("literal_nil_rejected", func(t *testing.T) {
		t.Parallel()
		cs := aipfeature.NewContributionSet()
		err := aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "test", []request.AttemptTransform{nil})
		require.Error(t, err, "literal nil AttemptTransforms element must fail Contribute")
		var attrErr *aipfeature.AttributedError
		require.ErrorAs(t, err, &attrErr)
		assert.Equal(t, aipfeature.PlaneAttemptTransforms.ID, attrErr.PlaneID)
	})

	t.Run("boxed_typed_nil_rejected", func(t *testing.T) {
		t.Parallel()
		cs := aipfeature.NewContributionSet()
		err := aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "test", []request.AttemptTransform{typedNilAttempt})
		require.Error(t, err, "boxed typed nil AttemptTransforms element must fail Contribute")
		var attrErr *aipfeature.AttributedError
		require.ErrorAs(t, err, &attrErr)
		assert.Equal(t, aipfeature.PlaneAttemptTransforms.ID, attrErr.PlaneID)
	})
}

func TestRequestTransformsProjection_TransactionalBinderReplacement(t *testing.T) {
	t.Parallel()

	b := testkit.FeatureBundle(t, "init", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "init", []request.AttemptTransform{
			stubAttemptTransform{id: "at-initial-1"},
			stubAttemptTransform{id: "at-initial-2"},
		})
	}, nil)
	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)

	// Replace at-initial-1 with new implementation
	replacement := []request.AttemptTransform{
		stubAttemptTransform{id: "at-initial-1"},
	}
	updated, err := gen.BindAttemptTransforms("plugin-replacer", replacement)
	require.NoError(t, err)

	frozenAT := aipfeature.Get(updated.Frozen, aipfeature.PlaneAttemptTransforms)
	require.Len(t, frozenAT, 2)
	assert.Equal(t, "at-initial-2", frozenAT[0].ID())
	assert.Equal(t, "at-initial-1", frozenAT[1].ID())

	// Failed replacement (e.g. nil element) leaves candidate unmodified
	_, failErr := updated.BindAttemptTransforms("plugin-bad", []request.AttemptTransform{nil})
	require.Error(t, failErr)

	// Original updated candidate was not mutated
	frozenAfterFail := aipfeature.Get(updated.Frozen, aipfeature.PlaneAttemptTransforms)
	require.Len(t, frozenAfterFail, 2)
	assert.Equal(t, "at-initial-2", frozenAfterFail[0].ID())
	assert.Equal(t, "at-initial-1", frozenAfterFail[1].ID())
}

// --- End-to-End CompileGeneration and Request Execution Tests ---

func TestCompileGeneration_RequestTransformsExecution(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	var executedTransforms []string
	var executedPreReqs []string
	var executedAttempts []string
	var mu sync.Mutex

	// Register feature 1: request transform and pre-request handler
	require.NoError(t, reg.RegisterFeature("test-shaping-1", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "test-shaping-1", func(cs *aipfeature.ContributionSet) error {
			if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "test-shaping-1", []request.Transform{
				stubReqTransform{
					id:     "tx-prefix",
					ord:    1,
					events: &executedTransforms,
					mu:     &mu,
					mutate: func(c *aipapi.Call) {
						if len(c.Messages) > 0 && len(c.Messages[0].Parts) > 0 {
							c.Messages[0].Parts[0].Text = "[PREFIX] " + c.Messages[0].Parts[0].Text
						}
					},
				},
			}); err != nil {
				return err
			}
			return aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "test-shaping-1", []prerequest.Handler{
				stubPreReqHandler{id: "pr-check", ord: 1, events: &executedPreReqs, mu: &mu},
			})
		}, nil), nil
	}))

	// Register feature 2: attempt transform
	require.NoError(t, reg.RegisterFeature("test-shaping-2", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "test-shaping-2", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "test-shaping-2", []request.AttemptTransform{
				stubAttemptTransform{
					id:     "at-modify",
					ord:    1,
					events: &executedAttempts,
					mu:     &mu,
					mutate: func(c *aipapi.Call) {
						if len(c.Messages) > 0 && len(c.Messages[0].Parts) > 0 {
							c.Messages[0].Parts[0].Text = c.Messages[0].Parts[0].Text + " [ATTEMPT]"
						}
					},
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

	cand := obsTestCandidateConfig(t, "test-shaping-1", "test-shaping-2")

	var gotMu sync.Mutex
	var capturedCall aipapi.Call
	var capturedCallsCount atomic.Int64

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
	ex := bundle.execution.executor
	require.NotNil(t, ex)

	ex.Backends = map[string]execbackend.Backend{
		"stub-backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				gotMu.Lock()
				capturedCall = call
				gotMu.Unlock()
				capturedCallsCount.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	inputCall := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub-backend:default"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello world"}}},
		},
	}

	stream, execErr := ex.Execute(context.Background(), inputCall)
	require.NoError(t, execErr)
	require.NotNil(t, stream)

	_, collectErr := aipapi.Collect(context.Background(), stream)
	require.NoError(t, collectErr)

	assert.Equal(t, int64(1), capturedCallsCount.Load())

	// Verify execution order and mutations
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, executedTransforms, "tx-prefix")
	assert.Contains(t, executedPreReqs, "pr-check")
	assert.Contains(t, executedAttempts, "at-modify")

	// Verify text received both prefix transform and attempt transform
	gotMu.Lock()
	defer gotMu.Unlock()
	require.Len(t, capturedCall.Messages, 1)
	require.Len(t, capturedCall.Messages[0].Parts, 1)
	assert.Equal(t, "[PREFIX] hello world [ATTEMPT]", capturedCall.Messages[0].Parts[0].Text)
}

func TestCompileGeneration_DisabledFeatureContributesNothing(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	require.NoError(t, reg.RegisterFeature("test-disabled-shaping", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "test-disabled-shaping", func(cs *aipfeature.ContributionSet) error {
			if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "test-disabled-shaping", []request.Transform{
				stubReqTransform{id: "disabled-tx"},
			}); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "test-disabled-shaping", []prerequest.Handler{
				stubPreReqHandler{id: "disabled-pr"},
			}); err != nil {
				return err
			}
			return aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "test-disabled-shaping", []request.AttemptTransform{
				stubAttemptTransform{id: "disabled-at"},
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

	// Feature registered in factory catalog but NOT enabled in candidate features
	cand := obsTestCandidateConfig(t)

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
	ex := bundle.execution.executor
	require.NotNil(t, ex)

	snap := ex.RuntimeSnapshot
	require.NotNil(t, snap)

	assert.Nil(t, snap.RequestTransforms())
	assert.Nil(t, snap.PreRequestHandlers())
	assert.Nil(t, snap.AttemptTransforms())
}

func TestCompileGeneration_CandidateFeaturePlanesOverlayShaping(t *testing.T) {
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

	var executedTransforms []string
	var executedPreReqs []string
	var executedAttempts []string
	var mu sync.Mutex

	candBundle := testkit.FeatureBundle(t, "cand", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "cand", []request.Transform{
			stubReqTransform{
				id:     "cand-tx-prefix",
				ord:    1,
				events: &executedTransforms,
				mu:     &mu,
				mutate: func(c *aipapi.Call) {
					if len(c.Messages) > 0 && len(c.Messages[0].Parts) > 0 {
						c.Messages[0].Parts[0].Text = "[CAND_PREFIX] " + c.Messages[0].Parts[0].Text
					}
				},
			},
		}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "cand", []prerequest.Handler{
			stubPreReqHandler{id: "cand-pr-check", ord: 1, events: &executedPreReqs, mu: &mu},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "cand", []request.AttemptTransform{
			stubAttemptTransform{
				id:     "cand-at-modify",
				ord:    1,
				events: &executedAttempts,
				mu:     &mu,
				mutate: func(c *aipapi.Call) {
					if len(c.Messages) > 0 && len(c.Messages[0].Parts) > 0 {
						c.Messages[0].Parts[0].Text = c.Messages[0].Parts[0].Text + " [CAND_ATTEMPT]"
					}
				},
			},
		})
	}, nil)
	candGen, err := featurebundle.MergeBundlesGenerated(candBundle)
	require.NoError(t, err)

	cand := obsTestCandidateConfig(t)

	var gotMu sync.Mutex
	var capturedCall aipapi.Call
	var capturedCallsCount atomic.Int64

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
	require.True(t, ok, "genRuntime must be *GenerationBundle")
	ex := bundle.execution.executor
	require.NotNil(t, ex)

	ex.Backends = map[string]execbackend.Backend{
		"stub-backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				gotMu.Lock()
				capturedCall = call
				gotMu.Unlock()
				capturedCallsCount.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	inputCall := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub-backend:default"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "candidate input"}}},
		},
	}

	stream, execErr := ex.Execute(context.Background(), inputCall)
	require.NoError(t, execErr)
	require.NotNil(t, stream)

	_, collectErr := aipapi.Collect(context.Background(), stream)
	require.NoError(t, collectErr)

	assert.Equal(t, int64(1), capturedCallsCount.Load())

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, executedTransforms, "cand-tx-prefix")
	assert.Contains(t, executedPreReqs, "cand-pr-check")
	assert.Contains(t, executedAttempts, "cand-at-modify")

	gotMu.Lock()
	defer gotMu.Unlock()
	require.Len(t, capturedCall.Messages, 1)
	require.Len(t, capturedCall.Messages[0].Parts, 1)
	assert.Equal(t, "[CAND_PREFIX] candidate input [CAND_ATTEMPT]", capturedCall.Messages[0].Parts[0].Text)
}

func TestCompileGeneration_CandidateFeaturePlanes_UnrelatedPlanesIgnored(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	var (
		rtCalled bool
		atCalled bool
		mu       sync.Mutex
	)

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

	// Candidate overlay contains allowed candidate request planes:
	// - RequestTransforms
	// - PreRequestHandlers
	// - AttemptTransforms
	// AND populated UNRELATED planes that must be ignored for this wave:
	// - TrafficObservers
	// - UsageObservers
	// - SubmitHooks
	// - StreamObserverFactories
	// - TerminalDecisionProvider
	candBundle := testkit.FeatureBundle(t, "cand", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "cand", []request.Transform{
			stubReqTransform{
				id:  "cand-rt",
				ord: 1,
				mutate: func(c *aipapi.Call) {
					mu.Lock()
					rtCalled = true
					mu.Unlock()
				},
			},
		}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "cand", []prerequest.Handler{
			stubPreReqHandler{
				id:  "cand-pr",
				ord: 1,
			},
		}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "cand", []request.AttemptTransform{
			stubAttemptTransform{
				id:  "cand-at",
				ord: 1,
				mutate: func(c *aipapi.Call) {
					mu.Lock()
					atCalled = true
					mu.Unlock()
				},
			},
		}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "cand", []traffic.Observer{
			stubTrafficObs{
				id:     "unrelated-cand-to",
				events: &[]string{},
				mu:     &mu,
			},
		}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "cand", []usage.Observer{
			stubUsageObs{
				id:     "unrelated-cand-uo",
				events: &[]string{},
				mu:     &mu,
			},
		}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, "cand", []sdkhooks.SubmitHook{
			stubSubmitHook{
				id:    "unrelated-cand-submit",
				order: 1,
			},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "cand", []response.StreamObserverFactory{
			stubStreamObsFactory{
				id: "unrelated-cand-so",
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
			if in.Frontends.TrafficPorts.Obs != nil {
				_ = in.Frontends.TrafficPorts.Obs.OnObservation(ctx, traffic.Observation{Leg: traffic.LegCTP})
			}
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok)
	ex := bundle.execution.executor
	require.NotNil(t, ex)

	ex.Backends = map[string]execbackend.Backend{
		"stub-backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	inputCall := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub-backend:default"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "negative candidate test"}}},
		},
	}

	stream, execErr := ex.Execute(context.Background(), inputCall)
	require.NoError(t, execErr)
	require.NotNil(t, stream)

	_, collectErr := aipapi.Collect(context.Background(), stream)
	require.NoError(t, collectErr)

	mu.Lock()
	defer mu.Unlock()

	// Candidate request-shaping planes were executed
	assert.True(t, rtCalled, "candidate request transform must run")
	assert.True(t, atCalled, "candidate attempt transform must run")

	// Unrelated candidate planes were ignored
	snap := ex.RuntimeSnapshot
	require.NotNil(t, snap)
	assert.Equal(t, traffic.NoopObserver{}, snap.TrafficObserver(), "unrelated candidate traffic observer must be ignored")
	assert.Equal(t, usage.NoopObserver{}, snap.UsageObserver(), "unrelated candidate usage observer must be ignored")
	assert.Empty(t, snap.StreamObserverFactories(), "unrelated candidate stream observer factory must be ignored")
}

func TestCompileGeneration_CandidateFeaturePlanes_ReasoningPreservationAttemptReplacement(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	egressRef := "egress-cand-reasoning-replace"
	node := reasoningYAMLForTypedNil(t, egressRef)

	scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner {
		return fixedRunnerForTypedNil{id: "reasoning-cand-runner"}
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

	cfg := obsTestProcessConfig()
	cfg.Plugins.Features = []config.PluginConfig{
		{ID: reasoningpreservation.ID, Enabled: true, Config: node},
	}
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:           cfg,
		Log:           testkit.DiscardLogger(),
		Opts:          &BuildOptions{PluginRegistry: reg, Production: prod},
		BackgroundAux: scheduler,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	var executionSequence []string
	var mu sync.Mutex

	reasoningXformID := reasoningpreservation.ID + "-transform"

	// Candidate overlay: contributes cand-at-1, a stub attempt transform with matching reasoning ID, and cand-at-2
	candBundle := testkit.FeatureBundle(t, "cand", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "cand", []request.Transform{
			stubReqTransform{
				id:     "cand-rt",
				ord:    1,
				events: &executionSequence,
				mu:     &mu,
			},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "cand", []request.AttemptTransform{
			stubAttemptTransform{
				id:     "cand-at-survivor-1",
				ord:    -10,
				events: &executionSequence,
				mu:     &mu,
			},
			stubAttemptTransform{
				id:     reasoningXformID,
				ord:    0,
				events: &executionSequence,
				mu:     &mu,
				mutate: func(c *aipapi.Call) {
					if len(c.Messages) > 0 && len(c.Messages[0].Parts) > 0 {
						c.Messages[0].Parts[0].Text += " [CAND_RAW_STUB]"
					}
				},
			},
			stubAttemptTransform{
				id:     "cand-at-survivor-2",
				ord:    10,
				events: &executionSequence,
				mu:     &mu,
			},
		})
	}, nil)
	candGen, err := featurebundle.MergeBundlesGenerated(candBundle)
	require.NoError(t, err)

	cand := obsTestCandidateConfig(t)
	cand.Plugins.Features = []config.PluginConfig{
		{ID: reasoningpreservation.ID, Enabled: true, Config: node},
	}

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
	ex := bundle.execution.executor
	require.NotNil(t, ex)

	var gotMu sync.Mutex
	var capturedCall aipapi.Call

	ex.Backends = map[string]execbackend.Backend{
		"stub-backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				gotMu.Lock()
				capturedCall = call
				gotMu.Unlock()
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	snap := ex.RuntimeSnapshot
	require.NotNil(t, snap)
	xforms := snap.AttemptTransforms()
	require.Len(t, xforms, 3)
	assert.Equal(t, "cand-at-survivor-1", xforms[0].ID())
	assert.Equal(t, reasoningXformID, xforms[1].ID())
	assert.Equal(t, "cand-at-survivor-2", xforms[2].ID())

	inputCall := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub-backend:default"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "reasoning test"}}},
		},
	}

	stream, execErr := ex.Execute(context.Background(), inputCall)
	require.NoError(t, execErr)
	require.NotNil(t, stream)

	_, collectErr := aipapi.Collect(context.Background(), stream)
	require.NoError(t, collectErr)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, executionSequence, 3)
	assert.Equal(t, "cand-rt", executionSequence[0])
	assert.Equal(t, "cand-at-survivor-1", executionSequence[1])
	assert.Equal(t, "cand-at-survivor-2", executionSequence[2])

	gotMu.Lock()
	defer gotMu.Unlock()
	assert.NotContains(t, capturedCall.Messages[0].Parts[0].Text, "[CAND_RAW_STUB]", "candidate stub transform must be replaced by bound generation binder")
}

func TestCompileGeneration_PreRequestDenyDecisionEvidence(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	var txCalled, atCalled, backendCalled bool
	var mu sync.Mutex

	require.NoError(t, reg.RegisterFeature("test-deny-feature", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "test-deny-feature", func(cs *aipfeature.ContributionSet) error {
			if err := aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "test-deny-feature", []prerequest.Handler{
				stubPreReqHandler{id: "pr-deny-gate", ord: 1, deny: true},
			}); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "test-deny-feature", []request.Transform{
				stubReqTransform{
					id:  "tx-should-not-run",
					ord: 1,
					mutate: func(c *aipapi.Call) {
						mu.Lock()
						txCalled = true
						mu.Unlock()
					},
				},
			}); err != nil {
				return err
			}
			return aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "test-deny-feature", []request.AttemptTransform{
				stubAttemptTransform{
					id:  "at-should-not-run",
					ord: 1,
					mutate: func(c *aipapi.Call) {
						mu.Lock()
						atCalled = true
						mu.Unlock()
					},
				},
			})
		}, nil), nil
	}))

	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	captured := &capturePolicyObserver{}
	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
			Policy: PolicyOptions{
				PolicyObservers: []policydecision.Observer{captured},
			},
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand := obsTestCandidateConfig(t, "test-deny-feature")

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
	require.True(t, ok)
	ex := bundle.execution.executor
	require.NotNil(t, ex)

	ex.Backends = map[string]execbackend.Backend{
		"stub-backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				mu.Lock()
				backendCalled = true
				mu.Unlock()
				return nil, nil
			},
		},
	}

	inputCall := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub-backend:default"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "should deny"}}},
		},
	}

	_, execErr := ex.Execute(context.Background(), inputCall)
	require.Error(t, execErr, "pre-request deny must fail execution")
	assert.Contains(t, execErr.Error(), "denied by pr-deny-gate")

	records := captured.snapshot()
	require.NotEmpty(t, records, "expected policy decision record emitted on denial")
	var found bool
	for _, rec := range records {
		if rec.Provider.Stage == aipfeature.StageIDPreRequest {
			found = true
			assert.Equal(t, policydecision.OutcomeDeny, rec.Outcome)
			assert.Equal(t, extensions.ReasonPreRequestDenied, rec.ReasonCode)
			assert.Equal(t, "pr-deny-gate", rec.Provider.ID)
			assert.False(t, rec.BackendAttempted)
			assert.Equal(t, "denied by pr-deny-gate", rec.ClientMessage)
			assert.Equal(t, policydecision.EffectNone, rec.Effect)
		}
	}
	assert.True(t, found, "expected pre_request stage policy decision record")

	mu.Lock()
	defer mu.Unlock()
	assert.True(t, txCalled, "request transform runs before pre-request stage")
	assert.False(t, atCalled, "attempt transform must not run when pre-request denies")
	assert.False(t, backendCalled, "backend must not run when pre-request denies")
}
