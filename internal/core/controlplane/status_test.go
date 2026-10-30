package controlplane_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestStatusStartsDisabled(t *testing.T) {
	t.Parallel()
	s := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityDisabled, RecordingPolicy: cp.RecordingDisabled})
	got := s.Snapshot()
	if got.State != cp.CapabilityDisabled {
		t.Fatalf("initial state lost: %q", got.State)
	}
	if got.RecordingPolicy != cp.RecordingDisabled {
		t.Fatalf("recording policy lost: %q", got.RecordingPolicy)
	}
}

func TestStatusTransitionsToReady(t *testing.T) {
	t.Parallel()
	s := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityDisabled})
	s.SetReady(cp.RecordingBestEffort, time.Now())
	if got := s.Snapshot().State; got != cp.CapabilityReady {
		t.Fatalf("expected ready, got %q", got)
	}
}

func TestStatusRecordFailureTransitionsToDegraded(t *testing.T) {
	t.Parallel()
	now := time.Now()
	s := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	s.RecordFailure(cp.ReasonRecordingFailure, now)
	got := s.Snapshot()
	if got.State != cp.CapabilityDegraded {
		t.Fatalf("expected degraded after recording failure, got %q", got.State)
	}
	if got.Reason != cp.ReasonRecordingFailure {
		t.Fatalf("reason lost: %q", got.Reason)
	}
	if !got.LastFailureAt.Equal(now) {
		t.Fatalf("last failure time lost: %v vs %v", got.LastFailureAt, now)
	}
}

func TestStatusUnavailableDoesNotDegradeReadySilently(t *testing.T) {
	t.Parallel()
	s := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	s.SetUnavailable(cp.ReasonBackingUnavailable, time.Now())
	if got := s.Snapshot().State; got != cp.CapabilityUnavailable {
		t.Fatalf("expected unavailable, got %q", got)
	}
}

func TestStatusDisabledWinsOverDegraded(t *testing.T) {
	t.Parallel()
	s := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady})
	s.Disable(time.Now())
	if got := s.Snapshot().State; got != cp.CapabilityDisabled {
		t.Fatalf("disable must override ready/degraded, got %q", got)
	}
	// a degraded failure must not revive a disabled capability
	s.RecordFailure(cp.ReasonRecordingFailure, time.Now())
	if got := s.Snapshot().State; got != cp.CapabilityDisabled {
		t.Fatalf("disabled must not degrade, got %q", got)
	}
}

func TestStatusReasonCodesAreBoundedAndSafe(t *testing.T) {
	t.Parallel()
	s := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady})
	s.RecordFailure(cp.ReasonQueryFailure, time.Now())
	got := s.Snapshot()
	if !got.Reason.IsKnown() {
		t.Fatalf("reason must be a bounded known code, got %q", got.Reason)
	}
}
