package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// Spec: tool-name-correction Task 2.1 (requirements 1.3, 2.1) and design.md
// "SteerOutcome" / "SteerGate and CorrectionRails". The assembler honors the
// additive ActionSteer outcome through a synchronous gate supplied with ingest:
// a usable steer is offered to the gate, accepted and declined steers both
// continue the finalizer chain exactly as an ActionPass does, only acceptance
// marks the attempt as withholding, and every unusable steer keeps the
// pre-existing unusable-result fallback.
//
// Task 2.2 supplies the production gate (rails plus the attempt mark) at the
// prepareRecvEvent call site; these tests drive the assembler directly.

const steerToolName = "offered_tool"

func steerCatalog() []aipapi.ToolDef {
	return []aipapi.ToolDef{{Name: steerToolName, Parameters: []byte(`{"type":"object"}`)}}
}

// steerFin returns one fixed Result per completed call and counts invocations.
type steerFin struct {
	id     string
	order  int
	result toolcall.Result
	calls  int
}

func (f *steerFin) ID() string { return f.id }

func (f *steerFin) Order() int { return f.order }

func (f *steerFin) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	f.calls++
	return f.result, nil
}

// steerDeclarerFin publishes an applicable mandatory completeness requirement
// and returns one fixed Result, so the assembler's requirement bookkeeping is
// observable around a steer.
type steerDeclarerFin struct {
	order  int
	result toolcall.Result
}

func (*steerDeclarerFin) ID() string { return "steer-declarer-fin" }

func (f *steerDeclarerFin) Order() int { return f.order }

func (*steerDeclarerFin) ToolCallBufferingRequirement() toolcall.BufferingSpec {
	return toolcall.BufferingSpec{MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes}
}

func (f *steerDeclarerFin) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return f.result, nil
}

func steerPassResult() toolcall.Result {
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}
}

func steerRequest() *toolcall.SteerRequest {
	return &toolcall.SteerRequest{Instruction: "call one of the available tools", TurnBudget: 1}
}

func steerResult() toolcall.Result {
	return toolcall.Result{Action: toolcall.ActionSteer, Steer: steerRequest(), ReasonCode: toolcall.ReasonUnknownToolName}
}

// steerFixtureEvents is the original started/args/finished fragment sequence a
// steered call is assembled from.
func steerFixtureEvents(id, callName string) []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: callName, MessageIndex: 3},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, ToolName: callName, MessageIndex: 3, Delta: `{"q":"x"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: id, ToolName: callName, MessageIndex: 3},
	}
}

// steerDriveCall feeds one completed call through the assembler and returns the
// drain lifecycle plus the held/err answer of its finished event.
func steerDriveCall(t *testing.T, a *toolCallAssembler, id, callName string, gate func(string, toolcall.SteerRequest) bool) ([]aipapi.Event, bool, error) {
	t.Helper()
	ctx := context.Background()
	meta := toolcall.Meta{}
	events := steerFixtureEvents(id, callName)
	for _, ev := range events[:len(events)-1] {
		held, err := a.ingest(ctx, ev, meta, gate)
		if err != nil || !held {
			t.Fatalf("ingest %s: held=%v err=%v", ev.Kind, held, err)
		}
	}
	held, err := a.ingest(ctx, events[len(events)-1], meta, gate)
	var drained []aipapi.Event
	for {
		ev, ok := a.popDrain()
		if !ok {
			break
		}
		drained = append(drained, ev)
	}
	return drained, held, err
}

func TestToolCallAssembler_SteerOutcomeReleasesPassBaselineLifecycle(t *testing.T) {
	t.Parallel()
	baseline := newToolCallAssembler([]toolcall.Finalizer{
		&steerFin{id: "baseline-pass", order: 10, result: steerPassResult()},
		&steerFin{id: "baseline-tail", order: 20, result: steerPassResult()},
	}, 0, steerCatalog())
	want, held, err := steerDriveCall(t, baseline, "c1", steerToolName, nil)
	if err != nil || !held {
		t.Fatalf("baseline finished: held=%v err=%v", held, err)
	}
	if !reflect.DeepEqual(want, steerFixtureEvents("c1", steerToolName)) {
		t.Fatalf("ActionPass baseline lifecycle = %#v, want the original fragments", want)
	}

	for _, tc := range []struct {
		name string
		gate func(string, toolcall.SteerRequest) bool
	}{
		{name: "accepted", gate: func(string, toolcall.SteerRequest) bool { return true }},
		{name: "declined", gate: func(string, toolcall.SteerRequest) bool { return false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			steer := &steerFin{id: "steer", order: 10, result: steerResult()}
			tail := &steerFin{id: "steer-tail", order: 20, result: steerPassResult()}
			a := newToolCallAssembler([]toolcall.Finalizer{steer, tail}, 0, steerCatalog())
			got, held, err := steerDriveCall(t, a, "c1", steerToolName, tc.gate)
			if err != nil || !held {
				t.Fatalf("finished: held=%v err=%v", held, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("steer lifecycle = %#v, want the ActionPass baseline %#v", got, want)
			}
			if steer.calls != 1 || tail.calls != 1 {
				t.Fatalf("finalizer calls = steer:%d tail:%d, want 1 and 1: the chain must continue past a steer", steer.calls, tail.calls)
			}
		})
	}
}

func TestToolCallAssembler_SteerAcceptanceMarksAttemptOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		result        toolcall.Result
		installGate   bool
		gateAccepts   bool
		wantMarked    bool
		wantGateCalls int
	}{
		{name: "accepted", result: steerResult(), installGate: true, gateAccepts: true, wantMarked: true, wantGateCalls: 1},
		{name: "declined", result: steerResult(), installGate: true, wantMarked: false, wantGateCalls: 1},
		{name: "nil gate", result: steerResult(), wantMarked: false, wantGateCalls: 0},
		{
			name:          "invalid request",
			result:        toolcall.Result{Action: toolcall.ActionSteer, Steer: &toolcall.SteerRequest{Instruction: "call one of the available tools", TurnBudget: 4}, ReasonCode: toolcall.ReasonUnknownToolName},
			installGate:   true,
			gateAccepts:   true,
			wantMarked:    false,
			wantGateCalls: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attempt := newAttemptSession(attemptSessionInput{})
			gateCalls := 0
			var gate func(string, toolcall.SteerRequest) bool
			if tc.installGate {
				gate = func(name string, req toolcall.SteerRequest) bool {
					gateCalls++
					if !tc.gateAccepts {
						return false
					}
					attempt.markWithholding(name, req.Instruction)
					return true
				}
			}
			a := newToolCallAssembler([]toolcall.Finalizer{&steerFin{id: "steer", order: 10, result: tc.result}}, 0, steerCatalog())
			if _, held, err := steerDriveCall(t, a, "c1", steerToolName, gate); err != nil || !held {
				t.Fatalf("finished: held=%v err=%v", held, err)
			}
			if got := attempt.isWithholding(); got != tc.wantMarked {
				t.Fatalf("isWithholding() = %v, want %v", got, tc.wantMarked)
			}
			if gateCalls != tc.wantGateCalls {
				t.Fatalf("gate calls = %d, want %d", gateCalls, tc.wantGateCalls)
			}
		})
	}
}

func TestToolCallAssembler_SteerGateSeesRewrittenName(t *testing.T) {
	t.Parallel()
	rewriter := &steerFin{id: "repair-rewrite", order: 10, result: toolcall.Result{
		Action:     toolcall.ActionRewrite,
		ToolName:   steerToolName,
		ArgsJSON:   []byte(`{"q":"rewritten"}`),
		ReasonCode: toolcall.ReasonValidPassThrough,
	}}
	steer := &steerFin{id: "steer", order: 20, result: steerResult()}
	var gateName string
	gate := func(name string, _ toolcall.SteerRequest) bool {
		gateName = name
		return false
	}
	a := newToolCallAssembler([]toolcall.Finalizer{rewriter, steer}, 0, steerCatalog())
	if _, held, err := steerDriveCall(t, a, "c1", "invented_tool", gate); err != nil || !held {
		t.Fatalf("finished: held=%v err=%v", held, err)
	}
	if gateName != steerToolName {
		t.Fatalf("gate saw name %q, want the rewritten %q", gateName, steerToolName)
	}
	if steer.calls != 1 {
		t.Fatalf("steer finalizer calls = %d, want 1", steer.calls)
	}
}

func TestToolCallAssembler_UnusableSteerTakesFallback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		result toolcall.Result
	}{
		{
			name:   "steer action without request",
			result: toolcall.Result{Action: toolcall.ActionSteer, ReasonCode: toolcall.ReasonUnknownToolName},
		},
		{
			name: "steer action with invalid request",
			result: toolcall.Result{
				Action:     toolcall.ActionSteer,
				Steer:      &toolcall.SteerRequest{Instruction: "call one of the available tools", TurnBudget: 4},
				ReasonCode: toolcall.ReasonUnknownToolName,
			},
		},
		{
			name: "rewrite carrying a request",
			result: toolcall.Result{
				Action:   toolcall.ActionRewrite,
				ToolName: steerToolName,
				ArgsJSON: []byte(`{"q":"rewritten"}`),
				Steer:    steerRequest(),
			},
		},
		{
			name:   "reject carrying a request",
			result: toolcall.Result{Action: toolcall.ActionReject, ReasonCode: toolcall.ReasonUnrepairable, Steer: steerRequest()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gateCalls := 0
			gate := func(string, toolcall.SteerRequest) bool {
				gateCalls++
				return true
			}
			a := newToolCallAssembler([]toolcall.Finalizer{&steerFin{id: "steer", order: 10, result: tc.result}}, 0, steerCatalog())
			got, held, err := steerDriveCall(t, a, "c1", steerToolName, gate)
			if err != nil || !held {
				t.Fatalf("finished: held=%v err=%v, want the unusable-result fallback", held, err)
			}
			if !reflect.DeepEqual(got, steerFixtureEvents("c1", steerToolName)) {
				t.Fatalf("lifecycle = %#v, want the original fragments replayed", got)
			}
			if gateCalls != 0 {
				t.Fatalf("gate calls = %d, want 0: an unusable steer must not be offered", gateCalls)
			}
		})
	}
}

func TestToolCallAssembler_SteerRequirementBookkeepingMatchesPass(t *testing.T) {
	t.Parallel()
	accept := func(string, toolcall.SteerRequest) bool { return true }

	t.Run("pass carrying a request leaves the requirement undecided", func(t *testing.T) {
		t.Parallel()
		fin := &steerDeclarerFin{order: 10, result: toolcall.Result{Action: toolcall.ActionPass, Steer: steerRequest()}}
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, steerCatalog())
		got, held, err := steerDriveCall(t, a, "c1", steerToolName, accept)
		if !held || err == nil {
			t.Fatalf("held=%v err=%v, want the undecided-requirement refusal", held, err)
		}
		var refusal *MandatoryBufferingError
		if !errors.As(err, &refusal) || refusal.Reason != ReasonMandatoryBufferingIncomplete {
			t.Fatalf("err = %v, want the incomplete mandatory-buffering refusal", err)
		}
		if len(got) != 0 {
			t.Fatalf("refused call released %d events", len(got))
		}
	})

	t.Run("accepted steer decides for the steering declarer", func(t *testing.T) {
		t.Parallel()
		steer := &steerDeclarerFin{order: 10, result: steerResult()}
		tail := &steerFin{id: "later-unusable", order: 20, result: toolcall.Result{Action: toolcall.ActionRewrite}}
		a := newToolCallAssembler([]toolcall.Finalizer{steer, tail}, 0, steerCatalog())
		got, held, err := steerDriveCall(t, a, "c1", steerToolName, accept)
		if err != nil || !held {
			t.Fatalf("held=%v err=%v, want the call released after an accepted steer", held, err)
		}
		if !reflect.DeepEqual(got, steerFixtureEvents("c1", steerToolName)) {
			t.Fatalf("lifecycle = %#v, want the original fragments", got)
		}
	})
}
