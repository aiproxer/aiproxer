package extensions_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

// preBackendContext builds a policydecision.Context for a pre-backend stage so the
// projector tests exercise the same shape Phase 4 runtime integration will use.
func preBackendContext(stage string) policydecision.Context {
	ctx := extensions.BuildDecisionContext(sampleViews(), stage, "p1", extensions.DecisionContextOptions{})
	return ctx
}

func streamContext(stage string, outputCommitted bool) policydecision.Context {
	ctx := extensions.BuildDecisionContext(sampleViews(), stage, "p1", extensions.DecisionContextOptions{
		OutputCommitted: outputCommitted,
	})
	return ctx
}

// assertLegal validates the record against the legality table so projections never
// emit malformed evidence.
func assertLegal(t *testing.T, r policydecision.Record) {
	t.Helper()
	if err := extensions.ValidateDecisionRecord(r); err != nil {
		t.Fatalf("projected record not legal: %#v -> %v", r, err)
	}
}

func TestProjectPreRequestDecision_AllowAnnotateDeny(t *testing.T) {
	t.Parallel()
	ctx := preBackendContext(feature.StageIDPreRequest)

	allow := extensions.ProjectPreRequestDecision(ctx, "p1", prerequest.Decision{})
	if allow.Outcome != policydecision.OutcomeAllow || allow.Effect != policydecision.EffectNone {
		t.Fatalf("allow mismatch: outcome=%q effect=%q", allow.Outcome, allow.Effect)
	}
	if allow.BackendAttempted {
		t.Fatalf("pre-request must record no backend attempt")
	}
	if allow.Provider.ID != "p1" || allow.Provider.Stage != feature.StageIDPreRequest {
		t.Fatalf("provider mismatch: %#v", allow.Provider)
	}
	assertLegal(t, allow)

	annotated := extensions.ProjectPreRequestDecision(ctx, "p1", prerequest.Decision{
		Annotations: map[string]string{"team": "platform"},
	})
	if annotated.Outcome != policydecision.OutcomeAllow || annotated.Effect != policydecision.EffectAnnotate {
		t.Fatalf("annotate mismatch: outcome=%q effect=%q", annotated.Outcome, annotated.Effect)
	}
	if annotated.Annotations["team"] != "platform" {
		t.Fatalf("decision annotations not projected: %#v", annotated.Annotations)
	}
	assertLegal(t, annotated)

	denied := extensions.ProjectPreRequestDecision(ctx, "p1", prerequest.Deny("not allowed"))
	if denied.Outcome != policydecision.OutcomeDeny || denied.Effect != policydecision.EffectNone {
		t.Fatalf("deny mismatch: outcome=%q effect=%q", denied.Outcome, denied.Effect)
	}
	if denied.ClientMessage != "not allowed" {
		t.Fatalf("deny client message not carried: %q", denied.ClientMessage)
	}
	if denied.ReasonCode != "prerequest_denied" {
		t.Fatalf("deny reason code mismatch: %q", denied.ReasonCode)
	}
	assertLegal(t, denied)
}

func TestProjectPreRequestDecision_MergesContextAnnotations(t *testing.T) {
	t.Parallel()
	ctx := preBackendContext(feature.StageIDPreRequest)
	rec := extensions.ProjectPreRequestDecision(ctx, "p1", prerequest.Decision{
		Annotations: map[string]string{"decision": "a"},
	})
	if rec.Annotations["source"] != "builder-test" || rec.Annotations["decision"] != "a" {
		t.Fatalf("context+decision annotations not merged: %#v", rec.Annotations)
	}
}

func TestProjectPreRequestDecision_ClonesAnnotationsFromInput(t *testing.T) {
	t.Parallel()
	ctx := preBackendContext(feature.StageIDPreRequest)
	decision := prerequest.Decision{Annotations: map[string]string{"k": "v"}}
	rec := extensions.ProjectPreRequestDecision(ctx, "p1", decision)
	rec.Annotations["k"] = "tampered"
	if decision.Annotations["k"] != "v" {
		t.Fatalf("input decision annotations mutated via record: %q", decision.Annotations["k"])
	}
}

func TestProjectRequestTransformResult_PassThroughMutationFailure(t *testing.T) {
	t.Parallel()
	ctx := preBackendContext(feature.StageIDRequestWide)

	pass := extensions.ProjectRequestTransformResult(ctx, "p1", false, nil)
	if pass.Outcome != policydecision.OutcomeAllow || pass.Effect != policydecision.EffectNone {
		t.Fatalf("passthrough mismatch: outcome=%q effect=%q", pass.Outcome, pass.Effect)
	}
	if pass.ReasonCode != "request_transform_passthrough" {
		t.Fatalf("passthrough reason mismatch: %q", pass.ReasonCode)
	}
	assertLegal(t, pass)

	mutated := extensions.ProjectRequestTransformResult(ctx, "p1", true, nil)
	if mutated.Outcome != policydecision.OutcomeAllow || mutated.Effect != policydecision.EffectMutate {
		t.Fatalf("mutation mismatch: outcome=%q effect=%q", mutated.Outcome, mutated.Effect)
	}
	if mutated.ReasonCode != "request_transform_mutation" {
		t.Fatalf("mutation reason mismatch: %q", mutated.ReasonCode)
	}
	assertLegal(t, mutated)

	failed := extensions.ProjectRequestTransformResult(ctx, "p1", false, errors.New("boom"))
	if failed.Outcome != policydecision.OutcomeError || failed.Effect != policydecision.EffectNone {
		t.Fatalf("failure mismatch: outcome=%q effect=%q", failed.Outcome, failed.Effect)
	}
	if failed.ReasonCode != "request_transform_failure" {
		t.Fatalf("failure reason mismatch: %q", failed.ReasonCode)
	}
	assertLegal(t, failed)
}

func TestProjectToolPolicyDecision_AllowDenyUnspecified(t *testing.T) {
	t.Parallel()
	ctx := streamContext(feature.StageIDToolEventReaction, false)

	allow := extensions.ProjectToolPolicyDecision(ctx, "p1", toolpolicy.DecisionAllow)
	if allow.Outcome != policydecision.OutcomeAllow || allow.Effect != policydecision.EffectNone {
		t.Fatalf("allow mismatch: outcome=%q effect=%q", allow.Outcome, allow.Effect)
	}
	if !allow.BackendAttempted {
		t.Fatalf("tool policy must record backend attempted (stream stage)")
	}
	assertLegal(t, allow)

	deny := extensions.ProjectToolPolicyDecision(ctx, "p1", toolpolicy.DecisionDeny)
	if deny.Outcome != policydecision.OutcomeDeny || deny.Effect != policydecision.EffectNone {
		t.Fatalf("deny mismatch: outcome=%q effect=%q", deny.Outcome, deny.Effect)
	}
	if deny.ReasonCode != "tool_policy_denied" {
		t.Fatalf("deny reason mismatch: %q", deny.ReasonCode)
	}
	assertLegal(t, deny)

	unspec := extensions.ProjectToolPolicyDecision(ctx, "p1", toolpolicy.DecisionUnspecified)
	if unspec.Outcome != policydecision.OutcomeAllow || unspec.Effect != policydecision.EffectNone {
		t.Fatalf("unspecified must project as allow/none: outcome=%q effect=%q", unspec.Outcome, unspec.Effect)
	}
	assertLegal(t, unspec)
}

func TestProjectToolPolicyDecision_UnknownDecisionProjectsAsMalformedError(t *testing.T) {
	t.Parallel()
	ctx := streamContext(feature.StageIDToolEventReaction, false)

	// An out-of-range Decision is treated by the existing runner as an error (tool_policy.go
	// default case). Evidence must not fabricate allow/none for malformed provider output; it
	// projects as OutcomeError/EffectNone with a malformed-safe reason and category that is
	// legal for StageIDToolEventReaction.
	unknown := extensions.ProjectToolPolicyDecision(ctx, "p1", toolpolicy.Decision(99))
	if unknown.Outcome != policydecision.OutcomeError || unknown.Effect != policydecision.EffectNone {
		t.Fatalf("unknown decision must project as error/none: outcome=%q effect=%q", unknown.Outcome, unknown.Effect)
	}
	if unknown.ReasonCode != "tool_policy_malformed" {
		t.Fatalf("unknown decision reason mismatch: %q", unknown.ReasonCode)
	}
	if unknown.ClientCategory != "policy_malformed" {
		t.Fatalf("unknown decision category mismatch: %q", unknown.ClientCategory)
	}
	if !unknown.BackendAttempted {
		t.Fatalf("tool policy must record backend attempted (stream stage)")
	}
	assertLegal(t, unknown)
}

func TestProjectToolReactorDecision_PassRewriteReplaceSwallow(t *testing.T) {
	t.Parallel()
	ctx := streamContext(feature.StageIDToolEventReaction, false)

	pass := extensions.ProjectToolReactorDecision(ctx, "p1", sdkhooks.ToolPass)
	if pass.Outcome != policydecision.OutcomeAllow || pass.Effect != policydecision.EffectNone {
		t.Fatalf("pass mismatch: outcome=%q effect=%q", pass.Outcome, pass.Effect)
	}
	assertLegal(t, pass)

	rewrite := extensions.ProjectToolReactorDecision(ctx, "p1", sdkhooks.ToolRewrite)
	if rewrite.Outcome != policydecision.OutcomeAllow || rewrite.Effect != policydecision.EffectMutate {
		t.Fatalf("rewrite mismatch: outcome=%q effect=%q", rewrite.Outcome, rewrite.Effect)
	}
	assertLegal(t, rewrite)

	replace := extensions.ProjectToolReactorDecision(ctx, "p1", sdkhooks.ToolReplace)
	if replace.Outcome != policydecision.OutcomeAllow || replace.Effect != policydecision.EffectReplace {
		t.Fatalf("replace mismatch: outcome=%q effect=%q", replace.Outcome, replace.Effect)
	}
	assertLegal(t, replace)

	swallow := extensions.ProjectToolReactorDecision(ctx, "p1", sdkhooks.ToolSwallow)
	if swallow.Outcome != policydecision.OutcomeSkip || swallow.Effect != policydecision.EffectSwallow {
		t.Fatalf("swallow mismatch: outcome=%q effect=%q", swallow.Outcome, swallow.Effect)
	}
	assertLegal(t, swallow)

	unspec := extensions.ProjectToolReactorDecision(ctx, "p1", sdkhooks.ToolDecisionUnspecified)
	if unspec.Outcome != policydecision.OutcomeAllow || unspec.Effect != policydecision.EffectNone {
		t.Fatalf("unspecified must project as allow/none: outcome=%q effect=%q", unspec.Outcome, unspec.Effect)
	}
	assertLegal(t, unspec)
}

func TestProjectCompletionOutcome_PassReplaceReplayReject(t *testing.T) {
	t.Parallel()
	ctx := streamContext(feature.StageIDCompletionGating, false)

	pass := extensions.ProjectCompletionOutcome(ctx, "p1", completion.PassOriginalOutcome())
	if pass.Outcome != policydecision.OutcomeAllow || pass.Effect != policydecision.EffectNone {
		t.Fatalf("pass mismatch: outcome=%q effect=%q", pass.Outcome, pass.Effect)
	}
	assertLegal(t, pass)

	replace := extensions.ProjectCompletionOutcome(ctx, "p1", completion.ReplaceOutcome([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}))
	if replace.Outcome != policydecision.OutcomeAllow || replace.Effect != policydecision.EffectReplace {
		t.Fatalf("replace mismatch: outcome=%q effect=%q", replace.Outcome, replace.Effect)
	}
	assertLegal(t, replace)

	replay := extensions.ProjectCompletionOutcome(ctx, "p1", completion.ReplayOriginalOutcome())
	if replay.Outcome != policydecision.OutcomeAllow || replay.Effect != policydecision.EffectReplay {
		t.Fatalf("replay mismatch: outcome=%q effect=%q", replay.Outcome, replay.Effect)
	}
	assertLegal(t, replay)

	reject := extensions.ProjectCompletionOutcome(ctx, "p1", completion.RejectOutcome(errors.New("nope")))
	if reject.Outcome != policydecision.OutcomeDeny || reject.Effect != policydecision.EffectNone {
		t.Fatalf("reject mismatch: outcome=%q effect=%q", reject.Outcome, reject.Effect)
	}
	if reject.ReasonCode != "completion_reject" {
		t.Fatalf("reject reason mismatch: %q", reject.ReasonCode)
	}
	assertLegal(t, reject)
}

func TestProjectCompletionOutcome_PostOutputReplaceBecomesSkip(t *testing.T) {
	t.Parallel()
	ctx := streamContext(feature.StageIDCompletionGating, true)

	rec := extensions.ProjectCompletionOutcome(ctx, "p1", completion.ReplaceOutcome([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}))
	if rec.Outcome != policydecision.OutcomeSkip || rec.Effect != policydecision.EffectNone {
		t.Fatalf("post-output replace must project as skip/none: outcome=%q effect=%q", rec.Outcome, rec.Effect)
	}
	if rec.ReasonCode != "completion_ignored_post_output" {
		t.Fatalf("post-output reason mismatch: %q", rec.ReasonCode)
	}
	assertLegal(t, rec)
}

func TestProjectCompletionOutcome_UnknownKindProjectsAsMalformedError(t *testing.T) {
	t.Parallel()
	ctx := streamContext(feature.StageIDCompletionGating, false)

	// An unknown OutcomeKind is treated by the existing completion runner as malformed for
	// fail-closed and ignored for fail-open (completion_run.go default case). Evidence must
	// not fabricate pass for malformed output; it projects as OutcomeError/EffectNone with a
	// malformed-safe reason and category that is legal for StageIDCompletionGating.
	unknown := extensions.ProjectCompletionOutcome(ctx, "p1", completion.Outcome{Kind: completion.OutcomeKind(99)})
	if unknown.Outcome != policydecision.OutcomeError || unknown.Effect != policydecision.EffectNone {
		t.Fatalf("unknown outcome must project as error/none: outcome=%q effect=%q", unknown.Outcome, unknown.Effect)
	}
	if unknown.ReasonCode != "completion_malformed" {
		t.Fatalf("unknown outcome reason mismatch: %q", unknown.ReasonCode)
	}
	if unknown.ClientCategory != "policy_malformed" {
		t.Fatalf("unknown outcome category mismatch: %q", unknown.ClientCategory)
	}
	if !unknown.BackendAttempted {
		t.Fatalf("completion must record backend attempted (stream stage)")
	}
	assertLegal(t, unknown)
}

func TestProjectCompletionOutcome_ReplayUnaffectedByOutputCommitted(t *testing.T) {
	t.Parallel()
	ctx := streamContext(feature.StageIDCompletionGating, true)
	rec := extensions.ProjectCompletionOutcome(ctx, "p1", completion.ReplayOriginalOutcome())
	if rec.Outcome != policydecision.OutcomeAllow || rec.Effect != policydecision.EffectReplay {
		t.Fatalf("replay must remain allow/replay post-output: outcome=%q effect=%q", rec.Outcome, rec.Effect)
	}
	assertLegal(t, rec)
}

func TestProjectSubmitOutcome_RejectAnnotateFailureAndNoOpSkip(t *testing.T) {
	t.Parallel()
	ctx := preBackendContext(feature.StageIDSubmit)

	rec, ok := extensions.ProjectSubmitOutcome(ctx, "p1", true, nil, nil)
	if !ok {
		t.Fatalf("reject must be representable")
	}
	if rec.Outcome != policydecision.OutcomeDeny || rec.Effect != policydecision.EffectNone {
		t.Fatalf("reject mismatch: outcome=%q effect=%q", rec.Outcome, rec.Effect)
	}
	if rec.BackendAttempted {
		t.Fatalf("submit rejection must record no backend attempt")
	}
	if rec.ReasonCode != "submit_rejected" {
		t.Fatalf("reject reason mismatch: %q", rec.ReasonCode)
	}
	assertLegal(t, rec)

	ann, ok := extensions.ProjectSubmitOutcome(ctx, "p1", false, map[string]string{"a": "b"}, nil)
	if !ok {
		t.Fatalf("annotated submit must be representable")
	}
	if ann.Outcome != policydecision.OutcomeAllow || ann.Effect != policydecision.EffectAnnotate {
		t.Fatalf("annotate mismatch: outcome=%q effect=%q", ann.Outcome, ann.Effect)
	}
	if ann.Annotations["a"] != "b" {
		t.Fatalf("submit annotations not projected: %#v", ann.Annotations)
	}
	assertLegal(t, ann)

	fail, ok := extensions.ProjectSubmitOutcome(ctx, "p1", false, nil, errors.New("boom"))
	if !ok {
		t.Fatalf("submit failure must be representable")
	}
	if fail.Outcome != policydecision.OutcomeError || fail.Effect != policydecision.EffectNone {
		t.Fatalf("failure mismatch: outcome=%q effect=%q", fail.Outcome, fail.Effect)
	}
	assertLegal(t, fail)

	noop, ok := extensions.ProjectSubmitOutcome(ctx, "p1", false, nil, nil)
	if ok {
		t.Fatalf("no-op submit must not be representable, got %#v", noop)
	}
}

func TestProjectToolCatalogOutcome_MutationFailureAndNoOpSkip(t *testing.T) {
	t.Parallel()
	ctx := preBackendContext(feature.StageIDToolCatalog)

	mut, ok := extensions.ProjectToolCatalogOutcome(ctx, "p1", true, nil)
	if !ok {
		t.Fatalf("catalog mutation must be representable")
	}
	if mut.Outcome != policydecision.OutcomeAllow || mut.Effect != policydecision.EffectMutate {
		t.Fatalf("mutation mismatch: outcome=%q effect=%q", mut.Outcome, mut.Effect)
	}
	if mut.ReasonCode != "tool_catalog_mutation" {
		t.Fatalf("mutation reason mismatch: %q", mut.ReasonCode)
	}
	assertLegal(t, mut)

	fail, ok := extensions.ProjectToolCatalogOutcome(ctx, "p1", false, errors.New("boom"))
	if !ok {
		t.Fatalf("catalog failure must be representable")
	}
	if fail.Outcome != policydecision.OutcomeError || fail.Effect != policydecision.EffectNone {
		t.Fatalf("failure mismatch: outcome=%q effect=%q", fail.Outcome, fail.Effect)
	}
	assertLegal(t, fail)

	noop, ok := extensions.ProjectToolCatalogOutcome(ctx, "p1", false, nil)
	if ok {
		t.Fatalf("no-op catalog filter must not be representable, got %#v", noop)
	}
}

func TestProjectRouteHintOutcome_ChangedFailureAndNoOpSkip(t *testing.T) {
	t.Parallel()
	ctx := preBackendContext(feature.StageIDRouteHinting)

	changed, ok := extensions.ProjectRouteHintOutcome(ctx, "p1", true, nil)
	if !ok {
		t.Fatalf("changed route hint must be representable")
	}
	if changed.Outcome != policydecision.OutcomeAllow || changed.Effect != policydecision.EffectAnnotate {
		t.Fatalf("changed mismatch: outcome=%q effect=%q", changed.Outcome, changed.Effect)
	}
	if changed.ReasonCode != "route_hint_changed" {
		t.Fatalf("changed reason mismatch: %q", changed.ReasonCode)
	}
	assertLegal(t, changed)

	fail, ok := extensions.ProjectRouteHintOutcome(ctx, "p1", false, errors.New("boom"))
	if !ok {
		t.Fatalf("route hint failure must be representable")
	}
	if fail.Outcome != policydecision.OutcomeError || fail.Effect != policydecision.EffectNone {
		t.Fatalf("failure mismatch: outcome=%q effect=%q", fail.Outcome, fail.Effect)
	}
	assertLegal(t, fail)

	noop, ok := extensions.ProjectRouteHintOutcome(ctx, "p1", false, nil)
	if ok {
		t.Fatalf("no-op route hint must not be representable, got %#v", noop)
	}
}

func TestProjectAttemptObservation_FailureAndSuccessSkip(t *testing.T) {
	t.Parallel()
	ctx := streamContext(feature.StageIDAttemptLifecycle, false)

	fail, ok := extensions.ProjectAttemptObservation(ctx, "p1", errors.New("boom"))
	if !ok {
		t.Fatalf("attempt failure must be representable")
	}
	if fail.Outcome != policydecision.OutcomeError || fail.Effect != policydecision.EffectNone {
		t.Fatalf("failure mismatch: outcome=%q effect=%q", fail.Outcome, fail.Effect)
	}
	if !fail.BackendAttempted {
		t.Fatalf("attempt observation must record backend attempted")
	}
	if fail.ReasonCode != "attempt_failure" {
		t.Fatalf("failure reason mismatch: %q", fail.ReasonCode)
	}
	assertLegal(t, fail)

	noop, ok := extensions.ProjectAttemptObservation(ctx, "p1", nil)
	if ok {
		t.Fatalf("successful attempt observation must not be representable, got %#v", noop)
	}
}

func TestProjectHelpers_DistinguishBackendAttempted(t *testing.T) {
	t.Parallel()
	preCtx := preBackendContext(feature.StageIDPreRequest)
	streamCtx := streamContext(feature.StageIDToolEventReaction, false)

	if rec := extensions.ProjectPreRequestDecision(preCtx, "p1", prerequest.Allow()); rec.BackendAttempted {
		t.Fatalf("pre-request must not record backend attempted")
	}
	if rec := extensions.ProjectToolPolicyDecision(streamCtx, "p1", toolpolicy.DecisionAllow); !rec.BackendAttempted {
		t.Fatalf("tool policy must record backend attempted")
	}
}

func TestProjectHelpers_PreserveLineageFromContext(t *testing.T) {
	t.Parallel()
	ctx := preBackendContext(feature.StageIDPreRequest)
	rec := extensions.ProjectPreRequestDecision(ctx, "p1", prerequest.Allow())
	if rec.TraceID != "trace-1" {
		t.Fatalf("trace id not carried: %q", rec.TraceID)
	}
	if rec.ALegID != "a-leg-1" {
		t.Fatalf("a-leg id not carried: %q", rec.ALegID)
	}
	if rec.BLegID != "b-leg-2" {
		t.Fatalf("b-leg id not carried: %q", rec.BLegID)
	}
	if rec.AttemptSeq != 3 {
		t.Fatalf("attempt seq not carried: %d", rec.AttemptSeq)
	}
	if rec.Scope.PrincipalID.String() != "auth-principal" {
		t.Fatalf("scope not carried: %q", rec.Scope.PrincipalID.String())
	}
}

func TestProjectHelpers_AllEmittedRecordsAreLegal(t *testing.T) {
	t.Parallel()
	// Sweep every projection helper outcome and confirm legality so projections never
	// emit malformed evidence that the legality matrix would reject.
	preCtx := preBackendContext(feature.StageIDPreRequest)
	reqCtx := preBackendContext(feature.StageIDRequestWide)
	toolCtx := streamContext(feature.StageIDToolEventReaction, false)
	compCtx := streamContext(feature.StageIDCompletionGating, false)
	compCommittedCtx := streamContext(feature.StageIDCompletionGating, true)
	submitCtx := preBackendContext(feature.StageIDSubmit)
	catalogCtx := preBackendContext(feature.StageIDToolCatalog)
	routeCtx := preBackendContext(feature.StageIDRouteHinting)
	attemptCtx := streamContext(feature.StageIDAttemptLifecycle, false)

	records := []policydecision.Record{
		extensions.ProjectPreRequestDecision(preCtx, "p1", prerequest.Allow()),
		extensions.ProjectPreRequestDecision(preCtx, "p1", prerequest.Deny("no")),
		extensions.ProjectRequestTransformResult(reqCtx, "p1", false, nil),
		extensions.ProjectRequestTransformResult(reqCtx, "p1", true, nil),
		extensions.ProjectRequestTransformResult(reqCtx, "p1", false, errors.New("boom")),
		extensions.ProjectToolPolicyDecision(toolCtx, "p1", toolpolicy.DecisionAllow),
		extensions.ProjectToolPolicyDecision(toolCtx, "p1", toolpolicy.DecisionDeny),
		extensions.ProjectToolPolicyDecision(toolCtx, "p1", toolpolicy.DecisionUnspecified),
		extensions.ProjectToolPolicyDecision(toolCtx, "p1", toolpolicy.Decision(99)),
		extensions.ProjectToolReactorDecision(toolCtx, "p1", sdkhooks.ToolPass),
		extensions.ProjectToolReactorDecision(toolCtx, "p1", sdkhooks.ToolRewrite),
		extensions.ProjectToolReactorDecision(toolCtx, "p1", sdkhooks.ToolReplace),
		extensions.ProjectToolReactorDecision(toolCtx, "p1", sdkhooks.ToolSwallow),
		extensions.ProjectCompletionOutcome(compCtx, "p1", completion.PassOriginalOutcome()),
		extensions.ProjectCompletionOutcome(compCtx, "p1", completion.ReplaceOutcome([]aipapi.Event{{Kind: aipapi.EventResponseFinished}})),
		extensions.ProjectCompletionOutcome(compCtx, "p1", completion.ReplayOriginalOutcome()),
		extensions.ProjectCompletionOutcome(compCtx, "p1", completion.RejectOutcome(errors.New("nope"))),
		extensions.ProjectCompletionOutcome(compCtx, "p1", completion.Outcome{Kind: completion.OutcomeKind(99)}),
		extensions.ProjectCompletionOutcome(compCommittedCtx, "p1", completion.ReplaceOutcome([]aipapi.Event{{Kind: aipapi.EventResponseFinished}})),
	}
	for _, r := range records {
		assertLegal(t, r)
	}

	okRecords := []policydecision.Record{}
	if r, ok := extensions.ProjectSubmitOutcome(submitCtx, "p1", true, nil, nil); ok {
		okRecords = append(okRecords, r)
	} else {
		t.Fatal("submit reject expected representable")
	}
	if r, ok := extensions.ProjectSubmitOutcome(submitCtx, "p1", false, map[string]string{"a": "b"}, nil); ok {
		okRecords = append(okRecords, r)
	} else {
		t.Fatal("submit annotate expected representable")
	}
	if r, ok := extensions.ProjectSubmitOutcome(submitCtx, "p1", false, nil, errors.New("boom")); ok {
		okRecords = append(okRecords, r)
	} else {
		t.Fatal("submit failure expected representable")
	}
	if r, ok := extensions.ProjectToolCatalogOutcome(catalogCtx, "p1", true, nil); ok {
		okRecords = append(okRecords, r)
	} else {
		t.Fatal("catalog mutation expected representable")
	}
	if r, ok := extensions.ProjectToolCatalogOutcome(catalogCtx, "p1", false, errors.New("boom")); ok {
		okRecords = append(okRecords, r)
	} else {
		t.Fatal("catalog failure expected representable")
	}
	if r, ok := extensions.ProjectRouteHintOutcome(routeCtx, "p1", true, nil); ok {
		okRecords = append(okRecords, r)
	} else {
		t.Fatal("route hint changed expected representable")
	}
	if r, ok := extensions.ProjectRouteHintOutcome(routeCtx, "p1", false, errors.New("boom")); ok {
		okRecords = append(okRecords, r)
	} else {
		t.Fatal("route hint failure expected representable")
	}
	if r, ok := extensions.ProjectAttemptObservation(attemptCtx, "p1", errors.New("boom")); ok {
		okRecords = append(okRecords, r)
	} else {
		t.Fatal("attempt failure expected representable")
	}
	for _, r := range okRecords {
		assertLegal(t, r)
	}
}
