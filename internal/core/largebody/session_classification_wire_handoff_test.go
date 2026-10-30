package largebody_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// TestWireClassificationEvidenceContextRoundTrip pins the bounded ctx handoff
// the wire lane uses to reach the proof-compiled classification evidence
// without any canonical request materialization: ContextWithWireProof must
// attach the assessed proof evidence verbatim, and the accessor must return it
// unchanged while still tolerating a nil or evidence-free context
// (Requirements 5.2, 5.4, 5.5).
func TestWireClassificationEvidenceContextRoundTrip(t *testing.T) {
	t.Parallel()

	evidence := sessionclassification.Evidence{
		Operation:       aipapi.OperationOpenAIResponses,
		ClientUserAgent: "codex-cli/0.42.0 (accepted)",
		ToolCategories: sessionclassification.ToolCategoryFileRead |
			sessionclassification.ToolCategoryFileEdit |
			sessionclassification.ToolCategoryOSCommand,
	}
	proof := largebody.Proof{
		ProfileID:              "openai-responses",
		Operation:              evidence.Operation,
		Identity:               largebody.NewIdentityDigest([32]byte{3, 3, 3, 3}),
		ClassificationEvidence: evidence,
	}

	ctx := largebody.ContextWithWireProof(context.Background(), proof, "req-round-trip")
	got, ok := largebody.WireClassificationEvidenceFromContext(ctx)
	require.True(t, ok, "ContextWithWireProof must attach the assessed proof classification evidence")
	require.Equal(t, evidence, got, "the wire handoff must carry the proof evidence verbatim")

	// Absent evidence is legal and must read as "stay unknown", never as a
	// negative classification (Requirement 5.4).
	absent, ok := largebody.WireClassificationEvidenceFromContext(context.Background())
	require.False(t, ok, "a context with no proof handoff must report absent evidence")
	require.True(t, absent.IsZero())
	require.Equal(t, sessionclassification.Evidence{}, absent)

	// A nil context is tolerated rather than dereferenced. The nil argument is
	// the point of this assertion, not an oversight.
	//nolint:staticcheck // SA1012: deliberately probing the documented nil-context guard.
	nilCtx, ok := largebody.WireClassificationEvidenceFromContext(nil)
	require.False(t, ok)
	require.Equal(t, sessionclassification.Evidence{}, nilCtx)

	// The handoff is single-valued: a later proof overwrites an earlier one, so a
	// stale caller context can never supply another turn's evidence.
	second := evidence
	second.ClientUserAgent = "claude-cli/1.0 (accepted)"
	second.ToolCategories = sessionclassification.ToolCategoryWebAccess
	overwritten := largebody.ContextWithWireProof(ctx, largebody.Proof{
		ProfileID:              "openai-chat",
		Identity:               largebody.NewIdentityDigest([32]byte{4, 4, 4, 4}),
		ClassificationEvidence: second,
	}, "req-round-trip-2")
	gotSecond, ok := largebody.WireClassificationEvidenceFromContext(overwritten)
	require.True(t, ok)
	require.Equal(t, second, gotSecond)

	// The carrier stays a fixed-shape metadata value: no header bag, tool list,
	// transcript or shadow canonical Call can ride this handoff.
	require.NoError(t, largebody.DefaultTestWireTurnFacts().AssertNoShadowCall())
}
