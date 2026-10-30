package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestTerminalDecisionEvidence_ContinuationSeesAttemptLocalCandidateText is
// the finding-3 regression: the candidate text projected for progress
// detection must cover only the current attempt's own answer. The cumulative
// released text would make identical answers on successive attempts produce
// different fingerprints, so the no-progress breaker could never fire.
//
// The test drives a real continuation transaction and the real evidence
// projection; it does not hand-craft terminaldecision.Input values.
func TestTerminalDecisionEvidence_ContinuationSeesAttemptLocalCandidateText(t *testing.T) {
	terminal, stream, b1, _, _ := newContinuationRedHarness(t, nil)
	stream.recovery.opener = func(_ context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		return continuationOpenResult(t, b1), nil
	}
	p := stream.responsePipeline

	p.rememberClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "Still working."})
	first := projectTerminalDecisionEvidence(stream.facts.terminalFacts(), b1, p)
	if first.CandidateText != "Still working." {
		t.Fatalf("B1 candidate text = %q, want the attempt's own answer", first.CandidateText)
	}

	published, err := runContinuationTransaction(context.Background(), terminal, stream, continuationIntent())
	if err != nil || !published {
		t.Fatalf("continuation = published %v, err %v", published, err)
	}
	b2 := stream.attempt.snapshot()
	if b2 == nil || b2 == b1 {
		t.Fatal("continuation did not publish a distinct B2")
	}

	// The new attempt emits the identical answer.
	p.rememberClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "Still working."})
	second := projectTerminalDecisionEvidence(stream.facts.terminalFacts(), b2, p)
	if second.CandidateText != first.CandidateText {
		t.Fatalf("B2 candidate text = %q, want the attempt-local answer %q, not the cumulative stream", second.CandidateText, first.CandidateText)
	}

	// Delivery keeps the cumulative view: the projection change must not
	// starve accounting, history, or the client.
	if got := p.releasedOutputText(); got != "Still working.Still working." {
		t.Fatalf("released output text = %q, want the cumulative stream preserved", got)
	}
}

// TestTerminalDecisionEvidence_ContinuationSeesNewAttemptText pins the other
// direction: genuinely new text on the continued attempt is fully visible to
// progress detection.
func TestTerminalDecisionEvidence_ContinuationSeesNewAttemptText(t *testing.T) {
	terminal, stream, b1, _, _ := newContinuationRedHarness(t, nil)
	stream.recovery.opener = func(_ context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		return continuationOpenResult(t, b1), nil
	}
	p := stream.responsePipeline

	p.rememberClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "Still working."})
	first := projectTerminalDecisionEvidence(stream.facts.terminalFacts(), b1, p)

	published, err := runContinuationTransaction(context.Background(), terminal, stream, continuationIntent())
	if err != nil || !published {
		t.Fatalf("continuation = published %v, err %v", published, err)
	}
	b2 := stream.attempt.snapshot()

	p.rememberClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "Backfill verified."})
	second := projectTerminalDecisionEvidence(stream.facts.terminalFacts(), b2, p)
	if second.CandidateText != "Backfill verified." {
		t.Fatalf("B2 candidate text = %q, want only the new attempt's answer", second.CandidateText)
	}
	if second.CandidateText == first.CandidateText {
		t.Fatalf("new answer %q must differ from the prior baseline %q", second.CandidateText, first.CandidateText)
	}
}
