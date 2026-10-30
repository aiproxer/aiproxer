package feature_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corehooks "github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// largePayload19ExpectedPlaneCount pins the Task 1.9 census with both the
// metadata-only session classifier plane and the canonical-required control
// tool provider plane added. Any added, removed, or renamed plane fails this
// ratchet until the census table below and evidence/1.9-plane-census.md are
// updated together.
const largePayload19ExpectedPlaneCount = 28

// largePayload19Posture pins the Task 1.9 initial V1 wire posture per plane.
// Labels mirror requirement 5.2 access classes (CanonicalRequired,
// MetadataOnly, ResponseOnly, WireContract). "Unclassified" is deliberately
// absent: an unclassified plane must fail closed, never ship as a silent
// default. Occupied == blocker applies when the frozen generation holds a
// value; nil/uncontributed planes are no-op facts for static disposition.
var largePayload19Posture = map[string]string{
	"submit_hooks":                          "canonical-required-when-occupied",
	"request_part_hooks":                    "canonical-required-when-occupied",
	"response_part_hooks":                   "response-only",
	"tool_reactors":                         "canonical-required-when-occupied",
	"session_openers":                       "metadata-only",
	"workspace_resolvers":                   "metadata-only",
	"tool_catalog_filters":                  "canonical-required-when-occupied",
	"tool_call_policies":                    "canonical-required-when-occupied",
	"tool_call_finalizers":                  "canonical-required-when-occupied",
	"tool_call_finalization_max_args_bytes": "metadata-only",
	"request_transforms":                    "canonical-required-when-occupied",
	"pre_request_handlers":                  "canonical-required-when-occupied",
	"route_hint_providers":                  "canonical-required-when-occupied",
	"completion_gates":                      "response-only",
	"attempt_transforms":                    "canonical-required-when-occupied",
	"stream_observer_factories":             "response-only",
	"traffic_observers":                     "canonical-required-when-occupied",
	"usage_observers":                       "response-only",
	"raw_capture_sinks":                     "canonical-required-when-occupied",
	"traffic_redactors":                     "canonical-required-when-occupied",
	"compaction_observers":                  "canonical-required-when-occupied",
	"compaction_preservers":                 "canonical-required-when-occupied",
	"secret_guards":                         "canonical-required-when-occupied",
	"secret_guard_execution":                "canonical-required-when-occupied",
	"local_turn_handlers":                   "canonical-required-when-occupied",
	"terminal_decision_provider":            "canonical-required-when-occupied",
	"session_classifier":                    "metadata-only",
	"control_tool_provider":                 "canonical-required-when-occupied",
}

// TestLargePayload19_PlaneCensusFrozen27 enumerates the census
// programmatically from feature.StandardPlanes (never a hand-copied list as
// source of truth) and fails closed on any new, removed, renamed, or
// unclassified plane.
func TestLargePayload19_PlaneCensusFrozen27(t *testing.T) {
	t.Parallel()

	planes := feature.StandardPlanes
	require.Len(t, planes, largePayload19ExpectedPlaneCount,
		"standard plane count changed: update the census table and evidence/1.9-plane-census.md together")

	require.NoError(t, feature.ValidateManifest(planes...),
		"StandardPlanes must pass manifest validation")

	seen := make(map[string]struct{}, len(planes))
	for _, decl := range planes {
		id := decl.PlaneID()
		t.Logf("plane: %s", id)
		_, dup := seen[id]
		require.False(t, dup, "duplicate plane ID in StandardPlanes: %s", id)
		seen[id] = struct{}{}
		require.NoError(t, decl.ValidateDeclaration(),
			"plane %s must pass declaration validation", id)
		posture, classified := largePayload19Posture[id]
		require.True(t, classified,
			"plane %s has no Task 1.9 wire classification: fail closed, classify it first", id)
		require.NotEmpty(t, posture, "plane %s has an empty wire posture", id)
	}

	// The census table must not carry stale entries for planes that no longer
	// exist (rename/removal fails closed in both directions).
	require.Len(t, largePayload19Posture, len(planes),
		"census table drifted from StandardPlanes: remove stale entries")

	// Requirement 5.4 named planes must be members of the frozen census.
	for _, id := range []string{
		feature.PlaneSecretGuardExecution.PlaneID(),
		feature.PlaneLocalTurnHandlers.PlaneID(),
		feature.PlaneTerminalDecisionProvider.PlaneID(),
		feature.PlaneControlToolProvider.PlaneID(),
	} {
		_, ok := seen[id]
		require.True(t, ok, "required plane %s missing from StandardPlanes", id)
	}
	require.Equal(t, "secret_guard_execution", feature.PlaneSecretGuardExecution.PlaneID())
	require.Equal(t, "local_turn_handlers", feature.PlaneLocalTurnHandlers.PlaneID())
	require.Equal(t, "terminal_decision_provider", feature.PlaneTerminalDecisionProvider.PlaneID())
	require.Equal(t, "control_tool_provider", feature.PlaneControlToolProvider.PlaneID())
}

// TestLargePayload19_NamedPlanesOccupiedSemantics pins the requirement 5.4
// source rules and the occupied/unoccupied signal the Task 3 static
// disposition will read: occupied Local Turn and Secret Guard execution are
// canonical blockers; an occupied Terminal Decision provider is a blocker
// unless Task 12 closes evidence + continuation-source parity.
func TestLargePayload19_NamedPlanesOccupiedSemantics(t *testing.T) {
	t.Parallel()

	// SecretGuardExecution is binder-only: no feature/host source may supply
	// execution posture, so an occupied value always means the standard
	// distribution composed engine posture for the generation.
	require.Equal(t, feature.MultExclusive, feature.PlaneSecretGuardExecution.Multiplicity)
	require.Equal(t, feature.CombUnsupported, feature.PlaneSecretGuardExecution.Rules.Feature)
	require.Equal(t, feature.CombUnsupported, feature.PlaneSecretGuardExecution.Rules.Host)
	require.Equal(t, feature.CombExclusive, feature.PlaneSecretGuardExecution.Rules.GenerationBinder)

	// LocalTurnHandlers receive the full ingress Call in Match and Handle and
	// may short-circuit the request: any occupant blocks the wire lane in V1.
	require.Equal(t, feature.MultOrdered, feature.PlaneLocalTurnHandlers.Multiplicity)
	require.Equal(t, feature.CombConcatenate, feature.PlaneLocalTurnHandlers.Rules.Feature)

	// TerminalDecisionProvider takes a bounded SDK Input, but DecisionContinue
	// can require trajectory/continuation state: occupied blocks V1.
	require.Equal(t, feature.MultExclusive, feature.PlaneTerminalDecisionProvider.Multiplicity)
	require.Equal(t, feature.CombExclusive, feature.PlaneTerminalDecisionProvider.Rules.Feature)
	require.Equal(t, feature.ErrTerminalDecisionProviderConflict,
		feature.PlaneTerminalDecisionProvider.ExclusiveConflictError)

	// Unoccupied generation: all three signals read empty (static-disposition
	// no-op facts, not blockers).
	emptyFrozen := feature.NewContributionSet().Freeze()
	require.Nil(t, feature.Get(emptyFrozen, feature.PlaneSecretGuardExecution))
	require.Empty(t, feature.Get(emptyFrozen, feature.PlaneLocalTurnHandlers))
	require.True(t, feature.Get(emptyFrozen, feature.PlaneTerminalDecisionProvider) == nil)

	// Occupied generation: each signal reads back the composed occupant.
	occupied := feature.NewContributionSet()
	require.NoError(t, feature.ContributeSource(occupied, feature.PlaneSecretGuardExecution,
		feature.SourceGenerationBinder, "secret-guard-execution",
		&secretguard.ExecutionConfig{AccessMode: "single_user", ConfigVersion: "v1"}))
	require.NoError(t, feature.Contribute(occupied, feature.PlaneLocalTurnHandlers,
		"plugin-local-turn", []localturn.Handler{freezeTestLocalTurnHandler{id: "lt-1", ord: 10}}))
	require.NoError(t, feature.Contribute(occupied, feature.PlaneTerminalDecisionProvider,
		"plugin-terminal", terminaldecision.Provider(dummyTerminalProvider{id: "term-1"})))
	frozen := occupied.Freeze()

	execCfg := feature.Get(frozen, feature.PlaneSecretGuardExecution)
	require.NotNil(t, execCfg, "occupied secret_guard_execution must read back")
	assert.Equal(t, "single_user", execCfg.AccessMode)

	handlers := feature.Get(frozen, feature.PlaneLocalTurnHandlers)
	require.Len(t, handlers, 1, "occupied local_turn_handlers must read back")
	assert.Equal(t, "lt-1", handlers[0].ID())

	provider := feature.Get(frozen, feature.PlaneTerminalDecisionProvider)
	require.NotNil(t, provider, "occupied terminal_decision_provider must read back")
	providerID, hasID := feature.FrozenIdentity(frozen, feature.PlaneTerminalDecisionProvider)
	require.True(t, hasID, "occupied terminal provider must carry a frozen identity")
	assert.Equal(t, "term-1", providerID)
}

// TestLargePayload19_HookBusInventorySeparate inventories hooks.Bus chains
// independently of the typed-plane manifest (requirement 5.7): occupied
// submit/request-part/tool chains that can inspect or mutate the canonical
// request are canonical-required unless given an explicit typed wire
// contract; response-only chains may remain active when proven response-only.
func TestLargePayload19_HookBusInventorySeparate(t *testing.T) {
	t.Parallel()

	// Exactly the four canonical hook planes project into HookConfig; no other
	// standard plane carries a HookTarget annotation.
	hookPlaneIDs := map[string]feature.HookTarget{}
	for _, decl := range feature.StandardPlanes {
		if target := feature.DeclaredHookTargetForTest(decl); target != "" {
			hookPlaneIDs[decl.PlaneID()] = target
		}
	}
	require.Equal(t, map[string]feature.HookTarget{
		"submit_hooks":        feature.HookTargetSubmitHooks,
		"request_part_hooks":  feature.HookTargetRequestPartHooks,
		"response_part_hooks": feature.HookTargetResponsePartHooks,
		"tool_reactors":       feature.HookTargetToolReactors,
	}, hookPlaneIDs, "hook-plane projection must stay exactly four chains")

	// Occupied chains project 1:1 through the generated HookConfig into the
	// core hook bus: submit Handle(*Call) can reject/mutate pre-route,
	// RequestPartHook HandleRequestParts(*Call) mutates request parts, and
	// ToolReactor HandleToolEvent rewrites tool lifecycle events, while
	// ResponsePartHook HandleEvent(*Event) touches response events only.
	cs := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(cs, feature.PlaneSubmitHooks,
		"plugin-hooks", []hooks.SubmitHook{dummySubmitHook{id: "sh-1", ord: 10}}))
	require.NoError(t, feature.Contribute(cs, feature.PlaneRequestPartHooks,
		"plugin-hooks", []hooks.RequestPartHook{dummyRequestPartHook{id: "rqh-1", ord: 20}}))
	require.NoError(t, feature.Contribute(cs, feature.PlaneResponsePartHooks,
		"plugin-hooks", []hooks.ResponsePartHook{dummyResponsePartHook{id: "rsh-1", ord: 30}}))
	require.NoError(t, feature.Contribute(cs, feature.PlaneToolReactors,
		"plugin-hooks", []hooks.ToolReactor{dummyToolReactor{id: "tr-1", ord: 40}}))
	cfg := feature.ProjectHookConfig(cs.Freeze(), hooks.ToolReactorErrorsFailClosed)
	require.Len(t, cfg.SubmitHooks, 1)
	require.Len(t, cfg.RequestPartHooks, 1)
	require.Len(t, cfg.ResponsePartHooks, 1)
	require.Len(t, cfg.ToolReactors, 1)

	bus := corehooks.New(cfg)
	submit, requestParts, responseParts, tools := bus.HookChainLengths()
	require.Equal(t, 1, submit, "bus submit chain must carry the occupied submit hook")
	require.Equal(t, 1, requestParts, "bus request-part chain must carry the occupied request hook")
	require.Equal(t, 1, responseParts, "bus response-part chain must carry the occupied response hook")
	require.Equal(t, 1, tools, "bus tool chain must carry the occupied tool reactor")
}

// TestLargePayload19_GeneratedManifestParity pins manifest/generated parity
// for the census: every StandardPlanes entry must have exactly one canonical
// generated policy and one generated access binding. Generator currency
// itself (-check) is enforced by TestGenerator_DeterministicTwoRunsByteIdentical
// in this same package.
func TestLargePayload19_GeneratedManifestParity(t *testing.T) {
	t.Parallel()

	repoRoot := findRepoRoot(t)
	genBytes, err := os.ReadFile(filepath.Join(repoRoot, "pkg", "aipsdk", "feature", "plane_generated.go"))
	require.NoError(t, err)
	gen := string(genBytes)

	require.Contains(t, gen, "Code generated by scripts/generate-feature-planes.go. DO NOT EDIT.")
	require.Equal(t, len(feature.StandardPlanes), strings.Count(gen, "= &generatedPolicy["),
		"generated policy bindings must match StandardPlanes cardinality")
	require.Equal(t, len(feature.StandardPlanes), strings.Count(gen, "= generatedAccess["),
		"generated access bindings must match StandardPlanes cardinality")
}
