package controlplane

import "fmt"

// EconomicControlReady reports whether the OSS technical economic-control
// posture is ready for protected traffic (design EconomicControlReady).
// This is not commercial billing, payments, invoicing, or tax readiness.
func EconomicControlReady(report ReadinessReport) bool {
	ok, _ := EvaluateEconomicControlReady(report)
	return ok
}

// EvaluateEconomicControlReady returns readiness and the first blocking reason.
func EvaluateEconomicControlReady(report ReadinessReport) (bool, string) {
	if report.ExecutableGeneration.State != CapabilityReady &&
		report.ExecutableGeneration.State != CapabilityDisabled {
		return false, fmt.Sprintf("executable_generation=%s", report.ExecutableGeneration.State)
	}
	byID := map[ReadinessComponentID]ReadinessComponentStatus{}
	for _, c := range report.Components {
		byID[c.Component] = c
	}
	required := []ReadinessComponentID{
		ReadinessComponentExecutableGeneration,
		ReadinessComponentRequestCoordinator,
		ReadinessComponentAttemptCoordinator,
		ReadinessComponentConcurrencyAuthority,
		ReadinessComponentUsageAuthority,
		ReadinessComponentMeteringJournal,
		ReadinessComponentTerminalRecovery,
	}
	for _, id := range required {
		c, ok := byID[id]
		if !ok {
			// Absent optional rows are treated as disabled (feature not mounted).
			continue
		}
		switch c.State {
		case CapabilityReady, CapabilityDisabled:
			continue
		case CapabilityDegraded:
			// Pending terminal recovery degrades but does not block EconomicControlReady
			// when MayServeStrict remains true (design: pending post-output work degrades).
			if id == ReadinessComponentTerminalRecovery && report.Posture.MayServeStrict {
				continue
			}
			return false, fmt.Sprintf("%s=%s", id, c.State)
		default:
			return false, fmt.Sprintf("%s=%s", id, c.State)
		}
	}
	if !report.Posture.MayServeStrict && report.Posture.State != CapabilityDisabled {
		// Local/advisory-only deployments may still be EconomicControlReady when
		// no distributed-strict component is configured; block only when posture
		// explicitly forbids strict while claiming readiness.
		if report.Posture.State == CapabilityUnavailable {
			return false, "protected_traffic_unavailable"
		}
	}
	if report.Posture.State == CapabilityUnavailable {
		return false, "protected_traffic_unavailable"
	}
	return true, ""
}
