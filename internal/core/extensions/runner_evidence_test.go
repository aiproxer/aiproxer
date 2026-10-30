package extensions_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

// runnerEvidenceObserver captures policy decision records emitted by a runner.
type runnerEvidenceObserver struct {
	mu      sync.Mutex
	records []policydecision.Record
}

func (o *runnerEvidenceObserver) OnPolicyDecision(_ context.Context, r policydecision.Record) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records = append(o.records, r)
	return nil
}

func (o *runnerEvidenceObserver) snapshot() []policydecision.Record {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]policydecision.Record, len(o.records))
	copy(out, o.records)
	return out
}

func (o *runnerEvidenceObserver) findByProvider(id string) (policydecision.Record, bool) {
	for _, r := range o.snapshot() {
		if r.Provider.ID == id {
			return r, true
		}
	}
	return policydecision.Record{}, false
}

// withRunnerEvidence returns a context carrying a DecisionEvidence seam bound to obs
// and the sample views, so stage runners project and emit per-provider evidence.
func withRunnerEvidence(ctx context.Context, obs policydecision.Observer) context.Context {
	return extensions.WithDecisionEvidence(ctx, &extensions.DecisionEvidence{
		Emitter: extensions.NewEvidenceEmitter(obs, nil, false),
		Views:   sampleViews(),
	})
}

// TestRunRequestTransformStage_EmitsPerTransformEvidence asserts the runner
// projects one record per transform with the transform's provider id. It fails
// if projection is removed from the runner (no records / aggregate provider id).
func TestRunRequestTransformStage_EmitsPerTransformEvidence(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	call := validCall()
	err := extensions.RunRequestTransformStage(ctx, nil, nil, []request.Transform{
		rtxNoop{id: "rtx-noop"},
		rtxAppendNamed{id: "rtx-append"},
	}, &call, request.RequestMeta{}, request.Services{State: state.DisabledStore{}})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	recs := obs.snapshot()
	if len(recs) != 2 {
		t.Fatalf("expected one record per transform (2), got %d: %+v", len(recs), recs)
	}
	noop, ok := obs.findByProvider("rtx-noop")
	if !ok {
		t.Fatalf("missing rtx-noop record; got %+v", recs)
	}
	if noop.Outcome != policydecision.OutcomeAllow || noop.Effect != policydecision.EffectNone {
		t.Fatalf("rtx-noop: want allow/none, got %s/%s", noop.Outcome, noop.Effect)
	}
	if noop.Stage != feature.StageIDRequestWide {
		t.Fatalf("rtx-noop stage: got %q want %q", noop.Stage, feature.StageIDRequestWide)
	}
	if noop.BackendAttempted {
		t.Fatalf("rtx-noop must record no backend attempt (pre-backend stage)")
	}
	app, ok := obs.findByProvider("rtx-append")
	if !ok {
		t.Fatalf("missing rtx-append record; got %+v", recs)
	}
	if app.Outcome != policydecision.OutcomeAllow || app.Effect != policydecision.EffectMutate {
		t.Fatalf("rtx-append: want allow/mutate, got %s/%s", app.Outcome, app.Effect)
	}
}

// TestRunRequestTransformStage_FailureEmitsPerProviderErrorEvidence asserts a
// failing transform emits an error/none record carrying the failing transform's
// provider id and fail-closed failure behavior.
func TestRunRequestTransformStage_FailureEmitsPerProviderErrorEvidence(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	call := validCall()
	_ = extensions.RunRequestTransformStage(ctx, nil, nil, []request.Transform{
		rtxFailNamed{id: "rtx-boom"},
	}, &call, request.RequestMeta{}, request.Services{State: state.DisabledStore{}})
	rec, ok := obs.findByProvider("rtx-boom")
	if !ok {
		t.Fatalf("expected rtx-boom failure record, got %+v", obs.snapshot())
	}
	if rec.Outcome != policydecision.OutcomeError || rec.Effect != policydecision.EffectNone {
		t.Fatalf("rtx-boom: want error/none, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.FailureBehavior != policydecision.FailureBehaviorFailClosed {
		t.Fatalf("rtx-boom: want fail-closed behavior, got %q", rec.FailureBehavior)
	}
}

// TestRunRequestTransformStage_NoEvidenceWithoutSeam asserts the runner emits
// nothing when no DecisionEvidence seam is attached (non-interference default).
func TestRunRequestTransformStage_NoEvidenceWithoutSeam(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	call := validCall()
	_ = extensions.RunRequestTransformStage(context.Background(), nil, nil,
		[]request.Transform{rtxAppendNamed{id: "rtx-append"}}, &call,
		request.RequestMeta{}, request.Services{State: state.DisabledStore{}})
	if len(obs.snapshot()) != 0 {
		t.Fatalf("expected no evidence without seam, got %+v", obs.snapshot())
	}
}

// TestRunPreRequestStage_EmitsPerHandlerDenyEvidence asserts a denying handler
// emits a deny/none record with the handler's provider id and BackendAttempted=false.
func TestRunPreRequestStage_EmitsPerHandlerDenyEvidence(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	call := validCall()
	_ = extensions.RunPreRequestStage(ctx, nil, nil, []prerequest.Handler{
		preReqHandler{id: "pr-allow", order: 1, decision: prerequest.Allow()},
		preReqHandler{id: "pr-deny", order: 2, decision: prerequest.Deny("nope")},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	deny, ok := obs.findByProvider("pr-deny")
	if !ok {
		t.Fatalf("expected pr-deny record, got %+v", obs.snapshot())
	}
	if deny.Outcome != policydecision.OutcomeDeny || deny.Effect != policydecision.EffectNone {
		t.Fatalf("pr-deny: want deny/none, got %s/%s", deny.Outcome, deny.Effect)
	}
	if deny.BackendAttempted {
		t.Fatalf("pr-deny must record no backend attempt (pre-backend denial)")
	}
	if deny.ClientMessage != "nope" {
		t.Fatalf("pr-deny client message: got %q want nope", deny.ClientMessage)
	}
	allow, ok := obs.findByProvider("pr-allow")
	if !ok {
		t.Fatalf("expected pr-allow record, got %+v", obs.snapshot())
	}
	if allow.Outcome != policydecision.OutcomeAllow || allow.Effect != policydecision.EffectNone {
		t.Fatalf("pr-allow: want allow/none, got %s/%s", allow.Outcome, allow.Effect)
	}
}

// TestRunPreRequestStage_AuxiliaryDepthSuppressesEvidence asserts the runner
// emits no evidence (and runs no handlers) on an auxiliary-depth context.
func TestRunPreRequestStage_AuxiliaryDepthSuppressesEvidence(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(execctx.WithAuxiliaryDepth(context.Background(), 1), obs)
	call := validCall()
	_ = extensions.RunPreRequestStage(ctx, nil, nil, []prerequest.Handler{
		preReqHandler{id: "pr-deny", order: 1, decision: prerequest.Deny("nope")},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if len(obs.snapshot()) != 0 {
		t.Fatalf("auxiliary depth must suppress evidence, got %+v", obs.snapshot())
	}
}

// TestRunToolPolicyStage_EmitsPerPolicyEvidence asserts the runner projects one
// record per policy with the policy's provider id; deny records BackendAttempted=true.
func TestRunToolPolicyStage_EmitsPerPolicyEvidence(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx: ctx,
		Policies: []toolpolicy.Policy{
			toolPolSeq{id: "tp-allow"},
			toolPolSeq{id: "tp-deny", handleHook: func(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
				return toolpolicy.DecisionDeny, nil
			}},
		},
		Event: validToolEvent(),
	})
	if err == nil {
		t.Fatal("expected deny error")
	}
	allow, ok := obs.findByProvider("tp-allow")
	if !ok {
		t.Fatalf("expected tp-allow record, got %+v", obs.snapshot())
	}
	if allow.Outcome != policydecision.OutcomeAllow || allow.Effect != policydecision.EffectNone {
		t.Fatalf("tp-allow: want allow/none, got %s/%s", allow.Outcome, allow.Effect)
	}
	if !allow.BackendAttempted {
		t.Fatalf("tp-allow must record backend attempted (stream stage)")
	}
	deny, ok := obs.findByProvider("tp-deny")
	if !ok {
		t.Fatalf("expected tp-deny record, got %+v", obs.snapshot())
	}
	if deny.Outcome != policydecision.OutcomeDeny {
		t.Fatalf("tp-deny: want deny, got %s", deny.Outcome)
	}
	if deny.ReasonCode != extensions.ReasonToolPolicyDenied {
		t.Fatalf("tp-deny reason: got %q want %q", deny.ReasonCode, extensions.ReasonToolPolicyDenied)
	}
}

// TestRunToolPolicyStage_MalformedDecisionEmitsErrorEvidence asserts an unknown
// tool policy decision emits a malformed error record with the policy's provider id.
func TestRunToolPolicyStage_MalformedDecisionEmitsErrorEvidence(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	_ = extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      ctx,
		Policies: []toolpolicy.Policy{unknownDecisionPol{}},
		Event:    validToolEvent(),
	})
	rec, ok := obs.findByProvider("bad-decision")
	if !ok {
		t.Fatalf("expected bad-decision malformed record, got %+v", obs.snapshot())
	}
	if rec.Outcome != policydecision.OutcomeError {
		t.Fatalf("malformed: want error, got %s", rec.Outcome)
	}
	if rec.ReasonCode != extensions.ReasonToolPolicyMalformed {
		t.Fatalf("malformed reason: got %q want %q", rec.ReasonCode, extensions.ReasonToolPolicyMalformed)
	}
}

// TestApplyCompletionGateChain_EmitsPerGateEvidence asserts the runner projects
// one record per gate with the gate's provider id and OutputCommitted=true.
func TestApplyCompletionGateChain_EmitsPerGateEvidence(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	orig := []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "a"}, {Kind: aipapi.EventResponseFinished}}
	_, err := extensions.ApplyCompletionGateChain(ctx, []completion.Gate{
		runnerPassGate{},
		runnerRejectGate{},
	}, completion.Meta{}, orig, false, completion.Services{State: state.DisabledStore{}, Aux: auxiliary.DisabledClient{}}, nil)
	if err == nil {
		t.Fatal("expected reject error")
	}
	pass, ok := obs.findByProvider("pd-pass-gate")
	if !ok {
		t.Fatalf("expected pd-pass-gate record, got %+v", obs.snapshot())
	}
	if pass.Outcome != policydecision.OutcomeAllow || pass.Effect != policydecision.EffectNone {
		t.Fatalf("pd-pass-gate: want allow/none, got %s/%s", pass.Outcome, pass.Effect)
	}
	// outputCommitted arg is false here; the record must preserve it rather than
	// forcing true (the runtime passes the authoritative per-event flag).
	if pass.OutputCommitted {
		t.Fatalf("pd-pass-gate: want OutputCommitted=false (preserved arg), got true")
	}
	if !pass.BackendAttempted {
		t.Fatalf("pd-pass-gate must record backend attempted (stream stage)")
	}
	rej, ok := obs.findByProvider("pd-reject-gate")
	if !ok {
		t.Fatalf("expected pd-reject-gate record, got %+v", obs.snapshot())
	}
	if rej.Outcome != policydecision.OutcomeDeny {
		t.Fatalf("pd-reject-gate: want deny, got %s", rej.Outcome)
	}
	if rej.ReasonCode != extensions.ReasonCompletionReject {
		t.Fatalf("pd-reject-gate reason: got %q want %q", rej.ReasonCode, extensions.ReasonCompletionReject)
	}
}

// TestApplyCompletionGateChain_NoEvidenceWithoutSeam asserts completion gates emit
// no evidence when no DecisionEvidence seam is attached.
func TestApplyCompletionGateChain_NoEvidenceWithoutSeam(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	orig := []aipapi.Event{{Kind: aipapi.EventResponseFinished}}
	_, _ = extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{runnerPassGate{}},
		completion.Meta{}, orig, false, completion.Services{}, nil)
	if len(obs.snapshot()) != 0 {
		t.Fatalf("expected no evidence without seam, got %+v", obs.snapshot())
	}
}

// --- local handler stubs with explicit ids ---

type runnerPassGate struct{}

func (runnerPassGate) ID() string                        { return "pd-pass-gate" }
func (runnerPassGate) Order() int                        { return 0 }
func (runnerPassGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (runnerPassGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type runnerRejectGate struct{}

func (runnerRejectGate) ID() string                        { return "pd-reject-gate" }
func (runnerRejectGate) Order() int                        { return 0 }
func (runnerRejectGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (runnerRejectGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.RejectOutcome(errors.New("completion rejected by gate")), nil
}

type panicGate struct{}

func (panicGate) ID() string                        { return "pd-panic-gate" }
func (panicGate) Order() int                        { return 0 }
func (panicGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (panicGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	panic("completion gate panic")
}

type rtxNoop struct{ id string }

func (r rtxNoop) ID() string                      { return r.id }
func (rtxNoop) Order() int                        { return 0 }
func (rtxNoop) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (rtxNoop) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type rtxAppendNamed struct{ id string }

func (r rtxAppendNamed) ID() string                      { return r.id }
func (rtxAppendNamed) Order() int                        { return 0 }
func (rtxAppendNamed) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (rtxAppendNamed) Handle(_ context.Context, call *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	call.Messages[0].Parts[0].Text += "!"
	return nil
}

type rtxFailNamed struct{ id string }

func (r rtxFailNamed) ID() string                      { return r.id }
func (rtxFailNamed) Order() int                        { return 0 }
func (rtxFailNamed) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (rtxFailNamed) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return errors.New("boom")
}

// --- tool-catalog + route-hint evidence (tasks 3.5 / 3.6) ---

type runnerCatalogMutate struct{ id string }

func (r runnerCatalogMutate) ID() string                      { return r.id }
func (runnerCatalogMutate) Order() int                        { return 0 }
func (runnerCatalogMutate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (runnerCatalogMutate) Handle(_ context.Context, call *aipapi.Call, _ toolcatalog.CatalogMeta, _ toolcatalog.Services) error {
	call.Tools = append(call.Tools, aipapi.ToolDef{Name: "extra"})
	return nil
}

func TestRunToolCatalogFilterStage_EmitsPerFilterMutationEvidence(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
		Tools:    []aipapi.ToolDef{{Name: "orig"}},
	}
	if err := extensions.RunToolCatalogFilterStage(ctx, nil, nil,
		[]toolcatalog.Filter{runnerCatalogMutate{id: "cat-add"}},
		&call, toolcatalog.CatalogMeta{}, toolcatalog.Services{State: state.DisabledStore{}}); err != nil {
		t.Fatalf("runner: %v", err)
	}
	rec, ok := obs.findByProvider("cat-add")
	if !ok {
		t.Fatalf("expected cat-add mutation record, got %+v", obs.snapshot())
	}
	if rec.Outcome != policydecision.OutcomeAllow || rec.Effect != policydecision.EffectMutate {
		t.Fatalf("cat-add: want allow/mutate, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.Stage != feature.StageIDToolCatalog {
		t.Fatalf("cat-add stage: got %q want %q", rec.Stage, feature.StageIDToolCatalog)
	}
}

type runnerCatalogInvalid struct{ id string }

func (r runnerCatalogInvalid) ID() string                      { return r.id }
func (runnerCatalogInvalid) Order() int                        { return 0 }
func (runnerCatalogInvalid) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (runnerCatalogInvalid) Handle(_ context.Context, call *aipapi.Call, _ toolcatalog.CatalogMeta, _ toolcatalog.Services) error {
	call.Tools = append(call.Tools, aipapi.ToolDef{})
	return nil
}

func TestRunToolCatalogFilterStage_PostChainMalformedEmitsEvidence(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	call := validCall()
	err := extensions.RunToolCatalogFilterStage(ctx, nil, nil,
		[]toolcatalog.Filter{runnerCatalogInvalid{id: "cat-invalid"}},
		&call, toolcatalog.CatalogMeta{}, toolcatalog.Services{State: state.DisabledStore{}})
	if err == nil {
		t.Fatal("expected malformed policy error")
	}
	if !aipapi.IsPolicyMalformed(err) {
		t.Fatalf("tool catalog post-chain validation must be policy malformed, got %v", err)
	}
	rec, ok := obs.findByProvider("tool_catalog_chain")
	if !ok {
		t.Fatalf("expected tool_catalog_chain malformed evidence, got %+v", obs.snapshot())
	}
	if rec.Stage != feature.StageIDToolCatalog {
		t.Fatalf("stage: got %q want %q", rec.Stage, feature.StageIDToolCatalog)
	}
	if rec.Outcome != policydecision.OutcomeError || rec.Effect != policydecision.EffectNone {
		t.Fatalf("want error/none, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.ClientCategory != extensions.CategoryMalformed {
		t.Fatalf("category: got %q want %q", rec.ClientCategory, extensions.CategoryMalformed)
	}
}

type runnerRouteHint struct {
	id   string
	keys []string
}

func (r runnerRouteHint) ID() string                      { return r.id }
func (runnerRouteHint) Order() int                        { return 0 }
func (runnerRouteHint) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (r runnerRouteHint) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{PreferredCandidateKeys: r.keys}, nil
}

func TestRunRouteHintStage_EmitsPerProviderHintEvidence(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	call := validCall()
	if _, err := extensions.RunRouteHintStage(ctx, nil,
		[]routehint.Provider{runnerRouteHint{id: "rh-1", keys: []string{"openai:gpt-4"}}},
		&call, routehint.Input{}); err != nil {
		t.Fatalf("runner: %v", err)
	}
	rec, ok := obs.findByProvider("rh-1")
	if !ok {
		t.Fatalf("expected rh-1 hint record, got %+v", obs.snapshot())
	}
	if rec.Outcome != policydecision.OutcomeAllow || rec.Effect != policydecision.EffectAnnotate {
		t.Fatalf("rh-1: want allow/annotate, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.Stage != feature.StageIDRouteHinting {
		t.Fatalf("rh-1 stage: got %q want %q", rec.Stage, feature.StageIDRouteHinting)
	}
}

// TestRunRouteHintStage_NoEvidenceForEmptyAdvisory asserts a route hint provider
// returning no candidates and no error emits no record (no representable semantics).
func TestRunRouteHintStage_NoEvidenceForEmptyAdvisory(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(context.Background(), obs)
	call := validCall()
	if _, err := extensions.RunRouteHintStage(ctx, nil,
		[]routehint.Provider{runnerRouteHint{id: "rh-empty", keys: nil}},
		&call, routehint.Input{}); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if len(obs.snapshot()) != 0 {
		t.Fatalf("expected no evidence for empty advisory, got %+v", obs.snapshot())
	}
}
