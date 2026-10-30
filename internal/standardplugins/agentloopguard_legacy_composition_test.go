package standardplugins

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/progress"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/protocolstate"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/verifier"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestStandardBundle_AgentLoopGuardOmittedStrategyContributesTerminalProviderOnly(t *testing.T) {
	t.Parallel()

	bundle := mustBuildALGBundle(t, "enabled: true\n")
	require.Equal(t, []string{aipfeature.PlaneTerminalDecisionProvider.ID}, occupiedStandardPlanes(t, bundle.PlaneSet))
	prov := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, prov)
	id, err := terminaldecision.ProviderIdentity(prov)
	require.NoError(t, err)
	assert.Equal(t, agentloopguard.ID, id)
}

func TestStandardBundle_AgentLoopGuardDisabledAndWithdrawnLeaveNoProvider(t *testing.T) {
	t.Parallel()

	t.Run("disabled", func(t *testing.T) {
		t.Parallel()
		bundle := mustBuildALGBundle(t, "enabled: false\n")
		assert.Empty(t, occupiedStandardPlanes(t, bundle.PlaneSet))
		assert.Nil(t, aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneTerminalDecisionProvider))
	})

	t.Run("withdrawn-registry", func(t *testing.T) {
		t.Parallel()
		reg := pluginreg.NewRegistry()
		stock := StandardBundle()
		withoutALG := Bundle{Features: make([]FeatureRegistration, 0, len(stock.Features))}
		for _, feature := range stock.Features {
			if feature.ID == agentloopguard.ID {
				continue
			}
			withoutALG.Features = append(withoutALG.Features, feature)
		}
		require.NoError(t, InstallBundleOn(reg, withoutALG))

		var node yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte("enabled: true\n"), &node))
		_, err := reg.BuildFeatureBundle(agentloopguard.ID, node)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown feature plugin")

		empty := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}
		assert.Nil(t, aipfeature.Get(empty.PlaneSet, aipfeature.PlaneTerminalDecisionProvider))
	})
}

func TestStandardBundle_AgentLoopGuardInvalidCandidateLeavesLastGoodBundle(t *testing.T) {
	t.Parallel()

	reg := testRegistryWithStdBundle(t)
	first := mustBuildALGBundleOn(t, reg, "enabled: true\nmax_semantic_continuations: 2\nno_progress_limit: 64\n")
	firstProv := aipfeature.Get(first.PlaneSet, aipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, firstProv)

	second := mustBuildALGBundleOn(t, reg, "enabled: true\nmax_semantic_continuations: 4\nno_progress_limit: 64\n")
	secondProv := aipfeature.Get(second.PlaneSet, aipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, secondProv)

	assertALGProviderTripsSemanticCap(t, firstProv, 2)
	assertALGProviderTripsSemanticCap(t, secondProv, 4)

	var invalid yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("enabled: true\nmax_semantic_continuations: 0\n"), &invalid))
	_, err := reg.BuildFeatureBundle(agentloopguard.ID, invalid)
	require.Error(t, err)

	stillFirst := aipfeature.Get(first.PlaneSet, aipfeature.PlaneTerminalDecisionProvider)
	stillSecond := aipfeature.Get(second.PlaneSet, aipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, stillFirst)
	require.NotNil(t, stillSecond)
	assertALGProviderTripsSemanticCap(t, stillFirst, 2)
	assertALGProviderTripsSemanticCap(t, stillSecond, 4)
	assert.Equal(t, []string{aipfeature.PlaneTerminalDecisionProvider.ID}, occupiedStandardPlanes(t, first.PlaneSet))
	assert.Equal(t, []string{aipfeature.PlaneTerminalDecisionProvider.ID}, occupiedStandardPlanes(t, second.PlaneSet))
}

// algLegacyParityCollector records the detached verifier request a real composed
// legacy bundle issues, so the old-style and the explicit legacy generation can
// be compared on retained verifier facts instead of only on their decisions.
type algLegacyParityCollector struct {
	response       string
	err            error
	calls          int
	req            auxiliary.Request
	hadDeadline    bool
	deadlineRemain time.Duration
}

func (c *algLegacyParityCollector) Collect(ctx context.Context, req auxiliary.Request) (aipapi.Collected, error) {
	c.calls++
	c.req = req
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			c.hadDeadline = true
			c.deadlineRemain = time.Until(deadline)
		}
	}
	if c.err != nil {
		return aipapi.Collected{}, c.err
	}
	var out aipapi.Collected
	out.Text.WriteString(c.response)
	return out, nil
}

func (*algLegacyParityCollector) Stream(context.Context, auxiliary.Request) (aipapi.EventStream, error) {
	return nil, nil
}

// algLegacyStateTokenPrefix is the pre-spec legacy continuation state namespace.
// The preferred strategy owns protocolstate.TokenPrefix instead, and neither
// codec may read the other's reference.
const algLegacyStateTokenPrefix = "alg-state-v1."

// algPublishedGeneration is one ALG generation compiled through the real
// composition path: its occupied planes and the production request snapshot
// built from them.
type algPublishedGeneration struct {
	planes aipfeature.FrozenPlaneSet
	snap   *extensions.RequestRuntimeSnapshot
}

func (g algPublishedGeneration) terminal() terminaldecision.Provider {
	return g.snap.TerminalDecisionProvider()
}

func (g algPublishedGeneration) control() controltool.Provider { return g.snap.ControlToolProvider() }

// compileALGGenerationSnapshot publishes one ALG generation through the real
// standard registry, the enabled-registration merge, and the production request
// snapshot builder, so every assertion below runs against the provider the
// factory actually composed rather than a manually assigned fake.
func compileALGGenerationSnapshot(t *testing.T, reg *pluginreg.Registry, raw string, outerEnabled bool) (algPublishedGeneration, error) {
	t.Helper()

	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		return algPublishedGeneration{}, err
	}
	registrations := []aipsdk.Registration{{
		ID:      agentloopguard.ID,
		Kind:    aipsdk.PluginKindFeature,
		Enabled: outerEnabled,
		Config:  aipsdk.ConfigPayload{Node: node},
	}}
	merged, err := featurebundle.MergeFeatureSurfacesWithHost(reg, registrations, featurebundle.HostContributions{})
	if err != nil {
		return algPublishedGeneration{}, err
	}
	return algPublishedGeneration{
		planes: merged.Frozen,
		snap:   extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{FeaturePlanes: merged.Frozen}),
	}, nil
}

func mustCompileALGGenerationSnapshot(t *testing.T, reg *pluginreg.Registry, raw string, outerEnabled bool) algPublishedGeneration {
	t.Helper()

	gen, err := compileALGGenerationSnapshot(t, reg, raw, outerEnabled)
	require.NoError(t, err)
	require.NotNil(t, gen.snap)
	return gen
}

// mustCompileALGWithoutRegisteredFeature publishes an empty generic surface from
// a registry that no longer registers ALG at all.
func mustCompileALGWithoutRegisteredFeature(t *testing.T) algPublishedGeneration {
	t.Helper()

	withdrawn := pluginreg.NewRegistry()
	stock := StandardBundle()
	withoutALG := Bundle{}
	for _, feature := range stock.Features {
		if feature.ID == agentloopguard.ID {
			continue
		}
		withoutALG.Features = append(withoutALG.Features, feature)
	}
	require.NoError(t, InstallBundleOn(withdrawn, withoutALG))
	merged, err := featurebundle.MergeFeatureSurfacesWithHost(withdrawn, nil, featurebundle.HostContributions{})
	require.NoError(t, err)
	gen := algPublishedGeneration{
		planes: merged.Frozen,
		snap:   extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{FeaturePlanes: merged.Frozen}),
	}
	require.NotNil(t, gen.snap)
	return gen
}

// TestStandardBundle_AgentLoopGuardLegacyBundlesMatchOldStyleConfig proves the
// composed real bundles, not just the feature package, keep one legacy decision
// table. The pre-spec enabled shape that omits the selector and the explicit
// semantic_verifier selector are each published through the real factory and
// registry, and the real terminal provider each generation actually contributed
// is invoked. Every fixture is checked against its own expectation first, and
// only then compared, so the equality is a parity result rather than a
// self-fulfilling comparison. Both generations must occupy the terminal plane
// only: the legacy strategy contributes no proxy-owned control provider.
func TestStandardBundle_AgentLoopGuardLegacyBundlesMatchOldStyleConfig(t *testing.T) {
	t.Parallel()

	reg := testRegistryWithStdBundle(t)
	fixtures := []struct {
		name       string
		configure  func(*terminaldecision.Input)
		response   string
		auxErr     error
		omitAux    bool
		wantKind   terminaldecision.DecisionKind
		wantReason string
		wantCalls  int
	}{
		{
			name:       "eligible clean stop uses the detached verifier",
			response:   `{"kind":"INCOMPLETE","objective":"resume tests"}`,
			wantKind:   terminaldecision.DecisionContinue,
			wantReason: progress.ReasonUnfinished,
			wantCalls:  1,
		},
		{
			name:       "verifier confirmed complete answer stops",
			response:   `{"kind":"COMPLETE"}`,
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: progress.ReasonComplete,
			wantCalls:  1,
		},
		{
			name:       "verifier transport error fails closed",
			auxErr:     errors.New("auxiliary transport detail"),
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: progress.ReasonUncertain,
			wantCalls:  1,
		},
		{
			name:       "verifier timeout fails closed",
			auxErr:     context.DeadlineExceeded,
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: progress.ReasonUncertain,
			wantCalls:  1,
		},
		{
			name:       "absent verifier fails closed",
			omitAux:    true,
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: progress.ReasonUncertain,
			wantCalls:  0,
		},
		{
			name: "authoritative refusal keeps cause ownership",
			configure: func(in *terminaldecision.Input) {
				in.Candidate.Cause = terminaldecision.CandidateCauseRefusal
			},
			response:   `{"kind":"INCOMPLETE","objective":"must not be used"}`,
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: "authoritative_candidate",
			wantCalls:  0,
		},
		{
			name: "pre-output transport keeps recovery ownership",
			configure: func(in *terminaldecision.Input) {
				in.Candidate.Cause = terminaldecision.CandidateCauseTransport
				in.Candidate.OutputCommitted = false
			},
			response:   `{"kind":"INCOMPLETE","objective":"must not be used"}`,
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: "pre_output_transport",
			wantCalls:  0,
		},
	}

	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			run := func(raw string) (terminaldecision.Decision, *algLegacyParityCollector) {
				gen := mustCompileALGGenerationSnapshot(t, reg, raw, true)
				prov := gen.terminal()
				require.NotNil(t, prov)
				require.Nil(t, gen.control(),
					"a legacy generation must never contribute a proxy-owned completion control provider")
				require.Equal(t, []string{aipfeature.PlaneTerminalDecisionProvider.ID}, occupiedStandardPlanes(t, gen.planes))
				id, err := terminaldecision.ProviderIdentity(prov)
				require.NoError(t, err)
				require.Equal(t, agentloopguard.ID, id)

				in := algPinnedSemanticInput()
				var collector *algLegacyParityCollector
				if !tc.omitAux {
					collector = &algLegacyParityCollector{response: tc.response, err: tc.auxErr}
					in.Auxiliary = collector
				}
				if tc.configure != nil {
					tc.configure(&in)
				}
				decision, err := prov.Decide(context.Background(), in)
				require.NoError(t, err)
				require.NoError(t, decision.Validate())
				assert.Equal(t, tc.wantKind, decision.Kind)
				assert.Equal(t, tc.wantReason, decision.ReasonCode)
				assert.Equal(t, tc.wantCalls, collectorCalls(collector))
				if tc.wantCalls > 0 {
					assert.True(t, collector.hadDeadline, "the legacy bundle must bound its verifier with a deadline")
					assert.InDelta(t, time.Duration(agentloopguard.DefaultVerifierTimeoutSeconds)*time.Second, collector.deadlineRemain, float64(time.Second),
						"the composed verifier deadline must be the configured verifier timeout")
					assert.Equal(t, agentloopguard.DefaultVerifierRole, collector.req.Role)
					assert.Equal(t, verifier.VisibilityPrivate, collector.req.Visibility)
					assert.Equal(t, auxiliary.SessionModeDetached, collector.req.SessionMode)
					assert.Equal(t, []string{verifier.RecursionPluginID}, collector.req.DisablePlugins)
					assert.Equal(t, "trace-1", collector.req.ParentTraceID)
					assert.Equal(t, "a-leg-1", collector.req.ParentALegID)
					assert.Equal(t, "b-leg-1", collector.req.ParentBLegID)
				}
				return decision, collector
			}

			oldYAML, oldCollector := run("enabled: true\n")
			explicit, explicitCollector := run("enabled: true\nstrategy: semantic_verifier\n")
			assert.Equal(t, oldYAML, explicit,
				"the explicit semantic_verifier selector must not change a composed legacy decision")
			if tc.wantCalls > 0 {
				assert.Equal(t, oldCollector.req, explicitCollector.req,
					"the retained detached-verifier request must be identical across both legacy shapes")
			}
			for _, decision := range []terminaldecision.Decision{oldYAML, explicit} {
				if decision.Kind != terminaldecision.DecisionContinue {
					continue
				}
				require.NotNil(t, decision.Continue)
				assert.Equal(t, "internal-control", decision.Continue.Provenance)
				assert.Equal(t, "trajectory-1", decision.Continue.TrajectoryRef)
				assert.Equal(t, progress.ReasonUnfinished, decision.Continue.ReasonCode)
				assert.Contains(t, decision.Continue.Instruction, "<automated-recovery>")
				assert.True(t, strings.HasPrefix(decision.Continue.ControlRef, algLegacyStateTokenPrefix))
				_, err := progress.DecodeState(decision.Continue.ControlRef)
				require.NoError(t, err, "a composed legacy continuation must carry legacy state")
				_, err = protocolstate.Decode(decision.Continue.ControlRef)
				assert.Error(t, err, "legacy state must never decode as preferred protocol state")
			}
		})
	}
}

func collectorCalls(c *algLegacyParityCollector) int {
	if c == nil {
		return 0
	}
	return c.calls
}

// assertALGPinnedLegacyPolicy proves one already-published legacy generation
// keeps its own selected strategy, verifier policy, and semantic budget after
// later generations were published.
func assertALGPinnedLegacyPolicy(t *testing.T, prov terminaldecision.Provider, role string, timeout time.Duration, cap uint8) {
	t.Helper()

	collector := &algLegacyParityCollector{response: `{"kind":"INCOMPLETE","objective":"resume tests"}`}
	in := algPinnedSemanticInput()
	in.Auxiliary = collector
	decision, err := prov.Decide(context.Background(), in)
	require.NoError(t, err)
	require.NoError(t, decision.Validate())
	require.Equal(t, terminaldecision.DecisionContinue, decision.Kind)
	require.NotNil(t, decision.Continue)
	assert.Equal(t, 1, collector.calls, "a pinned legacy generation must keep consulting its own verifier")
	assert.Equal(t, role, collector.req.Role, "a pinned generation must keep its own verifier role")
	assert.Equal(t, verifier.VisibilityPrivate, collector.req.Visibility)
	assert.Equal(t, auxiliary.SessionModeDetached, collector.req.SessionMode)
	assert.True(t, collector.hadDeadline)
	assert.InDelta(t, timeout.Seconds(), collector.deadlineRemain.Seconds(), 1.0)
	assert.Equal(t, "trace-1", collector.req.ParentTraceID)
	assert.Equal(t, "a-leg-1", collector.req.ParentALegID)
	assert.Equal(t, "b-leg-1", collector.req.ParentBLegID)
	assert.Equal(t, "internal-control", decision.Continue.Provenance)
	assert.True(t, strings.HasPrefix(decision.Continue.ControlRef, algLegacyStateTokenPrefix),
		"a pinned legacy generation must keep the pre-spec state namespace")
	_, err = protocolstate.Decode(decision.Continue.ControlRef)
	assert.Error(t, err)
	assertALGProviderTripsSemanticCap(t, prov, cap)
}

// TestStandardBundle_AgentLoopGuardPinnedLegacyProviderSurvivesStrategyWithdrawal
// proves generation pinning across a selected-strategy change and a withdrawal.
// The in-flight legacy generation keeps its own terminal provider, verifier
// policy, and semantic budget; the replacement generation contributes the real
// terminal and completion-control providers and still cannot reach the
// auxiliary verifier; a removed and an unregistered ALG leave the generic
// control-tool seam inert.
func TestStandardBundle_AgentLoopGuardPinnedLegacyProviderSurvivesStrategyWithdrawal(t *testing.T) {
	t.Parallel()

	reg := testRegistryWithStdBundle(t)
	const pinned = "enabled: true\nstrategy: semantic_verifier\nverifier_role: parity_verifier\n" +
		"verifier_timeout_seconds: 9\nmax_semantic_continuations: 2\nno_progress_limit: 64\n"

	first := mustCompileALGGenerationSnapshot(t, reg, pinned, true)
	require.Nil(t, first.control(), "the legacy bundle contributes no control provider")
	firstProv := first.terminal()
	require.NotNil(t, firstProv)

	// A replacement generation selects the preferred strategy.
	preferred := mustCompileALGGenerationSnapshot(t, reg, "enabled: true\nstrategy: attempt_completion\n", true)
	preferredProv := preferred.terminal()
	require.NotNil(t, preferredProv)
	control := preferred.control()
	require.NotNil(t, control, "the preferred bundle contributes the real completion control provider")
	require.Equal(t, reflect.TypeOf(agentloopguard.NewCompletionToolProvider()), reflect.TypeOf(control))
	aux := &algForbiddenAux{}
	in := algPinnedSemanticInput()
	in.Evidence.ExplicitCompletionExpected = true
	in.Evidence.Lineage.ProgressRef = in.Request.BLegID
	in.Auxiliary = aux
	decision, err := preferredProv.Decide(context.Background(), in)
	require.NoError(t, err)
	require.NoError(t, decision.Validate())
	require.Equal(t, terminaldecision.DecisionContinue, decision.Kind)
	require.NotNil(t, decision.Continue)
	assert.True(t, strings.HasPrefix(decision.Continue.ControlRef, protocolstate.TokenPrefix),
		"the preferred strategy must own its own state namespace exclusively")
	_, err = progress.DecodeState(decision.Continue.ControlRef)
	assert.Error(t, err, "preferred protocol state must never be decoded with the legacy codec")
	assert.Zero(t, aux.calls, "the preferred bundle must not reach the auxiliary verifier")

	// A replacement generation removes ALG, a nested disable contributes
	// nothing, and then ALG is unregistered entirely.
	removed := mustCompileALGGenerationSnapshot(t, reg, pinned, false)
	require.Nil(t, removed.terminal())
	require.Nil(t, removed.control())
	nested := mustCompileALGGenerationSnapshot(t, reg, "enabled: false\nstrategy: semantic_verifier\n", true)
	require.Nil(t, nested.terminal())
	require.Nil(t, nested.control())
	require.Empty(t, occupiedStandardPlanes(t, nested.planes))
	withdrawn := mustCompileALGWithoutRegisteredFeature(t)
	require.Nil(t, withdrawn.terminal())
	require.Nil(t, withdrawn.control())

	// The in-flight generation is unchanged by all of the above: it still
	// decides with its own verifier role, timeout, and semantic budget, and its
	// terminal provider is still the legacy implementation, not the preferred one.
	assertALGPinnedLegacyPolicy(t, first.terminal(), "parity_verifier", 9*time.Second, 2)
	assert.Equal(t, firstProv, first.terminal(), "a pinned generation must keep the same frozen provider value")
	assert.NotEqual(t, reflect.TypeOf(firstProv), reflect.TypeOf(preferredProv),
		"the pinned legacy provider and the replacement preferred provider are different implementations")
}

func mustBuildALGBundle(t *testing.T, raw string) aipfeature.FeatureBundle {
	t.Helper()
	return mustBuildALGBundleOn(t, testRegistryWithStdBundle(t), raw)
}

func mustBuildALGBundleOn(t *testing.T, reg *pluginreg.Registry, raw string) aipfeature.FeatureBundle {
	t.Helper()
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(raw), &node))
	bundle, err := reg.BuildFeatureBundle(agentloopguard.ID, node)
	require.NoError(t, err)
	require.NoError(t, bundle.Validate())
	return bundle
}

func occupiedStandardPlanes(t *testing.T, set aipfeature.FrozenPlaneSet) []string {
	t.Helper()
	cs := set.ToContributions()
	ids := make([]string, 0, 1)
	for _, plane := range aipfeature.StandardPlanes {
		if cs.Has(plane.PlaneID()) {
			ids = append(ids, plane.PlaneID())
		}
	}
	return ids
}

type algPinCollector struct {
	responses []string
	calls     int
}

func (c *algPinCollector) Collect(context.Context, auxiliary.Request) (aipapi.Collected, error) {
	var out aipapi.Collected
	if c.calls < len(c.responses) {
		out.Text.WriteString(c.responses[c.calls])
	}
	c.calls++
	return out, nil
}

func (*algPinCollector) Stream(context.Context, auxiliary.Request) (aipapi.EventStream, error) {
	return nil, nil
}

func algPinnedSemanticInput() terminaldecision.Input {
	return terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{
			Cause:           terminaldecision.CandidateCauseNormal,
			Reference:       "candidate-1",
			OutputCommitted: true,
		},
		Request: terminaldecision.RequestIdentity{
			RequestID: "request-1",
			TraceID:   "trace-1",
			ALegID:    "a-leg-1",
			BLegID:    "b-leg-1",
		},
		Policy: terminaldecision.PolicySnapshot{
			Revision:                "policy-1",
			MaxContinuationAttempts: 8,
		},
		Continuation: terminaldecision.ContinuationEvidence{
			TrajectoryRef: "trajectory-1",
			Attempt:       1,
		},
		Evidence: terminaldecision.Evidence{
			Objective:     "finish the requested change",
			RecentText:    "run the focused tests",
			CandidateText: "the implementation is ready",
			Actions: [terminaldecision.MaxEvidenceActions]terminaldecision.ActionFact{
				{
					ItemID: "item-1",
					CallID: "call-1",
					Kind:   aipapi.ItemKindToolResult,
					Status: aipapi.ItemStatusCompleted,
					Name:   "go_test",
				},
				{
					ItemID: "item-2",
					Kind:   aipapi.ItemKindMessage,
					Status: aipapi.ItemStatusInProgress,
				},
			},
			ActionCount: 2,
			Lineage: terminaldecision.EvidenceLineage{
				TrajectoryRef: "trajectory-1",
				ParentRef:     "trajectory-0",
				Attempt:       1,
			},
		},
		Deadline: time.Date(2035, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}

func assertALGProviderTripsSemanticCap(t *testing.T, prov terminaldecision.Provider, cap uint8) {
	t.Helper()
	require.Greater(t, cap, uint8(1))

	in := algPinnedSemanticInput()
	responses := make([]string, int(cap)+1)
	for i := range responses {
		responses[i] = `{"kind":"INCOMPLETE","objective":"resume tests"}`
	}
	in.Auxiliary = &algPinCollector{responses: responses}

	var prev terminaldecision.Decision
	for attempt := uint8(1); attempt < cap; attempt++ {
		if attempt > 1 {
			in.Continuation.Attempt = attempt
			in.Evidence.Lineage.Attempt = attempt
			require.NotNil(t, prev.Continue)
			in.Evidence.Lineage.ProgressRef = prev.Continue.ControlRef
		}
		decision, err := prov.Decide(context.Background(), in)
		require.NoError(t, err)
		require.NoError(t, decision.Validate())
		require.Equal(t, terminaldecision.DecisionContinue, decision.Kind, "attempt %d under cap %d should continue", attempt, cap)
		require.NotNil(t, decision.Continue)
		prev = decision
	}

	in.Continuation.Attempt = cap
	in.Evidence.Lineage.Attempt = cap
	require.NotNil(t, prev.Continue)
	in.Evidence.Lineage.ProgressRef = prev.Continue.ControlRef
	last, err := prov.Decide(context.Background(), in)
	require.NoError(t, err)
	require.NoError(t, last.Validate())
	assert.Equal(t, terminaldecision.DecisionAllowStop, last.Kind)
	assert.Equal(t, progress.ReasonBudgetExhausted, last.ReasonCode)
	assert.Nil(t, last.Continue)
}
