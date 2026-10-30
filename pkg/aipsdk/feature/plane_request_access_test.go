package feature_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// expectedPlaneRequestAccess pins the Task 3.1 initial truthful request-body
// access classification for all 28 production planes. Values derive from the
// Task 1.8 Call/authority census and the Task 1.9 plane census evidence:
// content-receiving or request-mutating planes are CanonicalRequired (erring
// canonical-required when uncertain); response-event-only planes are
// ResponseOnly; bounded session/workspace/int-knob planes are MetadataOnly.
// No plane carries a wire contract yet; WireContract is reserved for a
// separately certified contract (group 3.5+).
var expectedPlaneRequestAccess = map[string]feature.RequestBodyAccess{
	"submit_hooks":                          feature.RequestBodyCanonicalRequired,
	"request_part_hooks":                    feature.RequestBodyCanonicalRequired,
	"response_part_hooks":                   feature.RequestBodyResponseOnly,
	"tool_reactors":                         feature.RequestBodyCanonicalRequired,
	"session_openers":                       feature.RequestBodyMetadataOnly,
	"workspace_resolvers":                   feature.RequestBodyMetadataOnly,
	"tool_catalog_filters":                  feature.RequestBodyCanonicalRequired,
	"tool_call_policies":                    feature.RequestBodyCanonicalRequired,
	"tool_call_finalizers":                  feature.RequestBodyCanonicalRequired,
	"tool_call_finalization_max_args_bytes": feature.RequestBodyMetadataOnly,
	"request_transforms":                    feature.RequestBodyCanonicalRequired,
	"pre_request_handlers":                  feature.RequestBodyCanonicalRequired,
	"route_hint_providers":                  feature.RequestBodyCanonicalRequired,
	"completion_gates":                      feature.RequestBodyResponseOnly,
	"attempt_transforms":                    feature.RequestBodyCanonicalRequired,
	"stream_observer_factories":             feature.RequestBodyResponseOnly,
	"traffic_observers":                     feature.RequestBodyCanonicalRequired,
	"usage_observers":                       feature.RequestBodyResponseOnly,
	"raw_capture_sinks":                     feature.RequestBodyCanonicalRequired,
	"traffic_redactors":                     feature.RequestBodyCanonicalRequired,
	"compaction_observers":                  feature.RequestBodyCanonicalRequired,
	"compaction_preservers":                 feature.RequestBodyCanonicalRequired,
	"secret_guards":                         feature.RequestBodyCanonicalRequired,
	"secret_guard_execution":                feature.RequestBodyCanonicalRequired,
	"local_turn_handlers":                   feature.RequestBodyCanonicalRequired,
	"terminal_decision_provider":            feature.RequestBodyCanonicalRequired,
	"session_classifier":                    feature.RequestBodyMetadataOnly,
	"control_tool_provider":                 feature.RequestBodyCanonicalRequired,
}

// TestPlaneRequestAccess_ZeroValueIsUnclassified pins that the zero value is
// Unclassified so an unannotated plane fails closed at generation/CI.
func TestPlaneRequestAccess_ZeroValueIsUnclassified(t *testing.T) {
	t.Parallel()

	var zero feature.RequestBodyAccess
	assert.Equal(t, feature.RequestBodyAccessUnclassified, zero)
}

// TestPlaneRequestAccess_StringMethods pins the string rendering of every
// access class plus the unknown-value fallback.
func TestPlaneRequestAccess_StringMethods(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "unclassified", feature.RequestBodyAccessUnclassified.String())
	assert.Equal(t, "canonical_required", feature.RequestBodyCanonicalRequired.String())
	assert.Equal(t, "metadata_only", feature.RequestBodyMetadataOnly.String())
	assert.Equal(t, "response_only", feature.RequestBodyResponseOnly.String())
	assert.Equal(t, "wire_contract", feature.RequestBodyWireContract.String())
	assert.Equal(t, "RequestBodyAccess(99)", feature.RequestBodyAccess(99).String())
}

// TestPlaneRequestAccess_AllStandardPlanesAnnotated verifies that every
// production plane declares a non-Unclassified request access class and that
// the annotated manifest still validates.
func TestPlaneRequestAccess_AllStandardPlanesAnnotated(t *testing.T) {
	t.Parallel()

	require.Len(t, feature.StandardPlanes, 28, "manifest must declare exactly 28 standard planes")

	for _, decl := range feature.StandardPlanes {
		access := feature.DeclaredRequestAccessForTest(decl)
		assert.NotEqual(t, feature.RequestBodyAccessUnclassified, access,
			"plane %s must declare a non-Unclassified request access class", decl.PlaneID())
		require.NoError(t, decl.ValidateDeclaration(),
			"plane %s must pass declaration validation", decl.PlaneID())
	}

	require.NoError(t, feature.ValidateManifest(feature.StandardPlanes...),
		"annotated StandardPlanes must pass manifest validation")
}

// TestPlaneRequestAccess_InitialClassificationMatchesCensus verifies the exact
// initial per-plane classification from actual plane semantics (Task 1.8/1.9
// evidence). A new production plane fails here until it is classified.
func TestPlaneRequestAccess_InitialClassificationMatchesCensus(t *testing.T) {
	t.Parallel()

	require.Len(t, feature.StandardPlanes, len(expectedPlaneRequestAccess),
		"classification table drifted from StandardPlanes")

	for _, decl := range feature.StandardPlanes {
		expected, classified := expectedPlaneRequestAccess[decl.PlaneID()]
		require.True(t, classified,
			"plane %s has no request access classification: classify it first", decl.PlaneID())
		assert.Equal(t, expected, feature.DeclaredRequestAccessForTest(decl),
			"plane %s has unexpected request access classification", decl.PlaneID())
	}
}

// TestPlaneRequestAccess_OutOfRangeValueRejected verifies that an unknown
// access class value fails declaration validation.
func TestPlaneRequestAccess_OutOfRangeValueRejected(t *testing.T) {
	t.Parallel()

	plane := feature.Plane[[]string]{
		ID:           "test.out_of_range_access",
		Multiplicity: feature.MultOrdered,
		Rules:        feature.SourceRules{Feature: feature.CombConcatenate},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
		RequestAccess: feature.RequestBodyAccess(99),
	}
	err := plane.ValidateDeclaration()
	require.Error(t, err)
	assert.ErrorIs(t, err, feature.ErrInvalidPlane)
	assert.Contains(t, err.Error(), "invalid request access class")
}
