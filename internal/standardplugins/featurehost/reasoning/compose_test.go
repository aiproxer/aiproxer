package reasoning_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	infraaux "github.com/aiproxer/aiproxer/internal/infra/auxiliary"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/reasoning"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type testEgressPolicy struct {
	version string
}

func (p testEgressPolicy) Decide(context.Context, reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: p.version}, nil
}

type testMatcherResolver struct{}

func (testMatcherResolver) Resolve(context.Context) (sdk.Matcher, error) {
	return testMatcher{}, nil
}

type testMatcher struct{}

func (testMatcher) ScanBytes(_ context.Context, _ []byte) ([]sdk.Finding, error) { return nil, nil }

func (testMatcher) ScanString(_ context.Context, _ string) ([]sdk.Finding, error) { return nil, nil }

func (testMatcher) RedactBytes(_ context.Context, b []byte) ([]byte, []sdk.Finding, error) {
	return b, nil, nil
}

func (testMatcher) RedactString(_ context.Context, s string) (string, []sdk.Finding, error) {
	return s, nil, nil
}

type testRunner struct {
	id string
}

func (r testRunner) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "ok-" + r.id}, {Kind: aipapi.EventResponseFinished}}), nil
}

type typedNilClient struct{}

func (t *typedNilClient) SubmitCollect(context.Context, auxiliary.Request, auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return "", nil
}

func (t *typedNilClient) Await(context.Context, auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}
func (t *typedNilClient) Forget(auxiliary.JobID) {}
func (t *typedNilClient) Poll(context.Context, auxiliary.JobID) (auxiliary.PollResult, error) {
	return auxiliary.PollResult{}, nil
}

type typedNilEgress struct{}

func (t *typedNilEgress) Decide(context.Context, reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: "v1"}, nil
}

type typedNilResolver struct{}

func (t *typedNilResolver) Resolve(context.Context) (sdk.Matcher, error) { return nil, nil }

func testReasoningYAML(t *testing.T, enabled bool, egressRef string) yaml.Node {
	t.Helper()
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
`
	if enabled {
		raw += `
compression:
  enabled: true
  mode: shadow
  route: test-route
  timeout: 5s
  max_input_tokens: 10000
  max_input_bytes: 100000
  max_output_tokens: 1000
  max_output_bytes: 100000
  max_surrogate_bytes: 50000
  min_source_bytes: 100
  min_saved_bytes: 50
  min_savings_ratio: 0.5
  max_pending_per_session: 10
  max_surrogate_bytes_per_session: 100000
  max_pending_total: 100
  max_surrogate_bytes_total: 1000000
  egress_policy_ref: ` + egressRef + `
`
	} else {
		raw += `
compression:
  enabled: false
`
	}
	var n yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(raw), &n))
	return n
}

func setupTestScheduler(t *testing.T) (auxiliary.BackgroundClient, auxiliary.BackgroundPoller) {
	t.Helper()
	scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner {
		return testRunner{id: "sched-runner"}
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 10})
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheduler.Close() })

	genRunner := infraaux.NewGenerationExecutorRunner()
	bound := scheduler.BindRunner(genRunner)
	poller, ok := bound.(auxiliary.BackgroundPoller)
	require.True(t, ok)
	return bound, poller
}

func TestReasoningCompression_OptionsPrecedence(t *testing.T) {
	t.Parallel()

	prodPolicy := testEgressPolicy{version: "prod"}
	testPolicy := testEgressPolicy{version: "test"}
	sharedPolicy := testEgressPolicy{version: "prod-override"}

	prod := reasoning.Options{
		EgressPolicies: map[string]reasoningpreservation.EgressPolicy{
			"prod-only": prodPolicy,
			"shared":    sharedPolicy,
		},
		MatcherResolver: testMatcherResolver{},
	}
	test := reasoning.Options{
		EgressPolicies: map[string]reasoningpreservation.EgressPolicy{
			"test-only": testPolicy,
			"shared":    testPolicy,
		},
	}

	merged := reasoning.ComposeOptions(prod, test)
	assert.Equal(t, 3, len(merged.EgressPolicies))
	assert.Equal(t, prodPolicy, merged.EgressPolicies["prod-only"])
	assert.Equal(t, testPolicy, merged.EgressPolicies["test-only"])
	assert.Equal(t, sharedPolicy, merged.EgressPolicies["shared"])
	assert.NotNil(t, merged.MatcherResolver)

	// Fallback to test matcher resolver when prod is nil capability
	var nilResolver *typedNilResolver
	prodWithNil := reasoning.Options{
		MatcherResolver: nilResolver,
	}
	testWithResolver := reasoning.Options{
		MatcherResolver: testMatcherResolver{},
	}
	mergedFallback := reasoning.ComposeOptions(prodWithNil, testWithResolver)
	assert.NotNil(t, mergedFallback.MatcherResolver)
}

func TestReasoningCompression_ValidatePrerequisites(t *testing.T) {
	t.Parallel()

	client, poller := setupTestScheduler(t)
	node := testReasoningYAML(t, true, "my-egress")
	reg := aipsdk.Registration{
		ID:          reasoningpreservation.ID,
		FactoryKind: reasoningpreservation.ID,
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: node},
	}

	t.Run("fails when BackgroundAux is nil", func(t *testing.T) {
		t.Parallel()
		in := reasoning.GenerationInput{
			Registrations: []aipsdk.Registration{reg},
			Client:        nil,
			Poller:        nil,
			Options: reasoning.Options{
				EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"my-egress": testEgressPolicy{}},
				MatcherResolver: testMatcherResolver{},
			},
		}
		err := reasoning.Validate(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BackgroundAux")
	})

	t.Run("fails when client or poller is typed nil", func(t *testing.T) {
		t.Parallel()
		var nilClient *typedNilClient
		var c auxiliary.BackgroundClient = nilClient
		var p auxiliary.BackgroundPoller = nilClient
		in := reasoning.GenerationInput{
			Registrations: []aipsdk.Registration{reg},
			Client:        c,
			Poller:        p,
			Options: reasoning.Options{
				EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"my-egress": testEgressPolicy{}},
				MatcherResolver: testMatcherResolver{},
			},
		}
		err := reasoning.Validate(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BackgroundAux")
	})

	t.Run("fails when egress policy is missing", func(t *testing.T) {
		t.Parallel()
		in := reasoning.GenerationInput{
			Registrations: []aipsdk.Registration{reg},
			Client:        client,
			Poller:        poller,
			Options: reasoning.Options{
				EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"other-egress": testEgressPolicy{}},
				MatcherResolver: testMatcherResolver{},
			},
		}
		err := reasoning.Validate(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `trusted EgressPolicy for "my-egress"`)
	})

	t.Run("fails when egress policy is typed nil", func(t *testing.T) {
		t.Parallel()
		var nilPolicy *typedNilEgress
		in := reasoning.GenerationInput{
			Registrations: []aipsdk.Registration{reg},
			Client:        client,
			Poller:        poller,
			Options: reasoning.Options{
				EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"my-egress": nilPolicy},
				MatcherResolver: testMatcherResolver{},
			},
		}
		err := reasoning.Validate(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `trusted EgressPolicy for "my-egress"`)
	})

	t.Run("fails when MatcherResolver is nil or typed nil", func(t *testing.T) {
		t.Parallel()
		var nilResolver *typedNilResolver
		in := reasoning.GenerationInput{
			Registrations: []aipsdk.Registration{reg},
			Client:        client,
			Poller:        poller,
			Options: reasoning.Options{
				EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"my-egress": testEgressPolicy{}},
				MatcherResolver: nilResolver,
			},
		}
		err := reasoning.Validate(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SecretGuard MatcherResolver")
	})

	t.Run("succeeds when compression is disabled", func(t *testing.T) {
		t.Parallel()
		disabledNode := testReasoningYAML(t, false, "")
		disabledReg := aipsdk.Registration{
			ID:          reasoningpreservation.ID,
			FactoryKind: reasoningpreservation.ID,
			Kind:        aipsdk.PluginKindFeature,
			Enabled:     true,
			Config:      aipsdk.ConfigPayload{Node: disabledNode},
		}
		in := reasoning.GenerationInput{
			Registrations: []aipsdk.Registration{disabledReg},
		}
		require.NoError(t, reasoning.Validate(in))
	})
}

func TestReasoningCompression_BindReplaceByIdentityAndIdempotence(t *testing.T) {
	t.Parallel()

	client, poller := setupTestScheduler(t)
	node := testReasoningYAML(t, true, "my-egress")
	reg := aipsdk.Registration{
		ID:          reasoningpreservation.ID,
		FactoryKind: reasoningpreservation.ID,
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: node},
	}
	opts := reasoning.Options{
		EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"my-egress": testEgressPolicy{version: "v1"}},
		MatcherResolver: testMatcherResolver{},
	}

	cs := aipfeature.NewContributionSet()
	initialGen := featurebundle.GeneratedMergeSurface{
		Frozen: cs.Freeze(),
	}

	in := reasoning.GenerationInput{
		Registrations: []aipsdk.Registration{reg},
		Client:        client,
		Poller:        poller,
		Options:       opts,
	}

	resGen1, err := reasoning.Bind(initialGen, in)
	require.NoError(t, err)

	obs1 := aipfeature.Get(resGen1.Frozen, aipfeature.PlaneStreamObserverFactories)
	xforms1 := aipfeature.Get(resGen1.Frozen, aipfeature.PlaneAttemptTransforms)
	require.Len(t, obs1, 1)
	require.Len(t, xforms1, 1)
	assert.Equal(t, reasoningpreservation.ID+"-observer", obs1[0].ID())
	assert.Equal(t, reasoningpreservation.ID+"-transform", xforms1[0].ID())

	// Idempotence: binding again should replace, not duplicate
	resGen2, err := reasoning.Bind(resGen1, in)
	require.NoError(t, err)

	obs2 := aipfeature.Get(resGen2.Frozen, aipfeature.PlaneStreamObserverFactories)
	xforms2 := aipfeature.Get(resGen2.Frozen, aipfeature.PlaneAttemptTransforms)
	assert.Len(t, obs2, 1)
	assert.Len(t, xforms2, 1)
}

func TestReasoningCompression_BindFailBeforeMutate_CandidateUnmodified(t *testing.T) {
	t.Parallel()

	client, poller := setupTestScheduler(t)
	node := testReasoningYAML(t, true, "missing-ref")
	reg := aipsdk.Registration{
		ID:          reasoningpreservation.ID,
		FactoryKind: reasoningpreservation.ID,
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: node},
	}

	cs := aipfeature.NewContributionSet()
	candGen := featurebundle.GeneratedMergeSurface{
		Frozen: cs.Freeze(),
	}

	in := reasoning.GenerationInput{
		Registrations: []aipsdk.Registration{reg},
		Client:        client,
		Poller:        poller,
		Options:       reasoning.Options{}, // missing egress policy
	}

	resGen, err := reasoning.Bind(candGen, in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trusted EgressPolicy")
	assert.True(t, resGen.Frozen.IsZero(), "failed bind must return zero surface")
}

func TestReasoningCompression_BindDisabledNoOp(t *testing.T) {
	t.Parallel()

	client, poller := setupTestScheduler(t)
	disabledNode := testReasoningYAML(t, false, "")
	reg := aipsdk.Registration{
		ID:          reasoningpreservation.ID,
		FactoryKind: reasoningpreservation.ID,
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: disabledNode},
	}

	cs := aipfeature.NewContributionSet()
	candGen := featurebundle.GeneratedMergeSurface{
		Frozen: cs.Freeze(),
	}

	in := reasoning.GenerationInput{
		Registrations: []aipsdk.Registration{reg},
		Client:        client,
		Poller:        poller,
	}

	resGen, err := reasoning.Bind(candGen, in)
	require.NoError(t, err)
	assert.Equal(t, candGen, resGen)
}
