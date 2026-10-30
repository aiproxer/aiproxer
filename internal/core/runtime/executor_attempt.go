package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/policy"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// attemptReasonMaxRunes caps persisted attempt lineage text (bounded diagnostic detail).
const attemptReasonMaxRunes = 512

// attemptReasonDetail returns truncated error text suitable for AttemptRecord.Reason persistence.
func attemptReasonDetail(err error) string {
	if err == nil {
		return ""
	}
	return diag.TruncErrDetail(err, attemptReasonMaxRunes)
}

// recordAttemptParams is the argument bundle for [Executor.recordAttempt].
type recordAttemptParams struct {
	ALegID  string
	BLeg    b2bua.BLegRecord
	Cand    routing.AttemptCandidate
	Outcome aipapi.AttemptOutcome
	Reason  string
	// DetailErr is the surfaced failure, when any, for secure-session outcome mapping.
	DetailErr error
	// OpenFailed marks the failure of a backend Open, which is pre-output by
	// definition.
	OpenFailed bool
	// BindFailed marks the failure of evidence binding after a successful
	// Open: pre-output, but not a failed open.
	BindFailed bool
}

func (e *Executor) recordAttempt(ctx context.Context, p recordAttemptParams) error {
	if e.Store == nil {
		return nil
	}
	now := e.now()
	rec := aipapi.AttemptRecord{
		BLegID:         p.BLeg.BLegID,
		ALegID:         p.ALegID,
		Seq:            p.BLeg.Seq,
		BackendID:      p.Cand.Primary.Backend,
		EffectiveModel: p.Cand.Primary.Model,
		StartedAt:      now,
		FinishedAt:     now,
		Outcome:        p.Outcome,
		Reason:         p.Reason,
	}
	if sink, ok := e.CandidateHealth.(policy.RoutingAttemptOutcomeSink); ok {
		sink.OnRoutingAttemptOutcome(p.Cand.Key, p.Outcome)
	}
	return e.Store.RecordAttempt(context.WithoutCancel(ctx), rec)
}

// recordAttemptLogged runs [Executor.recordAttempt]; persistence failure is logged at debug only.
func (e *Executor) recordAttemptLogged(ctx context.Context, p recordAttemptParams, attrOpts diag.AttrOpts) {
	if e == nil {
		return
	}
	if e.Metrics != nil {
		e.Metrics.OnAttemptRecorded(p.Outcome, p.Cand.Primary.Backend)
	}
	if err := e.recordAttempt(ctx, p); err != nil && e.Log != nil {
		base := diag.Attrs(ctx, attrOpts)
		attrs := make([]slog.Attr, 0, len(base)+1)
		attrs = append(attrs, base...)
		attrs = append(attrs, slog.Any("error", err))
		e.Log.LogAttrs(ctx, slog.LevelDebug, "record_attempt_failed", attrs...)
	}
	if m := e.secureSessionForAttempt(); m != nil {
		st, ok := execctx.SecureSessionTurnFromContext(ctx)
		if !ok {
			return
		}
		out := secureAttemptOutcome(st, p.BLeg, p, e.now())
		if err := m.RecordAttemptOutcome(context.WithoutCancel(ctx), out); err != nil && e.Log != nil {
			base := diag.Attrs(ctx, attrOpts)
			attrs := make([]slog.Attr, 0, len(base)+1)
			attrs = append(attrs, base...)
			attrs = append(attrs, slog.Any("error", err))
			e.Log.LogAttrs(ctx, slog.LevelDebug, "secure_session_attempt_outcome_failed", attrs...)
		}
	}
	// Attempt lifecycle evidence: emit observational evidence for attempt failures
	// at the attempt-record boundary, the narrow runtime seam (requirements 3.6,
	// 4.5, 7.2, 7.5). The seam is attached by the stream path (withDecisionEvidence);
	// when absent (no snapshot/emitter, or non-stream callsites) no evidence is
	// emitted, preserving the no-observer/non-interference default (requirements 7.6,
	// 10.5). Cancellation and success are not policy failures and are not projected
	// (requirement 6.4). This is a side-effect-only emission; retry/failover behavior
	// and no-output/failover invariants are unchanged.
	if isAttemptFailureForEvidence(p) {
		if fn := extensions.AttemptEvidenceFromContext(ctx); fn != nil {
			fn(ctx, strings.TrimSpace(p.Cand.Primary.Backend), p.DetailErr)
		}
	}
}

// isAttemptFailureForEvidence reports whether p represents an attempt failure that
// [extensions.ProjectAttemptObservation] can represent (non-nil DetailErr and a
// surfaced or swallowed failure outcome). Cancellation and success are excluded.
func isAttemptFailureForEvidence(p recordAttemptParams) bool {
	if p.DetailErr == nil {
		return false
	}
	if aipapi.IsPolicyDecisionError(p.DetailErr) {
		return false
	}
	return p.Outcome == aipapi.AttemptSurfacedFailure || p.Outcome == aipapi.AttemptSwallowedFailure
}

func backendCallWithRouteParams(work aipapi.Call, cand routing.AttemptCandidate) (aipapi.Call, error) {
	merged, err := aipapi.MergeRouteQueryIntoGenerationOptions(work.Options, cand.Primary.Params)
	if err != nil {
		return aipapi.Call{}, fmt.Errorf("route generation options: %w", err)
	}
	work.Options = merged
	return work, nil
}
