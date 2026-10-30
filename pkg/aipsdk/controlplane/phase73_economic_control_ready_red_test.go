package controlplane_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestPhase73_EconomicControlReadyWhenRequiredComponentsReady(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000730, 0).UTC()
	report := controlplane.ReadinessReport{
		ExecutableGeneration: controlplane.ExecutableGenerationStatus{
			State: controlplane.CapabilityReady, Version: "g1", LastUpdatedAt: now,
		},
		Components: []controlplane.ReadinessComponentStatus{
			{Component: controlplane.ReadinessComponentExecutableGeneration, State: controlplane.CapabilityReady, EnforcementScope: controlplane.EnforcementScopeDistributedStrict},
			{Component: controlplane.ReadinessComponentRequestCoordinator, State: controlplane.CapabilityReady, EnforcementScope: controlplane.EnforcementScopeDistributedStrict},
			{Component: controlplane.ReadinessComponentAttemptCoordinator, State: controlplane.CapabilityReady, EnforcementScope: controlplane.EnforcementScopeDistributedStrict},
			{Component: controlplane.ReadinessComponentConcurrencyAuthority, State: controlplane.CapabilityReady, EnforcementScope: controlplane.EnforcementScopeDistributedStrict},
			{Component: controlplane.ReadinessComponentUsageAuthority, State: controlplane.CapabilityReady, EnforcementScope: controlplane.EnforcementScopeDistributedStrict},
			{Component: controlplane.ReadinessComponentMeteringJournal, State: controlplane.CapabilityReady, EnforcementScope: controlplane.EnforcementScopeDistributedStrict},
			{Component: controlplane.ReadinessComponentTerminalRecovery, State: controlplane.CapabilityReady},
		},
		Posture: controlplane.ProtectedTrafficPosture{State: controlplane.CapabilityReady, MayServeStrict: true, LastUpdatedAt: now},
	}
	if !controlplane.EconomicControlReady(report) {
		t.Fatal("expected EconomicControlReady")
	}
}

func TestPhase73_EconomicControlReadyFalseWhenUsageAuthorityDown(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000731, 0).UTC()
	report := controlplane.ReadinessReport{
		ExecutableGeneration: controlplane.ExecutableGenerationStatus{State: controlplane.CapabilityReady},
		Components: []controlplane.ReadinessComponentStatus{
			{Component: controlplane.ReadinessComponentUsageAuthority, State: controlplane.CapabilityUnavailable, EnforcementScope: controlplane.EnforcementScopeDistributedStrict},
			{Component: controlplane.ReadinessComponentMeteringJournal, State: controlplane.CapabilityReady},
		},
		Posture: controlplane.AggregateProtectedTrafficPosture([]controlplane.ReadinessComponentStatus{
			{Component: controlplane.ReadinessComponentUsageAuthority, State: controlplane.CapabilityUnavailable, EnforcementScope: controlplane.EnforcementScopeDistributedStrict},
		}, now),
	}
	ok, reason := controlplane.EvaluateEconomicControlReady(report)
	if ok {
		t.Fatal("usage authority unavailable must block EconomicControlReady")
	}
	if reason == "" {
		t.Fatal("expected blocking reason")
	}
}

func TestPhase73_EconomicControlReadyAllowsPendingTerminalDegraded(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000732, 0).UTC()
	components := []controlplane.ReadinessComponentStatus{
		{Component: controlplane.ReadinessComponentUsageAuthority, State: controlplane.CapabilityReady, EnforcementScope: controlplane.EnforcementScopeDistributedStrict},
		{Component: controlplane.ReadinessComponentConcurrencyAuthority, State: controlplane.CapabilityReady, EnforcementScope: controlplane.EnforcementScopeDistributedStrict},
		{Component: controlplane.ReadinessComponentRequestCoordinator, State: controlplane.CapabilityReady},
		{Component: controlplane.ReadinessComponentAttemptCoordinator, State: controlplane.CapabilityReady},
		{Component: controlplane.ReadinessComponentMeteringJournal, State: controlplane.CapabilityReady},
		{Component: controlplane.ReadinessComponentExecutableGeneration, State: controlplane.CapabilityReady},
		{Component: controlplane.ReadinessComponentTerminalRecovery, State: controlplane.CapabilityDegraded, Reason: controlplane.ReasonPendingTerminalWork},
	}
	report := controlplane.ReadinessReport{
		ExecutableGeneration: controlplane.ExecutableGenerationStatus{State: controlplane.CapabilityReady},
		Components:           components,
		Posture:              controlplane.AggregateProtectedTrafficPosture(components, now),
	}
	if !controlplane.EconomicControlReady(report) {
		t.Fatal("pending terminal work degrades but must not block EconomicControlReady when authority is ready")
	}
}

func TestPhase73_EconomicControlReadyRejectsEmptyReport(t *testing.T) {
	t.Parallel()
	// Empty/zero report is not EconomicControlReady; commercial billing is never implied.
	if controlplane.EconomicControlReady(controlplane.ReadinessReport{}) {
		t.Fatal("empty report must not be EconomicControlReady")
	}
}
