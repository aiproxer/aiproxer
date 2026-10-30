package controlplane_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// Phase 4.5 RED/GREEN: terminal_recovery readiness component and pending-work
// query class (requirements 12.1–12.4, 12.6; design EconomicControlReady).

func TestPhase45_ReadinessComponentTerminalRecoveryExists(t *testing.T) {
	t.Parallel()
	id := controlplane.ReadinessComponentTerminalRecovery
	if id != "terminal_recovery" {
		t.Fatalf("id=%q want terminal_recovery", id)
	}
}

func TestPhase45_QueryClassPendingTerminalWork(t *testing.T) {
	t.Parallel()
	c := controlplane.QueryClassPendingTerminalWork
	if c != "pending_terminal_work" {
		t.Fatalf("class=%q", c)
	}
	if !c.IsKnown() {
		t.Fatal("pending_terminal_work must be a known query class")
	}
}

func TestPhase45_AggregateDegradesWhenTerminalRecoveryPending(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0).UTC()
	components := []controlplane.ReadinessComponentStatus{
		{
			Component:        controlplane.ReadinessComponentUsageAuthority,
			State:            controlplane.CapabilityReady,
			EnforcementScope: controlplane.EnforcementScopeDistributedStrict,
		},
		{
			Component:        controlplane.ReadinessComponentTerminalRecovery,
			State:            controlplane.CapabilityDegraded,
			Reason:           controlplane.ReasonPendingTerminalWork,
			EnforcementScope: controlplane.EnforcementScopeAdvisorySingleProcess,
		},
	}
	posture := controlplane.AggregateProtectedTrafficPosture(components, now)
	if posture.State != controlplane.CapabilityDegraded {
		t.Fatalf("state=%q want degraded", posture.State)
	}
	if !posture.MayServeStrict {
		t.Fatal("pending post-output work degrades but must not block strict when authority is ready")
	}
}

func TestPhase45_ReasonPendingTerminalWorkIsKnown(t *testing.T) {
	t.Parallel()
	if !controlplane.ReasonPendingTerminalWork.IsKnown() {
		t.Fatal("ReasonPendingTerminalWork must be documented")
	}
}
