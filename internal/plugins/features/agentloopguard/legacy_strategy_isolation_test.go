package agentloopguard

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/progress"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/protocolpolicy"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/protocolstate"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/verifier"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file certifies the legacy semantic-verifier strategy's isolation after
// strategy dispatch was introduced (requirements 1.3, 1.5, 9.1-9.6). The legacy
// cause policy, verifier, progress, and recovery behavior is preserved
// unchanged, so every assertion here characterizes current behavior rather than
// requesting a change: the old programmatic API stays behaviorally legacy, the
// legacy strategy never consumes the preferred protocol's request-local
// expectation or state namespace, and each strategy's continuation carries only
// its own fixed vocabulary over the one shared generic continuation contract.

const legacyUnfinishedVerifierResponse = `{"kind":"INCOMPLETE","objective":"resume tests"}`

// legacySemanticVerifierFromStrategy builds the explicit-strategy legacy
// provider through the strict error-returning constructor.
func legacySemanticVerifierFromStrategy(t *testing.T) terminaldecision.Provider {
	t.Helper()
	prov, err := NewConfiguredProvider(Config{Enabled: true, Strategy: StrategySemanticVerifier})
	require.NoError(t, err)
	require.NotNil(t, prov)
	return prov
}

// assertDetachedDefaultVerifier proves the certified legacy verifier request
// shape rather than only the constructed configuration.
func assertDetachedDefaultVerifier(t *testing.T, collector *legacyMatrixCollector, wantTimeout time.Duration) {
	t.Helper()
	require.True(t, collector.hadDeadline, "the legacy path must bound the verifier with its configured timeout")
	assert.InDelta(t, wantTimeout.Seconds(), collector.deadlineRemain.Seconds(), 1.0,
		"Collect deadline must be the configured verifier timeout, not the platform Input.Deadline")
	assert.Equal(t, DefaultVerifierRole, collector.req.Role)
	assert.Equal(t, verifier.VisibilityPrivate, collector.req.Visibility)
	assert.Equal(t, auxiliary.SessionModeDetached, collector.req.SessionMode)
	assert.Equal(t, []string{verifier.RecursionPluginID}, collector.req.DisablePlugins)
}

// TestLegacyProgrammaticAPIRemainsBehaviorallyOmittedStrategy certifies the old
// variadic programmatic API itself, not just the concrete value it returns. The
// no-argument form, a fully omitted enabled config, a partially specified legacy
// config, and the historical invalid-legacy fallback must each keep performing
// exactly one detached bounded verifier call and return the certified default
// legacy continuation over the legacy state namespace.
func TestLegacyProgrammaticAPIRemainsBehaviorallyOmittedStrategy(t *testing.T) {
	t.Parallel()

	defaultTimeout := time.Duration(DefaultVerifierTimeoutSeconds) * time.Second
	cases := []struct {
		name string
		// variadic keeps the historical call shapes, including the no-argument
		// form that cannot be expressed as a single fixed argument.
		variadic []Config
	}{
		{name: "no arguments", variadic: nil},
		{name: "enabled with every legacy field omitted", variadic: []Config{{Enabled: true}}},
		{name: "partially specified legacy config", variadic: []Config{{Enabled: true, MaxSemanticContinuations: 2, NoProgressLimit: 64}}},
		{name: "historical invalid legacy fallback", variadic: []Config{{Enabled: true, VerifierTimeoutSeconds: MaxVerifierTimeoutSeconds + 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			prov := NewProvider(tc.variadic...)
			require.NotNil(t, prov, "the old signature has no error result and must always return a provider for legacy input")

			collector := &legacyMatrixCollector{response: legacyUnfinishedVerifierResponse}
			in := semanticProviderInput()
			in.Auxiliary = collector

			decision := mustDecide(t, prov, in)
			require.Equal(t, terminaldecision.DecisionContinue, decision.Kind)
			require.NotNil(t, decision.Continue)
			assert.Equal(t, progress.ReasonUnfinished, decision.ReasonCode)
			assert.Equal(t, 1, collector.calls, "the legacy strategy must consult its detached verifier once")
			assertDetachedDefaultVerifier(t, collector, defaultTimeout)

			// The old wrapper keeps the legacy progress namespace only.
			_, err := progress.DecodeState(decision.Continue.ControlRef)
			require.NoError(t, err)
			_, err = protocolstate.Decode(decision.Continue.ControlRef)
			require.Error(t, err, "a legacy control reference must never decode as preferred protocol state")
		})
	}
}

// TestLegacyStrategyIgnoresPreferredProtocolExpectation proves the legacy
// strategy does not consume the preferred protocol's request-local projection.
// The canonical contract states that a provider which ignores
// ExplicitCompletionExpected observes exactly the pre-existing contract, so the
// legacy decision, its opaque control reference, and its verifier usage must be
// identical whether or not the platform advertised a completion protocol.
func TestLegacyStrategyIgnoresPreferredProtocolExpectation(t *testing.T) {
	t.Parallel()

	prov := legacySemanticVerifierFromStrategy(t)
	decisions := make([]terminaldecision.Decision, 0, 2)
	for _, expected := range []bool{false, true} {
		collector := &legacyMatrixCollector{response: legacyUnfinishedVerifierResponse}
		in := semanticProviderInput()
		in.Evidence.ExplicitCompletionExpected = expected
		in.Auxiliary = collector

		decision := mustDecide(t, prov, in)
		require.Equal(t, terminaldecision.DecisionContinue, decision.Kind)
		require.Equal(t, 1, collector.calls)
		decisions = append(decisions, decision)
	}
	assert.Equal(t, decisions[0], decisions[1],
		"the preferred completion-protocol expectation must not change the legacy decision or its control reference")
}

// TestLegacyStrategyRefusesPreferredProtocolState proves the legacy strategy
// fails closed on a real preferred protocol state reference instead of learning
// the preferred namespace, and that neither strategy's opaque control reference
// is ever readable as the other's state.
func TestLegacyStrategyRefusesPreferredProtocolState(t *testing.T) {
	t.Parallel()

	prov := legacySemanticVerifierFromStrategy(t)
	base := semanticProviderInput()
	preferredState, err := protocolstate.Encode(protocolstate.State{
		Reprompts:       1,
		LastFingerprint: protocolstate.Fingerprint(base),
	})
	require.NoError(t, err)

	in := base
	in.Continuation.Attempt = 2
	in.Evidence.Lineage.Attempt = 2
	in.Evidence.Lineage.ProgressRef = preferredState
	collector := &legacyMatrixCollector{response: `{"kind":"INCOMPLETE","objective":"must not be used"}`}
	in.Auxiliary = collector

	decision := mustDecide(t, prov, in)
	assert.Equal(t, terminaldecision.DecisionAllowStop, decision.Kind)
	assert.Equal(t, reasonInvalidProgress, decision.ReasonCode)
	assert.Nil(t, decision.Continue)
	assert.Zero(t, collector.calls, "the legacy strategy must refuse the reference before any verifier work")

	// A genuine preferred protocol state token stays valid in its own codec.
	state, err := protocolstate.Decode(preferredState)
	require.NoError(t, err)
	assert.Equal(t, 1, state.Reprompts)
	_, err = progress.DecodeState(preferredState)
	require.Error(t, err, "preferred protocol state must never decode as legacy progress state")
}

// TestStrategyContinuationsCarryOnlyTheirOwnWording proves each strategy's
// continuation keeps its own fixed vocabulary while both use the one generic
// terminal-decision continuation: the legacy intent stays verifier-backed
// recovery text with the legacy reason code, and the preferred intent stays the
// fixed missing-signal repair text. Neither strategy imports the other's
// wording, and no second terminal publication path exists.
func TestStrategyContinuationsCarryOnlyTheirOwnWording(t *testing.T) {
	t.Parallel()

	legacyCollector := &legacyMatrixCollector{response: legacyUnfinishedVerifierResponse}
	legacyInput := semanticProviderInput()
	legacyInput.Auxiliary = legacyCollector
	legacyDecision := mustDecide(t, legacySemanticVerifierFromStrategy(t), legacyInput)
	require.Equal(t, terminaldecision.DecisionContinue, legacyDecision.Kind)
	require.NotNil(t, legacyDecision.Continue)
	require.Equal(t, 1, legacyCollector.calls, "the legacy continuation must be verifier-backed")

	prefIn := preferredInput()
	prefIn.Auxiliary = &forbiddenPreferredCollector{}
	preferredDecision := mustDecide(t, preferredProviderFromConfig(t, Config{Enabled: true, Strategy: StrategyAttemptCompletion}), prefIn)
	require.Equal(t, terminaldecision.DecisionContinue, preferredDecision.Kind)
	require.NotNil(t, preferredDecision.Continue)

	legacyIntent := legacyDecision.Continue
	preferredIntent := preferredDecision.Continue
	require.NoError(t, legacyIntent.Validate())
	require.NoError(t, preferredIntent.Validate())

	// The same generic internal-control continuation is shared by both
	// strategies; only the policy-owned text and reason code differ.
	assert.Equal(t, legacyIntent.Provenance, preferredIntent.Provenance)
	assert.NotEqual(t, legacyDecision.ReasonCode, preferredDecision.ReasonCode)
	assert.Equal(t, progress.ReasonUnfinished, legacyIntent.ReasonCode)
	assert.Equal(t, protocolpolicy.ReasonMissingSignal, preferredIntent.ReasonCode)

	assert.Contains(t, legacyIntent.Instruction, "unfinished_objective")
	assert.NotContains(t, legacyIntent.Instruction, "attempt_completion")
	assert.NotContains(t, legacyIntent.Instruction, "automated-completion-protocol-repair")

	assert.Contains(t, preferredIntent.Instruction, "attempt_completion")
	assert.NotContains(t, preferredIntent.Instruction, "unfinished_objective")
	assert.NotContains(t, preferredIntent.Instruction, "automated-recovery")

	assert.NotEmpty(t, preferredIntent.TrajectoryRef, "the preferred intent keeps a canonical trajectory reference")
}
