package routinghealth

import (
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/policy"
)

// CandidateHealthFromConfig returns a [policy.CandidateHealth] for the executor from cfg.
// When routing.health.circuit_breaker is disabled, returns [Empty]. When enabled, returns
// a [policy.CircuitBreaker] (failure threshold and open duration from config; invalid open_for
// defaults as documented on [config.CircuitBreakerConfig] validation).
func CandidateHealthFromConfig(cfg *config.Config, now func() time.Time) policy.CandidateHealth {
	if cfg == nil {
		return Empty()
	}
	cb := cfg.Routing.Health.CircuitBreaker
	if !cb.Enabled {
		return Empty()
	}
	return policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: cb.FailureThreshold,
		OpenDuration:     openForFromCircuitBreakerConfig(cb),
		Now:              now,
	})
}

// CandidateHealthPolicyFromState returns a [policy.CandidateHealth] view scoped
// to one config generation's threshold/open-duration/enabled policy, evaluated
// and recorded against a process-shared [policy.CircuitBreakerState]. Compatible
// overlapping generations observe the same failure/blockedUntil counters while
// each applies its own generation-owned policy (design "Health policy reload";
// req 7.4, 9.1). When routing.health.circuit_breaker is disabled, returns [Empty].
func CandidateHealthPolicyFromState(cfg *config.Config, state *policy.CircuitBreakerState, now func() time.Time) policy.CandidateHealth {
	if cfg == nil || state == nil {
		return Empty()
	}
	cb := cfg.Routing.Health.CircuitBreaker
	if !cb.Enabled {
		return Empty()
	}
	return policy.NewCircuitBreakerPolicy(state, policy.CircuitBreakerOptions{
		FailureThreshold: cb.FailureThreshold,
		OpenDuration:     openForFromCircuitBreakerConfig(cb),
		Now:              now,
	})
}

// openForFromCircuitBreakerConfig resolves the effective open duration. Default
// when open_for is omitted; invalid or non-positive durations fall back to the
// default (config.Validate rejects unparsable durations before production Build).
func openForFromCircuitBreakerConfig(cb config.CircuitBreakerConfig) time.Duration {
	openFor := 30 * time.Second
	if s := strings.TrimSpace(cb.OpenFor); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			openFor = d
		}
	}
	return openFor
}
