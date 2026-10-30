package authorityattribution

import "github.com/aiproxer/aiproxer/pkg/aipsdk/authority"

// ReadinessRank returns the severity rank for one readiness value.
// Canonical order: ready (0) < degraded (1) < disabled (2) < unavailable (3).
func ReadinessRank(r authority.Readiness) int {
	switch r {
	case "", authority.ReadinessReady:
		return 0
	case authority.ReadinessDegraded:
		return 1
	case authority.ReadinessDisabled:
		return 2
	case authority.ReadinessUnavailable:
		return 3
	default:
		return 1
	}
}

// AggregateReadiness picks the most severe readiness among inputs.
func AggregateReadiness(values ...authority.Readiness) authority.Readiness {
	worst := authority.ReadinessReady
	for _, r := range values {
		if ReadinessRank(r) > ReadinessRank(worst) {
			worst = r
		}
	}
	if worst == "" {
		return authority.ReadinessReady
	}
	return worst
}
