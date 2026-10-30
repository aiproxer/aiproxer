package metrics

import (
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/prometheus/client_golang/prometheus"
)

// LargePayloadProm holds Prometheus collectors for large-payload fast-path diagnostics
// (Task 19.1; Requirements 20, 22; design section 15).
// Enforces bounded static labels only (never backend/model/user/session IDs, body/path/spool path/resume token).
type LargePayloadProm struct {
	stages      *prometheus.CounterVec
	declines    *prometheus.CounterVec
	captured    *prometheus.CounterVec
	replays     prometheus.Counter
	rewrites    prometheus.Counter
	stageDur    *prometheus.HistogramVec
	activeSpool prometheus.Gauge
}

var largePayloadStageBuckets = []float64{
	0.0001, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// RegisterLargePayloadProm registers aip_large_payload_* collectors on reg.
func RegisterLargePayloadProm(reg prometheus.Registerer) *LargePayloadProm {
	m := &LargePayloadProm{
		stages: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "large_payload_pipeline_stages_total",
				Help:      "Large-payload fast-path pipeline stage counts (labels: stage).",
			},
			[]string{"stage"},
		),
		declines: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "large_payload_declines_total",
				Help:      "Large-payload fast-path decline counts (labels: reason).",
			},
			[]string{"reason"},
		),
		captured: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "large_payload_captured_total",
				Help:      "Large-payload fast-path captures by size bucket and storage (labels: size_bucket, storage).",
			},
			[]string{"size_bucket", "storage"},
		),
		replays: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "large_payload_replays_total",
				Help:      "Large-payload fast-path replay source executions.",
			},
		),
		rewrites: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "large_payload_rewrites_total",
				Help:      "Large-payload fast-path model token rewrites applied.",
			},
		),
		stageDur: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "large_payload_stage_duration_seconds",
				Help:      "Duration of large-payload fast-path stages in seconds (labels: stage).",
				Buckets:   largePayloadStageBuckets,
			},
			[]string{"stage"},
		),
		activeSpool: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "large_payload_active_spool_bytes",
				Help:      "Current active logical spool bytes reserved in SpoolLedger.",
			},
		),
	}
	reg.MustRegister(
		m.stages,
		m.declines,
		m.captured,
		m.replays,
		m.rewrites,
		m.stageDur,
		m.activeSpool,
	)
	return m
}

type largePayloadPromSink struct {
	p       *LargePayloadProm
	mu      sync.Mutex
	lastSeq uint64
	hasSeq  bool
}

// NewLargePayloadPromSink adapts LargePayloadProm to largebody.DiagnosticsObserver.
func NewLargePayloadPromSink(p *LargePayloadProm) largebody.DiagnosticsObserver {
	if p == nil {
		return largebody.NoopDiagnosticsObserver{}
	}
	return &largePayloadPromSink{p: p}
}

func (s *largePayloadPromSink) OnPipelineStage(stage largebody.PipelineStage) {
	if s == nil || s.p == nil {
		return
	}
	s.p.stages.WithLabelValues(sanitizeLargePayloadStage(string(stage))).Inc()
}

func (s *largePayloadPromSink) OnDecline(reason string) {
	if s == nil || s.p == nil {
		return
	}
	s.p.declines.WithLabelValues(sanitizeDeclineReason(reason)).Inc()
}

func (s *largePayloadPromSink) OnCapture(sizeBucket string, storage largebody.StorageKind) {
	if s == nil || s.p == nil {
		return
	}
	s.p.captured.WithLabelValues(sanitizeSizeBucket(sizeBucket), sanitizeStorage(string(storage))).Inc()
}

func (s *largePayloadPromSink) OnReplay() {
	if s == nil || s.p == nil {
		return
	}
	s.p.replays.Inc()
}

func (s *largePayloadPromSink) OnRewrite() {
	if s == nil || s.p == nil {
		return
	}
	s.p.rewrites.Inc()
}

func (s *largePayloadPromSink) OnStageDuration(stage string, d time.Duration) {
	if s == nil || s.p == nil {
		return
	}
	s.p.stageDur.WithLabelValues(sanitizeDurationStage(stage)).Observe(d.Seconds())
}

func (s *largePayloadPromSink) OnActiveSpoolBytes(seq uint64, bytes int64) {
	if s == nil || s.p == nil {
		return
	}
	s.mu.Lock()
	if s.hasSeq && seq < s.lastSeq {
		s.mu.Unlock()
		return
	}
	s.hasSeq = true
	s.lastSeq = seq
	s.p.activeSpool.Set(float64(bytes))
	s.mu.Unlock()
}

// Bounded label sanitizers prevent dynamic cardinality explosions or secret leaks (Requirement 22.2, 22.3).

func sanitizeLargePayloadStage(stage string) string {
	switch stage {
	case string(largebody.StageConsidered),
		string(largebody.StageStaticCanonical),
		string(largebody.StageCaptured),
		string(largebody.StageProfileProven),
		string(largebody.StageAssessmentEligible),
		string(largebody.StageWire),
		string(largebody.StageCanonical):
		return stage
	default:
		return "other"
	}
}

func sanitizeDurationStage(stage string) string {
	switch stage {
	case "pre_capture_gates", "capture", "proof", "assessment", "execution":
		return stage
	default:
		return "other"
	}
}

func sanitizeDeclineReason(reason string) string {
	switch reason {
	case largebody.DeclineReasonLocalTurn,
		largebody.DeclineReasonSecretGuard,
		largebody.DeclineReasonTerminalDecision,
		largebody.DeclineReasonFrontendRouteResolver,
		largebody.DeclineReasonTraffic,
		largebody.DeclineReasonAccountingCounting,
		largebody.DeclineReasonCustomCallCallback,
		largebody.DeclineReasonBackendDomain,
		largebody.DeclineReasonFeatureDisabled,
		largebody.DeclineReasonBelowThreshold,
		largebody.DeclineReasonGzipCompressed,
		largebody.DeclineReasonStaticBlocker,
		largebody.DeclineReasonSpoolBudgetExhausted,
		largebody.DeclineReasonProofUncertain.String(),
		largebody.DeclineReasonAuthorityBlocker.String(),
		largebody.DeclineReasonRouteIncompatible.String(),
		largebody.DeclineReasonBackendIncompatible.String(),
		largebody.DeclineReasonRewriteUnsupported.String(),
		largebody.DeclineReasonSessionUnsupported.String(),
		largebody.DeclineReasonMeteringUnsupported.String(),
		largebody.DeclineReasonCountingUnsupported.String(),
		largebody.DeclineReasonGenerationMismatch.String(),
		largebody.DeclineReasonCanceled.String(),
		largebody.DeclineReasonLimitExceeded,
		largebody.DeclineReasonReadError,
		"none":
		return reason
	default:
		return "other"
	}
}

func sanitizeSizeBucket(bucket string) string {
	switch bucket {
	case "lt_32k", "32k_256k", "256k_1m", "1m_5m", "5m_20m", "gte_20m":
		return bucket
	default:
		return "other"
	}
}

func sanitizeStorage(storage string) string {
	switch storage {
	case string(largebody.StorageMemory):
		return string(largebody.StorageMemory)
	case string(largebody.StorageFile):
		return string(largebody.StorageFile)
	default:
		return "unknown"
	}
}
