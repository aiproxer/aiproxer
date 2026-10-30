package runtime

import (
	"context"
	"testing"

	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestContinuationCarriesProviderIntentMetadataToB2TerminalDecisionInput(t *testing.T) {
	terminalOwner, stream, b1, _, _ := newContinuationRedHarness(t, nil)
	intent := continuationIntent()
	intent.TrajectoryRef = "provider-trajectory"
	intent.ControlRef = "provider-control"

	var pinned requestTerminalFacts
	var b2 *attemptSession
	stream.recovery.opener = func(_ context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		pinned = req.pinnedFacts.terminalFacts()
		out := continuationOpenResult(t, b1)
		b2 = out.ready.session
		return out, nil
	}

	published, err := runContinuationTransaction(context.Background(), terminalOwner, stream, intent)
	if err != nil || !published {
		t.Fatalf("continuation = published %v, err %v", published, err)
	}
	if !pinned.continuationIntent.set {
		t.Fatal("B2 pinned facts did not retain provider continuation metadata")
	}

	input := terminalOwner.terminalDecisionInput(
		sdkterminal.CommandNormalFinish,
		pinned,
		b2,
		stream.responsePipeline,
		coreterm.NewAccumulatorSnapshot(nil, true),
	)
	wantAttempt := uint8(b1.bleg.Seq + 1)
	if input.Continuation.TrajectoryRef != intent.TrajectoryRef {
		t.Fatalf("continuation trajectory = %q, want %q", input.Continuation.TrajectoryRef, intent.TrajectoryRef)
	}
	if input.Evidence.Lineage.ProgressRef != intent.ControlRef {
		t.Fatalf("evidence progress ref = %q, want %q", input.Evidence.Lineage.ProgressRef, intent.ControlRef)
	}
	if input.Continuation.Attempt != wantAttempt || input.Evidence.Lineage.Attempt != wantAttempt {
		t.Fatalf("semantic attempt = continuation %d/evidence %d, want %d", input.Continuation.Attempt, input.Evidence.Lineage.Attempt, wantAttempt)
	}
}
