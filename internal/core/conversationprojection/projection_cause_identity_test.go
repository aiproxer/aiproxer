package conversationprojection_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Projection rejects through a stable class (ErrProjectionFailed,
// ErrInvalidPlacement) and formats the cause, which is itself a published
// sentinel of this package. Formatting the cause with %v left a caller able to
// read the class and unable to classify the cause: a retry/classification path
// could only tell "unknown placement" from "invalid anchor" by matching text.
// %w keeps the cause reachable through errors.Is and renders the same message.
func Test_PublishedCauseIdentitySurvivesProjectionWraps(t *testing.T) {
	t.Parallel()

	// An empty role has no semantic identity, so identity derivation refuses it.
	invalidRole := aipapi.Call{Messages: []aipapi.Message{
		{Role: "", Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello"}}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "done"}}},
	}}
	tagged := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "tagged"}}}
	id, err := conversationprojection.MessageIdentityOf(tagged)
	require.NoError(t, err)
	snap := conversationprojection.Snapshot{StateRevision: 1, NeverBackend: []conversationprojection.Tag{{Identity: id, Reason: "reason_one"}}}
	badAnchor := conversationprojection.MessageAnchor{Identity: "not-an-identity", Occurrence: 1}

	_, _, projectErr := conversationprojection.Project(invalidRole, conversationprojection.Snapshot{})
	_, _, reassertErr := conversationprojection.Reassert(invalidRole, snap, nil, aipapi.Call{})
	placementErr := (conversationprojection.Placement{Kind: conversationprojection.PlacementAfterMessage, Anchor: &badAnchor}).Validate()

	require.Error(t, projectErr)
	assert.ErrorIs(t, projectErr, conversationprojection.ErrProjectionFailed, "the outer class stays primary")
	assert.ErrorIs(t, projectErr, conversationprojection.ErrInvalidRole, "the identity cause stays classifiable")

	require.Error(t, reassertErr)
	assert.ErrorIs(t, reassertErr, conversationprojection.ErrProjectionFailed, "the outer reassert class stays primary")
	assert.ErrorIs(t, reassertErr, conversationprojection.ErrInvalidRole, "the identity cause stays classifiable")

	require.Error(t, placementErr)
	assert.ErrorIs(t, placementErr, conversationprojection.ErrInvalidPlacement, "the placement class stays primary")
	assert.ErrorIs(t, placementErr, conversationprojection.ErrInvalidMessageIdentity, "the anchor cause stays classifiable")

	// %w renders exactly what %v rendered: class, separator, cause text.
	for _, tc := range []struct {
		err   error
		class error
	}{
		{projectErr, conversationprojection.ErrProjectionFailed},
		{placementErr, conversationprojection.ErrInvalidPlacement},
	} {
		assert.True(t, strings.HasPrefix(tc.err.Error(), tc.class.Error()+": "),
			"the rendered text keeps the class prefix: %q", tc.err.Error())
	}
}
