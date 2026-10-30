package extensions

import (
	"maps"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

// Projection reason codes. They are bounded safe tokens ([a-z0-9_.-]) suitable for
// evidence and frontend classification. Each stage family uses a stable reason code
// so observers can distinguish projected outcomes without interpreting provider
// semantics.
const (
	ReasonPreRequestPass            = "prerequest_allow"
	ReasonPreRequestDenied          = "prerequest_denied"
	ReasonPreRequestFailure         = "prerequest_failure"
	ReasonPreRequestMalformed       = "prerequest_malformed"
	ReasonSecretGuardPass           = "secret_guard_pass"
	ReasonSecretGuardLog            = "secret_guard_log"
	ReasonSecretGuardRedacted       = "secret_guard_redacted"
	ReasonSecretGuardBlocked        = "secret_guard_blocked"
	ReasonSecretGuardFailure        = "secret_guard_failure"
	ReasonSecretGuardMalformed      = "secret_guard_malformed"
	ReasonRequestTransformPass      = "request_transform_passthrough"
	ReasonRequestTransformMutated   = "request_transform_mutation"
	ReasonRequestTransformFailure   = "request_transform_failure"
	ReasonRequestTransformMalformed = "request_transform_malformed"
	ReasonToolPolicyAllow           = "tool_policy_allow"
	ReasonToolPolicyDenied          = "tool_policy_denied"
	ReasonToolPolicyFailure         = "tool_policy_failure"
	ReasonToolPolicyMalformed       = "tool_policy_malformed"
	ReasonToolReactorPass           = "tool_reactor_pass"
	ReasonToolReactorRewrite        = "tool_reactor_rewrite"
	ReasonToolReactorReplace        = "tool_reactor_replace"
	ReasonToolReactorSwallow        = "tool_reactor_swallow"
	ReasonCompletionPass            = "completion_pass"
	ReasonCompletionReplace         = "completion_replace"
	ReasonCompletionReplay          = "completion_replay"
	ReasonCompletionReject          = "completion_reject"
	ReasonCompletionFailure         = "completion_failure"
	ReasonCompletionIgnored         = "completion_ignored_post_output"
	ReasonCompletionMalformed       = "completion_malformed"
	ReasonSubmitAnnotated           = "submit_annotated"
	ReasonSubmitRejected            = "submit_rejected"
	ReasonSubmitFailure             = "submit_failure"
	ReasonToolCatalogMutation       = "tool_catalog_mutation"
	ReasonToolCatalogFailure        = "tool_catalog_failure"
	ReasonToolCatalogMalformed      = "tool_catalog_malformed"
	ReasonRouteHintChanged          = "route_hint_changed"
	ReasonRouteHintFailure          = "route_hint_failure"
	ReasonAttemptFailure            = "attempt_failure"
)

// Client categories carried on projected records. Only ClientCategory and
// ClientMessage are intended for frontend use; the values mirror the stable
// policy error categories so a projected record can be classified the same way
// as an explicit policy error. The canonical owner is
// [policydecision.Category*]; these package-level aliases preserve the
// pre-existing extensions API and keep wire/JSON values unchanged.
const (
	CategoryAllowed   = policydecision.CategoryAllowed
	CategorySkipped   = policydecision.CategorySkipped
	CategoryDenied    = policydecision.CategoryDenied
	CategoryFailure   = policydecision.CategoryFailure
	CategoryObserved  = policydecision.CategoryObserved
	CategoryMalformed = policydecision.CategoryMalformed
)

// recordFromContext lifts the safe, request-scoped attribution and lifecycle fields
// from a policydecision.Context into a policydecision.Record shell. backendAttempted
// is set by the caller according to the stage family's lifecycle position: false for
// pre-backend stages (request transform, pre-request, submit, tool catalog, route
// hint) and true for stream/attempt stages (tool policy, tool reactor, completion,
// attempt observation). The stage and provider ref come from ctx so a single
// BuildDecisionContext call drives both decision evaluation and evidence shape.
func recordFromContext(ctx policydecision.Context, providerID string, backendAttempted bool) policydecision.Record {
	stage := ctx.Stage
	return policydecision.Record{
		TraceID:            ctx.TraceID,
		ALegID:             ctx.ALegID,
		BLegID:             ctx.BLegID,
		AttemptSeq:         ctx.AttemptSeq,
		Stage:              stage,
		Provider:           policydecision.ProviderRef{ID: providerID, Stage: stage},
		Scope:              ctx.Scope.Clone(),
		Annotations:        maps.Clone(ctx.Annotations),
		OutputCommitted:    ctx.OutputCommitted,
		BackendAttempted:   backendAttempted,
		EvaluationTimeout:  ctx.EvaluationTimeout,
		EvaluationDeadline: ctx.EvaluationDeadline,
	}
}

// mergeAnnotations returns a new map combining base and extra. Nil inputs are
// preserved as nil when both are nil. The returned map is independent of both inputs.
func mergeAnnotations(base, extra map[string]string) map[string]string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(extra))
	maps.Copy(out, base)
	maps.Copy(out, extra)
	return out
}

// ProjectSecretGuardDecision projects a secret-guard Evaluate decision into a shared
// policy decision record. Findings, FailureReason, and any secret material must never
// appear in ClientMessage or ReasonCode (D8/D9). BackendAttempted is always false.
func ProjectSecretGuardDecision(ctx policydecision.Context, providerID string, decision secretguard.Decision) policydecision.Record {
	rec := recordFromContext(ctx, providerID, false)
	switch decision.Outcome {
	case secretguard.OutcomeBlock:
		rec.Outcome = policydecision.OutcomeDeny
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonSecretGuardBlocked
		rec.ClientCategory = CategoryDenied
		rec.ClientMessage = secretGuardBlockedClientMessage
	case secretguard.OutcomeRedacted:
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectMutate
		rec.ReasonCode = ReasonSecretGuardRedacted
		rec.ClientCategory = CategoryAllowed
	case secretguard.OutcomeLog:
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonSecretGuardLog
		rec.ClientCategory = CategoryAllowed
	case secretguard.OutcomePass:
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonSecretGuardPass
		rec.ClientCategory = CategoryAllowed
	default:
		rec.Outcome = policydecision.OutcomeError
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonSecretGuardMalformed
		rec.ClientCategory = CategoryMalformed
	}
	return rec
}

// ProjectPreRequestDecision projects a pre-request admission handler decision into a
// shared policy decision record (requirements 1.7, 3.1, 4.6, 9.1, 9.5). The projection
// is lossy-but-compatible: allow/annotate/deny map directly to the legality table for
// the pre-request stage. Provider failure, timeout, skip, and no-backend-attempt
// denial evidence are emitted by Phase 4 runtime integration through the error and
// timeout helpers; this helper only projects the Decision outcome.
func ProjectPreRequestDecision(ctx policydecision.Context, providerID string, decision prerequest.Decision) policydecision.Record {
	rec := recordFromContext(ctx, providerID, false)
	if decision.Deny {
		rec.Outcome = policydecision.OutcomeDeny
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonPreRequestDenied
		rec.ClientCategory = CategoryDenied
		rec.ClientMessage = decision.DenyMessage
		rec.Annotations = mergeAnnotations(ctx.Annotations, decision.Annotations)
		return rec
	}
	rec.Annotations = mergeAnnotations(ctx.Annotations, decision.Annotations)
	if len(decision.Annotations) != 0 {
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectAnnotate
		rec.ReasonCode = ReasonPreRequestPass
		rec.ClientCategory = CategoryAllowed
		return rec
	}
	rec.Outcome = policydecision.OutcomeAllow
	rec.Effect = policydecision.EffectNone
	rec.ReasonCode = ReasonPreRequestPass
	rec.ClientCategory = CategoryAllowed
	return rec
}

// ProjectRequestTransformResult projects a request-wide transform outcome into a
// shared policy decision record (requirements 1.7, 3.2, 4.6, 9.1, 9.5). mutated reports
// whether the transform changed the canonical call; err is the transform's returned
// error. Timeout and fail-open skip evidence are emitted by Phase 4 runtime
// integration; this helper projects pass-through, mutation, and provider failure.
func ProjectRequestTransformResult(ctx policydecision.Context, providerID string, mutated bool, err error) policydecision.Record {
	rec := recordFromContext(ctx, providerID, false)
	if err != nil {
		rec.Outcome = policydecision.OutcomeError
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonRequestTransformFailure
		rec.ClientCategory = CategoryFailure
		return rec
	}
	if mutated {
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectMutate
		rec.ReasonCode = ReasonRequestTransformMutated
		rec.ClientCategory = CategoryAllowed
		return rec
	}
	rec.Outcome = policydecision.OutcomeAllow
	rec.Effect = policydecision.EffectNone
	rec.ReasonCode = ReasonRequestTransformPass
	rec.ClientCategory = CategoryAllowed
	return rec
}

// ProjectToolPolicyDecision projects a canonical tool-call policy decision into a
// shared policy decision record (requirements 1.7, 3.3, 4.6, 9.1, 9.5). The tool
// policy stage runs after backend attempt is committed, so BackendAttempted is true.
func ProjectToolPolicyDecision(ctx policydecision.Context, providerID string, decision toolpolicy.Decision) policydecision.Record {
	rec := recordFromContext(ctx, providerID, true)
	switch decision {
	case toolpolicy.DecisionDeny:
		rec.Outcome = policydecision.OutcomeDeny
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonToolPolicyDenied
		rec.ClientCategory = CategoryDenied
	case toolpolicy.DecisionAllow, toolpolicy.DecisionUnspecified:
		// DecisionAllow and DecisionUnspecified both pass the tool call through; the
		// runner treats unspecified as allow (tool_policy.go).
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonToolPolicyAllow
		rec.ClientCategory = CategoryAllowed
	default:
		// Out-of-range decisions are returned as errors by the runner (tool_policy.go
		// default case). Evidence must not fabricate allow/none for malformed provider
		// output; project as OutcomeError/EffectNone with a malformed-safe reason and
		// category, legal for StageIDToolEventReaction.
		rec.Outcome = policydecision.OutcomeError
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonToolPolicyMalformed
		rec.ClientCategory = CategoryMalformed
	}
	return rec
}

// ProjectToolReactorDecision projects a tool reactor outcome into a shared policy
// decision record (requirements 1.7, 3.3, 4.6, 9.1, 9.5). Frontend-specific tool
// syntax is kept out of evidence semantics: only the canonical ToolDecision enum is
// projected. Tool reactor runs after backend attempt is committed, so
// BackendAttempted is true.
func ProjectToolReactorDecision(ctx policydecision.Context, providerID string, decision sdkhooks.ToolDecision) policydecision.Record {
	rec := recordFromContext(ctx, providerID, true)
	switch decision {
	case sdkhooks.ToolRewrite:
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectMutate
		rec.ReasonCode = ReasonToolReactorRewrite
		rec.ClientCategory = CategoryAllowed
	case sdkhooks.ToolReplace:
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectReplace
		rec.ReasonCode = ReasonToolReactorReplace
		rec.ClientCategory = CategoryAllowed
	case sdkhooks.ToolSwallow:
		rec.Outcome = policydecision.OutcomeSkip
		rec.Effect = policydecision.EffectSwallow
		rec.ReasonCode = ReasonToolReactorSwallow
		rec.ClientCategory = CategorySkipped
	default:
		// ToolPass, ToolDecisionUnspecified, and any out-of-range value project as
		// allow/none, matching the reactor runner's pass-through default (tool.go).
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonToolReactorPass
		rec.ClientCategory = CategoryAllowed
	}
	return rec
}

// ProjectCompletionOutcome projects a completion-gate outcome into a shared policy
// decision record (requirements 1.7, 3.4, 4.6, 9.1, 9.5). When output is already
// committed, a replacement outcome is ignored at runtime (completion_run.go), so the
// projection records skip/none with a post-output reason rather than fabricating a
// replace effect. Replay is unaffected by output-committed state. The completion gate
// runs after backend attempt is committed, so BackendAttempted is true.
func ProjectCompletionOutcome(ctx policydecision.Context, providerID string, outcome completion.Outcome) policydecision.Record {
	rec := recordFromContext(ctx, providerID, true)
	switch outcome.Kind {
	case completion.OutcomeReplace:
		if ctx.OutputCommitted {
			rec.Outcome = policydecision.OutcomeSkip
			rec.Effect = policydecision.EffectNone
			rec.ReasonCode = ReasonCompletionIgnored
			rec.ClientCategory = CategorySkipped
			return rec
		}
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectReplace
		rec.ReasonCode = ReasonCompletionReplace
		rec.ClientCategory = CategoryAllowed
	case completion.OutcomeReplayOriginal:
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectReplay
		rec.ReasonCode = ReasonCompletionReplay
		rec.ClientCategory = CategoryAllowed
	case completion.OutcomeReject:
		rec.Outcome = policydecision.OutcomeDeny
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonCompletionReject
		rec.ClientCategory = CategoryDenied
	case completion.OutcomePassOriginal:
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonCompletionPass
		rec.ClientCategory = CategoryAllowed
	default:
		// Unknown outcome kinds are treated by the completion runner as malformed for
		// fail-closed and ignored for fail-open (completion_run.go default case). Evidence
		// must not fabricate pass for malformed output; project as OutcomeError/EffectNone
		// with a malformed-safe reason and category, legal for StageIDCompletionGating.
		rec.Outcome = policydecision.OutcomeError
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonCompletionMalformed
		rec.ClientCategory = CategoryMalformed
	}
	return rec
}

// ProjectSubmitOutcome projects a submit-hook outcome into a shared policy decision
// record (requirements 1.7, 4.6, 9.1, 9.5). The second return value is ok=false when
// the outcome has no observable policy semantics to represent without inventing new
// submit semantics: a submit hook that neither rejected, errored, nor annotated did
// not produce a policy decision the shared vocabulary can describe. Runtime behavior
// is still preserved; callers simply skip emission when ok is false.
func ProjectSubmitOutcome(ctx policydecision.Context, providerID string, rejected bool, annotations map[string]string, err error) (policydecision.Record, bool) {
	if err != nil {
		rec := recordFromContext(ctx, providerID, false)
		rec.Outcome = policydecision.OutcomeError
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonSubmitFailure
		rec.ClientCategory = CategoryFailure
		return rec, true
	}
	if rejected {
		rec := recordFromContext(ctx, providerID, false)
		rec.Outcome = policydecision.OutcomeDeny
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonSubmitRejected
		rec.ClientCategory = CategoryDenied
		rec.Annotations = mergeAnnotations(ctx.Annotations, annotations)
		return rec, true
	}
	if len(annotations) != 0 {
		rec := recordFromContext(ctx, providerID, false)
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectAnnotate
		rec.ReasonCode = ReasonSubmitAnnotated
		rec.ClientCategory = CategoryAllowed
		rec.Annotations = mergeAnnotations(ctx.Annotations, annotations)
		return rec, true
	}
	// No reject, no error, no annotations: the submit hook produced no observable
	// policy semantics. Submit hooks may mutate the canonical call, but SubmitDecision
	// does not report mutation, so projecting allow/mutate would invent semantics and
	// projecting allow/none would fabricate a decision where none occurred.
	return policydecision.Record{}, false
}

// ProjectToolCatalogOutcome projects a tool-catalog filter outcome into a shared
// policy decision record (requirements 1.7, 4.6, 9.1, 9.5). ok=false when the filter
// neither mutated the advertised tool list nor errored: a pure no-op filter has no
// policy semantics to represent. Advertised-tool mutation behavior is unchanged; the
// helper only records compatible evidence.
func ProjectToolCatalogOutcome(ctx policydecision.Context, providerID string, mutated bool, err error) (policydecision.Record, bool) {
	if err != nil {
		rec := recordFromContext(ctx, providerID, false)
		rec.Outcome = policydecision.OutcomeError
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonToolCatalogFailure
		rec.ClientCategory = CategoryFailure
		return rec, true
	}
	if mutated {
		rec := recordFromContext(ctx, providerID, false)
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectMutate
		rec.ReasonCode = ReasonToolCatalogMutation
		rec.ClientCategory = CategoryAllowed
		return rec, true
	}
	return policydecision.Record{}, false
}

// ProjectRouteHintOutcome projects a route-hint provider outcome into a shared policy
// decision record (requirements 1.7, 4.5, 4.6, 9.1, 9.5). Route hints remain advisory
// through existing route preference contracts; the policy record does not directly
// mutate route plans. ok=false when the provider returned no preferred candidates and
// no error: an empty advisory opinion has no policy semantics to represent.
func ProjectRouteHintOutcome(ctx policydecision.Context, providerID string, changed bool, err error) (policydecision.Record, bool) {
	if err != nil {
		rec := recordFromContext(ctx, providerID, false)
		rec.Outcome = policydecision.OutcomeError
		rec.Effect = policydecision.EffectNone
		rec.ReasonCode = ReasonRouteHintFailure
		rec.ClientCategory = CategoryFailure
		return rec, true
	}
	if changed {
		rec := recordFromContext(ctx, providerID, false)
		rec.Outcome = policydecision.OutcomeAllow
		rec.Effect = policydecision.EffectAnnotate
		rec.ReasonCode = ReasonRouteHintChanged
		rec.ClientCategory = CategoryObserved
		return rec, true
	}
	return policydecision.Record{}, false
}

// ProjectAttemptObservation projects an attempt-lifecycle observation into a shared
// policy decision record (requirements 1.7, 4.5, 4.6, 9.1, 9.5). Attempt lifecycle
// records are observational; ok=false when the attempt succeeded with no error, since
// a successful observation has no policy semantics to represent. The attempt stage
// runs after backend attempt is committed, so BackendAttempted is true.
func ProjectAttemptObservation(ctx policydecision.Context, providerID string, err error) (policydecision.Record, bool) {
	if err == nil {
		return policydecision.Record{}, false
	}
	rec := recordFromContext(ctx, providerID, true)
	rec.Outcome = policydecision.OutcomeError
	rec.Effect = policydecision.EffectNone
	rec.ReasonCode = ReasonAttemptFailure
	rec.ClientCategory = CategoryFailure
	return rec, true
}
