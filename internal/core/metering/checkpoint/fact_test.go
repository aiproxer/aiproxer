package checkpoint_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

type memRecorder struct {
	facts []metering.Fact
}

func (m *memRecorder) Append(ctx context.Context, fact metering.Fact) error {
	_ = ctx
	m.facts = append(m.facts, fact)
	return nil
}

func TestFactFromEgress_BackendAndFrontend(t *testing.T) {
	t.Parallel()
	beIn, err := checkpoint.CaptureBackendIngress(checkpoint.BackendIngressInput{
		Call:         aipapi.Call{ID: "r1", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}}},
		AttemptID:    "att-1",
		BLegID:       "b-1",
		CheckpointID: "be-in",
		StreamID:     "be-stream",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	beFact, err := checkpoint.FactFromEgress(checkpoint.EgressFactInput{
		Checkpoint: checkpoint.BackendEgressCheckpoint(beIn, metering.AttemptOutcomeLoser, metering.SurfacedNo),
		FactID:     "be-eg-1",
		Sequence:   1,
		Quantities: checkpoint.QuantitiesFromTokenCounts(10, 2, 0, 0, 0, 12, true),
		Outcome:    metering.AttemptOutcomeLoser,
		Surfaced:   metering.SurfacedNo,
		Now:        time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if beFact.Boundary != metering.BoundaryBackendEgress || beFact.AttemptOutcome != metering.AttemptOutcomeLoser {
		t.Fatalf("%+v", beFact)
	}

	feIn, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "r1"},
		CheckpointID: "fe",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	feFact, err := checkpoint.FactFromEgress(checkpoint.EgressFactInput{
		Checkpoint: checkpoint.FrontendEgressCheckpoint(feIn),
		FactID:     "fe-eg-1",
		Sequence:   1,
		Quantities: checkpoint.QuantitiesFromTokenCounts(10, 5, 0, 0, 0, 15, true),
		Now:        time.Unix(3, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if feFact.Boundary != metering.BoundaryFrontendEgress {
		t.Fatal(feFact.Boundary)
	}

	rec := &memRecorder{}
	if err := rec.Append(context.Background(), beFact); err != nil {
		t.Fatal(err)
	}
	if err := rec.Append(context.Background(), feFact); err != nil {
		t.Fatal(err)
	}
	if len(rec.facts) != 2 {
		t.Fatalf("facts=%d", len(rec.facts))
	}
	if rec.facts[0].Boundary != metering.BoundaryBackendEgress || rec.facts[1].Boundary != metering.BoundaryFrontendEgress {
		t.Fatal("order")
	}
}
