package controlplane_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase35_UsageRowsFromMeteringFacts_CompleteIngressRecorded(t *testing.T) {
	t.Parallel()
	facts := []metering.Fact{
		phase35Fact("fe-in", "cust-req", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendIngress, 10, 0),
		phase35Fact("fe-out", "cust-req", 2, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 4),
	}
	rows := controlplane.UsageRowsFromMeteringFacts(facts)
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	for _, r := range rows {
		if r.EvidenceState != cp.EvidenceRecorded {
			t.Fatalf("complete stream row evidence=%q want recorded", r.EvidenceState)
		}
	}
}

func TestPhase35_UsageRowsFromMeteringFacts_MissingIngressMarkedPartial(t *testing.T) {
	t.Parallel()
	facts := []metering.Fact{
		phase35Fact("fe-out", "cust-req", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 4),
	}
	rows := controlplane.UsageRowsFromMeteringFacts(facts)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	if rows[0].EvidenceState != cp.EvidencePartial {
		t.Fatalf("evidence=%q want partial (incomplete without ingress)", rows[0].EvidenceState)
	}
	if rows[0].InputTokens != 0 || rows[0].TokenPresence.InputTokens {
		t.Fatal("must not invent ingress input tokens")
	}
}

func TestPhase35_UsageRowFromMeteringFact_LegacyAloneStillRecorded(t *testing.T) {
	t.Parallel()
	// Singular projector preserves prior behavior; incompleteness requires stream context.
	f := phase35Fact("fe-out", "cust-req", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 4)
	row := controlplane.UsageRowFromMeteringFact(f)
	if row.EvidenceState != cp.EvidenceRecorded {
		t.Fatalf("singular projector evidence=%q", row.EvidenceState)
	}
}

func phase35Fact(id, stream string, seq int64, pers metering.EconomicPerspective, bound metering.Boundary, in, out int64) metering.Fact {
	qs := make([]metering.Quantity, 0, 2)
	if in > 0 {
		qs = append(qs, metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: in, Present: true})
	}
	if out > 0 {
		qs = append(qs, metering.Quantity{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: out, Present: true})
	}
	return metering.Fact{
		FactID: id, StreamID: stream, Sequence: seq,
		Kind: metering.FactKindCumulative, Perspective: pers, Boundary: bound,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Correlation: metering.Correlation{RequestID: "r1", ALegID: "a1"},
		Source:      metering.SourceObserved, Authority: metering.AuthorityAuthoritative,
		Presence: metering.PresencePresent, RecordedAt: time.Unix(seq, 0).UTC(),
		Quantities: qs,
	}
}
