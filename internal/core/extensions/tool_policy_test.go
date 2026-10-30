package extensions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

type toolPolSeq struct {
	id         string
	order      int
	mode       sdkhooks.FailureMode
	calls      *[]string
	handleHook func(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error)
}

func (p toolPolSeq) ID() string                        { return p.id }
func (p toolPolSeq) Order() int                        { return p.order }
func (p toolPolSeq) FailureMode() sdkhooks.FailureMode { return p.mode }
func (p toolPolSeq) Handle(ctx context.Context, event aipapi.ToolEvent, meta toolpolicy.Meta, svc toolpolicy.Services) (toolpolicy.Decision, error) {
	if p.calls != nil {
		*p.calls = append(*p.calls, p.id)
	}
	if p.handleHook != nil {
		return p.handleHook(ctx, event, meta, svc)
	}
	return toolpolicy.DecisionAllow, nil
}

type unknownDecisionPol struct{}

func (unknownDecisionPol) ID() string                        { return "bad-decision" }
func (unknownDecisionPol) Order() int                        { return 0 }
func (unknownDecisionPol) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (unknownDecisionPol) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.Decision(99), nil
}

func validToolEvent() aipapi.ToolEvent {
	return aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolCallID: "tc1", ToolName: "fn"}
}

type metricsSpy struct {
	failOpenStages []string
	lastOutcome    string
}

func (m *metricsSpy) ObserveStage(_, outcome string, _ float64) {
	m.lastOutcome = outcome
}

func (m *metricsSpy) IncFailOpenSkip(stage string) {
	m.failOpenStages = append(m.failOpenStages, stage)
}

func TestRunToolPolicyStage_nilContext(t *testing.T) {
	t.Parallel()
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Event: validToolEvent(),
	})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestRunToolPolicyStage_lowerOrderRunsBeforeHigherIDLexicographic(t *testing.T) {
	t.Parallel()
	var calls []string
	ev := validToolEvent()
	policies := []toolpolicy.Policy{
		toolPolSeq{id: "zzz", order: 10, calls: &calls},
		toolPolSeq{id: "aaa", order: 0, calls: &calls},
	}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      context.Background(),
		Policies: toolpolicy.MaterializeSorted(policies),
		Event:    ev,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(calls) != 2 || calls[0] != "aaa" || calls[1] != "zzz" {
		t.Fatalf("calls %#v", calls)
	}
}

func TestRunToolPolicyStage_sortedOrderStableIDsAndRegistrationTieBreak(t *testing.T) {
	t.Parallel()
	var calls []string
	ev := validToolEvent()
	policies := []toolpolicy.Policy{
		toolPolSeq{id: "gamma", order: 0, calls: &calls},
		toolPolSeq{id: "alpha", order: 0, calls: &calls},
		toolPolSeq{id: "beta", order: 0, calls: &calls},
		toolPolSeq{id: "tie-a", order: 1, calls: &calls},
		toolPolSeq{id: "tie-b", order: 1, calls: &calls},
	}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      context.Background(),
		Policies: toolpolicy.MaterializeSorted(policies),
		Event:    ev,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"alpha", "beta", "gamma", "tie-a", "tie-b"}
	if len(calls) != len(want) {
		t.Fatalf("calls %#v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("index %d want %q got %q full %#v", i, want[i], calls[i], calls)
		}
	}
}

func TestRunToolPolicyStage_decisionDenyReturnsErrorAndStopsChain(t *testing.T) {
	t.Parallel()
	var calls []string
	ev := validToolEvent()
	policies := []toolpolicy.Policy{
		toolPolSeq{id: "first-deny", order: 0, mode: sdkhooks.FailClosed, calls: &calls, handleHook: func(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
			return toolpolicy.DecisionDeny, nil
		}},
		toolPolSeq{id: "never-run", order: 1, mode: sdkhooks.FailClosed, calls: &calls},
	}
	ms := &metricsSpy{}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      context.Background(),
		Obs:      ms,
		Policies: toolpolicy.MaterializeSorted(policies),
		Event:    ev,
	})
	if err == nil {
		t.Fatal("want deny error")
	}
	if len(calls) != 1 || calls[0] != "first-deny" {
		t.Fatalf("calls %#v", calls)
	}
	if ms.lastOutcome != "denied" {
		t.Fatalf("outcome %q", ms.lastOutcome)
	}
}

func TestRunToolPolicyStage_failOpenHandlerErrorSkipsAndContinues(t *testing.T) {
	t.Parallel()
	var calls []string
	ev := validToolEvent()
	policies := []toolpolicy.Policy{
		toolPolSeq{id: "skip-me", order: 0, mode: sdkhooks.FailOpen, calls: &calls, handleHook: func(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
			return toolpolicy.DecisionAllow, errors.New("planned")
		}},
		toolPolSeq{id: "after-skip", order: 1, mode: sdkhooks.FailClosed, calls: &calls},
	}
	ms := &metricsSpy{}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      context.Background(),
		Obs:      ms,
		Policies: toolpolicy.MaterializeSorted(policies),
		Event:    ev,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(calls) != 2 || calls[0] != "skip-me" || calls[1] != "after-skip" {
		t.Fatalf("calls %#v", calls)
	}
	if len(ms.failOpenStages) != 1 || ms.failOpenStages[0] != extensions.StageToolEventReaction {
		t.Fatalf("fail-open skips %#v", ms.failOpenStages)
	}
}

func TestRunToolPolicyStage_failClosedHandlerErrorReturnsError(t *testing.T) {
	t.Parallel()
	var calls []string
	ev := validToolEvent()
	policies := []toolpolicy.Policy{
		toolPolSeq{id: "boom", order: 0, mode: sdkhooks.FailClosed, calls: &calls, handleHook: func(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
			return toolpolicy.DecisionAllow, errors.New("closed")
		}},
		toolPolSeq{id: "later", order: 1, mode: sdkhooks.FailClosed, calls: &calls},
	}
	ms := &metricsSpy{}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      context.Background(),
		Obs:      ms,
		Policies: toolpolicy.MaterializeSorted(policies),
		Event:    ev,
	})
	if err == nil {
		t.Fatal("want error")
	}
	if len(calls) != 1 || calls[0] != "boom" {
		t.Fatalf("calls %#v", calls)
	}
	if ms.lastOutcome != "error" {
		t.Fatalf("outcome %q", ms.lastOutcome)
	}
}

func TestRunToolPolicyStage_failOpenPanicSkipsAndContinues(t *testing.T) {
	t.Parallel()
	var calls []string
	ev := validToolEvent()
	policies := []toolpolicy.Policy{
		toolPolSeq{id: "panic-open", order: 0, mode: sdkhooks.FailOpen, calls: &calls, handleHook: func(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
			panic("policy panic open")
		}},
		toolPolSeq{id: "after-panic", order: 1, mode: sdkhooks.FailClosed, calls: &calls},
	}
	ms := &metricsSpy{}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      context.Background(),
		Obs:      ms,
		Policies: toolpolicy.MaterializeSorted(policies),
		Event:    ev,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(calls) != 2 || calls[1] != "after-panic" {
		t.Fatalf("calls %#v", calls)
	}
	if len(ms.failOpenStages) != 1 {
		t.Fatalf("fail-open skips %#v", ms.failOpenStages)
	}
}

func TestRunToolPolicyStage_failClosedPanicReturnsError(t *testing.T) {
	t.Parallel()
	var calls []string
	ev := validToolEvent()
	policies := []toolpolicy.Policy{
		toolPolSeq{id: "panic-closed", order: 0, mode: sdkhooks.FailClosed, calls: &calls, handleHook: func(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
			panic("policy panic closed")
		}},
		toolPolSeq{id: "later", order: 1, mode: sdkhooks.FailClosed, calls: &calls},
	}
	ms := &metricsSpy{}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      context.Background(),
		Obs:      ms,
		Policies: toolpolicy.MaterializeSorted(policies),
		Event:    ev,
	})
	if err == nil {
		t.Fatal("want error")
	}
	if len(calls) != 1 || calls[0] != "panic-closed" {
		t.Fatalf("calls %#v", calls)
	}
	if ms.lastOutcome != "error" {
		t.Fatalf("outcome %q", ms.lastOutcome)
	}
}

func TestRunToolPolicyStage_unknownDecisionReturnsError(t *testing.T) {
	t.Parallel()
	ev := validToolEvent()
	ms := &metricsSpy{}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      context.Background(),
		Obs:      ms,
		Policies: toolpolicy.MaterializeSorted([]toolpolicy.Policy{unknownDecisionPol{}}),
		Event:    ev,
	})
	if err == nil {
		t.Fatal("want error")
	}
	if ms.lastOutcome != "error" {
		t.Fatalf("outcome %q", ms.lastOutcome)
	}
}

func TestRunToolPolicyStage_invalidToolEventRejected(t *testing.T) {
	t.Parallel()
	ev := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolName: "fn"}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:   context.Background(),
		Event: ev,
	})
	if err == nil {
		t.Fatal("want validation error")
	}
}
