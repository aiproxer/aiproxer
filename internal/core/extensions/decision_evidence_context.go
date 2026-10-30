package extensions

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// DecisionEvidence is the request-scoped policy decision evidence seam carried
// through the context for stage runners (requirements 3.1-3.6, 4.1-4.4, 7.6).
// It binds the evidence emitter to the safe execution views and timeout budget
// source the runners use to build policydecision.Context per provider.
//
// Stage runners look this up via [DecisionEvidenceFromContext]. When absent (or
// when Emitter is nil), runners emit no evidence, preserving the
// no-observer/non-interference default (requirements 9.1, 10.5).
//
// Views carries the authoritative safe attribution used to build decision
// contexts. For pre-backend stages the runtime attaches a pre-backend views
// snapshot here (execctx views are not yet on the context). For stream stages
// the runtime may leave Views zero; the helper prefers execctx views already
// attached to the context when present.
type DecisionEvidence struct {
	Emitter       *EvidenceEmitter
	Views         execctx.Views
	TimeoutBudget TimeoutBudgetSource
	TimeoutGuard  *ProviderTimeoutGuard
	// OutputCommittedSource, when non-nil, returns the current client-visible
	// output-committed state for stream-stage evidence. It is consulted by
	// decisionContextFor only to escalate a caller-supplied false to true (never
	// to downgrade an explicit true), so callers that already know the
	// authoritative committed state (e.g. the completion gate) are unaffected.
	//
	// The func is invoked fresh on each evidence emission; runtime context caching
	// must not snapshot its result, so a stream whose commitment state changes
	// mid-flight is never recorded with a stale bool. nil preserves pre-backend
	// behavior (OutputCommitted stays false).
	OutputCommittedSource func() bool
}

type decisionEvidenceCtxKey struct{}

// stageTimeoutFor returns the configured evaluation timeout and the derived deadline
// (now+timeout) for a stage/provider. Zero (or nil seam/budget) preserves legacy
// synchronous behavior: the caller runs the provider directly without a child context.
func stageTimeoutFor(ev *DecisionEvidence, stage, providerID string) (time.Duration, time.Time) {
	if ev == nil || ev.TimeoutBudget == nil {
		return 0, time.Time{}
	}
	timeout := ev.TimeoutBudget.TimeoutFor(stage, providerID)
	if timeout <= 0 {
		return 0, time.Time{}
	}
	return timeout, time.Now().Add(timeout)
}

// WithViews returns a copy of ev with Views replaced by views. The Emitter,
// TimeoutBudget and OutputCommittedSource are shared so callers can refresh the
// safe attribution snapshot between phases (e.g. submit vs post-submit pre-backend
// stages) without rebuilding the seam or re-resolving the emitter. Returns nil
// when ev is nil so callers can chain on an absent seam.
func (ev *DecisionEvidence) WithViews(views execctx.Views) *DecisionEvidence {
	if ev == nil {
		return nil
	}
	return &DecisionEvidence{
		Emitter:               ev.Emitter,
		Views:                 views,
		TimeoutBudget:         ev.TimeoutBudget,
		TimeoutGuard:          ev.TimeoutGuard,
		OutputCommittedSource: ev.OutputCommittedSource,
	}
}

// WithDecisionEvidence attaches ev to ctx so stage runners can project and emit
// per-provider policy decision evidence. A nil ev means no evidence is emitted.
func WithDecisionEvidence(ctx context.Context, ev *DecisionEvidence) context.Context {
	if ctx == nil || ev == nil {
		return ctx
	}
	return context.WithValue(ctx, decisionEvidenceCtxKey{}, ev)
}

// DecisionEvidenceFromContext returns the evidence seam attached by
// [WithDecisionEvidence], or nil when none is attached.
func DecisionEvidenceFromContext(ctx context.Context) *DecisionEvidence {
	if ctx == nil {
		return nil
	}
	raw := ctx.Value(decisionEvidenceCtxKey{})
	if raw == nil {
		return nil
	}
	ev, ok := raw.(*DecisionEvidence)
	if !ok {
		return nil
	}
	return ev
}

// decisionContextFor builds a policydecision.Context for a stage/provider from
// the evidence seam with a zero evaluation deadline. It is the convenience wrapper
// for non-timeout paths (success/failure/malformed evidence on the direct path) where
// no derived deadline is available. Timeout and bounded-path evidence callers must use
// [decisionContextForDeadline] with the exact deadline threaded from the bounded result.
//
// It prefers execctx views already attached to ctx (stream stages), falling back to the
// seam's Views snapshot (pre-backend stages). The evaluation timeout is derived from the
// seam's timeout budget source when configured; zero preserves legacy behavior
// (requirement 6.3).
func decisionContextFor(ctx context.Context, ev *DecisionEvidence, stage, providerID string, outputCommitted bool) policydecision.Context {
	return decisionContextForDeadline(ctx, ev, stage, providerID, outputCommitted, time.Time{})
}

// decisionContextForDeadline builds a policydecision.Context for a stage/provider from
// the evidence seam, projecting the supplied evaluation deadline onto the emitted
// record (requirement 6.3). deadline must be the exact time.Time used to bound the
// provider call (the bounded result's Deadline); zero preserves legacy behavior and is
// used by non-timeout / direct-path evidence.
func decisionContextForDeadline(ctx context.Context, ev *DecisionEvidence, stage, providerID string, outputCommitted bool, deadline time.Time) policydecision.Context {
	views := ev.Views
	if v, ok := execctx.FromContext(ctx); ok {
		views = v
	}
	// Only escalate false -> true from the dynamic source; never downgrade an
	// explicit true. This keeps pre-backend stages (nil source) and callers that
	// already pass the authoritative value (completion gate) unchanged, while
	// letting stream-stage evidence (tool reactor) reflect live commitment.
	if !outputCommitted && ev.OutputCommittedSource != nil && ev.OutputCommittedSource() {
		outputCommitted = true
	}
	var timeout time.Duration
	if ev.TimeoutBudget != nil {
		timeout = ev.TimeoutBudget.TimeoutFor(stage, providerID)
	}
	return BuildDecisionContext(views, stage, providerID, DecisionContextOptions{
		OutputCommitted:    outputCommitted,
		EvaluationTimeout:  timeout,
		EvaluationDeadline: deadline,
	})
}

// emitDecisionRecord emits rec through the seam's emitter. It is a no-op when
// the seam or emitter is absent, so runners can call it unconditionally.
func emitDecisionRecord(ctx context.Context, ev *DecisionEvidence, rec policydecision.Record) {
	if ev == nil || ev.Emitter == nil {
		return
	}
	ev.Emitter.Emit(ctx, rec)
}
