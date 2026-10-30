package feature_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

// TestOrderedIdentityPlanes_ZeroLiveIDCallsOnReplay_Generated asserts that:
// 1. Initial contribution calls ID() exactly once to capture identity.
// 2. Clone(), FreezeRequestPlanes(), ToContributions(), FrozenIdentity(), and Validate() do not increase ID() count.
// 3. ReplayTo() does not increase ID() count (replays from immutable cached metadata).
// 4. Frozen destination returns exact original cached ID.
func TestOrderedIdentityPlanes_ZeroLiveIDCallsOnReplay_Generated(t *testing.T) {
	t.Parallel()

	t.Run("PlaneAttemptTransforms", func(t *testing.T) {
		t.Parallel()

		calls := 0
		xform := callCountingAttemptTransform{id: "at-count-1", calls: &calls}

		// 1. Create empty ContributionSet and contribute one value under SourceFeature.
		cs := feature.NewContributionSet()
		err := feature.Contribute(cs, feature.PlaneAttemptTransforms, "plugin-1", []request.AttemptTransform{xform})
		require.NoError(t, err)
		assert.Equal(t, 1, calls, "after contribution")

		// 2. Freeze the set.
		frozen := cs.Freeze()
		assert.Equal(t, 1, calls, "after freeze")

		// 3. Clone the frozen set.
		cloned := frozen.Clone()
		assert.Equal(t, 1, calls, "after clone")

		// 4. Request freeze.
		_ = feature.FreezeRequestPlanes(frozen)
		assert.Equal(t, 1, calls, "after request freeze")

		// 5. Thaw to mutable contributions.
		_ = frozen.ToContributions()
		assert.Equal(t, 1, calls, "after thaw")

		// 6. Retrieve FrozenIdentity.
		id, ok := feature.FrozenIdentity(frozen, feature.PlaneAttemptTransforms)
		assert.True(t, ok)
		assert.Equal(t, "at-count-1", id)
		assert.Equal(t, 1, calls, "after FrozenIdentity")

		// 7. Validate frozen set and clone.
		require.NoError(t, frozen.Validate())
		require.NoError(t, cloned.Validate())
		assert.Equal(t, 1, calls, "after validation")

		// 8. Replay the frozen set into a fresh empty ContributionSet with ReplayTo().
		dst := feature.NewContributionSet()
		err = frozen.ReplayTo(dst, "replay-plugin")
		require.NoError(t, err)
		assert.Equal(t, 1, calls, "after replay")

		// 9. Freeze destination and assert FrozenIdentity returns exact original cached ID.
		dstFrozen := dst.Freeze()
		assert.Equal(t, 1, calls, "after destination freeze")

		dstID, dstOK := feature.FrozenIdentity(dstFrozen, feature.PlaneAttemptTransforms)
		assert.True(t, dstOK)
		assert.Equal(t, "at-count-1", dstID)
		assert.Equal(t, 1, calls, "after destination FrozenIdentity")
	})

	t.Run("PlaneStreamObserverFactories", func(t *testing.T) {
		t.Parallel()

		calls := 0
		sof := callCountingStreamObserverFactory{id: "sof-count-1", calls: &calls}

		// 1. Create empty ContributionSet and contribute one value under SourceFeature.
		cs := feature.NewContributionSet()
		err := feature.Contribute(cs, feature.PlaneStreamObserverFactories, "plugin-1", []response.StreamObserverFactory{sof})
		require.NoError(t, err)
		assert.Equal(t, 1, calls, "after contribution")

		// 2. Freeze the set.
		frozen := cs.Freeze()
		assert.Equal(t, 1, calls, "after freeze")

		// 3. Clone the frozen set.
		cloned := frozen.Clone()
		assert.Equal(t, 1, calls, "after clone")

		// 4. Request freeze.
		_ = feature.FreezeRequestPlanes(frozen)
		assert.Equal(t, 1, calls, "after request freeze")

		// 5. Thaw to mutable contributions.
		_ = frozen.ToContributions()
		assert.Equal(t, 1, calls, "after thaw")

		// 6. Retrieve FrozenIdentity.
		id, ok := feature.FrozenIdentity(frozen, feature.PlaneStreamObserverFactories)
		assert.True(t, ok)
		assert.Equal(t, "sof-count-1", id)
		assert.Equal(t, 1, calls, "after FrozenIdentity")

		// 7. Validate frozen set and clone.
		require.NoError(t, frozen.Validate())
		require.NoError(t, cloned.Validate())
		assert.Equal(t, 1, calls, "after validation")

		// 8. Replay the frozen set into a fresh empty ContributionSet with ReplayTo().
		dst := feature.NewContributionSet()
		err = frozen.ReplayTo(dst, "replay-plugin")
		require.NoError(t, err)
		assert.Equal(t, 1, calls, "after replay")

		// 9. Freeze destination and assert FrozenIdentity returns exact original cached ID.
		dstFrozen := dst.Freeze()
		assert.Equal(t, 1, calls, "after destination freeze")

		dstID, dstOK := feature.FrozenIdentity(dstFrozen, feature.PlaneStreamObserverFactories)
		assert.True(t, dstOK)
		assert.Equal(t, "sof-count-1", dstID)
		assert.Equal(t, 1, calls, "after destination FrozenIdentity")
	})

	t.Run("PlaneCompactionPreservers", func(t *testing.T) {
		t.Parallel()

		calls := 0
		cp := callCountingCompactionPreserver{stubPreserver: stubPreserver{id: "cp-count-1"}, calls: &calls}

		// 1. Create empty ContributionSet and contribute one value under SourceFeature.
		cs := feature.NewContributionSet()
		err := feature.Contribute(cs, feature.PlaneCompactionPreservers, "plugin-1", []compaction.Preserver{cp})
		require.NoError(t, err)
		assert.Equal(t, 1, calls, "after contribution")

		// 2. Freeze the set.
		frozen := cs.Freeze()
		assert.Equal(t, 1, calls, "after freeze")

		// 3. Clone the frozen set.
		cloned := frozen.Clone()
		assert.Equal(t, 1, calls, "after clone")

		// 4. Request freeze.
		_ = feature.FreezeRequestPlanes(frozen)
		assert.Equal(t, 1, calls, "after request freeze")

		// 5. Thaw to mutable contributions.
		_ = frozen.ToContributions()
		assert.Equal(t, 1, calls, "after thaw")

		// 6. Retrieve FrozenIdentity.
		id, ok := feature.FrozenIdentity(frozen, feature.PlaneCompactionPreservers)
		assert.True(t, ok)
		assert.Equal(t, "cp-count-1", id)
		assert.Equal(t, 1, calls, "after FrozenIdentity")

		// 7. Validate frozen set and clone.
		require.NoError(t, frozen.Validate())
		require.NoError(t, cloned.Validate())
		assert.Equal(t, 1, calls, "after validation")

		// 8. Replay the frozen set into a fresh empty ContributionSet with ReplayTo().
		dst := feature.NewContributionSet()
		err = frozen.ReplayTo(dst, "replay-plugin")
		require.NoError(t, err)
		assert.Equal(t, 1, calls, "after replay")

		// 9. Freeze destination and assert FrozenIdentity returns exact original cached ID.
		dstFrozen := dst.Freeze()
		assert.Equal(t, 1, calls, "after destination freeze")

		dstID, dstOK := feature.FrozenIdentity(dstFrozen, feature.PlaneCompactionPreservers)
		assert.True(t, dstOK)
		assert.Equal(t, "cp-count-1", dstID)
		assert.Equal(t, 1, calls, "after destination FrozenIdentity")
	})
}

// TestOrderedIdentityPlanes_ConcatenationIntoNonEmptyDestination asserts that:
// 1. Destination already contains ID "existing".
// 2. Frozen source contains ID "incoming".
// 3. Replay source using SourceFeature does not increase either ID count.
// 4. Retained values are [existing, incoming].
// 5. Destination FrozenIdentity is "existing", because identity is derived from the first retained element.
func TestOrderedIdentityPlanes_ConcatenationIntoNonEmptyDestination(t *testing.T) {
	t.Parallel()

	t.Run("PlaneAttemptTransforms", func(t *testing.T) {
		t.Parallel()

		callsExist := 0
		existXform := callCountingAttemptTransform{id: "existing", calls: &callsExist}
		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneAttemptTransforms, "plugin-exist", []request.AttemptTransform{existXform}))
		assert.Equal(t, 1, callsExist)

		callsInc := 0
		incXform := callCountingAttemptTransform{id: "incoming", calls: &callsInc}
		srcSet := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(srcSet, feature.PlaneAttemptTransforms, "plugin-inc", []request.AttemptTransform{incXform}))
		assert.Equal(t, 1, callsInc)

		srcFrozen := srcSet.Freeze()

		// Replay source into nonempty destination
		err := srcFrozen.ReplayTo(dst, "replay-plugin")
		require.NoError(t, err)

		// Assert neither count increased
		assert.Equal(t, 1, callsExist, "destination ID() must not be called during replay")
		assert.Equal(t, 1, callsInc, "source ID() must not be called during replay")

		dstFrozen := dst.Freeze()
		retained := feature.Get(dstFrozen, feature.PlaneAttemptTransforms)
		require.Len(t, retained, 2)
		assert.Equal(t, "existing", retained[0].ID())
		assert.Equal(t, "incoming", retained[1].ID())

		dstID, ok := feature.FrozenIdentity(dstFrozen, feature.PlaneAttemptTransforms)
		assert.True(t, ok)
		assert.Equal(t, "existing", dstID, "destination FrozenIdentity must be 'existing' derived from first retained element")
	})

	t.Run("PlaneStreamObserverFactories", func(t *testing.T) {
		t.Parallel()

		callsExist := 0
		existSOF := callCountingStreamObserverFactory{id: "existing", calls: &callsExist}
		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneStreamObserverFactories, "plugin-exist", []response.StreamObserverFactory{existSOF}))
		assert.Equal(t, 1, callsExist)

		callsInc := 0
		incSOF := callCountingStreamObserverFactory{id: "incoming", calls: &callsInc}
		srcSet := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(srcSet, feature.PlaneStreamObserverFactories, "plugin-inc", []response.StreamObserverFactory{incSOF}))
		assert.Equal(t, 1, callsInc)

		srcFrozen := srcSet.Freeze()

		// Replay source into nonempty destination
		err := srcFrozen.ReplayTo(dst, "replay-plugin")
		require.NoError(t, err)

		// Assert neither count increased
		assert.Equal(t, 1, callsExist, "destination ID() must not be called during replay")
		assert.Equal(t, 1, callsInc, "source ID() must not be called during replay")

		dstFrozen := dst.Freeze()
		retained := feature.Get(dstFrozen, feature.PlaneStreamObserverFactories)
		require.Len(t, retained, 2)
		assert.Equal(t, "existing", retained[0].ID())
		assert.Equal(t, "incoming", retained[1].ID())

		dstID, ok := feature.FrozenIdentity(dstFrozen, feature.PlaneStreamObserverFactories)
		assert.True(t, ok)
		assert.Equal(t, "existing", dstID, "destination FrozenIdentity must be 'existing' derived from first retained element")
	})

	t.Run("PlaneCompactionPreservers", func(t *testing.T) {
		t.Parallel()

		callsExist := 0
		existCP := callCountingCompactionPreserver{stubPreserver: stubPreserver{id: "existing"}, calls: &callsExist}
		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneCompactionPreservers, "plugin-exist", []compaction.Preserver{existCP}))
		assert.Equal(t, 1, callsExist)

		callsInc := 0
		incCP := callCountingCompactionPreserver{stubPreserver: stubPreserver{id: "incoming"}, calls: &callsInc}
		srcSet := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(srcSet, feature.PlaneCompactionPreservers, "plugin-inc", []compaction.Preserver{incCP}))
		assert.Equal(t, 1, callsInc)

		srcFrozen := srcSet.Freeze()

		// Replay source into nonempty destination
		err := srcFrozen.ReplayTo(dst, "replay-plugin")
		require.NoError(t, err)

		// Assert neither count increased
		assert.Equal(t, 1, callsExist, "destination ID() must not be called during replay")
		assert.Equal(t, 1, callsInc, "source ID() must not be called during replay")

		dstFrozen := dst.Freeze()
		retained := feature.Get(dstFrozen, feature.PlaneCompactionPreservers)
		require.Len(t, retained, 2)
		assert.Equal(t, "existing", retained[0].ID())
		assert.Equal(t, "incoming", retained[1].ID())

		dstID, ok := feature.FrozenIdentity(dstFrozen, feature.PlaneCompactionPreservers)
		assert.True(t, ok)
		assert.Equal(t, "existing", dstID, "destination FrozenIdentity must be 'existing' derived from first retained element")
	})
}

// TestOrderedIdentityPlanes_MalformedSourceValidationAndRollback_Generated tests that:
// - nonempty value plus HasID=false and empty ID fails;
// - identity metadata present while value is nil fails;
// - nonempty ID plus HasID=false fails;
// - explicit nonnil empty slice plus no identity passes;
// - replay failure leaves destination unchanged (fail-before-mutate);
// - error is *AttributedError with replay contributor ID and exact plane ID.
func TestOrderedIdentityPlanes_MalformedSourceValidationAndRollback_Generated(t *testing.T) {
	t.Parallel()

	initDest := func(t *testing.T) *feature.ContributionSet {
		t.Helper()
		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneSubmitHooks, "init-plugin", []hooks.SubmitHook{
			dummySubmitHook{id: "init-hook", ord: 1},
		}))
		return dst
	}

	assertRollbackAndAttribution := func(t *testing.T, dst *feature.ContributionSet, badFrozen feature.FrozenPlaneSet, expectedPlaneID string) {
		t.Helper()
		snapBefore := dst.Freeze()

		err := badFrozen.ReplayTo(dst, "bad-contributor")
		require.Error(t, err)

		var attrErr *feature.AttributedError
		require.True(t, errors.As(err, &attrErr), "error must be *AttributedError, got %T: %v", err, err)
		assert.Equal(t, "bad-contributor", attrErr.PluginID)
		assert.Equal(t, expectedPlaneID, attrErr.PlaneID)
		assert.True(t, errors.Is(err, feature.ErrInvalidContribution))

		snapAfter := dst.Freeze()
		assert.Equal(t, feature.Get(snapBefore, feature.PlaneSubmitHooks), feature.Get(snapAfter, feature.PlaneSubmitHooks))
	}

	t.Run("PlaneAttemptTransforms", func(t *testing.T) {
		t.Parallel()
		tr := []request.AttemptTransform{dummyAttemptTransform{id: "at-1"}}

		// 1. nonempty value plus HasID=false and empty ID fails
		fNoID := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest(tr, "", false)
		require.Error(t, fNoID.Validate())
		dst1 := initDest(t)
		assertRollbackAndAttribution(t, dst1, fNoID, feature.PlaneAttemptTransforms.ID)

		// 2. identity metadata present while value is nil fails
		fNilVal := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest(nil, "at-1", true)
		require.Error(t, fNilVal.Validate())
		dst2 := initDest(t)
		assertRollbackAndAttribution(t, dst2, fNilVal, feature.PlaneAttemptTransforms.ID)

		// 3. nonempty ID plus HasID=false fails
		fIDNoHas := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest(tr, "at-1", false)
		require.Error(t, fIDNoHas.Validate())
		dst3 := initDest(t)
		assertRollbackAndAttribution(t, dst3, fIDNoHas, feature.PlaneAttemptTransforms.ID)

		// 4. explicit nonnil empty slice plus no identity passes
		fEmptySlice := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest([]request.AttemptTransform{}, "", false)
		require.NoError(t, fEmptySlice.Validate())
		dst4 := initDest(t)
		require.NoError(t, fEmptySlice.ReplayTo(dst4, "good-plugin"))
	})

	t.Run("PlaneStreamObserverFactories", func(t *testing.T) {
		t.Parallel()
		sof := []response.StreamObserverFactory{dummyStreamObserverFactory{id: "sof-1"}}

		// 1. nonempty value plus HasID=false and empty ID fails
		fNoID := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest(sof, "", false)
		require.Error(t, fNoID.Validate())
		dst1 := initDest(t)
		assertRollbackAndAttribution(t, dst1, fNoID, feature.PlaneStreamObserverFactories.ID)

		// 2. identity metadata present while value is nil fails
		fNilVal := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest(nil, "sof-1", true)
		require.Error(t, fNilVal.Validate())
		dst2 := initDest(t)
		assertRollbackAndAttribution(t, dst2, fNilVal, feature.PlaneStreamObserverFactories.ID)

		// 3. nonempty ID plus HasID=false fails
		fIDNoHas := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest(sof, "sof-1", false)
		require.Error(t, fIDNoHas.Validate())
		dst3 := initDest(t)
		assertRollbackAndAttribution(t, dst3, fIDNoHas, feature.PlaneStreamObserverFactories.ID)

		// 4. explicit nonnil empty slice plus no identity passes
		fEmptySlice := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest([]response.StreamObserverFactory{}, "", false)
		require.NoError(t, fEmptySlice.Validate())
		dst4 := initDest(t)
		require.NoError(t, fEmptySlice.ReplayTo(dst4, "good-plugin"))
	})

	t.Run("PlaneCompactionPreservers", func(t *testing.T) {
		t.Parallel()
		cp := []compaction.Preserver{dummyPreserver{id: "cp-1"}}

		// 1. nonempty value plus HasID=false and empty ID fails
		fNoID := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest(cp, "", false)
		require.Error(t, fNoID.Validate())
		dst1 := initDest(t)
		assertRollbackAndAttribution(t, dst1, fNoID, feature.PlaneCompactionPreservers.ID)

		// 2. identity metadata present while value is nil fails
		fNilVal := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest(nil, "cp-1", true)
		require.Error(t, fNilVal.Validate())
		dst2 := initDest(t)
		assertRollbackAndAttribution(t, dst2, fNilVal, feature.PlaneCompactionPreservers.ID)

		// 3. nonempty ID plus HasID=false fails
		fIDNoHas := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest(cp, "cp-1", false)
		require.Error(t, fIDNoHas.Validate())
		dst3 := initDest(t)
		assertRollbackAndAttribution(t, dst3, fIDNoHas, feature.PlaneCompactionPreservers.ID)

		// 4. explicit nonnil empty slice plus no identity passes
		fEmptySlice := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest([]compaction.Preserver{}, "", false)
		require.NoError(t, fEmptySlice.Validate())
		dst4 := initDest(t)
		require.NoError(t, fEmptySlice.ReplayTo(dst4, "good-plugin"))
	})
}

// TestOrderedIdentityPlanes_ValidatorInvocationProof verifies that:
// - ValidateIdentity is genuinely invoked with the exact cached ID;
// - FrozenPlaneSet.Validate() returns an error satisfying errors.Is(err, errValidator) with validatorCalls == 1;
// - ReplayTo() returns an attributed error satisfying errors.Is(err, errValidator) with exact contributor/plane, and increments validator exactly once;
// - live Identity() / object ID() counter remains 0 throughout both operations.
func TestOrderedIdentityPlanes_ValidatorInvocationProof(t *testing.T) { //nolint:paralleltest // mutates package-level plane ValidateIdentity globals
	// Note: non-parallel execution to safely swap plane ValidateIdentity temporarily
	wantID := "cached-test-id"
	errValidator := errors.New("validator sentinel error")

	t.Run("PlaneAttemptTransforms", func(t *testing.T) { //nolint:paralleltest // mutates package-level plane ValidateIdentity globals
		origValidator := feature.PlaneAttemptTransforms.ValidateIdentity
		validatorCalls := 0
		validateFn := func(id string) error {
			validatorCalls++
			require.Equal(t, wantID, id)
			return errValidator
		}
		feature.PlaneAttemptTransforms.ValidateIdentity = validateFn
		restoreCanonical := feature.SetCanonicalValidateIdentityForTest(feature.PlaneAttemptTransforms, validateFn)
		t.Cleanup(func() {
			feature.PlaneAttemptTransforms.ValidateIdentity = origValidator
			restoreCanonical()
		})

		calls := 0
		tr := []request.AttemptTransform{callCountingAttemptTransform{id: "orig-live-id", calls: &calls}}
		fMut := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest(tr, wantID, true)

		// 1. Frozen validation uses cached string and invokes ValidateIdentity
		errVal := fMut.Validate()
		require.Error(t, errVal)
		assert.True(t, errors.Is(errVal, errValidator), "Validate() must return error satisfying errors.Is(err, errValidator)")
		assert.Equal(t, 1, validatorCalls, "Validate() must invoke ValidateIdentity exactly once")
		assert.Equal(t, 0, calls, "frozen validation must not invoke live ID()")

		// FrozenIdentity returns cached ID without live calls or validator calls
		id, ok := feature.FrozenIdentity(fMut, feature.PlaneAttemptTransforms)
		assert.True(t, ok)
		assert.Equal(t, wantID, id)
		assert.Equal(t, 0, calls, "FrozenIdentity must not invoke live ID()")

		// 2. Replay validates using cached string and fails before mutating destination
		dst := feature.NewContributionSet()
		errRep := fMut.ReplayTo(dst, "test-plugin")
		require.Error(t, errRep)
		assert.True(t, errors.Is(errRep, errValidator), "ReplayTo() must return error satisfying errors.Is(err, errValidator)")

		var attrErr *feature.AttributedError
		require.True(t, errors.As(errRep, &attrErr), "ReplayTo() error must be *AttributedError")
		assert.Equal(t, "test-plugin", attrErr.PluginID)
		assert.Equal(t, feature.PlaneAttemptTransforms.ID, attrErr.PlaneID)
		assert.Equal(t, 2, validatorCalls, "ReplayTo() must invoke ValidateIdentity during validation")
		assert.Equal(t, 0, calls, "replay must not invoke live ID()")
	})

	t.Run("PlaneStreamObserverFactories", func(t *testing.T) { //nolint:paralleltest // mutates package-level plane ValidateIdentity globals
		origValidator := feature.PlaneStreamObserverFactories.ValidateIdentity
		validatorCalls := 0
		validateFn := func(id string) error {
			validatorCalls++
			require.Equal(t, wantID, id)
			return errValidator
		}
		feature.PlaneStreamObserverFactories.ValidateIdentity = validateFn
		restoreCanonical := feature.SetCanonicalValidateIdentityForTest(feature.PlaneStreamObserverFactories, validateFn)
		t.Cleanup(func() {
			feature.PlaneStreamObserverFactories.ValidateIdentity = origValidator
			restoreCanonical()
		})

		calls := 0
		sof := []response.StreamObserverFactory{callCountingStreamObserverFactory{id: "orig-live-id", calls: &calls}}
		fMut := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest(sof, wantID, true)

		errVal := fMut.Validate()
		require.Error(t, errVal)
		assert.True(t, errors.Is(errVal, errValidator), "Validate() must return error satisfying errors.Is(err, errValidator)")
		assert.Equal(t, 1, validatorCalls, "Validate() must invoke ValidateIdentity exactly once")
		assert.Equal(t, 0, calls, "frozen validation must not invoke live ID()")

		id, ok := feature.FrozenIdentity(fMut, feature.PlaneStreamObserverFactories)
		assert.True(t, ok)
		assert.Equal(t, wantID, id)
		assert.Equal(t, 0, calls, "FrozenIdentity must not invoke live ID()")

		dst := feature.NewContributionSet()
		errRep := fMut.ReplayTo(dst, "test-plugin")
		require.Error(t, errRep)
		assert.True(t, errors.Is(errRep, errValidator), "ReplayTo() must return error satisfying errors.Is(err, errValidator)")

		var attrErr *feature.AttributedError
		require.True(t, errors.As(errRep, &attrErr), "ReplayTo() error must be *AttributedError")
		assert.Equal(t, "test-plugin", attrErr.PluginID)
		assert.Equal(t, feature.PlaneStreamObserverFactories.ID, attrErr.PlaneID)
		assert.Equal(t, 2, validatorCalls, "ReplayTo() must invoke ValidateIdentity during validation")
		assert.Equal(t, 0, calls, "replay must not invoke live ID()")
	})

	t.Run("PlaneCompactionPreservers", func(t *testing.T) { //nolint:paralleltest // mutates package-level plane ValidateIdentity globals
		origValidator := feature.PlaneCompactionPreservers.ValidateIdentity
		validatorCalls := 0
		validateFn := func(id string) error {
			validatorCalls++
			require.Equal(t, wantID, id)
			return errValidator
		}
		feature.PlaneCompactionPreservers.ValidateIdentity = validateFn
		restoreCanonical := feature.SetCanonicalValidateIdentityForTest(feature.PlaneCompactionPreservers, validateFn)
		t.Cleanup(func() {
			feature.PlaneCompactionPreservers.ValidateIdentity = origValidator
			restoreCanonical()
		})

		calls := 0
		cp := []compaction.Preserver{callCountingCompactionPreserver{stubPreserver: stubPreserver{id: "orig-live-id"}, calls: &calls}}
		fMut := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest(cp, wantID, true)

		errVal := fMut.Validate()
		require.Error(t, errVal)
		assert.True(t, errors.Is(errVal, errValidator), "Validate() must return error satisfying errors.Is(err, errValidator)")
		assert.Equal(t, 1, validatorCalls, "Validate() must invoke ValidateIdentity exactly once")
		assert.Equal(t, 0, calls, "frozen validation must not invoke live ID()")

		id, ok := feature.FrozenIdentity(fMut, feature.PlaneCompactionPreservers)
		assert.True(t, ok)
		assert.Equal(t, wantID, id)
		assert.Equal(t, 0, calls, "FrozenIdentity must not invoke live ID()")

		dst := feature.NewContributionSet()
		errRep := fMut.ReplayTo(dst, "test-plugin")
		require.Error(t, errRep)
		assert.True(t, errors.Is(errRep, errValidator), "ReplayTo() must return error satisfying errors.Is(err, errValidator)")

		var attrErr *feature.AttributedError
		require.True(t, errors.As(errRep, &attrErr), "ReplayTo() error must be *AttributedError")
		assert.Equal(t, "test-plugin", attrErr.PluginID)
		assert.Equal(t, feature.PlaneCompactionPreservers.ID, attrErr.PlaneID)
		assert.Equal(t, 2, validatorCalls, "ReplayTo() must invoke ValidateIdentity during validation")
		assert.Equal(t, 0, calls, "replay must not invoke live ID()")
	})
}

// TestOrderedIdentityPlanes_GenerationBinderReplayRejected asserts that ReplaySourceTo
// with SourceGenerationBinder rejects identity-bearing planes with CombReplaceByIdentity
// with ErrUnsupportedReplaySource before validation, mutation, or live ID calls.
func TestOrderedIdentityPlanes_GenerationBinderReplayRejected(t *testing.T) {
	t.Parallel()

	initDest := func(t *testing.T) *feature.ContributionSet {
		t.Helper()
		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneSubmitHooks, "init-plugin", []hooks.SubmitHook{
			dummySubmitHook{id: "init-hook", ord: 1},
		}))
		return dst
	}

	t.Run("Generated_PlaneAttemptTransforms", func(t *testing.T) {
		t.Parallel()
		calls := 0
		xform := callCountingAttemptTransform{id: "at-binder-1", calls: &calls}
		src := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(src, feature.PlaneAttemptTransforms, "plugin-1", []request.AttemptTransform{xform}))
		assert.Equal(t, 1, calls, "initial contribution must call ID() once")

		frozen := src.Freeze()
		dst := initDest(t)
		snapBefore := dst.Freeze()

		err := frozen.ReplaySourceTo(dst, feature.SourceGenerationBinder, "binder-contributor")
		require.Error(t, err)
		assert.True(t, errors.Is(err, feature.ErrUnsupportedReplaySource), "must satisfy errors.Is(err, ErrUnsupportedReplaySource)")

		var attrErr *feature.AttributedError
		require.True(t, errors.As(err, &attrErr), "must be *AttributedError")
		assert.Equal(t, "binder-contributor", attrErr.PluginID)
		assert.Equal(t, feature.PlaneAttemptTransforms.ID, attrErr.PlaneID)

		// Assert zero live ID calls during rejection
		assert.Equal(t, 1, calls, "rejection must happen before any live ID calls")

		// Assert destination is unchanged
		snapAfter := dst.Freeze()
		assert.Equal(t, feature.Get(snapBefore, feature.PlaneSubmitHooks), feature.Get(snapAfter, feature.PlaneSubmitHooks))

		// Assert SourceFeature remains supported
		dstGood := feature.NewContributionSet()
		require.NoError(t, frozen.ReplaySourceTo(dstGood, feature.SourceFeature, "feature-contributor"))
	})

	t.Run("Generated_PlaneStreamObserverFactories", func(t *testing.T) {
		t.Parallel()
		calls := 0
		sof := callCountingStreamObserverFactory{id: "sof-binder-1", calls: &calls}
		src := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(src, feature.PlaneStreamObserverFactories, "plugin-1", []response.StreamObserverFactory{sof}))
		assert.Equal(t, 1, calls, "initial contribution must call ID() once")

		frozen := src.Freeze()
		dst := initDest(t)
		snapBefore := dst.Freeze()

		err := frozen.ReplaySourceTo(dst, feature.SourceGenerationBinder, "binder-contributor")
		require.Error(t, err)
		assert.True(t, errors.Is(err, feature.ErrUnsupportedReplaySource), "must satisfy errors.Is(err, ErrUnsupportedReplaySource)")

		var attrErr *feature.AttributedError
		require.True(t, errors.As(err, &attrErr), "must be *AttributedError")
		assert.Equal(t, "binder-contributor", attrErr.PluginID)
		assert.Equal(t, feature.PlaneStreamObserverFactories.ID, attrErr.PlaneID)

		assert.Equal(t, 1, calls, "rejection must happen before any live ID calls")

		snapAfter := dst.Freeze()
		assert.Equal(t, feature.Get(snapBefore, feature.PlaneSubmitHooks), feature.Get(snapAfter, feature.PlaneSubmitHooks))
	})

	t.Run("Generated_PlaneCompactionPreservers", func(t *testing.T) {
		t.Parallel()
		calls := 0
		cp := callCountingCompactionPreserver{stubPreserver: stubPreserver{id: "cp-binder-1"}, calls: &calls}
		src := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(src, feature.PlaneCompactionPreservers, "plugin-1", []compaction.Preserver{cp}))
		assert.Equal(t, 1, calls, "initial contribution must call ID() once")

		frozen := src.Freeze()
		dst := initDest(t)
		snapBefore := dst.Freeze()

		err := frozen.ReplaySourceTo(dst, feature.SourceGenerationBinder, "binder-contributor")
		require.Error(t, err)
		assert.True(t, errors.Is(err, feature.ErrUnsupportedReplaySource), "must satisfy errors.Is(err, ErrUnsupportedReplaySource)")

		var attrErr *feature.AttributedError
		require.True(t, errors.As(err, &attrErr), "must be *AttributedError")
		assert.Equal(t, "binder-contributor", attrErr.PluginID)
		assert.Equal(t, feature.PlaneCompactionPreservers.ID, attrErr.PlaneID)

		assert.Equal(t, 1, calls, "rejection must happen before any live ID calls")

		snapAfter := dst.Freeze()
		assert.Equal(t, feature.Get(snapBefore, feature.PlaneSubmitHooks), feature.Get(snapAfter, feature.PlaneSubmitHooks))
	})

	t.Run("Generated_MultiplePlanes_FirstInManifestOrder", func(t *testing.T) {
		t.Parallel()
		callsAT := 0
		callsSOF := 0
		xform := callCountingAttemptTransform{id: "at-1", calls: &callsAT}
		sof := callCountingStreamObserverFactory{id: "sof-1", calls: &callsSOF}

		src := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(src, feature.PlaneStreamObserverFactories, "plugin-1", []response.StreamObserverFactory{sof}))
		require.NoError(t, feature.Contribute(src, feature.PlaneAttemptTransforms, "plugin-1", []request.AttemptTransform{xform}))

		frozen := src.Freeze()
		dst := initDest(t)

		err := frozen.ReplaySourceTo(dst, feature.SourceGenerationBinder, "binder-contributor")
		require.Error(t, err)
		assert.True(t, errors.Is(err, feature.ErrUnsupportedReplaySource))

		var attrErr *feature.AttributedError
		require.True(t, errors.As(err, &attrErr))
		assert.Equal(t, "binder-contributor", attrErr.PluginID)
		// PlaneAttemptTransforms comes before PlaneStreamObserverFactories in manifest order
		assert.Equal(t, feature.PlaneAttemptTransforms.ID, attrErr.PlaneID)
	})

	t.Run("NonIdentityPlaneUnderGenerationBinder_NotRejected", func(t *testing.T) {
		t.Parallel()
		hook := dummySubmitHook{id: "submit-hook-1", ord: 10}
		src := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(src, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{hook}))

		frozen := src.Freeze()
		dst := feature.NewContributionSet()

		// PlaneSubmitHooks has no CombReplaceByIdentity rule on GenerationBinder (CombUnsupported on GenerationBinder, or Combine error if not supported, but NOT ErrUnsupportedReplaySource sentinel before replay)
		err := frozen.ReplaySourceTo(dst, feature.SourceGenerationBinder, "binder-contributor")
		// Should NOT be ErrUnsupportedReplaySource
		assert.False(t, errors.Is(err, feature.ErrUnsupportedReplaySource))
	})
}

func TestOrderedIdentityPlanes_CandidateReplay_ZeroLiveIDCalls_Generated(t *testing.T) {
	t.Parallel()

	t.Run("PlaneAttemptTransforms_EmptyDestination", func(t *testing.T) {
		t.Parallel()
		calls := 0
		xform := callCountingAttemptTransform{id: "cand-at-1", calls: &calls}

		candCS := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(candCS, feature.PlaneAttemptTransforms, "plugin-cand", []request.AttemptTransform{xform}))
		assert.Equal(t, 1, calls, "after candidate contribution")

		candFrozen := candCS.Freeze()
		assert.Equal(t, 1, calls, "after candidate freeze")

		dst := feature.NewContributionSet()
		require.NoError(t, candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate"))
		assert.Equal(t, 1, calls, "candidate replay into empty destination must not invoke live ID()")

		dstFrozen := dst.Freeze()
		assert.Equal(t, 1, calls, "after destination freeze")

		id, ok := feature.FrozenIdentity(dstFrozen, feature.PlaneAttemptTransforms)
		assert.True(t, ok)
		assert.Equal(t, "cand-at-1", id)
		assert.Equal(t, 1, calls, "after destination FrozenIdentity")
	})

	t.Run("PlaneAttemptTransforms_NonEmptyDestination", func(t *testing.T) {
		t.Parallel()
		dstCalls := 0
		dstXform := callCountingAttemptTransform{id: "dst-at-1", calls: &dstCalls}
		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneAttemptTransforms, "plugin-dst", []request.AttemptTransform{dstXform}))
		assert.Equal(t, 1, dstCalls)

		candCalls := 0
		candXform := callCountingAttemptTransform{id: "cand-at-2", calls: &candCalls}
		candCS := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(candCS, feature.PlaneAttemptTransforms, "plugin-cand", []request.AttemptTransform{candXform}))
		assert.Equal(t, 1, candCalls)

		candFrozen := candCS.Freeze()
		assert.Equal(t, 1, candCalls)

		require.NoError(t, candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate"))
		assert.Equal(t, 1, dstCalls, "destination ID() must not be called during candidate replay")
		assert.Equal(t, 1, candCalls, "candidate ID() must not be called during candidate replay")

		dstFrozen := dst.Freeze()
		assert.Equal(t, 1, dstCalls)
		assert.Equal(t, 1, candCalls)

		retained := feature.Get(dstFrozen, feature.PlaneAttemptTransforms)
		require.Len(t, retained, 2)
		assert.Equal(t, "dst-at-1", retained[0].ID())
		assert.Equal(t, "cand-at-2", retained[1].ID())

		id, ok := feature.FrozenIdentity(dstFrozen, feature.PlaneAttemptTransforms)
		assert.True(t, ok)
		assert.Equal(t, "dst-at-1", id)
	})

	t.Run("PlaneStreamObserverFactories_CandidateReplayIgnored_ZeroLiveIDCalls", func(t *testing.T) {
		t.Parallel()
		calls := 0
		sof := callCountingStreamObserverFactory{id: "cand-sof-1", calls: &calls}

		candCS := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(candCS, feature.PlaneStreamObserverFactories, "plugin-cand", []response.StreamObserverFactory{sof}))
		assert.Equal(t, 1, calls)

		candFrozen := candCS.Freeze()
		assert.Equal(t, 1, calls)

		dst := feature.NewContributionSet()
		require.NoError(t, candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate"))
		assert.Equal(t, 1, calls, "candidate replay must not invoke live ID()")

		dstFrozen := dst.Freeze()
		assert.Equal(t, 1, calls)
		assert.Empty(t, feature.Get(dstFrozen, feature.PlaneStreamObserverFactories))
	})

	t.Run("PlaneCompactionPreservers_EmptyDestination", func(t *testing.T) {
		t.Parallel()
		calls := 0
		cp := callCountingCompactionPreserver{stubPreserver: stubPreserver{id: "cand-cp-1"}, calls: &calls}

		candCS := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(candCS, feature.PlaneCompactionPreservers, "plugin-cand", []compaction.Preserver{cp}))
		assert.Equal(t, 1, calls)

		candFrozen := candCS.Freeze()
		assert.Equal(t, 1, calls)

		dst := feature.NewContributionSet()
		require.NoError(t, candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate"))
		assert.Equal(t, 1, calls, "candidate replay into empty destination must not invoke live ID()")

		dstFrozen := dst.Freeze()
		assert.Equal(t, 1, calls)

		id, ok := feature.FrozenIdentity(dstFrozen, feature.PlaneCompactionPreservers)
		assert.True(t, ok)
		assert.Equal(t, "cand-cp-1", id)
		assert.Equal(t, 1, calls)
	})

	t.Run("PlaneCompactionPreservers_NonEmptyDestination", func(t *testing.T) {
		t.Parallel()
		dstCalls := 0
		dstCP := callCountingCompactionPreserver{stubPreserver: stubPreserver{id: "dst-cp-1"}, calls: &dstCalls}
		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneCompactionPreservers, "plugin-dst", []compaction.Preserver{dstCP}))
		assert.Equal(t, 1, dstCalls)

		candCalls := 0
		candCP := callCountingCompactionPreserver{stubPreserver: stubPreserver{id: "cand-cp-2"}, calls: &candCalls}
		candCS := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(candCS, feature.PlaneCompactionPreservers, "plugin-cand", []compaction.Preserver{candCP}))
		assert.Equal(t, 1, candCalls)

		candFrozen := candCS.Freeze()
		assert.Equal(t, 1, candCalls)

		require.NoError(t, candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate"))
		assert.Equal(t, 1, dstCalls, "destination ID() must not be called during candidate replay")
		assert.Equal(t, 1, candCalls, "candidate ID() must not be called during candidate replay")

		dstFrozen := dst.Freeze()
		assert.Equal(t, 1, dstCalls)
		assert.Equal(t, 1, candCalls)

		retained := feature.Get(dstFrozen, feature.PlaneCompactionPreservers)
		require.Len(t, retained, 2)
		assert.Equal(t, "dst-cp-1", retained[0].ID())
		assert.Equal(t, "cand-cp-2", retained[1].ID())

		id, ok := feature.FrozenIdentity(dstFrozen, feature.PlaneCompactionPreservers)
		assert.True(t, ok)
		assert.Equal(t, "dst-cp-1", id)
	})
}

func TestReplay_SourceAdmission_FailBeforeMutate(t *testing.T) {
	t.Parallel()

	t.Run("ReplaySourceTo_UnsupportedSource_RejectsBeforeMutatingDestination", func(t *testing.T) {
		t.Parallel()

		src := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(src, feature.PlaneSubmitHooks, "plugin-src", []hooks.SubmitHook{
			dummySubmitHook{id: "hook-src", ord: 1},
		}))
		frozenSrc := src.Freeze()

		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneSubmitHooks, "plugin-dst", []hooks.SubmitHook{
			dummySubmitHook{id: "hook-dst", ord: 10},
		}))

		err := frozenSrc.ReplaySourceTo(dst, feature.SourceHost, "host-replayer")
		require.Error(t, err)
		assert.True(t, errors.Is(err, feature.ErrUnsupportedSource), "must satisfy errors.Is(err, ErrUnsupportedSource)")

		var attrErr *feature.AttributedError
		require.True(t, errors.As(err, &attrErr), "must be *AttributedError")
		assert.Equal(t, "host-replayer", attrErr.PluginID)
		assert.Equal(t, feature.PlaneSubmitHooks.ID, attrErr.PlaneID)

		// Destination must remain unchanged (fail-before-mutate)
		retained := feature.Get(dst.Freeze(), feature.PlaneSubmitHooks)
		require.Len(t, retained, 1)
		assert.Equal(t, "hook-dst", retained[0].ID())
	})

	t.Run("ContributeCandidateTo_UnsupportedSource_RejectsBeforeMutatingDestination", func(t *testing.T) {
		t.Parallel()

		src := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(src, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-cand", 1024))
		frozenCand := src.Freeze()

		dst := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dst, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-dst", 512))

		err := frozenCand.ContributeCandidateTo(dst, feature.SourceHost, "candidate-actor")
		require.Error(t, err)
		assert.True(t, errors.Is(err, feature.ErrUnsupportedSource))

		var attrErr *feature.AttributedError
		require.True(t, errors.As(err, &attrErr))
		assert.Equal(t, "candidate-actor", attrErr.PluginID)
		assert.Equal(t, feature.PlaneToolCallFinalizationMaxArgsBytes.ID, attrErr.PlaneID)

		// Destination must remain unchanged (fail-before-mutate)
		assert.Equal(t, 512, feature.Get(dst.Freeze(), feature.PlaneToolCallFinalizationMaxArgsBytes))
	})
}
