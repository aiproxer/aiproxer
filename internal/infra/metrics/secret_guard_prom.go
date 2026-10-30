package metrics

import (
	"cmp"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/prometheus/client_golang/prometheus"
)

// SecretGuardProm holds bounded Prometheus counters for secret-guard decisions.
type SecretGuardProm struct {
	decisions   *prometheus.CounterVec
	matches     *prometheus.CounterVec
	quarantines *prometheus.CounterVec
	failures    *prometheus.CounterVec
	scanLimits  *prometheus.CounterVec
}

var secretGuardLabelNames = []string{"action", "outcome", "source_category"}

// RegisterSecretGuardProm registers aip_secret_guard_* collectors on reg.
func RegisterSecretGuardProm(reg prometheus.Registerer) *SecretGuardProm {
	m := &SecretGuardProm{
		decisions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "secret_guard_decisions_total",
				Help:      "Secret-guard pass/log/redact/block decisions (bounded labels only).",
			},
			secretGuardLabelNames,
		),
		matches: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "secret_guard_matches_total",
				Help:      "Secret-guard decisions with at least one safe finding (bounded labels only).",
			},
			secretGuardLabelNames,
		),
		quarantines: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "secret_guard_quarantines_total",
				Help:      "Secret-guard quarantine attempts after block (bounded labels only).",
			},
			secretGuardLabelNames,
		),
		failures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "secret_guard_failures_total",
				Help:      "Secret-guard handler failures and fail-closed errors (bounded labels only).",
			},
			secretGuardLabelNames,
		),
		scanLimits: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "secret_guard_scan_limits_total",
				Help:      "Secret-guard scan_max_bytes limit hits (bounded labels only).",
			},
			secretGuardLabelNames,
		),
	}
	reg.MustRegister(m.decisions, m.matches, m.quarantines, m.failures, m.scanLimits)
	return m
}

type secretGuardDecisionSink struct {
	p *SecretGuardProm
}

// NewSecretGuardDecisionSink adapts [SecretGuardProm] to [extensions.SecretGuardDecisionMetrics].
func NewSecretGuardDecisionSink(p *SecretGuardProm) extensions.SecretGuardDecisionMetrics {
	if p == nil {
		return nil
	}
	return &secretGuardDecisionSink{p: p}
}

func (s *secretGuardDecisionSink) IncDecision(action, outcome, sourceCategory string) {
	s.inc(s.p.decisions, action, outcome, sourceCategory)
}

func (s *secretGuardDecisionSink) IncMatch(action, outcome, sourceCategory string) {
	s.inc(s.p.matches, action, outcome, sourceCategory)
}

func (s *secretGuardDecisionSink) IncQuarantine(action, outcome, sourceCategory string) {
	s.inc(s.p.quarantines, action, outcome, sourceCategory)
}

func (s *secretGuardDecisionSink) IncFailure(action, outcome, sourceCategory string) {
	s.inc(s.p.failures, action, outcome, sourceCategory)
}

func (s *secretGuardDecisionSink) IncScanLimit(action, outcome, sourceCategory string) {
	s.inc(s.p.scanLimits, action, outcome, sourceCategory)
}

func (s *secretGuardDecisionSink) inc(vec *prometheus.CounterVec, action, outcome, sourceCategory string) {
	if s == nil || s.p == nil || vec == nil {
		return
	}
	action = cmp.Or(action, "unknown")
	outcome = cmp.Or(outcome, "unknown")
	sourceCategory = cmp.Or(sourceCategory, "unknown")
	vec.WithLabelValues(action, outcome, sourceCategory).Inc()
}
