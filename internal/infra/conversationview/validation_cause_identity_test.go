package conversationview_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
)

// The conversation-view store rejects through a stable class
// (ErrInvalidTagRequest, ErrInvalidSteeringRequest, ErrALegNotFound) and
// formats the validator cause. The cause is a published sentinel of the same
// package, so %v left an adapter able to read the class and unable to classify
// the cause: it could only distinguish a malformed identity from a malformed
// reason code, or "absent" from "malformed", by matching text. %w keeps the
// cause reachable through errors.Is and renders the same message.
func Test_PublishedCauseIdentitySurvivesStoreValidationWraps(t *testing.T) {
	t.Parallel()

	const aLeg = "a_causidentity0000000000000000001"
	store := conversationview.NewReferenceStore()
	require.NoError(t, store.CreateALeg(t.Context(), aLeg))

	_, tagErr := store.TagNeverBackend(t.Context(), aLeg, []conversationview.TagRequest{{
		Identity: "not-an-identity",
		Reason:   "reason_one",
	}})
	badAnchor := conversationview.MessageAnchor{Identity: "not-an-identity", Occurrence: 1}
	_, putErr := store.PutSteering(t.Context(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov-bad-anchor",
		Message:             conversationview.StoredMessageV1{Role: "user", Text: "steering"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &badAnchor},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "reason_one",
	})
	_, snapshotErr := store.Snapshot(t.Context(), strings.Repeat("a", conversationview.MaxALegIDBytes+1))

	require.Error(t, tagErr)
	assert.ErrorIs(t, tagErr, conversationview.ErrInvalidTagRequest, "the batch class stays primary")
	assert.ErrorIs(t, tagErr, conversationview.ErrInvalidMessageIdentity, "the identity cause stays classifiable")

	require.Error(t, putErr)
	assert.ErrorIs(t, putErr, conversationview.ErrInvalidSteeringRequest, "the request class stays primary")
	assert.ErrorIs(t, putErr, conversationview.ErrInvalidMessageIdentity, "the anchor cause stays classifiable")

	require.Error(t, snapshotErr)
	assert.ErrorIs(t, snapshotErr, conversationview.ErrALegNotFound, "the not-found class stays primary")
	assert.ErrorIs(t, snapshotErr, conversationview.ErrInvalidALegID, "the identifier cause stays classifiable")

	for _, tc := range []struct {
		err   error
		class error
	}{
		{tagErr, conversationview.ErrInvalidTagRequest},
		{putErr, conversationview.ErrInvalidSteeringRequest},
		{snapshotErr, conversationview.ErrALegNotFound},
	} {
		assert.True(t, strings.HasPrefix(tc.err.Error(), tc.class.Error()+": "),
			"the rendered text keeps the class prefix: %q", tc.err.Error())
	}
}
