package metrics

import (
	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	"github.com/prometheus/client_golang/prometheus"
)

// QuirkAppliedProm counts strict-provider quirk applications that actually
// rewrote the B-leg wire. Its method set structurally satisfies the compatible
// adapters' QuirkObserver seam without importing provider-specific packages.
// Production wiring of the bundle handle into backend construction is deferred
// (backend factories receive no metrics deps today); the zero value at the
// adapter seam stays silent until then.
type QuirkAppliedProm struct{ applied *prometheus.CounterVec }

// RegisterQuirkAppliedProm registers aip_provider_quirk_applied_total on reg.
func RegisterQuirkAppliedProm(reg prometheus.Registerer) *QuirkAppliedProm {
	m := &QuirkAppliedProm{applied: prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "provider_quirk_applied_total",
			Help:      "Strict-provider quirk applications that rewrote the wire (bounded quirk label).",
		},
		[]string{"quirk"},
	)}
	reg.MustRegister(m.applied)
	return m
}

// ObserveQuirkApplied counts one applied quirk. Only the bounded counted-quirk
// set mints series; any other ID is dropped so the quirk label cannot grow.
// Refusals must never reach here: they surface as classified terminal errors
// with their own telemetry. Nil-safe for adapters built without metrics.
func (m *QuirkAppliedProm) ObserveQuirkApplied(quirk providerprofiles.QuirkID) {
	if m == nil {
		return
	}
	switch quirk {
	case providerprofiles.QuirkOpenAIChatReasoningContent,
		providerprofiles.QuirkOpenAIResponsesToolAdjacency:
		m.applied.WithLabelValues(string(quirk)).Inc()
	}
}
