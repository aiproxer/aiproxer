package controlplane_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase35_Report_FEEgressPlusBEIngress_WithoutFEIngress_Incomplete(t *testing.T) {
	t.Parallel()
	facts := []metering.Fact{
		phase35ReportFact("fe-out", "req", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 4, 0, ""),
		phase35ReportFact("be-in", "att", 1, metering.PerspectiveOperator, metering.BoundaryBackendIngress, 8, 0, 0, ""),
	}
	in, err := controlplane.DualPlaneReportInputsFromFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	if in.Completeness != controlplane.ReportCompletenessIncomplete {
		t.Fatalf("completeness=%q want incomplete (customer missing FE ingress)", in.Completeness)
	}
	if in.LegacyProvenance != controlplane.ReportLegacyProvenanceHistoricalWithoutIngress {
		t.Fatalf("provenance=%q", in.LegacyProvenance)
	}
}

func TestPhase35_Report_FEIngressPlusBEEgress_WithoutBEIngress_Incomplete(t *testing.T) {
	t.Parallel()
	facts := []metering.Fact{
		phase35ReportFact("fe-in", "req", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendIngress, 10, 0, 0, ""),
		phase35ReportFact("be-out", "att", 1, metering.PerspectiveOperator, metering.BoundaryBackendEgress, 0, 3, 10, "USD"),
	}
	in, err := controlplane.DualPlaneReportInputsFromFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	if in.Completeness != controlplane.ReportCompletenessIncomplete {
		t.Fatalf("completeness=%q want incomplete (operator missing BE ingress)", in.Completeness)
	}
}

func TestPhase35_Report_BothPlanesWithMatchingIngress_Complete(t *testing.T) {
	t.Parallel()
	facts := []metering.Fact{
		phase35ReportFact("fe-in", "req", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendIngress, 10, 0, 0, ""),
		phase35ReportFact("fe-out", "req", 2, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 2, 0, ""),
		phase35ReportFact("be-in", "att", 1, metering.PerspectiveOperator, metering.BoundaryBackendIngress, 4, 0, 0, ""),
		phase35ReportFact("be-out", "att", 2, metering.PerspectiveOperator, metering.BoundaryBackendEgress, 0, 2, 1, "USD"),
	}
	in, err := controlplane.DualPlaneReportInputsFromFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	if in.Completeness != controlplane.ReportCompletenessComplete {
		t.Fatalf("completeness=%q", in.Completeness)
	}
}
