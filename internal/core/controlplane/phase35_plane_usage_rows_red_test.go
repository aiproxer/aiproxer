package controlplane_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase35_UsageRows_PlaneAwarePartial_FEEgressPlusBEIngress(t *testing.T) {
	t.Parallel()
	facts := []metering.Fact{
		phase35Fact("fe-out", "mixed", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 4),
		phase35Fact("be-in", "mixed", 2, metering.PerspectiveOperator, metering.BoundaryBackendIngress, 8, 0),
	}
	rows := controlplane.UsageRowsFromMeteringFacts(facts)
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	var customerPartial, operatorRecorded bool
	for _, r := range rows {
		switch r.Perspective {
		case cp.UsagePerspectiveCustomer:
			customerPartial = r.EvidenceState == cp.EvidencePartial
		case cp.UsagePerspectiveOperator:
			operatorRecorded = r.EvidenceState == cp.EvidenceRecorded
		}
	}
	if !customerPartial {
		t.Fatalf("customer row must be partial; rows=%#v", rows)
	}
	if !operatorRecorded {
		t.Fatalf("operator BE ingress row must stay recorded; rows=%#v", rows)
	}
}

func TestPhase35_Bridge_PlaneAwareIncompleteAcrossPages(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{
		StoreID: "plane-pages", MaxPageSize: 1, DefaultPageSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	facts := []metering.Fact{
		phase35Fact("fe-out", "mixed", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 4),
		func() metering.Fact {
			f := phase35Fact("be-in", "mixed", 2, metering.PerspectiveOperator, metering.BoundaryBackendIngress, 8, 0)
			f.Lifecycle = metering.LifecycleBackendAttempt
			f.Correlation.AttemptID = "att-1"
			f.Correlation.BLegID = "b1"
			return f
		}(),
	}
	for _, f := range facts {
		if err := s.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	q := metering.Query{StreamID: "mixed", Limit: 1}
	rows, page, err := controlplane.ListUsageRowsFromMetering(ctx, s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || page.NextCursor != "" {
		t.Fatalf("rows=%d next=%q", len(rows), page.NextCursor)
	}
	in, _, err := controlplane.DualPlaneReportInputsFromMetering(ctx, s, q)
	if err != nil {
		t.Fatal(err)
	}
	if in.Completeness != cp.ReportCompletenessIncomplete {
		t.Fatalf("completeness=%q want incomplete across pages", in.Completeness)
	}
}
