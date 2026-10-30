package planeparity

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/featurebundle"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AssertGeneratedMergeInvariants builds an independent oracle by validating and replaying
// each bundle sequentially into an independent ContributionSet using public aipfeature operations,
// and accumulating lifecycles only on success. It executes MergeBundlesGenerated and asserts exact
// value, nilness, identity, diagnostics, fail-before-mutate, and error attribution invariants.
func AssertGeneratedMergeInvariants(tb testing.TB, bundles ...aipfeature.FeatureBundle) {
	tb.Helper()

	// 1. Execute production MergeBundlesGenerated
	gen, genErr := featurebundle.MergeBundlesGenerated(bundles...)

	// 2. Build independent oracle outcome sequentially per-bundle
	oracleCS := aipfeature.NewContributionSet()
	var expectedLifecycles []aipplugin.Lifecycle

	for i, b := range bundles {
		// a. Derive exact production pluginID independently (not calling merge)
		pluginID := fmt.Sprintf("bundle-%d", i)
		if id, ok := aipfeature.FrozenIdentity(b.PlaneSet, aipfeature.PlaneTerminalDecisionProvider); ok && id != "" {
			pluginID = id
		}

		// b. Validate current bundle
		if valErr := b.Validate(); valErr != nil {
			require.Error(tb, genErr, "generated merge should fail when bundle validation fails at index %d", i)
			assert.Equal(tb, featurebundle.GeneratedMergeSurface{}, gen, "generated merge must return zero GeneratedMergeSurface on validation failure")
			assert.Nil(tb, gen.Lifecycles, "generated merge lifecycles must be nil on validation failure")

			assertValidationErrorParity(tb, genErr, valErr, pluginID)
			return
		}

		// c. Snapshot oracleCS before replay via Freeze plus diagnostics
		beforeFrozen := oracleCS.Freeze()
		beforeDiag := aipfeature.ProjectDiagnostics(beforeFrozen)

		// d. ReplayTo oracleCS
		var replayErr error
		if !b.PlaneSet.IsZero() {
			replayErr = b.PlaneSet.ReplayTo(oracleCS, pluginID)
		}

		if replayErr != nil {
			// Assert oracleCS.Freeze deep equals before (all plane Gets/identities/diagnostics)
			afterFrozen := oracleCS.Freeze()
			afterDiag := aipfeature.ProjectDiagnostics(afterFrozen)
			assert.Equal(tb, beforeDiag, afterDiag, "oracle diagnostics must not change on replay failure (fail-before-mutate)")
			assertPlaneCensus(tb, afterFrozen, beforeFrozen)

			// Assert production result zero/lifecycles nil
			require.Error(tb, genErr, "generated merge should fail on replay error at index %d", i)
			assert.Equal(tb, featurebundle.GeneratedMergeSurface{}, gen, "generated merge must return zero GeneratedMergeSurface on replay failure")
			assert.Nil(tb, gen.Lifecycles, "generated merge lifecycles must be nil on replay failure")

			assertReplayErrorParity(tb, genErr, replayErr, pluginID, bundles[:i+1])
			return
		}

		// e. Append lifecycle independently only after success preserving nil/empty and cloning
		if b.Lifecycles != nil {
			if expectedLifecycles == nil {
				expectedLifecycles = make([]aipplugin.Lifecycle, 0, len(b.Lifecycles))
			}
			expectedLifecycles = append(expectedLifecycles, slices.Clone(b.Lifecycles)...)
		}
	}

	// 3. Verify Success Path
	require.NoError(tb, genErr, "generated merge should succeed when oracle succeeds")

	oracleFrozen := oracleCS.Freeze()

	// Lifecycles side-channel invariants (exact shape/order/nilness)
	assert.Equal(tb, expectedLifecycles, gen.Lifecycles, "Lifecycles value mismatch")
	assert.Equal(tb, expectedLifecycles == nil, gen.Lifecycles == nil, "Lifecycles nilness mismatch")

	// Diagnostics projection equivalence
	assert.Equal(tb, aipfeature.ProjectDiagnostics(oracleFrozen), aipfeature.ProjectDiagnostics(gen.Frozen), "Diagnostics projection mismatch")

	// 26 Standard Plane Census
	assertPlaneCensus(tb, gen.Frozen, oracleFrozen)
}

func assertValidationErrorParity(tb testing.TB, genErr, valErr error, pluginID string) {
	tb.Helper()

	assert.Contains(tb, genErr.Error(), pluginID, "production error must contain pluginID %q", pluginID)

	sentinels := []error{
		aipfeature.ErrInvalidContribution,
		aipfeature.ErrNilContribution,
		aipfeature.ErrInvalidPlane,
		aipfeature.ErrUnsupportedSource,
		aipfeature.ErrUnsupportedReplaySource,
		aipfeature.ErrExclusiveConflict,
		aipfeature.ErrTerminalDecisionProviderConflict,
		aipfeature.ErrControlToolProviderConflict,
	}
	for _, sentinel := range sentinels {
		if errors.Is(valErr, sentinel) {
			assert.ErrorIs(tb, genErr, sentinel, "production error must wrap sentinel %v", sentinel)
		}
	}

	if expAttrErr, ok := errors.AsType[*aipfeature.AttributedError](valErr); ok {
		var genAttrErr *aipfeature.AttributedError
		if assert.ErrorAs(tb, genErr, &genAttrErr, "production error must wrap AttributedError") {
			assert.Equal(tb, expAttrErr.PlaneID, genAttrErr.PlaneID, "AttributedError plane ID mismatch")
			if expAttrErr.PluginID != "" {
				assert.Equal(tb, expAttrErr.PluginID, genAttrErr.PluginID, "AttributedError plugin ID mismatch")
			}
		}
	}
}

func assertReplayErrorParity(tb testing.TB, genErr, replayErr error, pluginID string, processedBundles []aipfeature.FeatureBundle) {
	tb.Helper()

	sentinels := []error{
		aipfeature.ErrExclusiveConflict,
		aipfeature.ErrTerminalDecisionProviderConflict,
		aipfeature.ErrControlToolProviderConflict,
		aipfeature.ErrInvalidContribution,
		aipfeature.ErrNilContribution,
		aipfeature.ErrUnsupportedSource,
		aipfeature.ErrUnsupportedReplaySource,
		aipfeature.ErrInvalidPlane,
	}
	for _, sentinel := range sentinels {
		if errors.Is(replayErr, sentinel) {
			assert.ErrorIs(tb, genErr, sentinel, "generated merge error must match sentinel %v", sentinel)
		}
	}

	if expAttrErr, ok := errors.AsType[*aipfeature.AttributedError](replayErr); ok {
		var genAttrErr *aipfeature.AttributedError
		if assert.ErrorAs(tb, genErr, &genAttrErr, "generated error must be an AttributedError") {
			assert.Equal(tb, expAttrErr.PlaneID, genAttrErr.PlaneID, "AttributedError plane ID mismatch")
			assert.Equal(tb, expAttrErr.PluginID, genAttrErr.PluginID, "AttributedError plugin ID mismatch")
		}
	}

	if errors.Is(replayErr, aipfeature.ErrExclusiveConflict) {
		var providerIDs []string
		for _, b := range processedBundles {
			if id, ok := aipfeature.FrozenIdentity(b.PlaneSet, aipfeature.PlaneTerminalDecisionProvider); ok && id != "" {
				providerIDs = append(providerIDs, id)
			}
		}
		if len(providerIDs) >= 2 {
			assert.Contains(tb, genErr.Error(), providerIDs[0], "genErr must contain first provider ID")
			assert.Contains(tb, genErr.Error(), providerIDs[len(providerIDs)-1], "genErr must contain conflicting provider ID")
		}
	}
}

func assertPlaneCensus(tb testing.TB, actual, expected aipfeature.FrozenPlaneSet) {
	tb.Helper()

	assertPlane(tb, actual, expected, aipfeature.PlaneSubmitHooks)
	assertPlane(tb, actual, expected, aipfeature.PlaneRequestPartHooks)
	assertPlane(tb, actual, expected, aipfeature.PlaneResponsePartHooks)
	assertPlane(tb, actual, expected, aipfeature.PlaneToolReactors)
	assertPlane(tb, actual, expected, aipfeature.PlaneSessionOpeners)
	assertPlane(tb, actual, expected, aipfeature.PlaneWorkspaceResolvers)
	assertPlane(tb, actual, expected, aipfeature.PlaneToolCatalogFilters)
	assertPlane(tb, actual, expected, aipfeature.PlaneToolCallPolicies)
	assertPlane(tb, actual, expected, aipfeature.PlaneToolCallFinalizers)
	assertPlane(tb, actual, expected, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	assertPlane(tb, actual, expected, aipfeature.PlaneRequestTransforms)
	assertPlane(tb, actual, expected, aipfeature.PlanePreRequestHandlers)
	assertPlane(tb, actual, expected, aipfeature.PlaneRouteHintProviders)
	assertPlane(tb, actual, expected, aipfeature.PlaneCompletionGates)
	assertPlane(tb, actual, expected, aipfeature.PlaneAttemptTransforms)
	assertPlane(tb, actual, expected, aipfeature.PlaneStreamObserverFactories)
	assertPlane(tb, actual, expected, aipfeature.PlaneTrafficObservers)
	assertPlane(tb, actual, expected, aipfeature.PlaneUsageObservers)
	assertPlane(tb, actual, expected, aipfeature.PlaneRawCaptureSinks)
	assertPlane(tb, actual, expected, aipfeature.PlaneTrafficRedactors)
	assertPlane(tb, actual, expected, aipfeature.PlaneCompactionObservers)
	assertPlane(tb, actual, expected, aipfeature.PlaneCompactionPreservers)
	assertPlane(tb, actual, expected, aipfeature.PlaneSecretGuards)
	assertPlane(tb, actual, expected, aipfeature.PlaneLocalTurnHandlers)
	assertPlane(tb, actual, expected, aipfeature.PlaneTerminalDecisionProvider)
	assertPlane(tb, actual, expected, aipfeature.PlaneControlToolProvider)
}

func assertPlane[T any](tb testing.TB, actual, expected aipfeature.FrozenPlaneSet, p aipfeature.Plane[T]) {
	tb.Helper()

	actVal := aipfeature.Get(actual, p)
	expVal := aipfeature.Get(expected, p)
	assert.Equal(tb, expVal, actVal, "plane %s value mismatch", p.ID)

	actID, actOK := aipfeature.FrozenIdentity(actual, p)
	expID, expOK := aipfeature.FrozenIdentity(expected, p)
	assert.Equal(tb, expOK, actOK, "plane %s identity presence mismatch", p.ID)
	assert.Equal(tb, expID, actID, "plane %s identity value mismatch", p.ID)
}
