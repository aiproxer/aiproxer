package conversationprojection_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestReassert_PreservesStructuralCause proves the final conversation-view
// reassertion keeps the proxy-owned structural cause of the request it
// rewrites. The reassertion rebuilds the steering placement of a late call, so
// a cause that a client request-part hook cleared must stay cleared, and a
// cause the proxy recorded must survive into the provider-bound request.
func TestReassert_PreservesStructuralCause(t *testing.T) {
	t.Parallel()

	sys := aipapi.Message{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("sys")}}
	user := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("user")}}
	overlay := conversationview.SteeringOverlay{
		OverlayID: "ov-structural-cause", Revision: 1, SlotOrdinal: 1, Active: true,
		Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "steering"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "r",
	}
	snap := conversationview.Snapshot{
		StateRevision: 1,
		Steering:      []conversationprojection.Overlay{overlay.ToProjectionOverlay()},
	}
	clientCall := aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{user}}
	baseline, ev, err := conversationprojection.Project(clientCall, snap)
	if err != nil {
		t.Fatalf("Project baseline: %v", err)
	}
	filtered, err := conversationprojection.FilterNeverBackend(clientCall, snap)
	if err != nil {
		t.Fatalf("FilterNeverBackend: %v", err)
	}

	// A late call that carries the proxy's structural cause keeps it.
	late := aipapi.CloneCall(baseline)
	late.StructuralCause = "compaction"
	repaired, _, err := conversationprojection.Reassert(late, snap, ev.Provenance, filtered)
	if err != nil {
		t.Fatalf("Reassert: %v", err)
	}
	if repaired.StructuralCause != "compaction" {
		t.Fatalf("structural cause = %q, want compaction", repaired.StructuralCause)
	}

	// A late call whose transformation cleared the cause stays cleared: the
	// reassertion never invents one.
	cleared := aipapi.CloneCall(baseline)
	repaired, _, err = conversationprojection.Reassert(cleared, snap, ev.Provenance, filtered)
	if err != nil {
		t.Fatalf("Reassert: %v", err)
	}
	if repaired.StructuralCause != "" {
		t.Fatalf("structural cause = %q, want empty", repaired.StructuralCause)
	}
}
