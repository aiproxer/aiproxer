package extensions

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

// RunPreRequestStage runs admission handlers in stable order after request shaping and before route planning.
func RunPreRequestStage(ctx context.Context, log *slog.Logger, obs StageMetrics, handlers []prerequest.Handler, call *aipapi.Call, meta prerequest.Meta, svc prerequest.Services) (err error) {
	if call == nil {
		return fmt.Errorf("extensions: nil call: %w", aipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return fmt.Errorf("extensions: %w", aipapi.ErrNilContext)
	}
	if execctx.AuxiliaryDepth(ctx) > 0 {
		return nil
	}

	start := time.Now()
	outcome := "ok"
	ctx, span := otel.Tracer(otelScopeExtensions).Start(ctx, "aip.extension.pre_request")
	defer func() {
		if obs != nil {
			obs.ObserveStage(MetricsStagePreRequest, outcome, time.Since(start).Seconds())
		}
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()

	ev := DecisionEvidenceFromContext(ctx)
	for _, h := range prerequest.MaterializeSorted(handlers) {
		if h == nil {
			continue
		}
		if execctx.IsSuppressedPluginID(ctx, h.ID()) {
			continue
		}
		mode := h.FailureMode()
		if mode == sdkhooks.FailureModeUnspecified {
			mode = sdkhooks.FailOpen
		}
		r := runBoundedCall(ctx, ev, feature.StageIDPreRequest, h.ID(), call, func(c context.Context, target *aipapi.Call) (prerequest.Decision, error) {
			return safety.CallValue(safety.BoundaryExtension, "pre_request", func() (prerequest.Decision, error) {
				return h.Handle(c, target, meta, svc)
			})
		})
		iterCtx := r.IterCtx
		deadline := r.Deadline
		if r.ParentCanceled {
			outcome = "error"
			return r.Err
		}
		if r.TimedOut {
			cont, terr := handleProviderTimeout(ctx, log, obs, ev, preRequestFailureCfg, iterCtx, h.ID(), deadline, mode)
			if cont {
				continue
			}
			outcome = "error"
			return terr
		}
		decision, hErr := r.Value, r.Err
		if hErr != nil {
			cont, ferr := handleProviderFailure(ctx, log, obs, ev, preRequestFailureCfg, iterCtx, h.ID(), deadline, mode, hErr)
			if cont {
				continue
			}
			outcome = "error"
			return ferr
		}
		if len(decision.Annotations) != 0 {
			if meta.Annotations == nil {
				meta.Annotations = make(map[string]string, len(decision.Annotations))
			}
			maps.Copy(meta.Annotations, decision.Annotations)
		}
		emitPreRequestDecisionEvidence(iterCtx, ev, h.ID(), decision, deadline)
		if decision.Deny {
			outcome = "denied"
			// Wrap the legacy RejectError as the cause so prerequest.IsRejected and
			// errors.As(*RejectError) keep working while the stable policy denied root
			// lets execerr classify KindPolicyDenied.
			return aipapi.NewPolicyDeniedError(feature.StageIDPreRequest, h.ID(), ReasonPreRequestDenied, CategoryDenied, decision.DenyMessage, prerequest.NewRejectError(h.ID(), decision.DenyMessage))
		}
	}
	if vErr := call.Validate(); vErr != nil {
		outcome = "error"
		emitPreRequestMalformedEvidence(ctx, ev)
		return PolicyErrorFromMalformed(feature.StageIDPreRequest, "pre_request_chain", vErr)
	}
	return nil
}

// preRequestFailureCfg is the shared per-stage naming/evidence config for the
// pre-request admission runner (requirements 6.1, 6.3, 6.5).
var preRequestFailureCfg = stageFailureConfig{
	Stage:        feature.StageIDPreRequest,
	MetricsStage: MetricsStagePreRequest,
	TimeoutMsg:   "pre_request: handler timed out (fail-open)",
	FailureMsg:   "pre_request: handler error (fail-open)",
	PanicStage:   "pre_request",
	ProviderAttr: "handler",
	EmitTimeout:  emitPreRequestTimeoutEvidence,
	EmitFailure:  emitPreRequestFailureEvidence,
}

// emitPreRequestDecisionEvidence projects one admission handler's allow/annotate/
// deny decision into shared evidence (requirements 3.1, 4.2, 4.3, 9.1, 9.5). Denial
// evidence records BackendAttempted=false (no backend attempt committed for pre-backend
// denials; requirements 8.1, 8.6). deadline is the exact evaluation deadline used to
// bound the provider call (zero on the direct path). No-op when no seam is attached.
func emitPreRequestDecisionEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, decision prerequest.Decision, deadline time.Time) {
	if ev == nil || ev.Emitter == nil {
		return
	}
	dctx := decisionContextForDeadline(ctx, ev, feature.StageIDPreRequest, providerID, false, deadline)
	emitDecisionRecord(ctx, ev, ProjectPreRequestDecision(dctx, providerID, decision))
}

// emitPreRequestFailureEvidence projects a handler failure into shared evidence
// with the provider's failure behavior (requirements 6.1, 6.2). deadline is the exact
// evaluation deadline used to bound the provider call (zero on the direct path).
func emitPreRequestFailureEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, _ error, deadline time.Time, mode sdkhooks.FailureMode) {
	emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
		Stage:          feature.StageIDPreRequest,
		ProviderID:     providerID,
		ReasonCode:     ReasonPreRequestFailure,
		ClientCategory: CategoryFailure,
		FailureMode:    mode,
		Deadline:       deadline,
	})
}

// emitPreRequestTimeoutEvidence projects a handler evaluation timeout into shared
// evidence with the provider's failure behavior (requirements 6.1, 6.3). deadline is
// the exact evaluation deadline used to bound the provider call; it is projected onto
// the record's EvaluationDeadline. No-op when no seam is attached.
func emitPreRequestTimeoutEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, deadline time.Time, mode sdkhooks.FailureMode) {
	emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
		Stage:          feature.StageIDPreRequest,
		ProviderID:     providerID,
		ReasonCode:     PolicyReasonTimeout,
		ClientCategory: CategoryFailure,
		FailureMode:    mode,
		Deadline:       deadline,
	})
}

// emitPreRequestMalformedEvidence records a malformed-validation outcome when
// the canonical call fails validation after the pre-request chain
// (requirements 3.1, 6.6). error/none is legal for StageIDPreRequest.
func emitPreRequestMalformedEvidence(ctx context.Context, ev *DecisionEvidence) {
	emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
		Stage:          feature.StageIDPreRequest,
		ProviderID:     "pre_request_chain",
		ReasonCode:     ReasonPreRequestMalformed,
		ClientCategory: CategoryMalformed,
	})
}
