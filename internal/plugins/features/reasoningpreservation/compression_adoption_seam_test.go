package reasoningpreservation_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// classifiedFailureStore returns a snapshot holding an artifact that fails
// validation. The attempt therefore cannot classify restore candidates, which
// is the branch that synthesizes a poll error before any poll is attempted.
type classifiedFailureStore struct {
	reasoningpreservation.CompressionStore
}

func (classifiedFailureStore) Snapshot(context.Context, reasoningpreservation.SessionPartition) ([]reasoningpreservation.TurnArtifact, error) {
	return []reasoningpreservation.TurnArtifact{{}}, nil
}

// TestTransform_ClassifiedFailureStillCrossesAdoptionSeam pins the adoption seam
// contract on the classification-failure branch. The injected stage is invoked
// exactly once with a non-adoptable result, and the attempt outcome still comes
// from the on_state_error policy rather than from the stage's projection: a
// synthesized poll error carries no candidate, so there is nothing to adopt.
func TestTransform_ClassifiedFailureStillCrossesAdoptionSeam(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		policy     string
		wantKind   request.AttemptDecisionKind
		wantReason string
	}{
		{name: "reject", policy: reasoningpreservation.PolicyReject, wantKind: request.AttemptExcludeCandidate, wantReason: "state_error"},
		{name: "log_skip", policy: reasoningpreservation.PolicyLogSkip, wantKind: request.AttemptContinue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := pollTestConfig(t)
			cfg.OnStateError = tc.policy
			store := classifiedFailureStore{CompressionStore: storeForSubmit(t, cfg)}
			var (
				calls int
				seen  reasoningpreservation.AdoptionResult
			)
			stage := func(_ context.Context, ar reasoningpreservation.AdoptionResult) reasoningpreservation.AdoptionResult {
				calls++
				seen = ar
				return ar
			}
			xform := reasoningpreservation.NewAttemptTransformWithCompanionPolicyServicesAndStage(
				cfg, store, reasoningpreservation.CompressionServices{}, reasoningpreservation.CompanionPolicy{}, stage)
			call, _ := missingRestoreFixture(t)
			dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
				BackendID: "be", Model: "m", ReplaySupport: pollTestSupport,
				Session: session.SessionView{AuthoritativeSessionID: "sess-classified-failure"},
			}, request.Services{})
			require.NoError(t, err)
			require.Equal(t, 1, calls, "the adoption chain must observe a classification-failure attempt exactly once")
			require.Equal(t, reasoningpreservation.AdoptionOutcomeNone, seen.Outcome)
			require.Nil(t, seen.Candidate, "a synthesized poll error carries no candidate to adopt")
			require.Equal(t, tc.wantKind, dec.Kind)
			require.Equal(t, tc.wantReason, dec.ReasonCode)
		})
	}
}
