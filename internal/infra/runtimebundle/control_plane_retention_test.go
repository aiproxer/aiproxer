package runtimebundle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// fakeRetentionStore is a minimal controlplane.Store fake for runtimebundle
// startup-retention tests. It cannot reuse the unexported recordingStore from
// the controlplane package, so it is defined here in the internal test package.
// ApplyRetention counts calls and returns the configured error (or success).
type fakeRetentionStore struct {
	applyErr error
	applied  atomic.Int64
}

func (s *fakeRetentionStore) Append(context.Context, cp.Event) (cp.RecordResult, error) {
	return cp.RecordResult{}, nil
}

func (s *fakeRetentionStore) Sessions(context.Context, cp.SessionQuery) (cp.Page[cp.SessionSummary], error) {
	return cp.Page[cp.SessionSummary]{}, nil
}

func (s *fakeRetentionStore) Attempts(context.Context, cp.AttemptQuery) (cp.Page[cp.AttemptRow], error) {
	return cp.Page[cp.AttemptRow]{}, nil
}

func (s *fakeRetentionStore) Usage(context.Context, cp.UsageQuery) (cp.Page[cp.UsageRow], error) {
	return cp.Page[cp.UsageRow]{}, nil
}

func (s *fakeRetentionStore) UsageAggregate(context.Context, cp.UsageAggregateQuery) (cp.Page[cp.UsageAggregate], error) {
	return cp.Page[cp.UsageAggregate]{}, nil
}

func (s *fakeRetentionStore) PolicyAudit(context.Context, cp.EvidenceQuery) (cp.Page[cp.PolicyAuditRow], error) {
	return cp.Page[cp.PolicyAuditRow]{}, nil
}

func (s *fakeRetentionStore) Events(context.Context, cp.EventQuery) (cp.Page[cp.Event], error) {
	return cp.Page[cp.Event]{}, nil
}

func (s *fakeRetentionStore) ApplyRetention(_ context.Context, _ controlplane.RetentionCommand) (controlplane.RetentionResult, error) {
	s.applied.Add(1)
	return controlplane.RetentionResult{}, s.applyErr
}
func (s *fakeRetentionStore) CheckReadiness(context.Context) error { return nil }

var _ controlplane.Store = (*fakeRetentionStore)(nil)

func newRetentionRuntimeForTest(store *fakeRetentionStore) (*controlPlaneRuntime, *controlplane.Status) {
	status := controlplane.NewStatus(cp.CapabilityStatus{
		State:           cp.CapabilityReady,
		RecordingPolicy: cp.RecordingBestEffort,
	})
	ctrl := controlplane.NewRetentionController(store, status, controlplane.RetentionControllerConfig{
		Profile: controlplane.RetentionProfileStandard,
		Window:  time.Hour,
		Clock:   controlplane.SystemClock{},
	})
	return &controlPlaneRuntime{enabled: true, retention: ctrl, status: status}, status
}

func TestControlPlaneRuntime_RunStartupRetention_AppliesAndDegradesOnFailure(t *testing.T) {
	t.Parallel()
	store := &fakeRetentionStore{applyErr: errors.New("boom")}
	rt, status := newRetentionRuntimeForTest(store)

	rt.runStartupRetention(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	if got := store.applied.Load(); got != 1 {
		t.Fatalf("expected exactly one ApplyRetention call, got %d", got)
	}
	snap := status.Snapshot()
	if snap.State != cp.CapabilityDegraded {
		t.Fatalf("expected degraded state after retention failure, got %q", snap.State)
	}
	if snap.Reason != cp.ReasonRetentionFailure {
		t.Fatalf("expected retention_failure reason, got %q", snap.Reason)
	}
}

func TestControlPlaneRuntime_RunStartupRetention_NoopWhenRetentionNil(t *testing.T) {
	t.Parallel()
	rt := &controlPlaneRuntime{enabled: true, retention: nil}
	// Must not panic; no assertions beyond not panicking.
	rt.runStartupRetention(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	// A nil *controlPlaneRuntime must also be safe.
	var nilRT *controlPlaneRuntime
	nilRT.runStartupRetention(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestControlPlaneRuntime_RunStartupRetention_NilLogSafe(t *testing.T) {
	t.Parallel()
	store := &fakeRetentionStore{applyErr: errors.New("boom")}
	rt, status := newRetentionRuntimeForTest(store)

	// Pass a nil logger; must not panic and must still apply + degrade.
	rt.runStartupRetention(context.Background(), nil)

	if got := store.applied.Load(); got != 1 {
		t.Fatalf("expected exactly one ApplyRetention call with nil log, got %d", got)
	}
	snap := status.Snapshot()
	if snap.State != cp.CapabilityDegraded {
		t.Fatalf("expected degraded state with nil log, got %q", snap.State)
	}
	if snap.Reason != cp.ReasonRetentionFailure {
		t.Fatalf("expected retention_failure reason with nil log, got %q", snap.Reason)
	}
}
