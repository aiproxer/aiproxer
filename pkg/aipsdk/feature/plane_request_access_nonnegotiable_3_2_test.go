package feature_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// Task 3.2 non-negotiable initial classifications (Requirements 5, 13, 19;
// Design section 7). Classification-hardening on top of the Task 3.1 truthful
// values: each rule below pins a starting posture that later tasks must not
// silently weaken. Occupied/active means the frozen generation holds a value;
// nil/uncontributed planes are no-op facts for the Task 3.5 summary and the
// Task 3.6 static disposition, which consume (but are not built by) this task.

// TestPlaneRequestAccess_NonNegotiable_LocalTurnOccupiedBlocks pins that an
// occupied PlaneLocalTurnHandlers is canonical-required in V1. Match and
// Handle receive a full aipapi.Call and may short-circuit the request
// (pkg/aipsdk/localturn/types.go Match/Handle; Task 1.8 section 4.8-8.1).
// Requirements 5.4, 13.4; design section 7 table.
func TestPlaneRequestAccess_NonNegotiable_LocalTurnOccupiedBlocks(t *testing.T) {
	t.Parallel()

	assert.Equal(t, feature.RequestBodyCanonicalRequired, feature.PlaneLocalTurnHandlers.RequestAccess,
		"occupied local_turn_handlers must stay canonical-required: Match/Handle take the full Call and may claim the turn")
	assert.Equal(t, feature.RequestBodyCanonicalRequired,
		feature.DeclaredRequestAccessForTest(feature.PlaneLocalTurnHandlers),
		"manifest declaration for local_turn_handlers must stay canonical-required")
}

// TestPlaneRequestAccess_NonNegotiable_SecretGuardBlocks pins that active
// Secret Guard execution and guards are canonical-required until a separately
// certified streaming guard contract preserves matching, quarantine, audit,
// and denial semantics. Guard.Evaluate takes the full Call
// (pkg/aipsdk/secretguard/types.go Evaluate; Task 1.8 section 4.8-8.2).
// Requirements 5.4, 13.3, 13.4; design section 7 table.
func TestPlaneRequestAccess_NonNegotiable_SecretGuardBlocks(t *testing.T) {
	t.Parallel()

	assert.Equal(t, feature.RequestBodyCanonicalRequired, feature.PlaneSecretGuards.RequestAccess,
		"occupied secret_guards must stay canonical-required until a streaming guard contract is certified")
	assert.Equal(t, feature.RequestBodyCanonicalRequired, feature.PlaneSecretGuardExecution.RequestAccess,
		"active secret_guard_execution must stay canonical-required until a streaming guard contract is certified")
	assert.Equal(t, feature.RequestBodyCanonicalRequired,
		feature.DeclaredRequestAccessForTest(feature.PlaneSecretGuards))
	assert.Equal(t, feature.RequestBodyCanonicalRequired,
		feature.DeclaredRequestAccessForTest(feature.PlaneSecretGuardExecution))
}

// TestPlaneRequestAccess_NonNegotiable_TerminalDecisionBlocksPendingTask12
// pins that an occupied PlaneTerminalDecisionProvider is canonical-required
// unless Task 12 implements bounded terminal evidence plus
// continuation-source parity. A bounded SDK Input alone is not sufficient
// proof because DecisionContinue can require trajectory state, and the
// current evidence producer is content-shaped (Task 1.8 section 4.4-4.4).
// Do NOT reclassify response-only merely because the SDK input is bounded.
// Requirements 5.4, 13.5; design section 7 table; Task 12.5 escape hatch only.
func TestPlaneRequestAccess_NonNegotiable_TerminalDecisionBlocksPendingTask12(t *testing.T) {
	t.Parallel()

	assert.Equal(t, feature.RequestBodyCanonicalRequired, feature.PlaneTerminalDecisionProvider.RequestAccess,
		"occupied terminal_decision_provider must stay canonical-required pending Task 12 evidence + continuation parity")
	access := feature.DeclaredRequestAccessForTest(feature.PlaneTerminalDecisionProvider)
	assert.Equal(t, feature.RequestBodyCanonicalRequired, access)
	assert.NotEqual(t, feature.RequestBodyResponseOnly, access,
		"terminal_decision_provider must not be response-only merely because SDK Input is bounded: DecisionContinue can require trajectory state")
	assert.NotEqual(t, feature.RequestBodyMetadataOnly, access,
		"terminal_decision_provider must not be metadata-only without Task 12 parity")
	assert.NotEqual(t, feature.RequestBodyWireContract, access,
		"terminal_decision_provider must not carry a wire contract without Task 12 parity")
	assert.NotEqual(t, feature.RequestBodyAccessUnclassified, access)
}

// TestPlaneRequestAccess_NonNegotiable_RequestMutatingHooksTransformsBlock
// pins that request-mutating hooks and transforms are canonical-required
// unless an explicit wire contract is certified. Submit/request-part/tool
// chains can inspect or mutate the canonical request (Requirement 5.7);
// request/attempt transforms and pre-request handlers take the full mutable
// Call; route-hint providers need their own bounded route-domain contract
// (Requirement 7.6). Requirements 5.7, 13.3; design section 7.
func TestPlaneRequestAccess_NonNegotiable_RequestMutatingHooksTransformsBlock(t *testing.T) {
	t.Parallel()

	mutatingPlaneIDs := []string{
		"submit_hooks",
		"request_part_hooks",
		"tool_reactors",
		"request_transforms",
		"attempt_transforms",
		"pre_request_handlers",
		"route_hint_providers",
		"tool_catalog_filters",
		"tool_call_policies",
		"tool_call_finalizers",
	}
	byID := make(map[string]feature.PlaneDeclaration, len(feature.StandardPlanes))
	for _, decl := range feature.StandardPlanes {
		byID[decl.PlaneID()] = decl
	}
	for _, id := range mutatingPlaneIDs {
		decl, ok := byID[id]
		require.True(t, ok, "mutating plane %s missing from StandardPlanes", id)
		assert.Equal(t, feature.RequestBodyCanonicalRequired,
			feature.DeclaredRequestAccessForTest(decl),
			"plane %s mutates or inspects the canonical request and must stay canonical-required without an explicit wire contract", id)
	}
}

// TestPlaneRequestAccess_NonNegotiable_TrafficCompactionBlock pins that
// capturing/observing/redacting traffic legs and content-shaped compaction
// legs are canonical-required when occupied. A provably no-op traffic bundle
// is a static-disposition fact (Tasks 3.5/3.6), not a reclassification.
// Requirement 13.1, 13.3; design section 7; Task 1.8 sections 3.8, 4.10-10.5.
func TestPlaneRequestAccess_NonNegotiable_TrafficCompactionBlock(t *testing.T) {
	t.Parallel()

	for _, id := range []string{
		"traffic_observers",
		"raw_capture_sinks",
		"traffic_redactors",
		"compaction_observers",
		"compaction_preservers",
	} {
		var found feature.PlaneDeclaration
		for _, decl := range feature.StandardPlanes {
			if decl.PlaneID() == id {
				found = decl
				break
			}
		}
		require.NotNil(t, found, "plane %s missing from StandardPlanes", id)
		assert.Equal(t, feature.RequestBodyCanonicalRequired,
			feature.DeclaredRequestAccessForTest(found),
			"plane %s must stay canonical-required when occupied", id)
	}
}

// TestPlaneRequestAccess_NonNegotiable_ResponseOnlyRequiresCharacterization
// pins the exact response-only set and its gate: these planes operate on
// canonical output events only (ResponsePartHook HandleEvent(*Event),
// completion Gate, StreamObserverFactory Observe(Event), usage Observer) and
// may remain active only when characterization proves no request-content
// dependency (Requirement 13.6). Classification alone never authorizes wire
// execution; Tasks 3.5/3.6 plus lane certification consume this gate.
func TestPlaneRequestAccess_NonNegotiable_ResponseOnlyRequiresCharacterization(t *testing.T) {
	t.Parallel()

	expectedResponseOnly := map[string]struct{}{
		"response_part_hooks":       {},
		"completion_gates":          {},
		"stream_observer_factories": {},
		"usage_observers":           {},
	}
	for _, decl := range feature.StandardPlanes {
		access := feature.DeclaredRequestAccessForTest(decl)
		_, want := expectedResponseOnly[decl.PlaneID()]
		if want {
			assert.Equal(t, feature.RequestBodyResponseOnly, access,
				"plane %s must stay response-only pending characterization evidence", decl.PlaneID())
			continue
		}
		assert.NotEqual(t, feature.RequestBodyResponseOnly, access,
			"plane %s must not become response-only without characterization proving no request-content dependency", decl.PlaneID())
	}
	require.Len(t, expectedResponseOnly, 4,
		"response-only set changed: characterization evidence must justify any addition")
}

// TestPlaneRequestAccess_NonNegotiable_MetadataOnlyRequiresBoundedParity pins
// the exact metadata-only set: bounded session/workspace/int authorities that
// may remain active only with exact bounded inputs (Requirement 13.7; design
// section 7). Any broader metadata claim needs its own parity proof.
func TestPlaneRequestAccess_NonNegotiable_MetadataOnlyRequiresBoundedParity(t *testing.T) {
	t.Parallel()

	expectedMetadataOnly := map[string]struct{}{
		"session_openers":                       {},
		"workspace_resolvers":                   {},
		"tool_call_finalization_max_args_bytes": {},
		"session_classifier":                    {},
	}
	for _, decl := range feature.StandardPlanes {
		access := feature.DeclaredRequestAccessForTest(decl)
		_, want := expectedMetadataOnly[decl.PlaneID()]
		if want {
			assert.Equal(t, feature.RequestBodyMetadataOnly, access,
				"plane %s must stay metadata-only with exact bounded-input parity", decl.PlaneID())
			continue
		}
		assert.NotEqual(t, feature.RequestBodyMetadataOnly, access,
			"plane %s must not become metadata-only without exact bounded-input parity", decl.PlaneID())
	}
}

// TestPlaneRequestAccess_NonNegotiable_NoWireContractWithoutCertification
// pins that no plane carries WireContract in V1: no streaming guard, hook,
// transform, or terminal wire contract has been separately certified yet.
// A future WireContract requires its own explicit certification change, never
// a silent reclassification. Requirements 5, 13; design section 7.
func TestPlaneRequestAccess_NonNegotiable_NoWireContractWithoutCertification(t *testing.T) {
	t.Parallel()

	for _, decl := range feature.StandardPlanes {
		assert.NotEqual(t, feature.RequestBodyWireContract,
			feature.DeclaredRequestAccessForTest(decl),
			"plane %s must not carry a wire contract without a separately certified contract", decl.PlaneID())
	}
}
