package runtime

import (
	"context"
	"strings"
	"testing"

	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

type evidenceSkipStubProvider struct{}

func (s *evidenceSkipStubProvider) ID() string { return "evidence-skip-stub" }

func (s *evidenceSkipStubProvider) Decide(_ context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "stub"}, nil
}

func evidenceSkipRequest() requestTerminalFacts {
	return requestTerminalFacts{
		traceID: "trace-skip",
		aLegID:  "a-leg-skip",
		call: aipapi.Call{
			Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("first question")}},
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("second question")}},
			},
		},
	}
}

func TestTerminalDecisionEvalInputSkipsEvidenceWithoutProvider(t *testing.T) {
	t.Parallel()

	turn := newTurnTerminal()
	req := evidenceSkipRequest()
	p := newResponsePipeline()
	snap := coreterm.NewAccumulatorSnapshot(nil, true)

	got := turn.terminalDecisionEvalInput(sdkterminal.CommandNormalFinish, req, nil, p, snap)
	if got.Evidence.Objective != "" || got.Evidence.RecentText != "" || got.Evidence.CandidateText != "" {
		t.Fatalf("nil-provider evidence body = %+v, want empty body", got.Evidence)
	}
	if got.Evidence.ActionCount != 0 {
		t.Fatalf("nil-provider action count = %d, want 0", got.Evidence.ActionCount)
	}
	// Every field the no-provider evaluator and observation consume stays intact.
	if got.Candidate.Cause != terminaldecision.CandidateCauseNormal {
		t.Fatalf("candidate cause = %q, want normal", got.Candidate.Cause)
	}
	if !got.Candidate.OutputCommitted {
		t.Fatal("candidate output committed = false, want true from snapshot")
	}
	if got.Request.TraceID != "trace-skip" || got.Request.ALegID != "a-leg-skip" {
		t.Fatalf("request identity = %+v, want trace/a-leg", got.Request)
	}
	if got.Policy.Revision != "0" {
		t.Fatalf("policy revision = %q, want default 0", got.Policy.Revision)
	}
	if got.Deadline.IsZero() {
		t.Fatal("deadline is zero, want evaluation budget")
	}

	// The skipped input decides exactly like the projected one: allow-stop
	// without a provider, independent of the evidence payload.
	full := turn.terminalDecisionInput(sdkterminal.CommandNormalFinish, req, nil, p, snap)
	if full.Evidence.RecentText == "" {
		t.Fatal("projected evidence recent text is empty, want history text")
	}
	if got.Continuation != full.Continuation {
		t.Fatalf("skipped continuation = %+v, want %+v", got.Continuation, full.Continuation)
	}
	want := turn.sharedTerminalDecision(context.Background(), nil, full)
	skip := turn.sharedTerminalDecision(context.Background(), nil, got)
	if skip != want {
		t.Fatalf("skipped outcome = %+v, want %+v", skip, want)
	}
	if skip.Decision.Kind != terminaldecision.DecisionAllowStop {
		t.Fatalf("skipped decision kind = %q, want allow_stop", skip.Decision.Kind)
	}
}

func TestTerminalDecisionEvalInputProjectsEvidenceWithProvider(t *testing.T) {
	t.Parallel()

	turn := newTurnTerminal()
	turn.terminalDecisionProvider = &evidenceSkipStubProvider{}
	req := evidenceSkipRequest()
	p := newResponsePipeline()
	snap := coreterm.NewAccumulatorSnapshot(nil, false)

	got := turn.terminalDecisionEvalInput(sdkterminal.CommandNormalFinish, req, nil, p, snap)
	if !strings.Contains(got.Evidence.RecentText, "first question") || !strings.Contains(got.Evidence.RecentText, "second question") {
		t.Fatalf("provider evidence recent text = %q, want history text", got.Evidence.RecentText)
	}
	if got.Evidence.Objective == "" {
		t.Fatal("provider evidence objective is empty, want user text")
	}
}
