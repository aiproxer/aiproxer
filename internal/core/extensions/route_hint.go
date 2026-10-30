package extensions

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
)

// RunRouteHintStage invokes route hint providers with fail-open semantics (design §17 route_hinting).
func RunRouteHintStage(ctx context.Context, log *slog.Logger, providers []routehint.Provider, call *aipapi.Call, meta routehint.Input) ([]string, error) {
	if call == nil {
		return nil, fmt.Errorf("extensions: nil call: %w", aipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return nil, fmt.Errorf("extensions: %w", aipapi.ErrNilContext)
	}
	var acc []string
	seen := map[string]struct{}{}
	sorted := routehint.MaterializeSorted(providers)
	ev := DecisionEvidenceFromContext(ctx)
	for _, p := range sorted {
		if p == nil {
			continue
		}
		if execctx.IsSuppressedPluginID(ctx, p.ID()) {
			continue
		}
		mode := p.FailureMode()
		if mode == sdkhooks.FailureModeUnspecified {
			mode = sdkhooks.FailOpen
		}
		r := runBoundedProvider(ctx, ev, feature.StageIDRouteHinting, p.ID(), func(c context.Context) (routehint.Result, error) {
			return safety.CallValue(safety.BoundaryExtension, "route_hint_provider", func() (routehint.Result, error) {
				return p.Hint(c, meta)
			})
		})
		if r.ParentCanceled {
			return nil, r.Err
		}
		if r.TimedOut {
			cont, terr := handleProviderTimeout(ctx, log, nil, ev, routeHintFailureCfg, r.IterCtx, p.ID(), r.Deadline, mode)
			if cont {
				continue
			}
			return nil, terr
		}
		iterCtx := r.IterCtx
		deadline := r.Deadline
		res, err := r.Value, r.Err
		if err != nil {
			cont, ferr := handleProviderFailure(ctx, log, nil, ev, routeHintFailureCfg, iterCtx, p.ID(), deadline, mode, err)
			if cont {
				continue
			}
			return nil, ferr
		}
		emitRouteHintEvidence(iterCtx, ev, p.ID(), len(res.PreferredCandidateKeys) > 0, nil, sdkhooks.FailureModeUnspecified, deadline)
		for _, k := range res.PreferredCandidateKeys {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			acc = append(acc, k)
		}
	}
	return acc, nil
}

// routeHintFailureCfg is the shared per-stage naming/evidence config for the
// route-hint advisory runner (requirements 6.1, 6.3, 6.5). Route hinting is advisory:
// no fail-open counter is incremented, so MetricsStage is empty. Failure evidence
// reuses emitRouteHintEvidence with changed=false, matching the prior inline behavior.
var routeHintFailureCfg = stageFailureConfig{
	Stage:        feature.StageIDRouteHinting,
	MetricsStage: "",
	TimeoutMsg:   "route_hinting: provider timed out (fail-open)",
	FailureMsg:   "route_hinting: provider error (fail-open)",
	PanicStage:   "route_hint",
	ProviderAttr: "provider",
	EmitTimeout:  emitRouteHintTimeoutEvidence,
	EmitFailure: func(ctx context.Context, ev *DecisionEvidence, providerID string, err error, deadline time.Time, mode sdkhooks.FailureMode) {
		emitRouteHintEvidence(ctx, ev, providerID, false, err, mode, deadline)
	},
}

// emitRouteHintEvidence projects one route-hint provider outcome into shared
// evidence when representable (requirements 3.6, 4.5, 9.1, 9.5). Route hints
// remain advisory; the policy record does not directly mutate route plans. deadline
// is the exact evaluation deadline used to bound the provider call (zero on the
// direct path). No-op when no seam is attached or when the provider returned no
// candidates and no error (no representable policy semantics).
func emitRouteHintEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, changed bool, err error, mode sdkhooks.FailureMode, deadline time.Time) {
	if ev == nil || ev.Emitter == nil {
		return
	}
	dctx := decisionContextForDeadline(ctx, ev, feature.StageIDRouteHinting, providerID, false, deadline)
	rec, ok := ProjectRouteHintOutcome(dctx, providerID, changed, err)
	if !ok {
		return
	}
	if err != nil {
		rec.FailureBehavior = failureBehaviorFromMode(mode)
	}
	emitDecisionRecord(ctx, ev, rec)
}

// emitRouteHintTimeoutEvidence projects a route-hint provider evaluation timeout into
// shared evidence with the provider's failure behavior (requirements 6.1, 6.3). Route
// hints remain advisory; timeout evidence does not mutate route plans. deadline is the
// exact evaluation deadline used to bound the provider call; it is projected onto the
// record's EvaluationDeadline. No-op when no seam is attached.
func emitRouteHintTimeoutEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, deadline time.Time, mode sdkhooks.FailureMode) {
	emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
		Stage:          feature.StageIDRouteHinting,
		ProviderID:     providerID,
		ReasonCode:     PolicyReasonTimeout,
		ClientCategory: CategoryFailure,
		FailureMode:    mode,
		Deadline:       deadline,
	})
}
