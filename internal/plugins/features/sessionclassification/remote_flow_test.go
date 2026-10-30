package sessionclassification_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// decisiveLocalInput is a turn local evaluation promotes on its own. It is the
// fixture that separates hybrid from jev at the decision point: hybrid stops
// here and never touches the lease, jev continues to the remote phase because
// only a remote positive may promote (requirements 6.2, 6.3, 6.4).
// stillUnknownRemoteInput is a turn whose local evidence is deliberately not
// decisive, so the remote phase is entered rather than short-circuited.
func stillUnknownRemoteInput(sessionID string) sdkclassification.Input {
	in := decisiveLocalInput(sessionID)
	in.Evidence.ClientUserAgent = "python-requests/2.32.3"
	return in
}

func decisiveLocalInput(sessionID string) sdkclassification.Input {
	return sdkclassification.Input{
		Session: session.SessionView{AuthoritativeSessionID: sessionID},
		Evidence: sdkclassification.Evidence{
			ClientUserAgent: "codex_cli_rs/1.2.3",
			Operation:       aipapi.OperationOpenAIResponses,
		},
	}
}

// remoteConfigWith returns the valid remote settings with two fields replaced, so
// a test can vary the finite attempt budget or the retry backoff without restating
// the whole posture.
func remoteConfigWith(attempts int, backoff time.Duration) *sessionclassification.RemoteConfig {
	cfg := *validTestRemoteConfig()
	cfg.MaxAttemptsPerSession = attempts
	cfg.RetryBackoff = backoff
	return &cfg
}

// TestRemoteCallCountMatrix pins the exact remote-work budget of every mode and
// every starting state. It is the load-bearing table for requirements 6.1-6.5
// and 10.1: each row states the number of granted leases and the number of
// decider calls one turn must produce, so a mode that reached the network when it
// should not, or skipped a turn it should have attempted, fails here.
func TestRemoteCallCountMatrix(t *testing.T) {
	t.Parallel()

	// A decider that would promote if it were ever called: a heuristic turn and a
	// decisive hybrid turn that made one call would be caught by the promoted
	// assertion, not only by the call count.
	promoting := func() *countingDecider {
		return &countingDecider{fallback: scriptedAnswer{
			decision: sessionclassification.RemoteDecision{CodingProbability: 0.99},
		}}
	}

	t.Run("heuristic unknown turn makes no call and no claim", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore()
		// A heuristic generation is refused a decider, so the count is observed
		// through the store's lease counters instead (requirement 6.2).
		classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, store, fixedNow())
		got, err := classifier.Classify(t.Context(), ambiguousInput("sess-heuristic-unknown"))
		if err != nil {
			t.Fatalf("Classify: %v", err)
		}
		if got != (session.Classification{}) {
			t.Fatalf("classification = %+v, want unknown", got)
		}
		if granted, completions, _ := store.leaseCounts(); granted != 0 || completions != 0 {
			t.Fatalf("heuristic granted %d leases and %d completions, want none", granted, completions)
		}
	})

	t.Run("jev unknown turn claims once and calls once", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore()
		decider := belowThresholdDecider()
		classifier := mustRemoteClassifier(t, sessionclassification.Config{
			Mode:   sessionclassification.ModeJev,
			Remote: validTestRemoteConfig(),
		}, store, fixedNow(), decider)

		if _, err := classifier.Classify(t.Context(), ambiguousInput("sess-jev-unknown")); err != nil {
			t.Fatalf("Classify: %v", err)
		}
		if decider.callCount() != 1 {
			t.Fatalf("remote calls = %d, want exactly one", decider.callCount())
		}
		granted, completions, maxActive := store.leaseCounts()
		if granted != 1 || completions != 1 || maxActive != 1 {
			t.Fatalf("leases granted=%d completed=%d maxActive=%d, want 1/1/1", granted, completions, maxActive)
		}
	})

	t.Run("jev turn with decisive local evidence still requires the remote positive", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore()
		decider := belowThresholdDecider()
		classifier := mustRemoteClassifier(t, sessionclassification.Config{
			Mode:   sessionclassification.ModeJev,
			Remote: validTestRemoteConfig(),
		}, store, fixedNow(), decider)

		got, err := classifier.Classify(t.Context(), decisiveLocalInput("sess-jev-decisive"))
		if err != nil {
			t.Fatalf("Classify: %v", err)
		}
		// Requirement 6.3: decisive local evidence is not sufficient in jev mode.
		if got != (session.Classification{}) {
			t.Fatalf("classification = %+v, want unknown without a remote positive", got)
		}
		if decider.callCount() != 1 {
			t.Fatalf("remote calls = %d, want exactly one", decider.callCount())
		}
		if _, promotes := store.counts(); promotes != 0 {
			t.Fatalf("durable promotions = %d, want zero without a remote positive", promotes)
		}
	})

	t.Run("hybrid decisive local turn promotes without any remote work", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore()
		decider := promoting()
		classifier := mustRemoteClassifier(t, sessionclassification.Config{
			Mode:   sessionclassification.ModeHybrid,
			Remote: validTestRemoteConfig(),
		}, store, fixedNow(), decider)

		got, err := classifier.Classify(t.Context(), decisiveLocalInput("sess-hybrid-decisive"))
		if err != nil {
			t.Fatalf("Classify: %v", err)
		}
		// Requirement 6.4: decisive local evidence promotes first.
		if !got.IsCodingAgent() || got.Source != session.SourceLocalIdentity {
			t.Fatalf("classification = %+v, want a local promotion", got)
		}
		if decider.callCount() != 0 {
			t.Fatalf("remote calls = %d, want none after a local promotion", decider.callCount())
		}
		if granted, _, _ := store.leaseCounts(); granted != 0 {
			t.Fatalf("granted remote leases = %d, want none after a local promotion", granted)
		}
	})

	t.Run("hybrid non-decisive turn claims once and calls once", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore()
		decider := belowThresholdDecider()
		classifier := mustRemoteClassifier(t, sessionclassification.Config{
			Mode:   sessionclassification.ModeHybrid,
			Remote: validTestRemoteConfig(),
		}, store, fixedNow(), decider)

		if _, err := classifier.Classify(t.Context(), ambiguousInput("sess-hybrid-ambiguous")); err != nil {
			t.Fatalf("Classify: %v", err)
		}
		if decider.callCount() != 1 {
			t.Fatalf("remote calls = %d, want exactly one", decider.callCount())
		}
		if granted, completions, _ := store.leaseCounts(); granted != 1 || completions != 1 {
			t.Fatalf("leases granted=%d completed=%d, want 1/1", granted, completions)
		}
	})

	t.Run("already-positive session makes no call in either remote mode", func(t *testing.T) {
		t.Parallel()

		for _, mode := range []sessionclassification.Mode{sessionclassification.ModeJev, sessionclassification.ModeHybrid} {
			t.Run(string(mode), func(t *testing.T) {
				t.Parallel()

				store := newFakeStore()
				prior := session.Classification{
					Kind:       session.KindCodingAgent,
					Source:     session.SourceLocalIdentity,
					Confidence: session.ConfidenceHigh,
					Evidence:   "client_family.codex",
					Revision:   1,
				}
				if _, promoted, err := store.Promote(t.Context(), sessionclassification.Key{
					Kind: sessionclassification.ScopeSecureSession, ID: "sess-positive-" + string(mode),
				}, prior, fixedNow()()); err != nil || !promoted {
					t.Fatalf("seed positive: promoted=%t err=%v", promoted, err)
				}
				decider := promoting()
				classifier := mustRemoteClassifier(t, sessionclassification.Config{
					Mode:   mode,
					Remote: validTestRemoteConfig(),
				}, store, fixedNow(), decider)

				in := ambiguousInput("sess-positive-" + string(mode))
				got, err := classifier.Classify(t.Context(), in)
				if err != nil {
					t.Fatalf("Classify: %v", err)
				}
				if got != prior {
					t.Fatalf("classification = %+v, want the preserved positive %+v", got, prior)
				}
				// Requirements 6.5 and 10.1: an established positive needs no
				// remote call and no durable remote work.
				if decider.callCount() != 0 {
					t.Fatalf("remote calls = %d, want none for an established positive", decider.callCount())
				}
				if granted, completions, _ := store.leaseCounts(); granted != 0 || completions != 0 {
					t.Fatalf("positive turn granted %d leases and %d completions, want none", granted, completions)
				}
			})
		}
	})
}

// TestRemotePositivePromotesMonotonicallyOnce pins requirements 6.8 and 9.1 for
// the promotion path: the accepted proposal is remote-sourced at the static
// remote evidence code and revision 1, exactly one transition observation is
// emitted, and a later turn of the same session makes no further remote call.
func TestRemotePositivePromotesMonotonicallyOnce(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	observer := &recordingObserver{}
	decider := &countingDecider{fallback: scriptedAnswer{
		decision: sessionclassification.RemoteDecision{CodingProbability: 0.97, Confidence: 0.97},
	}}
	classifier := observedRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: validTestRemoteConfig(),
	}, &fakeAuthority{store: store}, observer, decider)

	got, err := classifier.Classify(t.Context(), ambiguousInput("sess-remote-positive"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	want := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceRemote,
		Confidence: session.ConfidenceHigh,
		Evidence:   sessionclassification.EvidenceCodeRemoteAboveThreshold,
		Revision:   1,
	}
	if got != want {
		t.Fatalf("classification = %+v, want %+v", got, want)
	}
	evaluations, transitions := observer.snapshot()
	if len(transitions) != 1 {
		t.Fatalf("transition observations = %d, want exactly one", len(transitions))
	}
	if transitions[0].Snapshot() != want {
		t.Fatalf("transition observation = %+v, want the accepted remote snapshot", transitions[0])
	}
	if evaluations[0].Outcome != sessionclassification.EvaluationPromoted {
		t.Fatalf("evaluation outcome = %q, want %q", evaluations[0].Outcome, sessionclassification.EvaluationPromoted)
	}
	// Requirement 9.3: the attempt reports its bounded outcome and a bounded
	// latency, never a request or response body.
	remote := observer.remoteObservations()
	if len(remote) != 1 || remote[0].Outcome != sessionclassification.RemotePositive {
		t.Fatalf("remote observations = %+v, want one positive", remote)
	}
	if !sessionclassification.ValidRemoteObservation(remote[0]) {
		t.Fatalf("remote observation %+v is outside the bounded vocabulary", remote[0])
	}

	// Requirement 6.5: the session is now coding_agent, so a second turn makes no
	// remote call at all.
	second, err := classifier.Classify(t.Context(), ambiguousInput("sess-remote-positive"))
	if err != nil {
		t.Fatalf("second Classify: %v", err)
	}
	if second != want {
		t.Fatalf("second classification = %+v, want the stable positive", second)
	}
	if decider.callCount() != 1 {
		t.Fatalf("remote calls over two turns = %d, want exactly one", decider.callCount())
	}
	if _, transitions := observer.snapshot(); len(transitions) != 1 {
		t.Fatalf("transition observations = %d, want one for the whole session", len(transitions))
	}
}

// TestRemoteBelowThresholdNeverWritesANegative pins requirements 6.8, 6.9, and
// 1.5: a valid but below-threshold remote answer, and an unusable answer, both
// leave the session unknown and create no durable classification at all. The
// completion still runs, so the lease is not abandoned.
func TestRemoteBelowThresholdNeverWritesANegative(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		decision sessionclassification.RemoteDecision
		err      error
	}{
		{
			name:     "valid below-threshold answer",
			decision: sessionclassification.RemoteDecision{CodingProbability: 0.89},
		},
		{
			// Requirement 6.9: a probability outside the thresholdable range is a
			// bounded remote error, not a negative classification.
			name:     "probability above one",
			decision: sessionclassification.RemoteDecision{CodingProbability: 1.5},
		},
		{
			name:     "not-a-number probability",
			decision: sessionclassification.RemoteDecision{CodingProbability: math.NaN()},
		},
		{
			name: "bounded remote failure",
			err:  boundedFailure{outcome: sessionclassification.RemoteMalformed, cause: errors.New("scripted")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeStore()
			key := sessionclassification.Key{Kind: sessionclassification.ScopeSecureSession, ID: "sess-below-" + tc.name}
			decider := &countingDecider{fallback: scriptedAnswer{decision: tc.decision, err: tc.err}}
			classifier := mustRemoteClassifier(t, sessionclassification.Config{
				Mode:   sessionclassification.ModeJev,
				Remote: remoteConfigWith(1, 0),
			}, store, fixedNow(), decider)

			got, err := classifier.Classify(t.Context(), ambiguousInput(key.ID))
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if got != (session.Classification{}) {
				t.Fatalf("classification = %+v, want unknown", got)
			}
			if decider.callCount() != 1 {
				t.Fatalf("remote calls = %d, want exactly one attempt", decider.callCount())
			}
			record, ok := store.stored(key)
			if !ok {
				t.Fatal("the completed attempt persisted no durable record")
			}
			if record.Classification != (session.Classification{}) {
				t.Fatalf("persisted classification = %+v, want none: absence of a positive is never negative", record.Classification)
			}
			// Requirement 6.7: the lease is finished, not abandoned.
			if record.RemoteLeaseID != "" || !record.RemoteLeaseUntil.IsZero() {
				t.Fatalf("persisted record still holds a lease: %+v", record)
			}
			if record.RemoteAttempts != 1 {
				t.Fatalf("persisted attempts = %d, want exactly one", record.RemoteAttempts)
			}
		})
	}
}

// TestRemoteFailureNeverFailsTheUserRequest pins requirements 6.9, 9.2, and
// 12.8 across every bounded failure shape requirement 12.8 names: a hard
// timeout, a rate limit, a server error, a malformed or oversized body, and a
// canceled call. Each returns unknown with no error, so the generic stage runner
// fails open and the request continues.
// TestRemoteStoreFailureIsReportedRatherThanSwallowed covers the two durable
// state-failure branches inside the remote phase. remote_flow.go wraps a
// ClaimRemote or CompleteRemote failure in ErrStateUnavailable, but the fake
// store's claimErr and completeErr fixtures were never set by any test, so both
// branches were unobservable and the wrapping could have been deleted silently.
// A durable-state failure is the one remote outcome that is NOT a fail-open:
// reporting it lets the caller distinguish "the vendor said no" from "we could
// not record the answer".
func TestRemoteStoreFailureIsReportedRatherThanSwallowed(t *testing.T) {
	t.Parallel()

	stateErr := errors.New("durable classification state is unavailable")
	for _, tc := range []struct {
		name  string
		apply func(*fakeStore)
	}{
		{name: "the claim fails", apply: func(s *fakeStore) { s.claimErr = stateErr }},
		{name: "the completion fails", apply: func(s *fakeStore) { s.completeErr = stateErr }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeStore()
			tc.apply(store)
			decider := belowThresholdDecider()
			classifier := observedRemoteClassifier(t, sessionclassification.Config{
				Mode:   sessionclassification.ModeJev,
				Remote: validTestRemoteConfig(),
			}, &fakeAuthority{store: store}, &recordingObserver{}, decider)

			_, err := classifier.Classify(t.Context(), stillUnknownRemoteInput("sess-store-failure"))
			if !errors.Is(err, sessionclassification.ErrStateUnavailable) {
				t.Fatalf("Classify error = %v, want it to report %v", err, sessionclassification.ErrStateUnavailable)
			}
			if !errors.Is(err, stateErr) {
				t.Fatalf("Classify error = %v, want it to wrap the underlying store failure", err)
			}
		})
	}
}

func TestRemoteFailureNeverFailsTheUserRequest(t *testing.T) {
	t.Parallel()

	outcomes := []struct {
		outcome sessionclassification.RemoteOutcome
		want    sessionclassification.EvaluationOutcome
	}{
		{outcome: sessionclassification.RemoteTimeout, want: sessionclassification.EvaluationRemoteTimeout},
		{outcome: sessionclassification.RemoteRateLimited, want: sessionclassification.EvaluationRemoteError},
		{outcome: sessionclassification.RemoteServerError, want: sessionclassification.EvaluationRemoteError},
		{outcome: sessionclassification.RemoteNetworkError, want: sessionclassification.EvaluationRemoteError},
		{outcome: sessionclassification.RemoteMalformed, want: sessionclassification.EvaluationRemoteError},
		// A credential or refused-request failure is an adapter-side refusal: no
		// usable decision arrived, so the bounded diagnostic is a bare unknown
		// rather than a remote error.
		{outcome: sessionclassification.RemoteSkipped, want: sessionclassification.EvaluationUnknown},
	}

	for _, tc := range outcomes {
		t.Run(string(tc.outcome), func(t *testing.T) {
			t.Parallel()

			store := newFakeStore()
			observer := &recordingObserver{}
			decider := &countingDecider{fallback: scriptedAnswer{
				err: boundedFailure{outcome: tc.outcome, cause: errors.New("scripted")},
			}}
			classifier := observedRemoteClassifier(t, sessionclassification.Config{
				Mode:   sessionclassification.ModeJev,
				Remote: remoteConfigWith(1, 0),
			}, &fakeAuthority{store: store}, observer, decider)

			got, err := classifier.Classify(t.Context(), ambiguousInput("sess-failure-"+string(tc.outcome)))
			if err != nil {
				t.Fatalf("Classify returned %v, want the request preserved with no error", err)
			}
			if got != (session.Classification{}) {
				t.Fatalf("classification = %+v, want unknown", got)
			}
			evaluations, transitions := observer.snapshot()
			if len(evaluations) != 1 || evaluations[0].Outcome != tc.want {
				t.Fatalf("evaluations = %+v, want one %q outcome", evaluations, tc.want)
			}
			if len(transitions) != 0 {
				t.Fatalf("transitions = %+v, want none without a positive", transitions)
			}
			remote := observer.remoteObservations()
			if len(remote) != 1 || remote[0].Outcome != tc.outcome {
				t.Fatalf("remote observations = %+v, want one %q", remote, tc.outcome)
			}
			// Requirement 6.7: the lease is completed even on failure, so the
			// attempt state is not left dangling.
			granted, completions, _ := store.leaseCounts()
			if granted != 1 || completions != 1 {
				t.Fatalf("leases granted=%d completed=%d, want 1/1", granted, completions)
			}
		})
	}
}

// TestUnboundedRemoteFailureRetriesUpToBudgetAsNetworkError proves the retry gate
// is a whitelist with a bounded fallback: an error that carries no bounded
// outcome at all is reported as a network failure and, because that fallback IS
// retryable, consumes exactly the finite per-session budget. Requirement 6.7's
// finite budget therefore holds even for an adapter whose error shape is unknown
// here.
func TestUnboundedRemoteFailureRetriesUpToBudgetAsNetworkError(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	observer := &recordingObserver{}
	decider := &countingDecider{fallback: scriptedAnswer{err: errors.New("unclassified failure")}}
	classifier := observedRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: remoteConfigWith(3, 0),
	}, &fakeAuthority{store: store}, observer, decider)

	if _, err := classifier.Classify(t.Context(), ambiguousInput("sess-unbounded-failure")); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	// An unclassifiable error maps to RemoteNetworkError, which IS retryable, so
	// the finite budget is what bounds it rather than a refusal.
	if decider.callCount() != 3 {
		t.Fatalf("remote calls = %d, want the finite budget of 3", decider.callCount())
	}
	remote := observer.remoteObservations()
	if len(remote) != 3 {
		t.Fatalf("remote observations = %d, want one per attempt", len(remote))
	}
	for _, observation := range remote {
		if observation.Outcome != sessionclassification.RemoteNetworkError {
			t.Fatalf("outcome = %q, want the bounded network fallback", observation.Outcome)
		}
	}
	record, ok := store.stored(sessionclassification.Key{
		Kind: sessionclassification.ScopeSecureSession, ID: "sess-unbounded-failure",
	})
	if !ok || record.RemoteAttempts != 3 {
		t.Fatalf("record = %+v found=%t, want exactly the finite budget consumed", record, ok)
	}
}

// TestRetryBackoffAndAttemptBudgetAreEnforced is the evidence for the obligation
// carried out of 8.2: RemoteConfig.RetryBackoff and
// RemoteConfig.MaxAttemptsPerSession are consumed by this loop.
//
// The proof observes a real second attempt: the scripted decider fails with a
// retryable rate limit, the classifier waits the configured backoff between
// attempts, and the turn stops exactly at the configured budget. A green suite
// that merely compiled would not show any of that.
func TestRetryBackoffAndAttemptBudgetAreEnforced(t *testing.T) {
	t.Parallel()

	const (
		attempts = 3
		backoff  = 20 * time.Millisecond
	)
	store := newFakeStore()
	decider := &countingDecider{answers: []scriptedAnswer{
		{err: boundedFailure{outcome: sessionclassification.RemoteRateLimited, cause: errors.New("first")}},
		{err: boundedFailure{outcome: sessionclassification.RemoteServerError, cause: errors.New("second")}},
		{err: boundedFailure{outcome: sessionclassification.RemoteTimeout, cause: errors.New("third")}},
	}}
	// The store anchors the completion-anchored retry backoff at the timestamp
	// the classifier supplies, so this fixture needs the process clock for the
	// deadline to be released at all.
	classifier := mustRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: remoteConfigWith(attempts, backoff),
	}, store, realNow(), decider)

	started := time.Now()
	got, err := classifier.Classify(t.Context(), ambiguousInput("sess-retry"))
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != (session.Classification{}) {
		t.Fatalf("classification = %+v, want unknown after exhausted retries", got)
	}
	// A real second and third attempt happened, not just a compiled loop.
	if decider.callCount() != attempts {
		t.Fatalf("remote calls = %d, want exactly the configured budget %d", decider.callCount(), attempts)
	}
	// The configured backoff was actually awaited between attempts: at least
	// attempts-1 waits of the configured duration elapsed.
	if want := time.Duration(attempts-1) * backoff; elapsed < want {
		t.Fatalf("elapsed = %s, want at least %s of configured retry backoff", elapsed, want)
	}
	record, ok := store.stored(sessionclassification.Key{Kind: sessionclassification.ScopeSecureSession, ID: "sess-retry"})
	if !ok {
		t.Fatal("retrying attempts persisted no record")
	}
	if record.RemoteAttempts != attempts {
		t.Fatalf("persisted attempts = %d, want exactly the configured budget", record.RemoteAttempts)
	}
	// Requirement 6.7: the finite budget is a per-session counter, so a later
	// turn of the same session performs no further egress.
	if _, err := classifier.Classify(t.Context(), ambiguousInput("sess-retry")); err != nil {
		t.Fatalf("second Classify: %v", err)
	}
	if decider.callCount() != attempts {
		t.Fatalf("remote calls after the budget was spent = %d, want %d", decider.callCount(), attempts)
	}
}

// TestNonRetryableRemoteFailureStopsAtOneAttempt pins the retry whitelist from
// the other side: a malformed response is a determined fact about the payload, so
// a second identical attempt is never made even though the budget allows it
// (requirements 6.7, 12.8).
func TestNonRetryableRemoteFailureStopsAtOneAttempt(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	decider := &countingDecider{fallback: scriptedAnswer{
		err: boundedFailure{outcome: sessionclassification.RemoteMalformed, cause: errors.New("unparseable")},
	}}
	classifier := mustRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: remoteConfigWith(5, 0),
	}, store, fixedNow(), decider)

	if _, err := classifier.Classify(t.Context(), ambiguousInput("sess-malformed")); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if decider.callCount() != 1 {
		t.Fatalf("remote calls = %d, want exactly one for a determined failure", decider.callCount())
	}
}

// TestBusyLeaseMakesNoRemoteCall proves requirement 6.6 at the classifier seam:
// when the shared store reports an active lease held by another turn, this turn
// performs no egress, reports the bounded lease-busy reason, and returns the
// current state unchanged.
func TestBusyLeaseMakesNoRemoteCall(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	observer := &recordingObserver{}
	key := sessionclassification.Key{Kind: sessionclassification.ScopeSecureSession, ID: "sess-busy"}
	// Simulate a concurrent turn that holds the lease: claim it directly, and do
	// not complete it.
	claim, _, granted, err := store.ClaimRemote(t.Context(), key, fixedNow()(), 2, 2*time.Second, 0)
	if err != nil || !granted {
		t.Fatalf("seed the concurrent lease: granted=%t err=%v", granted, err)
	}
	if claim.LeaseID == "" {
		t.Fatal("seeded lease has no identifier")
	}

	decider := belowThresholdDecider()
	classifier := observedRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: remoteConfigWith(1, 0),
	}, &fakeAuthority{store: store}, observer, decider)

	got, err := classifier.Classify(t.Context(), ambiguousInput("sess-busy"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != (session.Classification{}) {
		t.Fatalf("classification = %+v, want unknown while another turn holds the lease", got)
	}
	if decider.callCount() != 0 {
		t.Fatalf("remote calls = %d, want none while a lease is active", decider.callCount())
	}
	// Requirement 9.3: the refusal is reported with its own bounded outcome, so a
	// lease-busy turn is distinguishable from a budget-exhausted or deferred one
	// in the metrics surface.
	if remote := observer.remoteObservations(); len(remote) != 1 ||
		remote[0].Outcome != sessionclassification.RemoteLeaseBusy {
		t.Fatalf("remote observations = %+v, want exactly one lease-busy outcome", remote)
	}
}

// TestRefusedClaimReportsItsBoundedReason pins the three refusals a store can
// return, each with its own bounded outcome. They are the diagnostics that
// requirement 9.3 needs to tell a busy session, a spent budget, and a
// completion-anchored backoff apart, and each of them must perform no egress.
func TestRefusedClaimReportsItsBoundedReason(t *testing.T) {
	t.Parallel()

	const sessionID = "sess-refusal-reason"
	key := sessionclassification.Key{Kind: sessionclassification.ScopeSecureSession, ID: sessionID}

	cases := []struct {
		name string
		// seed publishes the shared state that makes the next claim refused.
		seed      func(t *testing.T, store *fakeStore)
		budget    int
		want      sessionclassification.RemoteOutcome
		wantCalls int
	}{
		{
			// Requirement 6.6: an active lease belongs to the turn that claimed it.
			name: "active lease reports lease busy",
			seed: func(t *testing.T, store *fakeStore) {
				t.Helper()
				if _, _, granted, err := store.ClaimRemote(t.Context(), key, fixedNow()(), 1, time.Hour, 0); err != nil || !granted {
					t.Fatalf("seed an active lease: granted=%t err=%v", granted, err)
				}
			},
			budget: 1,
			want:   sessionclassification.RemoteLeaseBusy,
		},
		{
			// Requirement 6.7: a spent per-session budget is reported distinctly so
			// an operator can see the limit rather than a stall.
			name: "spent budget reports budget exhausted",
			seed: func(t *testing.T, store *fakeStore) {
				t.Helper()
				claim, _, granted, err := store.ClaimRemote(t.Context(), key, fixedNow()(), 1, time.Hour, 0)
				if err != nil || !granted {
					t.Fatalf("seed one attempt: granted=%t err=%v", granted, err)
				}
				if _, err := store.CompleteRemote(t.Context(), claim, sessionclassification.RemoteCompletion{}, fixedNow()()); err != nil {
					t.Fatalf("finish the seeded attempt: %v", err)
				}
			},
			budget: 1,
			want:   sessionclassification.RemoteBudgetExhausted,
		},
		{
			// Requirement 6.7: a completion-anchored retry backoff defers the next
			// attempt without spending another one.
			name: "unelapsed backoff reports a skipped attempt",
			seed: func(t *testing.T, store *fakeStore) {
				t.Helper()
				now := fixedNow()()
				claim, _, granted, err := store.ClaimRemote(t.Context(), key, now, 4, time.Hour, time.Hour)
				if err != nil || !granted {
					t.Fatalf("seed one attempt: granted=%t err=%v", granted, err)
				}
				if _, err := store.CompleteRemote(t.Context(), claim, sessionclassification.RemoteCompletion{}, now); err != nil {
					t.Fatalf("finish the seeded attempt: %v", err)
				}
			},
			budget: 4,
			want:   sessionclassification.RemoteSkipped,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeStore()
			observer := &recordingObserver{}
			tc.seed(t, store)
			decider := belowThresholdDecider()
			classifier := observedRemoteClassifier(t, sessionclassification.Config{
				Mode:   sessionclassification.ModeJev,
				Remote: remoteConfigWith(tc.budget, 0),
			}, &fakeAuthority{store: store}, observer, decider)

			got, err := classifier.Classify(t.Context(), ambiguousInput(sessionID))
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if got != (session.Classification{}) {
				t.Fatalf("classification = %+v, want unknown", got)
			}
			if decider.callCount() != tc.wantCalls {
				t.Fatalf("remote calls = %d, want %d for a refused claim", decider.callCount(), tc.wantCalls)
			}
			if remote := observer.remoteObservations(); len(remote) != 1 || remote[0].Outcome != tc.want {
				t.Fatalf("remote observations = %+v, want exactly one %q outcome", remote, tc.want)
			}
			// A refused claim must not consume an attempt beyond the seeded ones.
			record, ok := store.stored(key)
			if !ok {
				t.Fatal("the seeded record disappeared")
			}
			if record.RemoteAttempts != uint32(1) {
				t.Fatalf("persisted attempts = %d, want the single seeded attempt", record.RemoteAttempts)
			}
		})
	}
}

// staleLeaseStore completes every remote attempt as if the lease had already been
// taken over by another turn. It is the shape requirement 6.11 makes possible: a
// process died holding the lease, its TTL expired, and a later turn reclaimed it
// while the original call was still in flight.
type staleLeaseStore struct {
	*fakeStore
	winner sessionclassification.Record
}

func (s *staleLeaseStore) CompleteRemote(
	_ context.Context,
	_ sessionclassification.RemoteClaim,
	_ sessionclassification.RemoteCompletion,
	_ time.Time,
) (sessionclassification.Record, error) {
	return s.winner, sessionclassification.ErrStaleRemoteClaim
}

// TestStaleCompletionNeverClaimsATransition pins requirements 9.1 and 12.5 at the
// seam a concurrent promotion creates. A completion whose lease has been taken
// over did not establish the positive, so this turn must project the winner's
// value, record no transition, and claim no remote work of its own.
func TestStaleCompletionNeverClaimsATransition(t *testing.T) {
	t.Parallel()

	winner := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalTooling,
		Confidence: session.ConfidenceHigh,
		Evidence:   "tooling.distinct_coding_cluster",
		Revision:   1,
	}
	store := &staleLeaseStore{
		fakeStore: newFakeStore(),
		winner:    sessionclassification.Record{Key: sessionclassification.Key{Kind: sessionclassification.ScopeSecureSession, ID: "sess-stale"}, Classification: winner},
	}
	observer := &recordingObserver{}
	decider := &countingDecider{fallback: scriptedAnswer{
		decision: sessionclassification.RemoteDecision{CodingProbability: 0.99},
	}}
	classifier := observedRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: remoteConfigWith(1, 0),
	}, &fakeAuthority{store: store}, observer, decider)

	got, err := classifier.Classify(t.Context(), ambiguousInput("sess-stale"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	// Requirement 1.4: the established positive is projected even though this
	// turn's own completion was stale.
	if got != winner {
		t.Fatalf("classification = %+v, want the winner's positive %+v", got, winner)
	}
	evaluations, transitions := observer.snapshot()
	if len(transitions) != 0 {
		t.Fatalf("transitions = %+v, want none: a stale completion established nothing", transitions)
	}
	if len(evaluations) != 1 || evaluations[0].Outcome != sessionclassification.EvaluationRestored {
		t.Fatalf("evaluations = %+v, want exactly one restored outcome", evaluations)
	}
	// Requirement 9.3: the attempt itself is still reported, because it happened.
	if remote := observer.remoteObservations(); len(remote) != 1 {
		t.Fatalf("remote observations = %+v, want exactly one attempted", remote)
	}
}

// TestConcurrentPromotionSeenByARefusedClaim pins the other concurrent shape: the
// promotion lands between this turn's authoritative read and its claim, so the
// claim is refused and the claim response already carries the positive. The turn
// must project it without egress and without claiming the transition.
func TestConcurrentPromotionSeenByARefusedClaim(t *testing.T) {
	t.Parallel()

	const sessionID = "sess-refused-claim-positive"
	key := sessionclassification.Key{Kind: sessionclassification.ScopeSecureSession, ID: sessionID}
	winner := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceRemote,
		Confidence: session.ConfidenceHigh,
		Evidence:   sessionclassification.EvidenceCodeRemoteAboveThreshold,
		Revision:   1,
	}
	// A promotedPromotionStore reports the session unknown on Load and carries the
	// winner's positive on the refused claim, which is exactly what a real store
	// returns when the promotion commits between the two calls.
	store := &promotedPromotionStore{fakeStore: newFakeStore(), winner: sessionclassification.Record{Key: key, Classification: winner}}
	observer := &recordingObserver{}
	decider := belowThresholdDecider()
	classifier := observedRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: remoteConfigWith(1, 0),
	}, &fakeAuthority{store: store}, observer, decider)

	got, err := classifier.Classify(t.Context(), ambiguousInput(sessionID))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != winner {
		t.Fatalf("classification = %+v, want the concurrent winner's positive %+v", got, winner)
	}
	if decider.callCount() != 0 {
		t.Fatalf("remote calls = %d, want none after a refused claim", decider.callCount())
	}
	evaluations, transitions := observer.snapshot()
	if len(transitions) != 0 {
		t.Fatalf("transitions = %+v, want none: the winner owns the transition", transitions)
	}
	if len(evaluations) != 1 || evaluations[0].Outcome != sessionclassification.EvaluationRestored {
		t.Fatalf("evaluations = %+v, want exactly one restored outcome", evaluations)
	}
}

type promotedPromotionStore struct {
	*fakeStore
	winner sessionclassification.Record
}

func (s *promotedPromotionStore) Load(context.Context, sessionclassification.Key) (sessionclassification.Record, bool, error) {
	// The authoritative read has not observed the promotion yet.
	return sessionclassification.Record{}, false, nil
}

func (s *promotedPromotionStore) ClaimRemote(
	_ context.Context,
	key sessionclassification.Key,
	_ time.Time,
	_ uint32,
	_ time.Duration,
	_ time.Duration,
) (sessionclassification.RemoteClaim, sessionclassification.Record, bool, error) {
	return sessionclassification.RemoteClaim{}, s.winner, false, nil
}

// TestCancelDuringRetryBackoffReturnsPromptly pins requirement 6.9's cancellation
// half for the retry path: a caller that ends while the turn waits out its
// configured backoff must return promptly with the cancellation rather than
// sleeping out the delay or spending another attempt.
func TestCancelDuringRetryBackoffReturnsPromptly(t *testing.T) {
	t.Parallel()

	const backoff = 30 * time.Second
	store := newFakeStore()
	ctx, cancel := context.WithCancel(t.Context())
	entered := make(chan struct{}, 1)
	// The decider fails retryably and signals that it ran, then the turn enters
	// its backoff wait.
	decider := &countingDecider{
		fallback: scriptedAnswer{
			err: boundedFailure{outcome: sessionclassification.RemoteRateLimited, cause: errors.New("scripted")},
		},
		entered: entered,
	}
	classifier := mustRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: remoteConfigWith(5, backoff),
	}, store, realNow(), decider)

	result := make(chan error, 1)
	go func() {
		_, err := classifier.Classify(ctx, ambiguousInput("sess-cancel-backoff"))
		result <- err
	}()

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first remote attempt never ran")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Classify error = %v, want the caller's cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a canceled caller was not released from the retry backoff")
	}
	// Requirement 6.7: the cancellation must have ended the turn at the backoff
	// wait. A wait that did not observe the context would have asked the store for
	// another lease and spent another attempt.
	if attempts := store.claimAttempts(); attempts != 1 {
		t.Fatalf("lease claims = %d, want only the one attempt that already ran", attempts)
	}
	record, ok := store.stored(sessionclassification.Key{
		Kind: sessionclassification.ScopeSecureSession, ID: "sess-cancel-backoff",
	})
	if !ok {
		t.Fatal("the failed attempt persisted no record")
	}
	if record.RemoteAttempts != 1 {
		t.Fatalf("persisted attempts = %d, want only the attempt that already ran", record.RemoteAttempts)
	}
}

// TestCanceledNonRetryableRemoteTurnReportsCancellation pins the guard that
// separates an abandoned request from a completed failure.
//
// A non-retryable remote failure would otherwise end the turn with a clean nil
// error, silently discarding the fact that the caller's context ended mid-call.
// Because the request is already canceled, reporting a bounded remote outcome as
// if the turn finished normally would hide it, so the turn reports the
// cancellation instead (requirements 6.9, 4.5).
func TestCanceledNonRetryableRemoteTurnReportsCancellation(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	entered := make(chan struct{}, 1)
	// The decider parks until the caller's context ends and then reports a
	// determined, non-retryable failure, which is the shape that would otherwise
	// finish the turn with a nil error.
	decider := &countingDecider{
		fallback: scriptedAnswer{
			err: boundedFailure{outcome: sessionclassification.RemoteMalformed, cause: errors.New("scripted")},
		},
		entered:        entered,
		blockOnContext: true,
		malformedOnEnd: true,
	}
	classifier := mustRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: remoteConfigWith(2, 0),
	}, store, fixedNow(), decider)

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := classifier.Classify(ctx, ambiguousInput("sess-canceled-nonderministic"))
		result <- err
	}()

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the remote call never started")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Classify error = %v, want the caller's cancellation rather than a clean nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a canceled caller was not released from the remote phase")
	}
}

// TestRemoteFailureFallsBackWhenTheAdapterOutcomeIsUnbounded pins the guard in
// remoteFailureOutcome: an adapter that reports a value outside the closed
// vocabulary cannot become a label or a retry decision, so the failure is
// reported as the one member that makes no stronger claim (requirements 9.4, 6.7).
func TestRemoteFailureFallsBackWhenTheAdapterOutcomeIsUnbounded(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	observer := &recordingObserver{}
	decider := &countingDecider{fallback: scriptedAnswer{
		err: boundedFailure{
			outcome: sessionclassification.RemoteOutcome("vendor_specific_detail"),
			cause:   errors.New("scripted"),
		},
	}}
	classifier := observedRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: remoteConfigWith(1, 0),
	}, &fakeAuthority{store: store}, observer, decider)

	if _, err := classifier.Classify(t.Context(), ambiguousInput("sess-unbounded-outcome")); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if remote := observer.remoteObservations(); len(remote) != 1 ||
		remote[0].Outcome != sessionclassification.RemoteNetworkError {
		t.Fatalf("remote observations = %+v, want the bounded network fallback", remote)
	}
}

// TestConcurrentAmbiguousTurnsShareOneLease pins requirements 6.6 and 12.5: many
// concurrent ambiguous turns of one logical session produce exactly one active
// lease, exactly one remote call, no duplicated egress, and every turn completes
// without an error.
//
// The decider is parked mid-flight, so the other turns genuinely run while the
// lease is held rather than after it is released.
func TestConcurrentAmbiguousTurnsShareOneLease(t *testing.T) {
	t.Parallel()

	const turns = 16
	store := newFakeStore()
	entered := make(chan struct{}, turns)
	release := make(chan struct{})
	decider := &countingDecider{
		fallback: scriptedAnswer{decision: sessionclassification.RemoteDecision{CodingProbability: 0.99}},
		entered:  entered,
		release:  release,
	}
	classifier := mustRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: remoteConfigWith(1, 0),
	}, store, fixedNow(), decider)

	start := make(chan struct{})
	results := make(chan session.Classification, turns)
	errs := make(chan error, turns)
	var wg sync.WaitGroup
	for range turns {
		wg.Go(func() {
			<-start
			got, err := classifier.Classify(context.Background(), ambiguousInput("sess-concurrent"))
			if err != nil {
				errs <- err
				return
			}
			results <- got
		})
	}
	close(start)

	// Wait for the single in-flight call, then let every other turn reach the
	// store while the lease is still held.
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no remote attempt entered the decider")
	}
	// Every other turn has now observed the active lease; releasing the single
	// attempt lets all of them finish.
	close(release)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Errorf("concurrent turn failed: %v", err)
	}
	// Requirement 12.5: every turn that observed the winner's positive projects
	// the same coherent snapshot. A turn that lost the lease race before the
	// promotion existed legitimately observed the current unknown state, so the
	// invariant is coherence rather than unanimity.
	positives := make(map[session.Classification]int)
	for got := range results {
		if got.IsCodingAgent() {
			positives[got]++
		}
	}
	if len(positives) > 1 {
		t.Fatalf("concurrent turns observed %d distinct positives, want exactly one coherent value", len(positives))
	}
	if len(positives) != 1 {
		t.Fatalf("promoted turns = %d, want at least one turn to establish the positive", len(positives))
	}
	for got, count := range positives {
		if got.Revision != 1 || got.Source != session.SourceRemote {
			t.Fatalf("concurrent positive = %+v seen by %d turns, want the remote positive at revision 1", got, count)
		}
	}
	// Requirement 6.6: one lease, one call, no duplicate egress.
	if decider.callCount() != 1 {
		t.Fatalf("remote calls = %d, want exactly one for concurrent turns", decider.callCount())
	}
	granted, completions, maxActive := store.leaseCounts()
	if granted != 1 {
		t.Fatalf("granted leases = %d, want exactly one", granted)
	}
	if completions != 1 {
		t.Fatalf("completions = %d, want exactly one", completions)
	}
	if maxActive != 1 {
		t.Fatalf("simultaneously active leases = %d, want exactly one", maxActive)
	}
	// Requirement 12.5: one coherent revision survives the race.
	record, ok := store.stored(sessionclassification.Key{
		Kind: sessionclassification.ScopeSecureSession, ID: "sess-concurrent",
	})
	if !ok || !record.Classification.IsCodingAgent() || record.Classification.Revision != 1 {
		t.Fatalf("stored record = %+v found=%t, want one positive at revision 1", record, ok)
	}
}

// TestRemoteCallHoldsNoStoreLockAcrossNetwork is the proof for requirements 6.7
// and 10.4: while a decider is parked mid-flight, an unrelated durable operation
// on a different authority key completes promptly.
//
// A lock held across the network call would block the unrelated operation, so the
// deadline below is the load-bearing instrument rather than a sleep.
func TestRemoteCallHoldsNoStoreLockAcrossNetwork(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	decider := &countingDecider{
		fallback: scriptedAnswer{decision: sessionclassification.RemoteDecision{CodingProbability: 0.99}},
		entered:  entered,
		release:  release,
	}
	classifier := mustRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: remoteConfigWith(1, 0),
	}, store, fixedNow(), decider)

	classified := make(chan error, 1)
	go func() {
		_, err := classifier.Classify(context.Background(), ambiguousInput("sess-locked-turn"))
		classified <- err
	}()

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the remote call never started")
	}

	// The lease for the in-flight turn is held; an unrelated key must still be
	// serviceable while the decider is parked.
	unrelated := sessionclassification.Key{Kind: sessionclassification.ScopeSecureSession, ID: "sess-unrelated"}
	unrelatedDone := make(chan error, 1)
	go func() {
		record, promoted, err := store.Promote(context.Background(), unrelated, session.Classification{
			Kind:       session.KindCodingAgent,
			Source:     session.SourceLocalTooling,
			Confidence: session.ConfidenceHigh,
			Evidence:   "tooling.distinct_coding_cluster",
			Revision:   1,
		}, fixedNow()())
		if err == nil && !promoted {
			err = errors.New("unrelated promotion was not applied")
		}
		if err == nil && !record.Classification.IsCodingAgent() {
			err = errors.New("unrelated promotion did not persist")
		}
		unrelatedDone <- err
	}()

	select {
	case err := <-unrelatedDone:
		if err != nil {
			t.Fatalf("unrelated durable operation during a remote call: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		<-classified
		t.Fatal("an unrelated durable operation blocked behind the in-flight remote call")
	}
	close(release)
	if err := <-classified; err != nil {
		t.Fatalf("classifying turn: %v", err)
	}
}

// TestRemoteRefusesInputOutsideTheBoundedContract proves requirement 7.2's
// runtime half: derived evidence that leaves the closed vocabularies is refused
// before any lease is claimed and before any egress, so a malformed turn cannot
// consume a session's finite attempt budget or reach the network.
func TestRemoteRefusesInputOutsideTheBoundedContract(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	decider := belowThresholdDecider()
	classifier := mustRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: remoteConfigWith(1, 0),
	}, store, fixedNow(), decider)

	in := ambiguousInput("sess-refused-input")
	// An operation outside the closed vocabulary has no representation in the
	// port, so the attempt is refused rather than forwarded.
	in.Evidence.Operation = aipapi.Operation("invented_operation")

	got, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != (session.Classification{}) {
		t.Fatalf("classification = %+v, want unknown for refused evidence", got)
	}
	if decider.callCount() != 0 {
		t.Fatalf("remote calls = %d, want none for refused evidence", decider.callCount())
	}
	if granted, completions, _ := store.leaseCounts(); granted != 0 || completions != 0 {
		t.Fatalf("refused evidence consumed %d leases and %d completions, want none", granted, completions)
	}
}

// TestRemoteInputCarriesOnlyDerivedBoundedFacts proves requirement 7.2 for the
// integration itself: what reaches the port is the derived, content-free input,
// never the raw User-Agent, the workspace path, or a tool name.
// TestRemoteInputCarriesTheDecisiveLocalEvidence pins the production wiring at
// remote_flow.go's BuildRemoteInput(in, EvaluateLocal(c.cfg, in)). The builder is
// unit-tested in remote_test.go, so without this integration assertion the call
// could be handed a zero LocalDecision and no test would fail: a jev turn's
// decisive local evidence would silently stop crossing the port, and the decider
// would be asked to decide from an incomplete picture.
func TestRemoteInputCarriesTheDecisiveLocalEvidence(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	decider := belowThresholdDecider()
	classifier := observedRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: validTestRemoteConfig(),
	}, &fakeAuthority{store: store}, &recordingObserver{}, decider)

	in := decisiveLocalInput("sess-local-evidence")
	if _, err := classifier.Classify(t.Context(), in); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	observed := decider.observedInputs()
	if len(observed) != 1 {
		t.Fatalf("observed remote inputs = %d, want exactly one", len(observed))
	}
	got := observed[0]
	if got.LocalEvidenceCode == "" {
		t.Fatal("the decider received no local evidence code; the local decision " +
			"was not carried across the port even though the local evaluation is decisive")
	}
	if err := sessionclassification.ValidateRemoteInput(got); err != nil {
		t.Fatalf("the carried input is outside the bounded contract: %v", err)
	}
	// The operation must cross too, so a decider can key its judgement.
	if got.Operation != aipapi.OperationOpenAIResponses {
		t.Fatalf("operation = %v, want the turn's canonical operation", got.Operation)
	}
}

func TestRemoteInputCarriesOnlyDerivedBoundedFacts(t *testing.T) {
	t.Parallel()

	const (
		sessionID = "sess-secret-4f2c19ab"
		userAgent = "openai-python/1.40.0-secret-token"
	)
	store := newFakeStore()
	observer := &recordingObserver{}
	decider := belowThresholdDecider()
	classifier := observedRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: validTestRemoteConfig(),
	}, &fakeAuthority{store: store}, observer, decider)

	in := sdkclassification.Input{
		Session:  session.SessionView{AuthoritativeSessionID: sessionID, ClientSessionHint: "client-hint"},
		Evidence: sdkclassification.Evidence{ClientUserAgent: userAgent, Operation: aipapi.OperationOpenAIResponses},
		Workspace: workspace.WorkspaceView{
			Markers:     []string{"go.mod"},
			ProjectRoot: "/home/user/private/repository",
		},
	}
	if _, err := classifier.Classify(t.Context(), in); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	observed := decider.observedInputs()
	if len(observed) != 1 {
		t.Fatalf("observed remote inputs = %d, want exactly one", len(observed))
	}
	if err := sessionclassification.ValidateRemoteInput(observed[0]); err != nil {
		t.Fatalf("the integration sent an input outside the bounded contract: %v", err)
	}
	// A generic SDK identity is ambiguous evidence, not a recognized family.
	if observed[0].ClientFamily != "" || !observed[0].HasAmbiguousClient {
		t.Fatalf("remote client identity = %+v, want an ambiguous generic identity", observed[0])
	}
	if observed[0].WorkspaceClass != sessionclassification.WorkspaceClassProjectMarker {
		t.Fatalf("remote workspace class = %q, want the bounded project-marker class", observed[0].WorkspaceClass)
	}
	if observed[0].LocalEvidenceCode != "" {
		t.Fatalf("remote local evidence = %q, want none for a non-decisive local evaluation", observed[0].LocalEvidenceCode)
	}
	// Requirement 7.2: the workspace root and the raw identity never reach the
	// port. The bounded class is the only workspace fact it carries.
	if observed[0].Operation != aipapi.OperationOpenAIResponses {
		t.Fatalf("remote operation = %q, want the bounded recorded operation", observed[0].Operation)
	}
}

// TestCanceledRemoteTurnLeavesSessionUnknown pins the cancellation half of
// requirement 6.9: when the caller's context ends during the remote call, the turn
// returns unknown with the cancellation error rather than a decision, and the
// lease is left to expire instead of being completed with a canceled context.
func TestCanceledRemoteTurnLeavesSessionUnknown(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	// The decider blocks until the caller's context ends, which is exactly what a
	// real adapter's in-flight HTTP call does when the request is canceled.
	entered := make(chan struct{}, 1)
	decider := &countingDecider{
		fallback:       scriptedAnswer{decision: sessionclassification.RemoteDecision{CodingProbability: 0.99}},
		entered:        entered,
		blockOnContext: true,
	}
	classifier := mustRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: remoteConfigWith(2, 0),
	}, store, fixedNow(), decider)

	ctx, cancel := context.WithCancel(t.Context())
	canceled := make(chan error, 1)
	go func() {
		_, err := classifier.Classify(ctx, ambiguousInput("sess-canceled"))
		canceled <- err
	}()

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the remote call never started")
	}
	cancel()
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Classify error = %v, want the caller's cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a canceled remote turn did not return")
	}
	// The abandoned lease is recoverable after its TTL (requirement 6.11) and no
	// completion was recorded for it.
	record, ok := store.stored(sessionclassification.Key{
		Kind: sessionclassification.ScopeSecureSession, ID: "sess-canceled",
	})
	if !ok {
		t.Fatal("the abandoned lease persisted no record")
	}
	if record.RemoteLeaseID == "" {
		t.Fatal("the in-flight lease was not published before the call")
	}
}

// TestClassifyRequiresADeciderForEveryRemoteMode pins requirements 6.1, 6.2, and
// 6.10 at construction: a remote-capable mode without a decider and a heuristic
// mode with one are both refused, so neither a partially configured remote mode
// nor a network-capable heuristic generation can be published at all.
func TestClassifyRequiresADeciderForEveryRemoteMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		cfg     sessionclassification.Config
		decider sessionclassification.RemoteDecider
		wantErr bool
	}{
		{
			name: "jev without a decider",
			cfg:  sessionclassification.Config{Mode: sessionclassification.ModeJev, Remote: validTestRemoteConfig()},
			// Requirement 6.10: a remote mode without its decider fails the
			// candidate rather than degrading to local-only classification.
			wantErr: true,
		},
		{
			name:    "hybrid without a decider",
			cfg:     sessionclassification.Config{Mode: sessionclassification.ModeHybrid, Remote: validTestRemoteConfig()},
			wantErr: true,
		},
		{
			name:    "heuristic with a decider",
			cfg:     sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
			decider: &countingDecider{},
			// Requirement 6.2: a heuristic generation may never hold a decider.
			wantErr: true,
		},
		{
			name: "heuristic without a decider",
			cfg:  sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
		},
		{
			name:    "jev with a decider",
			cfg:     sessionclassification.Config{Mode: sessionclassification.ModeJev, Remote: validTestRemoteConfig()},
			decider: &countingDecider{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			classifier, err := sessionclassification.NewClassifier(tc.cfg, sessionclassification.ClassifierDeps{
				State:  &fakeAuthority{store: newFakeStore()},
				Remote: tc.decider,
				Now:    fixedNow(),
			})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NewClassifier accepted %+v with a decider %v", tc.cfg, tc.decider != nil)
				}
				if classifier != nil {
					t.Fatal("NewClassifier returned a classifier together with an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewClassifier: %v", err)
			}
			if classifier == nil {
				t.Fatal("NewClassifier returned no classifier")
			}
		})
	}
}

// TestClassifierCopiesRemoteSettingsAtConstruction pins requirement 8.5's
// immutability: an in-place edit of the decoded configuration after construction
// cannot change the timeout, threshold, attempt budget, or lease TTL an
// already-admitted generation enforces.
func TestClassifierCopiesRemoteSettingsAtConstruction(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	remote := validTestRemoteConfig()
	// An unpromotable threshold proves which threshold the generation enforces.
	remote.PositiveThreshold = 0.99
	cfg := sessionclassification.Config{Mode: sessionclassification.ModeJev, Remote: remote}
	decider := &countingDecider{fallback: scriptedAnswer{
		decision: sessionclassification.RemoteDecision{CodingProbability: 0.95},
	}}
	classifier := mustRemoteClassifier(t, cfg, store, fixedNow(), decider)

	// The caller mutates its own decoded configuration after the generation was
	// admitted.
	remote.PositiveThreshold = 0.10
	remote.MaxAttemptsPerSession = 5

	got, err := classifier.Classify(t.Context(), ambiguousInput("sess-immutable"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.IsCodingAgent() {
		t.Fatalf("classification = %+v, want unknown: the copied threshold 0.99 must still decide", got)
	}
	if decider.callCount() != 1 {
		t.Fatalf("remote calls = %d, want exactly the copied single-attempt budget", decider.callCount())
	}
}

// TestRemoteObservationCarriesNoRequestContent pins requirements 7.5 and 9.4 for
// the integration's own observation: the recorded outcome and latency are bounded
// vocabulary values and carry no session identifier, User-Agent, or marker.
func TestRemoteObservationCarriesNoRequestContent(t *testing.T) {
	t.Parallel()

	const (
		sessionID = "sess-observation-secret-7a1c"
		userAgent = "openai-python/1.40.0-secret"
		marker    = "private-repo-go.mod"
	)
	store := newFakeStore()
	observer := &recordingObserver{}
	decider := belowThresholdDecider()
	classifier := observedRemoteClassifier(t, sessionclassification.Config{
		Mode:   sessionclassification.ModeJev,
		Remote: validTestRemoteConfig(),
	}, &fakeAuthority{store: store}, observer, decider)

	in := sdkclassification.Input{
		Session:  session.SessionView{AuthoritativeSessionID: sessionID},
		Evidence: sdkclassification.Evidence{ClientUserAgent: userAgent, Operation: aipapi.OperationOpenAIResponses},
		Workspace: workspace.WorkspaceView{
			Markers:     []string{marker},
			ProjectRoot: "/home/user/private",
		},
	}
	if _, err := classifier.Classify(t.Context(), in); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	for _, observation := range observer.remoteObservations() {
		if !sessionclassification.ValidRemoteObservation(observation) {
			t.Fatalf("remote observation %+v is outside the bounded vocabulary", observation)
		}
		text := fmt.Sprintf("%+v", observation)
		for _, secret := range []string{sessionID, userAgent, marker, "private"} {
			if strings.Contains(text, secret) {
				t.Fatalf("remote observation %q echoed bounded-secret %q", text, secret)
			}
		}
	}
}

// remoteObservationSnapshot returns the recorded remote observations under the
// observer's lock.
func (r *recordingObserver) remoteObservations() []sessionclassification.RemoteObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sessionclassification.RemoteObservation(nil), r.remotes...)
}
