package metrics

import (
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/prometheus/client_golang/prometheus"
)

// ExecutorProm holds Prometheus collectors for executor attempt and open latency.
type ExecutorProm struct {
	attempts           *prometheus.CounterVec
	openDur            *prometheus.HistogramVec
	transportDecisions *prometheus.CounterVec
	cancellations      *prometheus.CounterVec
	// reasoningReplaySkipped counts per-attempt log_skip strips of
	// unrepresentable client-sent reasoning, labelled by backend instance id.
	// The label set is bounded: backend ids are operator-configured.
	reasoningReplaySkipped *prometheus.CounterVec
	// emptyVisibleTurn counts clean terminal turns whose surviving attempt
	// produced no visible output (text, tool calls, or assistant media).
	// Reasoning-only output counts as empty. The label set is bounded:
	// backend instance ids are operator-configured.
	emptyVisibleTurn *prometheus.CounterVec
	// emptyVisibleTurnRetry counts corrective-retry decisions for candidate
	// empty visible turns, labelled by backend instance id and the closed
	// outcome set corrected|skipped|failed. An outcome outside that set is
	// dropped, never labelled, so the label set is bounded by construction:
	// backend instance ids are operator-configured and the outcome set is
	// closed.
	emptyVisibleTurnRetry *prometheus.CounterVec
	// backgroundSponsorshipRefused counts background operations refused
	// before any provider dispatch. The label set is bounded: the reason is
	// a closed enum that never names a principal, an account or a sponsor.
	backgroundSponsorshipRefused *prometheus.CounterVec
}

var openAttemptBuckets = []float64{
	0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60,
}

// RegisterExecutorProm registers aip_executor_* series on reg.
func RegisterExecutorProm(reg prometheus.Registerer) *ExecutorProm {
	m := &ExecutorProm{
		attempts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "executor_attempts_total",
				Help:      "B-leg attempts recorded to continuity (labels: bounded outcome + backend instance id).",
			},
			[]string{"outcome", "backend"},
		),
		openDur: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "executor_backend_open_seconds",
				Help:      "Time from B-leg allocation until backend Open returns (labels: backend instance id).",
				Buckets:   openAttemptBuckets,
			},
			[]string{"backend"},
		),
		transportDecisions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "executor_transport_negotiations_total",
				Help:      "Transport negotiation decisions before backend open (bounded operation, mode, and outcome labels).",
			},
			[]string{"operation", "mode", "outcome"},
		),
		cancellations: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "executor_cancellations_total",
				Help:      "Executor cancellations recorded with low-cardinality cause class, mode, phase, and fallback.",
			},
			[]string{"cause_class", "mode", "phase", "fallback"},
		),
		reasoningReplaySkipped: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "reasoning_replay_skipped_total",
				Help:      "Per-attempt log_skip strips of unrepresentable client-sent reasoning (labels: backend instance id).",
			},
			[]string{"backend"},
		),
		emptyVisibleTurn: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "empty_visible_turn_total",
				Help:      "Clean terminal turns whose surviving attempt produced no visible output (labels: backend instance id).",
			},
			[]string{"backend"},
		),
		emptyVisibleTurnRetry: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "empty_visible_turn_retry_total",
				Help:      "Corrective-retry decisions for candidate empty visible turns (labels: backend instance id; closed outcome set corrected|skipped|failed).",
			},
			[]string{"backend", "outcome"},
		),
		backgroundSponsorshipRefused: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "background_sponsorship_refused_total",
				Help:      "Background operations refused before any provider dispatch (bounded reason label).",
			},
			[]string{"reason"},
		),
	}
	reg.MustRegister(m.attempts, m.openDur, m.transportDecisions, m.cancellations, m.reasoningReplaySkipped, m.emptyVisibleTurn, m.emptyVisibleTurnRetry, m.backgroundSponsorshipRefused)
	return m
}

// ObserveCancellation records a cancellation event with strictly bounded low-cardinality labels.
func (m *ExecutorProm) ObserveCancellation(obs runtime.CancellationObservation) {
	if m == nil || m.cancellations == nil {
		return
	}
	m.cancellations.WithLabelValues(string(obs.CauseClass), string(obs.Mode), string(obs.Phase), string(obs.Fallback)).Inc()
}

// ObserveEmptyVisibleTurn counts one clean terminal turn whose surviving
// attempt produced no visible output. An empty backend folds to the bounded
// "unknown" bucket, never an empty label value.
func (m *ExecutorProm) ObserveEmptyVisibleTurn(backend string) {
	if m == nil || m.emptyVisibleTurn == nil {
		return
	}
	m.emptyVisibleTurn.WithLabelValues(labelOrUnknown(backend)).Inc()
}

// ObserveEmptyVisibleTurnRetry counts one corrective-retry decision for a
// candidate empty visible turn. An outcome outside the closed set
// corrected|skipped|failed is dropped rather than labelled, so the label set
// cannot grow; an empty backend folds to the bounded "unknown" bucket, never
// an empty label value.
func (m *ExecutorProm) ObserveEmptyVisibleTurnRetry(backend, outcome string) {
	if m == nil || m.emptyVisibleTurnRetry == nil {
		return
	}
	switch outcome {
	case "corrected", "skipped", "failed":
	default:
		return
	}
	m.emptyVisibleTurnRetry.WithLabelValues(labelOrUnknown(backend), outcome).Inc()
}

type executorPromSink struct {
	p *ExecutorProm
}

// NewExecutorPromSink adapts [ExecutorProm] to [runtime.MetricsSink].
func NewExecutorPromSink(p *ExecutorProm) runtime.MetricsSink {
	if p == nil {
		return nil
	}
	return &executorPromSink{p: p}
}

func (s *executorPromSink) OnAttemptRecorded(outcome aipapi.AttemptOutcome, backend string) {
	if s == nil || s.p == nil {
		return
	}
	b := backend
	if b == "" {
		b = "unknown"
	}
	s.p.attempts.WithLabelValues(string(outcome), b).Inc()
}

func (s *executorPromSink) OnBackendOpenDuration(backend string, seconds float64) {
	if s == nil || s.p == nil {
		return
	}
	b := backend
	if b == "" {
		b = "unknown"
	}
	s.p.openDur.WithLabelValues(b).Observe(seconds)
}

func (s *executorPromSink) OnTransportNegotiation(operation aipapi.Operation, mode aipapi.TransportMode, outcome string) {
	if s == nil || s.p == nil {
		return
	}
	op := string(operation)
	if op == "" {
		op = "unknown"
	}
	m := string(mode)
	if m == "" {
		m = "unknown"
	}
	o := outcome
	switch o {
	case "accept", "reject":
	case "":
		o = "unknown"
	default:
		o = "other"
	}
	s.p.transportDecisions.WithLabelValues(op, m, o).Inc()
}

func (s *executorPromSink) OnCancellation(obs runtime.CancellationObservation) {
	if s == nil || s.p == nil {
		return
	}
	s.p.ObserveCancellation(obs)
}

// OnReasoningReplaySkipped implements runtime.ReasoningReplaySkipObserver: one
// bounded increment per stripped attempt, labelled by backend instance id.
func (s *executorPromSink) OnReasoningReplaySkipped(backend string) {
	if s == nil || s.p == nil {
		return
	}
	b := backend
	if b == "" {
		b = "unknown"
	}
	s.p.reasoningReplaySkipped.WithLabelValues(b).Inc()
}

// OnEmptyVisibleTurn implements runtime.EmptyVisibleTurnObserver: one bounded
// increment per clean terminal turn with no visible output, labelled by
// backend instance id.
func (s *executorPromSink) OnEmptyVisibleTurn(backend string) {
	if s == nil || s.p == nil {
		return
	}
	s.p.ObserveEmptyVisibleTurn(backend)
}

// OnEmptyVisibleTurnRetry implements runtime.EmptyVisibleTurnRetryObserver: one
// bounded increment per corrective-retry decision, labelled by backend instance
// id and the closed outcome set corrected|skipped|failed. An outcome outside
// that set is dropped without labelling.
func (s *executorPromSink) OnEmptyVisibleTurnRetry(backend, outcome string) {
	if s == nil || s.p == nil {
		return
	}
	s.p.ObserveEmptyVisibleTurnRetry(backend, outcome)
}

// OnBackgroundSponsorshipRefused implements
// runtime.BackgroundSponsorshipObserver: one bounded increment per background
// operation refused before any provider dispatch, labelled by the closed
// reason enum.
func (s *executorPromSink) OnBackgroundSponsorshipRefused(reason string) {
	if s == nil || s.p == nil {
		return
	}
	s.p.backgroundSponsorshipRefused.WithLabelValues(labelOrUnknown(reason)).Inc()
}
