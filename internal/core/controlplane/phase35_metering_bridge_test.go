package controlplane_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase35_ListUsageRowsFromMetering_IncompleteLegacyProjection(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "bridge-incomplete"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f := metering.Fact{
		FactID: "fe-out", StreamID: "cust-req", Sequence: 1,
		Kind: metering.FactKindCumulative, Perspective: metering.PerspectiveCustomer,
		Boundary: metering.BoundaryFrontendEgress, Lifecycle: metering.LifecycleLogicalRequest,
		Correlation: metering.Correlation{RequestID: "r1", ALegID: "a1"},
		Source:      metering.SourceObserved, Authority: metering.AuthorityAuthoritative,
		Presence: metering.PresencePresent, RecordedAt: time.Unix(1, 0).UTC(),
		Quantities: []metering.Quantity{{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 4, Present: true}},
	}
	if err := s.Append(ctx, f); err != nil {
		t.Fatal(err)
	}
	rows, page, err := controlplane.ListUsageRowsFromMetering(ctx, s, metering.Query{StreamID: "cust-req", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 || len(rows) != 1 {
		t.Fatalf("facts=%d rows=%d", len(page.Facts), len(rows))
	}
	if rows[0].EvidenceState != cp.EvidencePartial {
		t.Fatalf("evidence=%q want partial", rows[0].EvidenceState)
	}
}

func TestPhase35_DualPlaneReportInputsFromMetering_CompleteStream(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "bridge-complete"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	facts := []metering.Fact{
		phase35Fact("fe-in", "cust-req", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendIngress, 10, 0),
		phase35Fact("fe-out", "cust-req", 2, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 3),
	}
	for _, f := range facts {
		if err := s.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	in, _, err := controlplane.DualPlaneReportInputsFromMetering(ctx, s, metering.Query{StreamID: "cust-req", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if in.Completeness != cp.ReportCompletenessComplete {
		t.Fatalf("completeness=%q", in.Completeness)
	}
	if !in.Customer.FrontendIngressTokens.Present || in.Customer.FrontendIngressTokens.Value != 10 {
		t.Fatalf("ingress=%#v", in.Customer.FrontendIngressTokens)
	}
}
