package reconcile_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/reconcile"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestStream_RestartNoDoubleCountAndUnresolved(t *testing.T) {
	t.Parallel()
	store, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "recon"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	egress := metering.Fact{
		FactID: "eg-1", StreamID: "s-recon", Sequence: 1,
		Kind: metering.FactKindDelta, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Source: metering.SourceObserved, Authority: metering.AuthorityAuthoritative,
		Presence: metering.PresencePresent, RecordedAt: time.Unix(1, 0).UTC(),
		Quantities: []metering.Quantity{{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 5, Present: true}},
	}
	unavail := egress
	unavail.FactID = "u-1"
	unavail.Sequence = 2
	unavail.Kind = metering.FactKindUnavailable
	unavail.Presence = metering.PresenceUnknown
	unavail.Quantities = nil
	if err := store.Append(ctx, egress); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, unavail); err != nil {
		t.Fatal(err)
	}

	r1, err := reconcile.Stream(ctx, store, "s-recon", reconcile.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := reconcile.Stream(ctx, store, "s-recon", reconcile.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if r1.Snapshot.Quantities[metering.ComponentInputToken] != 5 {
		t.Fatalf("snap=%v", r1.Snapshot.Quantities)
	}
	if r2.Snapshot.Quantities[metering.ComponentInputToken] != r1.Snapshot.Quantities[metering.ComponentInputToken] {
		t.Fatal("restart double-count")
	}
	if len(r1.Unresolved) == 0 {
		t.Fatal("expected unresolved orphan/unavailable")
	}
	foundOrphan := false
	foundUnavail := false
	for _, u := range r1.Unresolved {
		if u.Reason == "orphan_egress_without_ingress" {
			foundOrphan = true
		}
		if u.FactID == "u-1" {
			foundUnavail = true
		}
	}
	if !foundOrphan || !foundUnavail {
		t.Fatalf("unresolved=%+v", r1.Unresolved)
	}
}
