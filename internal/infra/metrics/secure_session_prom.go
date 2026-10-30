package metrics

import (
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/prometheus/client_golang/prometheus"
)

// SecureSessionProm holds Prometheus series for secure-session operations.
type SecureSessionProm struct {
	beginNew    prometheus.Counter
	beginResume prometheus.Counter
	denied      *prometheus.CounterVec
	storageFail prometheus.Counter
	touchDur    prometheus.Histogram
	recTurnFail *prometheus.CounterVec
	recStream   *prometheus.CounterVec
}

var touchActivityBuckets = []float64{
	0.0001, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2,
}

// RegisterSecureSessionProm registers aip_secure_session_* series.
func RegisterSecureSessionProm(reg prometheus.Registerer) *SecureSessionProm {
	m := &SecureSessionProm{
		beginNew: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "secure_session_begin_new_total",
			Help:      "Successful secure-session new session begin turns (before backend open).",
		}),
		beginResume: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "secure_session_begin_resume_total",
			Help:      "Successful secure-session resume begin turns (before backend open).",
		}),
		denied: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "secure_session_begin_denied_total",
				Help:      "Failed BeginTurn before backend open (label: stable denial code or unknown).",
			},
			[]string{"code"},
		),
		storageFail: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "secure_session_storage_unavailable_total",
			Help:      "Storage unavailable outcomes surfaced through secure session prepare.",
		}),
		touchDur: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "secure_session_last_activity_touch_seconds",
				Help:      "Latency of durable store last-activity touch in BeginTurn.",
				Buckets:   touchActivityBuckets,
			},
		),
		recTurnFail: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "secure_session_recorder_client_turn_failures_total",
				Help:      "Client-turn recorder failures after gate (label: mandatory).",
			},
			[]string{"mandatory"},
		),
		recStream: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "secure_session_recorder_stream_event_failures_total",
				Help:      "Post-hook stream event recorder failures (labels: committed, mandatory).",
			},
			[]string{"committed", "mandatory"},
		),
	}
	reg.MustRegister(
		m.beginNew, m.beginResume, m.denied, m.storageFail, m.touchDur, m.recTurnFail, m.recStream,
	)
	return m
}

// RecordActivityTouchSeconds records last-activity touch latency in BeginTurn.
func (m *SecureSessionProm) RecordActivityTouchSeconds(seconds float64) {
	if m == nil {
		return
	}
	m.touchDur.Observe(seconds)
}

type secureSessionPromSink struct{ p *SecureSessionProm }

// SecureSessionMetricsSink adapts [SecureSessionProm] to [runtime.SecureSessionMetrics]. Nil when p is nil.
func SecureSessionMetricsSink(p *SecureSessionProm) runtime.SecureSessionMetrics {
	if p == nil {
		return nil
	}
	return &secureSessionPromSink{p: p}
}

func (s *secureSessionPromSink) ObserveBeginTurnNew() {
	if s == nil || s.p == nil {
		return
	}
	s.p.beginNew.Inc()
}

func (s *secureSessionPromSink) ObserveBeginTurnResume() {
	if s == nil || s.p == nil {
		return
	}
	s.p.beginResume.Inc()
}

func (s *secureSessionPromSink) ObserveBeginTurnDenied(code string) {
	if s == nil || s.p == nil {
		return
	}
	c := code
	if c == "" {
		c = "unknown"
	}
	s.p.denied.WithLabelValues(c).Inc()
}

func (s *secureSessionPromSink) ObserveStorageUnavailable() {
	if s == nil || s.p == nil {
		return
	}
	s.p.storageFail.Inc()
}

func (s *secureSessionPromSink) ObserveActivityTouch(seconds float64) {
	if s == nil || s.p == nil {
		return
	}
	s.p.touchDur.Observe(seconds)
}

func (s *secureSessionPromSink) ObserveRecorderClientTurnFailed(mandatory bool) {
	if s == nil || s.p == nil {
		return
	}
	lab := "false"
	if mandatory {
		lab = "true"
	}
	s.p.recTurnFail.WithLabelValues(lab).Inc()
}

func (s *secureSessionPromSink) ObserveRecorderStreamEventFailed(committed bool, mandatory bool) {
	if s == nil || s.p == nil {
		return
	}
	commitLab := "false"
	if committed {
		commitLab = "true"
	}
	manLab := "false"
	if mandatory {
		manLab = "true"
	}
	s.p.recStream.WithLabelValues(commitLab, manLab).Inc()
}
