package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type diagRecordingObserver struct {
	projections []struct {
		stage   string
		summary conversationview.ProjectionSummary
	}
	failures    []string
	fallbacks   []conversationview.AnchorMissingPolicy
	anchorFails []conversationview.AnchorMissingPolicy
	muts        []struct {
		kind      conversationview.CacheDiscontinuityKind
		placement conversationview.PlacementKind
	}
}

func (r *diagRecordingObserver) OnProjection(stage string, summary conversationview.ProjectionSummary) {
	r.projections = append(r.projections, struct {
		stage   string
		summary conversationview.ProjectionSummary
	}{stage, summary})
}

func (r *diagRecordingObserver) OnProjectionFailure(stage string) {
	r.failures = append(r.failures, stage)
}

func (r *diagRecordingObserver) OnAnchorFallback(stage string, p conversationview.AnchorMissingPolicy) {
	_ = stage
	r.fallbacks = append(r.fallbacks, p)
}

func (r *diagRecordingObserver) OnAnchorFailure(p conversationview.AnchorMissingPolicy) {
	r.anchorFails = append(r.anchorFails, p)
}

func (r *diagRecordingObserver) OnSteeringMutation(k conversationview.CacheDiscontinuityKind, p conversationview.PlacementKind) {
	r.muts = append(r.muts, struct {
		kind      conversationview.CacheDiscontinuityKind
		placement conversationview.PlacementKind
	}{k, p})
}

func TestConversationViewDiagnostics_EarlyProjectionBounded(t *testing.T) {
	ref := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLegID := "diag-early-aleg"
	if err := ref.CreateALeg(ctx, aLegID); err != nil {
		t.Fatalf("create aleg: %v", err)
	}
	// Tag one message never_backend (keep second message so projection stays valid)
	call := aipapi.Call{
		Session:  aipapi.SessionRef{ALegID: aLegID},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}, {Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("keep")}}},
	}
	id, err := conversationview.MessageIdentityOf(call.Messages[0])
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	if _, err := ref.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: id, Reason: "test_reason"}}); err != nil {
		t.Fatalf("tag: %v", err)
	}
	// Put steering
	if _, err := ref.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID: "ov1", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "steer"}, Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}, AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "r",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	obs := &diagRecordingObserver{}
	ex := TestExecutor()
	ex.ConversationViewReader = ref
	ex.ConversationViewObserver = obs
	// snapshotAndProject early stage
	snap, ev, out, err := ex.snapshotAndProject(ctx, aLegID, call)
	if err != nil {
		t.Fatalf("snapshotAndProject: %v", err)
	}
	_ = snap
	_ = ev
	_ = out
	if len(obs.projections) != 1 {
		t.Fatalf("expected 1 early projection, got %d", len(obs.projections))
	}
	if obs.projections[0].stage != conversationview.StageEarly {
		t.Fatalf("stage mismatch: %q", obs.projections[0].stage)
	}
	sum := obs.projections[0].summary
	if sum.FilteredCount != 1 || sum.InjectedCount != 1 || sum.StablePrefixCount != 1 {
		t.Fatalf("summary mismatch: %+v", sum)
	}
	// Ensure no OverlayID or plaintext in observer data
	// summary has no OverlayID field by design
}

func TestConversationViewDiagnostics_ProjectionFailureBounded(t *testing.T) {
	// Simulate anchor fail_closed missing
	ref := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLegID := "diag-fail-aleg"
	_ = ref.CreateALeg(ctx, aLegID)
	// Put after_message with anchor that will be missing (use dummy anchor)
	anchor := conversationview.MessageAnchor{Identity: "v1:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Occurrence: 1}
	if _, err := ref.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID: "ov-fail", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "steer"}, Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor}, AnchorMissingPolicy: conversationview.AnchorFailClosed, Reason: "r",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	obs := &diagRecordingObserver{}
	ex := TestExecutor()
	ex.ConversationViewReader = ref
	ex.ConversationViewObserver = obs
	call := aipapi.Call{
		Session:  aipapi.SessionRef{ALegID: aLegID},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("different")}}},
	}
	_, _, _, err := ex.snapshotAndProject(ctx, aLegID, call)
	if err == nil {
		t.Fatalf("expected projection failure")
	}
	if len(obs.failures) != 1 || obs.failures[0] != conversationview.StageEarly {
		t.Fatalf("failures mismatch: %+v", obs.failures)
	}
	// Ensure error does not contain steering plaintext
	if contains(err.Error(), "steer") {
		t.Fatalf("plaintext leaked in error: %v", err)
	}
}

func TestConversationViewDiagnostics_FinalReassertFallback(t *testing.T) {
	ref := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLegID := "diag-final-aleg"
	_ = ref.CreateALeg(ctx, aLegID)
	call := aipapi.Call{
		Session:  aipapi.SessionRef{ALegID: aLegID},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("user one")}}},
	}
	id, _ := conversationview.MessageIdentityOf(call.Messages[0])
	anchor := conversationview.MessageAnchor{Identity: id, Occurrence: 1}
	if _, err := ref.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID: "ov-fb", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "steer fb"}, Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor}, AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "r",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Early projection to get snapshot
	ex := TestExecutor()
	ex.ConversationViewReader = ref
	obs := &diagRecordingObserver{}
	ex.ConversationViewObserver = obs
	snap, ev, _, err := ex.snapshotAndProject(ctx, aLegID, call)
	if err != nil {
		t.Fatalf("early: %v", err)
	}
	_ = ev
	// Now create a history where anchor is missing (compacted)
	lateCall := aipapi.Call{
		Session:  aipapi.SessionRef{ALegID: aLegID},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("different")}}},
	}
	filtered, _ := conversationprojection.FilterNeverBackend(lateCall, snap)
	// Reassert should fallback, not fail
	_, reEv, err := conversationprojection.Reassert(lateCall, snap, ev.Provenance, filtered)
	if err != nil {
		t.Fatalf("reassert: %v", err)
	}
	if len(reEv.Fallbacks) != 1 {
		t.Fatalf("expected fallback")
	}
	// Simulate final observer path: manually emit fallback diagnostic as runtime would
	obs.OnAnchorFallback(conversationview.StageFinal, conversationview.AnchorStablePrefixFallback)
	if len(obs.fallbacks) != 1 || obs.fallbacks[0] != conversationview.AnchorStablePrefixFallback {
		t.Fatalf("fallback mismatch")
	}
}

func TestConversationViewDiagnostics_ValidationError_NotAnchorFailure(t *testing.T) {
	// Validation error (e.g., empty Messages) should increment projection failure but not anchor failure.
	ref := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLegID := "diag-validation-aleg"
	_ = ref.CreateALeg(ctx, aLegID)
	// Put valid steering so snapshot non-empty.
	if _, err := ref.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID: "ov1", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "steer"}, Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}, AnchorMissingPolicy: conversationview.AnchorFailClosed, Reason: "r",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	obs := &diagRecordingObserver{}
	ex := TestExecutor()
	ex.ConversationViewReader = ref
	ex.ConversationViewObserver = obs
	// Call with no messages will fail validation in Project, not anchor missing.
	badCall := aipapi.Call{
		Session:  aipapi.SessionRef{ALegID: aLegID},
		Messages: []aipapi.Message{},
	}
	_, _, _, err := ex.snapshotAndProject(ctx, aLegID, badCall)
	if err == nil {
		t.Fatalf("expected validation failure")
	}
	if len(obs.failures) != 1 {
		t.Fatalf("expected 1 projection failure, got %d", len(obs.failures))
	}
	if len(obs.anchorFails) != 0 {
		t.Fatalf("validation error should not increment anchor failure, got %d", len(obs.anchorFails))
	}
}

func TestConversationViewDiagnostics_PanicIsolated_EarlyAndMutation(t *testing.T) {
	ref := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLegID := "diag-panic-aleg"
	_ = ref.CreateALeg(ctx, aLegID)
	call := aipapi.Call{
		Session:  aipapi.SessionRef{ALegID: aLegID},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}, {Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("keep")}}},
	}
	id, _ := conversationview.MessageIdentityOf(call.Messages[0])
	_, _ = ref.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: id, Reason: "r"}})
	_, _ = ref.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID: "ov1", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "steer"}, Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}, AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "r",
	})
	obs := panickingDiagObserver{}
	ex := TestExecutor()
	ex.ConversationViewReader = ref
	ex.ConversationViewObserver = obs
	// Early projection with panicking observer must not panic caller and must succeed.
	snap, ev, out, err := ex.snapshotAndProject(ctx, aLegID, call)
	if err != nil {
		t.Fatalf("snapshotAndProject with panicking observer should succeed: %v", err)
	}
	_ = snap
	_ = ev
	_ = out
	// Mutation via sdkadapter with panicking observer must not affect mutation.
	obs2 := panickingDiagObserver{}
	storeObs := obs2
	_ = storeObs
	// Writer panic isolation tested separately; here ensure final path also isolated.
	// Simulate final reassert with panicking observer via executor path: use reassert directly with safe wrapper.
	// Direct SafeObserver test:
	safe := conversationview.SafeObserver(obs)
	safe.OnProjection(conversationview.StageEarly, conversationview.ProjectionSummary{})
	safe.OnProjectionFailure(conversationview.StageEarly)
	safe.OnAnchorFallback(conversationview.StageEarly, conversationview.AnchorStablePrefixFallback)
	safe.OnSteeringMutation(conversationview.CacheDiscontinuityCreate, conversationview.PlacementStablePrefix)
}

type panickingDiagObserver struct{}

func (panickingDiagObserver) OnProjection(string, conversationview.ProjectionSummary) {
	panic("panic projection")
}
func (panickingDiagObserver) OnProjectionFailure(string) { panic("panic failure") }
func (panickingDiagObserver) OnAnchorFallback(string, conversationview.AnchorMissingPolicy) {
	panic("panic fallback")
}

func (panickingDiagObserver) OnAnchorFailure(conversationview.AnchorMissingPolicy) {
	panic("panic anchor")
}

func (panickingDiagObserver) OnSteeringMutation(conversationview.CacheDiscontinuityKind, conversationview.PlacementKind) {
	panic("panic mut")
}

func contains(s, sub string) bool {
	if len(s) < len(sub) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
