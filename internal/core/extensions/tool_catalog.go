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
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

// RunToolCatalogFilterStage runs tool catalog filters in stable order (design §4, §17).
// After the chain, [aipapi.ReconcileToolChoiceAfterToolListChange] runs once, then the call is validated.
func RunToolCatalogFilterStage(ctx context.Context, log *slog.Logger, obs StageMetrics, filters []toolcatalog.Filter, call *aipapi.Call, meta toolcatalog.CatalogMeta, svc toolcatalog.Services) (err error) {
	if call == nil {
		return fmt.Errorf("extensions: nil call: %w", aipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return fmt.Errorf("extensions: %w", aipapi.ErrNilContext)
	}
	start := time.Now()
	outcome := "ok"
	ctx, span := otel.Tracer(otelScopeExtensions).Start(ctx, "aip.extension.tool_catalog")
	defer func() {
		if obs != nil {
			obs.ObserveStage(StageToolCatalog, outcome, time.Since(start).Seconds())
		}
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()

	sorted := toolcatalog.MaterializeSorted(filters)
	ev := DecisionEvidenceFromContext(ctx)
	for _, f := range sorted {
		if f == nil {
			continue
		}
		if execctx.IsSuppressedPluginID(ctx, f.ID()) {
			continue
		}
		mode := f.FailureMode()
		if mode == sdkhooks.FailureModeUnspecified {
			mode = sdkhooks.FailOpen
		}
		before := aipapi.CloneCall(*call)
		r := runBoundedCall(ctx, ev, feature.StageIDToolCatalog, f.ID(), call, func(c context.Context, target *aipapi.Call) (struct{}, error) {
			return struct{}{}, safety.Call(safety.BoundaryExtension, "tool_catalog_filter", func() error {
				return f.Handle(c, target, meta, svc)
			})
		})
		iterCtx := r.IterCtx
		deadline := r.Deadline
		if r.ParentCanceled {
			outcome = "error"
			return r.Err
		}
		if r.TimedOut {
			cont, terr := handleProviderTimeout(ctx, log, obs, ev, toolCatalogFailureCfg, iterCtx, f.ID(), deadline, mode)
			if cont {
				continue
			}
			outcome = "error"
			return terr
		}
		hErr := r.Err
		if hErr != nil {
			cont, ferr := handleProviderFailure(ctx, log, obs, ev, toolCatalogFailureCfg, iterCtx, f.ID(), deadline, mode, hErr)
			if cont {
				continue
			}
			outcome = "error"
			return ferr
		}
		mutated := !reflect.DeepEqual(before, *call)
		emitToolCatalogEvidence(iterCtx, ev, f.ID(), mutated, nil, sdkhooks.FailureModeUnspecified, deadline)
	}
	aipapi.ReconcileToolChoiceAfterToolListChange(call)
	if vErr := call.Validate(); vErr != nil {
		outcome = "error"
		emitToolCatalogMalformedEvidence(ctx, ev)
		return PolicyErrorFromMalformed(feature.StageIDToolCatalog, "tool_catalog_chain", vErr)
	}
	return nil
}

// toolCatalogFailureCfg is the shared per-stage naming/evidence config for the
// tool-catalog filter runner (requirements 6.1, 6.3, 6.5). Failure evidence reuses
// emitToolCatalogEvidence with mutated=false, matching the prior inline behavior.
var toolCatalogFailureCfg = stageFailureConfig{
	Stage:        feature.StageIDToolCatalog,
	MetricsStage: StageToolCatalog,
	TimeoutMsg:   "tool_catalog_filter: handler timed out (fail-open)",
	FailureMsg:   "tool_catalog_filter: handler error (fail-open)",
	PanicStage:   "tool_catalog_filter",
	ProviderAttr: "filter",
	EmitTimeout:  emitToolCatalogTimeoutEvidence,
	EmitFailure: func(ctx context.Context, ev *DecisionEvidence, providerID string, err error, deadline time.Time, mode sdkhooks.FailureMode) {
		emitToolCatalogEvidence(ctx, ev, providerID, false, err, mode, deadline)
	},
}

// emitToolCatalogEvidence projects one tool-catalog filter outcome into shared
// evidence when representable (requirements 3.5, 4.3, 9.1, 9.5). deadline is the
// exact evaluation deadline used to bound the provider call (zero on the direct
// path). No-op when no seam is attached or when the outcome has no representable
// policy semantics (pure no-op filter). Advertised-tool mutation behavior is unchanged.
func emitToolCatalogEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, mutated bool, err error, mode sdkhooks.FailureMode, deadline time.Time) {
	if ev == nil || ev.Emitter == nil {
		return
	}
	dctx := decisionContextForDeadline(ctx, ev, feature.StageIDToolCatalog, providerID, false, deadline)
	rec, ok := ProjectToolCatalogOutcome(dctx, providerID, mutated, err)
	if !ok {
		return
	}
	if err != nil {
		rec.FailureBehavior = failureBehaviorFromMode(mode)
	}
	emitDecisionRecord(ctx, ev, rec)
}

// emitToolCatalogTimeoutEvidence projects a tool-catalog filter evaluation timeout
// into shared evidence with the provider's failure behavior (requirements 6.1, 6.3).
// deadline is the exact evaluation deadline used to bound the provider call; it is
// projected onto the record's EvaluationDeadline. No-op when no seam is attached.
func emitToolCatalogTimeoutEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, deadline time.Time, mode sdkhooks.FailureMode) {
	emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
		Stage:          feature.StageIDToolCatalog,
		ProviderID:     providerID,
		ReasonCode:     PolicyReasonTimeout,
		ClientCategory: CategoryFailure,
		FailureMode:    mode,
		Deadline:       deadline,
	})
}

// emitToolCatalogMalformedEvidence records a malformed-validation outcome when
// the canonical call fails validation after the tool-catalog chain
// (requirements 3.5, 6.6). error/none is legal for StageIDToolCatalog.
func emitToolCatalogMalformedEvidence(ctx context.Context, ev *DecisionEvidence) {
	emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
		Stage:          feature.StageIDToolCatalog,
		ProviderID:     "tool_catalog_chain",
		ReasonCode:     ReasonToolCatalogMalformed,
		ClientCategory: CategoryMalformed,
	})
}
