package controlplane_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase35_DualPlaneReportInputsFromFacts_ReconstructsPlanes(t *testing.T) {
	t.Parallel()
	facts := []metering.Fact{
		phase35ReportFact("fe-in", "req", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendIngress, 100, 0, 0, ""),
		phase35ReportFact("fe-out", "req", 2, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 20, 500, "USD"),
		phase35ReportFact("be-in", "att-1", 1, metering.PerspectiveOperator, metering.BoundaryBackendIngress, 40, 0, 0, ""),
		phase35ReportFact("be-out", "att-1", 2, metering.PerspectiveOperator, metering.BoundaryBackendEgress, 0, 22, 200, "USD"),
		phase35ReportFact("be-in-2", "att-2", 1, metering.PerspectiveOperator, metering.BoundaryBackendIngress, 5, 0, 0, ""),
		func() metering.Fact {
			f := phase35ReportFact("be-out-2", "att-2", 2, metering.PerspectiveOperator, metering.BoundaryBackendEgress, 0, 1, 15, "USD")
			f.Surfaced = metering.SurfacedNo
			return f
		}(),
	}
	in, err := controlplane.DualPlaneReportInputsFromFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	if !in.Customer.FrontendIngressTokens.Present || in.Customer.FrontendIngressTokens.Value != 100 {
		t.Fatalf("customer FE ingress=%#v", in.Customer.FrontendIngressTokens)
	}
	if !in.Customer.FrontendEgressTokens.Present || in.Customer.FrontendEgressTokens.Value != 20 {
		t.Fatalf("customer FE egress=%#v", in.Customer.FrontendEgressTokens)
	}
	if !in.Customer.Money.Present || in.Customer.Money.NanoUnits != 500 {
		t.Fatalf("customer money=%#v", in.Customer.Money)
	}
	if !in.Operator.BackendIngressTokens.Present || in.Operator.BackendIngressTokens.Value != 45 {
		t.Fatalf("operator BE ingress=%#v", in.Operator.BackendIngressTokens)
	}
	if !in.Operator.BackendEgressTokens.Present || in.Operator.BackendEgressTokens.Value != 23 {
		t.Fatalf("operator BE egress=%#v", in.Operator.BackendEgressTokens)
	}
	if !in.Operator.Money.Present || in.Operator.Money.NanoUnits != 215 {
		t.Fatalf("operator money=%#v", in.Operator.Money)
	}
	if !in.Compression.FrontendInput.Present || in.Compression.FrontendInput.Value != 100 {
		t.Fatalf("compression FE=%#v", in.Compression.FrontendInput)
	}
	if !in.Compression.BackendInput.Present || in.Compression.BackendInput.Value != 45 {
		t.Fatalf("compression BE=%#v", in.Compression.BackendInput)
	}
	if !in.RoutingOverhead.AttemptCount.Present || in.RoutingOverhead.AttemptCount.Value != 2 {
		t.Fatalf("attempt count=%#v", in.RoutingOverhead.AttemptCount)
	}
	if !in.RoutingOverhead.NonSurfacedAttempts.Present || in.RoutingOverhead.NonSurfacedAttempts.Value != 1 {
		t.Fatalf("non-surfaced=%#v", in.RoutingOverhead.NonSurfacedAttempts)
	}
	if !in.RoutingOverhead.OverheadCost.Present || in.RoutingOverhead.OverheadCost.NanoUnits != 15 {
		t.Fatalf("overhead cost=%#v", in.RoutingOverhead.OverheadCost)
	}
	if in.Completeness != controlplane.ReportCompletenessComplete {
		t.Fatalf("completeness=%q", in.Completeness)
	}
	savings, err := controlplane.CalculateCompressionTokenSavings(in.Compression)
	if err != nil {
		t.Fatal(err)
	}
	if savings.Quantity.Value != 55 {
		t.Fatalf("savings=%d", savings.Quantity.Value)
	}
	overhead, err := controlplane.CalculateRoutingOverheadTotal(in.RoutingOverhead)
	if err != nil {
		t.Fatal(err)
	}
	if overhead.Money.NanoUnits != 15 {
		t.Fatalf("routing overhead total=%#v", overhead.Money)
	}
}

func TestPhase35_DualPlaneReportInputsFromFacts_MissingIngressIncomplete(t *testing.T) {
	t.Parallel()
	facts := []metering.Fact{
		phase35ReportFact("fe-out", "req", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 4, 0, ""),
	}
	in, err := controlplane.DualPlaneReportInputsFromFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	if in.Completeness != controlplane.ReportCompletenessIncomplete {
		t.Fatalf("completeness=%q want incomplete", in.Completeness)
	}
	if in.Customer.FrontendIngressTokens.Present {
		t.Fatal("must not invent FE ingress tokens")
	}
	if in.LegacyProvenance != controlplane.ReportLegacyProvenanceHistoricalWithoutIngress {
		t.Fatalf("legacy provenance=%q", in.LegacyProvenance)
	}
}

func TestPhase35_DualPlaneReportInputsFromFacts_CurrencyMismatchSafeError(t *testing.T) {
	t.Parallel()
	facts := []metering.Fact{
		phase35ReportFact("be-out-1", "a1", 1, metering.PerspectiveOperator, metering.BoundaryBackendEgress, 0, 1, 10, "USD"),
		phase35ReportFact("be-out-2", "a2", 1, metering.PerspectiveOperator, metering.BoundaryBackendEgress, 0, 1, 10, "EUR"),
	}
	_, err := controlplane.DualPlaneReportInputsFromFacts(facts)
	if err == nil {
		t.Fatal("expected currency mismatch error")
	}
}

func phase35ReportFact(id, stream string, seq int64, pers metering.EconomicPerspective, bound metering.Boundary, in, out, money int64, currency string) metering.Fact {
	qs := make([]metering.Quantity, 0, 2)
	if in > 0 || bound == metering.BoundaryFrontendIngress || bound == metering.BoundaryBackendIngress {
		if in > 0 {
			qs = append(qs, metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: in, Present: true})
		}
	}
	if out > 0 {
		qs = append(qs, metering.Quantity{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: out, Present: true})
	}
	f := metering.Fact{
		FactID: id, StreamID: stream, Sequence: seq,
		Kind: metering.FactKindCumulative, Perspective: pers, Boundary: bound,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Correlation: metering.Correlation{RequestID: "r1", ALegID: "a1", BLegID: stream},
		Source:      metering.SourceObserved, Authority: metering.AuthorityAuthoritative,
		Presence: metering.PresencePresent, RecordedAt: time.Unix(seq, 0).UTC(),
		Quantities: qs, Surfaced: metering.SurfacedYes,
	}
	if money != 0 || currency != "" {
		f.Money = &metering.MoneyObservation{NanoUnits: money, Currency: currency, Present: true, Source: metering.SourceObserved}
	}
	return f
}
