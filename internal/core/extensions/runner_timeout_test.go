package extensions_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

// perProviderTimeoutBudget is a test TimeoutBudgetSource that returns a configured
// budget only for specific (stage, providerID) pairs and zero otherwise, so tests can
// enable timeout enforcement for one hung provider without affecting the rest of the
// chain. It is safe for concurrent reads.
type perProviderTimeoutBudget struct {
	mu      sync.Mutex
	budgets map[[2]string]time.Duration
}

func (s *perProviderTimeoutBudget) TimeoutFor(stage, providerID string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budgets[[2]string{stage, providerID}]
}

func withRunnerEvidenceAndBudget(ctx context.Context, obs policydecision.Observer, budget extensions.TimeoutBudgetSource) context.Context {
	return extensions.WithDecisionEvidence(ctx, &extensions.DecisionEvidence{
		Emitter:       extensions.NewEvidenceEmitter(obs, nil, false),
		Views:         sampleViews(),
		TimeoutBudget: budget,
	})
}

// hungPreReqHandler blocks until ctx is done and returns ctx.Err(), observing the
// timeout/cancellation contract. deadlineOk records whether the received ctx carried a
// deadline (non-zero timeout path sets one; zero-timeout legacy path does not).
type hungPreReqHandler struct {
	id         string
	mode       sdkhooks.FailureMode
	deadlineOk *bool
}

func (h hungPreReqHandler) ID() string                        { return h.id }
func (h hungPreReqHandler) Order() int                        { return 0 }
func (h hungPreReqHandler) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h hungPreReqHandler) Handle(ctx context.Context, _ *aipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	if h.deadlineOk != nil {
		_, *h.deadlineOk = ctx.Deadline()
	}
	<-ctx.Done()
	return prerequest.Decision{}, ctx.Err()
}

// hungRtx blocks until ctx is done and returns ctx.Err().
type hungRtx struct {
	id         string
	mode       sdkhooks.FailureMode
	deadlineOk *bool
}

func (h hungRtx) ID() string                        { return h.id }
func (h hungRtx) Order() int                        { return 0 }
func (h hungRtx) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h hungRtx) Handle(ctx context.Context, _ *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	if h.deadlineOk != nil {
		_, *h.deadlineOk = ctx.Deadline()
	}
	<-ctx.Done()
	return ctx.Err()
}

// hungToolPol blocks until ctx is done and returns ctx.Err().
type hungToolPol struct {
	id   string
	mode sdkhooks.FailureMode
}

func (h hungToolPol) ID() string                        { return h.id }
func (h hungToolPol) Order() int                        { return 0 }
func (h hungToolPol) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h hungToolPol) Handle(ctx context.Context, _ aipapi.ToolEvent, _ toolpolicy.Meta, _ toolpolicy.Services) (toolpolicy.Decision, error) {
	<-ctx.Done()
	return toolpolicy.DecisionUnspecified, ctx.Err()
}

// TestRunPreRequestStage_TimeoutBoundsHungProviderAndEmitsEvidence asserts a hung
// fail-closed pre-request handler is bounded by its evaluation budget and emits a
// timeout evidence record carrying the evaluation timeout and a non-zero deadline.
// It uses testing/synctest.Test (Go 1.25+) so the 30ms evaluation timeout advances on
// the bubble's fake clock instead of blocking on real wall-clock time. The hung
// handler's bounded goroutine still only completes once its childCtx.Done() fires, so
// synctest.Test's exit-tracking (wait for all bubble goroutines to finish) exercises
// the exact same "late/still-running provider" shutdown path as the real-time version.
func TestRunPreRequestStage_TimeoutBoundsHungProviderAndEmitsEvidence(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		obs := &runnerEvidenceObserver{}
		budget := &perProviderTimeoutBudget{budgets: map[[2]string]time.Duration{
			{feature.StageIDPreRequest, "pr-hung"}: 30 * time.Millisecond,
		}}
		ctx := withRunnerEvidenceAndBudget(t.Context(), obs, budget)
		call := validCall()
		start := time.Now()
		err := extensions.RunPreRequestStage(ctx, nil, nil, []prerequest.Handler{
			hungPreReqHandler{id: "pr-hung", mode: sdkhooks.FailClosed},
		}, &call, prerequest.Meta{}, prerequest.Services{})
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("expected timeout failure error")
		}
		if !aipapi.IsPolicyFailure(err) {
			t.Fatalf("timeout fail-closed must be policy failure, got %v", err)
		}
		if elapsed > 500*time.Millisecond {
			t.Fatalf("timeout must bound hung provider, elapsed %v", elapsed)
		}
		rec, ok := obs.findByProvider("pr-hung")
		if !ok {
			t.Fatalf("expected pr-hung timeout record, got %+v", obs.snapshot())
		}
		if rec.Outcome != policydecision.OutcomeError || rec.Effect != policydecision.EffectNone {
			t.Fatalf("timeout record: want error/none, got %s/%s", rec.Outcome, rec.Effect)
		}
		if rec.ReasonCode != extensions.PolicyReasonTimeout {
			t.Fatalf("timeout reason: got %q want %q", rec.ReasonCode, extensions.PolicyReasonTimeout)
		}
		if rec.FailureBehavior != policydecision.FailureBehaviorFailClosed {
			t.Fatalf("timeout failure behavior: got %q want fail-closed", rec.FailureBehavior)
		}
		if rec.EvaluationTimeout != 30*time.Millisecond {
			t.Fatalf("evaluation timeout carried: got %v want 30ms", rec.EvaluationTimeout)
		}
		if rec.EvaluationDeadline.IsZero() {
			t.Fatal("evaluation deadline must be populated when timeout is non-zero")
		}
	})
}

// TestRunRequestTransformStage_TimeoutBoundsHungProviderAndEmitsEvidence asserts a
// hung fail-closed request-transform provider is bounded and emits timeout evidence.
func TestRunRequestTransformStage_TimeoutBoundsHungProviderAndEmitsEvidence(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	budget := &perProviderTimeoutBudget{budgets: map[[2]string]time.Duration{
		{feature.StageIDRequestWide, "rtx-hung"}: 30 * time.Millisecond,
	}}
	ctx := withRunnerEvidenceAndBudget(t.Context(), obs, budget)
	call := validCall()
	start := time.Now()
	err := extensions.RunRequestTransformStage(ctx, nil, nil, []request.Transform{
		hungRtx{id: "rtx-hung", mode: sdkhooks.FailClosed},
	}, &call, request.RequestMeta{}, request.Services{State: state.DisabledStore{}})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout failure error")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("timeout fail-closed must be policy failure, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("timeout must bound hung provider, elapsed %v", elapsed)
	}
	rec, ok := obs.findByProvider("rtx-hung")
	if !ok {
		t.Fatalf("expected rtx-hung timeout record, got %+v", obs.snapshot())
	}
	if rec.ReasonCode != extensions.PolicyReasonTimeout {
		t.Fatalf("timeout reason: got %q want %q", rec.ReasonCode, extensions.PolicyReasonTimeout)
	}
	if rec.EvaluationTimeout != 30*time.Millisecond {
		t.Fatalf("evaluation timeout carried: got %v want 30ms", rec.EvaluationTimeout)
	}
	if rec.EvaluationDeadline.IsZero() {
		t.Fatal("evaluation deadline must be populated when timeout is non-zero")
	}
}

// TestRunPreRequestStage_TimeoutFailOpenContinuesAndEmitsSkipEvidence asserts a hung
// fail-open pre-request handler is bounded, skipped, and emits timeout evidence while
// the chain continues without changing outcomes.
func TestRunPreRequestStage_TimeoutFailOpenContinuesAndEmitsSkipEvidence(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	budget := &perProviderTimeoutBudget{budgets: map[[2]string]time.Duration{
		{feature.StageIDPreRequest, "pr-hung-open"}: 30 * time.Millisecond,
	}}
	ctx := withRunnerEvidenceAndBudget(t.Context(), obs, budget)
	call := validCall()
	var seen []string
	err := extensions.RunPreRequestStage(ctx, nil, nil, []prerequest.Handler{
		preReqHandler{id: "before", order: 1, seen: &seen},
		hungPreReqHandler{id: "pr-hung-open", mode: sdkhooks.FailOpen},
		preReqHandler{id: "after", order: 3, seen: &seen},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if err != nil {
		t.Fatalf("fail-open timeout must not fail the chain: %v", err)
	}
	if len(seen) != 2 || seen[0] != "before" || seen[1] != "after" {
		t.Fatalf("chain must continue past hung fail-open provider, seen %#v", seen)
	}
	rec, ok := obs.findByProvider("pr-hung-open")
	if !ok {
		t.Fatalf("expected pr-hung-open timeout record, got %+v", obs.snapshot())
	}
	if rec.FailureBehavior != policydecision.FailureBehaviorFailOpen {
		t.Fatalf("timeout failure behavior: got %q want fail-open", rec.FailureBehavior)
	}
	if rec.EvaluationDeadline.IsZero() {
		t.Fatal("evaluation deadline must be populated for fail-open timeout")
	}
}

// TestRunPreRequestStage_ParentCancelNotClassifiedAsTimeout asserts that when the
// parent request context is canceled while a hung provider is blocked, the runner
// returns the parent cancellation error and does NOT emit a policy timeout record.
func TestRunPreRequestStage_ParentCancelNotClassifiedAsTimeout(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	budget := &perProviderTimeoutBudget{budgets: map[[2]string]time.Duration{
		{feature.StageIDPreRequest, "pr-hung"}: 30 * time.Second,
	}}
	parent, cancel := context.WithCancel(t.Context())
	ctx := withRunnerEvidenceAndBudget(parent, obs, budget)
	call := validCall()
	// Cancel before running so the provider observes an already-canceled ctx.
	cancel()
	err := extensions.RunPreRequestStage(ctx, nil, nil, []prerequest.Handler{
		hungPreReqHandler{id: "pr-hung", mode: sdkhooks.FailClosed},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation must be returned as cancellation, got %v", err)
	}
	if aipapi.IsPolicyFailure(err) {
		t.Fatalf("parent cancellation must not be converted to policy failure, got %v", err)
	}
	for _, r := range obs.snapshot() {
		if r.ReasonCode == extensions.PolicyReasonTimeout {
			t.Fatalf("parent cancellation must not emit timeout evidence, got %+v", r)
		}
	}
}

// TestRunPreRequestStage_ZeroTimeoutPreservesSynchronousBehavior asserts that with no
// budget configured, the provider runs synchronously against the runner ctx (no
// deadline child) and evidence carries a zero evaluation deadline (legacy default).
func TestRunPreRequestStage_ZeroTimeoutPreservesSynchronousBehavior(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	budget := &perProviderTimeoutBudget{budgets: map[[2]string]time.Duration{}}
	ctx := withRunnerEvidenceAndBudget(t.Context(), obs, budget)
	call := validCall()
	var deadlineOk bool
	if err := extensions.RunPreRequestStage(ctx, nil, nil, []prerequest.Handler{
		hungPreReqHandlerWithDone{id: "pr-sync", mode: sdkhooks.FailClosed, deadlineOk: &deadlineOk, fast: true},
	}, &call, prerequest.Meta{}, prerequest.Services{}); err != nil {
		t.Fatalf("zero timeout synchronous provider must not error: %v", err)
	}
	if deadlineOk {
		t.Fatal("zero timeout must not attach a deadline to the provider ctx (legacy synchronous path)")
	}
	rec, ok := obs.findByProvider("pr-sync")
	if !ok {
		t.Fatalf("expected pr-sync record, got %+v", obs.snapshot())
	}
	if !rec.EvaluationDeadline.IsZero() {
		t.Fatalf("zero timeout must keep zero evaluation deadline, got %v", rec.EvaluationDeadline)
	}
	if rec.EvaluationTimeout != 0 {
		t.Fatalf("zero timeout must keep zero evaluation timeout, got %v", rec.EvaluationTimeout)
	}
}

// hungPreReqHandlerWithDone is a variant that can return immediately (fast) after
// observing ctx.Deadline(), used to assert the synchronous (zero-timeout) path does
// not attach a deadline.
type hungPreReqHandlerWithDone struct {
	id         string
	mode       sdkhooks.FailureMode
	deadlineOk *bool
	fast       bool
}

func (h hungPreReqHandlerWithDone) ID() string                        { return h.id }
func (h hungPreReqHandlerWithDone) Order() int                        { return 0 }
func (h hungPreReqHandlerWithDone) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h hungPreReqHandlerWithDone) Handle(ctx context.Context, _ *aipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	_, *h.deadlineOk = ctx.Deadline()
	if h.fast {
		return prerequest.Allow(), nil
	}
	<-ctx.Done()
	return prerequest.Decision{}, ctx.Err()
}

// TestRunToolPolicyStage_TimeoutBoundsHungPolicyAndEmitsEvidence asserts a hung
// fail-closed tool policy is bounded and emits a stream-stage timeout record with
// BackendAttempted=true and a non-zero evaluation deadline.
func TestRunToolPolicyStage_TimeoutBoundsHungPolicyAndEmitsEvidence(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	budget := &perProviderTimeoutBudget{budgets: map[[2]string]time.Duration{
		{feature.StageIDToolEventReaction, "tp-hung"}: 30 * time.Millisecond,
	}}
	ctx := withRunnerEvidenceAndBudget(t.Context(), obs, budget)
	start := time.Now()
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx: ctx,
		Policies: []toolpolicy.Policy{
			hungToolPol{id: "tp-hung", mode: sdkhooks.FailClosed},
		},
		Event: validToolEvent(),
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout failure error")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("timeout fail-closed must be policy failure, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("timeout must bound hung policy, elapsed %v", elapsed)
	}
	rec, ok := obs.findByProvider("tp-hung")
	if !ok {
		t.Fatalf("expected tp-hung timeout record, got %+v", obs.snapshot())
	}
	if rec.ReasonCode != extensions.PolicyReasonTimeout {
		t.Fatalf("timeout reason: got %q want %q", rec.ReasonCode, extensions.PolicyReasonTimeout)
	}
	if !rec.BackendAttempted {
		t.Fatalf("tool policy timeout must record backend attempted (stream stage)")
	}
	if rec.EvaluationDeadline.IsZero() {
		t.Fatal("evaluation deadline must be populated when timeout is non-zero")
	}
}

// deadlineCapturePreReq records the deadline carried by the provider ctx so a test
// can assert the emitted record EvaluationDeadline equals the exact time.Time used
// to bound the provider (requirement 6.3 single-deadline-source fix).
type deadlineCapturePreReq struct {
	id       string
	mode     sdkhooks.FailureMode
	deadline *time.Time
	ok       *bool
	done     chan struct{}
}

func (h deadlineCapturePreReq) ID() string                        { return h.id }
func (h deadlineCapturePreReq) Order() int                        { return 0 }
func (h deadlineCapturePreReq) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h deadlineCapturePreReq) Handle(ctx context.Context, _ *aipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	defer func() {
		if h.done != nil {
			close(h.done)
		}
	}()
	d, ok := ctx.Deadline()
	if h.ok != nil {
		*h.ok = ok
	}
	if h.deadline != nil && ok {
		*h.deadline = d
	}
	<-ctx.Done()
	return prerequest.Decision{}, ctx.Err()
}

// TestRunPreRequestStage_TimeoutEvidenceDeadlineEqualsProviderCtxDeadline asserts the
// EvaluationDeadline projected onto the emitted record is exactly the same time.Time
// the provider observed as its ctx deadline (the one used in context.WithDeadline).
func TestRunPreRequestStage_TimeoutEvidenceDeadlineEqualsProviderCtxDeadline(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	budget := &perProviderTimeoutBudget{budgets: map[[2]string]time.Duration{
		{feature.StageIDPreRequest, "pr-deadline"}: 100 * time.Millisecond,
	}}
	var captured time.Time
	var saw bool
	ctx := withRunnerEvidenceAndBudget(t.Context(), obs, budget)
	call := validCall()
	done := make(chan struct{})
	_ = extensions.RunPreRequestStage(ctx, nil, nil, []prerequest.Handler{
		deadlineCapturePreReq{id: "pr-deadline", mode: sdkhooks.FailClosed, deadline: &captured, ok: &saw, done: done},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	<-done
	if !saw {
		t.Fatal("provider ctx must carry a deadline on the timeout path")
	}
	rec, ok := obs.findByProvider("pr-deadline")
	if !ok {
		t.Fatalf("expected pr-deadline timeout record, got %+v", obs.snapshot())
	}
	if rec.ReasonCode != extensions.PolicyReasonTimeout {
		t.Fatalf("timeout reason: got %q want %q", rec.ReasonCode, extensions.PolicyReasonTimeout)
	}
	if !rec.EvaluationDeadline.Equal(captured) {
		t.Fatalf("EvaluationDeadline %v must equal provider ctx deadline %v exactly",
			rec.EvaluationDeadline, captured)
	}
}

// deadlineResultCapturePreReq records the deadline carried by the provider ctx so a
// test can assert the emitted timeout record EvaluationDeadline equals the deadline
// the runner derived for the bounded call (requirement 6.3, single deadline source).
type deadlineResultCapturePreReq struct {
	id       string
	mode     sdkhooks.FailureMode
	deadline *time.Time
	done     chan struct{}
}

func (h deadlineResultCapturePreReq) ID() string                        { return h.id }
func (h deadlineResultCapturePreReq) Order() int                        { return 0 }
func (h deadlineResultCapturePreReq) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h deadlineResultCapturePreReq) Handle(ctx context.Context, _ *aipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	defer func() {
		if h.done != nil {
			close(h.done)
		}
	}()
	if d, ok := ctx.Deadline(); ok && h.deadline != nil {
		*h.deadline = d
	}
	<-ctx.Done()
	return prerequest.Decision{}, ctx.Err()
}

// TestRunPreRequestStage_TimeoutEvidenceDeadlineMatchesBoundedDeadline asserts the
// EvaluationDeadline projected onto the emitted timeout record equals the exact
// deadline the runner used to bound the provider call, threaded explicitly through the
// bounded result rather than via context values.
func TestRunPreRequestStage_TimeoutEvidenceDeadlineMatchesBoundedDeadline(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	budget := &perProviderTimeoutBudget{budgets: map[[2]string]time.Duration{
		{feature.StageIDPreRequest, "pr-result-deadline"}: 100 * time.Millisecond,
	}}
	var captured time.Time
	ctx := withRunnerEvidenceAndBudget(t.Context(), obs, budget)
	call := validCall()
	done := make(chan struct{})
	_ = extensions.RunPreRequestStage(ctx, nil, nil, []prerequest.Handler{
		deadlineResultCapturePreReq{id: "pr-result-deadline", mode: sdkhooks.FailClosed, deadline: &captured, done: done},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	<-done
	if captured.IsZero() {
		t.Fatal("provider ctx must carry a deadline on the timeout path")
	}
	rec, ok := obs.findByProvider("pr-result-deadline")
	if !ok {
		t.Fatalf("expected pr-result-deadline timeout record, got %+v", obs.snapshot())
	}
	if rec.ReasonCode != extensions.PolicyReasonTimeout {
		t.Fatalf("timeout reason: got %q want %q", rec.ReasonCode, extensions.PolicyReasonTimeout)
	}
	if !rec.EvaluationDeadline.Equal(captured) {
		t.Fatalf("EvaluationDeadline %v must equal bounded provider deadline %v exactly",
			rec.EvaluationDeadline, captured)
	}
}

// deadlineCaptureRtx is the request-transform variant of deadlineCapturePreReq.
type deadlineCaptureRtx struct {
	id       string
	mode     sdkhooks.FailureMode
	deadline *time.Time
	ok       *bool
	done     chan struct{}
}

func (h deadlineCaptureRtx) ID() string                        { return h.id }
func (h deadlineCaptureRtx) Order() int                        { return 0 }
func (h deadlineCaptureRtx) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h deadlineCaptureRtx) Handle(ctx context.Context, _ *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	defer func() {
		if h.done != nil {
			close(h.done)
		}
	}()
	d, ok := ctx.Deadline()
	if h.ok != nil {
		*h.ok = ok
	}
	if h.deadline != nil && ok {
		*h.deadline = d
	}
	<-ctx.Done()
	return ctx.Err()
}

// TestRunRequestTransformStage_TimeoutEvidenceDeadlineEqualsProviderCtxDeadline asserts
// the same single-deadline-source invariant for the request-transform mutable stage.
func TestRunRequestTransformStage_TimeoutEvidenceDeadlineEqualsProviderCtxDeadline(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	budget := &perProviderTimeoutBudget{budgets: map[[2]string]time.Duration{
		{feature.StageIDRequestWide, "rtx-deadline"}: 100 * time.Millisecond,
	}}
	var captured time.Time
	var saw bool
	ctx := withRunnerEvidenceAndBudget(t.Context(), obs, budget)
	call := validCall()
	done := make(chan struct{})
	_ = extensions.RunRequestTransformStage(ctx, nil, nil, []request.Transform{
		deadlineCaptureRtx{id: "rtx-deadline", mode: sdkhooks.FailClosed, deadline: &captured, ok: &saw, done: done},
	}, &call, request.RequestMeta{}, request.Services{State: state.DisabledStore{}})
	<-done
	if !saw {
		t.Fatal("provider ctx must carry a deadline on the timeout path")
	}
	rec, ok := obs.findByProvider("rtx-deadline")
	if !ok {
		t.Fatalf("expected rtx-deadline timeout record, got %+v", obs.snapshot())
	}
	if !rec.EvaluationDeadline.Equal(captured) {
		t.Fatalf("EvaluationDeadline %v must equal provider ctx deadline %v exactly",
			rec.EvaluationDeadline, captured)
	}
}

// TestRunPreRequestStage_NoObserverTimeoutStillEnforces asserts that a nil emitter
// (no-op observer / silent evidence) still enforces a non-default timeout budget.
// The default zero-timeout/no-observer path remains cheap and silent because the
// zero-budget source keeps runners on the legacy synchronous path.
func TestRunPreRequestStage_NoObserverTimeoutStillEnforces(t *testing.T) {
	t.Parallel()
	budget := &perProviderTimeoutBudget{budgets: map[[2]string]time.Duration{
		{feature.StageIDPreRequest, "pr-hung-silent"}: 30 * time.Millisecond,
	}}
	ctx := extensions.WithDecisionEvidence(t.Context(), &extensions.DecisionEvidence{
		Emitter:       nil,
		Views:         sampleViews(),
		TimeoutBudget: budget,
	})
	call := validCall()
	start := time.Now()
	err := extensions.RunPreRequestStage(ctx, nil, nil, []prerequest.Handler{
		hungPreReqHandler{id: "pr-hung-silent", mode: sdkhooks.FailClosed},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout failure with nil emitter")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("want policy failure, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("timeout must bound hung provider even with nil emitter, elapsed %v", elapsed)
	}
}

// lateMutateRtx blocks until ctx is done (timeout), then waits for an unblock signal
// before mutating the call it was handed. Used to prove a provider that mutates after
// timeout touches only the clone, never the live call. The unblock channel is closed
// by the test to release the goroutine and avoid leaks.
type lateMutateRtx struct {
	id      string
	mode    sdkhooks.FailureMode
	unblock chan struct{}
	mutated *bool
	done    chan struct{}
}

func (h lateMutateRtx) ID() string                        { return h.id }
func (h lateMutateRtx) Order() int                        { return 0 }
func (h lateMutateRtx) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h lateMutateRtx) Handle(ctx context.Context, call *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	defer func() {
		if h.done != nil {
			close(h.done)
		}
	}()
	<-ctx.Done()
	<-h.unblock
	if len(call.Messages) > 0 && len(call.Messages[0].Parts) > 0 {
		if h.mutated != nil {
			*h.mutated = true
		}
		call.Messages[0].Parts[0].Text = "late-mutation-after-timeout"
	}
	return ctx.Err()
}

// TestRunRequestTransformStage_TimeoutDoesNotMutateLiveCallAfterTimeout asserts a
// request-transform provider that mutates after the evaluation deadline does not
// mutate the live call. The provider goroutine is unblocked before the test ends to
// avoid goroutine leaks (requirement 6.5).
func TestRunRequestTransformStage_TimeoutDoesNotMutateLiveCallAfterTimeout(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	budget := &perProviderTimeoutBudget{budgets: map[[2]string]time.Duration{
		{feature.StageIDRequestWide, "rtx-late-mut"}: 30 * time.Millisecond,
	}}
	ctx := withRunnerEvidenceAndBudget(t.Context(), obs, budget)
	call := validCall()
	original := aipapi.CloneCall(call)
	unblock := make(chan struct{})
	done := make(chan struct{})
	var providerMutatedClone bool
	err := extensions.RunRequestTransformStage(ctx, nil, nil, []request.Transform{
		lateMutateRtx{id: "rtx-late-mut", mode: sdkhooks.FailClosed, unblock: unblock, mutated: &providerMutatedClone, done: done},
	}, &call, request.RequestMeta{}, request.Services{State: state.DisabledStore{}})
	if err == nil {
		t.Fatal("expected timeout failure")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("want policy failure, got %v", err)
	}
	if !reflect.DeepEqual(original, call) {
		t.Fatalf("live call must not be mutated after timeout, got %+v want %+v", call, original)
	}
	// Release the still-running provider goroutine so it can mutate the clone and exit.
	// Waiting on done (closed when Handle returns) gives a happens-before edge for the
	// mutated read and avoids racing the bounded goroutine.
	close(unblock)
	<-done
	if !providerMutatedClone {
		t.Fatalf("provider goroutine never observed unblock; potential leak")
	}
	if !reflect.DeepEqual(original, call) {
		t.Fatalf("live call must not be mutated by late clone mutation, got %+v want %+v", call, original)
	}
}

// deadlineErrCapturePreReq records ctx.Err() after the provider ctx is done so a test
// can assert the bounded provider observes context.DeadlineExceeded (the evaluation
// deadline) rather than context.Canceled when the parent request is still active
// (requirement 6.4, design §Timeout Contract).
type deadlineErrCapturePreReq struct {
	id     string
	mode   sdkhooks.FailureMode
	ctxErr *error
	done   chan struct{}
}

func (h deadlineErrCapturePreReq) ID() string                        { return h.id }
func (h deadlineErrCapturePreReq) Order() int                        { return 0 }
func (h deadlineErrCapturePreReq) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h deadlineErrCapturePreReq) Handle(ctx context.Context, _ *aipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	defer func() {
		if h.done != nil {
			close(h.done)
		}
	}()
	<-ctx.Done()
	if h.ctxErr != nil {
		*h.ctxErr = ctx.Err()
	}
	return prerequest.Decision{}, ctx.Err()
}

// TestRunPreRequestStage_TimeoutProviderCtxReportsDeadlineExceeded asserts that when the
// evaluation deadline elapses while the parent request context is still active, the
// bounded provider's context reports context.DeadlineExceeded (not context.Canceled).
// This distinguishes "evaluation deadline expired" from "parent canceled" at the
// provider-context level (requirement 6.4, design §Timeout Contract) and is distinct
// from TestRunPreRequestStage_ParentCancelNotClassifiedAsTimeout, which pre-cancels the
// parent.
func TestRunPreRequestStage_TimeoutProviderCtxReportsDeadlineExceeded(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	budget := &perProviderTimeoutBudget{budgets: map[[2]string]time.Duration{
		{feature.StageIDPreRequest, "pr-deadline-err"}: 30 * time.Millisecond,
	}}
	ctx := withRunnerEvidenceAndBudget(t.Context(), obs, budget)
	call := validCall()
	var captured error
	done := make(chan struct{})
	err := extensions.RunPreRequestStage(ctx, nil, nil, []prerequest.Handler{
		deadlineErrCapturePreReq{id: "pr-deadline-err", mode: sdkhooks.FailClosed, ctxErr: &captured, done: done},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if err == nil {
		t.Fatal("expected timeout failure error")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("timeout fail-closed must be policy failure, got %v", err)
	}
	// Waiting on done (closed when Handle returns) gives a happens-before edge for the
	// captured ctx.Err() read and avoids racing the bounded goroutine.
	<-done
	if captured == nil {
		t.Fatal("provider goroutine never observed ctx.Done(); potential leak")
	}
	if !errors.Is(captured, context.DeadlineExceeded) {
		t.Fatalf("bounded provider ctx.Err() = %v, want context.DeadlineExceeded", captured)
	}
	if errors.Is(captured, context.Canceled) {
		t.Fatalf("bounded provider ctx must not report Canceled when parent is active, got %v", captured)
	}
}

// TestRunPreRequestStage_FailOpenCancellationIsPreservedNotSwallowed asserts that on
// the direct (zero-timeout) execution path, a fail-open handler returning a
// cancellation-derived error does NOT have its error swallowed as a fail-open
// continue: parent cancellation is preserved (requirement 6.4) and no
// provider-failure evidence is emitted. This covers the regression where
// handleProviderFailure checked FailOpen before IsContextCancellation.
func TestRunPreRequestStage_FailOpenCancellationIsPreservedNotSwallowed(t *testing.T) {
	t.Parallel()
	obs := &runnerEvidenceObserver{}
	budget := &perProviderTimeoutBudget{budgets: map[[2]string]time.Duration{}}
	parent, cancel := context.WithCancel(t.Context())
	cancel()
	ctx := withRunnerEvidenceAndBudget(parent, obs, budget)
	call := validCall()
	err := extensions.RunPreRequestStage(ctx, nil, nil, []prerequest.Handler{
		preReqHandler{id: "pr-cancel", order: 1, err: context.Canceled, mode: sdkhooks.FailOpen},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("fail-open must not swallow parent cancellation, got %v", err)
	}
	if aipapi.IsPolicyFailure(err) {
		t.Fatalf("cancellation must not be converted to policy failure, got %v", err)
	}
	if rec, ok := obs.findByProvider("pr-cancel"); ok {
		t.Fatalf("cancellation must not emit provider-failure evidence, got %+v", rec)
	}
}
