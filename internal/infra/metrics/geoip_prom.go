package metrics

import (
	"time"

	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
	"github.com/prometheus/client_golang/prometheus"
)

// GeoIPProm exposes only finite-label process metrics for the ingress gate.
type GeoIPProm struct {
	decisions *prometheus.CounterVec
	updates   *prometheus.CounterVec
	ready     prometheus.Gauge
	age       prometheus.Gauge
}

func RegisterGeoIPProm(reg prometheus.Registerer) *GeoIPProm {
	m := &GeoIPProm{
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "geoip_decisions_total",
			Help:      "GeoIP ingress decisions by finite outcome and reason.",
		}, []string{"decision", "reason"}),
		updates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "geoip_updates_total",
			Help:      "GeoIP database update outcomes by finite result.",
		}, []string{"result"}),
		ready: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "geoip_database_ready",
			Help:      "Whether a validated GeoIP Country database is active.",
		}),
		age: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "geoip_database_age_seconds",
			Help:      "Age of the active GeoIP database in seconds.",
		}),
	}
	reg.MustRegister(m.decisions, m.updates, m.ready, m.age)
	return m
}

func (m *GeoIPProm) Decision(reason coregeoip.Reason, allow bool) {
	if m == nil || m.decisions == nil {
		return
	}
	m.decisions.WithLabelValues(decisionLabel(allow), reasonLabel(reason)).Inc()
}

func (m *GeoIPProm) Update(result string) {
	if m == nil || m.updates == nil {
		return
	}
	switch result {
	case "unchanged", "updated", "failed", "recovered":
	default:
		result = "unknown"
	}
	m.updates.WithLabelValues(result).Inc()
}

func (m *GeoIPProm) SetReady(ready bool) {
	if m != nil && m.ready != nil {
		if ready {
			m.ready.Set(1)
		} else {
			m.ready.Set(0)
		}
	}
}

func (m *GeoIPProm) SetAge(age time.Duration) {
	if m != nil && m.age != nil {
		m.age.Set(age.Seconds())
	}
}

func decisionLabel(allow bool) string {
	if allow {
		return "allow"
	}
	return "deny"
}

func reasonLabel(reason coregeoip.Reason) string {
	switch reason {
	case coregeoip.ReasonCIDRAllow, coregeoip.ReasonCIDRDeny,
		coregeoip.ReasonCountryAllow, coregeoip.ReasonCountryDeny,
		coregeoip.ReasonDefaultAllow, coregeoip.ReasonDefaultDeny,
		coregeoip.ReasonClientIPError, coregeoip.ReasonLookupError:
		return string(reason)
	default:
		return "unknown"
	}
}
