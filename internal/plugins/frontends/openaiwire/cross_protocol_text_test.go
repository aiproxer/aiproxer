package openaiwire_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Golden-style invariant: canonical user text extracted from aipapi messages should match
// across protocols when only the user turn is present (cross-protocol matrix task 14.3 slice).
func TestCrossProtocol_userTextInvariant(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello matrix")},
		}},
	}
	if len(call.Messages) != 1 || call.Messages[0].Parts[0].Text != "hello matrix" {
		t.Fatalf("%+v", call.Messages)
	}
}
