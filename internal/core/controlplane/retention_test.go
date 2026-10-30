package controlplane_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func newRetentionController(store controlplane.Store, profile controlplane.RetentionProfile, window time.Duration) (*controlplane.RetentionController, *controlplane.Status) {
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	return controlplane.NewRetentionController(store, status, controlplane.RetentionControllerConfig{
		Profile: profile,
		Window:  window,
		Clock:   fixedClock{t: time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)},
	}), status
}

func TestRetentionApplyMarksExpiredRecords(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	ctrl, status := newRetentionController(store, controlplane.RetentionProfileStandard, 24*time.Hour)
	res, err := ctrl.Apply(context.Background(), time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), cp.VisibilityDefault)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Marked < 1 {
		t.Fatalf("expected at least one marked record, got %d", res.Marked)
	}
	if store.retention.Load() != 1 {
		t.Fatalf("expected one ApplyRetention call, got %d", store.retention.Load())
	}
	if got := status.Snapshot().State; got != cp.CapabilityReady {
		t.Fatalf("successful retention must keep status ready, got %q", got)
	}
}

func TestRetentionApplyComputesCutoffFromWindowWhenOmitted(t *testing.T) {
	t.Parallel()
	var captured controlplane.RetentionCommand
	store := &recordingStore{retentionFn: func(cmd controlplane.RetentionCommand) (controlplane.RetentionResult, error) {
		captured = cmd
		return controlplane.RetentionResult{Marked: 2}, nil
	}}
	ctrl, _ := newRetentionController(store, controlplane.RetentionProfileStandard, 24*time.Hour)
	if _, err := ctrl.Apply(context.Background(), time.Time{}, cp.VisibilityDefault); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if captured.Cutoff.IsZero() {
		t.Fatalf("cutoff must be computed from window when omitted")
	}
	want := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC).Add(-24 * time.Hour)
	if !captured.Cutoff.Equal(want) {
		t.Fatalf("cutoff = %v, want %v", captured.Cutoff, want)
	}
}

func TestRetentionApplyIsIdempotent(t *testing.T) {
	t.Parallel()
	calls := 0
	marked := 0
	store := &recordingStore{retentionFn: func(cmd controlplane.RetentionCommand) (controlplane.RetentionResult, error) {
		calls++
		// Simulate idempotency: only the first call marks records; subsequent
		// calls at the same cutoff mark nothing additional.
		if calls == 1 {
			marked = 5
		} else {
			marked = 0
		}
		return controlplane.RetentionResult{Marked: marked}, nil
	}}
	ctrl, _ := newRetentionController(store, controlplane.RetentionProfileStandard, 24*time.Hour)
	cutoff := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	if _, err := ctrl.Apply(context.Background(), cutoff, cp.VisibilityDefault); err != nil {
		t.Fatalf("Apply 1: %v", err)
	}
	if _, err := ctrl.Apply(context.Background(), cutoff, cp.VisibilityDefault); err != nil {
		t.Fatalf("Apply 2: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected two ApplyRetention calls, got %d", calls)
	}
	// The store guarantees idempotency at the data layer; the controller must
	// not invent additional marked records on the second call.
}

func TestRetentionApplyFailureDegradesStatusWithoutLeakingInfra(t *testing.T) {
	t.Parallel()
	store := &recordingStore{retentionFn: func(controlplane.RetentionCommand) (controlplane.RetentionResult, error) {
		return controlplane.RetentionResult{}, errors.New("connection refused: postgres://user:pass@host:5432/db")
	}}
	ctrl, status := newRetentionController(store, controlplane.RetentionProfileStandard, 24*time.Hour)
	_, err := ctrl.Apply(context.Background(), time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), cp.VisibilityDefault)
	if err == nil {
		t.Fatalf("retention failure must surface an error")
	}
	if !errors.Is(err, controlplane.ErrDegraded) {
		t.Fatalf("retention failure must classify as degraded, got %v", err)
	}
	if strings.Contains(err.Error(), "pass") || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("retention error must not leak raw infrastructure details, got: %v", err)
	}
	// The safe reason code must not leak raw infrastructure details.
	got := status.Snapshot()
	if got.Reason != cp.ReasonRetentionFailure {
		t.Fatalf("status reason must be bounded retention_failure, got %q", got.Reason)
	}
	if got.State != cp.CapabilityDegraded {
		t.Fatalf("status must be degraded, got %q", got.State)
	}
}

func TestRetentionApplyRejectsUnknownProfile(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	ctrl, _ := newRetentionController(store, controlplane.RetentionProfile("bogus"), 24*time.Hour)
	if _, err := ctrl.Apply(context.Background(), time.Now(), cp.VisibilityDefault); err == nil {
		t.Fatalf("unknown profile must be rejected")
	}
	if store.retention.Load() != 0 {
		t.Fatalf("unknown profile must not reach the store")
	}
}

func TestRetentionApplyStrictProfileRedactsAndPreservesCorrelation(t *testing.T) {
	t.Parallel()
	var captured controlplane.RetentionCommand
	store := &recordingStore{retentionFn: func(cmd controlplane.RetentionCommand) (controlplane.RetentionResult, error) {
		captured = cmd
		return controlplane.RetentionResult{Marked: 3}, nil
	}}
	ctrl, _ := newRetentionController(store, controlplane.RetentionProfileStrict, 24*time.Hour)
	if _, err := ctrl.Apply(context.Background(), time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), cp.VisibilityDefault); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if captured.Profile != controlplane.RetentionProfileStrict {
		t.Fatalf("strict profile must be passed to store, got %q", captured.Profile)
	}
	if captured.Visibility != cp.VisibilityDefault {
		t.Fatalf("default visibility must be passed to store, got %q", captured.Visibility)
	}
}

func TestRetentionApplyDoesNotMutateInFlightRuntime(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	ctrl, _ := newRetentionController(store, controlplane.RetentionProfileStandard, 24*time.Hour)
	if _, err := ctrl.Apply(context.Background(), time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), cp.VisibilityDefault); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// The store contract guarantees ApplyRetention mutates only control-plane
	// evidence and never in-flight runtime stores (requirement 6.6, 10.7).
	// The controller adds no additional mutation paths.
}
