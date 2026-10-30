package feature_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

type sessionClassifierPlaneProbe struct {
	id      string
	panicID bool
}

func (p *sessionClassifierPlaneProbe) ID() string {
	if p.panicID {
		panic("classifier identity unavailable")
	}
	return p.id
}

func (*sessionClassifierPlaneProbe) Classify(context.Context, sessionclassification.Input) (session.Classification, error) {
	return session.Classification{}, nil
}

func TestSessionClassifierPlane_DeclarationAndGeneratedAccessors(t *testing.T) {
	t.Parallel()

	plane := feature.PlaneSessionClassifier
	require.Equal(t, "session_classifier", plane.ID)
	require.Equal(t, feature.RequestBodyMetadataOnly, plane.RequestAccess)
	require.Equal(t, feature.MultExclusive, plane.Multiplicity)
	require.Equal(t, feature.CombExclusive, plane.Rules.Feature)
	require.Equal(t, feature.NilReject, plane.NilPolicy)
	require.Equal(t, feature.StageIDSessionClassification, plane.Diagnostics.StageID)

	classifier := &sessionClassifierPlaneProbe{id: "classification-probe"}
	contributions := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(contributions, plane, "classification-feature", sessionclassification.Classifier(classifier)))

	frozen := contributions.Freeze()
	require.Same(t, classifier, feature.Get(frozen, plane))
	identity, ok := feature.FrozenIdentity(frozen, plane)
	require.True(t, ok)
	require.Equal(t, classifier.id, identity)

	occupants := plane.Diagnostics.Materialize(classifier)
	require.Equal(t, []feature.DiagnosticOccupant{{Label: "classifier"}}, occupants)
}

func TestSessionClassifierPlane_RejectsInvalidIdentityAtComposition(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		classifier *sessionClassifierPlaneProbe
	}{
		{name: "empty", classifier: &sessionClassifierPlaneProbe{}},
		{name: "oversized", classifier: &sessionClassifierPlaneProbe{id: strings.Repeat("x", 257)}},
		{name: "control character", classifier: &sessionClassifierPlaneProbe{id: "classifier\ninvalid"}},
		{name: "panicking", classifier: &sessionClassifierPlaneProbe{panicID: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			contributions := feature.NewContributionSet()
			err := feature.Contribute(contributions, feature.PlaneSessionClassifier, "classification-feature", sessionclassification.Classifier(tc.classifier))
			require.Error(t, err)
			require.Empty(t, feature.Get(contributions.Freeze(), feature.PlaneSessionClassifier))
		})
	}

	var typedNil *sessionClassifierPlaneProbe
	contributions := feature.NewContributionSet()
	err := feature.Contribute(contributions, feature.PlaneSessionClassifier, "classification-feature", sessionclassification.Classifier(typedNil))
	require.Error(t, err)
	require.Empty(t, feature.Get(contributions.Freeze(), feature.PlaneSessionClassifier))
}

func TestSessionClassifierPlane_CachedIdentitySurvivesIDMutationAndPanic(t *testing.T) {
	t.Parallel()

	classifier := &sessionClassifierPlaneProbe{id: "classification-probe"}
	contributions := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(contributions, feature.PlaneSessionClassifier, "classification-feature", sessionclassification.Classifier(classifier)))
	frozen := contributions.Freeze()

	classifier.id = "changed-after-compose"
	classifier.panicID = true

	require.NoError(t, frozen.Validate(), "frozen validation must use the cached identity")
	identity, ok := feature.FrozenIdentity(frozen, feature.PlaneSessionClassifier)
	require.True(t, ok)
	require.Equal(t, "classification-probe", identity)

	replayed := feature.NewContributionSet()
	require.NoError(t, frozen.ReplayTo(replayed, "replay"), "replay must use the cached identity")
	replayedIdentity, ok := feature.FrozenIdentity(replayed.Freeze(), feature.PlaneSessionClassifier)
	require.True(t, ok)
	require.Equal(t, "classification-probe", replayedIdentity)

	projections := feature.ProjectDiagnostics(frozen)
	var classificationProjection *feature.DiagnosticPlaneProjection
	for i := range projections {
		if projections[i].PlaneID == feature.PlaneSessionClassifier.ID {
			classificationProjection = &projections[i]
			break
		}
	}
	require.NotNil(t, classificationProjection, "classifier diagnostics must remain occupied after composition")
	require.Equal(t, feature.StageIDSessionClassification, classificationProjection.StageID)
	require.Equal(t, []feature.DiagnosticOccupant{{Label: "classifier"}}, classificationProjection.Occupants)
}
