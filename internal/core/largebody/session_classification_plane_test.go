package largebody_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

type eligibleSessionClassifier struct{ id string }

func (c eligibleSessionClassifier) ID() string { return c.id }

func (eligibleSessionClassifier) Classify(context.Context, sessionclassification.Input) (session.Classification, error) {
	return session.Classification{}, nil
}

func TestSessionClassifierPlane_OccupiedMetadataOnlyPlaneStaysWireEligible(t *testing.T) {
	t.Parallel()

	contributions := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(contributions, feature.PlaneSessionClassifier, "session-classification", sessionclassification.Classifier(eligibleSessionClassifier{id: "session-classification"})))
	frozen := contributions.Freeze()
	require.NotNil(t, feature.Get(frozen, feature.PlaneSessionClassifier))

	input := occupy35Plane(eligible35Input("gen-session-classification"), feature.PlaneSessionClassifier.ID)
	summary, err := largebody.CompileWireEligibilitySummary(input, testEligibilityBudget)
	require.NoError(t, err)
	require.False(t, summary.HasStaticBlocker())

	disposition, reason := summary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   "gen-session-classification",
		ThresholdBytes: 1,
		ContentLength:  2,
	})
	require.Equal(t, largebody.NeedsRequestAssessment, disposition)
	require.Equal(t, largebody.StaticWireReasonNone, reason)
	require.NoError(t, largebody.DefaultTestWireTurnFacts().AssertNoShadowCall())
}

// TestSessionClassifierPlane_ProductionCensusRecordsOccupancyWithoutCanonicalBlocker
// covers the production census the runtime bundle actually compiles, not the
// test-local access map: NewStandardDependencyCensus must place the classifier
// at its manifest position with a MetadataOnly access class, and occupying that
// plane must record occupancy in the fixed plane order without ever setting the
// canonical static blocker bit (requirements 5.1, 5.6, 12.11).
//
// The final sub-case is the discriminating control. Re-classifying the very
// same plane as CanonicalRequired and occupying it must set exactly that
// plane's blocker bit, so the "no blocker" assertion above cannot pass merely
// because the compiler ignores occupancy or because the bit index is wrong.
func TestSessionClassifierPlane_ProductionCensusRecordsOccupancyWithoutCanonicalBlocker(t *testing.T) {
	t.Parallel()

	const generationID = "gen-session-classification-production-census"

	census := largebody.NewStandardDependencyCensus(generationID)
	require.Len(t, census.Planes, largebody.WireEligibilityPlaneCount,
		"production census must cover every standard plane")
	require.Equal(t, largebody.WireEligibilityPlaneCount, len(feature.StandardPlanes),
		"production census count must track the plane manifest")

	idx, ok := largebody.WireEligibilityPlaneIndex(feature.PlaneSessionClassifier.ID)
	require.True(t, ok, "production census must know the classifier plane ID")
	require.Equal(t, feature.PlaneSessionClassifier.ID, feature.StandardPlanes[idx].PlaneID(),
		"classifier census index must equal its manifest position (order is part of the closed census)")

	var classified bool
	for _, plane := range census.Planes {
		if plane.ID != feature.PlaneSessionClassifier.ID {
			continue
		}
		classified = true
		require.Equal(t, largebody.PlaneAccessMetadataOnly, plane.Access,
			"the classifier plane must stay metadata-only so its presence alone never forces canonical execution")
		require.False(t, plane.Occupied, "the baseline census starts unoccupied")
	}
	require.True(t, classified, "production census omitted the classifier plane")

	require.Equal(t, feature.RequestBodyMetadataOnly, feature.PlaneSessionClassifier.RequestAccess,
		"the declared request-access class must remain metadata-only")

	occupied := occupyCensusPlane(census, feature.PlaneSessionClassifier.ID)
	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              generationID,
		Planes:                    occupied.Planes,
		Hooks:                     occupied.Hooks,
		Ports:                     occupied.Ports,
		TwoPhaseExecutorAvailable: occupied.TwoPhaseExecutorAvailable,
	}, testEligibilityBudget)
	require.NoError(t, err)
	require.True(t, summary.Sealed())
	require.Zero(t, summary.PlaneBlockers(),
		"an occupied metadata-only classifier must set no plane blocker at all")
	require.Zero(t, summary.PlaneBlockers()&(1<<uint(idx)),
		"the classifier's own census bit must stay clear")
	require.False(t, summary.HasStaticBlocker())
	require.Equal(t, largebody.WirePortBlocker(0), summary.PortBlockers())

	disposition, reason := summary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   generationID,
		ThresholdBytes: 1,
		ContentLength:  2,
	})
	require.Equal(t, largebody.NeedsRequestAssessment, disposition)
	require.Equal(t, largebody.StaticWireReasonNone, reason)

	t.Run("canonical-required access is the discriminating control", func(t *testing.T) {
		t.Parallel()

		widened := occupyCensusPlane(census, feature.PlaneSessionClassifier.ID)
		widened.Planes[idx].Access = largebody.PlaneAccessCanonicalRequired
		control, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
			GenerationID:              generationID,
			Planes:                    widened.Planes,
			Hooks:                     widened.Hooks,
			Ports:                     widened.Ports,
			TwoPhaseExecutorAvailable: widened.TwoPhaseExecutorAvailable,
		}, testEligibilityBudget)
		require.NoError(t, err)
		require.NotZero(t, control.PlaneBlockers()&(1<<uint(idx)),
			"widening the classifier to canonical-required must set exactly its blocker bit")
		require.True(t, control.HasStaticBlocker())

		disp, rsn := control.StaticDisposition(largebody.StaticDispositionInput{
			FeatureEnabled: true,
			GenerationID:   generationID,
			ThresholdBytes: 1,
			ContentLength:  2,
		})
		require.Equal(t, largebody.DefinitelyCanonical, disp)
		require.Equal(t, largebody.StaticWireReasonStaticBlocker, rsn)
	})
}

// occupyCensusPlane returns a copy of the production census with one plane
// marked occupied, mirroring how the runtime bundle derives occupancy from the
// frozen contribution set.
func occupyCensusPlane(census largebody.DependencyCensus, id string) largebody.DependencyCensus {
	out := census
	out.Planes = append([]largebody.PlaneEligibilityInput(nil), census.Planes...)
	found := false
	for i := range out.Planes {
		if out.Planes[i].ID == id {
			out.Planes[i].Occupied = true
			found = true
		}
	}
	if !found {
		panic("occupyCensusPlane: unknown plane " + id)
	}
	return out
}
