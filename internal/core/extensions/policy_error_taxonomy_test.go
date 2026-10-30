package extensions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

// These tests assert that stage denial/failure/malformed paths return stable
// aipapi policy errors (requirements 5.1, 5.6, 6.1, 6.5, 6.6) so the execerr
// classifier can reach KindPolicyDenied/KindPolicyFailure/KindPolicyMalformed
// for live runtime errors, not just for errors constructed directly via
// aipapi.NewPolicy*Error. They fail before the stage runners convert their
// legacy/plain errors to the stable policy error roots.

// ---- pre-request ----

func TestRunPreRequestStage_DenyReturnsPolicyDenied(t *testing.T) {
	t.Parallel()
	call := validCall()
	err := extensions.RunPreRequestStage(context.Background(), nil, nil, []prerequest.Handler{
		preReqHandler{id: "deny", order: 1, decision: prerequest.Deny("blocked")},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if err == nil {
		t.Fatal("expected denial error")
	}
	if !aipapi.IsPolicyDenied(err) {
		t.Fatalf("pre-request deny must be policy denied, got %v", err)
	}
	// Legacy RejectError must still be reachable so existing callers/tests keep working.
	if _, ok := errors.AsType[*prerequest.RejectError](err); !ok {
		t.Fatalf("pre-request deny must preserve *prerequest.RejectError cause, got %T", err)
	}
	if !prerequest.IsRejected(err) {
		t.Fatalf("pre-request deny must still satisfy prerequest.IsRejected, got %v", err)
	}
}

func TestRunPreRequestStage_FailClosedErrorIsPolicyFailure(t *testing.T) {
	t.Parallel()
	call := validCall()
	cause := errors.New("boom")
	err := extensions.RunPreRequestStage(context.Background(), nil, nil, []prerequest.Handler{
		preReqHandler{id: "bad", order: 1, err: cause, mode: sdkhooks.FailClosed},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if err == nil {
		t.Fatal("expected fail-closed error")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("pre-request fail-closed must be policy failure, got %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("pre-request fail-closed must preserve cause, got %v", err)
	}
}

func TestRunPreRequestStage_FailOpenErrorNotPolicyFailure(t *testing.T) {
	t.Parallel()
	call := validCall()
	err := extensions.RunPreRequestStage(context.Background(), nil, nil, []prerequest.Handler{
		preReqHandler{id: "bad", order: 1, err: errors.New("boom"), mode: sdkhooks.FailOpen},
		preReqHandler{id: "next", order: 2},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if err != nil {
		t.Fatalf("fail-open must not surface error, got %v", err)
	}
	if aipapi.IsPolicyFailure(err) {
		t.Fatalf("fail-open must not be policy failure")
	}
}

func TestRunPreRequestStage_MalformedValidationIsPolicyMalformed(t *testing.T) {
	t.Parallel()
	call := validCall()
	err := extensions.RunPreRequestStage(context.Background(), nil, nil, []prerequest.Handler{
		preReqHandler{id: "mutate-bad", order: 1, mutate: func(c *aipapi.Call) { c.Messages = nil }},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if err == nil {
		t.Fatal("expected malformed validation error")
	}
	if !aipapi.IsPolicyMalformed(err) {
		t.Fatalf("pre-request malformed validation must be policy malformed, got %v", err)
	}
}

// ---- tool policy ----

func TestRunToolPolicyStage_DenyReturnsPolicyDenied(t *testing.T) {
	t.Parallel()
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx: context.Background(),
		Policies: []toolpolicy.Policy{
			toolPolSeq{id: "deny", handleHook: func(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
				return toolpolicy.DecisionDeny, nil
			}},
		},
		Event: validToolEvent(),
	})
	if err == nil {
		t.Fatal("expected deny error")
	}
	if !aipapi.IsPolicyDenied(err) {
		t.Fatalf("tool policy deny must be policy denied, got %v", err)
	}
}

func TestRunToolPolicyStage_FailClosedErrorIsPolicyFailure(t *testing.T) {
	t.Parallel()
	cause := errors.New("boom")
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx: context.Background(),
		Policies: []toolpolicy.Policy{
			toolPolSeq{id: "bad", mode: sdkhooks.FailClosed, handleHook: func(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
				return toolpolicy.DecisionAllow, cause
			}},
		},
		Event: validToolEvent(),
	})
	if err == nil {
		t.Fatal("expected fail-closed error")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("tool policy fail-closed must be policy failure, got %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("tool policy fail-closed must preserve cause, got %v", err)
	}
}

func TestRunToolPolicyStage_UnknownDecisionIsPolicyMalformed(t *testing.T) {
	t.Parallel()
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx: context.Background(),
		Policies: []toolpolicy.Policy{
			unknownDecisionPol{},
		},
		Event: validToolEvent(),
	})
	if err == nil {
		t.Fatal("expected malformed error")
	}
	if !aipapi.IsPolicyMalformed(err) {
		t.Fatalf("tool policy unknown decision must be policy malformed, got %v", err)
	}
}

// ---- request transform ----

type rtxHandler struct {
	id   string
	mode sdkhooks.FailureMode
	err  error
	mut  func(*aipapi.Call)
}

func (h rtxHandler) ID() string                        { return h.id }
func (h rtxHandler) Order() int                        { return 0 }
func (h rtxHandler) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h rtxHandler) Handle(_ context.Context, call *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	if h.mut != nil {
		h.mut(call)
	}
	return h.err
}

func TestRunRequestTransformStage_FailClosedErrorIsPolicyFailure(t *testing.T) {
	t.Parallel()
	call := validCall()
	cause := errors.New("boom")
	err := extensions.RunRequestTransformStage(context.Background(), nil, nil, []request.Transform{
		rtxHandler{id: "bad", mode: sdkhooks.FailClosed, err: cause},
	}, &call, request.RequestMeta{}, request.Services{})
	if err == nil {
		t.Fatal("expected fail-closed error")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("request transform fail-closed must be policy failure, got %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("request transform fail-closed must preserve cause, got %v", err)
	}
}

func TestRunRequestTransformStage_MalformedValidationIsPolicyMalformed(t *testing.T) {
	t.Parallel()
	call := validCall()
	err := extensions.RunRequestTransformStage(context.Background(), nil, nil, []request.Transform{
		rtxHandler{id: "mutate-bad", mode: sdkhooks.FailClosed, mut: func(c *aipapi.Call) { c.Messages = nil }},
	}, &call, request.RequestMeta{}, request.Services{})
	if err == nil {
		t.Fatal("expected malformed validation error")
	}
	if !aipapi.IsPolicyMalformed(err) {
		t.Fatalf("request transform malformed validation must be policy malformed, got %v", err)
	}
}

// ---- tool catalog ----

type tcfHandler struct {
	id   string
	mode sdkhooks.FailureMode
	err  error
}

func (h tcfHandler) ID() string                        { return h.id }
func (h tcfHandler) Order() int                        { return 0 }
func (h tcfHandler) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h tcfHandler) Handle(_ context.Context, _ *aipapi.Call, _ toolcatalog.CatalogMeta, _ toolcatalog.Services) error {
	return h.err
}

func TestRunToolCatalogFilterStage_FailClosedErrorIsPolicyFailure(t *testing.T) {
	t.Parallel()
	call := validCall()
	cause := errors.New("boom")
	err := extensions.RunToolCatalogFilterStage(context.Background(), nil, nil, []toolcatalog.Filter{
		tcfHandler{id: "bad", mode: sdkhooks.FailClosed, err: cause},
	}, &call, toolcatalog.CatalogMeta{}, toolcatalog.Services{})
	if err == nil {
		t.Fatal("expected fail-closed error")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("tool catalog fail-closed must be policy failure, got %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("tool catalog fail-closed must preserve cause, got %v", err)
	}
}

// ---- route hint ----

type rhProvider struct {
	id   string
	mode sdkhooks.FailureMode
	err  error
}

func (p rhProvider) ID() string                        { return p.id }
func (p rhProvider) Order() int                        { return 0 }
func (p rhProvider) FailureMode() sdkhooks.FailureMode { return p.mode }
func (p rhProvider) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, p.err
}

func TestRunRouteHintStage_FailClosedErrorIsPolicyFailure(t *testing.T) {
	t.Parallel()
	call := validCall()
	cause := errors.New("boom")
	_, err := extensions.RunRouteHintStage(context.Background(), nil, []routehint.Provider{
		rhProvider{id: "bad", mode: sdkhooks.FailClosed, err: cause},
	}, &call, routehint.Input{})
	if err == nil {
		t.Fatal("expected fail-closed error")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("route hint fail-closed must be policy failure, got %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("route hint fail-closed must preserve cause, got %v", err)
	}
}

// ---- completion gate ----

type failClosedMalformedGate struct{}

func (failClosedMalformedGate) ID() string                        { return "fc-malformed" }
func (failClosedMalformedGate) Order() int                        { return 0 }
func (failClosedMalformedGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (failClosedMalformedGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.Outcome{Kind: completion.OutcomePassOriginal, Err: errors.New("illegal err on pass")}, nil
}

type failClosedHandlerErrGate struct{}

func (failClosedHandlerErrGate) ID() string                        { return "fc-handler-fail" }
func (failClosedHandlerErrGate) Order() int                        { return 0 }
func (failClosedHandlerErrGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (failClosedHandlerErrGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.Outcome{Kind: completion.OutcomeKind(0)}, errors.New("handler boom")
}

type rejectGate struct{}

func (rejectGate) ID() string                        { return "pd-reject" }
func (rejectGate) Order() int                        { return 0 }
func (rejectGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (rejectGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.RejectOutcome(errors.New("completion rejected by gate")), nil
}

func TestApplyCompletionGateChain_FailClosedMalformedIsPolicyMalformed(t *testing.T) {
	t.Parallel()
	orig := []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "a"}, {Kind: aipapi.EventResponseFinished}}
	_, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{failClosedMalformedGate{}},
		completion.Meta{}, orig, false, completion.Services{State: state.DisabledStore{}}, nil)
	if err == nil {
		t.Fatal("expected malformed fail-closed error")
	}
	if !aipapi.IsPolicyMalformed(err) {
		t.Fatalf("completion malformed fail-closed must be policy malformed, got %v", err)
	}
}

func TestApplyCompletionGateChain_FailClosedHandlerErrorIsPolicyFailure(t *testing.T) {
	t.Parallel()
	orig := []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "a"}, {Kind: aipapi.EventResponseFinished}}
	_, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{failClosedHandlerErrGate{}},
		completion.Meta{}, orig, false, completion.Services{State: state.DisabledStore{}}, nil)
	if err == nil {
		t.Fatal("expected fail-closed handler error")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("completion fail-closed handler error must be policy failure, got %v", err)
	}
}

func TestApplyCompletionGateChain_RejectIsPolicyDenied(t *testing.T) {
	t.Parallel()
	orig := []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "a"}, {Kind: aipapi.EventResponseFinished}}
	_, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{rejectGate{}},
		completion.Meta{}, orig, false, completion.Services{State: state.DisabledStore{}}, nil)
	if err == nil {
		t.Fatal("expected reject error")
	}
	if !aipapi.IsPolicyDenied(err) {
		t.Fatalf("completion reject must be policy denied, got %v", err)
	}
}
