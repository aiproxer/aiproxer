package extensions

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

const secretGuardBlockedClientMessage = "request blocked; start a new session"

// RunSecretGuardStage evaluates secret guards in caller-provided execution order.
// guards must already be sorted as produced by [secretguard.MaterializeSorted] or
// [RequestRuntimeSnapshot.SecretGuardExecutionPlane].
func RunSecretGuardStage(ctx context.Context, log *slog.Logger, obs StageMetrics, guards []secretguard.Guard, call *aipapi.Call, meta secretguard.Meta, svc secretguard.Services, audit *SecretGuardAudit, decisionMetrics SecretGuardDecisionMetrics) (block *SecretGuardBlockInfo, err error) {
	if call == nil {
		return nil, fmt.Errorf("extensions: nil call: %w", aipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return nil, fmt.Errorf("extensions: %w", aipapi.ErrNilContext)
	}
	if len(guards) == 0 {
		return nil, nil
	}

	start := time.Now()
	outcome := "ok"
	ctx, span := otel.Tracer(otelScopeExtensions).Start(ctx, "aip.extension.secret_guard")
	defer func() {
		if obs != nil {
			obs.ObserveStage(MetricsStageSecretGuard, outcome, time.Since(start).Seconds())
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
	for _, guard := range guards {
		if guard == nil {
			continue
		}
		if execctx.IsSuppressedPluginID(ctx, guard.ID()) {
			continue
		}
		mode := sdkhooks.FailureMode(guard.FailureMode())
		if mode == sdkhooks.FailureModeUnspecified {
			mode = sdkhooks.FailClosed
		}
		r := runBoundedCall(ctx, ev, feature.StageIDSecretGuard, guard.ID(), call, func(c context.Context, target *aipapi.Call) (secretguard.Decision, error) {
			return safety.CallValue(safety.BoundaryExtension, "secret_guard", func() (secretguard.Decision, error) {
				return guard.Evaluate(c, target, meta, svc)
			})
		})
		iterCtx := r.IterCtx
		deadline := r.Deadline
		if r.ParentCanceled {
			outcome = "error"
			return nil, r.Err
		}
		if r.TimedOut {
			cont, terr := handleProviderTimeout(ctx, log, obs, ev, secretGuardFailureCfg, iterCtx, guard.ID(), deadline, mode)
			if cont {
				continue
			}
			outcome = "error"
			return nil, terr
		}
		decision, hErr := r.Value, r.Err
		if hErr != nil {
			if decisionMetrics != nil {
				decisionMetrics.IncFailure("failed", "failed", string(secretguard.SourceCategoryUnknown))
			}
			cont, ferr := handleProviderFailure(ctx, log, obs, ev, secretGuardFailureCfg, iterCtx, guard.ID(), deadline, mode, hErr)
			if cont {
				continue
			}
			outcome = "error"
			return nil, ferr
		}
		if vErr := decision.Validate(); vErr != nil {
			outcome = "error"
			emitSecretGuardMalformedEvidence(ctx, ev)
			return nil, PolicyErrorFromMalformed(feature.StageIDSecretGuard, guard.ID(), vErr)
		}
		emitSecretGuardDecisionEvidence(iterCtx, ev, guard.ID(), decision, deadline)
		recordSecretGuardDecisionMetrics(decisionMetrics, decision)
		switch decision.Outcome {
		case secretguard.OutcomePass, secretguard.OutcomeLog, secretguard.OutcomeRedacted:
			if aerr := emitSecretGuardAudit(iterCtx, audit, meta, call, guard.ID(), decision, secretguard.QuarantineResultNA, false); aerr != nil {
				outcome = "error"
				return nil, fmt.Errorf("%w: %w", ErrSecretAuditDelivery, aerr)
			}
			continue
		case secretguard.OutcomeBlock:
			outcome = "denied"
			return &SecretGuardBlockInfo{GuardID: guard.ID(), Decision: decision}, nil
		default:
			outcome = "error"
			return nil, PolicyErrorFromMalformed(
				feature.StageIDSecretGuard,
				guard.ID(),
				fmt.Errorf("secret_guard %q returned unknown outcome %q", guard.ID(), decision.Outcome),
			)
		}
	}
	if vErr := call.Validate(); vErr != nil {
		outcome = "error"
		emitSecretGuardMalformedEvidence(ctx, ev)
		return nil, PolicyErrorFromMalformed(feature.StageIDSecretGuard, "secret_guard_chain", vErr)
	}
	return nil, nil
}

func recordSecretGuardDecisionMetrics(metrics SecretGuardDecisionMetrics, decision secretguard.Decision) {
	if metrics == nil {
		return
	}
	action, outcome, cat := secretguard.DecisionMetricLabels(decision)
	metrics.IncDecision(action, outcome, cat)
	for _, c := range decisionMetricSourceCategories(decision.Findings) {
		metrics.IncMatch(action, outcome, c)
	}
	if decision.ScanLimitHit {
		metrics.IncScanLimit(action, "scan_limit", cat)
	}
}

func decisionMetricSourceCategories(findings []secretguard.Finding) []string {
	if len(findings) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(findings))
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		c := string(f.SourceCategory)
		if c == "" {
			c = string(secretguard.SourceCategoryUnknown)
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

var secretGuardFailureCfg = stageFailureConfig{
	Stage:        feature.StageIDSecretGuard,
	MetricsStage: MetricsStageSecretGuard,
	TimeoutMsg:   "secret_guard: handler timed out (fail-open)",
	FailureMsg:   "secret_guard: handler error (fail-open)",
	PanicStage:   "secret_guard",
	ProviderAttr: "guard",
	EmitTimeout:  emitSecretGuardTimeoutEvidence,
	EmitFailure:  emitSecretGuardFailureEvidence,
}

func emitSecretGuardDecisionEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, decision secretguard.Decision, deadline time.Time) {
	if ev == nil || ev.Emitter == nil {
		return
	}
	dctx := decisionContextForDeadline(ctx, ev, feature.StageIDSecretGuard, providerID, false, deadline)
	emitDecisionRecord(ctx, ev, ProjectSecretGuardDecision(dctx, providerID, decision))
}

func emitSecretGuardFailureEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, _ error, deadline time.Time, mode sdkhooks.FailureMode) {
	emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
		Stage:          feature.StageIDSecretGuard,
		ProviderID:     providerID,
		ReasonCode:     ReasonSecretGuardFailure,
		ClientCategory: CategoryFailure,
		FailureMode:    mode,
		Deadline:       deadline,
	})
}

func emitSecretGuardTimeoutEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, deadline time.Time, mode sdkhooks.FailureMode) {
	emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
		Stage:          feature.StageIDSecretGuard,
		ProviderID:     providerID,
		ReasonCode:     PolicyReasonTimeout,
		ClientCategory: CategoryFailure,
		FailureMode:    mode,
		Deadline:       deadline,
	})
}

func emitSecretGuardMalformedEvidence(ctx context.Context, ev *DecisionEvidence) {
	emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
		Stage:          feature.StageIDSecretGuard,
		ProviderID:     "secret_guard_chain",
		ReasonCode:     ReasonSecretGuardMalformed,
		ClientCategory: CategoryMalformed,
	})
}
