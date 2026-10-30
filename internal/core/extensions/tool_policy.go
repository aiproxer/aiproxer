package extensions

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	"go.opentelemetry.io/otel/attribute"
)

// ToolPolicyStageInput carries inputs for [RunToolPolicyStage].
type ToolPolicyStageInput struct {
	Ctx      context.Context
	Log      *slog.Logger
	Obs      StageMetrics
	Policies []toolpolicy.Policy // execution order; see RunToolPolicyStage
	Event    aipapi.ToolEvent
	Meta     toolpolicy.Meta
	Svc      toolpolicy.Services
}

// RunToolPolicyStage runs provider-neutral tool-call policies before tool reactors.
// in.Policies must already be in execution order (as produced by [toolpolicy.MaterializeSorted] or
// by [RequestRuntimeSnapshot.ToolCallPoliciesExecution]); the stage does not re-sort.
func RunToolPolicyStage(in ToolPolicyStageInput) (err error) {
	ctx := in.Ctx
	log := in.Log
	obs := in.Obs
	policies := in.Policies
	event := in.Event
	meta := in.Meta
	svc := in.Svc
	if ctx == nil {
		return fmt.Errorf("extensions: %w", aipapi.ErrNilContext)
	}
	if err := hooks.ValidateToolEventAfterPolicy("tool_policy:input", &event); err != nil {
		return err
	}
	start := time.Now()
	outcome := "ok"
	ctx, endSpan := startSpan(
		ctx, "aip.extension.tool_policy",
		attribute.String("aip.extension.stage", "tool_policy"),
		attribute.Int("aip.extension.tool_policy.policy_count", len(policies)),
	)
	defer func() {
		if obs != nil {
			obs.ObserveStage(StageToolEventReaction, outcome, time.Since(start).Seconds())
		}
		endSpan(err)
	}()

	ev := DecisionEvidenceFromContext(ctx)
	for _, p := range policies {
		if p == nil {
			continue
		}
		if execctx.IsSuppressedPluginID(ctx, p.ID()) {
			continue
		}
		mode := p.FailureMode()
		if mode == sdkhooks.FailureModeUnspecified {
			mode = sdkhooks.FailClosed
		}
		res := runBoundedProvider(ctx, ev, feature.StageIDToolEventReaction, p.ID(), func(c context.Context) (toolpolicy.Decision, error) {
			return safety.CallValue(safety.BoundaryExtension, "tool_policy", func() (toolpolicy.Decision, error) {
				return p.Handle(c, event, meta, svc)
			})
		})
		deadline := res.Deadline
		if res.ParentCanceled {
			outcome = "error"
			return res.Err
		}
		if res.TimedOut {
			cont, terr := handleProviderTimeout(ctx, log, obs, ev, toolPolicyFailureCfg, res.IterCtx, p.ID(), deadline, mode)
			if cont {
				continue
			}
			outcome = "error"
			return terr
		}
		decision, hErr := res.Value, res.Err
		if hErr != nil {
			cont, ferr := handleProviderFailure(ctx, log, obs, ev, toolPolicyFailureCfg, res.IterCtx, p.ID(), deadline, mode, hErr)
			if cont {
				continue
			}
			outcome = "error"
			return ferr
		}
		emitToolPolicyDecisionEvidence(res.IterCtx, ev, p.ID(), decision, deadline)
		switch decision {
		case toolpolicy.DecisionAllow, toolpolicy.DecisionUnspecified:
			continue
		case toolpolicy.DecisionDeny:
			outcome = "denied"
			return aipapi.NewPolicyDeniedError(feature.StageIDToolEventReaction, p.ID(), ReasonToolPolicyDenied, CategoryDenied, "tool policy denied", nil)
		default:
			outcome = "error"
			return PolicyErrorFromMalformed(feature.StageIDToolEventReaction, p.ID(), fmt.Errorf("tool policy %q returned unknown decision %d", p.ID(), decision))
		}
	}
	return nil
}

// toolPolicyFailureCfg is the shared per-stage naming/evidence config for the
// tool-policy runner (requirements 6.1, 6.3, 6.5). Tool policy runs after the
// backend attempt is committed, so its evidence helpers record BackendAttempted=true.
var toolPolicyFailureCfg = stageFailureConfig{
	Stage:        feature.StageIDToolEventReaction,
	MetricsStage: StageToolEventReaction,
	TimeoutMsg:   "tool_policy: handler timed out (fail-open)",
	FailureMsg:   "tool_policy: handler error (fail-open)",
	PanicStage:   "tool_policy",
	ProviderAttr: "policy",
	EmitTimeout:  emitToolPolicyTimeoutEvidence,
	EmitFailure:  emitToolPolicyFailureEvidence,
}

// emitToolPolicyDecisionEvidence projects one tool policy's allow/deny/malformed
// decision into shared evidence (requirements 3.3, 4.3, 9.1, 9.5). Tool policy
// runs after backend attempt is committed, so BackendAttempted is true. deadline is
// the exact evaluation deadline used to bound the provider call (zero on the direct
// path).
func emitToolPolicyDecisionEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, decision toolpolicy.Decision, deadline time.Time) {
	if ev == nil || ev.Emitter == nil {
		return
	}
	dctx := decisionContextForDeadline(ctx, ev, feature.StageIDToolEventReaction, providerID, false, deadline)
	emitDecisionRecord(ctx, ev, ProjectToolPolicyDecision(dctx, providerID, decision))
}

// emitToolPolicyFailureEvidence projects a tool policy provider failure into
// shared evidence with the provider's failure behavior (requirements 6.1, 6.2).
// deadline is the exact evaluation deadline used to bound the provider call.
func emitToolPolicyFailureEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, _ error, deadline time.Time, mode sdkhooks.FailureMode) {
	emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
		Stage:            feature.StageIDToolEventReaction,
		ProviderID:       providerID,
		BackendAttempted: true,
		ReasonCode:       ReasonToolPolicyFailure,
		ClientCategory:   CategoryFailure,
		FailureMode:      mode,
		Deadline:         deadline,
	})
}

// emitToolPolicyTimeoutEvidence projects a tool policy evaluation timeout into shared
// evidence with the provider's failure behavior (requirements 6.1, 6.3). Tool policy
// runs after backend attempt is committed, so BackendAttempted is true. deadline is
// the exact evaluation deadline used to bound the provider call; it is projected onto
// the record's EvaluationDeadline. No-op when no seam is attached.
func emitToolPolicyTimeoutEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, deadline time.Time, mode sdkhooks.FailureMode) {
	emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
		Stage:            feature.StageIDToolEventReaction,
		ProviderID:       providerID,
		BackendAttempted: true,
		ReasonCode:       PolicyReasonTimeout,
		ClientCategory:   CategoryFailure,
		FailureMode:      mode,
		Deadline:         deadline,
	})
}
