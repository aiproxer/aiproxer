package extensions

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

const (
	ReasonAttemptTransformInvalidDecision = "attempt_transform_invalid_decision"
	ReasonAttemptTransformExcluded        = "attempt_transform_excluded"
	ReasonAttemptTransformFailure         = "attempt_transform_failure"
)

type AttemptTransformStageResult struct {
	Excluded   bool
	ReasonCode string
	ProviderID string
}

func RunCandidateAttemptTransformStage(
	ctx context.Context,
	log *slog.Logger,
	obs StageMetrics,
	transforms []request.AttemptTransform,
	call *aipapi.Call,
	meta request.AttemptMeta,
	svc request.Services,
) (res AttemptTransformStageResult, err error) {
	if call == nil {
		return res, fmt.Errorf("extensions: nil call: %w", aipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return res, fmt.Errorf("extensions: %w", aipapi.ErrNilContext)
	}
	filtered := make([]request.AttemptTransform, 0, len(transforms))
	for _, tr := range transforms {
		if tr != nil {
			filtered = append(filtered, tr)
		}
	}
	sorted := request.MaterializeAttemptsSorted(filtered)
	active := 0
	for _, tr := range sorted {
		if !execctx.IsSuppressedPluginID(ctx, tr.ID()) {
			active++
		}
	}
	if active == 0 {
		return res, nil
	}
	start := time.Now()
	outcome := "ok"
	ctx, span := otel.Tracer(otelScopeExtensions).Start(ctx, "aip.extension.candidate_attempt_transform")
	defer func() {
		RecordStageObservation(obs, MetricsStageCandidateAttemptTransform, outcome, time.Since(start).Seconds(), 1, SafeCallReasoningObserveBytes(call))
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()

	ev := DecisionEvidenceFromContext(ctx)
	for _, tr := range sorted {
		if execctx.IsSuppressedPluginID(ctx, tr.ID()) {
			continue
		}
		mode := tr.FailureMode()
		if mode == sdkhooks.FailureModeUnspecified {
			mode = sdkhooks.FailOpen
		}
		before := aipapi.CloneCall(*call)
		r := runBoundedCall(ctx, ev, feature.StageIDCandidateAttemptTransform, tr.ID(), call, func(c context.Context, target *aipapi.Call) (request.AttemptDecision, error) {
			return safety.CallValue(safety.BoundaryExtension, "candidate_attempt_transform", func() (request.AttemptDecision, error) {
				return tr.HandleAttempt(c, target, meta, svc)
			})
		})
		if r.ParentCanceled || r.TimedOut || r.Err != nil {
			*call = before
			if r.ParentCanceled {
				outcome = "error"
				return res, r.Err
			}
			if r.TimedOut {
				if cont, terr := handleProviderTimeout(ctx, log, obs, ev, attemptTransformFailureCfg, r.IterCtx, tr.ID(), r.Deadline, mode); cont {
					continue
				} else {
					outcome = "error"
					return res, terr
				}
			}
			if cont, ferr := handleProviderFailure(ctx, log, obs, ev, attemptTransformFailureCfg, r.IterCtx, tr.ID(), r.Deadline, mode, r.Err); cont {
				continue
			} else {
				outcome = "error"
				return res, ferr
			}
		}
		switch r.Value.Kind {
		case request.AttemptContinue:
			if vErr := call.Validate(); vErr != nil {
				*call = before
				outcome = "error"
				return res, PolicyErrorFromMalformed(feature.StageIDCandidateAttemptTransform, tr.ID(), vErr)
			}
		case request.AttemptExcludeCandidate:
			*call = before
			outcome = "excluded"
			res = AttemptTransformStageResult{Excluded: true, ProviderID: tr.ID(), ReasonCode: safeAttemptTransformReasonCode(r.Value.ReasonCode)}
			return res, nil
		default:
			*call = before
			outcome = "error"
			return res, PolicyErrorFromMalformed(feature.StageIDCandidateAttemptTransform, tr.ID(),
				fmt.Errorf("%s: %q", ReasonAttemptTransformInvalidDecision, r.Value.Kind))
		}
	}
	return res, nil
}

var attemptTransformFailureCfg = stageFailureConfig{
	Stage:        feature.StageIDCandidateAttemptTransform,
	MetricsStage: MetricsStageCandidateAttemptTransform,
	TimeoutMsg:   "candidate_attempt_transform: handler timed out (fail-open)",
	FailureMsg:   "candidate_attempt_transform: handler error (fail-open)",
	PanicStage:   "candidate_attempt_transform",
	ProviderAttr: "transform",
	EmitTimeout: func(ctx context.Context, ev *DecisionEvidence, providerID string, deadline time.Time, mode sdkhooks.FailureMode) {
		emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
			Stage: feature.StageIDCandidateAttemptTransform, ProviderID: providerID,
			ReasonCode: PolicyReasonTimeout, ClientCategory: CategoryFailure, FailureMode: mode, Deadline: deadline,
		})
	},
	EmitFailure: func(ctx context.Context, ev *DecisionEvidence, providerID string, _ error, deadline time.Time, mode sdkhooks.FailureMode) {
		emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
			Stage: feature.StageIDCandidateAttemptTransform, ProviderID: providerID,
			ReasonCode: ReasonAttemptTransformFailure, ClientCategory: CategoryFailure, FailureMode: mode, Deadline: deadline,
		})
	},
}

func safeAttemptTransformReasonCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" || len(code) > 64 {
		return ReasonAttemptTransformExcluded
	}
	for _, r := range code {
		if r > unicode.MaxASCII || (!unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '.' && r != '-') {
			return ReasonAttemptTransformExcluded
		}
	}
	return code
}
