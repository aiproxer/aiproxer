package routinghealth_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/policy"
	"github.com/aiproxer/aiproxer/internal/infra/routinghealth"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCandidateHealthFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *config.Config
		now  func() time.Time
		want func(t *testing.T, h policy.CandidateHealth)
	}{
		{
			name: "nil config uses empty health",
			cfg:  nil,
			now:  func() time.Time { return time.Unix(0, 0) },
			want: func(t *testing.T, h policy.CandidateHealth) {
				t.Helper()
				if h == nil {
					t.Fatal("expected non-nil")
				}
				if h.UnhealthyCandidateKeys() != nil {
					t.Fatalf("expected no unhealthy keys, got %v", h.UnhealthyCandidateKeys())
				}
			},
		},
		{
			name: "circuit breaker when enabled",
			cfg: &config.Config{
				Routing: config.RoutingConfig{
					Health: config.RoutingHealthConfig{
						CircuitBreaker: config.CircuitBreakerConfig{
							Enabled:          true,
							FailureThreshold: 2,
							OpenFor:          "3s",
						},
					},
				},
			},
			now: func() time.Time { return time.Unix(100, 0) },
			want: func(t *testing.T, h policy.CandidateHealth) {
				t.Helper()
				if _, ok := h.(*policy.CircuitBreaker); !ok {
					t.Fatalf("want *policy.CircuitBreaker, got %T", h)
				}
			},
		},
		{
			name: "disabled circuit breaker uses empty",
			cfg: &config.Config{
				Routing: config.RoutingConfig{
					Health: config.RoutingHealthConfig{
						CircuitBreaker: config.CircuitBreakerConfig{Enabled: false},
					},
				},
			},
			now: nil,
			want: func(t *testing.T, h policy.CandidateHealth) {
				t.Helper()
				if h.UnhealthyCandidateKeys() != nil {
					t.Fatalf("expected no unhealthy keys when disabled, got %v", h.UnhealthyCandidateKeys())
				}
			},
		},
		{
			name: "invalid open_for still yields circuit breaker with default duration",
			cfg: &config.Config{
				Routing: config.RoutingConfig{
					Health: config.RoutingHealthConfig{
						CircuitBreaker: config.CircuitBreakerConfig{
							Enabled:          true,
							FailureThreshold: 1,
							OpenFor:          "not-a-valid-duration",
						},
					},
				},
			},
			now: func() time.Time { return time.Unix(0, 0) },
			want: func(t *testing.T, h policy.CandidateHealth) {
				t.Helper()
				if _, ok := h.(*policy.CircuitBreaker); !ok {
					t.Fatalf("want *policy.CircuitBreaker, got %T", h)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := tt.now
			if now == nil {
				now = time.Now
			}
			h := routinghealth.CandidateHealthFromConfig(tt.cfg, now)
			tt.want(t, h)
		})
	}
}

// TestCandidateHealthPolicyFromState proves the generation-scoped policy view
// shares observation state across two config generations with different
// threshold/open-for values while resolving disabled/nil inputs safely.
func TestCandidateHealthPolicyFromState(t *testing.T) {
	t.Parallel()

	now := time.Unix(500, 0).UTC()
	nowFn := func() time.Time { return now }
	state := policy.NewCircuitBreakerState(policy.CircuitBreakerStateOptions{})

	strictCfg := &config.Config{Routing: config.RoutingConfig{Health: config.RoutingHealthConfig{
		CircuitBreaker: config.CircuitBreakerConfig{Enabled: true, FailureThreshold: 1, OpenFor: "1h"},
	}}}
	lenientCfg := &config.Config{Routing: config.RoutingConfig{Health: config.RoutingHealthConfig{
		CircuitBreaker: config.CircuitBreakerConfig{Enabled: true, FailureThreshold: 5, OpenFor: "1h"},
	}}}

	strict := routinghealth.CandidateHealthPolicyFromState(strictCfg, state, nowFn)
	lenient := routinghealth.CandidateHealthPolicyFromState(lenientCfg, state, nowFn)

	if sink, ok := strict.(policy.RoutingAttemptOutcomeSink); ok {
		sink.OnRoutingAttemptOutcome("be:m", aipapi.AttemptSurfacedFailure)
	} else {
		t.Fatal("expected RoutingAttemptOutcomeSink")
	}

	if u := strict.UnhealthyCandidateKeys(); len(u) != 1 {
		t.Fatalf("strict generation (threshold=1) must open after 1 failure, got %v", u)
	}
	if u := lenient.UnhealthyCandidateKeys(); len(u) != 1 {
		t.Fatalf("lenient generation must observe the same shared failure via strict's record, got %v", u)
	}

	if h := routinghealth.CandidateHealthPolicyFromState(nil, state, nowFn); h.UnhealthyCandidateKeys() != nil {
		t.Fatal("nil config must resolve to empty health")
	}
	if h := routinghealth.CandidateHealthPolicyFromState(strictCfg, nil, nowFn); h.UnhealthyCandidateKeys() != nil {
		t.Fatal("nil state must resolve to empty health")
	}
	disabledCfg := &config.Config{}
	if h := routinghealth.CandidateHealthPolicyFromState(disabledCfg, state, nowFn); h.UnhealthyCandidateKeys() != nil {
		t.Fatal("disabled circuit breaker must resolve to empty health")
	}
}
