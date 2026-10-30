package largebody_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// This implementation-time baseline uses an existing MetadataOnly plane as a
// stand-in for the not-yet-declared session classifier plane. When the
// classifier plane is added, keep this access/disposition behavior and extend
// the census-backed coverage to its ID.
func TestSessionClassificationBaseline_OccupiedMetadataOnlyPlane(t *testing.T) {
	t.Parallel()

	const generationID = "gen-session-classification-baseline"
	summary := compile35(t, occupy35Plane(eligible35Input(generationID), "session_openers"))
	disposition, reason := summary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   generationID,
		ThresholdBytes: 1,
		ContentLength:  2,
	})

	require.Equal(t, largebody.NeedsRequestAssessment, disposition)
	require.Equal(t, largebody.StaticWireReasonNone, reason)
}

// Task 7.1 replaced the pre-carrier absence assertion with the bounded
// carrier. Classification evidence now exists on the proof and the wire
// session facts, so the remaining invariant to pin here is that it stays a
// single bounded SDK value with no parallel loose field, and that the
// no-shadow-Call guard still holds. The carrier shape, accounting and
// retention guard live in session_classification_evidence_test.go.
func TestSessionClassificationBaseline_WireFactsCarryOnlyTheBoundedEvidenceCarrier(t *testing.T) {
	t.Parallel()

	evidenceType := reflect.TypeFor[sessionclassification.Evidence]()
	for _, typ := range []reflect.Type{
		reflect.TypeFor[largebody.Proof](),
		reflect.TypeFor[largebody.WireSessionFacts](),
		reflect.TypeFor[largebody.WireTurnFacts](),
	} {
		carriers := 0
		for field := range typ.Fields() {
			if field.Type == evidenceType {
				carriers++
				require.Equal(t, "ClassificationEvidence", field.Name,
					"%s must expose the carrier under the reviewed name", typ)
				continue
			}
			// No parallel loose identity or tool-bits field beside the carrier.
			require.NotEqual(t, "ClientUserAgent", field.Name,
				"%s must not expose a raw client identity outside the bounded carrier", typ)
			require.NotEqual(t, "ToolCategories", field.Name,
				"%s must not expose tool classification bits outside the bounded carrier", typ)
		}
		if typ == reflect.TypeFor[largebody.WireTurnFacts]() {
			require.Zero(t, carriers, "WireTurnFacts reaches the carrier through its session domain")
		} else {
			require.Equal(t, 1, carriers, "%s must carry exactly one bounded classification carrier", typ)
		}
	}

	require.NoError(t, largebody.DefaultTestWireTurnFacts().AssertNoShadowCall())
}
