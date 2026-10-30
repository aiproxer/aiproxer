package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// This file is the test-only seam between the in-package guard tests and the
// external failure test. It is compiled only into the test binary, so it adds no
// production surface: an external test package in this directory cannot reach the
// collector fields directly, and the failure proof must install a sink whose
// label handling actually panics.

// InstallPanickingLabelSink rebinds the collector's denial and transition vectors
// to prometheus vectors that declare one more variable label than the
// SelfDefenseProm code path supplies. prometheus.CounterVec panics inside
// WithLabelValues whenever the supplied label-value count does not match the
// declared variable-label count, so this is the real, client-sanctioned shape of
// an observability fault: a label lookup that fails at request time.
//
// It is deliberately not recoverable by any change to the reason vocabulary or
// by the entry-count source, so a test that keeps it installed proves that the
// deferred recover, not the input, is what keeps the wire answer intact.
func InstallPanickingLabelSink(m *SelfDefenseProm) {
	if m == nil {
		return
	}
	m.denials = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "self_defense_denials_panicking_sink",
		Help:      "Test-only counter vec whose declared label arity never matches the supplied values.",
	}, []string{"reason", "unused_extra"})
	m.transitions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "self_defense_transitions_panicking_sink",
		Help:      "Test-only counter vec whose declared label arity never matches the supplied values.",
	}, []string{"reason", "unused_extra"})
}
