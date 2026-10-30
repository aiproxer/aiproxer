package metrics

import (
	"strings"

	accountingobs "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/observability"
	"github.com/prometheus/client_golang/prometheus"
)

type TokenAccountingProm struct {
	observations *prometheus.CounterVec
	duration     *prometheus.HistogramVec
}

var tokenAccountingDurationBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

func RegisterTokenAccountingProm(reg prometheus.Registerer) *TokenAccountingProm {
	m := &TokenAccountingProm{
		observations: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "token_accounting_observations_total",
				Help:      "Token-accounting observations by bounded accounting labels.",
			},
			[]string{"plane", "source", "authority", "status", "fallback", "unavailable", "backend", "model"},
		),
		duration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "token_accounting_observation_seconds",
				Help:      "Token-accounting observation duration by bounded accounting labels.",
				Buckets:   tokenAccountingDurationBuckets,
			},
			[]string{"plane", "source", "authority", "status", "fallback", "unavailable", "backend", "model"},
		),
	}
	reg.MustRegister(m.observations, m.duration)
	return m
}

type TokenAccountingPromSink struct {
	p *TokenAccountingProm
}

func NewTokenAccountingPromSink(p *TokenAccountingProm) *TokenAccountingPromSink {
	if p == nil {
		return nil
	}
	return &TokenAccountingPromSink{p: p}
}

func (s *TokenAccountingPromSink) Record(obs accountingobs.Observation) {
	if s == nil || s.p == nil {
		return
	}
	labels := []string{
		labelOrUnknown(string(obs.Labels.Plane)),
		labelOrUnknown(string(obs.Labels.Source)),
		labelOrUnknown(string(obs.Labels.Authority)),
		labelOrUnknown(string(obs.Status)),
		labelOrNone(string(obs.FallbackReason)),
		labelOrNone(string(obs.UnavailableReason)),
		labelOrUnknown(obs.Labels.Backend),
		modelLabel(obs.Labels.Model),
	}
	s.p.observations.WithLabelValues(labels...).Inc()
	s.p.duration.WithLabelValues(labels...).Observe(obs.Duration.Seconds())
}

func labelOrUnknown(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "unknown"
	}
	return v
}

func labelOrNone(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "none"
	}
	return v
}

func modelLabel(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return "specified"
}
