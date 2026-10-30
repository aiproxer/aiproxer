package runtime

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestFreezeBackendIngress_RejectsWidening(t *testing.T) {
	t.Parallel()
	authorized := aipapi.Call{
		ID: "r",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("ok")},
		}},
	}
	snap, err := checkpoint.CaptureBackendIngress(checkpoint.BackendIngressInput{
		Call:         authorized,
		AttemptID:    "att",
		BLegID:       "b",
		CheckpointID: "cp",
		StreamID:     "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	wide := aipapi.CloneCall(authorized)
	wide.Messages[0].Parts[0].Text = "widened"
	if err := checkpoint.AssertNotWidened(snap.Call, wide); !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
		t.Fatalf("got %v", err)
	}
}

// TestOpenPathWidening_IndependentFreezeCopy mirrors executor_open_attempt:
// store/compare use a CloneCall freeze so in-place mutation of the live call
// after freeze is detectable (requirement 7.5; avoids Store→Assert tautology).
func TestOpenPathWidening_IndependentFreezeCopy(t *testing.T) {
	t.Parallel()
	openCall := aipapi.Call{
		ID: "r",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("ok")},
		}},
	}
	authorizedFreeze := aipapi.CloneCall(openCall)
	holder := &checkpoint.RequestHolder{}
	if _, err := holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:         authorizedFreeze,
		AttemptID:    "b1",
		BLegID:       "b1",
		CheckpointID: "be",
		StreamID:     "be",
	}); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.AssertNotWidened(authorizedFreeze, openCall); err != nil {
		t.Fatalf("pre-mutation: %v", err)
	}
	openCall.Messages[0].Parts[0].Text = "widened after freeze"
	if err := checkpoint.AssertNotWidened(authorizedFreeze, openCall); !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
		t.Fatalf("post-mutation got %v", err)
	}
	// Wire path must keep the freeze, not the mutated live call.
	if authorizedFreeze.Messages[0].Parts[0].Text != "ok" {
		t.Fatal("freeze mutated")
	}
}
