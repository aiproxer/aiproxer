package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestRetryRecvStream_tryReplacement_blockedAfterMandatoryRecorderFailure(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.SecureSessionRecordingMandatory = true
	s := &retryRecvStream{
		terminal:         newTurnTerminal(),
		recovery:         &recoveryController{},
		responsePipeline: &responsePipeline{recordingOutcome: responseRecordingMandatoryPostCommitFailure},
		attempt:          testAttemptSlot(b2bua.BLegRecord{}, routing.AttemptCandidate{Key: "cand-1"}, authorityLifecycle{}),
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID: "tr-mand",
			aLegID:  "a-mand",
		}),
	}
	bindTestRuntimeOwners(s, ex)
	s.terminal.markCommitted(s.attempt.snapshot())
	request := s.facts.terminalFacts()
	request.replacementBlocked = true
	_, err := s.recovery.tryReplacementIteration(context.Background(), request, s.attempt.require(), s.terminal.committed())
	if err == nil {
		t.Fatal("expected error")
	}
	var uf *aipapi.UpstreamFailureError
	if !errors.As(err, &uf) || uf.Phase != aipapi.PhasePostOutput || uf.Recoverable {
		t.Fatalf("unexpected error: %v", err)
	}
}
