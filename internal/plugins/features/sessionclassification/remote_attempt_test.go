package sessionclassification_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// ambiguousInput is a turn no local rule accepts: a generic SDK User-Agent, a
// web-only tool category, and no workspace marker. It is the only input class
// that may reach a remote decision, so it drives the remote-attempt proofs.
//
// The operation is set because the canonical executor always supplies it, and
// the port refuses a turn whose operation is outside the closed vocabulary
// before any egress (requirement 7.2).
func ambiguousInput(sessionID string) sdkclassification.Input {
	return sdkclassification.Input{
		Session: session.SessionView{AuthoritativeSessionID: sessionID},
		Evidence: sdkclassification.Evidence{
			ClientUserAgent: "openai-python/1.40.0",
			Operation:       aipapi.OperationOpenAIResponses,
		},
	}
}

// TestStillUnknownRemoteCapableTurnAttemptsOneLeasedRemoteDecision pins the
// task's central behavior: a still-unknown turn in a remote-capable mode claims
// exactly one lease, performs exactly one remote decision, and completes that
// lease, instead of reporting a skipped remote decision (requirements 6.3, 6.4,
// 6.6, 6.7).
func TestStillUnknownRemoteCapableTurnAttemptsOneLeasedRemoteDecision(t *testing.T) {
	t.Parallel()

	for _, mode := range []sessionclassification.Mode{sessionclassification.ModeJev, sessionclassification.ModeHybrid} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()

			store := newFakeStore()
			decider := &countingDecider{fallback: scriptedAnswer{
				decision: sessionclassification.RemoteDecision{CodingProbability: 0.99},
			}}
			classifier := mustRemoteClassifier(t, sessionclassification.Config{
				Mode:   mode,
				Remote: validTestRemoteConfig(),
			}, store, fixedNow(), decider)

			got, err := classifier.Classify(t.Context(), ambiguousInput("sess-attempt-"+string(mode)))
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			granted, completions, maxActive := store.leaseCounts()
			if granted != 1 {
				t.Fatalf("granted remote leases = %d, want exactly one", granted)
			}
			if completions != 1 {
				t.Fatalf("remote completions = %d, want exactly one", completions)
			}
			if maxActive != 1 {
				t.Fatalf("simultaneously active leases = %d, want exactly one", maxActive)
			}
			if decider.callCount() != 1 {
				t.Fatalf("remote calls = %d, want exactly one", decider.callCount())
			}
			// Requirement 6.8: an above-threshold remote answer promotes with the
			// remote source and the remote evidence code, never a local one.
			if !got.IsCodingAgent() || got.Source != session.SourceRemote ||
				got.Evidence != sessionclassification.EvidenceCodeRemoteAboveThreshold {
				t.Fatalf("classification = %+v, want a remote-sourced promotion", got)
			}
		})
	}
}
