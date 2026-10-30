package metrics

import (
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/configreload"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"github.com/prometheus/client_golang/prometheus"
)

// ReloadProm holds fixed-label Prometheus series for configuration reload (req 14.4-14.5).
type ReloadProm struct {
	attempts          *prometheus.CounterVec
	duration          *prometheus.HistogramVec
	stageDuration     *prometheus.HistogramVec
	activeGenerations prometheus.Gauge
	retired           prometheus.Gauge
	pinned            prometheus.Gauge
	retentionPressure prometheus.Gauge
}

// ReloadGenerationSnapshot is an aggregate, label-free generation posture view.
type ReloadGenerationSnapshot struct {
	Active            int
	Retired           int
	Pinned            int
	RetentionPressure int // 0 or 1
}

var (
	reloadTriggerAllow = map[string]bool{
		string(sdkreload.TriggerSIGHUP): true,
		string(sdkreload.TriggerAPI):    true,
	}
	// Built from canonical ResultCategory constants only — never from the
	// exported mutable compatibility enumeration — so package-init ordering
	// cannot inject labels.
	reloadResultAllow = map[string]bool{
		string(sdkreload.ResultPublished):         true,
		string(sdkreload.ResultNoop):              true,
		string(sdkreload.ResultBusy):              true,
		string(sdkreload.ResultRestartRequired):   true,
		string(sdkreload.ResultRetentionBlocked):  true,
		string(sdkreload.ResultInvalid):           true,
		string(sdkreload.ResultSourceIntegrity):   true,
		string(sdkreload.ResultCanceled):          true,
		string(sdkreload.ResultPreparationFailed): true,
		string(sdkreload.ResultInternalFailed):    true,
		"quiesce_failed":                          true,
		"cleanup_failed":                          true,
		"other":                                   true,
	}
	reloadStageAllow = map[string]bool{
		configreload.StageRead:      true,
		configreload.StageLoad:      true,
		configreload.StageNoop:      true,
		configreload.StageClassify:  true,
		configreload.StageCompile:   true,
		configreload.StagePrepare:   true,
		configreload.StageRetention: true,
		configreload.StagePublish:   true,
		configreload.StageRollback:  true,
		configreload.StageShutdown:  true,
		configreload.StageBusy:      true,
		configreload.StageCoalesce:  true,
		configreload.StagePanic:     true,
		"validation":                true,
		"quiesce":                   true,
		"cleanup":                   true,
		"other":                     true,
	}
)

var reloadDurationBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60,
}

// RegisterReloadProm registers aip_reload_* collectors on reg.
func RegisterReloadProm(reg prometheus.Registerer) *ReloadProm {
	if reg == nil {
		return nil
	}
	m := &ReloadProm{
		attempts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "reload_attempts_total",
				Help:      "Configuration reload attempts (fixed trigger/result labels only).",
			},
			[]string{"trigger", "result"},
		),
		duration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "reload_duration_seconds",
				Help:      "Total configuration reload attempt duration in seconds.",
				Buckets:   reloadDurationBuckets,
			},
			[]string{"trigger", "result"},
		),
		stageDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "reload_stage_duration_seconds",
				Help:      "Configuration reload stage duration in seconds (fixed stage/result labels).",
				Buckets:   reloadDurationBuckets,
			},
			[]string{"stage", "result"},
		),
		activeGenerations: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "reload_active_generations",
			Help:      "Number of active configuration generations (0 or 1).",
		}),
		retired: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "reload_retired_generations",
			Help:      "Number of retained/retired configuration generations.",
		}),
		pinned: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "reload_pinned_references",
			Help:      "Aggregate pinned/lease reference count across generations.",
		}),
		retentionPressure: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "reload_retention_pressure",
			Help:      "1 when retained-generation budget would block publication; else 0.",
		}),
	}
	reg.MustRegister(
		m.attempts, m.duration, m.stageDuration,
		m.activeGenerations, m.retired, m.pinned, m.retentionPressure,
	)
	return m
}

// ObserveAttempt records one terminal reload attempt with fixed labels only.
func (m *ReloadProm) ObserveAttempt(trigger, result string, d time.Duration) {
	if m == nil {
		return
	}
	trig := boundReloadTrigger(trigger)
	res := boundReloadResult(result)
	if m.attempts != nil {
		m.attempts.WithLabelValues(trig, res).Inc()
	}
	if m.duration != nil {
		sec := d.Seconds()
		if sec < 0 {
			sec = 0
		}
		m.duration.WithLabelValues(trig, res).Observe(sec)
	}
}

// ObserveStage records one reload stage duration with fixed labels only.
func (m *ReloadProm) ObserveStage(stage, result string, d time.Duration) {
	if m == nil || m.stageDuration == nil {
		return
	}
	sec := d.Seconds()
	if sec < 0 {
		sec = 0
	}
	m.stageDuration.WithLabelValues(boundReloadStage(stage), boundReloadResult(result)).Observe(sec)
}

// ApplyGenerationSnapshot updates aggregate generation gauges (no ID labels).
func (m *ReloadProm) ApplyGenerationSnapshot(s ReloadGenerationSnapshot) {
	if m == nil {
		return
	}
	if m.activeGenerations != nil {
		m.activeGenerations.Set(float64(nonNeg(s.Active)))
	}
	if m.retired != nil {
		m.retired.Set(float64(nonNeg(s.Retired)))
	}
	if m.pinned != nil {
		m.pinned.Set(float64(nonNeg(s.Pinned)))
	}
	if m.retentionPressure != nil {
		p := min(max(s.RetentionPressure, 0), 1)
		m.retentionPressure.Set(float64(p))
	}
}

func nonNeg(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func boundReloadTrigger(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	if reloadTriggerAllow[v] {
		return v
	}
	return "other"
}

func boundReloadResult(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	// Accept both design vocabulary and hyphenated ResultCategory values.
	if reloadResultAllow[v] {
		return v
	}
	switch v {
	case "no-op", "noop":
		return string(sdkreload.ResultNoop)
	case "restart_required", "restart-required":
		return string(sdkreload.ResultRestartRequired)
	case "retention_blocked", "retention-blocked":
		return string(sdkreload.ResultRetentionBlocked)
	case "source_integrity_failed", "source-integrity-failed", "source_integrity":
		return string(sdkreload.ResultSourceIntegrity)
	case "preparation_failed", "preparation-failed":
		return string(sdkreload.ResultPreparationFailed)
	case "internal_failed", "internal-failed":
		return string(sdkreload.ResultInternalFailed)
	}
	// Strip hostile suffixes after first '/' or ':'.
	if i := strings.IndexAny(v, "/:"); i > 0 {
		return boundReloadResult(v[:i])
	}
	return "other"
}

func boundReloadStage(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	if reloadStageAllow[v] {
		return v
	}
	if i := strings.IndexAny(v, "/:"); i > 0 {
		return boundReloadStage(v[:i])
	}
	return "other"
}
