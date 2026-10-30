package controlplane_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestConcurrencyAuthorityEnumsKnown(t *testing.T) {
	t.Parallel()
	if !controlplane.ConcurrencyAuthorityReady.IsKnown() {
		t.Fatal("ready must be known")
	}
	if controlplane.ConcurrencyAuthorityState("bogus").IsKnown() {
		t.Fatal("bogus state must be unknown")
	}
	if !controlplane.ConcurrencyLeaseActive.IsKnown() {
		t.Fatal("active lease state must be known")
	}
	row := controlplane.ConcurrencyLeaseRow{
		LeaseID:   "cls_1",
		RequestID: "req-1",
		State:     controlplane.ConcurrencyLeaseActive,
		ExpiresAt: time.Unix(100, 0).UTC(),
	}
	if row.LeaseID == "" || !row.State.IsKnown() {
		t.Fatalf("row=%+v", row)
	}
}
