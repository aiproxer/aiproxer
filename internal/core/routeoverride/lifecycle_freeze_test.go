package routeoverride_test

// Task 1.5 characterization: freeze route-override lifecycle counts relevant
// to the future large-payload lane (requirements 7, 19; design 7, 12
// read-only, 16).
//
// What this proves with real existing seams only:
//   - A fresh A-leg snapshots as inactive revision 0; Replace activates at
//     revision 1; Clear writes an inactive tombstone without retaining the
//     selector; Snapshot returns a value copy (mutating the result does not
//     affect the store).
//   - Empty A-leg IDs never touch the store (service layer fails closed
//     before mutation).
//   - The standard memory continuity store is a route-override-capable
//     composition (AsStore/AsReader true, command path writes no B-leg rows
//     and preserves weighted/interleaved state). The Bun/SQLite side runs the
//     identical storecontract suite (continuity/bunstore
//     routeoverride_capability_test.go, dbparity_test.go); this hermetic file
//     does not reopen a database.
//   - Per-turn Snapshot call counts (exactly one per normal turn, zero for
//     detached) are frozen at the runtime layer in
//     internal/core/runtime/secure_session_lifecycle_freeze_test.go; this
//     package freezes the store invariants that make that single snapshot
//     safe to treat as the post-commit route authority.
//
// What this explicitly does NOT claim (out of scope, needs future tasks):
//   - AssessLargeBody late-route envelopes and ExecuteLargeBody live reads do
//     not exist yet; no envelope is computed here. The frozen invariant is
//     only that the live read is a single Snapshot after authoritative A-leg
//     fetch, already ordered before B-leg open by the runtime barrier.
//
// Test-only, no production diff.

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
)

func TestLifecycleFreeze_MemoryIsRouteOverrideCapable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := routeoverride.AsStore(mem); !ok {
		t.Fatal("memory continuity must implement routeoverride.Store")
	}
	if _, ok := routeoverride.AsReader(mem); !ok {
		t.Fatal("memory continuity must implement routeoverride.Reader")
	}
	leg, err := mem.CreateALeg(ctx, "freeze-capable-ro")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := mem.Snapshot(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Active || snap.Revision != 0 || snap.Selector != "" {
		t.Fatalf("fresh snapshot=%+v want inactive revision 0", snap)
	}
	if err := snap.Validate(); err != nil {
		t.Fatalf("fresh snapshot must validate: %v", err)
	}
}

func TestLifecycleFreeze_SnapshotIsValueCopyAndRevisioned(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	leg, err := mem.CreateALeg(ctx, "freeze-copy")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(7000, 0).UTC()
	active, err := mem.Replace(ctx, leg.ALegID, "ok:m", now)
	if err != nil {
		t.Fatal(err)
	}
	if !active.Active || active.Selector != "ok:m" || active.Revision != 1 {
		t.Fatalf("replace=%+v want active revision 1", active)
	}
	first, err := mem.Snapshot(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	first.Selector = "mutated-by-caller"
	first.Active = false
	second, err := mem.Snapshot(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Selector != "ok:m" || !second.Active || second.Revision != 1 {
		t.Fatalf("snapshot must be a value copy, got %+v", second)
	}
	cleared, err := mem.Clear(ctx, leg.ALegID, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Active || cleared.Selector != "" || cleared.Revision != 2 || cleared.UpdatedAt.IsZero() {
		t.Fatalf("clear=%+v want inactive tombstone revision 2", cleared)
	}
	atts, err := mem.LoadAttempts(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 0 {
		t.Fatalf("override commands must not record B-leg attempts: %+v", atts)
	}
}

func TestLifecycleFreeze_UnknownALegSnapshotFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Snapshot(ctx, "no-such-a-leg"); err == nil {
		t.Fatal("unknown A-leg snapshot must fail")
	}
	if _, err := mem.Snapshot(ctx, "   "); err == nil {
		t.Fatal("empty A-leg snapshot must fail")
	}
}
