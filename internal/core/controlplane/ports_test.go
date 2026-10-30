package controlplane_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// stubStore is a test-only adapter proving the core-owned Store port is
// implementable by a memory-style adapter without SQL, Bun, or HTTP types.
type stubStore struct{}

func (stubStore) Append(context.Context, cp.Event) (cp.RecordResult, error) {
	return cp.RecordResult{ID: cp.EventID{StoreID: "stub", Sequence: 1}, Dedupe: cp.DedupeInserted}, nil
}

func (stubStore) Sessions(context.Context, cp.SessionQuery) (cp.Page[cp.SessionSummary], error) {
	return cp.Page[cp.SessionSummary]{Visibility: cp.VisibilityDefault}, nil
}

func (stubStore) Attempts(context.Context, cp.AttemptQuery) (cp.Page[cp.AttemptRow], error) {
	return cp.Page[cp.AttemptRow]{Visibility: cp.VisibilityDefault}, nil
}

func (stubStore) Usage(context.Context, cp.UsageQuery) (cp.Page[cp.UsageRow], error) {
	return cp.Page[cp.UsageRow]{Visibility: cp.VisibilityDefault}, nil
}

func (stubStore) UsageAggregate(context.Context, cp.UsageAggregateQuery) (cp.Page[cp.UsageAggregate], error) {
	return cp.Page[cp.UsageAggregate]{Visibility: cp.VisibilityDefault}, nil
}

func (stubStore) PolicyAudit(context.Context, cp.EvidenceQuery) (cp.Page[cp.PolicyAuditRow], error) {
	return cp.Page[cp.PolicyAuditRow]{Visibility: cp.VisibilityDefault}, nil
}

func (stubStore) Events(context.Context, cp.EventQuery) (cp.Page[cp.Event], error) {
	return cp.Page[cp.Event]{Visibility: cp.VisibilityDefault}, nil
}

func (stubStore) ApplyRetention(context.Context, controlplane.RetentionCommand) (controlplane.RetentionResult, error) {
	return controlplane.RetentionResult{}, nil
}
func (stubStore) CheckReadiness(context.Context) error { return nil }

func TestStorePortIsImplementable(t *testing.T) {
	t.Parallel()
	var _ controlplane.EventAppender = stubStore{}
	var _ controlplane.QuerySource = stubStore{}
	var _ controlplane.RetentionApplier = stubStore{}
	var _ controlplane.ReadinessProbe = stubStore{}
	var _ controlplane.Store = stubStore{}
}

func TestClockAndIDGeneratorPortsAreImplementable(t *testing.T) {
	t.Parallel()
	var _ controlplane.Clock = controlplane.SystemClock{}
	gen := controlplane.NewMonotonicIDGenerator("mem")
	var _ controlplane.EventIDGenerator = gen
	id := gen.NewEventID(42)
	if id.StoreID != "mem" || id.Sequence != 42 {
		t.Fatalf("NewEventID lost inputs: %#v", id)
	}
}

func TestSystemClockReturnsRealTime(t *testing.T) {
	t.Parallel()
	c := controlplane.SystemClock{}
	before := time.Now()
	got := c.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("SystemClock.Now out of range: %v", got)
	}
}

func TestRetentionCommandCarriesProfileAndCutoff(t *testing.T) {
	t.Parallel()
	cmd := controlplane.RetentionCommand{
		Cutoff:     time.Now(),
		Profile:    controlplane.RetentionProfileStandard,
		Visibility: cp.VisibilityDefault,
	}
	if cmd.Profile != controlplane.RetentionProfileStandard {
		t.Fatalf("profile lost: %q", cmd.Profile)
	}
}
