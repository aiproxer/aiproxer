package metrics

import (
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/prometheus/client_golang/prometheus"
)

// AuthorityStageProm holds Prometheus series for usage-authority stage latency (16.5).
type AuthorityStageProm struct {
	stageDur *prometheus.HistogramVec
	timeouts *prometheus.CounterVec
}

var authorityStageBuckets = []float64{
	0.0001, 0.0005, 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5,
}

// RegisterAuthorityStageProm registers aip_authority_stage_* collectors on reg.
func RegisterAuthorityStageProm(reg prometheus.Registerer) *AuthorityStageProm {
	m := &AuthorityStageProm{
		stageDur: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "authority_stage_duration_seconds",
				Help:      "Wall time for one usage-authority stage (labels: stage, provider, outcome).",
				Buckets:   authorityStageBuckets,
			},
			[]string{"stage", "provider", "outcome"},
		),
		timeouts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "authority_stage_timeouts_total",
				Help:      "Usage-authority stage timeouts (labels: stage, provider).",
			},
			[]string{"stage", "provider"},
		),
	}
	reg.MustRegister(m.stageDur, m.timeouts)
	return m
}

type authorityStageSink struct {
	p *AuthorityStageProm
}

// NewAuthorityStageSink adapts AuthorityStageProm to usagelimit.StageMetrics.
func NewAuthorityStageSink(p *AuthorityStageProm) usagelimit.StageMetrics {
	if p == nil {
		return nil
	}
	return &authorityStageSink{p: p}
}

func (s *authorityStageSink) ObserveStage(stage, provider, outcome string, seconds float64) {
	if s == nil || s.p == nil {
		return
	}
	if stage == "" {
		stage = "unknown"
	}
	if provider == "" {
		provider = "unknown"
	}
	if outcome == "" {
		outcome = "unknown"
	}
	s.p.stageDur.WithLabelValues(stage, provider, outcome).Observe(seconds)
	if outcome == usagelimit.OutcomeTimeout {
		s.p.timeouts.WithLabelValues(stage, provider).Inc()
	}
}
