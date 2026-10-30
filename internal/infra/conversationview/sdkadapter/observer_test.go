package sdkadapter

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

type recordingObserver struct {
	muts []struct {
		kind      conversationview.CacheDiscontinuityKind
		placement conversationview.PlacementKind
	}
}

func (r *recordingObserver) OnProjection(string, conversationview.ProjectionSummary)       {}
func (r *recordingObserver) OnProjectionFailure(string)                                    {}
func (r *recordingObserver) OnAnchorFallback(string, conversationview.AnchorMissingPolicy) {}
func (r *recordingObserver) OnAnchorFailure(conversationview.AnchorMissingPolicy)          {}
func (r *recordingObserver) OnSteeringMutation(k conversationview.CacheDiscontinuityKind, p conversationview.PlacementKind) {
	r.muts = append(r.muts, struct {
		kind      conversationview.CacheDiscontinuityKind
		placement conversationview.PlacementKind
	}{k, p})
}

func TestWriter_Observer_WiresCacheDiscontinuity(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLegID := "a-leg-obs-test"
	if err := store.CreateALeg(ctx, aLegID); err != nil {
		t.Fatalf("create aleg: %v", err)
	}
	obs := &recordingObserver{}
	w, err := NewWriterWithObserver(store, aLegID, nil, obs)
	if err != nil {
		t.Fatalf("NewWriterWithObserver: %v", err)
	}
	// Create stable_prefix overlay -> should emit create/stable_prefix
	st, err := w.Put(ctx, steering.PutRequest{
		OverlayID:           "ov1",
		Message:             steering.Message{Role: aipapi.RoleSystem, Text: "steer one"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "test_reason",
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	_ = st
	if len(obs.muts) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(obs.muts))
	}
	if obs.muts[0].kind != conversationview.CacheDiscontinuityCreate || obs.muts[0].placement != conversationview.PlacementStablePrefix {
		t.Fatalf("mutation mismatch: %+v", obs.muts[0])
	}
	// No-op put (same content) should not emit.
	obs.muts = nil
	_, err = w.Put(ctx, steering.PutRequest{
		OverlayID:           "ov1",
		Message:             steering.Message{Role: aipapi.RoleSystem, Text: "steer one"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "test_reason",
	})
	if err != nil {
		t.Fatalf("put no-op: %v", err)
	}
	if len(obs.muts) != 0 {
		t.Fatalf("no-op should not emit, got %d", len(obs.muts))
	}
	// Replace content -> should emit replace
	_, err = w.Put(ctx, steering.PutRequest{
		OverlayID:           "ov1",
		Message:             steering.Message{Role: aipapi.RoleSystem, Text: "steer two"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "test_reason",
	})
	if err != nil {
		t.Fatalf("put replace: %v", err)
	}
	if len(obs.muts) != 1 || obs.muts[0].kind != conversationview.CacheDiscontinuityReplace {
		t.Fatalf("replace mutation mismatch: %+v", obs.muts)
	}
	// Deactivate -> should emit deactivate
	obs.muts = nil
	_, err = w.Deactivate(ctx, "ov1")
	if err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if len(obs.muts) != 1 || obs.muts[0].kind != conversationview.CacheDiscontinuityDeactivate {
		t.Fatalf("deactivate mismatch: %+v", obs.muts)
	}
	// Ensure no OverlayID leaked as label: observer only receives bounded enums, not IDs.
	for _, m := range obs.muts {
		if string(m.kind) == "ov1" || string(m.placement) == "ov1" {
			t.Fatalf("high cardinality leak: %+v", m)
		}
	}
}

func TestWriter_NilObserver_NoPanic(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLegID := "a-leg-nil-obs"
	_ = store.CreateALeg(ctx, aLegID)
	w, _ := NewWriter(store, aLegID, nil)
	_, err := w.Put(ctx, steering.PutRequest{
		OverlayID:           "ov2",
		Message:             steering.Message{Role: aipapi.RoleUser, Text: "hi"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "r",
	})
	if err != nil {
		t.Fatalf("put with nil observer: %v", err)
	}
}

func TestWriter_Observer_PanicIsolated(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLegID := "a-leg-panic-obs"
	_ = store.CreateALeg(ctx, aLegID)
	obs := panickingObserver{}
	w, _ := NewWriterWithObserver(store, aLegID, nil, obs)
	// Put should succeed despite observer panic.
	_, err := w.Put(ctx, steering.PutRequest{
		OverlayID:           "ov-panic",
		Message:             steering.Message{Role: aipapi.RoleSystem, Text: "hello panic"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "r",
	})
	if err != nil {
		t.Fatalf("put with panicking observer should not fail: %v", err)
	}
	_, err = w.Deactivate(ctx, "ov-panic")
	if err != nil {
		t.Fatalf("deactivate with panicking observer should not fail: %v", err)
	}
}

type panickingObserver struct{}

func (panickingObserver) OnProjection(string, conversationview.ProjectionSummary) { panic("boom") }
func (panickingObserver) OnProjectionFailure(string)                              { panic("boom") }
func (panickingObserver) OnAnchorFallback(string, conversationview.AnchorMissingPolicy) {
	panic("boom")
}
func (panickingObserver) OnAnchorFailure(conversationview.AnchorMissingPolicy) { panic("boom") }
func (panickingObserver) OnSteeringMutation(conversationview.CacheDiscontinuityKind, conversationview.PlacementKind) {
	panic("boom")
}
