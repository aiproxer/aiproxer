package extensions

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

// RunRequestTransformStage runs request-wide transforms in stable order (design §5, §17).
// Errors from handlers with FailOpen are logged and skipped; FailClosed stops the chain.
// The call is re-validated after the chain completes.
func RunRequestTransformStage(ctx context.Context, log *slog.Logger, obs StageMetrics, transforms []request.Transform, call *aipapi.Call, meta request.RequestMeta, svc request.Services) (err error) {
	if call == nil {
		return fmt.Errorf("extensions: nil call: %w", aipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return fmt.Errorf("extensions: %w", aipapi.ErrNilContext)
	}
	start := time.Now()
	outcome := "ok"
	ctx, span := otel.Tracer(otelScopeExtensions).Start(ctx, "aip.extension.request_transform")
	defer func() {
		if obs != nil {
			obs.ObserveStage(MetricsStageRequestTransform, outcome, time.Since(start).Seconds())
		}
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()

	sorted := request.MaterializeSorted(transforms)
	ev := DecisionEvidenceFromContext(ctx)
	for _, tr := range sorted {
		if tr == nil {
			continue
		}
		if execctx.IsSuppressedPluginID(ctx, tr.ID()) {
			continue
		}
		mode := tr.FailureMode()
		if mode == sdkhooks.FailureModeUnspecified {
			mode = sdkhooks.FailOpen
		}
		before := aipapi.CloneCall(*call)
		r := runBoundedCall(ctx, ev, feature.StageIDRequestWide, tr.ID(), call, func(c context.Context, target *aipapi.Call) (struct{}, error) {
			return struct{}{}, safety.Call(safety.BoundaryExtension, "request_transform", func() error {
				return tr.Handle(c, target, meta, svc)
			})
		})
		iterCtx := r.IterCtx
		deadline := r.Deadline
		if r.ParentCanceled {
			outcome = "error"
			return r.Err
		}
		if r.TimedOut {
			cont, terr := handleProviderTimeout(ctx, log, obs, ev, requestTransformFailureCfg, iterCtx, tr.ID(), deadline, mode)
			if cont {
				continue
			}
			outcome = "error"
			return terr
		}
		hErr := r.Err
		if hErr != nil {
			cont, ferr := handleProviderFailure(ctx, log, obs, ev, requestTransformFailureCfg, iterCtx, tr.ID(), deadline, mode, hErr)
			if cont {
				continue
			}
			outcome = "error"
			return ferr
		}
		mutated := !reflect.DeepEqual(before, *call)
		emitRequestTransformEvidence(iterCtx, ev, tr.ID(), mutated, nil, sdkhooks.FailureModeUnspecified, deadline)
	}
	if vErr := call.Validate(); vErr != nil {
		outcome = "error"
		emitRequestTransformMalformedEvidence(ctx, ev)
		return PolicyErrorFromMalformed(feature.StageIDRequestWide, "request_transform_chain", vErr)
	}
	return nil
}

// requestTransformFailureCfg is the shared per-stage naming/evidence config for the
// request-transform runner (requirements 6.1, 6.3, 6.5). Failure evidence reuses
// emitRequestTransformEvidence with mutated=false, matching the prior inline behavior.
var requestTransformFailureCfg = stageFailureConfig{
	Stage:        feature.StageIDRequestWide,
	MetricsStage: MetricsStageRequestTransform,
	TimeoutMsg:   "request_transform: handler timed out (fail-open)",
	FailureMsg:   "request_transform: handler error (fail-open)",
	PanicStage:   "request_transform",
	ProviderAttr: "transform",
	EmitTimeout:  emitRequestTransformTimeoutEvidence,
	EmitFailure: func(ctx context.Context, ev *DecisionEvidence, providerID string, err error, deadline time.Time, mode sdkhooks.FailureMode) {
		emitRequestTransformEvidence(ctx, ev, providerID, false, err, mode, deadline)
	},
}

// emitRequestTransformEvidence projects one transform's pass/mutation/failure
// outcome into shared evidence and emits it through the context-carried seam
// (requirements 3.2, 4.3, 9.1, 9.5). deadline is the exact evaluation deadline used
// to bound the provider call (zero on the direct path). No-op when no seam is
// attached, preserving the no-observer default. Existing return values and ordering
// are unchanged.
func emitRequestTransformEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, mutated bool, err error, mode sdkhooks.FailureMode, deadline time.Time) {
	if ev == nil || ev.Emitter == nil {
		return
	}
	dctx := decisionContextForDeadline(ctx, ev, feature.StageIDRequestWide, providerID, false, deadline)
	rec := ProjectRequestTransformResult(dctx, providerID, mutated, err)
	if err != nil {
		rec.FailureBehavior = failureBehaviorFromMode(mode)
	}
	emitDecisionRecord(ctx, ev, rec)
}

// emitRequestTransformTimeoutEvidence projects a transform evaluation timeout into
// shared evidence with the provider's failure behavior (requirements 6.1, 6.3).
// deadline is the exact evaluation deadline used to bound the provider call; it is
// projected onto the record's EvaluationDeadline. No-op when no seam is attached.
func emitRequestTransformTimeoutEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, deadline time.Time, mode sdkhooks.FailureMode) {
	emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
		Stage:          feature.StageIDRequestWide,
		ProviderID:     providerID,
		ReasonCode:     PolicyReasonTimeout,
		ClientCategory: CategoryFailure,
		FailureMode:    mode,
		Deadline:       deadline,
	})
}

// emitRequestTransformMalformedEvidence records a malformed-validation outcome
// when the canonical call fails validation after the transform chain
// (requirements 3.2, 6.6). error/none is legal for StageIDRequestWide.
func emitRequestTransformMalformedEvidence(ctx context.Context, ev *DecisionEvidence) {
	emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
		Stage:          feature.StageIDRequestWide,
		ProviderID:     "request_transform_chain",
		ReasonCode:     ReasonRequestTransformMalformed,
		ClientCategory: CategoryMalformed,
	})
}
