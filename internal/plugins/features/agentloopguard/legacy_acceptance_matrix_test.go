package agentloopguard

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/progress"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/protocolstate"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/verifier"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// legacyConfigSource names an enabled-path YAML shape for the reusable legacy
// matrix. Omitted strategy is the pre-spec enabled path; the explicit
// semantic_verifier selector must produce the same observable decisions
// (requirements 1.5, 9.6) instead of gaining a second decision table.
type legacyConfigSource string

const (
	legacyConfigOmittedStrategy legacyConfigSource = "omitted_strategy"
	// legacyConfigExplicitStrategy is the new explicit legacy selector. Task
	// 9.2 runs every certified case through both shapes; the explicit one may
	// never change a decision, a verifier invocation, or a state namespace.
	legacyConfigExplicitStrategy legacyConfigSource = "explicit_semantic_verifier"
)

// legacyStateTokenPrefix is the pre-spec legacy continuation state namespace
// pinned from the original enabled-path baseline. The preferred strategy owns a
// different namespace, and neither codec may read the other's reference.
const legacyStateTokenPrefix = "alg-state-v1."

var legacyEnabledConfigSources = []legacyConfigSource{
	legacyConfigOmittedStrategy,
	legacyConfigExplicitStrategy,
}

func legacyEnabledYAML(source legacyConfigSource, extra string) string {
	switch source {
	case legacyConfigOmittedStrategy:
		return "enabled: true\n" + extra
	case legacyConfigExplicitStrategy:
		return "enabled: true\nstrategy: semantic_verifier\n" + extra
	default:
		panic("unknown legacy config source " + string(source))
	}
}

type legacyAcceptanceCase struct {
	name         string
	yamlExtra    string
	response     string
	auxErr       error
	omitAux      bool
	explicit     bool
	uncommitted  bool
	cause        terminaldecision.CandidateCause
	mutate       func(*terminaldecision.Input)
	wantKind     terminaldecision.DecisionKind
	wantReason   string
	wantCalls    int
	wantVerifier bool
	wantContinue bool
}

// legacyAcceptanceCases is the certified omitted-strategy decision matrix.
// Keep case names and expected outcomes stable so later explicit
// semantic_verifier YAML can reuse this table without rewriting fixtures.
var legacyAcceptanceCases = []legacyAcceptanceCase{
	{
		name:         "eligible clean stop uses detached verifier",
		response:     `{"kind":"INCOMPLETE","objective":"resume tests"}`,
		wantKind:     terminaldecision.DecisionContinue,
		wantReason:   progress.ReasonUnfinished,
		wantCalls:    1,
		wantVerifier: true,
		wantContinue: true,
	},
	{
		name:       "trusted explicit completion bypasses verifier",
		response:   `{"kind":"INCOMPLETE","objective":"must not be used"}`,
		explicit:   true,
		wantKind:   terminaldecision.DecisionAllowStop,
		wantReason: reasonExplicitComplete,
		wantCalls:  0,
	},
	{
		name:         "verify policy uses verifier for explicit completion",
		yamlExtra:    "explicit_completion_policy: verify\n",
		response:     `{"kind":"INCOMPLETE","objective":"resume tests"}`,
		explicit:     true,
		wantKind:     terminaldecision.DecisionContinue,
		wantReason:   progress.ReasonUnfinished,
		wantCalls:    1,
		wantVerifier: true,
		wantContinue: true,
	},
	{
		name:       "missing verifier is uncertain",
		omitAux:    true,
		wantKind:   terminaldecision.DecisionAllowStop,
		wantReason: progress.ReasonUncertain,
		wantCalls:  0,
	},
	{
		name:         "malformed verifier is uncertain",
		response:     "not-json",
		wantKind:     terminaldecision.DecisionAllowStop,
		wantReason:   progress.ReasonUncertain,
		wantCalls:    1,
		wantVerifier: true,
	},
	{
		name:         "uncertain verifier is uncertain",
		response:     `{"kind":"UNCERTAIN"}`,
		wantKind:     terminaldecision.DecisionAllowStop,
		wantReason:   progress.ReasonUncertain,
		wantCalls:    1,
		wantVerifier: true,
	},
	{
		name:         "verifier error is uncertain",
		auxErr:       errors.New("transport detail"),
		wantKind:     terminaldecision.DecisionAllowStop,
		wantReason:   progress.ReasonUncertain,
		wantCalls:    1,
		wantVerifier: true,
	},
	{
		name:         "verifier timeout error is uncertain",
		auxErr:       context.DeadlineExceeded,
		wantKind:     terminaldecision.DecisionAllowStop,
		wantReason:   progress.ReasonUncertain,
		wantCalls:    1,
		wantVerifier: true,
	},
	{
		name:         "complete without explicit completion stops after verifier",
		response:     `{"kind":"COMPLETE"}`,
		wantKind:     terminaldecision.DecisionAllowStop,
		wantReason:   progress.ReasonComplete,
		wantCalls:    1,
		wantVerifier: true,
	},
	{
		// The verifier confirms the answer, so a stop for work the user must
		// finish is a real semantic decision rather than a heuristic.
		name:     "user directed question stops after verifier",
		response: `{"kind":"COMPLETE"}`,
		mutate: func(in *terminaldecision.Input) {
			in.Evidence.CandidateText = "Would you like to provide the missing account?"
			in.Evidence.Actions[1] = terminaldecision.ActionFact{}
			in.Evidence.ActionCount = 1
		},
		wantKind:     terminaldecision.DecisionAllowStop,
		wantReason:   progress.ReasonComplete,
		wantCalls:    1,
		wantVerifier: true,
	},
	{
		name:     "optional improvement stops after verifier",
		response: `{"kind":"COMPLETE"}`,
		mutate: func(in *terminaldecision.Input) {
			in.Evidence.CandidateText = "The requested change is complete; an optional cleanup is available."
			in.Evidence.Actions[1] = terminaldecision.ActionFact{}
			in.Evidence.ActionCount = 1
		},
		wantKind:     terminaldecision.DecisionAllowStop,
		wantReason:   progress.ReasonComplete,
		wantCalls:    1,
		wantVerifier: true,
	},
	{
		name:         "committed transport interruption follows verifier",
		cause:        terminaldecision.CandidateCauseTransport,
		response:     `{"kind":"INCOMPLETE","objective":"resume tests"}`,
		wantKind:     terminaldecision.DecisionContinue,
		wantReason:   progress.ReasonUnfinished,
		wantCalls:    1,
		wantVerifier: true,
		wantContinue: true,
	},
	{
		name:         "committed limit follows verifier",
		cause:        terminaldecision.CandidateCauseLimit,
		response:     `{"kind":"INCOMPLETE","objective":"resume tests"}`,
		wantKind:     terminaldecision.DecisionContinue,
		wantReason:   progress.ReasonUnfinished,
		wantCalls:    1,
		wantVerifier: true,
		wantContinue: true,
	},
	{
		name:       "authoritative refusal skips verifier",
		cause:      terminaldecision.CandidateCauseRefusal,
		response:   `{"kind":"INCOMPLETE","objective":"must not be used"}`,
		wantKind:   terminaldecision.DecisionAllowStop,
		wantReason: "authoritative_candidate",
		wantCalls:  0,
	},
	{
		name:       "authoritative cancellation skips verifier",
		cause:      terminaldecision.CandidateCauseCancellation,
		response:   `{"kind":"INCOMPLETE","objective":"must not be used"}`,
		wantKind:   terminaldecision.DecisionAllowStop,
		wantReason: "authoritative_candidate",
		wantCalls:  0,
	},
	{
		name:       "authoritative content filter skips verifier",
		cause:      terminaldecision.CandidateCauseContentFilter,
		response:   `{"kind":"INCOMPLETE","objective":"must not be used"}`,
		wantKind:   terminaldecision.DecisionAllowStop,
		wantReason: "authoritative_candidate",
		wantCalls:  0,
	},
	{
		name:        "pre-output transport skips verifier",
		cause:       terminaldecision.CandidateCauseTransport,
		uncommitted: true,
		response:    `{"kind":"INCOMPLETE","objective":"must not be used"}`,
		wantKind:    terminaldecision.DecisionAllowStop,
		wantReason:  "pre_output_transport",
		wantCalls:   0,
	},
	{
		name:     "unsafe partial action skips verifier",
		cause:    terminaldecision.CandidateCauseProviderError,
		response: `{"kind":"INCOMPLETE","objective":"must not be used"}`,
		mutate: func(in *terminaldecision.Input) {
			in.Evidence.Actions[0] = terminaldecision.ActionFact{
				CallID: "call-unsafe",
				Kind:   aipapi.ItemKindToolCall,
				Status: aipapi.ItemStatusInProgress,
				Name:   "run_sensitive_tool",
			}
			in.Evidence.Actions[1] = terminaldecision.ActionFact{}
			in.Evidence.ActionCount = 1
		},
		wantKind:   terminaldecision.DecisionAllowStop,
		wantReason: "unsafe_action",
		wantCalls:  0,
	},
}

type legacyMatrixCollector struct {
	response       string
	err            error
	calls          int
	hadDeadline    bool
	deadlineRemain time.Duration
	req            auxiliary.Request
}

func (c *legacyMatrixCollector) Collect(ctx context.Context, req auxiliary.Request) (aipapi.Collected, error) {
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

func (*legacyMatrixCollector) Stream(context.Context, auxiliary.Request) (aipapi.EventStream, error) {
	return nil, nil
}

// TestLegacyEnabledYAMLOmitsStrategyAndKeepsVerifierDefaults keeps the original
// task 1.2 name while running every enabled legacy YAML shape: the pre-spec
// omission and the explicit semantic_verifier selector must both resolve to the
// certified legacy defaults.
func TestLegacyEnabledYAMLOmitsStrategyAndKeepsVerifierDefaults(t *testing.T) {
	t.Parallel()

	for _, source := range legacyEnabledConfigSources {
		t.Run(string(source), func(t *testing.T) {
			t.Parallel()
			cfg := decodeLegacyYAML(t, legacyEnabledYAML(source, ""))
			require.True(t, cfg.Enabled)
			assert.Equal(t, DefaultVerifierRole, cfg.VerifierRole)
			assert.Equal(t, DefaultVerifierTimeoutSeconds, cfg.VerifierTimeoutSeconds)
			assert.Equal(t, DefaultMaxSemanticContinuations, cfg.MaxSemanticContinuations)
			assert.Equal(t, DefaultNoProgressLimit, cfg.NoProgressLimit)
			assert.Equal(t, ExplicitCompletionPolicyTrust, cfg.ExplicitCompletionPolicy)
		})
	}
}

func TestLegacyEnabledYAMLAcceptsExplicitSemanticVerifierSelector(t *testing.T) {
	t.Parallel()

	cfg, err := decodeLegacyYAMLErr(t, "enabled: true\nstrategy: semantic_verifier\n")
	require.NoError(t, err)
	assert.Equal(t, StrategySemanticVerifier, cfg.Strategy)
	assert.Equal(t, DefaultVerifierRole, cfg.VerifierRole)
	assert.Equal(t, DefaultVerifierTimeoutSeconds, cfg.VerifierTimeoutSeconds)
	assert.Equal(t, DefaultMaxSemanticContinuations, cfg.MaxSemanticContinuations)
	assert.Equal(t, DefaultNoProgressLimit, cfg.NoProgressLimit)
	assert.Equal(t, ExplicitCompletionPolicyTrust, cfg.ExplicitCompletionPolicy)
}

func TestLegacyAcceptanceMatrix(t *testing.T) {
	t.Parallel()

	for _, source := range legacyEnabledConfigSources {
		t.Run(string(source), func(t *testing.T) {
			t.Parallel()
			for _, tc := range legacyAcceptanceCases {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					runLegacyAcceptanceCase(t, source, tc)
				})
			}
		})
	}
}

func TestLegacyAcceptanceMatrixProgressAndBudgetCaps(t *testing.T) {
	t.Parallel()

	for _, source := range legacyEnabledConfigSources {
		t.Run(string(source), func(t *testing.T) {
			t.Parallel()

			t.Run("no-progress", func(t *testing.T) {
				t.Parallel()
				prov := NewProvider(decodeLegacyYAML(t, legacyEnabledYAML(source, "max_semantic_continuations: 5\nno_progress_limit: 2\n")))
				in := semanticProviderInput()
				in.Policy.MaxContinuationAttempts = 5
				collector := &providerSemanticCollector{responses: []string{
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
				}}
				in.Auxiliary = collector

				first := mustDecide(t, prov, in)
				require.Equal(t, terminaldecision.DecisionContinue, first.Kind)
				require.NotNil(t, first.Continue)

				secondIn := in
				secondIn.Continuation.Attempt = 2
				secondIn.Evidence.Lineage.Attempt = 2
				secondIn.Evidence.Lineage.ProgressRef = first.Continue.ControlRef
				second := mustDecide(t, prov, secondIn)
				require.Equal(t, terminaldecision.DecisionContinue, second.Kind)
				require.NotNil(t, second.Continue)

				thirdIn := secondIn
				thirdIn.Continuation.Attempt = 3
				thirdIn.Evidence.Lineage.Attempt = 3
				thirdIn.Evidence.Lineage.ProgressRef = second.Continue.ControlRef
				third := mustDecide(t, prov, thirdIn)
				assert.Equal(t, terminaldecision.DecisionAllowStop, third.Kind)
				assert.Equal(t, progress.ReasonNoProgress, third.ReasonCode)
				assert.Nil(t, third.Continue)
			})

			t.Run("semantic-budget", func(t *testing.T) {
				t.Parallel()
				prov := NewProvider(decodeLegacyYAML(t, legacyEnabledYAML(source, "max_semantic_continuations: 3\nno_progress_limit: 64\n")))
				in := semanticProviderInput()
				in.Policy.MaxContinuationAttempts = 3
				in.Auxiliary = &providerSemanticCollector{responses: []string{
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
				}}

				first := mustDecide(t, prov, in)
				require.Equal(t, terminaldecision.DecisionContinue, first.Kind)
				require.NotNil(t, first.Continue)

				secondIn := in
				secondIn.Continuation.Attempt = 2
				secondIn.Evidence.Lineage.Attempt = 2
				secondIn.Evidence.Lineage.ProgressRef = first.Continue.ControlRef
				second := mustDecide(t, prov, secondIn)
				require.Equal(t, terminaldecision.DecisionContinue, second.Kind)
				require.NotNil(t, second.Continue)

				thirdIn := secondIn
				thirdIn.Continuation.Attempt = 3
				thirdIn.Evidence.Lineage.Attempt = 3
				thirdIn.Evidence.Lineage.ProgressRef = second.Continue.ControlRef
				third := mustDecide(t, prov, thirdIn)
				assert.Equal(t, terminaldecision.DecisionAllowStop, third.Kind)
				assert.Equal(t, progress.ReasonBudgetExhausted, third.ReasonCode)
				assert.Nil(t, third.Continue)
			})

			// Real progress on every turn must not replenish the immutable
			// semantic-continuation total, and the strictest no-progress limit
			// must stay the only reason a progressing sequence could stop early.
			t.Run("progress keeps the immutable total budget", func(t *testing.T) {
				t.Parallel()
				prov := NewProvider(decodeLegacyYAML(t, legacyEnabledYAML(source, "max_semantic_continuations: 3\nno_progress_limit: 1\n")))
				in := semanticProviderInput()
				in.Policy.MaxContinuationAttempts = 3
				in.Auxiliary = &providerSemanticCollector{responses: []string{
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
				}}

				first := mustDecide(t, prov, in)
				require.Equal(t, terminaldecision.DecisionContinue, first.Kind)
				require.NotNil(t, first.Continue)

				secondIn := in
				secondIn.Continuation.Attempt = 2
				secondIn.Evidence.Lineage.Attempt = 2
				secondIn.Evidence.Lineage.ProgressRef = first.Continue.ControlRef
				secondIn.Evidence.CandidateText = "the second turn produced new canonical work"
				second := mustDecide(t, prov, secondIn)
				require.Equal(t, terminaldecision.DecisionContinue, second.Kind)
				require.NotNil(t, second.Continue)

				thirdIn := secondIn
				thirdIn.Continuation.Attempt = 3
				thirdIn.Evidence.Lineage.Attempt = 3
				thirdIn.Evidence.Lineage.ProgressRef = second.Continue.ControlRef
				thirdIn.Evidence.CandidateText = "the third turn produced more canonical work"
				third := mustDecide(t, prov, thirdIn)
				assert.Equal(t, terminaldecision.DecisionAllowStop, third.Kind)
				assert.Equal(t, progress.ReasonBudgetExhausted, third.ReasonCode,
					"progress must not reset the immutable total continuation budget")
				assert.Nil(t, third.Continue)
			})
		})
	}
}

// TestLegacyExplicitStrategyDecisionsMatchOldStyleConfig is the 9.2 parity
// matrix (requirements 1.5, 9.1-9.6). Every certified case runs through three
// shapes: the pre-spec enabled YAML that omits the new selector, the historical
// programmatic struct literal that no longer carries a selector at all, and the
// explicit semantic_verifier selector. Each run is first checked against the
// table's own independent expectations, so the equality between the shapes is a
// parity result rather than a self-fulfilling comparison, and the comparison
// covers the decision, the continuation intent, the emitted state namespace, the
// verifier invocation count, and the retained detached-verifier request facts.
func TestLegacyExplicitStrategyDecisionsMatchOldStyleConfig(t *testing.T) {
	t.Parallel()

	for _, tc := range legacyAcceptanceCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			oldYAML := observeLegacyAcceptanceCase(t, legacyConfigOmittedStrategy, tc)
			oldProgrammatic := observeLegacyConfig(t, legacyProgrammaticConfig(t, tc), tc)
			explicit := observeLegacyAcceptanceCase(t, legacyConfigExplicitStrategy, tc)
			for _, obs := range []legacyCaseObservation{oldYAML, oldProgrammatic, explicit} {
				assertLegacyCaseExpectation(t, tc, obs)
				assertLegacyStateNamespace(t, obs.decision)
			}

			assert.Equal(t, oldYAML.decision, explicit.decision,
				"an explicit semantic_verifier selector must not change the observable decision")
			assert.Equal(t, oldProgrammatic.decision, explicit.decision,
				"the historical programmatic path must still decide exactly like the explicit selector")
			assert.Equal(t, oldYAML.calls, explicit.calls,
				"an explicit semantic_verifier selector must not change verifier invocation")
			assert.Equal(t, oldProgrammatic.calls, explicit.calls)
			if tc.wantVerifier {
				assert.Equal(t, oldYAML.request, explicit.request,
					"the retained detached-verifier request, including its lineage facts, must be identical")
				assert.Equal(t, oldProgrammatic.request, explicit.request)
			}
		})
	}
}

// TestLegacyExplicitStrategyHandlesUnknownAndCrossStrategyState proves both
// legacy configuration shapes fail closed on the same unusable state references
// and never adopt the preferred strategy's namespace. A real preferred protocol
// token, an unknown namespace, and a corrupt legacy token are refused before
// any verifier work on a later attempt, while the pre-continuation lineage
// tolerance of the original enabled path is preserved for a first candidate and
// still emits a fresh legacy token.
func TestLegacyExplicitStrategyHandlesUnknownAndCrossStrategyState(t *testing.T) {
	t.Parallel()

	base := semanticProviderInput()
	preferredToken, err := protocolstate.Encode(protocolstate.State{
		Reprompts:       1,
		LastFingerprint: protocolstate.Fingerprint(base),
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(preferredToken, protocolstate.TokenPrefix))

	cases := []struct {
		name       string
		ref        string
		attempt    uint8
		wantKind   terminaldecision.DecisionKind
		wantReason string
		wantCalls  int
	}{
		{
			name:       "later attempt refuses a real preferred protocol token",
			ref:        preferredToken,
			attempt:    2,
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: reasonInvalidProgress,
			wantCalls:  0,
		},
		{
			name:       "later attempt refuses an unknown state namespace",
			ref:        "alg-state-v9.AAAAAAAAAAAAAAAA",
			attempt:    2,
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: reasonInvalidProgress,
			wantCalls:  0,
		},
		{
			name: "later attempt refuses a corrupt legacy token",
			ref:  legacyStateTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte{2, 0, 0, 0, 0, 1, 0, 0, 0, 1, 0}),
			// A same-namespace token with an unknown version is not readable
			// progress state and must not be treated as a fresh baseline.
			attempt:    2,
			wantKind:   terminaldecision.DecisionAllowStop,
			wantReason: reasonInvalidProgress,
			wantCalls:  0,
		},
		{
			// Pre-spec behavior: a first candidate's lineage reference predates
			// the opaque state token, so it is not prior progress state at all.
			name:       "first candidate tolerates a foreign reference as pre-continuation lineage",
			ref:        preferredToken,
			attempt:    1,
			wantKind:   terminaldecision.DecisionContinue,
			wantReason: progress.ReasonUnfinished,
			wantCalls:  1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			observe := func(source legacyConfigSource) legacyCaseObservation {
				collector := &legacyMatrixCollector{response: `{"kind":"INCOMPLETE","objective":"must not be used"}`}
				in := semanticProviderInput()
				in.Continuation.Attempt = tc.attempt
				in.Evidence.Lineage.Attempt = tc.attempt
				in.Evidence.Lineage.ProgressRef = tc.ref
				in.Auxiliary = collector
				decision, err := NewProvider(decodeLegacyYAML(t, legacyEnabledYAML(source, ""))).Decide(context.Background(), in)
				require.NoError(t, err)
				require.NoError(t, decision.Validate())
				return legacyCaseObservation{decision: decision, calls: collector.calls}
			}

			old := observe(legacyConfigOmittedStrategy)
			explicit := observe(legacyConfigExplicitStrategy)
			for label, obs := range map[string]legacyCaseObservation{
				string(legacyConfigOmittedStrategy):  old,
				string(legacyConfigExplicitStrategy): explicit,
			} {
				assert.Equal(t, tc.wantKind, obs.decision.Kind, label)
				assert.Equal(t, tc.wantReason, obs.decision.ReasonCode, label)
				assert.Equal(t, tc.wantCalls, obs.calls, label)
			}
			assert.Equal(t, old.decision, explicit.decision)
			assert.Equal(t, old.calls, explicit.calls)
			assertLegacyStateNamespace(t, old.decision)
			assertLegacyStateNamespace(t, explicit.decision)
			if tc.wantKind == terminaldecision.DecisionContinue {
				assert.NotEqual(t, tc.ref, old.decision.Continue.ControlRef,
					"a tolerated foreign reference must never be echoed into a legacy continuation")
				assert.NotEqual(t, tc.ref, explicit.decision.Continue.ControlRef)
			}
		})
	}
}

func TestLegacyEnabledSnapshotPinsProviderAndSurvivesInvalidCandidate(t *testing.T) {
	t.Parallel()

	first, err := composeLegacyALGBundle(t, legacyEnabledYAML(legacyConfigOmittedStrategy, "max_semantic_continuations: 2\nno_progress_limit: 64\n"))
	require.NoError(t, err)
	require.Equal(t, []string{aipfeature.PlaneTerminalDecisionProvider.ID}, occupiedStandardPlanes(t, first.PlaneSet))
	firstProv := aipfeature.Get(first.PlaneSet, aipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, firstProv)

	second, err := composeLegacyALGBundle(t, legacyEnabledYAML(legacyConfigOmittedStrategy, "max_semantic_continuations: 4\nno_progress_limit: 64\n"))
	require.NoError(t, err)
	secondProv := aipfeature.Get(second.PlaneSet, aipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, secondProv)

	assertProviderTripsSemanticCap(t, firstProv, 2)
	assertProviderTripsSemanticCap(t, secondProv, 4)

	cloned := first.PlaneSet.Clone()
	fromClone := aipfeature.Get(cloned, aipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, fromClone)
	assertProviderTripsSemanticCap(t, fromClone, 2)

	_, err = composeLegacyALGBundle(t, "enabled: true\nmax_semantic_continuations: 0\n")
	require.Error(t, err)

	stillFirst := aipfeature.Get(first.PlaneSet, aipfeature.PlaneTerminalDecisionProvider)
	stillSecond := aipfeature.Get(second.PlaneSet, aipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, stillFirst)
	require.NotNil(t, stillSecond)
	assertProviderTripsSemanticCap(t, stillFirst, 2)
	assertProviderTripsSemanticCap(t, stillSecond, 4)
}

func TestLegacyDisabledAndEmptyPlanesContributeNoProvider(t *testing.T) {
	t.Parallel()

	disabled, err := composeLegacyALGBundle(t, "enabled: false\n")
	require.NoError(t, err)
	assert.Empty(t, occupiedStandardPlanes(t, disabled.PlaneSet))
	assert.Nil(t, aipfeature.Get(disabled.PlaneSet, aipfeature.PlaneTerminalDecisionProvider))

	empty := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}
	assert.Nil(t, aipfeature.Get(empty.PlaneSet, aipfeature.PlaneTerminalDecisionProvider))
}

// legacyCaseObservation is everything one certified case observably produces:
// the decision, the retained detached-verifier request facts, and the
// configured bound the verifier deadline must match. The old-vs-explicit parity
// comparison consumes this value, and the certified expectations are asserted
// against the same value, so parity is never established by comparing two
// decisions that share no independent baseline.
type legacyCaseObservation struct {
	decision        terminaldecision.Decision
	calls           int
	request         auxiliary.Request
	hadDeadline     bool
	deadlineRemain  time.Duration
	verifierTimeout time.Duration
}

func runLegacyAcceptanceCase(t *testing.T, source legacyConfigSource, tc legacyAcceptanceCase) {
	t.Helper()

	assertLegacyCaseExpectation(t, tc, observeLegacyAcceptanceCase(t, source, tc))
}

func observeLegacyAcceptanceCase(t *testing.T, source legacyConfigSource, tc legacyAcceptanceCase) legacyCaseObservation {
	t.Helper()

	return observeLegacyConfig(t, decodeLegacyYAML(t, legacyEnabledYAML(source, tc.yamlExtra)), tc)
}

// legacyProgrammaticConfig rebuilds the pre-spec programmatic struct literal for
// a case: the decoded legacy settings with every field the strategy dispatch
// introduced absent. This is the only shape that still reaches the historical
// NewProvider partial-default construction path, so comparing it with the
// explicit selector is not the same function call twice.
func legacyProgrammaticConfig(t *testing.T, tc legacyAcceptanceCase) Config {
	t.Helper()

	cfg := decodeLegacyYAML(t, legacyEnabledYAML(legacyConfigOmittedStrategy, tc.yamlExtra))
	cfg.Strategy = ""
	cfg.MaxProtocolReprompts = 0
	return cfg
}

func observeLegacyConfig(t *testing.T, cfg Config, tc legacyAcceptanceCase) legacyCaseObservation {
	t.Helper()

	prov := NewProvider(cfg)
	cause := tc.cause
	if cause == "" {
		cause = terminaldecision.CandidateCauseNormal
	}
	in := semanticProviderInput()
	in.Candidate.Cause = cause
	in.Evidence.ExplicitCompletion = tc.explicit
	if tc.uncommitted {
		in.Candidate.OutputCommitted = false
	}
	if tc.mutate != nil {
		tc.mutate(&in)
	}

	var collector *legacyMatrixCollector
	if !tc.omitAux {
		collector = &legacyMatrixCollector{response: tc.response, err: tc.auxErr}
		in.Auxiliary = collector
	}

	decision, err := prov.Decide(context.Background(), in)
	require.NoError(t, err)
	require.NoError(t, decision.Validate())

	obs := legacyCaseObservation{decision: decision, verifierTimeout: cfg.VerifierTimeout}
	if collector != nil {
		obs.calls = collector.calls
		obs.request = collector.req
		obs.hadDeadline = collector.hadDeadline
		obs.deadlineRemain = collector.deadlineRemain
	}
	return obs
}

func assertLegacyCaseExpectation(t *testing.T, tc legacyAcceptanceCase, obs legacyCaseObservation) {
	t.Helper()

	decision := obs.decision
	assert.Equal(t, tc.wantKind, decision.Kind)
	assert.Equal(t, tc.wantReason, decision.ReasonCode)
	if tc.wantContinue {
		require.NotNil(t, decision.Continue)
		require.NoError(t, decision.Continue.Validate())
		assert.Equal(t, terminaldecision.DecisionContinue, decision.Kind)
	} else {
		assert.Nil(t, decision.Continue)
	}

	assert.Equal(t, tc.wantCalls, obs.calls)
	if tc.wantVerifier {
		assert.True(t, obs.hadDeadline, "current enabled path must bound the verifier with a deadline")
		assert.InDelta(t, obs.verifierTimeout.Seconds(), obs.deadlineRemain.Seconds(), 1.0,
			"Collect deadline must be VerifierTimeout, not the platform Input.Deadline")
		assert.Equal(t, DefaultVerifierRole, obs.request.Role)
		assert.Equal(t, verifier.VisibilityPrivate, obs.request.Visibility)
		assert.Equal(t, auxiliary.SessionModeDetached, obs.request.SessionMode)
		require.Equal(t, []string{verifier.RecursionPluginID}, obs.request.DisablePlugins)
		// The auxiliary lineage facts that keep legacy verifier usage and trace
		// separately attributable must be retained unchanged.
		assert.Equal(t, "trace-1", obs.request.ParentTraceID)
		assert.Equal(t, "a-leg-1", obs.request.ParentALegID)
		assert.Equal(t, "b-leg-1", obs.request.ParentBLegID)
	}
}

// assertLegacyStateNamespace pins the legacy continuation's placement and state
// facts: a verifier-backed continuation keeps the generic internal-control
// intent, the canonical trajectory it continues, and the pre-spec opaque state
// token, which is readable by the legacy codec and by nothing else.
func assertLegacyStateNamespace(t *testing.T, decision terminaldecision.Decision) {
	t.Helper()

	if decision.Kind != terminaldecision.DecisionContinue {
		assert.Nil(t, decision.Continue)
		return
	}
	intent := decision.Continue
	require.NotNil(t, intent)
	assert.Equal(t, "internal-control", intent.Provenance, "the legacy recovery instruction stays platform-internal")
	assert.Equal(t, "trajectory-1", intent.TrajectoryRef, "the legacy continuation keeps the retained canonical trajectory")
	assert.Equal(t, progress.ReasonUnfinished, intent.ReasonCode)
	assert.Equal(t, decision.ReasonCode, intent.ReasonCode)
	assert.Contains(t, intent.Instruction, "<automated-recovery>")

	state, err := progress.DecodeState(intent.ControlRef)
	require.NoError(t, err, "a legacy continuation must carry a decodable legacy state token")
	assert.Equal(t, 1, state.TotalAttempts, "the first continuation records one observed attempt")
	assert.True(t, state.HasBaseline)
	assert.False(t, state.NoProgressTripped)
	assert.False(t, state.BudgetExhausted)
	assert.False(t, state.Terminal)
	assert.NotEmpty(t, state.LastFingerprint, "progress state stays a digest of canonical evidence, not the evidence itself")

	assert.True(t, strings.HasPrefix(intent.ControlRef, legacyStateTokenPrefix),
		"the legacy strategy must keep the pre-spec state namespace, got %q", intent.ControlRef)
	assert.NotContains(t, intent.ControlRef, protocolstate.TokenPrefix)
	_, err = protocolstate.Decode(intent.ControlRef)
	assert.Error(t, err, "a legacy state token must never decode as preferred protocol state")
}

func assertProviderTripsSemanticCap(t *testing.T, prov terminaldecision.Provider, cap uint8) {
	t.Helper()
	require.Greater(t, cap, uint8(1))

	in := semanticProviderInput()
	in.Policy.MaxContinuationAttempts = 8
	responses := make([]string, int(cap)+1)
	for i := range responses {
		responses[i] = `{"kind":"INCOMPLETE","objective":"resume tests"}`
	}
	in.Auxiliary = &providerSemanticCollector{responses: responses}

	var prev terminaldecision.Decision
	for attempt := uint8(1); attempt < cap; attempt++ {
		if attempt > 1 {
			in.Continuation.Attempt = attempt
			in.Evidence.Lineage.Attempt = attempt
			require.NotNil(t, prev.Continue)
			in.Evidence.Lineage.ProgressRef = prev.Continue.ControlRef
		}
		decision := mustDecide(t, prov, in)
		require.Equal(t, terminaldecision.DecisionContinue, decision.Kind, "attempt %d under cap %d should continue", attempt, cap)
		require.NotNil(t, decision.Continue)
		prev = decision
	}

	in.Continuation.Attempt = cap
	in.Evidence.Lineage.Attempt = cap
	require.NotNil(t, prev.Continue)
	in.Evidence.Lineage.ProgressRef = prev.Continue.ControlRef
	last := mustDecide(t, prov, in)
	assert.Equal(t, terminaldecision.DecisionAllowStop, last.Kind)
	assert.Equal(t, progress.ReasonBudgetExhausted, last.ReasonCode)
	assert.Nil(t, last.Continue)
}

func mustDecide(t *testing.T, prov terminaldecision.Provider, in terminaldecision.Input) terminaldecision.Decision {
	t.Helper()
	decision, err := prov.Decide(context.Background(), in)
	require.NoError(t, err)
	require.NoError(t, decision.Validate())
	return decision
}

func decodeLegacyYAML(t *testing.T, raw string) Config {
	t.Helper()
	cfg, err := decodeLegacyYAMLErr(t, raw)
	require.NoError(t, err)
	return cfg
}

func decodeLegacyYAMLErr(t *testing.T, raw string) (Config, error) {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		return Config{}, err
	}
	return DecodeConfig(node)
}

func composeLegacyALGBundle(t *testing.T, raw string) (aipfeature.FeatureBundle, error) {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cfg, err := DecodeConfig(node)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	if !cfg.Enabled {
		return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}, nil
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, ID, NewProvider(cfg)); err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
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
