package leglifecycle

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestCoordinator_StartALeg_AliasIsolation_DistinctScopes(t *testing.T) {
	t.Parallel()
	c := NewCoordinator(CoordinatorConfig{CancelTimeout: time.Second})

	// Parent lifecycle scope that exists before authoritative creation.
	parent := c.StartALeg("parent-a-leg-isolated")
	parentBLeg := &recordingBLeg{}
	if err := parent.RegisterBLeg(context.Background(), BLegHandle{ID: "b-parent", Attempt: parentBLeg}); err != nil {
		t.Fatalf("parent RegisterBLeg: %v", err)
	}

	// Authoritative A-leg must not alias client-provided hint; StartALeg is single-ID
	// and hint remains correlation-only. Even after hint was registered, new ID must yield distinct scope.
	child := c.StartALeg("auth-a-leg-isolated")
	if child == parent {
		t.Fatalf("authoritative A-leg must not share *ALeg with hint: got same pointer %p", child)
	}

	childBLeg := &recordingBLeg{}
	if err := child.RegisterBLeg(context.Background(), BLegHandle{ID: "b-child", Attempt: childBLeg}); err != nil {
		t.Fatalf("child RegisterBLeg: %v", err)
	}

	if err := c.CancelALeg(context.Background(), "auth-a-leg-isolated", CancelCause{Kind: CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg child: %v", err)
	}
	if got := childBLeg.calls(); !reflect.DeepEqual(got, []string{"cancel:explicit", "close"}) {
		t.Fatalf("child b-leg calls = %v want [cancel:explicit close]", got)
	}
	if got := parentBLeg.calls(); len(got) != 0 {
		t.Fatalf("parent b-leg must remain untouched after child cancel, got %v", got)
	}

	// Parent must still be registered and cancellable.
	if err := c.CancelALeg(context.Background(), "parent-a-leg-isolated", CancelCause{Kind: CancelClientGone}); err != nil {
		t.Fatalf("CancelALeg parent: %v", err)
	}
	if got := parentBLeg.calls(); !reflect.DeepEqual(got, []string{"cancel:client_gone", "close"}) {
		t.Fatalf("parent b-leg after parent cancel = %v want [cancel:client_gone close]", got)
	}
}

func TestCoordinator_EndALeg_RemovesOnlyTargetID(t *testing.T) {
	t.Parallel()
	c := NewCoordinator(CoordinatorConfig{CancelTimeout: time.Second})

	parent := c.StartALeg("parent-end-isolated")
	parentBLeg := &recordingBLeg{}
	if err := parent.RegisterBLeg(context.Background(), BLegHandle{ID: "b-parent", Attempt: parentBLeg}); err != nil {
		t.Fatalf("parent RegisterBLeg: %v", err)
	}

	child := c.StartALeg("auth-end-isolated")
	if child == parent {
		t.Fatalf("authoritative A-leg must not share *ALeg with hint before End: got same pointer")
	}
	childBLeg := &recordingBLeg{}
	if err := child.RegisterBLeg(context.Background(), BLegHandle{ID: "b-child", Attempt: childBLeg}); err != nil {
		t.Fatalf("child RegisterBLeg: %v", err)
	}

	c.EndALeg("auth-end-isolated")

	// Ending child must not delete parent mapping. Parent must still be cancellable via coordinator.
	if err := c.CancelALeg(context.Background(), "parent-end-isolated", CancelCause{Kind: CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg parent after child End: %v", err)
	}
	if got := parentBLeg.calls(); !reflect.DeepEqual(got, []string{"cancel:explicit", "close"}) {
		t.Fatalf("parent b-leg after child End and parent cancel = %v want [cancel:explicit close]", got)
	}
	if got := childBLeg.calls(); len(got) != 0 {
		t.Fatalf("child b-leg must not be affected by parent cancel after child End already removed child scope, got %v", got)
	}

	// Child ID should be removable independently; cancelling it now creates a fresh scope without touching parent state.
	// Parent is already canceled and removed from blegs slice, but its entry was correctly targeted.
	// Verify child entry is gone: a new StartALeg with child's ID must yield fresh ALeg, not resurrected parent state.
	fresh := c.StartALeg("auth-end-isolated")
	if fresh == child {
		t.Fatalf("fresh StartALeg after End must not return same pointer as old canceled child")
	}
	if fresh == parent {
		t.Fatalf("fresh StartALeg for child ID must not alias parent")
	}
}

func TestCoordinator_StartALeg_SameIDIdempotentDoesNotAliasOther(t *testing.T) {
	t.Parallel()
	c := NewCoordinator(CoordinatorConfig{})

	a1 := c.StartALeg("dup-id")
	a2 := c.StartALeg("dup-id")
	if a1 != a2 {
		t.Fatalf("same ID StartALeg must be idempotent: got %p vs %p", a1, a2)
	}
	b := c.StartALeg("other-id")
	if b == a1 {
		t.Fatalf("different ID must not alias: other %p same as dup %p", b, a1)
	}
}
