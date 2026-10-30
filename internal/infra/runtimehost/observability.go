package runtimehost

import (
	"context"
	"log/slog"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ReloadObserver records structured logs, process-owned spans, and metrics
// for reload attempts without owning reload logic or canonical status
// history (Task 6.4: bounded history moved exclusively to ReloadState).
type ReloadObserver struct {
	log     *slog.Logger
	tracer  trace.Tracer
	metrics *metrics.ReloadProm
}

// ReloadObserverDeps wires optional telemetry sinks for a ReloadObserver.
type ReloadObserverDeps struct {
	Logger  *slog.Logger
	Tracer  trace.Tracer
	Metrics *metrics.ReloadProm
}

// NewReloadObserver constructs a process-owned reload observer. Nil sinks are no-ops.
func NewReloadObserver(deps ReloadObserverDeps) *ReloadObserver {
	return &ReloadObserver{
		log:     deps.Logger,
		tracer:  deps.Tracer,
		metrics: deps.Metrics,
	}
}

type attemptScope struct {
	obs       *ReloadObserver
	attemptID int64
	trigger   sdkreload.TriggerKind
	actor     string
	start     time.Time
	active    int64
	parent    trace.Span
	stages    map[string]time.Time
}

// BeginAttempt starts process-owned reload spans and returns an end callback.
func (o *ReloadObserver) BeginAttempt(ctx context.Context, trigger sdkreload.Trigger, attemptID, activeGen int64) (outCtx context.Context, end func(sdkreload.Result)) {
	outCtx, end = ctx, func(sdkreload.Result) {}
	if o == nil {
		return outCtx, end
	}
	var span trace.Span
	defer func() {
		if recover() != nil {
			if span != nil {
				span.End()
			}
			outCtx, end = ctx, func(sdkreload.Result) {}
		}
	}()
	start := time.Now()
	if trigger.AcceptedAt.IsZero() {
		trigger.AcceptedAt = start
	}
	ctx, span = o.startSpan(ctx, "reload", attribute.Int64("attempt_id", attemptID),
		attribute.String("trigger", string(trigger.Kind)))
	scope := &attemptScope{
		obs: o, attemptID: attemptID, trigger: trigger.Kind, actor: trigger.SafeActor,
		start: start, active: activeGen, parent: span, stages: make(map[string]time.Time),
	}
	o.logAttrs(
		ctx, slog.LevelInfo, "reload attempt accepted",
		slog.Int64("attempt_id", attemptID),
		slog.String("trigger", string(trigger.Kind)),
		slog.String("stage", "accepted"),
		slog.String("result", "accepted"),
		slog.Int64("active_generation", activeGen),
		slog.Int64("duration_ms", 0),
		slog.String("safe_actor", truncateActorLog(trigger.SafeActor)),
	)
	return ctx, scope.End
}

// BeginStage opens a child span for a reload pipeline stage.
func (o *ReloadObserver) BeginStage(ctx context.Context, stage string) (outCtx context.Context, end func(result string)) {
	outCtx, end = ctx, func(string) {}
	if o == nil {
		return outCtx, end
	}
	var span trace.Span
	defer func() {
		if recover() != nil {
			if span != nil {
				span.End()
			}
			outCtx, end = ctx, func(string) {}
		}
	}()
	stage = boundStageName(stage)
	start := time.Now()
	ctx, span = o.startSpan(ctx, stage, attribute.String("stage", stage))
	return ctx, func(result string) {
		defer func() { _ = recover() }()
		d := time.Since(start)
		res := boundResultName(result)
		span.SetAttributes(attribute.String("result", res))
		if res != string(sdkreload.ResultPublished) && res != string(sdkreload.ResultNoop) && res != "ok" && res != "" && res != "accepted" {
			span.SetStatus(codes.Error, res)
		}
		span.End()
		if o.metrics != nil {
			o.metrics.ObserveStage(stage, res, d)
		}
	}
}

// End records logs and metrics for a finished attempt. Canonical status
// history is owned exclusively by ReloadState (Task 6.4); this scope
// performs safe logs/spans/metrics only.
func (scope *attemptScope) End(res sdkreload.Result) {
	defer func() { _ = recover() }()
	if scope == nil || scope.obs == nil {
		return
	}
	o := scope.obs
	d := time.Since(scope.start)
	if res.AttemptID == 0 {
		res.AttemptID = scope.attemptID
	}
	if res.ActiveGeneration == 0 {
		res.ActiveGeneration = scope.active
	}
	stage := res.ReasonCategory
	if stage == "" {
		stage = string(res.Category)
	}
	stage = boundStageName(stage)
	o.logAttrs(
		context.Background(), slog.LevelInfo, "reload attempt finished",
		slog.Int64("attempt_id", res.AttemptID),
		slog.String("trigger", string(scope.trigger)),
		slog.String("stage", stage),
		slog.String("result", string(res.Category)),
		slog.Int64("active_generation", res.ActiveGeneration),
		slog.Int64("candidate_generation", candidateGeneration(res)),
		slog.Int64("duration_ms", d.Milliseconds()),
		slog.Int("restart_field_count", res.RestartFieldCount),
		slog.String("reason_category", res.ReasonCategory),
		slog.String("safe_actor", truncateActorLog(scope.actor)),
	)
	if o.metrics != nil {
		o.metrics.ObserveAttempt(string(scope.trigger), string(res.Category), d)
	}
	if scope.parent != nil {
		scope.parent.SetAttributes(
			attribute.String("result", string(res.Category)),
			attribute.Int64("active_generation", res.ActiveGeneration),
		)
		if res.Category != sdkreload.ResultPublished && res.Category != sdkreload.ResultNoop {
			scope.parent.SetStatus(codes.Error, string(res.Category))
		}
		scope.parent.End()
	}
}

// ObserveLifecycle records post-commit quiesce/cleanup stage telemetry.
func (o *ReloadObserver) ObserveLifecycle(ctx context.Context, stage string, result string, d time.Duration) {
	defer func() { _ = recover() }()
	if o == nil {
		return
	}
	stage = boundStageName(stage)
	result = boundResultName(result)
	ctx, span := o.startSpan(ctx, stage, attribute.String("stage", stage), attribute.String("result", result))
	if result != "ok" && result != "" {
		span.SetStatus(codes.Error, result)
	}
	span.End()
	if o.metrics != nil {
		o.metrics.ObserveStage(stage, result, d)
	}
	o.logAttrs(
		ctx, slog.LevelInfo, "reload lifecycle stage",
		slog.String("stage", stage),
		slog.String("result", result),
		slog.Int64("duration_ms", d.Milliseconds()),
		slog.Int64("attempt_id", 0),
		slog.String("trigger", ""),
		slog.Int64("active_generation", 0),
	)
}

// RefreshGauges updates aggregate generation gauges from the manager.
func (o *ReloadObserver) RefreshGauges(mgr *Manager) {
	defer func() { _ = recover() }()
	if o == nil || o.metrics == nil || mgr == nil {
		return
	}
	snap := mgr.ObservabilitySnapshot()
	pressure := 0
	if snap.RetentionWouldBlock {
		pressure = 1
	}
	o.metrics.ApplyGenerationSnapshot(metrics.ReloadGenerationSnapshot{
		Active:            snap.Active,
		Retired:           snap.Retired,
		Pinned:            snap.Pinned,
		RetentionPressure: pressure,
	})
}

func candidateGeneration(res sdkreload.Result) int64 {
	if res.Category == sdkreload.ResultPublished {
		return res.ActiveGeneration
	}
	return 0
}

func (o *ReloadObserver) startSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if o == nil || o.tracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	return o.tracer.Start(ctx, name, trace.WithAttributes(attrs...))
}

func (o *ReloadObserver) logAttrs(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	if o == nil || o.log == nil {
		return
	}
	o.log.LogAttrs(ctx, level, msg, attrs...)
}

func boundStageName(s string) string {
	s = stringsTrimSpace(s)
	switch s {
	case "", "other":
		if s == "" {
			return "other"
		}
		return s
	case configreload.StageRead, configreload.StageLoad, configreload.StageNoop,
		configreload.StageClassify, configreload.StageCompile, configreload.StagePrepare,
		configreload.StageRetention, configreload.StagePublish, configreload.StageRollback,
		configreload.StageShutdown, configreload.StageBusy, configreload.StageCoalesce,
		configreload.StagePanic, "validation", "quiesce", "cleanup", "accepted":
		return s
	default:
		return "other"
	}
}

func boundResultName(s string) string {
	s = stringsTrimSpace(s)
	if s == "" {
		return "other"
	}
	cat := sdkreload.ResultCategory(s)
	if sdkreload.NormalizeResultCategory(cat) == cat {
		return s
	}
	switch s {
	case "ok", "accepted", "quiesce_failed", "cleanup_failed", "other":
		return s
	default:
		return "other"
	}
}

func truncateActorLog(s string) string {
	s = stringsTrimSpace(s)
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func stringsTrimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\t' && c != '\n' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// GenerationObservability is an aggregate, ID-free manager posture snapshot.
type GenerationObservability struct {
	Active              int
	Retired             int
	Pinned              int
	RetentionWouldBlock bool
}

// ObservabilitySnapshot returns aggregate active/retired/pinned/retention gauges.
func (m *Manager) ObservabilitySnapshot() GenerationObservability {
	if m == nil {
		return GenerationObservability{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := GenerationObservability{Retired: len(m.retained)}
	if g := m.active.Load(); g != nil {
		out.Active = 1
		out.Pinned = int(g.Refs())
	}
	for _, g := range m.retained {
		if g != nil {
			out.Pinned += int(g.Refs())
		}
	}
	if len(m.retained) >= m.maxRetained && out.Active == 1 {
		out.RetentionWouldBlock = true
	}
	return out
}

// DataPlaneReady reports whether a healthy active generation is published.
// Reload-control failures must not flip this to false while last-good remains active (req 13.1-13.2).
func DataPlaneReady(mgr *Manager) bool {
	if mgr == nil || mgr.ShuttingDown() {
		return false
	}
	g := mgr.Active()
	if g == nil {
		return false
	}
	switch g.Lifecycle() {
	case GenActive, GenRetiring, GenQuiescing, GenQuiesced:
		return true
	default:
		return false
	}
}
