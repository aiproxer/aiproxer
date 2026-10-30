package metrics

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// TerminalWorkProm holds bounded Prometheus series for terminal-work recovery
// (requirements 8.9, 12.5, 12.8; design D14).
type TerminalWorkProm struct {
	backlog     prometheus.Gauge
	oldestAge   prometheus.Gauge
	pending     prometheus.Gauge
	retrying    prometheus.Gauge
	quarantined prometheus.Gauge
	completed   prometheus.Gauge
	claimed     prometheus.Gauge
	transitions *prometheus.CounterVec
}

// TerminalWorkSnapshot is a Prom-facing view of MetricsObserver counts.
type TerminalWorkSnapshot struct {
	Backlog      int
	OldestAgeSec float64
	Pending      int
	Retrying     int
	Quarantined  int
	Completed    int
	Claimed      int
}

// providerBucketCount is the fixed provider_id label cardinality (hash buckets).
const providerBucketCount = 16

var terminalWorkTransitionLabels = []string{"state", "kind", "provider_id"}

// RegisterTerminalWorkProm registers aip_terminal_work_* collectors on reg.
func RegisterTerminalWorkProm(reg prometheus.Registerer) *TerminalWorkProm {
	if reg == nil {
		return nil
	}
	m := &TerminalWorkProm{
		backlog: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "terminal_work_backlog",
			Help:      "Outstanding terminal-work rows awaiting recovery (pending/retry/claimed/intent).",
		}),
		oldestAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "terminal_work_oldest_age_seconds",
			Help:      "Age in seconds of the oldest outstanding terminal-work row.",
		}),
		pending: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "terminal_work_pending",
			Help:      "Terminal-work rows in pending state.",
		}),
		retrying: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "terminal_work_retrying",
			Help:      "Terminal-work rows in retry state.",
		}),
		quarantined: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "terminal_work_quarantined",
			Help:      "Terminal-work rows in quarantined state.",
		}),
		completed: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "terminal_work_completed",
			Help:      "Terminal-work rows in completed state (scan window).",
		}),
		claimed: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "terminal_work_claimed",
			Help:      "Terminal-work rows in claimed state.",
		}),
		transitions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "terminal_work_transitions_total",
				Help:      "Terminal-work state transitions (bounded state/kind/provider_bucket labels only).",
			},
			terminalWorkTransitionLabels,
		),
	}
	reg.MustRegister(
		m.backlog, m.oldestAge, m.pending, m.retrying, m.quarantined, m.completed, m.claimed, m.transitions,
	)
	return m
}

// ApplySnapshot updates gauge series from a metrics observer snapshot.
func (m *TerminalWorkProm) ApplySnapshot(s TerminalWorkSnapshot) {
	if m == nil {
		return
	}
	m.SetBacklog(s.Backlog)
	if m.oldestAge != nil {
		age := s.OldestAgeSec
		if age < 0 {
			age = 0
		}
		m.oldestAge.Set(age)
	}
	if m.pending != nil {
		m.pending.Set(float64(s.Pending))
	}
	if m.retrying != nil {
		m.retrying.Set(float64(s.Retrying))
	}
	if m.quarantined != nil {
		m.quarantined.Set(float64(s.Quarantined))
	}
	if m.completed != nil {
		m.completed.Set(float64(s.Completed))
	}
	if m.claimed != nil {
		m.claimed.Set(float64(s.Claimed))
	}
}

// SetBacklog updates the backlog gauge.
func (m *TerminalWorkProm) SetBacklog(n int) {
	if m == nil || m.backlog == nil {
		return
	}
	m.backlog.Set(float64(n))
}

// SetOldestAge updates the oldest-age gauge from a duration.
func (m *TerminalWorkProm) SetOldestAge(d time.Duration) {
	if m == nil || m.oldestAge == nil {
		return
	}
	if d < 0 {
		d = 0
	}
	m.oldestAge.Set(d.Seconds())
}

// ObserveTransition increments the bounded transition counter.
// provider_id is mapped to a fixed hash bucket so cardinality cannot grow with
// arbitrary/hostile provider strings and raw IDs/secrets never appear as labels.
func (m *TerminalWorkProm) ObserveTransition(state, kind, providerID string) {
	if m == nil || m.transitions == nil {
		return
	}
	state = boundWorkLabel(state)
	kind = boundWorkLabel(kind)
	providerID = providerBucketLabel(providerID)
	m.transitions.WithLabelValues(state, kind, providerID).Inc()
}

func boundWorkLabel(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "unknown"
	}
	return v
}

func providerBucketLabel(providerID string) string {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return "b00"
	}
	sum := sha256.Sum256([]byte(providerID))
	// Fixed 16-bucket cardinality (b00..b0f); raw IDs never appear as labels.
	n := int(sum[0]) % providerBucketCount
	return fmt.Sprintf("b%02x", n)
}
