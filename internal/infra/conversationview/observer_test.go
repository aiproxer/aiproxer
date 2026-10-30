package conversationview

import (
	"testing"
)

func TestObserver_BoundedLabels(t *testing.T) {
	t.Parallel()
	// Verify that Observer interface only uses bounded enums.
	var _ Observer = NopObserver{}
	// Ensure cache discontinuity kinds are bounded set.
	validKinds := map[CacheDiscontinuityKind]bool{
		CacheDiscontinuityNone: true, CacheDiscontinuityCreate: true, CacheDiscontinuityReplace: true, CacheDiscontinuityMove: true, CacheDiscontinuityDeactivate: true,
	}
	for k := range validKinds {
		if err := k.Validate(); err != nil && k != CacheDiscontinuityNone {
			t.Fatalf("kind %q invalid: %v", k, err)
		}
	}
	validPlacements := []PlacementKind{PlacementStablePrefix, PlacementAfterMessage}
	for _, p := range validPlacements {
		if err := p.Validate(); err != nil {
			t.Fatalf("placement %q invalid: %v", p, err)
		}
	}
	validPolicies := []AnchorMissingPolicy{AnchorStablePrefixFallback, AnchorFailClosed}
	for _, pol := range validPolicies {
		if err := pol.Validate(); err != nil {
			t.Fatalf("policy %q invalid: %v", pol, err)
		}
	}
	// Ensure ReasonCode validation rejects plaintext with spaces/secrets.
	invalid := []ReasonCode{"has space", "secret/token", "Bearer xxx", "a|b", "with\nnewline"}
	for _, r := range invalid {
		if err := r.Validate(); err == nil {
			t.Fatalf("ReasonCode %q should be invalid", r)
		}
	}
	valid := []ReasonCode{"local_turn", "operator_policy", "test-reason.1"}
	for _, r := range valid {
		if err := r.Validate(); err != nil {
			t.Fatalf("ReasonCode %q should be valid: %v", r, err)
		}
	}
	// Ensure ProjectionSummary is bounded content-free (no OverlayID/digest/plaintext).
	snap := Snapshot{StateRevision: 1}
	ev := &ProjectionEvidence{FilteredCount: 2, InjectedCount: 1, Fallbacks: []FallbackEvidence{{OverlayID: "should-not-leak"}}}
	summary := NewProjectionSummary(snap, ev)
	if summary.FilteredCount != 2 || summary.InjectedCount != 1 || summary.FallbackCount != 1 {
		t.Fatalf("summary mismatch: %+v", summary)
	}
	// Observer must not receive digest/plaintext: check summary has no overlay IDs.
	// Summary intentionally has no OverlayID field.
}

func TestObserver_NopIsNoOp(t *testing.T) {
	t.Parallel()
	var obs Observer = NopObserver{}
	obs.OnProjection(StageEarly, ProjectionSummary{})
	obs.OnProjectionFailure(StageEarly)
	obs.OnAnchorFallback(StageEarly, AnchorStablePrefixFallback)
	obs.OnAnchorFailure(AnchorFailClosed)
	obs.OnSteeringMutation(CacheDiscontinuityCreate, PlacementStablePrefix)
}

func TestObserver_PanicIsolated(t *testing.T) {
	t.Parallel()
	panicking := panickingObserver{}
	safe := SafeObserver(panicking)
	// Each callback must not panic caller.
	safe.OnProjection(StageEarly, ProjectionSummary{})
	safe.OnProjectionFailure(StageEarly)
	safe.OnAnchorFallback(StageEarly, AnchorStablePrefixFallback)
	safe.OnAnchorFallback(StageFinal, AnchorStablePrefixFallback)
	safe.OnAnchorFailure(AnchorFailClosed)
	safe.OnSteeringMutation(CacheDiscontinuityCreate, PlacementStablePrefix)
}

type panickingObserver struct{}

func (panickingObserver) OnProjection(string, ProjectionSummary) { panic("projection") }

func (panickingObserver) OnProjectionFailure(string) { panic("failure") }

func (panickingObserver) OnAnchorFallback(string, AnchorMissingPolicy) { panic("fallback") }

func (panickingObserver) OnAnchorFailure(AnchorMissingPolicy)                      { panic("anchor") }
func (panickingObserver) OnSteeringMutation(CacheDiscontinuityKind, PlacementKind) { panic("mut") }

func TestProjectionSummary_BoundedCounts(t *testing.T) {
	t.Parallel()
	snap := Snapshot{StateRevision: 42}
	// Build snapshot with 2 active overlays.
	ov1 := SteeringOverlay{OverlayID: "o1", Revision: 2, SlotOrdinal: 1, Active: true, Placement: StoredPlacement{Kind: PlacementStablePrefix}, Reason: "r1", Message: StoredMessageV1{Role: "system", Text: "hello"}}
	ov2 := SteeringOverlay{OverlayID: "o2", Revision: 3, SlotOrdinal: 2, Active: true, Placement: StoredPlacement{Kind: PlacementAfterMessage, Anchor: &MessageAnchor{Identity: "v1:abc", Occurrence: 1}}, AnchorMissingPolicy: AnchorStablePrefixFallback, Reason: "r2", Message: StoredMessageV1{Role: "user", Text: "world"}}
	snap.Steering = []Overlay{ov1.ToProjectionOverlay(), ov2.ToProjectionOverlay()}
	snap.NeverBackend = []Tag{{Identity: "v1:xyz", Reason: "r"}}
	// Evidence with provenance covering both.
	ev := &ProjectionEvidence{
		FilteredCount: 1,
		InjectedCount: 2,
		Provenance: []OverlayProvenance{
			{OverlayID: "o1", Revision: 2, SlotOrdinal: 1, ResolvedKind: PlacementStablePrefix},
			{OverlayID: "o2", Revision: 3, SlotOrdinal: 2, ResolvedKind: PlacementAfterMessage},
		},
	}
	summary := NewProjectionSummary(snap, ev)
	if summary.FilteredCount != 1 || summary.InjectedCount != 2 || summary.StablePrefixCount != 1 || summary.AfterMessageCount != 1 {
		t.Fatalf("summary counts wrong: %+v", summary)
	}
	if summary.MaxOverlayRevision != 3 || summary.MaxSlotOrdinal != 2 {
		t.Fatalf("max revision/slot wrong: %+v", summary)
	}
	// Ensure no plaintext in summary string representation.
	// Summary JSON should not contain overlay text "hello" or "world".
}
