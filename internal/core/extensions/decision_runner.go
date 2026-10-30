package extensions

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// boundedProviderResult carries the outcome of a stage runner's bounded decision
// provider call (requirements 6.1, 6.3, 6.4). IterCtx is the context runners must
// use for evidence emission (execctx views and emit dispatch); it carries no deadline
// value. Deadline is the exact time.Time used to bound the provider call on the bounded
// path (the same value passed to [RunDecisionProviderWithDeadline]); it is zero on the
// legacy synchronous (zero-timeout) path. Runners thread Deadline explicitly into
// timeout/bounded evidence projection so policydecision.Context.EvaluationDeadline
// equals the bounding deadline without carrying a business value through context.
// UsedTimeout distinguishes the bounded path from the legacy synchronous (zero-timeout)
// path; runners that mutate a clone commit it back only when UsedTimeout is true and
// neither TimedOut nor ParentCanceled is set.
type boundedProviderResult[T any] struct {
	IterCtx              context.Context
	Value                T
	Err                  error
	TimedOut             bool
	ParentCanceled       bool
	UsedTimeout          bool
	Deadline             time.Time
	ProviderStillRunning bool
	GuardRejected        bool
}

// runBoundedCall centralizes timeout orchestration for mutable stage runners that
// operate on the canonical call (pre-request, request transform, tool catalog). Zero
// timeout preserves the legacy synchronous path: run is invoked directly against the
// live call with no child context, no goroutine, no clone, and no deadline. Non-zero
// timeout clones the call synchronously before launching the bounded goroutine and
// commits the clone back only if the provider returns before timeout or parent
// cancellation (requirements 6.1, 6.3, 6.4, 6.5).
func runBoundedCall[T any](ctx context.Context, ev *DecisionEvidence, stage, providerID string, call *aipapi.Call, run func(context.Context, *aipapi.Call) (T, error)) boundedProviderResult[T] {
	timeout, deadline := stageTimeoutFor(ev, stage, providerID)
	if timeout <= 0 {
		v, err := run(ctx, call)
		return boundedProviderResult[T]{IterCtx: ctx, Value: v, Err: err}
	}
	guard := providerTimeoutGuard(ev)
	// Snapshot mutable state synchronously in the caller goroutine before the bounded
	// goroutine is launched, so a late timed-out provider can only touch this snapshot.
	clone := aipapi.CloneCall(*call)
	res := RunDecisionProviderWithDeadlineGuarded(ctx, deadline, guard, stage, providerID, func(c context.Context) (T, error) {
		return run(c, &clone)
	})
	out := boundedProviderResult[T]{
		IterCtx:              ctx,
		Value:                res.Value,
		Err:                  res.Err,
		TimedOut:             res.TimedOut,
		ParentCanceled:       res.ParentCanceled,
		UsedTimeout:          true,
		Deadline:             deadline,
		ProviderStillRunning: res.ProviderStillRunning,
		GuardRejected:        res.GuardRejected,
	}
	if !res.TimedOut && !res.ParentCanceled {
		*call = clone
	}
	return out
}

// runBoundedProvider centralizes timeout orchestration for immutable stage runners
// (route hints, tool policy, completion gates). Zero timeout invokes run directly with
// no child context, no goroutine, and no deadline.
func runBoundedProvider[T any](ctx context.Context, ev *DecisionEvidence, stage, providerID string, call func(context.Context) (T, error)) boundedProviderResult[T] {
	timeout, deadline := stageTimeoutFor(ev, stage, providerID)
	if timeout <= 0 {
		v, err := call(ctx)
		return boundedProviderResult[T]{IterCtx: ctx, Value: v, Err: err}
	}
	guard := providerTimeoutGuard(ev)
	res := RunDecisionProviderWithDeadlineGuarded(ctx, deadline, guard, stage, providerID, call)
	return boundedProviderResult[T]{
		IterCtx:              ctx,
		Value:                res.Value,
		Err:                  res.Err,
		TimedOut:             res.TimedOut,
		ParentCanceled:       res.ParentCanceled,
		UsedTimeout:          true,
		Deadline:             deadline,
		ProviderStillRunning: res.ProviderStillRunning,
		GuardRejected:        res.GuardRejected,
	}
}

func providerTimeoutGuard(ev *DecisionEvidence) *ProviderTimeoutGuard {
	if ev == nil || ev.TimeoutGuard == nil {
		return nil
	}
	return ev.TimeoutGuard
}

// stageFailureConfig carries the per-stage naming and evidence hooks used by the
// shared timeout/failure handlers (requirements 6.1, 6.3, 6.5). Per-stage log
// messages, metric labels, and provider-attr keys are passed here so the shared
// helpers do not hardcode them.
//
//   - Stage is the feature stage id used to build fail-closed policy errors.
//   - MetricsStage is the metric label passed to obs.IncFailOpenSkip on fail-open;
//     empty skips the metric (e.g. advisory stages without fail-open counters).
//   - TimeoutMsg is the fail-open timeout warn log message; empty skips logging
//     (e.g. the completion gate, which logs nothing on timeout).
//   - FailureMsg is the fail-open non-panic provider-error warn log message; empty
//     skips the non-panic warn (panics are still routed through
//     [logFailOpenExtensionPanic] when log is non-nil).
//   - PanicStage is the extension_stage attr passed to [logFailOpenExtensionPanic].
//   - ProviderAttr is the slog attr key carrying the provider id in warn logs
//     (e.g. "handler", "transform", "filter", "policy", "provider").
//   - EmitTimeout / EmitFailure project timeout / failure evidence through the seam.
//     deadline is the exact evaluation deadline used to bound the provider call (zero
//     on the direct path) so projected records carry the same EvaluationDeadline that
//     bounded the provider (requirement 6.3).
type stageFailureConfig struct {
	Stage        string
	MetricsStage string
	TimeoutMsg   string
	FailureMsg   string
	PanicStage   string
	ProviderAttr string
	EmitTimeout  func(ctx context.Context, ev *DecisionEvidence, providerID string, deadline time.Time, mode sdkhooks.FailureMode)
	EmitFailure  func(ctx context.Context, ev *DecisionEvidence, providerID string, err error, deadline time.Time, mode sdkhooks.FailureMode)
}

// handleProviderTimeout performs the shared evaluation-timeout outcome handling
// (requirements 6.1, 6.3): it emits timeout evidence, applies fail-open
// logging/metrics/continue, and returns a fail-closed [PolicyErrorFromTimeout]. It
// never converts parent cancellation to a policy timeout; callers must check
// ParentCanceled on the bounded result before calling this helper.
//
// deadline is the exact evaluation deadline used to bound the provider call (the
// bounded result's Deadline); it is projected onto the emitted timeout record so the
// record's EvaluationDeadline equals the bounding deadline.
//
// Returns cont=true when the chain should continue (fail-open skip); err is non-nil
// when the caller should return (fail-closed). cont and err are mutually exclusive.
func handleProviderTimeout(ctx context.Context, log *slog.Logger, obs StageMetrics, ev *DecisionEvidence, cfg stageFailureConfig, iterCtx context.Context, providerID string, deadline time.Time, mode sdkhooks.FailureMode) (cont bool, err error) { //nolint:revive // context-as-argument: iterCtx is the evidence-emission iteration context, threaded at the seam.
	if cfg.EmitTimeout != nil {
		cfg.EmitTimeout(iterCtx, ev, providerID, deadline, mode)
	}
	if mode == sdkhooks.FailOpen {
		if cfg.TimeoutMsg != "" && log != nil {
			log.WarnContext(ctx, cfg.TimeoutMsg, cfg.ProviderAttr, providerID)
		}
		if cfg.MetricsStage != "" && obs != nil {
			obs.IncFailOpenSkip(cfg.MetricsStage)
		}
		return true, nil
	}
	return false, PolicyErrorFromTimeout(cfg.Stage, providerID, failureBehaviorFromMode(mode))
}

// handleProviderFailure performs the shared provider-failure outcome handling
// (requirements 6.1, 6.2, 6.4, 6.5): it emits failure evidence, applies fail-open
// panic-vs-warn logging/metrics/continue, preserves parent cancellation, and returns
// a fail-closed [PolicyErrorFromProviderFailure].
//
// deadline is the exact evaluation deadline used to bound the provider call (the
// bounded result's Deadline); it is projected onto the emitted failure record so a
// bounded-path failure records the same EvaluationDeadline that bounded the provider.
// It is zero on the direct path.
//
// Parent cancellation is preserved: when [IsContextCancellation] reports the parent
// context was canceled/expired, the original err is returned unchanged so the
// runtime keeps cancellation as cancellation rather than converting it to a policy
// failure (requirement 6.4).
//
// Returns cont=true when the chain should continue (fail-open skip); err is non-nil
// when the caller should return (fail-closed or parent cancellation). cont and err
// are mutually exclusive.
//
// The completion-gate panic special case is intentionally NOT routed through this
// helper: completion panics return through the stream boundary as fail-closed policy
// provider failures, which the completion runner keeps inline.
func handleProviderFailure(ctx context.Context, log *slog.Logger, obs StageMetrics, ev *DecisionEvidence, cfg stageFailureConfig, iterCtx context.Context, providerID string, deadline time.Time, mode sdkhooks.FailureMode, err error) (cont bool, retErr error) { //nolint:revive // context-as-argument: iterCtx is the evidence-emission iteration context, threaded at the seam.
	// Preserve parent cancellation before evidence emission and fail-open: a
	// cancellation-derived error must short-circuit the chain as cancellation
	// (requirement 6.4), not be swallowed as a fail-open continue or recorded as
	// a provider-failure record.
	if IsContextCancellation(ctx, err) {
		return false, err
	}
	if cfg.EmitFailure != nil {
		cfg.EmitFailure(iterCtx, ev, providerID, err, deadline, mode)
	}
	if mode == sdkhooks.FailOpen {
		if log != nil {
			if _, ok := errors.AsType[*safety.PanicError](err); ok {
				logFailOpenExtensionPanic(ctx, log, cfg.PanicStage, providerID, err)
			} else if cfg.FailureMsg != "" {
				log.WarnContext(ctx, cfg.FailureMsg, cfg.ProviderAttr, providerID, "error", err)
			}
		}
		if cfg.MetricsStage != "" && obs != nil {
			obs.IncFailOpenSkip(cfg.MetricsStage)
		}
		return true, nil
	}
	return false, PolicyErrorFromProviderFailure(cfg.Stage, providerID, failureBehaviorFromMode(mode), err)
}
