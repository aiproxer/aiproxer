package keepwarm

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// PrometheusCollector exports only bounded keep-warm state and finite event
// labels. It retains no provider handles, cache identities, prompts, or
// session IDs.
//
// The collector lives with its feature owner (not in generic metrics
// infrastructure): it reads MetricsSnapshot and owns the feature-specific
// event vocabulary. Collector registration lifetime stays with the metrics
// bundle owner, which registers this collector through a generic
// prometheus.Registerer without importing the feature.
type PrometheusCollector struct {
	mu      sync.RWMutex
	manager *Manager
	swaps   int

	activeEpochs  *prometheus.Desc
	activeTargets *prometheus.Desc
	events        *prometheus.Desc
}

// NewPrometheusCollector constructs an unregistered collector. Registration
// is the metrics bundle owner's responsibility.
func NewPrometheusCollector() *PrometheusCollector {
	return &PrometheusCollector{
		activeEpochs: prometheus.NewDesc(
			"aip_prompt_cache_keepwarm_active_epochs", "Active keep-warm idle epochs.", nil, nil,
		),
		activeTargets: prometheus.NewDesc(
			"aip_prompt_cache_keepwarm_active_targets", "Active keep-warm residency targets.", nil, nil,
		),
		events: prometheus.NewDesc(
			"aip_prompt_cache_keepwarm_events_total", "Keep-warm events by bounded event name.", []string{"event"}, nil,
		),
	}
}

// SetManager changes the generation whose state is exported.
func (p *PrometheusCollector) SetManager(manager *Manager) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.manager = manager
	p.swaps++
}

// Manager returns the currently exported keep-warm manager, if any.
func (p *PrometheusCollector) Manager() *Manager {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.manager
}

// SwapCount returns the number of times SetManager was invoked.
func (p *PrometheusCollector) SwapCount() int {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.swaps
}

func (p *PrometheusCollector) Describe(ch chan<- *prometheus.Desc) {
	if p == nil {
		return
	}
	ch <- p.activeEpochs
	ch <- p.activeTargets
	ch <- p.events
}

func (p *PrometheusCollector) Collect(ch chan<- prometheus.Metric) {
	if p == nil {
		return
	}
	p.mu.RLock()
	manager := p.manager
	p.mu.RUnlock()
	if manager == nil {
		return
	}
	snapshot := manager.Metrics()
	ch <- prometheus.MustNewConstMetric(p.activeEpochs, prometheus.GaugeValue, float64(snapshot.ActiveEpochs))
	ch <- prometheus.MustNewConstMetric(p.activeTargets, prometheus.GaugeValue, float64(snapshot.ActiveTargets))
	for event, count := range snapshot.Events {
		if !metricEventAllowed(event) {
			continue
		}
		ch <- prometheus.MustNewConstMetric(p.events, prometheus.CounterValue, float64(count), event)
	}
}

func metricEventAllowed(event string) bool {
	switch event {
	case "armed", "disabled_global", "disabled_session", "uncommitted", "no_os_command",
		"invalid_lineage", "revision_exhausted", "no_eligible_target", "generation_quiescing",
		"expired", "unsafe_window", "no_schedule", "budget_unknown", "budget_exhausted",
		"capacity", "cancel_foreground", "cancel_session_end", "cancel_disabled",
		"cancel_arm_replacement", "cancel_quiesce", "cancel_exhausted",
		"stale_result", "control_error", "accounting_error", "renewed",
		"still_resident", "cold_recreated", "stale", "unsupported", "control_failed",
		"release_dropped":
		return true
	default:
		return false
	}
}

var _ prometheus.Collector = (*PrometheusCollector)(nil)
